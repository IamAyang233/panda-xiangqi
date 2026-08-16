// Package api 提供 REST 与 WebSocket 接口及静态资源服务。
package api

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
)

// wsGUID RFC 6455 握手魔串。
const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// wsConn 单个 WebSocket 连接（服务端角色：发送不掩码，接收须解掩码）。
type wsConn struct {
	conn   net.Conn
	br     *bufio.Reader
	wmu    sync.Mutex
	closed bool
}

// upgradeWS 完成 HTTP → WebSocket 升级握手。
func upgradeWS(w http.ResponseWriter, r *http.Request) (*wsConn, error) {
	if r.Method != http.MethodGet {
		http.Error(w, "需要 GET", http.StatusMethodNotAllowed)
		return nil, errUpgrade
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		http.Error(w, "非 WebSocket 请求", http.StatusBadRequest)
		return nil, errUpgrade
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "连接不可接管", http.StatusInternalServerError)
		return nil, errUpgrade
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return nil, err
	}
	sum := sha1.Sum([]byte(key + wsGUID))
	accept := base64.StdEncoding.EncodeToString(sum[:])
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"
	if _, err := conn.Write([]byte(resp)); err != nil {
		conn.Close()
		return nil, err
	}
	return &wsConn{conn: conn, br: rw.Reader}, nil
}

// SendJSON 发送一条文本消息（JSON 编码）。
func (c *wsConn) SendJSON(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.closed {
		return
	}
	if err := c.writeFrame(0x1, data); err != nil {
		c.conn.Close()
		c.closed = true
	}
}

// Close 关闭连接（发送 close 帧后断开）。
func (c *wsConn) Close() {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.closed {
		return
	}
	_ = c.writeFrame(0x8, []byte{0x03, 0xe8})
	c.conn.Close()
	c.closed = true
}

// writeFrame 写一帧（服务端不掩码）。opcode: 0x1 文本, 0x8 关闭, 0xA 心跳回应。
func (c *wsConn) writeFrame(opcode byte, payload []byte) error {
	header := []byte{0x80 | opcode}
	n := len(payload)
	switch {
	case n < 126:
		header = append(header, byte(n))
	case n < 1<<16:
		header = append(header, 126, byte(n>>8), byte(n))
	default:
		header = append(header, 127,
			byte(n>>56), byte(n>>48), byte(n>>40), byte(n>>32),
			byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
	}
	if _, err := c.conn.Write(header); err != nil {
		return err
	}
	if n > 0 {
		if _, err := c.conn.Write(payload); err != nil {
			return err
		}
	}
	return nil
}

// ReadMessage 读取一条完整文本消息；连接关闭时返回 io.EOF。
// 处理 ping/close/分片（简易），掩码解包按 RFC 6455 §5.3。
func (c *wsConn) ReadMessage() ([]byte, error) {
	var assembled []byte
	for {
		fin, opcode, payload, err := c.readFrame()
		if err != nil {
			return nil, err
		}
		switch opcode {
		case 0x8: // close
			c.wmu.Lock()
			if !c.closed {
				_ = c.writeFrame(0x8, payload[:min(2, len(payload))])
				c.conn.Close()
				c.closed = true
			}
			c.wmu.Unlock()
			return nil, io.EOF
		case 0x9: // ping → pong
			c.wmu.Lock()
			if !c.closed {
				_ = c.writeFrame(0xA, payload)
			}
			c.wmu.Unlock()
		case 0xA: // pong 忽略
		case 0x1, 0x2, 0x0:
			assembled = append(assembled, payload...)
			if fin {
				return assembled, nil
			}
		default:
			return nil, io.EOF
		}
	}
}

func (c *wsConn) readFrame() (fin bool, opcode byte, payload []byte, err error) {
	var h [2]byte
	if _, err = io.ReadFull(c.br, h[:]); err != nil {
		return
	}
	fin = h[0]&0x80 != 0
	opcode = h[0] & 0x0F
	masked := h[1]&0x80 != 0
	length := uint64(h[1] & 0x7F)
	switch length {
	case 126:
		var ext [2]byte
		if _, err = io.ReadFull(c.br, ext[:]); err != nil {
			return
		}
		length = uint64(ext[0])<<8 | uint64(ext[1])
	case 127:
		var ext [8]byte
		if _, err = io.ReadFull(c.br, ext[:]); err != nil {
			return
		}
		length = uint64(ext[0])<<56 | uint64(ext[1])<<48 | uint64(ext[2])<<40 | uint64(ext[3])<<32 |
			uint64(ext[4])<<24 | uint64(ext[5])<<16 | uint64(ext[6])<<8 | uint64(ext[7])
	}
	if length > 1<<20 { // 单帧上限 1MB（消息熔断）
		err = errFrameTooLarge
		return
	}
	var mask [4]byte
	if masked {
		if _, err = io.ReadFull(c.br, mask[:]); err != nil {
			return
		}
	}
	payload = make([]byte, length)
	if _, err = io.ReadFull(c.br, payload); err != nil {
		return
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i&3]
		}
	}
	return
}

type wsError string

func (e wsError) Error() string { return string(e) }

var (
	errUpgrade       = wsError("websocket 升级失败")
	errFrameTooLarge = wsError("帧过大")
)

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// logWS 简单 WS 日志。
func logWS(format string, a ...any) {
	log.Printf("ws: "+format, a...)
}
