package session_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IamAyang233/panda-xiangqi/internal/engine"
	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/llm"
	"github.com/IamAyang233/panda-xiangqi/internal/session"
)

// 本文件钉住「大模型应着不会被 90s 卡死」这条契约（2026-09-22）。
//
// 背景：aiReply 的 ctx 原本一律 90s，而它比 llm 包里那个超时更靠近模型、**更早到期**
// —— 推理模型一步棋实测 45~90s，模型还在思考就被取消、静默降级；更糟的是错误提示
// 让用户去调大设置里的超时，而真凶是这 90s，调多大都没用。

// slowChatServer 返回「睡 sleepSec 秒后才回答」的服务，并用 served 报告是否答完。
//
// ⚠️ 它必须从提示词的候选/合法着法表里挑一手返回，不能写死某个着法：写死的那手
// 对「轮到的那一方」往往非法（例如人类刚走过 h2e2，AI 再走就非法），会触发
// 「解析失败→重试」，于是每个用例要跑 N 倍睡眠时间，测的也不再是超时链路。
type slowChatServer struct {
	sleepSec int
	once     sync.Once
	served   chan struct{}
}

func (s *slowChatServer) start(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		time.Sleep(time.Duration(s.sleepSec) * time.Second)

		// 从最后一条 user 消息里取合法着法/候选表，挑中间一手（对该方一定合法）
		content := `{"from":"h2","to":"e2","comment":"兜底"}`
		for i := len(req.Messages) - 1; i >= 0; i-- {
			if req.Messages[i].Role != "user" {
				continue
			}
			if m := mockCandRe.FindStringSubmatch(req.Messages[i].Content); len(m) == 2 {
				list := strings.Split(m[1], ", ")
				mv := list[len(list)/2]
				content = `{"from":"` + mv[:2] + `","to":"` + mv[2:] + `","comment":"慢思考一手"}`
			}
			break
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message":       map[string]string{"content": content},
				"finish_reason": "stop",
			}},
		})
		// ⚠️ 必须只关一次：解析失败会重试，同一 handler 会被再次调用，
		// 裸 close 在第二次调用时 panic（close of closed channel）。
		s.once.Do(func() { close(s.served) })
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestLLMThinkTimeoutAllowsSlowModel 用户把超时设得比引擎默认（90s）大时，
// 大模型应着必须能跑满这段时间，而不是被 90s 掐断。
//
// 参数余量说明：模型睡 92s（**大于 90s**，足以证明修复生效；修复前会在 90s 被掐断），
// 用户超时给 120s 留 28s 余量 —— 余量不能小：应着前的引擎候选排序要首次加载
// 68MB 权重（约 1~3s），余量只有 3s 时会在边界上翻车（实测）。
// 单用例约 100s，-short 下跳过。
func TestLLMThinkTimeoutAllowsSlowModel(t *testing.T) {
	if testing.Short() {
		t.Skip("需要约 100s 等待，-short 下跳过")
	}
	const (
		userTimeoutMs = 120000
		modelSleepSec = 92
	)
	srv := &slowChatServer{sleepSec: modelSleepSec, served: make(chan struct{})}
	cfg := llm.DefaultConfig()
	cfg.BaseURL = srv.start(t)
	cfg.Model = "mock"
	cfg.TimeoutMs = userTimeoutMs

	conn := &recConn{}
	sess := session.NewSession(session.ModeLLM, game.Red, 4, cfg, nil, engine.NewManager(""))
	sess.Join(conn)
	defer sess.Close()

	if err := sess.ApplyPlayerMove("h2", "e2"); err != nil {
		t.Fatalf("走子失败: %v", err)
	}

	select {
	case <-srv.served:
		// 模型完整答完 ⇒ 应着 ctx 足够长
	case <-time.After(time.Duration(userTimeoutMs)*time.Millisecond + 15*time.Second):
		t.Fatalf("模型在 %ds 后仍未跑完：应着 ctx 被 90s 掐断了", modelSleepSec)
	}

	// 结果应真的落到棋盘：AI 已应着、轮回到红方，且没有被降级
	deadline := time.Now().Add(10 * time.Second)
	for sess.MoveCount() < 2 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if got := sess.MoveCount(); got < 2 {
		t.Fatalf("应着未落地, moves=%d", got)
	}
	if fen := sess.SnapshotFEN(); !strings.Contains(fen, " w ") {
		t.Errorf("AI 应着后应轮红方, fen=%s", fen)
	}
	if ev := conn.msgsOfType("llm_fallback"); len(ev) != 0 {
		t.Errorf("模型正常返回时不该降级，实际收到 %d 条降级事件", len(ev))
	}
}

// TestLLMTimeoutHintNamesTimeout 真正超时的情况下，降级原因必须点明「超时」，
// 而不是含糊的「输出未能解析」或冒号后空白 —— 用户据此才知道该调什么。
func TestLLMTimeoutHintNamesTimeout(t *testing.T) {
	srv := &slowChatServer{sleepSec: 5, served: make(chan struct{})}
	cfg := llm.DefaultConfig()
	cfg.BaseURL = srv.start(t)
	cfg.Model = "mock"
	cfg.TimeoutMs = 1000 // 1s，远小于模型 5s ⇒ 必然超时

	conn := &recConn{}
	sess := session.NewSession(session.ModeLLM, game.Red, 4, cfg, nil, engine.NewManager(""))
	sess.Join(conn)
	defer sess.Close()

	if err := sess.ApplyPlayerMove("h2", "e2"); err != nil {
		t.Fatalf("走子失败: %v", err)
	}

	deadline := time.Now().Add(30 * time.Second)
	for len(conn.msgsOfType("llm_comment")) == 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	ms := conn.msgsOfType("llm_comment")
	if len(ms) == 0 {
		t.Fatal("应下发 llm_comment（含降级原因）")
	}
	comment, _ := ms[len(ms)-1]["comment"].(string)
	if !strings.Contains(comment, "超时") {
		t.Errorf("降级原因应点明超时，实际: %q", comment)
	}
	// 该报的是本次**实际生效**的上限（1s），不是其它量级
	if !strings.Contains(comment, "1000") {
		t.Errorf("降级原因应报出实际生效的 1000ms 上限，实际: %q", comment)
	}
}
