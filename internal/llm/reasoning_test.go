package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
)

// 本文件覆盖「推理模型（reasoning model）」这一类的兼容问题（2026-09-22）。
//
// 症状：大模型对弈每一步都降级为本地引擎，提示为
// 「本地引擎代走（输出未能解析为合法着法: ）」—— 冒号后是空的。
//
// 根因（实测某 OpenAI 兼容推理模型）：
//   - 请求写死 max_tokens=400，而该模型回一个着法要 300~3600 个**思考** token；
//   - 额度被思考吃光后 content 为空、finish_reason=length；
//   - 旧实现把空串格式化成「…: %.120s」⇒ 用户看到冒号后什么都没有；
//   - Ping 只看 err==nil ⇒ 空响应也报「连接成功」，掩盖了问题。
//
// 修复：识别 reasoning_content、截断时加倍额度重试、空内容单独报因、
// Ping 校验非空。

// reasoningServer 按脚本返回响应，并记录每次请求的 max_tokens。
type reasoningServer struct {
	mu       sync.Mutex
	requests []int // 每次请求的 max_tokens
	replies  []map[string]any
}

func (s *reasoningServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		s.mu.Lock()
		s.requests = append(s.requests, req.MaxTokens)
		idx := len(s.requests) - 1
		if idx >= len(s.replies) {
			idx = len(s.replies) - 1
		}
		reply := s.replies[idx]
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{reply},
		})
	})
}

// TestReasoningFallbackParsesMove 正文为空、答案落在 reasoning_content 里时，
// 应能从思考过程里捞着法，而不是直接降级。
func TestReasoningFallbackParsesMove(t *testing.T) {
	srv := &reasoningServer{replies: []map[string]any{{
		"message": map[string]string{
			"content":           "",
			"reasoning_content": `分析局面后我决定走 {"from":"h2","to":"e2","comment":"中炮"} 这一手`,
		},
		"finish_reason": "stop",
	}}}
	hs := httptest.NewServer(srv.handler())
	defer hs.Close()

	p := game.NewPosition()
	res, err := NewPlayer(testConfig(hs.URL)).BestMove(context.Background(), p, nil)
	if err != nil {
		t.Fatalf("应从 reasoning 里解析出着法，实际报错: %v", err)
	}
	if res.Fallback {
		t.Error("解析成功就不该标记为降级")
	}
	if res.Move.String() != "h2e2" {
		t.Errorf("应从 reasoning 中取出 h2e2，实际 %s", res.Move.String())
	}
}

// TestTruncatedOutputEscalatesMaxTokens 被截断（finish_reason=length）时应**加倍额度**
// 再试，而不是原样重发同样的请求。
func TestTruncatedOutputEscalatesMaxTokens(t *testing.T) {
	srv := &reasoningServer{replies: []map[string]any{
		// 第 1 次：思考吃满额度，正文为空 ⇒ 触发加倍
		{"message": map[string]string{"content": "", "reasoning_content": "思考中…"},
			"finish_reason": "length"},
		// 第 2 次（额度翻倍）：正常产出
		{"message": map[string]string{"content": `{"from":"b2","to":"e2"}`, "reasoning_content": ""},
			"finish_reason": "stop"},
	}}
	hs := httptest.NewServer(srv.handler())
	defer hs.Close()

	cfg := testConfig(hs.URL)
	cfg.MaxTokens = 512
	p := game.NewPosition()
	res, err := NewPlayer(cfg).BestMove(context.Background(), p, nil)
	if err != nil {
		t.Fatalf("第二次应成功: %v", err)
	}
	if res.Fallback || res.Move.String() != "b2e2" {
		t.Errorf("期望第二次成功取到 b2e2，实际 fallback=%v move=%s", res.Fallback, res.Move.String())
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.requests) < 2 {
		t.Fatalf("应发出至少 2 次请求（第二次加倍），实际 %d 次", len(srv.requests))
	}
	if srv.requests[1] != srv.requests[0]*2 {
		t.Errorf("截断后应加倍 max_tokens：第1次=%d 第2次=%d", srv.requests[0], srv.requests[1])
	}
}

// TestEmptyContentReportsConcreteReason 全程空正文且未截断时，错误必须**说明是空内容**，
// 不能退化成「输出未能解析为合法着法: 」这种冒号后什么都没有的提示。
func TestEmptyContentReportsConcreteReason(t *testing.T) {
	srv := &reasoningServer{replies: []map[string]any{{
		"message":       map[string]string{"content": "", "reasoning_content": ""},
		"finish_reason": "stop",
	}}}
	hs := httptest.NewServer(srv.handler())
	defer hs.Close()

	p := game.NewPosition()
	res, err := NewPlayer(testConfig(hs.URL)).BestMove(context.Background(), p, nil)
	if err == nil {
		t.Fatal("空正文应报错")
	}
	if !strings.Contains(err.Error(), "空内容") {
		t.Errorf("错误应点明「空内容」，实际: %v", err)
	}
	if !res.Fallback || !p.IsLegal(res.Move) {
		t.Errorf("仍须降级到合法着法: %+v", res)
	}
	if !strings.Contains(res.Comment, "空内容") {
		t.Errorf("降级提示应带出具体原因，实际: %q", res.Comment)
	}
}

// TestTruncatedReportsReason 反复截断（额度到顶仍不够）时，原因必须点明「截断」。
func TestTruncatedReportsReason(t *testing.T) {
	srv := &reasoningServer{replies: []map[string]any{{
		"message":       map[string]string{"content": "", "reasoning_content": "思考中…"},
		"finish_reason": "length",
	}}}
	hs := httptest.NewServer(srv.handler())
	defer hs.Close()

	p := game.NewPosition()
	_, err := NewPlayer(testConfig(hs.URL)).BestMove(context.Background(), p, nil)
	if err == nil || !strings.Contains(err.Error(), "截断") {
		t.Errorf("应报「输出被截断」，实际: %v", err)
	}
}

// TestPingRejectsEmptyContent 连通性测试必须校验「真的拿到了内容」。
//
// 旧实现只看 err==nil，于是「思考吃满额度、content 为空」的模型也会被判成
// 连接成功 —— 用户以为配置没问题，直到每步棋都降级才发现。
func TestPingRejectsEmptyContent(t *testing.T) {
	srv := &reasoningServer{replies: []map[string]any{{
		"message":       map[string]string{"content": "", "reasoning_content": ""},
		"finish_reason": "stop",
	}}}
	hs := httptest.NewServer(srv.handler())
	defer hs.Close()

	if _, err := NewClient(testConfig(hs.URL)).Ping(context.Background()); err == nil {
		t.Error("空内容必须让连通性测试失败，否则会掩盖「模型不产出正文」这一故障")
	}
}

// TestPingAcceptsReasoningOnly 仅 reasoning 有内容时，连通性测试应放行 ——
// 模型确实在正常工作（答案在思考里），不该被判成连不通。
func TestPingAcceptsReasoningOnly(t *testing.T) {
	srv := &reasoningServer{replies: []map[string]any{{
		"message":       map[string]string{"content": "", "reasoning_content": "pong"},
		"finish_reason": "stop",
	}}}
	hs := httptest.NewServer(srv.handler())
	defer hs.Close()

	if _, err := NewClient(testConfig(hs.URL)).Ping(context.Background()); err != nil {
		t.Errorf("reasoning 有内容应视为连通正常，实际: %v", err)
	}
}
