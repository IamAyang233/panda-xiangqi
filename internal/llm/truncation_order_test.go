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

// 本文件钉住「被截断时先加倍重试，不从半截思考里捞着法」这条契约（2026-09-22）。
//
// 背景：一次真机实测暴露的坑 —— 推理模型思考超出 max_tokens 时 finish_reason=length、
// content 为空、reasoning 是**半截思维链**。当时实现先调 Text()（content 空则回退
// reasoning）再解析，于是直接从半截思考里捞出一手就返回了：
//   - 那一手往往只是模型**中途考虑过**的一手（思维链天然会枚举并否定多手），不是结论；
//   - 且该路径不返回棋评，界面上解说恒为空；
//   - 更糟的是它跳过了「加倍额度重试」，模型本可以好好答完。
//
// 现在顺序改为：截断 → 加倍重试；只有额度到顶仍截断才如实报错并降级。

// truncThenOK 第 1 次返回「截断且思考里含合法着法」，第 2 次返回正常 JSON。
func truncThenOK(t *testing.T, reasoning string, second string) (*httptest.Server, func() []int) {
	t.Helper()
	var (
		mu    sync.Mutex
		toks  []int
		calls int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var req chatRequest
		_ = json.Unmarshal(b, &req)

		mu.Lock()
		calls++
		n := calls
		toks = append(toks, req.MaxTokens)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []any{map[string]any{
					"message": map[string]string{
						"content":           "",
						"reasoning_content": reasoning,
					},
					"finish_reason": "length", // 截断
				}},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message":       map[string]string{"content": second},
				"finish_reason": "stop",
			}},
		})
	}))
	t.Cleanup(srv.Close)
	return srv, func() []int {
		mu.Lock()
		defer mu.Unlock()
		return append([]int(nil), toks...)
	}
}

// TestTruncatedDoesNotSalvageFromReasoning 截断时不得从半截思考里捞着法，
// 而应加倍额度重试并采用模型真正给出的答案（含棋评）。
func TestTruncatedDoesNotSalvageFromReasoning(t *testing.T) {
	pos, err := game.ParseFEN(openingFEN)
	if err != nil {
		t.Fatal(err)
	}
	pool := pos.LegalMoves(pos.Turn)

	// 找一个「思考里提到、但不是结论」的合法着法
	var bait game.Move
	for _, m := range pool {
		if m.String() == "h2e2" {
			bait = m
			break
		}
	}
	if bait.String() != "h2e2" {
		t.Fatal("前提失效：找不到 h2e2")
	}

	// 第一次：思考里明确提到 h2e2（诱饵），但被截断
	reasoning := "我先看 炮二平五（h2e2）这手，它中路进攻……（思考被截断）"
	// 第二次：模型真正给出的结论与棋评
	second := `{"from":"b2","to":"e2","comment":"八路炮平中"}`

	srv, toks := truncThenOK(t, reasoning, second)
	cfg := DefaultConfig()
	cfg.BaseURL = srv.URL
	cfg.Model = "mock"
	cfg.MaxTokens = 512 // 小额度便于观察加倍

	res, err := NewPlayer(cfg).BestMove(context.Background(), pos, nil)
	if err != nil {
		t.Fatalf("第二次应成功: %v", err)
	}
	if res.Move.String() != "b2e2" {
		t.Errorf("应采纳第二次（模型真正的结论 b2e2），实际 %s —— 说明又从半截思考里捞了诱饵",
			res.Move.String())
	}
	if res.Comment != "八路炮平中" {
		t.Errorf("应采用第二次的棋评，实际 %q —— 从思考里捞取时拿不到棋评", res.Comment)
	}
	got := toks()
	if len(got) < 2 {
		t.Fatalf("截断后应重试，实际只发了 %d 次请求", len(got))
	}
	if got[1] != got[0]*2 {
		t.Errorf("重试应加倍额度：第1次=%d 第2次=%d", got[0], got[1])
	}
}

// TestTruncatedAtCapReportsClearly 额度到顶仍被截断时，要如实报「截断」并降级，
// 不能靠捞取掩盖过去（那样用户既不知道模型没答完，也不知道可以调大输出上限）。
func TestTruncatedAtCapReportsClearly(t *testing.T) {
	pos, err := game.ParseFEN(openingFEN)
	if err != nil {
		t.Fatal(err)
	}
	// 一直截断，且思考里始终含一个合法着法（诱饵）
	reasoning := "我在想 炮二平五（h2e2）……（永远被截断）"

	var (
		mu   sync.Mutex
		cnt  int
		last int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var req chatRequest
		_ = json.Unmarshal(b, &req)
		mu.Lock()
		cnt++
		last = req.MaxTokens
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]string{
					"content":           "",
					"reasoning_content": reasoning,
				},
				"finish_reason": "length",
			}},
		})
	}))
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.BaseURL = srv.URL
	cfg.Model = "mock"
	// 默认额度已很大，这里设一个能快速触顶的值
	cfg.MaxTokens = 4096

	res, err := NewPlayer(cfg).BestMove(context.Background(), pos, nil)
	if err == nil {
		t.Fatal("持续截断应返回错误")
	}
	if !strings.Contains(err.Error(), "截断") {
		t.Errorf("错误应点明「截断」，实际: %v", err)
	}
	if !strings.Contains(err.Error(), "输出上限") {
		t.Errorf("错误应提示可以调大「输出上限」，实际: %v", err)
	}
	// 必须降级到合法着法，而不是拿诱饵当结论
	if !res.Fallback || !pos.IsLegal(res.Move) {
		t.Errorf("触顶后应降级为合法着法: fallback=%v move=%s", res.Fallback, res.Move)
	}
	mu.Lock()
	defer mu.Unlock()
	if cnt < 2 {
		t.Errorf("触顶过程中应至少重试一次（额度加倍），实际请求 %d 次", cnt)
	}
	if last <= 4096 {
		t.Errorf("重试应增大额度，最后一次 max_tokens=%d", last)
	}
}
