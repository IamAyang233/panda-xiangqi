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
	// cleared 是前端判「通关成功 / 挑战失败」的唯一依据（它拿不到 stars）。
	// 漏发或发错会让赢棋显示成失败 —— 端到端黑盒实测踩过这个坑。
	if got, ok := goMsg["cleared"].(bool); !ok || !got {
		t.Fatalf("通关标志 cleared 应为 true，实际 %v", goMsg["cleared"])
	}
	if _, has := goMsg["stars"]; has {
		t.Fatalf("自摆残局不应评星，实际 stars=%v", goMsg["stars"])
	}
}

// TestCustomPuzzleGoalDrawNotClearedByWin 目标为「求和」的自摆残局，玩家把对方将死
// （而不是走成和棋）不算通关 —— 验证 cleared 会如实为 false，而不是「只要赢了就算过」。
func TestCustomPuzzleGoalDrawNotClearedByWin(t *testing.T) {
	p := customFixture()
	p.Goal = "draw"
	conn := &recConn{}
	sess := session.NewSession(session.ModePuzzle, game.Red, 4, llm.DefaultConfig(), p, engine.NewManager(""))
	sess.Join(conn)
	defer sess.Close()
	waitPlayerTurn(t, sess, game.Red)
	if err := sess.ApplyPlayerMove("a1", "d1"); err != nil {
		t.Fatalf("着法应被接受: %v", err)
	}
	goMsg, ok := conn.lastGameOver()
	if !ok {
		t.Fatal("未收到 game_over")
	}
	if got, _ := goMsg["result"].(string); got != game.ResultRedWin {
		t.Fatalf("局面本身是红胜，实际 %v", got)
	}
	if got, ok := goMsg["cleared"].(bool); !ok || got {
		t.Fatalf("目标为求和时，将死对方不应算通关，cleared 应为 false，实际 %v", goMsg["cleared"])
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

// TestCustomPuzzleFirstSideRedWithBlackPlayer 「红先、我执黑」这种组合：
// 服务端下发的 puzzle.firstSide 必须是 red（局面轮走方），而不是拿 playerSide 反推；
// 并且开局该由 AI（红方）先动，等它应着后轮到玩家。
//
// 对局屏的「我执黑 · 红先」就靠这个字段显示，标错会让玩家以为是自己先走。
func TestCustomPuzzleFirstSideRedWithBlackPlayer(t *testing.T) {
	p := customFixture() // FEN 是红先（… w）
	p.PlayerSide = "black"
	conn := &recConn{}
	sess := session.NewSession(session.ModePuzzle, game.Black, 4, llm.DefaultConfig(), p, engine.NewManager(""))
	// 与 api.handleCreateGame 的调用顺序一致：建局后先拉一次「该 AI 先走吗」，
	// 再让客户端 Join（Join 会下发当前完整 state）。少这一步 AI 永远不会动。
	sess.StartIfAIToMove()
	sess.Join(conn)
	defer sess.Close()

	// 首个 state 里的两个字段必须各说各话
	conn.mu.Lock()
	var pzInfo map[string]any
	for _, m := range conn.msgs {
		if msg, ok := m.(map[string]any); ok && msg["type"] == "state" {
			if sub, ok := msg["puzzle"].(map[string]any); ok {
				pzInfo = sub
			}
		}
	}
	conn.mu.Unlock()
	if pzInfo == nil {
		t.Fatal("首个 state 未带 puzzle 子对象")
	}
	if got := pzInfo["firstSide"]; got != "red" {
		t.Errorf("firstSide 应为 red（FEN 红先），实际 %v", got)
	}
	if got := pzInfo["playerSide"]; got != "black" {
		t.Errorf("playerSide 应为 black，实际 %v", got)
	}

	// AI（红）先动：等它走完，局面应轮到黑方（玩家）
	waitPlayerTurn(t, sess, game.Black)
	if _, ok := conn.lastMoveMsg(); !ok {
		t.Error("AI 应先应着一手（着法列表为空）")
	}
}
