package session_test

import (
	"testing"

	"github.com/IamAyang233/panda-xiangqi/internal/engine"
	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/llm"
	"github.com/IamAyang233/panda-xiangqi/internal/puzzle"
	"github.com/IamAyang233/panda-xiangqi/internal/session"
)

// fixturePuzzle 用内联数据构造测试用残局，避免测试与线上题库（可被整体替换）耦合。
func fixturePuzzle(id, fen, playerSide, goal string, par int, sol []string) *puzzle.Puzzle {
	return &puzzle.Puzzle{
		ID:         id,
		Name:       id,
		Source:     "测试fixture",
		Difficulty: "入门",
		PlayerSide: playerSide,
		Goal:       goal,
		FEN:        fen,
		ParMoves:   par,
		Solution:   sol,
		Tags:       []string{"测试"},
		Verified:   true,
	}
}

// runPuzzleMoves 按序走玩家着法（黑方由引擎/正解应着），返回 game_over.stars。
func runPuzzleMoves(t *testing.T, p *puzzle.Puzzle, moves []string, useHint bool) any {
	t.Helper()
	humanSide := game.Red
	if p.PlayerSide == "black" {
		humanSide = game.Black
	}
	conn := &recConn{}
	sess := session.NewSession(session.ModePuzzle, humanSide, 4, llm.DefaultConfig(), p, engine.NewManager(""))
	sess.Join(conn)
	defer sess.Close()
	if useHint {
		if err := sess.Hint(); err != nil {
			t.Fatalf("hint: %v", err)
		}
	}
	for _, m := range moves {
		waitPlayerTurn(t, sess, humanSide)
		if err := sess.ApplyPlayerMove(m[:2], m[2:]); err != nil {
			t.Fatalf("move %s: %v", m, err)
		}
		if go_, ok := conn.lastGameOver(); ok {
			return go_["stars"]
		}
	}
	return "NO_GAMEOVER"
}

// 正解一步杀 → 3 星（可记录/覆盖）
func TestPuzzleStarPerfectLine(t *testing.T) {
	p := fixturePuzzle("pzl-001", "4k4/3R1R3/9/9/9/9/9/9/9/R2K5 w - - 0 1", "red", "win", 1, []string{"a0a9"})
	if got := runPuzzleMoves(t, p, []string{"a0a9"}, false); got != 3 {
		t.Fatalf("正解一步杀应 3 星, 实际 %v", got)
	}
}

// 偏离正解但最终将死 → 1 星（原来 stars=nil 不记录）
func TestPuzzleStarDeviateStillWins(t *testing.T) {
	p := fixturePuzzle("pzl-001", "4k4/3R1R3/9/9/9/9/9/9/9/R2K5 w - - 0 1", "red", "win", 1, []string{"a0a9"})
	// d8e8 偏离正解（a0a9），黑方引擎应着，若玩家仍能成杀则应有星而非 nil
	got := runPuzzleMoves(t, p, []string{"d8e8", "a0a9"}, false)
	if got == nil {
		t.Fatalf("偏离正解但赢棋应发星（≥1）, 实际 stars=nil")
	}
	n, _ := got.(int)
	if n < 1 || n > 3 {
		t.Fatalf("偏离正解赢棋应 1-3 星, 实际 %v", got)
	}
}
