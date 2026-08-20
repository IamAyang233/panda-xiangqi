package session_test

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IamAyang233/panda-xiangqi/internal/engine"
	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/llm"
	"github.com/IamAyang233/panda-xiangqi/internal/puzzle"
	"github.com/IamAyang233/panda-xiangqi/internal/session"
)

// recConn 记录所有下发消息（用于断言 game_over）。
type recConn struct {
	mu   sync.Mutex
	msgs []any
}

func (c *recConn) SendJSON(v any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgs = append(c.msgs, v)
}
func (c *recConn) Close() {}

func (c *recConn) lastGameOver() (map[string]any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.msgs) - 1; i >= 0; i-- {
		if m, ok := c.msgs[i].(map[string]any); ok {
			if t, _ := m["type"].(string); t == "game_over" {
				return m, true
			}
		}
	}
	return nil, false
}

func humanTurn(fen string, humanSide int) bool {
	if humanSide == game.Red {
		return strings.Contains(fen, " w ")
	}
	return strings.Contains(fen, " b ")
}

func waitPlayerTurn(t *testing.T, sess *session.Session, humanSide int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if humanTurn(sess.SnapshotFEN(), humanSide) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待玩家行棋超时, fen=%s", sess.SnapshotFEN())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func playPuzzle(t *testing.T, p *puzzle.Puzzle, conn *recConn) {
	t.Helper()
	humanSide := game.Red
	if p.PlayerSide == "black" {
		humanSide = game.Black
	}
	sess := session.NewSession(session.ModePuzzle, humanSide, 4, llm.DefaultConfig(), p, engine.NewManager(""))
	sess.Join(conn)
	defer sess.Close()

	for i := 0; i < len(p.Solution); i += 2 {
		waitPlayerTurn(t, sess, humanSide)
		uci := p.Solution[i]
		if err := sess.ApplyPlayerMove(uci[:2], uci[2:]); err != nil {
			t.Fatalf("第 %d 步玩家着法 %s 失败: %v", i, uci, err)
		}
		if _, over := conn.lastGameOver(); over {
			break
		}
	}
}

// lastMoveMsg 返回最后一条 move 类型消息（含残局 step）。
func (c *recConn) lastMoveMsg() (map[string]any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.msgs) - 1; i >= 0; i-- {
		if m, ok := c.msgs[i].(map[string]any); ok {
			if t, _ := m["type"].(string); t == "move" {
				return m, true
			}
		}
	}
	return nil, false
}

func TestPuzzleMoveCarriesStep(t *testing.T) {
	p := fixturePuzzle("pzl-042", "3k5/3r5/9/9/9/9/9/9/4K4/3R5 b - - 0 1", "black", "win", 3,
		[]string{"d8d0", "e1f1", "d0d1", "f1f0", "d1e1"}) // 黑先防守关
	conn := &recConn{}
	sess := session.NewSession(session.ModePuzzle, game.Black, 4, llm.DefaultConfig(), p, engine.NewManager(""))
	sess.Join(conn)
	defer sess.Close()

	waitPlayerTurn(t, sess, game.Black)
	// 黑先第 1 步正解 d8d0
	if err := sess.ApplyPlayerMove("d8", "d0"); err != nil {
		t.Fatalf("玩家第 1 步失败: %v", err)
	}
	m, ok := conn.lastMoveMsg()
	if !ok {
		t.Fatal("未收到 move 消息")
	}
	if got, _ := m["step"].(int); got != 1 {
		t.Fatalf("玩家走 1 步后期望 move.step=1, 实际 %v", m["step"])
	}
}

// TestPuzzleStepCountsWrongMoves 走错路线时"已走步数"仍应 +1（按玩家实际落子计数，而非正解进度）。
func TestPuzzleStepCountsWrongMoves(t *testing.T) {
	p := fixturePuzzle("pzl-042", "3k5/3r5/9/9/9/9/9/9/4K4/3R5 b - - 0 1", "black", "win", 3,
		[]string{"d8d0", "e1f1", "d0d1", "f1f0", "d1e1"})
	conn := &recConn{}
	sess := session.NewSession(session.ModePuzzle, game.Black, 4, llm.DefaultConfig(), p, engine.NewManager(""))
	sess.Join(conn)
	defer sess.Close()

	waitPlayerTurn(t, sess, game.Black)
	// 走错误着法（黑车 d8d7，正解为 d8d0）→ 仍应计 1 步
	if err := sess.ApplyPlayerMove("d8", "d7"); err != nil {
		t.Fatalf("错误着法应可下: %v", err)
	}
	m, ok := conn.lastMoveMsg()
	if !ok {
		t.Fatal("未收到 move 消息")
	}
	if got, _ := m["step"].(int); got != 1 {
		t.Fatalf("走错 1 步后期望 move.step=1, 实际 %v", m["step"])
	}
	if got, _ := m["type"].(string); got != "move" {
		t.Fatalf("期望 move 类型, 实际 %v", m["type"])
	}
}

func TestPuzzleSessionRedWin(t *testing.T) {
	p := fixturePuzzle("pzl-037", "9/3k5/9/9/9/9/9/R8/R8/4K4 w - - 0 1", "red", "win", 2,
		[]string{"a1e1", "d8d9", "a2d2"})
	conn := &recConn{}
	playPuzzle(t, p, conn)
	goMsg, ok := conn.lastGameOver()
	if !ok {
		t.Fatal("未收到 game_over")
	}
	if got := goMsg["result"]; got != game.ResultRedWin {
		t.Fatalf("期望 red_win, 实际 %v", got)
	}
	if got := goMsg["reason"]; got != game.ReasonCheckmate {
		t.Fatalf("期望 checkmate, 实际 %v", got)
	}
}

func TestPuzzleSessionBlackWin(t *testing.T) {
	p := fixturePuzzle("pzl-039", "4k4/r8/r8/9/9/9/9/9/3K5/9 b - - 0 1", "black", "win", 2,
		[]string{"a7e7", "d1d2", "a8d8"})
	conn := &recConn{}
	playPuzzle(t, p, conn)
	goMsg, ok := conn.lastGameOver()
	if !ok {
		t.Fatal("未收到 game_over（黑先胜关应可玩到终局）")
	}
	if got := goMsg["result"]; got != game.ResultBlackWin {
		t.Fatalf("期望 black_win（玩家执黑将死对方）, 实际 %v", got)
	}
	if got := goMsg["reason"]; got != game.ReasonCheckmate {
		t.Fatalf("期望 checkmate, 实际 %v", got)
	}
}

func TestPuzzleSessionDraw(t *testing.T) {
	p := fixturePuzzle("pzl-040", "4k4/9/9/9/9/9/9/4R4/9/3K5 w - - 0 1", "red", "draw", 6,
		[]string{"e2c2", "e9f9", "c2c3", "f9e9", "c3c2", "e9f9", "c2c3", "f9e9", "c3c2", "e9f9", "c2c3", "f9e9"})
	conn := &recConn{}
	playPuzzle(t, p, conn)
	goMsg, ok := conn.lastGameOver()
	if !ok {
		t.Fatal("未收到 game_over（和棋关应可玩到终局）")
	}
	if got := goMsg["result"]; got != game.ResultDraw {
		t.Fatalf("期望 draw, 实际 %v", got)
	}
	if got := goMsg["reason"]; got != game.ReasonRepetition {
		t.Fatalf("期望 repetition, 实际 %v", got)
	}
}
