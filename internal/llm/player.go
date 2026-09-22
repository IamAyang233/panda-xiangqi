package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

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

	// maxTok 是本次的输出上限，被截断时翻倍再试：推理模型的思考长度随局面变化，
	// 固定额度总会在某些局面上不够（实测同一模型回一个着法可能花 300~3600 个思考 token）。
	maxTok := p.cfg.maxTokens()
	// 上限取 16384：默认已是 8192，再翻一倍给极端局面留余地；再高的话
	// 「截断→加倍→再截断」的代价（与等待时间）会明显变大，
	// 而思考超过 16k 还回不出一个着法已属异常，报清楚原因让用户自己决定更合适。
	const maxTokCap = 16384

	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		ctx2, cancel := context.WithTimeout(ctx, p.cfg.timeout())
		// 本次**实际生效**的上限：配置值与上游 ctx 剩余里更早的那个。
		// 超时提示必须报这个值 —— 只报配置值会误导用户（真凶常是上游）。
		effectiveMs := minMs(p.cfg.timeout().Milliseconds(), remainingMs(ctx))
		reply, err := p.client.ChatWithLimit(ctx2, messages, maxTok)
		cancel()
		if err != nil {
			// 服务端明确要求用 max_completion_tokens（新版 OpenAI 系）：改用新字段名
			// 再试一次。这只在收到那句报错时触发，老服务端不受影响。
			if wantsMaxCompletionTokens(err) {
				p.client.UseMaxCompletionTokens()
				lastErr = err
				continue
			}
			// 限流/5xx 是瞬时的：退避后重试，别直接降级。
			// 原先一律立刻降级，限流密集时整局几乎全由本地引擎代走。
			if he, ok := AsHTTPError(err); ok && he.Retryable() && attempt < 3 {
				lastErr = he
				time.Sleep(time.Duration(attempt) * 800 * time.Millisecond)
				continue
			}
			lastErr = paramHint(withTimeoutHint(err, effectiveMs))
			break // 网络/超时/配置类错误 → 直接降级
		}

		// 被截断：先加倍额度重试，**不要**急着从（没写完的）文本里捞着法。
		//
		// ⚠️ 实测教训：思考被截断时 finish_reason=length、content 为空、reasoning 是
		// 半截思维链。此时 Text() 会回退到 reasoning，parseMove 又能从里面捞出某个
		// 着法 —— 但那往往只是模型**中途考虑过**的一手（思维链天然会枚举并否定多手），
		// 既不是它的结论，也没有棋评。原先在此直接返回，既走了错棋又跳过了重试。
		if reply.Truncated() {
			lastErr = fmt.Errorf("模型输出被截断：max_tokens=%d 不足（思考占用过多，未产出完整正文；"+
				"可在设置中调大「输出上限」）", maxTok)
			if maxTok < maxTokCap {
				maxTok *= 2
				continue
			}
			// 额度已到上限仍不够：如实报错并降级，**不**从半截思考里捞着法 ——
			// 那多半是模型中途考虑过、后来否掉的一手，还会让「降级」这件事被掩盖。
			// 引擎候选本身就是强着，比赌一手更可靠；用户也能据提示调大输出上限。
			break
		}

		// 未被截断：正常解析（正文优先，正文为空时回退思考过程）
		if !reply.Empty() {
			if mv, comment, ok := parseMove(reply.Text(), pos, candidates); ok {
				return Result{Move: mv, Comment: comment, Attempts: attempt}, nil
			}
		}

		// 未被截断但解析不成。按原因分派：
		//   ① 完全空 —— 模型什么都没给。
		//   ② 有文本但解析不出 —— 把原文带进错误里，用户才看得到模型到底回了什么。
		if reply.Empty() {
			lastErr = fmt.Errorf("模型返回空内容（content 与 reasoning 均为空）")
			break
		}
		text := reply.Text()
		lastErr = fmt.Errorf("输出未能解析为合法着法: %.120s", strings.TrimSpace(text))
		messages = append(messages,
			chatMessage{Role: "assistant", Content: text},
			chatMessage{Role: "user", Content: fmt.Sprintf(
				"上一手输出非法（%s）。%s：%s。请重新只输出 JSON。",
				strings.TrimSpace(text), feedbackLabel(assist), strings.Join(candUci, ", "))})
	}
	res := p.degrade(ctx, pos, candidates, 3)
	res.Comment = fmt.Sprintf("本地引擎代走（%v）", lastErr)
	return res, lastErr
}

// withTimeoutHint 给超时类错误补一句可操作的说明。
//
// 推理模型一步棋实测可达 50~90s，而默认/用户设置里常有 30s —— 这时报
// 「请求失败: context deadline exceeded」用户看不懂是模型太慢还是连不上。
//
// ⚠️ 关键：这里报的必须是**实际生效**的上限，而不是配置值。ctx 取消时
// 真正到期的可能是上游（对局侧应着 ctx）而非本包的 timeout；只报配置值会让用户
// 一直调大设置里的超时却毫无效果。所以调用方要把「本次实际剩余时间」传进来。
func withTimeoutHint(err error, effectiveMs int64) error {
	if !isTimeout(err) {
		return err
	}
	if effectiveMs > 0 {
		return fmt.Errorf("请求超时（本次生效上限约 %dms，推理模型单步可能要 1 分钟以上；"+
			"可在设置中把「超时」调大）", effectiveMs)
	}
	return fmt.Errorf("请求超时（推理模型单步可能要 1 分钟以上，可在设置中把「超时」调大）")
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// remainingMs 返回 ctx 的剩余毫秒数（无 deadline 或已过期返回 0）。
//
// 用于把「这次到底被哪个上限卡住」如实报出来。
func remainingMs(ctx context.Context) int64 {
	if dl, ok := ctx.Deadline(); ok {
		ms := time.Until(dl).Milliseconds()
		if ms > 0 {
			return ms
		}
	}
	return 0
}

// minMs 取两个上限里更早的那个；任一为 0（表示未知/无限制）时取另一个。
func minMs(a, b int64) int64 {
	switch {
	case a <= 0:
		return b
	case b <= 0:
		return a
	case a < b:
		return a
	default:
		return b
	}
}

// wantsMaxCompletionTokens 判断服务端是否在要求改用 max_completion_tokens。
//
// 新版 OpenAI（o1/o3/gpt-5）与部分 Azure 部署会明确回一句
// 「This model does not support max_tokens, please use max_completion_tokens」。
// 只认这种明确报错，不做猜测 —— 老服务端不认识新字段名，主动改用反而弄坏。
func wantsMaxCompletionTokens(err error) bool {
	he, ok := AsHTTPError(err)
	if !ok || he.Status != http.StatusBadRequest {
		return false
	}
	return strings.Contains(strings.ToLower(he.Message), "max_completion_tokens")
}

// paramHint 给「参数不被该模型支持」的 400 补一句可操作提示。
//
// 这类错误原文是英文术语（temperature / max_tokens），用户看不出该动设置里的哪一项。
func paramHint(err error) error {
	he, ok := AsHTTPError(err)
	if !ok || he.Status != http.StatusBadRequest {
		return err
	}
	m := strings.ToLower(he.Message)
	if strings.Contains(m, "temperature") {
		return fmt.Errorf("%w（该模型不接受自定义温度：把设置里的「温度」清空即不发送该参数）", err)
	}
	if strings.Contains(m, "max_tokens") || strings.Contains(m, "max_completion_tokens") {
		return fmt.Errorf("%w（该模型对输出上限的字段名有要求：可尝试调小设置里的输出上限）", err)
	}
	return err
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
	// 3) UCI 着法串（如 "h2e2"）
	if mv, _, ok := pickByPositionUCI(resp, pool); ok {
		return mv, "", true
	}
	// 4) 中文着法生成-匹配（A13）
	if mv, _, ok := pickByPositionCN(resp, pos, pool); ok {
		return mv, "", true
	}
	return game.Move{}, "", false
}

// matchHit 是「文本里命中了某个 pool 着法」的一次记录。
type matchHit struct {
	move  game.Move
	index int // 命中片段在文本中的起始字节位置
}

// uciRe 匹配形如 h2e2 的着法串。
//
// ⚠️ 用它而不是 strings.Fields 切词：Fields 只按**空白**切分，中文标点不是空白，
// 于是「不走 h2e2，而走 b2e2」会被切成 "h2e2，而走" 与 "b2e2" 两块 —— 前者去掉
// 首尾标点后仍混着汉字、解析失败，白丢一个候选。带位置的正则能在任意分隔
// （空格、中文逗号、顿号、换行）下都取到真正的着法串。
// 可能引入的误报由「必须落在 pool 内」这层过滤兜住。
var uciRe = regexp.MustCompile(`[a-i][0-9][a-i][0-9]`)

// pickByPositionUCI 在文本里找出所有能匹配 pool 的 UCI 串，按**出现位置**裁决。
//
// ⚠️ 不能像以前那样「取第一个能匹配上的就返回」：模型解说里提到别的着法
// （例如「不走 h2e2 而走 b2e2」）时，会命中先出现的那个，静默走出一手非其本意的棋，
// 而且不报错、不降级。这里收集全部命中后按文本位置取**最后出现**的
// —— 模型通常在结尾给出结论。
func pickByPositionUCI(text string, pool []game.Move) (game.Move, int, bool) {
	var hits []matchHit
	for _, loc := range uciRe.FindAllStringIndex(text, -1) {
		m, ok := game.MoveFromUCI(text[loc[0]:loc[1]])
		if !ok {
			continue
		}
		for _, l := range pool {
			if l == m {
				hits = append(hits, matchHit{move: l, index: loc[0]})
				break
			}
		}
	}
	return lastHit(hits)
}

// pickByPositionCN 在文本里找出所有命中的中文着法串，按**出现位置**裁决。
//
// 规则：多个命中时取**最后出现**的（模型一般在结尾给出结论）；位置也相同时，
// 取更长的那个（带「前/后」前缀的串更具体，短串可能只是它的子串）。
func pickByPositionCN(text string, pos *game.Position, pool []game.Move) (game.Move, int, bool) {
	norm := game.NormalizeCN(text)
	if norm == "" {
		return game.Move{}, 0, false
	}
	var hits []matchHit
	for _, l := range pool {
		cn := game.NormalizeCN(pos.MoveToChinese(l))
		if cn == "" {
			continue
		}
		// 同一着法串可能出现多次，全部记录，以便按位置公平比较
		for at := 0; ; {
			i := strings.Index(norm[at:], cn)
			if i < 0 {
				break
			}
			hits = append(hits, matchHit{move: l, index: at + i})
			at += i + len(cn)
		}
	}
	return lastHit(hits)
}

// lastHit 从命中集合里选出最终着法：位置最靠后者优先。
func lastHit(hits []matchHit) (game.Move, int, bool) {
	if len(hits) == 0 {
		return game.Move{}, 0, false
	}
	best := hits[0]
	for _, h := range hits[1:] {
		if h.index > best.index {
			best = h
		}
	}
	return best.move, best.index, true
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
