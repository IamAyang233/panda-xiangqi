// Package review 复盘：关键手筛选与「问 AI」讲解。
//
// 分成两件事，因为它们的花费差三个数量级：
//   - 关键手筛选（Analyze）：纯本地，规则层零成本 + 引擎浅搜（每手几十毫秒），
//     一局几秒钟就能算完，结果落盘缓存；
//   - AI 讲解（Explain）：一次调用 45~90 秒（重推理模型实测），所以**只在用户
//     点某一手时按需触发**，讲过的写回棋谱，下次直接看。
package review

import (
	"context"
	"fmt"
	"time"

	"github.com/IamAyang233/panda-xiangqi/internal/engine"
	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/llm"
	"github.com/IamAyang233/panda-xiangqi/internal/record"
)

const (
	// perMoveBudget 每手浅搜的时间预算。现在每手只搜**一次**（拿根节点分值），
	// 一局 40 手约 2 秒 —— 再高会让打开详情变慢，也会和进行中的对局抢引擎。
	perMoveBudget = 50 * time.Millisecond
	// maxPlies 分析上限：长局（顶和、连将循环）不该把 CPU 吃光。
	maxPlies = 240
	// 落差阈值（与 Evaluate 同量级，兵约 100）。刻意偏保守：浅搜本身有噪声，
	// 门槛压太低会把正常出子也标成「疑问手」（实测 120 就会误伤「马8进7」），
	// 而误报比漏报更伤口碑 —— 用户会直接不信这个列表。
	blunderDelta = 400 // 丢一个大子级别的亏
	mistakeDelta = 200 // 明显不划算
)

// replayTo 把局面重放到第 upto 手**之前**（upto=0 即起始局面）。
//
// 顺手校验每一手在该局面合法：棋谱是自己写出来的，但目录用户可碰，
// 一条坏数据不该让复盘给出似是而非的结论 —— 直接报错更诚实。
func replayTo(rec *record.Record, upto int) (*game.Position, error) {
	pos, err := game.ParseFEN(rec.StartFEN)
	if err != nil {
		return nil, fmt.Errorf("起始局面无法解析: %w", err)
	}
	n := upto
	if n > len(rec.Moves) {
		n = len(rec.Moves)
	}
	for i := 0; i < n; i++ {
		mv, ok := game.MoveFromUCI(rec.Moves[i].UCI)
		if !ok {
			return nil, fmt.Errorf("第 %d 手的 uci 不合法: %q", i+1, rec.Moves[i].UCI)
		}
		if !isLegal(pos, mv) {
			return nil, fmt.Errorf("第 %d 手 %s 在该局面不合法（棋谱数据有误）", i+1, rec.Moves[i].UCI)
		}
		pos.Make(mv)
	}
	return pos, nil
}

// rootScore 在根节点分值表里找这一手的分值。
// 找不到（被剪枝/该着法非法）时返回 false —— 此时宁可不打失误标签，
// 也不能拿别的数去凑一个分差。
func rootScore(ps engine.PositionScore, uci string) (int, bool) {
	for _, r := range ps.Roots {
		if r.UCI == uci {
			return r.Score, true
		}
	}
	return 0, false
}

func isLegal(pos *game.Position, mv game.Move) bool {
	for _, lm := range pos.LegalMoves(pos.Turn) {
		if lm == mv {
			return true
		}
	}
	return false
}

// Analyze 重放整局并标出关键手（规则层 + 引擎浅搜）。
//
// 标签优先级：终局手（mate）＞ 引擎判定的失误/疑问手 ＞ 将军 ＞ 吃子。
// 一手只给一个标签：界面上一行一个标记才看得清，而「既是吃子又是失误」时
// 更该让人看到的是失误。
func Analyze(ctx context.Context, rec *record.Record, engines *engine.Manager) ([]record.KeyMove, error) {
	if engines == nil {
		return nil, fmt.Errorf("引擎不可用")
	}
	pos, err := replayTo(rec, 0)
	if err != nil {
		return nil, err
	}
	out := []record.KeyMove{}
	for i, mv := range rec.Moves {
		if i >= maxPlies {
			break
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		m, ok := game.MoveFromUCI(mv.UCI)
		if !ok {
			return nil, fmt.Errorf("第 %d 手的 uci 不合法: %q", i+1, mv.UCI)
		}
		// 必须逐手校验：棋盘目录用户可碰，一条坏数据若被静默应用，
		// 后面每一手的「关键手」都会基于一个不存在的局面算出来 —— 似是而非的结论。
		if !isLegal(pos, m) {
			return nil, fmt.Errorf("第 %d 手 %s 在该局面不合法（棋谱数据有误）", i+1, mv.UCI)
		}
		// 这一手之前的局面：最佳手与**根节点各着法的分值**（轮走方视角，同一次搜索）
		before := engines.ScorePosition(ctx, pos, perMoveBudget)
		played, known := rootScore(before, m.String())
		pos.Make(m)
		st := pos.CheckStatus()
		inCheck := st.Result == "" && pos.InCheck(pos.Turn)
		// 分差取自同一次搜索：最佳手分值 − 实际这一手的分值（都是走子前的根节点分）
		delta := before.Score - played

		km := record.KeyMove{Index: i}
		tagged := false
		switch {
		case st.Result != "" && (rec.Reason == game.ReasonCheckmate || rec.Reason == game.ReasonStalemate):
			// 终局那一手：不能再拿引擎分差去标（终局分值是极值，会把它误判成失误）
			km.Tag = record.TagMate
			tagged = true
		case before.Strong && known && delta >= blunderDelta:
			km.Tag, km.Delta, km.BestUCI = record.TagBlunder, delta, before.Best
			tagged = true
		case before.Strong && known && delta >= mistakeDelta:
			km.Tag, km.Delta, km.BestUCI = record.TagMistake, delta, before.Best
			tagged = true
		case inCheck:
			km.Tag = record.TagCheck
			tagged = true
		case mv.Captured != "":
			km.Tag = record.TagCapture
			tagged = true
		}
		if tagged {
			out = append(out, km)
		}
	}
	return out, nil
}

// Explain 为第 index 手生成一段讲解（按需触发；调用方负责把结果写回棋谱）。
func Explain(ctx context.Context, cfg llm.Config, rec *record.Record, index int, key *record.KeyMove) (string, error) {
	if index < 0 || index >= len(rec.Moves) {
		return "", fmt.Errorf("手数超出范围")
	}
	pos, err := replayTo(rec, index)
	if err != nil {
		return "", err
	}
	mv, _ := game.MoveFromUCI(rec.Moves[index].UCI)
	side := "red"
	if pos.Turn == game.Black {
		side = "black"
	}
	// 之前最多 8 手，够模型理解上下文又不至于把提示词撑长
	from := index - 8
	if from < 0 {
		from = 0
	}
	recent := make([]string, 0, index-from)
	for i := from; i < index; i++ {
		recent = append(recent, rec.Moves[i].CN)
	}
	var tags []string
	bestCN := ""
	delta := 0
	if key != nil {
		if key.Tag != "" {
			tags = append(tags, key.Tag)
		}
		delta = key.Delta
		if key.BestUCI != "" {
			if bm, ok := game.MoveFromUCI(key.BestUCI); ok {
				bestCN = pos.MoveToChinese(bm)
			}
		}
	}
	return llm.Explain(ctx, cfg, llm.ExplainRequest{
		FEN:        pos.FEN(),
		SideToMove: side,
		RecentCN:   recent,
		MoveCN:     rec.Moves[index].CN,
		MoveUCI:    mv.String(),
		Tags:       tags,
		BestCN:     bestCN,
		Delta:      delta,
		Result:     rec.Result,
		Reason:     rec.Reason,
	})
}
