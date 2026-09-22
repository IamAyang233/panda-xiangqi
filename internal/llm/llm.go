// Package llm 实现大模型对弈：OpenAI 兼容协议客户端、着法解析（JSON + 生成-匹配）、
// 带反馈重试与本地引擎降级链（计划书 A11~A13）。
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Config 用户大模型配置（仅存浏览器 localStorage，服务端内存透传、不落盘不写日志）。
type Config struct {
	BaseURL string `json:"baseURL"`
	APIKey  string `json:"apiKey"`
	Model   string `json:"model"`
	// Temperature 为 nil 表示**不发送该字段**（设置页留空即此语义）。
	//
	// ⚠️ 用指针而不是 float64：OpenAI o1/o3 系只接受默认温度，收到任意显式值都 400。
	// 恒发（哪怕恒发 0.3）会让这类模型每一步都失败降级；能表达「不指定」才有救。
	Temperature  *float64 `json:"temperature"`
	TimeoutMs    int      `json:"timeoutMs"`
	IncludeLegal bool     `json:"includeLegalMoves"`
	EngineAssist bool     `json:"engineAssist"` // 引擎候选模式：模型从引擎排序的候选中选着，更快更强
	// MaxTokens 单次回复的 token 上限。留 0 用 DefaultMaxTokens。
	// ⚠️ 不能设小：推理模型（返回 reasoning_content 的那类）会先把额度花在思考上，
	// 上限不足时 content 为空、finish_reason=length —— 表现为「模型什么都没说」。
	MaxTokens int `json:"maxTokens,omitempty"`
}

// Float64 便于构造 Temperature 指针（配置与测试都常用）。
func Float64(v float64) *float64 { return &v }

// DefaultMaxTokens 是单次回复的默认 token 上限。
//
// 取 8192 而不是最初的 400：实测推理模型（如 DeepSeek 系 / 各家 hybrid 思考模型）
// 只回一个着法 JSON 也可能花掉 3000~6000 个思考 token，额度不足时 content 恒为空、
// finish_reason=length。非推理模型会在 JSON 输出完就自行停止，不会真的用满，
// 所以调大几乎不增加成本，却能让思考型模型第一次尝试就有余量
// —— 否则「截断 → 加倍重试」会白等一整轮（实测一轮 50s+）。
const DefaultMaxTokens = 8192

// DefaultTimeoutMs 是默认超时。
//
// ⚠️ 从 30s 一路抬到 180s：推理模型一步棋实测 50~90s；而且「被截断→加倍额度重试」
// 是两次请求**共用**这一个时间上限（外层 ctx 覆盖整次应着），只留 120s 时第二次
// 会被掐断，用户看到的是「请求超时」而不是更该看到的「输出被截断」。
const DefaultTimeoutMs = 180000

// DefaultConfig 返回默认配置。
func DefaultConfig() Config {
	return Config{
		BaseURL:      "https://api.deepseek.com/v1",
		Temperature:  Float64(0.3),
		TimeoutMs:    DefaultTimeoutMs,
		IncludeLegal: true,
		EngineAssist: true,
	}
}

// Validate 检查必填项。
func (c *Config) Validate() error {
	if strings.TrimSpace(c.BaseURL) == "" {
		return fmt.Errorf("Base URL 不能为空")
	}
	if strings.TrimSpace(c.Model) == "" {
		return fmt.Errorf("模型名称不能为空")
	}
	return nil
}

func (c *Config) timeout() time.Duration {
	if c.TimeoutMs <= 0 {
		return time.Duration(DefaultTimeoutMs) * time.Millisecond
	}
	return time.Duration(c.TimeoutMs) * time.Millisecond
}

// maxTokens 返回本次请求的输出上限。
func (c *Config) maxTokens() int {
	if c.MaxTokens <= 0 {
		return DefaultMaxTokens
	}
	return c.MaxTokens
}

// ---------------------------------------------------------------- HTTP 客户端

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	// Temperature 用指针 + omitempty：留空时**整个字段不发**。
	// ⚠️ 恒发是兼容性陷阱——OpenAI o1/o3 系只接受默认温度，收到别的值直接 400；
	// 恒发 0.3（前端默认）会让这类模型每一步都失败降级。
	Temperature *float64 `json:"temperature,omitempty"`
	// MaxTokens 与 MaxCompletionTokens 二选一：前者是通行写法，后者是新版 OpenAI
	// （o1/o3/gpt-5）与部分网关强制要求的字段名。由服务端报错驱动切换，见
	// Client.UseMaxCompletionTokens。
	MaxTokens           int  `json:"max_tokens,omitempty"`
	MaxCompletionTokens int  `json:"max_completion_tokens,omitempty"`
	Stream              bool `json:"stream"`
}

// choiceMessage 是响应里的 message。自定义解码以兼容 content 的多种形态。
type choiceMessage struct {
	Content string
	// 推理模型把思考过程放这里（DeepSeek-R1 系、各家 hybrid 思考模型的字段名
	// 不统一，reasoning_content / reasoning 都见过）。content 为空时它是唯一线索。
	ReasoningContent string
	Reasoning        string
}

// UnmarshalJSON 兼容 content 的两种现实形态：
//
//	"content": "文本"                                 —— OpenAI 标准
//	"content": [{"type":"text","text":"文本"}, ...]   —— Claude 系兼容层（One-API/New API 转发）
//
// ⚠️ 后者若按 string 解析会**整体 Unmarshal 失败**，连重试机会都没有，报「响应解析失败」。
func (m *choiceMessage) UnmarshalJSON(b []byte) error {
	var raw struct {
		Content          json.RawMessage `json:"content"`
		ReasoningContent string          `json:"reasoning_content"`
		Reasoning        string          `json:"reasoning"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	m.Content = decodeContent(raw.Content)
	m.ReasoningContent = raw.ReasoningContent
	m.Reasoning = raw.Reasoning
	return nil
}

// decodeContent 从 content 的任意形态里取出文本。
func decodeContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var b strings.Builder
		for _, blk := range blocks {
			b.WriteString(blk.Text) // 只取文本块，忽略其它类型
		}
		return b.String()
	}
	return ""
}

// apiError 是错误体。自定义解码以兼容它的多种形态：
//
//	"error": "invalid api key"                          —— 纯字符串
//	"error": {"message": "..."}                         —— 标准
//	"error": {"code": "invalid_api_key"}                —— 只有 code（部分网关）
//
// ⚠️ 字符串形态若按对象解析会整体失败、丢掉 HTTP 状态；只有 code 时若只读 message
// 会输出「API 错误: 」这种冒号后空白的提示（与之前修过的同一种病）。
type apiError struct {
	Message string
}

func (e *apiError) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		e.Message = s
		return nil
	}
	var o struct {
		Message string `json:"message"`
		Code    string `json:"code"`
		Type    string `json:"type"`
	}
	if err := json.Unmarshal(b, &o); err != nil {
		return err
	}
	e.Message = o.Message
	if e.Message == "" {
		e.Message = o.Code
	}
	if e.Message == "" {
		e.Message = o.Type
	}
	return nil
}

type chatResponse struct {
	Choices []struct {
		Message choiceMessage `json:"message"`
		// Text 是 completion 风格端点的输出字段（gpt-3.5-turbo-instruct、
		// 部分 llama.cpp / LocalAI / One-API 兼容端点）。不读它会把
		// 「模型明明答了」误判成「返回空内容」，线索完全指错方向。
		Text         string `json:"text"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *apiError `json:"error"`
}

// ChatReply 一次对话的结构化结果。
//
// 之所以不只返回字符串：推理模型可能「思考完了但额度用尽」，此时 content 为空
// 而 finish_reason=length —— 调用方必须能区分「模型没说话（被截断）」与
// 「模型说了但格式不对」，否则只能给出一句没有信息量的错误。
type ChatReply struct {
	Content      string // message.content（模型正式输出）
	Reasoning    string // 思考过程（可能为空）
	FinishReason string // stop / length / ...
}

// Truncated 报告本次输出是否因 token 上限被截断。
func (r ChatReply) Truncated() bool { return r.FinishReason == "length" }

// Text 返回可供着法解析的文本：优先正式输出，为空时退回思考过程。
//
// 退回思考过程是有意的兜底：部分模型的答案会先出现在 reasoning 里，
// 而 parseMove 的宽松提取能从中捞出 JSON/UCI/中文着法 —— 总比直接降级好。
func (r ChatReply) Text() string {
	if strings.TrimSpace(r.Content) != "" {
		return r.Content
	}
	if strings.TrimSpace(r.Reasoning) != "" {
		return r.Reasoning
	}
	return ""
}

// Empty 报告本次回复完全没有可解析文本。
func (r ChatReply) Empty() bool { return strings.TrimSpace(r.Text()) == "" }

// Client OpenAI 兼容 Chat Completions 客户端。
type Client struct {
	cfg  Config
	http *http.Client

	mu sync.Mutex
	// useMaxCompletionTokens 记录「该服务端要求用 max_completion_tokens 而非 max_tokens」。
	// 由服务端的一次明确报错驱动，之后本会话的请求都改用新字段名。
	useMaxCompletionTokens bool
}

// NewClient 创建客户端。
func NewClient(cfg Config) *Client {
	return &Client{
		cfg:  cfg,
		http: &http.Client{Timeout: cfg.timeout()},
	}
}

// UseMaxCompletionTokens 让后续请求改用 max_completion_tokens 字段名。
//
// 仅供「服务端明确要求」时调用（见 player 里对 400 的识别）：老服务端不认识这个
// 字段名，主动改用会把本来能用的模型弄坏，所以不做成默认或猜测。
func (c *Client) UseMaxCompletionTokens() {
	c.mu.Lock()
	c.useMaxCompletionTokens = true
	c.mu.Unlock()
}

func (c *Client) wantsMaxCompletionTokens() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.useMaxCompletionTokens
}

// endpointURL 由用户填的 Base URL 推出 chat completions 端点。
//
// 现实里用户会填出各种形态，直接拼 "/chat/completions" 会 404，且错误信息看不出原因：
//
//	http://host:11434/v1                     → 正常补后缀
//	http://host:7863/v1/chat/completions     → 已含端点，直接用（原先会拼成 .../completions/chat/completions）
//	http://host/v1//chat/completions/        → 折叠重复斜杠、去尾斜杠
//	api.deepseek.com/v1                      → 缺协议头，报明确错误（原先报 unsupported protocol scheme）
//
// 不做「自动补 /v1」的猜测：Ollama 与多数服务端都在 /v1 下，但把 /v1 猜错同样 404，
// 反而掩盖用户填错的事实；README 已写明正确写法。
func endpointURL(baseURL string) (string, error) {
	s := strings.TrimSpace(baseURL)
	if s == "" {
		return "", fmt.Errorf("Base URL 不能为空")
	}
	if !strings.Contains(s, "://") {
		return "", fmt.Errorf("Base URL 缺少协议前缀，应以 http:// 或 https:// 开头（当前为 %q）", s)
	}
	// 折叠路径里的重复斜杠（保留协议后的 "//"）
	if i := strings.Index(s, "://"); i >= 0 {
		head, tail := s[:i+3], s[i+3:]
		for strings.Contains(tail, "//") {
			tail = strings.ReplaceAll(tail, "//", "/")
		}
		s = head + tail
	}
	s = strings.TrimRight(s, "/")
	if strings.HasSuffix(s, "/chat/completions") {
		return s, nil
	}
	return s + "/chat/completions", nil
}

// HTTPError 是带状态码的 HTTP 错误，供调用方判断可否重试。
type HTTPError struct {
	Status  int
	Message string
}

func (e *HTTPError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("HTTP %d", e.Status)
	}
	return fmt.Sprintf("HTTP %d: %s", e.Status, e.Message)
}

// Retryable 报告该状态是否值得重试。
//
// 限流（429）与 5xx 多是瞬时的，重试有意义；原先一律与「密钥错」同等对待、
// 立即降级，限流密集时会让整个对弈几乎全程由本地引擎代走。
func (e *HTTPError) Retryable() bool {
	return e.Status == http.StatusTooManyRequests || e.Status >= 500
}

// AsHTTPError 取错误链里的 HTTPError。
func AsHTTPError(err error) (*HTTPError, bool) {
	var he *HTTPError
	if errors.As(err, &he) {
		return he, true
	}
	return nil, false
}

// Chat 发送一轮对话，返回结构化结果。
func (c *Client) Chat(ctx context.Context, messages []chatMessage) (ChatReply, error) {
	return c.ChatWithLimit(ctx, messages, c.cfg.maxTokens())
}

// ChatWithLimit 与 Chat 相同，但显式指定本次输出的 token 上限。
//
// 单独暴露上限是为了让调用方在「被截断」时用更大的额度重试 —— 推理模型的思考
// 长度是变化的，固定一个值总会在某些局面上不够。
func (c *Client) ChatWithLimit(ctx context.Context, messages []chatMessage, maxTokens int) (ChatReply, error) {
	if maxTokens <= 0 {
		maxTokens = DefaultMaxTokens
	}
	reqBody := chatRequest{
		Model:       c.cfg.Model,
		Messages:    messages,
		Temperature: c.cfg.Temperature, // nil ⇒ 整个字段不发
	}
	if c.wantsMaxCompletionTokens() {
		reqBody.MaxCompletionTokens = maxTokens
	} else {
		reqBody.MaxTokens = maxTokens
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return ChatReply{}, err
	}
	url, err := endpointURL(c.cfg.BaseURL)
	if err != nil {
		return ChatReply{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return ChatReply{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return ChatReply{}, fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 响应大小熔断 1MB
	if err != nil {
		return ChatReply{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return ChatReply{}, &HTTPError{Status: resp.StatusCode, Message: extractErrMessage(data)}
	}
	var cr chatResponse
	if err := json.Unmarshal(data, &cr); err != nil {
		return ChatReply{}, fmt.Errorf("响应解析失败: %w", err)
	}
	if cr.Error != nil {
		return ChatReply{}, fmt.Errorf("API 错误: %s", cr.Error.Message)
	}
	if len(cr.Choices) == 0 {
		return ChatReply{}, fmt.Errorf("空响应（无 choices）")
	}
	ch := cr.Choices[0]
	return ChatReply{
		Content: firstNonEmptyStr(ch.Message.Content, ch.Text),
		// 先 reasoning_content 再 reasoning；正文为空时它是唯一线索。
		Reasoning:    firstNonEmptyStr(ch.Message.ReasoningContent, ch.Message.Reasoning),
		FinishReason: ch.FinishReason,
	}, nil
}

// extractErrMessage 从非 200 响应体里抽出可读信息。
//
// 兼容三种形态（见 apiError）：纯字符串、只有 message、只有 code；
// 都抽不出就退回截断的原文，至少让用户看到服务端说了什么。
func extractErrMessage(data []byte) string {
	var wrap struct {
		Error *apiError `json:"error"`
	}
	if err := json.Unmarshal(data, &wrap); err == nil && wrap.Error != nil && wrap.Error.Message != "" {
		return wrap.Error.Message
	}
	msg := strings.TrimSpace(string(data))
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return msg
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// Ping 连通性测试。
//
// ⚠️ 必须校验「拿到了非空内容」：只看 err==nil 的话，一个把额度全花在思考上、
// content 为空的模型也会被判成「连接成功」，用户会以为配置没问题，
// 直到每步棋都降级才发觉 —— 那正是这次被报上来的症状。
func (c *Client) Ping(ctx context.Context) (latencyMs int64, err error) {
	start := time.Now()
	reply, err := c.Chat(ctx, []chatMessage{{Role: "user", Content: "ping，请只回复 pong"}})
	elapsed := time.Since(start).Milliseconds()
	if err != nil {
		return elapsed, err
	}
	if reply.Empty() {
		if reply.Truncated() {
			return elapsed, fmt.Errorf("模型未返回内容：输出被 max_tokens 截断（思考占用全部额度）。"+
				"请调大设置里的 maxTokens（当前 %d）或改用非推理模型", c.cfg.maxTokens())
		}
		return elapsed, fmt.Errorf("模型返回空内容（content 与 reasoning 均为空）")
	}
	return elapsed, nil
}
