package session_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/IamAyang233/panda-xiangqi/internal/engine"
	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/llm"
	"github.com/IamAyang233/panda-xiangqi/internal/session"
)

// 本文件钉住「无棋评时也要下发 llm_comment」这条契约（2026-09-22）。
//
// 背景：前端 _onComment 是**替换**气泡内容的。后端原先写成 `if comment != ""`
// 才下发，于是模型某一手没给 comment 时，气泡会一直留着上一手的解说 ——
// 看起来像在描述当前这一手。实测该模型第二步只回了 {"from","to"} 就撞上了。
// 现在后端在 LLM 模式下恒发一条（可能是空串），前端据此复位气泡。

var mockCandRe = regexp.MustCompile(
	`(?:可选合法着法|引擎推荐候选着法（由强到弱）)：([a-i][0-9][a-i][0-9](?:, [a-i][0-9][a-i][0-9])*)`)

// mockLLMNoComment 返回**不带 comment 字段**的合法着法。
func mockLLMNoComment(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var lastUser string
		for i := len(req.Messages) - 1; i >= 0; i-- {
			if req.Messages[i].Role == "user" {
				lastUser = req.Messages[i].Content
				break
			}
		}
		content := ""
		if m := mockCandRe.FindStringSubmatch(lastUser); len(m) == 2 {
			list := strings.Split(m[1], ", ")
			mv := list[len(list)/3]
			content = `{"from":"` + mv[:2] + `","to":"` + mv[2:] + `"}` // 故意不给 comment
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message":       map[string]string{"content": content},
				"finish_reason": "stop",
			}},
		})
	}))
}

// msgsOfType 返回全部指定类型的消息。
func (c *recConn) msgsOfType(kind string) []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []map[string]any
	for _, v := range c.msgs {
		if m, ok := v.(map[string]any); ok && m["type"] == kind {
			out = append(out, m)
		}
	}
	return out
}

func TestLLMEmptyCommentStillSent(t *testing.T) {
	srv := mockLLMNoComment(t)
	defer srv.Close()

	cfg := llm.DefaultConfig()
	cfg.BaseURL = srv.URL
	cfg.Model = "mock"
	cfg.TimeoutMs = 3000

	conn := &recConn{}
	sess := session.NewSession(session.ModeLLM, game.Red, 4, cfg, nil, engine.NewManager(""))
	sess.Join(conn)
	defer sess.Close()

	if err := sess.ApplyPlayerMove("h2", "e2"); err != nil {
		t.Fatalf("人类走子失败: %v", err)
	}

	deadline := time.Now().Add(15 * time.Second)
	for len(conn.msgsOfType("llm_comment")) == 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}

	msgs := conn.msgsOfType("llm_comment")
	if len(msgs) == 0 {
		t.Fatal("模型没给棋评时仍应下发一条 llm_comment（空串），否则前端气泡会残留上一手的解说")
	}
	if got := msgs[len(msgs)-1]["comment"]; got != "" {
		t.Errorf("本手无棋评，comment 应为空串，实际 %q", got)
	}
}
