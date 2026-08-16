package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/IamAyang233/panda-xiangqi/internal/engine"
	"github.com/IamAyang233/panda-xiangqi/internal/game"
)

const systemPrompt = `你是一位中国象棋高手。棋盘坐标规则：列 a~i 从红方左侧数起，行 0~9 从红方底线数起（红方在下方）。
你必须只输出一个 JSON 对象，格式：{"from":"<起点坐标>","to":"<终点坐标>","comment":"不超过20字的着法解说"}
例如 {"from":"h2","to":"e2","comment":"中炮开局，直指中路"}。不要输出任何其他内容。`

// Result 一步大模型决策的结果。
type Result struct {
	Move     game.Move
	Comment  string // 棋评（可能为空）
	Fallback bool   // true = 本步由本地引擎代走
	Attempts int    // 实际尝试次数
}

// Player 大模型棋手：解析 → 重试 → 降级链（A11）。
// 一个 Player 对应一局会话：内部 http.Client 连接池复用（keep-alive），避免每步重建 TCP。
type Player struct {
	cfg      Config
	client   *Client
	fallback *engine.SimpleEngine
}

// NewPlayer 创建大模型棋手。
func NewPlayer(cfg Config) *Player {
	return &Player{
		cfg:      cfg,
		client:   NewClient(cfg),
		fallback: engine.NewSimpleEngine(),
	}
}

// BestMove 主流程。candidates 非空时启用"引擎候选"模式：
// 模型只从引擎排序的候选中挑选并解说——提示词更短（更快）、着法更强、
// 解析失败时直接取候选首位，不 再二次搜索。
func (p *Player) BestMove(ctx context.Context, pos *game.Position, candidates []game.Move) (Result, error) {
	legal := pos.LegalMoves(pos.Turn)
	if len(legal) == 0 {
		return Result{}, fmt.Errorf("无合法着法")
	}
	assist := len(candidates) > 0
	if !assist {
		candidates = legal
	}
	if err := p.cfg.Validate(); err != nil {
		return p.degrade(ctx, pos, candidates, 0), err
	}

	legalUci := make([]string, 0, len(legal))
	for _, m := range legal {
		legalUci = append(legalUci, m.String())
	}
	candUci := make([]string, 0, len(candidates))
	for _, m := range candidates {
		candUci = append(candUci, m.String())
	}

	messages := []chatMessage{{Role: "system", Content: systemPrompt}}
	messages = append(messages, chatMessage{Role: "user", Content: p.buildPrompt(pos, legalUci, candUci, assist)})

	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		ctx2, cancel := context.WithTimeout(ctx, p.cfg.timeout())
		resp, err := p.client.Chat(ctx2, messages)
		cancel()
		if err != nil {
			lastErr = err
			break // 网络/超时错误 → 直接降级
		}
		mv, comment, ok := parseMove(resp, pos, candidates)
		if ok {
			return Result{Move: mv, Comment: comment, Attempts: attempt}, nil
		}
		// 非法着法：携带反馈重试（A11 第 9 行）
		lastErr = fmt.Errorf("输出未能解析为合法着法: %.120s", strings.TrimSpace(resp))
		messages = append(messages,
			chatMessage{Role: "assistant", Content: resp},
			chatMessage{Role: "user", Content: fmt.Sprintf(
				"上一手输出非法（%s）。%s：%s。请重新只输出 JSON。",
				strings.TrimSpace(resp), feedbackLabel(assist), strings.Join(candUci, ", "))})
	}
	res := p.degrade(ctx, pos, candidates, 3)
	res.Comment = fmt.Sprintf("本地引擎代走（%v）", lastErr)
	return res, lastErr
}

func feedbackLabel(assist bool) string {
	if assist {
		return "允许的候选着法"
	}
	return "合法着法坐标表"
}

// degrade 降级：引擎候选模式下直接取候选首位（零延迟），否则自研引擎低档代走。
func (p *Player) degrade(ctx context.Context, pos *game.Position, candidates []game.Move, attempts int) Result {
	if len(candidates) > 0 {
		return Result{Move: candidates[0], Fallback: true, Attempts: attempts}
	}
	mv, err := p.fallback.BestMove(ctx, pos, 2)
	if err != nil {
		legal := pos.LegalMoves(pos.Turn)
		if len(legal) == 0 {
			return Result{Attempts: attempts}
		}
		return Result{Move: legal[0], Fallback: true, Attempts: attempts}
	}
	return Result{Move: mv, Fallback: true, Attempts: attempts}
}

// buildPrompt 构造用户提示词（§4.3）。历史仅保留最近 12 手，控制 token 加速响应。
func (p *Player) buildPrompt(pos *game.Position, legalUci, candUci []string, assist bool) string {
	side := "红方"
	if pos.Turn == game.Black {
		side = "黑方"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "当前局面 FEN：%s，轮到%s行棋。\n", pos.FEN(), side)
	if hist := pos.HistoryMoves(); len(hist) > 0 {
		tail := hist
		if len(tail) > 12 {
			tail = tail[len(tail)-12:]
		}
		ucis := make([]string, 0, len(tail))
		for _, m := range tail {
			ucis = append(ucis, m.String())
		}
		fmt.Fprintf(&b, "最近着法（UCI）：%s\n", strings.Join(ucis, " "))
	}
	if assist {
		fmt.Fprintf(&b, "引擎推荐候选着法（由强到弱）：%s\n", strings.Join(candUci, ", "))
		b.WriteString("请从候选中选出你认为最有利于进攻与防守的一手，并给出简短解说。")
		return b.String()
	}
	if p.cfg.IncludeLegal {
		fmt.Fprintf(&b, "可选合法着法：%s\n", strings.Join(legalUci, ", "))
	}
	b.WriteString("请给出你的最佳着法。")
	return b.String()
}

// ---------------------------------------------------------------- 解析（A13）

var jsonRe = regexp.MustCompile(`\{[^{}]*\}`)

// parseMove 解析模型输出：严格 JSON → 宽松 JSON 提取 → 中文着法生成-匹配。
// pool 为允许的着法集合（引擎候选或全部合法着法）。
func parseMove(resp string, pos *game.Position, pool []game.Move) (game.Move, string, bool) {
	resp = strings.TrimSpace(resp)

	// 兼容各家模型常见的字段名：from/to、move、best_move、bestmove、uci
	type moveJSON struct {
		From      string `json:"from"`
		To        string `json:"to"`
		Move      string `json:"move"`
		BestMove  string `json:"best_move"`
		BestMove2 string `json:"bestmove"`
		Uci       string `json:"uci"`
		Comment   string `json:"comment"`
		Thought   string `json:"thought"`
	}
	tryJSON := func(s string) (game.Move, string, bool) {
		var mj moveJSON
		if err := json.Unmarshal([]byte(s), &mj); err != nil {
			return game.Move{}, "", false
		}
		whole := firstNonEmpty(mj.Move, mj.BestMove, mj.BestMove2, mj.Uci)
		if mv, ok := matchUCI(mj.From, mj.To, whole, pool); ok {
			comment := firstNonEmpty(mj.Comment, mj.Thought)
			return mv, strings.TrimSpace(comment), true
		}
		return game.Move{}, "", false
	}

	// 1) 整体即 JSON
	if mv, c, ok := tryJSON(resp); ok {
		return mv, c, true
	}
	// 2) 从文本中提取 JSON 对象
	for _, frag := range jsonRe.FindAllString(resp, -1) {
		if mv, c, ok := tryJSON(frag); ok {
			return mv, c, true
		}
	}
	// 3) UCI 着法串（如 "h2e2"）直接匹配
	if m, ok := game.MoveFromUCI(resp); ok {
		for _, l := range pool {
			if l == m {
				return l, "", true
			}
		}
	}
	for _, tok := range strings.Fields(resp) {
		if m, ok := game.MoveFromUCI(strings.Trim(tok, "，。,.、")); ok {
			for _, l := range pool {
				if l == m {
					return l, "", true
				}
			}
		}
	}
	// 4) 中文着法生成-匹配（A13）
	norm := game.NormalizeCN(resp)
	if norm != "" {
		for _, l := range pool {
			cn := game.NormalizeCN(pos.MoveToChinese(l))
			if cn != "" && strings.Contains(norm, cn) {
				return l, "", true
			}
		}
	}
	return game.Move{}, "", false
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// matchUCI 把 JSON 中的坐标字段对着合法表匹配。
func matchUCI(from, to, whole string, pool []game.Move) (game.Move, bool) {
	if whole != "" {
		if m, ok := game.MoveFromUCI(whole); ok {
			for _, l := range pool {
				if l == m {
					return l, true
				}
			}
		}
	}
	if from != "" && to != "" {
		if m, ok := game.MoveFromUCI(from + to); ok {
			for _, l := range pool {
				if l == m {
					return l, true
				}
			}
		}
	}
	return game.Move{}, false
}
