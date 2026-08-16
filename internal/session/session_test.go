package session

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
)

var mockLegalRe = regexp.MustCompile(`(?:可选合法着法|引擎推荐候选着法（由强到弱）)：([a-i][0-9][a-i][0-9](?:, [a-i][0-9][a-i][0-9])*)`)

// mockLLM 模拟 OpenAI 兼容服务：从提示词合法着法表取一手。
func mockLLM(t *testing.T, mode string) *httptest.Server {
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
		content := "我走车九平十！"
		if mode != "illegal" {
			if m := mockLegalRe.FindStringSubmatch(lastUser); len(m) == 2 {
				mv := strings.Split(m[1], ", ")[len(strings.Split(m[1], ", "))/3]
				content = `{"from":"` + mv[:2] + `","to":"` + mv[2:] + `","comment":"熊猫咬合"}`
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]string{"content": content}}},
		})
	}))
}

// TestLLMModeEndToEnd 大模型模式全链路：人类走子 → LLM 应着（合法解析 + 棋评）。
func TestLLMModeEndToEnd(t *testing.T) {
	srv := mockLLM(t, "legal")
	defer srv.Close()

	cfg := llm.DefaultConfig()
	cfg.BaseURL = srv.URL
	cfg.Model = "mock"
	cfg.TimeoutMs = 3000

	sess := NewSession(ModeLLM, game.Red, 4, cfg, nil, engine.NewManager(""))
	if err := sess.ApplyPlayerMove("h2", "e2"); err != nil {
		t.Fatalf("人类走子失败: %v", err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for sess.MoveCount() < 2 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if got := sess.MoveCount(); got < 2 {
		t.Fatalf("LLM 未应着, moves=%d", got)
	}
	st := sess.SnapshotFEN()
	if !strings.Contains(st, " w ") {
		t.Errorf("LLM 应着后应轮红方, fen=%s", st)
	}
}

// TestLLMModeFallback 非法输出 ×3 → 本地引擎降级代走且对局继续。
func TestLLMModeFallback(t *testing.T) {
	srv := mockLLM(t, "illegal")
	defer srv.Close()

	cfg := llm.DefaultConfig()
	cfg.BaseURL = srv.URL
	cfg.Model = "mock"
	cfg.TimeoutMs = 2000

	sess := NewSession(ModeLLM, game.Red, 4, cfg, nil, engine.NewManager(""))
	if err := sess.ApplyPlayerMove("b2", "e2"); err != nil {
		t.Fatalf("人类走子失败: %v", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for sess.MoveCount() < 2 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if got := sess.MoveCount(); got < 2 {
		t.Fatalf("降级代走未发生, moves=%d", got)
	}
	st := sess.SnapshotFEN()
	if !strings.Contains(st, " w ") {
		t.Errorf("降级后应轮红方, fen=%s", st)
	}
}
