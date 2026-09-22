package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
)

// 本文件钉住「超时提示必须报实际生效的上限」这条契约（2026-09-22）。
//
// 背景：ctx 取消时真正到期的那一层，可能是上游调用方给的 ctx，而不是本包的
// config.TimeoutMs。原先 withTimeoutHint 一律打印 config 值，于是用户会看到
// 「超时 120000ms」却依然失败 —— 照着提示把这个值调得更大当然毫无效果。

// TestTimeoutHintReportsUpstreamLimit 上游 ctx 比配置更紧时，
// 提示必须指向那个更紧的上限，而不是配置里的宽松值。
func TestTimeoutHintReportsUpstreamLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(3 * time.Second) // 必然超过下面的上游 300ms
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message":       map[string]string{"content": "{}"},
				"finish_reason": "stop",
			}},
		})
	}))
	defer srv.Close()

	const cfgTimeoutMs = 200000 // 配置很宽松
	cfg := DefaultConfig()
	cfg.BaseURL = srv.URL
	cfg.Model = "mock"
	cfg.TimeoutMs = cfgTimeoutMs

	p := NewPlayer(cfg)
	// 上游只给 300ms ⇒ 实际生效的上限是 ~300ms
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	pos, err := game.ParseFEN("rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.BestMove(ctx, pos, nil)
	if err == nil {
		t.Fatal("上游 ctx 超时应当返回错误")
	}
	msg := err.Error()
	if !strings.Contains(msg, "超时") {
		t.Fatalf("应识别为超时，实际: %v", err)
	}
	if strings.Contains(msg, "200000") {
		t.Errorf("提示报了配置值 200000ms，实际生效的是上游约 300ms —— 用户照此调大设置不会有任何效果: %v", err)
	}
	if !strings.Contains(msg, "300") && !strings.Contains(msg, "299") {
		t.Errorf("提示应给出上游的实际量级（约 300ms），实际: %v", err)
	}
}

// TestTimeoutHintWhenNoDeadline ctx 无 deadline 时不报数字，但必须点明超时。
func TestTimeoutHintWhenNoDeadline(t *testing.T) {
	err := withTimeoutHint(context.DeadlineExceeded, 0)
	if !strings.Contains(err.Error(), "超时") {
		t.Errorf("应点明超时，实际: %v", err)
	}
}

// TestNonTimeoutErrorUntouched 非超时错误不应被改写（保留原始信息）。
func TestNonTimeoutErrorUntouched(t *testing.T) {
	orig := errStr("HTTP 401: unauthorized")
	got := withTimeoutHint(orig, 1000)
	if got.Error() != orig.Error() {
		t.Errorf("非超时错误不该被改写: 原 %q 现 %q", orig, got)
	}
}

type errStr string

func (e errStr) Error() string { return string(e) }
