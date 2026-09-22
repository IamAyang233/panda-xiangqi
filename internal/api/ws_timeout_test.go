package api

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 空闲（无心跳）连接必须被服务端按 wsReadTimeout 回收：
// 否则锁屏/断网产生的 TCP 半开会一直占着 goroutine，缓慢泄漏。
func TestWSIdleConnectionReaped(t *testing.T) {
	old := wsReadTimeout
	wsReadTimeout = 400 * time.Millisecond
	defer func() { wsReadTimeout = old }()

	ts, _ := testServer(t, "", "")
	defer ts.Close()

	resp, err := ts.Client().Post(ts.URL+"/api/games", "application/json",
		strings.NewReader(`{"mode":"local_2p"}`))
	if err != nil {
		t.Fatal(err)
	}
	var created struct {
		GameID string `json:"gameId"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if created.GameID == "" {
		t.Fatal("未拿到 gameId")
	}

	conn, br, err := wsHandshake(t, ts, created.GameID)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// 首条 state
	if _, err := tryReadWSFrame(br); err != nil {
		t.Fatalf("未读到首条 state: %v", err)
	}

	start := time.Now()
	for {
		if _, err := tryReadWSFrame(br); err != nil {
			// ✅ 服务端主动断开（读超时）
			if d := time.Since(start); d > 3*time.Second {
				t.Fatalf("空闲连接回收过晚: %v", d)
			}
			return
		}
		if time.Since(start) > 3*time.Second {
			t.Fatal("空闲连接 3s 内未被服务端回收（goroutine 泄漏）")
		}
	}
}

// 有心跳的连接必须跨过 wsReadTimeout 仍然存活（读超时按消息续期的回归测试）。
func TestWSHeartbeatKeepsAlive(t *testing.T) {
	old := wsReadTimeout
	wsReadTimeout = 400 * time.Millisecond
	defer func() { wsReadTimeout = old }()

	ts, _ := testServer(t, "", "")
	defer ts.Close()

	resp, err := ts.Client().Post(ts.URL+"/api/games", "application/json",
		strings.NewReader(`{"mode":"local_2p"}`))
	if err != nil {
		t.Fatal(err)
	}
	var created struct {
		GameID string `json:"gameId"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()

	conn, br, err := wsHandshake(t, ts, created.GameID)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// 客户端读超时给足余量
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))

	if _, err := tryReadWSFrame(br); err != nil {
		t.Fatalf("未读到首条 state: %v", err)
	}

	// 每 150ms 一次 ping，持续 1.2s = 3 倍于读超时 → 连接必须仍然可用
	pongs := 0
	deadline := time.Now().Add(1200 * time.Millisecond)
	for time.Now().Before(deadline) {
		writeWSFrame(t, conn, []byte(`{"type":"ping"}`))
		msg, err := tryReadWSFrame(br)
		if err != nil {
			t.Fatalf("有心跳的连接被服务端断开（读超时未续期）: %v", err)
		}
		if msg["type"] == "pong" {
			pongs++
		}
		time.Sleep(100 * time.Millisecond)
	}
	if pongs == 0 {
		t.Fatal("未收到任何 pong")
	}
}

// wsHandshake 完成原生 TCP + WS 升级，返回连接与读缓冲。
func wsHandshake(t *testing.T, ts *httptest.Server, gameID string) (net.Conn, *bufio.Reader, error) {
	t.Helper()
	host := strings.TrimPrefix(ts.URL, "http://")
	conn, err := net.DialTimeout("tcp", host, 2*time.Second)
	if err != nil {
		return nil, nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	req := "GET /api/ws?gameId=" + gameID + " HTTP/1.1\r\n" +
		"Host: " + host + "\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		conn.Close()
		return nil, nil, err
	}
	br := bufio.NewReader(conn)
	status, err := br.ReadString('\n')
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	if !strings.Contains(status, "101") {
		conn.Close()
		return nil, nil, errUpgrade
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			conn.Close()
			return nil, nil, err
		}
		if line == "\r\n" {
			break
		}
	}
	_ = conn.SetDeadline(time.Time{}) // 清掉握手用期限，后续由用例自控
	return conn, br, nil
}

// tryReadWSFrame 非致命版读帧：EOF/错误原样返回（服务端主动断开时即 EOF）。
func tryReadWSFrame(br *bufio.Reader) (map[string]any, error) {
	h := make([]byte, 2)
	if _, err := io.ReadFull(br, h); err != nil {
		return nil, err
	}
	length := int(h[1] & 0x7F)
	if length == 126 {
		ext := make([]byte, 2)
		if _, err := io.ReadFull(br, ext); err != nil {
			return nil, err
		}
		length = int(ext[0])<<8 | int(ext[1])
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(br, payload); err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(payload, &m); err != nil {
		return nil, err
	}
	return m, nil
}
