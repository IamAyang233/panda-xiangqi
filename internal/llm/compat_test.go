package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
)

// 本文件覆盖「各家 OpenAI 兼容实现的现实变体」（2026-09-22）。
//
// 每一条都对应一种真实存在、而原实现会硬失败或给错线索的形态：
//   - temperature 恒发 → OpenAI o1/o3 系直接 400
//   - max_tokens 被拒 → 新版 OpenAI 要求 max_completion_tokens
//   - content 是分块数组 → Claude 系兼容层，按 string 解析会整体 Unmarshal 失败
//   - choices[0].text → completion 风格端点，会被误判成「模型返回空内容」
//   - error 是纯字符串 / 只有 code → 丢失 HTTP 状态，或提示冒号后空白
//   - BaseURL 已含端点 / 重复斜杠 / 缺协议头 → 404 或费解的 scheme 报错

// captureBody 起一个记录请求体的服务，返回固定响应。
func captureBody(t *testing.T, resp string) (url string, body func() string) {
	t.Helper()
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(resp))
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func() string { return got }
}

func okResp(content string) string {
	b, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{
			"message":       map[string]string{"content": content},
			"finish_reason": "stop",
		}},
	})
	return string(b)
}

// TestTemperatureOmittedWhenNil 温度留空时不得发送该字段（o1/o3 系只接受默认温度）。
func TestTemperatureOmittedWhenNil(t *testing.T) {
	url, body := captureBody(t, okResp("pong"))
	cfg := DefaultConfig()
	cfg.BaseURL = url
	cfg.Model = "mock"
	cfg.Temperature = nil // 设置页留空即此语义

	if _, err := NewClient(cfg).Chat(context.Background(), []chatMessage{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatalf("请求应成功: %v", err)
	}
	if strings.Contains(body(), "temperature") {
		t.Errorf("温度为 nil 时不该发送该字段，实际请求体: %s", body())
	}
}

// TestTemperatureSentWhenSet 显式设了温度就要发出去（不能为了兼容 o1 把普通模型也改坏）。
func TestTemperatureSentWhenSet(t *testing.T) {
	url, body := captureBody(t, okResp("pong"))
	cfg := DefaultConfig()
	cfg.BaseURL = url
	cfg.Model = "mock"
	cfg.Temperature = Float64(0.7)

	if _, err := NewClient(cfg).Chat(context.Background(), []chatMessage{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatalf("请求应成功: %v", err)
	}
	if !strings.Contains(body(), `"temperature":0.7`) {
		t.Errorf("显式温度应被发送，实际请求体: %s", body())
	}
}

// TestSwitchesToMaxCompletionTokens 服务端明确要求 max_completion_tokens 时，
// 应改用该字段重试一次并成功（否则这类模型每步都降级）。
func TestSwitchesToMaxCompletionTokens(t *testing.T) {
	var (
		mu     sync.Mutex
		bodies []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		n := len(bodies)
		mu.Unlock()
		if n == 1 {
			// 第一次：明确要求换字段名
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"This model does not support max_tokens, please use max_completion_tokens"}}`))
			return
		}
		_, _ = w.Write([]byte(okResp(`{"from":"h2","to":"e2"}`)))
	}))
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.BaseURL = srv.URL
	cfg.Model = "mock"
	cfg.TimeoutMs = 5000

	pos, err := game.ParseFEN("rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	res, _ := NewPlayer(cfg).BestMove(context.Background(), pos, nil)
	if res.Fallback {
		t.Fatalf("换字段名后应成功，不该降级（%d 次请求，请求体: %v）", len(bodies), bodies)
	}
	if len(bodies) < 2 {
		t.Fatalf("应发出 2 次请求，实际 %d", len(bodies))
	}
	if !strings.Contains(bodies[1], "max_completion_tokens") {
		t.Errorf("第二次请求应改用 max_completion_tokens，实际: %s", bodies[1])
	}
	if strings.Contains(bodies[1], `"max_tokens"`) {
		t.Errorf("第二次请求不该再带 max_tokens，实际: %s", bodies[1])
	}
}

// TestContentArrayForm Claude 系兼容层把 content 返回成分块数组：
// 按 string 解析会整体失败，必须能取出文本。
func TestContentArrayForm(t *testing.T) {
	resp := `{"choices":[{"message":{"content":[{"type":"text","text":"{\"from\":\"h2\",\"to\":\"e2\"}"}]},"finish_reason":"stop"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(resp))
	}))
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.BaseURL = srv.URL
	cfg.Model = "mock"
	reply, err := NewClient(cfg).Chat(context.Background(), []chatMessage{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("分块数组形态不该导致解析失败: %v", err)
	}
	if !strings.Contains(reply.Content, "h2") {
		t.Errorf("应从 content 数组里取出文本，实际: %q", reply.Content)
	}
}

// TestCompletionStyleText completion 风格端点把输出放在 choices[0].text，
// 原实现会误判成「模型返回空内容」——线索完全指错方向。
func TestCompletionStyleText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"text":"{\"from\":\"h2\",\"to\":\"e2\"}","finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.BaseURL = srv.URL
	cfg.Model = "mock"
	reply, err := NewClient(cfg).Chat(context.Background(), []chatMessage{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("completion 风格不该报错: %v", err)
	}
	if reply.Empty() {
		t.Fatal("应把 choices[0].text 当作输出，而不是「空内容」")
	}
	if !strings.Contains(reply.Text(), "h2") {
		t.Errorf("应取到 text 内容，实际: %q", reply.Text())
	}
}

// TestErrorBodyVariants 错误体的三种现实形态都要能读出信息，且保留 HTTP 状态。
func TestErrorBodyVariants(t *testing.T) {
	cases := []struct{ name, body, wantSub string }{
		{"纯字符串 error", `{"error":"invalid api key"}`, "invalid api key"},
		{"只有 code", `{"error":{"code":"invalid_api_key"}}`, "invalid_api_key"},
		{"标准 message", `{"error":{"message":"rate limited"}}`, "rate limited"},
		{"非 JSON 原文", `upstream timeout`, "upstream timeout"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(c.body))
			}))
			defer srv.Close()

			cfg := DefaultConfig()
			cfg.BaseURL = srv.URL
			cfg.Model = "mock"
			_, err := NewClient(cfg).Chat(context.Background(), []chatMessage{{Role: "user", Content: "hi"}})
			if err == nil {
				t.Fatal("应返回错误")
			}
			if !strings.Contains(err.Error(), c.wantSub) {
				t.Errorf("错误信息应包含 %q，实际 %q", c.wantSub, err.Error())
			}
			// 冒号后空白是之前修过的同一种病，这里守住
			if strings.HasSuffix(strings.TrimSpace(err.Error()), ":") {
				t.Errorf("错误信息不该以冒号结尾（冒号后空白）: %q", err.Error())
			}
			he, ok := AsHTTPError(err)
			if !ok || he.Status != http.StatusUnauthorized {
				t.Errorf("应保留 HTTP 状态码，实际 %+v", err)
			}
		})
	}
}

// TestRetryableStatuses 限流与 5xx 判为可重试；4xx 配置类不重试。
func TestRetryableStatuses(t *testing.T) {
	cases := map[int]bool{429: true, 500: true, 502: true, 503: true, 400: false, 401: false, 404: false}
	for status, want := range cases {
		he := &HTTPError{Status: status}
		if got := he.Retryable(); got != want {
			t.Errorf("HTTP %d 可重试应为 %v，实际 %v", status, want, got)
		}
	}
}

// TestEndpointURLVariants Base URL 的各种真实写法都要拼对。
func TestEndpointURLVariants(t *testing.T) {
	const want = "http://h:7863/v1/chat/completions"
	cases := []struct{ in, want string }{
		{"http://h:7863/v1", want},
		{"http://h:7863/v1/", want},
		{"http://h:7863/v1//", want},
		{"http://h:7863/v1/chat/completions", want},  // 已含端点，别重复拼
		{"http://h:7863/v1/chat/completions/", want}, // 尾斜杠
		{"  http://h:7863/v1  ", want},               // 首尾空白
		{"https://api.deepseek.com/v1", "https://api.deepseek.com/v1/chat/completions"},
	}
	for _, c := range cases {
		got, err := endpointURL(c.in)
		if err != nil {
			t.Errorf("endpointURL(%q) 报错: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("endpointURL(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// TestEndpointURLMissingScheme 缺协议头要给明确提示，而不是 unsupported protocol scheme。
func TestEndpointURLMissingScheme(t *testing.T) {
	_, err := endpointURL("api.deepseek.com/v1")
	if err == nil {
		t.Fatal("缺协议头应报错")
	}
	if !strings.Contains(err.Error(), "http://") {
		t.Errorf("提示应说明要补 http:// 或 https://，实际: %v", err)
	}
}

// TestParamHintForTemperature 温度被拒时报错要指向设置里的「温度」项。
func TestParamHintForTemperature(t *testing.T) {
	err := paramHint(&HTTPError{Status: 400, Message: `'temperature' does not support 0.3`})
	if !strings.Contains(err.Error(), "温度") {
		t.Errorf("应提示去改设置里的温度，实际: %v", err)
	}
}
