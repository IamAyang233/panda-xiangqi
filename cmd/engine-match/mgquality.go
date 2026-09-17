package main

// 中局走子质量：用「深度仲裁」把两套引擎的着法比出高低。
//
// 为什么需要它：残局题曲线已证明我们在残局上不落后（甚至更好），而实战 4 局全负
// 都是被将死/长将 —— 弱项在中局。但中局没有「正解」可对，命中率那套用不了。
//
// 做法：同样预算下各出一招，**再用一个远强于双方的仲裁者给两个结果局面打分**。
//
//	position P（S 行棋）
//	  ├ S 走 M_ours → P_ours，仲裁者搜 P_ours 得 s_ours（从对手视角）
//	  └ S 走 M_pik  → P_pik ，仲裁者搜 P_pik  得 s_pik
//	走子方 S 希望对手的评价越低越好 ⇒ s 越小越好
//	⇒ M_ours 更优 ⟺ s_ours < s_pik，优势 = s_pik − s_ours
//
// ⚠️ 仲裁者只用它**评分**、不用它选着法，所以不是循环论证；但它仍是皮卡鱼，
// 对「皮卡鱼风格的着法」会有偏好 —— 这是本判据已知的偏差方向，读结论时要记住。
//
// ⚠️⚠️ **必须跑对照组**：同一引擎「预算 B vs 预算 B/8」的着法对比。如果仲裁者
// 分辨不出这个真实存在的强弱差，那本判据对「我们 vs 皮卡鱼」的结论也不可信。
// 只有对照组给出显著的正结果，实验组的数字才有意义。
//
// ---------------------------------------------------------------------------
// ⚠️ **实测结论：这个度量不可用（对照组证明它没有分辨力）。**
//
// 对照组是「我们预算 B 的着法 vs 预算 B/8 的着法」——两者相差约 3 层搜索
// （EBF 2.2，8 倍节点 ≈ 2.4 层），是**确定存在的强弱差**。仲裁者应当能分辨。
//
//	仲裁预算 200 万（20×）：不同着的 9 个局面里 B 优 2 / 相当 6 / 差 1，平均 +4.9 分
//	仲裁预算 800 万（80×）：不同着的 9 个局面里 B 优 0 / 相当 8 / 差 1，平均 +0.9 分
//
// **加大仲裁预算并没有提高分辨力，反而让差异更小。** 这不是「仲裁者不够强」，
// 而是「走完一步后的局面评分」这个度量本身分辨不出着法质量：
// 一个少搜 3 层的着法，往往通向一个**评估值几乎相同**的局面 —— 差距要经过
// 整局才显现，不在即时评估里。
//
// ⇒ 实验组（我们 vs 皮卡鱼，14 个不同着，0 优 / 12 相当 / 2 差，平均 −9.5 分）
//   **因此无法解读**，不能当成「我们中局略差」的证据。
//
// **下一步该换度量**：
//   - **着法级**（已实现，见 `agree.go` 的 `-mode agree`）：用远超双方的仲裁预算取
//     「仲裁者的最佳着法」，比各自与它的一致率。
//   - **结果级**：把候选着法强制走一步后让双方续弈，比最终胜负。信号最强，
//     但每局成本高、样本量需求大。
//
// 另一条独立结论：**安静局面语料根本测不出东西** —— 20 个里 17 个两引擎选
// 同一着（见 `-mgdump` 的注释）。用对局采集的紧张局面分歧率是 50%，高 3 倍。
//
// 已冻结一份对局采集的中局语料供复用（10 局 ×150ms 对局中 ply 12~60、
// 双方非兵非将 ≥3 子的局面，去重后 300 条）：
//
//	internal/search/testdata/live_game_fens.txt
//
// 复现命令：
//
//	go build -o em.exe ./cmd/engine-match
//	./em.exe -mode games -games 10 -movetime 150 -maxply 70 -mgdump ./mg_fens.txt \
//	         -uci dist/pikafish.exe -flat engines/pikafish.nnue.flat
//	./em.exe -mode mg -corpus ./mg_fens.txt -mgl 25 -mgbudget 100000 -mgarbiter 8000000 \
//	         -uci dist/pikafish.exe -flat engines/pikafish.nnue.flat
// ---------------------------------------------------------------------------

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/nnue"
	"github.com/IamAyang233/panda-xiangqi/internal/search"
)

// goNodesScore 在给定节点预算下搜索并返回**最后一条 info 的分值**。
//
// 分值视角：UCI 的 score 是「行棋方视角」，也就是走完这步之后轮到的那一方。
// 调用方要自己取负才是走子方的收益。
func (s *uciSession) goNodesScore(fen string, budget int64, timeout time.Duration) (int, int, error) {
	s.send("ucinewgame")
	s.send("position fen " + fen)
	s.send(fmt.Sprintf("go nodes %d", budget))

	deadline := time.Now().Add(timeout)
	score, depth := 0, 0
	for time.Now().Before(deadline) {
		type lineRes struct {
			line string
			err  error
		}
		ch := make(chan lineRes, 1)
		go func() {
			l, rerr := s.rd.ReadString('\n')
			ch <- lineRes{l, rerr}
		}()

		var got lineRes
		select {
		case got = <-ch:
		case <-time.After(time.Until(deadline)):
			return score, depth, fmt.Errorf("nodes %d 超时", budget)
		}
		if got.err != nil {
			if got.err == io.EOF {
				return score, depth, fmt.Errorf("引擎输出流关闭")
			}
			return score, depth, got.err
		}
		line := strings.TrimSpace(got.line)
		if strings.HasPrefix(line, "info ") && strings.Contains(line, " score ") {
			if v, ok := parseUCIScore(line); ok {
				score = v
			}
			if d := fieldInt(line, "depth"); d > depth {
				depth = d
			}
			continue
		}
		if strings.HasPrefix(line, "bestmove") {
			return score, depth, nil
		}
	}
	return score, depth, fmt.Errorf("nodes %d 未收到 bestmove", budget)
}

// parseUCIScore 从 info 行取出分值（cp 直接取值，mate 折算成很大的分）。
func parseUCIScore(line string) (int, bool) {
	f := strings.Fields(line)
	for i := 0; i+2 < len(f); i++ {
		if f[i] != "score" {
			continue
		}
		switch f[i+1] {
		case "cp":
			if v, err := strconv.Atoi(f[i+2]); err == nil {
				return v, true
			}
		case "mate":
			n, err := strconv.Atoi(f[i+2])
			if err != nil {
				return 0, false
			}
			v := 30000 - absInt(n)
			if n < 0 {
				v = -v
			}
			return v, true
		}
	}
	return 0, false
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// scoreAfterMove 走完一步后用仲裁者给结果局面打分（从走子方的**对手**视角）。
func scoreAfterMove(sess *uciSession, fen, uciMove string, budget int64) (int, error) {
	pos, err := game.ParseFEN(fen)
	if err != nil {
		return 0, err
	}
	m, ok := game.MoveFromUCI(uciMove)
	if !ok {
		return 0, fmt.Errorf("着法 %q 无法解析", uciMove)
	}
	if !pos.IsLegal(m) {
		return 0, fmt.Errorf("着法 %q 在 %s 上不合法", uciMove, fen)
	}
	pos.Make(m)
	s, _, err := sess.goNodesScore(pos.FEN(), budget, 300*time.Second)
	return s, err
}

// verdictStat 累计一组对比的胜负。
//
// ⚠️ `sameMove` 是必须单独统计的：两侧选的**根本是同一着**时，比分差恒为 0，
// 把它算进「相当」会把真实的分辨力稀释掉。第一次跑就是因为没分这个，对照组
// 8 个局面全「相当」，看起来像仲裁者不敏感，其实是 8 个局面里 7 个同一着。
type verdictStat struct {
	better, equal, worse int
	diffSum, diffSumDiff int // 后者只统计「着法不同」的子集
	diffN                int
	maxDiff              int
	sameMove             int
}

func (v *verdictStat) add(mine, theirs int, movesDiffer bool) {
	d := theirs - mine // 正 = 我们的着法更好
	v.diffSum += d
	if absInt(d) > v.maxDiff {
		v.maxDiff = absInt(d)
	}
	if !movesDiffer {
		v.sameMove++
		return
	}
	v.diffSumDiff += d
	v.diffN++
	switch {
	case d > 20:
		v.better++
	case d < -20:
		v.worse++
	default:
		v.equal++
	}
}

func (v verdictStat) report(label string, n int) {
	fmt.Printf("%s\n", label)
	fmt.Printf("  着法与对方相同 %d/%d（比分为 0，无分辨力）\n", v.sameMove, n)
	fmt.Printf("  只看不同着的 %d 个局面：我们更优 %d｜相当 %d｜更差 %d\n",
		v.diffN, v.better, v.equal, v.worse)
	if v.diffN > 0 {
		fmt.Printf("  平均优势 %+.1f 分（正 = 我们更好），最大 |分差| %d\n",
			float64(v.diffSumDiff)/float64(v.diffN), v.maxDiff)
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// mgQuality 跑中局走子质量对比（含判别力对照组）。
func mgQuality(flatPath, uciPath, corpus string, limit int, compareBudget, arbiterBudget int64) {
	w, err := nnue.Load(flatPath)
	if err != nil {
		fmt.Println("加载 NNUE 权重失败：", err)
		return
	}
	fens, err := readFENFile(corpus)
	if err != nil {
		fmt.Printf("读取语料 %s 失败：%v\n", corpus, err)
		return
	}
	if limit > 0 && len(fens) > limit {
		fens = fens[:limit]
	}
	if len(fens) == 0 {
		fmt.Println("语料为空")
		return
	}

	sess, err := newUCISession(uciPath, 20)
	if err != nil {
		fmt.Println("皮卡鱼会话建立失败：", err)
		return
	}
	defer sess.Close()

	fmt.Printf("=== 中局走子质量（深度仲裁）===\n")
	fmt.Printf("语料 %s｜%d 个局面｜对比预算 %d 节点｜仲裁预算 %d 节点（%.0f×）\n\n",
		corpus, len(fens), compareBudget, arbiterBudget, float64(arbiterBudget)/float64(compareBudget))

	var vsPik, control verdictStat
	skipped := 0
	for i, fen := range fens {
		pos, perr := game.ParseFEN(fen)
		if perr != nil {
			skipped++
			continue
		}
		// 我们：预算 B 的着法；以及 B/8 的着法（对照组用）
		ours := search.New(w).SearchNodes(pos.Clone(), compareBudget).Best.String()
		oursWeak := search.New(w).SearchNodes(pos.Clone(), compareBudget/8).Best.String()
		// 皮卡鱼：预算 B 的着法
		pik, _, _, uerr := sess.goNodesBest(fen, compareBudget, 300*time.Second)
		if uerr != nil {
			skipped++
			continue
		}
		sOurs, e1 := scoreAfterMove(sess, fen, ours, arbiterBudget)
		sPik, e2 := scoreAfterMove(sess, fen, pik, arbiterBudget)
		sWeak, e3 := scoreAfterMove(sess, fen, oursWeak, arbiterBudget)
		if e1 != nil || e2 != nil || e3 != nil {
			skipped++
			continue
		}
		vsPik.add(sOurs, sPik, ours != pik)
		// 对照组：预算 B 的着法 vs 预算 B/8 的着法（同一引擎，真实强弱差已知）
		control.add(sOurs, sWeak, ours != oursWeak)
		if (i+1)%10 == 0 {
			fmt.Printf("  ...已跑 %d/%d\n", i+1, len(fens))
		}
	}
	n := len(fens) - skipped
	fmt.Printf("\n有效局面 %d（跳过 %d）\n", n, skipped)
	vsPik.report("实验组：我们 vs 皮卡鱼", n)
	control.report("对照组：预算B vs 预算B/8", n)
	fmt.Printf("\n⚠️ 只有**对照组**显著偏向「预算 B」时，实验组的数字才可信 ——\n")
	fmt.Printf("   否则说明仲裁者对真实强弱差都不敏感，实验组的结论只是噪声。\n")
}

func readFENFile(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		if s := strings.TrimSpace(line); s != "" && !strings.HasPrefix(s, "#") {
			out = append(out, s)
		}
	}
	return out, nil
}
