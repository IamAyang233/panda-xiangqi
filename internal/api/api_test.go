package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/IamAyang233/panda-xiangqi/internal/engine"
	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/puzzle"
	"github.com/IamAyang233/panda-xiangqi/internal/session"
)

func testServer(t *testing.T, updateAPI, feedbackToken string) (*httptest.Server, *session.Manager) {
	t.Helper()
	pz, err := puzzle.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	mgr := session.NewManager()
	srv := &Server{
		Sessions:      mgr,
		Engines:       engine.NewManager(""),
		Puzzles:       pz,
		UpdateAPI:     updateAPI,
		FeedbackToken: feedbackToken,
	}
	return httptest.NewServer(srv.Handler()), mgr
}

// TestRESTCreateAndPuzzles 验证 REST 创建对局与残局列表。
func TestRESTCreateAndPuzzles(t *testing.T) {
	ts, mgr := testServer(t, "", "")
	defer ts.Close()

	// 创建双人对局
	resp, err := ts.Client().Post(ts.URL+"/api/games", "application/json",
		strings.NewReader(`{"mode":"local_2p","side":"red","level":4}`))
	if err != nil {
		t.Fatal(err)
	}
	var created struct {
		GameID  string `json:"gameId"`
		YouSide string `json:"youSide"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if created.GameID == "" || created.YouSide != "red" {
		t.Fatalf("创建结果异常: %+v", created)
	}
	if _, ok := mgr.Get(created.GameID); !ok {
		t.Error("会话未注册到管理器")
	}

	// 残局列表
	resp, err = ts.Client().Get(ts.URL + "/api/puzzles")
	if err != nil {
		t.Fatal(err)
	}
	var list []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(list) == 0 {
		t.Error("残局列表为空")
	}
	for _, p := range list {
		if _, has := p["solution"]; has {
			t.Error("残局列表不应包含答案")
		}
	}
}

// TestWSHandshakeAndPlay 用手写 WS 客户端（掩码帧）完成握手并模拟一步棋。
func TestWSHandshakeAndPlay(t *testing.T) {
	ts, _ := testServer(t, "", "")
	defer ts.Close()

	// 创建残局会话（pzl-001：三车逼宫，一步杀 a0a9）
	resp, err := ts.Client().Post(ts.URL+"/api/games", "application/json",
		strings.NewReader(`{"mode":"puzzle","puzzleId":"pzl-001"}`))
	if err != nil {
		t.Fatal(err)
	}
	var created struct {
		GameID string `json:"gameId"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()

	// 原生 TCP 连接完成 WS 握手
	host := strings.TrimPrefix(ts.URL, "http://")
	conn, err := net.DialTimeout("tcp", host, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	req := "GET /api/ws?gameId=" + created.GameID + " HTTP/1.1\r\n" +
		"Host: " + host + "\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	status, err := br.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status, "101") {
		t.Fatalf("握手失败: %s", status)
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\r\n" {
			break
		}
	}

	// 读取首条 state 消息
	state := readWSFrame(t, br)
	if state["type"] != "state" || state["mode"] != "puzzle" {
		t.Fatalf("首条消息异常: %v", state)
	}

	// 走杀着 a0a9
	writeWSFrame(t, conn, []byte(`{"type":"move","from":"a0","to":"a9"}`))

	// 依次应收到 move 与 game_over
	var gameOver map[string]any
	for i := 0; i < 6; i++ {
		msg := readWSFrame(t, br)
		if msg["type"] == "game_over" {
			gameOver = msg
			break
		}
	}
	if gameOver == nil {
		t.Fatal("未收到 game_over")
	}
	if gameOver["result"] != game.ResultRedWin || gameOver["reason"] != game.ReasonCheckmate {
		t.Errorf("game_over 异常: %v", gameOver)
	}
	if stars, ok := gameOver["stars"].(float64); !ok || stars != 3 {
		t.Errorf("一步杀 + 无提示应得 3 星, got %v", gameOver["stars"])
	}
}

// readWSFrame 读一帧服务端（无掩码）文本并解析 JSON。
func readWSFrame(t *testing.T, br *bufio.Reader) map[string]any {
	t.Helper()
	h := make([]byte, 2)
	if _, err := ioReadFull(br, h); err != nil {
		t.Fatal(err)
	}
	length := int(h[1] & 0x7F)
	if length == 126 {
		ext := make([]byte, 2)
		if _, err := ioReadFull(br, ext); err != nil {
			t.Fatal(err)
		}
		length = int(ext[0])<<8 | int(ext[1])
	}
	payload := make([]byte, length)
	if _, err := ioReadFull(br, payload); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(payload, &m); err != nil {
		t.Fatalf("帧解析失败: %v: %s", err, payload)
	}
	return m
}

// writeWSFrame 写一帧客户端（掩码）文本。
func writeWSFrame(t *testing.T, conn net.Conn, payload []byte) {
	t.Helper()
	var buf bytes.Buffer
	buf.WriteByte(0x81) // FIN + text
	n := len(payload)
	switch {
	case n < 126:
		buf.WriteByte(0x80 | byte(n))
	default:
		buf.WriteByte(0x80 | 126)
		buf.WriteByte(byte(n >> 8))
		buf.WriteByte(byte(n))
	}
	mask := []byte{0x11, 0x22, 0x33, 0x44}
	buf.Write(mask)
	masked := make([]byte, n)
	for i, b := range payload {
		masked[i] = b ^ mask[i&3]
	}
	buf.Write(masked)
	if _, err := conn.Write(buf.Bytes()); err != nil {
		t.Fatal(err)
	}
}

// TestUpdateForward 验证 /api/update 转发 PanDa 并覆盖 current 为自身版本。
func TestUpdateForward(t *testing.T) {
	panda := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/app-update/panda-xiangqi" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"app":"panda-xiangqi","current":"9.9.9","releases":[
			{"version":"1.2.0","title":"新版","content":"修复若干","pub_date":"2026-08-16","download_url":""},
			{"version":"1.1.0","title":"旧版","content":"首个版本","pub_date":"2026-08-01"}]}`))
	}))
	defer panda.Close()

	ts, _ := testServer(t, panda.URL, "fnos-panda-xiangqi-feedback")
	defer ts.Close()
	srv := ts.URL

	resp, err := ts.Client().Get(srv + "/api/update")
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if out["current"] != AppVersion || out["selfVersion"] != AppVersion {
		t.Errorf("current 应覆盖为自身版本: %v", out["current"])
	}
	releases, _ := out["releases"].([]any)
	if len(releases) != 2 {
		t.Errorf("releases 应透传 2 条, got %d", len(releases))
	}
}

// TestFeedbackForward 验证 /api/feedback 转发 PanDa（Token 头 + 诊断日志）。
func TestFeedbackForward(t *testing.T) {
	var gotBody map[string]any
	var gotToken string
	panda := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/app-feedback/"+AppName {
			http.NotFound(w, r)
			return
		}
		gotToken = r.Header.Get("X-Feedback-Token")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"ok":true,"message":"saved"}`))
	}))
	defer panda.Close()

	ts, _ := testServer(t, panda.URL, "fnos-panda-xiangqi-feedback")
	defer ts.Close()
	base := ts.URL

	resp, err := ts.Client().Post(base+"/api/feedback", "application/json",
		strings.NewReader(`{"category":"功能异常","title":"测试反馈","description":"d","contact":"a@b.c","includeLogs":true,"clientInfo":"UA: test"}`))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if out["ok"] != true {
		t.Fatalf("反馈提交失败: %v", out)
	}
	if gotToken != "fnos-panda-xiangqi-feedback" {
		t.Errorf("缺少 X-Feedback-Token: %q", gotToken)
	}
	if gotBody["app"] != AppName || gotBody["version"] != AppVersion {
		t.Errorf("反馈应带应用标识与版本: %v", gotBody)
	}
	if logs, _ := gotBody["logs"].(string); !strings.Contains(logs, "诊断信息") {
		t.Error("includeLogs=true 应附带诊断日志")
	}
}

func ioReadFull(br *bufio.Reader, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := br.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}
