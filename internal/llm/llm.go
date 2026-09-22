// Package llm 实现大模型对弈：OpenAI 兼容协议客户端、着法解析（JSON + 生成-匹配）、
// 带反馈重试与本地引擎降级链（计划书 A11~A13）。
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Config 用户大模型配置（仅存浏览器 localStorage，服务端内存透传、不落盘不写日志）。
type Config struct {
	BaseURL      string  `json:"baseURL"`
	APIKey       string  `json:"apiKey"`
	Model        string  `json:"model"`
	Temperature  float64 `json:"temperature"`
	TimeoutMs    int     `json:"timeoutMs"`
	IncludeLegal bool    `json:"includeLegalMoves"`
	EngineAssist bool    `json:"engineAssist"` // 引擎候选模式：模型从引擎排序的候选中选着，更快更强
	// MaxTokens 单次回复的 token 上限。留 0 用 DefaultMaxTokens。
	// ⚠️ 不能设小：推理模型（返回 reasoning_content 的那类）会先把额度花在思考上，
	// 上限不足时 content 为空、finish_reason=length —— 表现为「模型什么都没说」。
	MaxTokens int `json:"maxTokens,omitempty"`
}

// DefaultMaxTokens 是单次回复的默认 token 上限。
//
// 取 4096 而不是原来的 400：实测推理模型（如 DeepSeek 系 / 各家 hybrid 思考模型）
// 只回一个着法 JSON 也可能花掉 3000+ 个思考 token，400 会让 content 恒为空。
// 非推理模型会在 JSON 输出完就自行停止，不会真的用满，所以调大几乎不增加成本。
const DefaultMaxTokens = 4096

// DefaultTimeoutMs 是默认超时。
//
// ⚠️ 从 30s 调到 120s：推理模型一步棋实测可达 50s+，30s 会在拿到结果前就超时。
const DefaultTimeoutMs = 120000

// DefaultConfig 返回默认配置。
func DefaultConfig() Config {
	return Config{
		BaseURL:      "https://api.deepseek.com/v1",
		Temperature:  0.3,
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
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
	MaxTokens   int           `json:"max_tokens,omitempty"` // 限制输出长度，加快响应
	Stream      bool          `json:"stream"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
			// 推理模型把思考过程放这里（DeepSeek-R1 系、各家 hybrid 思考模型的字段名
			// 不统一，reasoning_content / reasoning 都见过）。content 为空时它是唯一线索。
			ReasoningContent string `json:"reasoning_content"`
			Reasoning        string `json:"reasoning"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
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
}

// NewClient 创建客户端。
func NewClient(cfg Config) *Client {
	return &Client{
		cfg:  cfg,
		http: &http.Client{Timeout: cfg.timeout()},
	}
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
	body, err := json.Marshal(chatRequest{
		Model:       c.cfg.Model,
		Messages:    messages,
		Temperature: c.cfg.Temperature,
		MaxTokens:   maxTokens,
	})
	if err != nil {
		return ChatReply{}, err
	}
	url := strings.TrimRight(c.cfg.BaseURL, "/") + "/chat/completions"
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
		msg := string(data)
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return ChatReply{}, fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
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
		Content:      ch.Message.Content,
		Reasoning:    firstNonEmptyStr(ch.Message.ReasoningContent, ch.Message.Reasoning),
		FinishReason: ch.FinishReason,
	}, nil
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
