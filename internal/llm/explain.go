package llm

// 复盘讲解：对着**已经下完**的一手棋要一段给人读的文字。
//
// 与 player.go 的出着契约刻意不同：出着那一套强制模型回 {"from","to","comment"}
// JSON（下棋需要机器可解析的着法 + 一句棋评），而复盘讲解是纯文本产物 ——
// 硬套 JSON 契约既限制表达，也会把「模型没按格式回」变成一次失败。所以这里
// 单独一个 prompt 与单独的容错：截断就加倍额度重试，失败就如实报错，
// **绝不**从半截思考里捞句子冒充讲解。

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ExplainRequest 一次复盘讲解请求。
type ExplainRequest struct {
	FEN        string   // 这一手**之前**的局面
	SideToMove string   // 这一手是谁走的：red | black
	RecentCN   []string // 之前几手的中文记谱（旧→新）
	MoveCN     string   // 这一手的中文记谱（如「炮二平五」）
	MoveUCI    string
	Tags       []string // 关键手标签：capture | check | blunder | mistake | mate
	BestCN     string   // 引擎建议的更好着法（中文，可空）
	Delta      int      // 与引擎最佳手的差距（可空，0 表示没这项信息）
	Result     string   // 本局结果（red_win | black_win | draw，可空）
	Reason     string   // 结束原因（checkmate | resign | …，可空）
}

const explainSystemPrompt = `你是中国象棋教练，正在给学员复盘一盘已经下完的棋。
要求：
- 只讲当前这一手，不要复述规则，不要输出 JSON，也不要列着法清单。
- 如果给了引擎的更好着法与分差，说清它好在哪里、实战这一手差在哪里；没给就讲这一手的意图与得失。
- 用中文，平实口语，不超过 150 字，不要客套话。`

// buildExplainPrompt 把请求拼成提示词。导出为不导出方法便于测试直接断言内容。
func buildExplainPrompt(req ExplainRequest) string {
	var b strings.Builder
	side := "红方"
	if req.SideToMove == "black" {
		side = "黑方"
	}
	fmt.Fprintf(&b, "局面 FEN：%s（%s走）\n", req.FEN, side)
	if len(req.RecentCN) > 0 {
		fmt.Fprintf(&b, "此前着法（旧→新）：%s\n", strings.Join(req.RecentCN, "、"))
	}
	fmt.Fprintf(&b, "需要讲解的这一手：%s（%s）%s\n", req.MoveCN, req.MoveUCI, tagText(req.Tags))
	if req.BestCN != "" {
		delta := ""
		if req.Delta > 0 {
			delta = fmt.Sprintf("（实战这一手亏约 %d 分）", req.Delta)
		}
		fmt.Fprintf(&b, "引擎认为更好的是：%s%s\n", req.BestCN, delta)
	}
	if req.Result != "" {
		fmt.Fprintf(&b, "本局最终结果：%s%s\n", resultText(req.Result), reasonText(req.Reason))
	}
	b.WriteString("\n请讲解这一手。")
	return b.String()
}

func tagText(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	m := map[string]string{
		"capture": "（吃子）", "check": "（将军）", "blunder": "（引擎判为失误）",
		"mistake": "（引擎判为疑问手）", "mate": "（终局一手）",
	}
	var out []string
	for _, t := range tags {
		if s, ok := m[t]; ok {
			out = append(out, s)
		}
	}
	return strings.Join(out, "")
}

func resultText(r string) string {
	switch r {
	case "red_win":
		return "红方胜"
	case "black_win":
		return "黑方胜"
	case "draw":
		return "和棋"
	default:
		return r
	}
}

func reasonText(r string) string {
	m := map[string]string{
		"checkmate": "将死", "stalemate": "困毙", "resign": "认输",
		"repetition": "三次重复", "long_check": "长将判负",
		"60_moves": "60 回合未分胜负", "insufficient": "子力不足",
	}
	if s, ok := m[r]; ok {
		return "（" + s + "）"
	}
	return ""
}

// Explain 生成一段复盘讲解。返回的文字可能为空串（模型只回了思考），此时调用方
// 应把「无讲解」如实呈现，而不是拿别的东西顶上。
func Explain(ctx context.Context, cfg Config, req ExplainRequest) (string, error) {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return "", fmt.Errorf("未配置大模型地址（Base URL）")
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return "", fmt.Errorf("未配置大模型名称")
	}
	client := NewClient(cfg)
	messages := []chatMessage{
		{Role: "system", Content: explainSystemPrompt},
		{Role: "user", Content: buildExplainPrompt(req)},
	}

	maxTok := cfg.maxTokens()
	const maxTokCap = 16384
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		cctx, cancel := context.WithTimeout(ctx, cfg.timeout())
		effectiveMs := minMs(cfg.timeout().Milliseconds(), remainingMs(ctx))
		reply, err := client.ChatWithLimit(cctx, messages, maxTok)
		cancel()
		if err != nil {
			if wantsMaxCompletionTokens(err) {
				client.UseMaxCompletionTokens()
				lastErr = err
				continue
			}
			if he, ok := AsHTTPError(err); ok && he.Retryable() && attempt < 3 {
				lastErr = he
				time.Sleep(time.Duration(attempt) * 800 * time.Millisecond)
				continue
			}
			lastErr = paramHint(withTimeoutHint(err, effectiveMs))
			break
		}
		if reply.Truncated() {
			lastErr = fmt.Errorf("模型输出被截断：max_tokens=%d 不足（可在设置中调大「输出上限」）", maxTok)
			if maxTok < maxTokCap {
				maxTok *= 2
				continue
			}
			break
		}
		text := strings.TrimSpace(reply.Content)
		if text == "" {
			// 只回了思考、正文为空：如实说清楚，不拿半截思考冒充讲解。
			lastErr = fmt.Errorf("模型没有给出讲解正文（思考占比过高，可在设置中调大「输出上限」）")
			break
		}
		return text, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("讲解失败")
	}
	return "", lastErr
}
