package api

// WebSocket 帧层的加固测试（2026-09-21 审查第六轮）。
//
// 两处都只靠 readFrame 的旧实现挡不住，而且都不需要真实 HTTP 连接就能测：
// 直接拿 net.Pipe 喂原始帧给 wsConn.ReadMessage。

import (
	"bufio"
	"bytes"
	"math/rand"
	"net"
	"testing"
)

// buildFrame 组装一个客户端帧（opcode / payload / 是否掩码 / 是否 FIN）。
func buildFrame(opcode byte, payload []byte, masked, fin bool) []byte {
	var buf bytes.Buffer
	b0 := opcode & 0x0F
	if fin {
		b0 |= 0x80
	}
	buf.WriteByte(b0)

	n := len(payload)
	var maskBit byte
	if masked {
		maskBit = 0x80
	}
	switch {
	case n < 126:
		buf.WriteByte(maskBit | byte(n))
	case n < 1<<16:
		buf.WriteByte(maskBit | 126)
		buf.WriteByte(byte(n >> 8))
		buf.WriteByte(byte(n))
	default:
		buf.WriteByte(maskBit | 127)
		for shift := 56; shift >= 0; shift -= 8 {
			buf.WriteByte(byte(n >> uint(shift)))
		}
	}

	if !masked {
		buf.Write(payload)
		return buf.Bytes()
	}
	var mask [4]byte
	rand.Read(mask[:])
	buf.Write(mask[:])
	for i, b := range payload {
		buf.WriteByte(b ^ mask[i&3])
	}
	return buf.Bytes()
}

// newPipeWSConn 建一个只用于读的 wsConn（写路径不参与这两个用例）。
func newPipeWSConn(t *testing.T) (c *wsConn, peer net.Conn) {
	t.Helper()
	srv, cli := net.Pipe()
	t.Cleanup(func() { srv.Close(); cli.Close() })
	return &wsConn{conn: srv, br: bufio.NewReader(srv)}, cli
}

// writeAsync 在后台写帧（net.Pipe 是同步的，必须与读并发）。
func writeAsync(t *testing.T, peer net.Conn, frame []byte) {
	t.Helper()
	go func() { _, _ = peer.Write(frame) }()
}

// TestReadMessageAcceptsMaskedFrame 正常路径：掩码帧照常解包。
// 加固改动不能把合规客户端一起挡掉 —— 这条是它的反面对照。
func TestReadMessageAcceptsMaskedFrame(t *testing.T) {
	c, peer := newPipeWSConn(t)
	want := []byte(`{"type":"ping"}`)
	writeAsync(t, peer, buildFrame(0x1, want, true, true))

	got, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("合规的掩码帧被拒: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("解包结果不符: %q", got)
	}
}

// TestReadMessageRejectsUnmaskedFrame RFC 6455 §5.1 要求客户端帧必须掩码。
//
// 旧实现是「没掩码就当掩码全零、直接用原始 payload」——静默接受。不合规，也放过
// 了「经由行为不一致的中间层」做帧缓存投毒那类攻击。
func TestReadMessageRejectsUnmaskedFrame(t *testing.T) {
	c, peer := newPipeWSConn(t)
	writeAsync(t, peer, buildFrame(0x1, []byte(`{"type":"ping"}`), false, true))

	if _, err := c.ReadMessage(); err != errUnmaskedFrame {
		t.Fatalf("未掩码帧应被拒（errUnmaskedFrame），实际 err=%v", err)
	}
}

// TestReadMessageRejectsOversizedFragmentedMessage 分片拼接的总长必须有上限。
//
// 只限单帧长度挡不住分片：两帧各 700KB 都合法，拼起来 1.4MB 就会把拼接缓冲撑爆。
// 旧实现只判单帧 ⇒ 客户端可以连发 N 个 1MB 续帧让内存无界增长。
func TestReadMessageRejectsOversizedFragmentedMessage(t *testing.T) {
	const chunk = 700 << 10 // 每帧都 < maxWSMessage
	part := bytes.Repeat([]byte{'a'}, chunk)

	c, peer := newPipeWSConn(t)
	go func() {
		_, _ = peer.Write(buildFrame(0x1, part, true, false)) // 首帧，未结束
		_, _ = peer.Write(buildFrame(0x0, part, true, true))  // 续帧，结束
	}()

	if _, err := c.ReadMessage(); err != errFrameTooLarge {
		t.Fatalf("分片总长超过上限应报 errFrameTooLarge，实际 err=%v", err)
	}
}

// TestReadMessageAcceptsLargeSingleFrame 单帧在限额内仍要放行（上一条的反面对照）。
func TestReadMessageAcceptsLargeSingleFrame(t *testing.T) {
	body := bytes.Repeat([]byte{'b'}, 300<<10)
	c, peer := newPipeWSConn(t)
	writeAsync(t, peer, buildFrame(0x1, body, true, true))

	got, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("限额内的帧被拒: %v", err)
	}
	if len(got) != len(body) {
		t.Errorf("长度不符: %d", len(got))
	}
}
