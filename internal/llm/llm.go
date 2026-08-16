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
}

// DefaultConfig 返回默认配置。
func DefaultConfig() Config {
	return Config{
		BaseURL:      "https://api.deepseek.com/v1",
		Temperature:  0.3,
		TimeoutMs:    30000,
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
		return 30 * time.Second
	}
	return time.Duration(c.TimeoutMs) * time.Millisecond
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
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

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

// Chat 发送一轮对话，返回模型输出文本。
func (c *Client) Chat(ctx context.Context, messages []chatMessage) (string, error) {
	body, err := json.Marshal(chatRequest{
		Model:       c.cfg.Model,
		Messages:    messages,
		Temperature: c.cfg.Temperature,
		MaxTokens:   400,
	})
	if err != nil {
		return "", err
	}
	url := strings.TrimRight(c.cfg.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 响应大小熔断 1MB
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		msg := string(data)
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
	}
	var cr chatResponse
	if err := json.Unmarshal(data, &cr); err != nil {
		return "", fmt.Errorf("响应解析失败: %w", err)
	}
	if cr.Error != nil {
		return "", fmt.Errorf("API 错误: %s", cr.Error.Message)
	}
	if len(cr.Choices) == 0 {
		return "", fmt.Errorf("空响应（无 choices）")
	}
	return cr.Choices[0].Message.Content, nil
}

// Ping 连通性测试。
func (c *Client) Ping(ctx context.Context) (latencyMs int64, err error) {
	start := time.Now()
	_, err = c.Chat(ctx, []chatMessage{{Role: "user", Content: "ping，请只回复 pong"}})
	return time.Since(start).Milliseconds(), err
}
