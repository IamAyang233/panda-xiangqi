package session_test

import (
	"testing"

	"github.com/IamAyang233/panda-xiangqi/internal/engine"
	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/llm"
	"github.com/IamAyang233/panda-xiangqi/internal/puzzle"
	"github.com/IamAyang233/panda-xiangqi/internal/session"
)

// 自摆残局：红车 a1 + 红帅 e0，黑将 d9（红先）。一步将死 = 车 a1→d1（d 线直取黑将）。
// 与 api 包用例同为「能安全进入对局路径」的合法局面，保证保存 → 挑战口径一致。
const customFEN = "3k5/9/9/9/9/9/9/9/R8/4K4 w - - 0 1"

// customFixture 构造一个自摆残局：Difficulty 为「自定义」、ParMoves=0、Solution 为空。
func customFixture() *puzzle.Puzzle {
	return &puzzle.Puzzle{
		ID:         "custom-test",
		Name:       "自摆残局",
		Source:     "自摆",
		Difficulty: "自定义",
		PlayerSide: "red",
		Goal:       "win",
		FEN:        customFEN,
		ParMoves:   0,
		Solution:   nil,
	}
}

// TestCustomPuzzleNoStars 自摆残局（ParMoves=0）通关时**不应给星**。
//
// 没有正解步数时 `playerMoves <= 0` 恒为假，会落到 default 一律给 2 星 ——
// 而自摆局面没有标准答案，凭步数评星没有意义，且前端会显示"★★☆ 挑战成功"这种误导。
func TestCustomPuzzleNoStars(t *testing.T) {
	p := customFixture()
	conn := &recConn{}
	// 自摆残局没有 Solution，playPuzzle 那种「按正解推进」的走法不适用，
	// 这里直接走那一步将死（车 a1→d1）。
	sess := session.NewSession(session.ModePuzzle, game.Red, 4, llm.DefaultConfig(), p, engine.NewManager(""))
	sess.Join(conn)
	defer sess.Close()
	waitPlayerTurn(t, sess, game.Red)
	if err := sess.ApplyPlayerMove("a1", "d1"); err != nil {
		t.Fatalf("一步将死的着法应可接受: %v", err)
	}
	goMsg, ok := conn.lastGameOver()
	if !ok {
		t.Fatalf("未收到 game_over（fen=%s）", sess.SnapshotFEN())
	}
	if got := goMsg["result"]; got != game.ResultRedWin {
		t.Fatalf("期望红胜（该局面一步将死），实际 %v", goMsg)
	}
	if _, has := goMsg["stars"]; has {
		t.Fatalf("自摆残局不应评星，实际 stars=%v", goMsg["stars"])
	}
}

// TestBuiltinPuzzleStillHasStars 内置残局（ParMoves>0）必须仍然评星：
// 上面那个修正不能把既有行为一起改掉。
func TestBuiltinPuzzleStillHasStars(t *testing.T) {
	p := fixturePuzzle("pzl-037", "9/3k5/9/9/9/9/9/R8/R8/4K4 w - - 0 1", "red", "win", 2,
		[]string{"a1e1", "d8d9", "a2d2"})
	conn := &recConn{}
	playPuzzle(t, p, conn)
	goMsg, ok := conn.lastGameOver()
	if !ok {
		t.Fatal("未收到 game_over")
	}
	stars, has := goMsg["stars"]
	if !has {
		t.Fatalf("内置残局应评星，实际 %v", goMsg)
	}
	if n, _ := stars.(int); n < 1 || n > 3 {
		t.Fatalf("星数应在 1~3，实际 %v", stars)
	}
}

// TestBuiltinPuzzleLevelMapping 内置五档难度的映射必须保持原样（含「大师」），
// 避免上面那处修正改动了既有行为。
// 档位映射的精确断言在同包的 puzzle_level_test.go（可直接访问 puzzleLevel）。
func TestBuiltinPuzzleLevelMapping(t *testing.T) {
	// 通过同一个残局、只改 Difficulty，断言不同难度下守方走子不会崩且能正常推进。
	// 映射表本身是私有函数，这里守住的是「五档都能跑通」这一回归底线。
	for _, diff := range []string{"入门", "初级", "中级", "高级", "大师"} {
		p := fixturePuzzle("pzl-"+diff, "9/3k5/9/9/9/9/9/R8/R8/4K4 w - - 0 1", "red", "win", 2,
			[]string{"a1e1", "d8d9", "a2d2"})
		p.Difficulty = diff
		conn := &recConn{}
		playPuzzle(t, p, conn)
		if _, ok := conn.lastGameOver(); !ok {
			t.Fatalf("难度 %s 未能正常结束对局", diff)
		}
	}
}

// TestCustomPuzzleNoDeviate 自摆残局没有正解，玩家走任何合法着法都不应触发
// 「偏离正解」事件（否则自摆局一走就被判错）。
func TestCustomPuzzleNoDeviate(t *testing.T) {
	p := customFixture()
	conn := &recConn{}
	sess := session.NewSession(session.ModePuzzle, game.Red, 4, llm.DefaultConfig(), p, engine.NewManager(""))
	sess.Join(conn)
	defer sess.Close()

	waitPlayerTurn(t, sess, game.Red)
	// 不走那步将死，改走一步普通的合法着法（车 a1→a5），确认不会被判「偏离正解」。
	if err := sess.ApplyPlayerMove("a1", "a5"); err != nil {
		t.Fatalf("自摆残局应接受任意合法着法: %v", err)
	}
	conn.mu.Lock()
	defer conn.mu.Unlock()
	for _, m := range conn.msgs {
		if msg, ok := m.(map[string]any); ok {
			if msg["type"] == "puzzle_event" && msg["event"] == "deviate" {
				t.Fatal("自摆残局不应触发偏离正解")
			}
		}
	}
}
