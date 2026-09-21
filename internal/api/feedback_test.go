package api

// /api/feedback 的失败透传测试（2026-09-21 审查第六轮）。
//
// 原来这里把响应里的 `ok` **无条件**改写成 true，于是远端返回 HTTP 200 +
// `ok:false`（标题重复 / 限流 / 内容校验不过）时，前端显示「✓ 反馈已提交」，
// 而那条反馈根本没入库 —— 静默丢数据，用户也不会重试。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func postFeedback(t *testing.T, srv string, body string) map[string]any {
	t.Helper()
	resp, err := http.DefaultClient.Post(srv+"/api/feedback", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("响应解析失败: %v", err)
	}
	return out
}

func TestFeedbackReportsRemoteFailure(t *testing.T) {
	panda := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":false,"message":"重复提交"}`))
	}))
	defer panda.Close()

	ts, _ := testServer(t, panda.URL, "tok")
	defer ts.Close()

	out := postFeedback(t, ts.URL, `{"category":"功能异常","title":"t","description":"d"}`)
	if ok, _ := out["ok"].(bool); ok {
		t.Errorf("远端 ok:false 必须原样透传（否则用户看到「已提交」而反馈其实被拒）: %v", out)
	}
	if out["message"] != "重复提交" {
		t.Errorf("远端的原因要带给用户，实际 %v", out["message"])
	}
}

// TestFeedbackDefaultsOkWhenRemoteSilent 反面对照：远端没给 ok 字段时仍按成功处理
// （既有行为，别被上一条修复顺手改坏）。
func TestFeedbackDefaultsOkWhenRemoteSilent(t *testing.T) {
	panda := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"message":"saved"}`))
	}))
	defer panda.Close()

	ts, _ := testServer(t, panda.URL, "tok")
	defer ts.Close()

	out := postFeedback(t, ts.URL, `{"category":"功能异常","title":"t"}`)
	if ok, _ := out["ok"].(bool); !ok {
		t.Errorf("远端未给 ok 字段时应默认成功: %v", out)
	}
}

// TestFeedbackEmptyTitleRejectedLocally 标题为空时不该发请求（既有行为）。
func TestFeedbackEmptyTitleRejectedLocally(t *testing.T) {
	var hits int
	panda := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer panda.Close()

	ts, _ := testServer(t, panda.URL, "tok")
	defer ts.Close()

	out := postFeedback(t, ts.URL, `{"category":"功能异常","title":"   "}`)
	if ok, _ := out["ok"].(bool); ok {
		t.Errorf("空标题应被拒: %v", out)
	}
	if hits != 0 {
		t.Errorf("空标题不该转发到远端，实际转发 %d 次", hits)
	}
}
