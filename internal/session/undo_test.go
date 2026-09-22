package session_test

// 悔棋的三个边界（2026-09-21 审查第六轮发现）：
//   1) 人执黑、AI 刚走完开局首手就悔棋 ⇒ 撤掉的是 AI 那一手，局面回到 AI 该走，
//      但没有人在算 ⇒ 对局永久卡死（Undo 是唯一不重新触发应着的回退路径）
//   2) 人机各走一手后悔棋 ⇒ 应回退整整一个回合、轮到玩家，且不该冒出新的 AI 应着
//   3) 残局偏离正解后悔棋 ⇒ 游标必须回到「已消费的正解手数」，否则玩家之后走对
//      也会被判「偏离正解」（静默误判）
//
// 这三条此前完全没有测试覆盖（grep Undo internal/session/*_test.go 为空）。

import (
	"testing"
	"time"

	"github.com/IamAyang233/panda-xiangqi/internal/engine"
	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/llm"
	"github.com/IamAyang233/panda-xiangqi/internal/session"
)

// firstLegalMove 返回当前局面的首个合法着法（from, to）。
func firstLegalMove(t *testing.T, sess *session.Session) (string, string) {
	t.Helper()
	p, err := game.ParseFEN(sess.SnapshotFEN())
	if err != nil {
		t.Fatalf("解析当前局面失败: %v", err)
	}
	mv := p.LegalMoves(p.Turn)
	if len(mv) == 0 {
		t.Fatal("当前局面无合法着法")
	}
	return game.SquareName(mv[0].From), game.SquareName(mv[0].To)
}

// deviateCountSince 统计第 n 条消息之后下发过多少条「偏离正解」事件。
func (c *recConn) deviateCountSince(n int) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	cnt := 0
	for _, v := range c.msgs[n:] {
		m, ok := v.(map[string]any)
		if !ok || m["type"] != "puzzle_event" {
			continue
		}
		if m["event"] == "deviate" {
			cnt++
		}
	}
	return cnt
}

// TestUndoAfterAIOpeningMoveReplaysAI 人执黑时，AI 走完开局首手后立刻悔棋。
//
// 此时 len(moves)==1，回退只手 ⇒ 撤掉的是 AI 的开局首手，局面回到红方（AI）该走。
// 修复前没有人重新触发应着，玩家此后任何落子都被拒「现在轮到对方行棋」。
func TestUndoAfterAIOpeningMoveReplaysAI(t *testing.T) {
	conn := &recConn{}
	sess := session.NewSession(session.ModeEngine, game.Black, 4, llm.DefaultConfig(), nil, engine.NewManager(""))
	sess.Join(conn)
	defer sess.Close()

	sess.StartIfAIToMove()              // api.go 创建会话后就是这样触发的
	waitPlayerTurn(t, sess, game.Black) // 等 AI（红）走完开局首手

	mark := conn.mark()
	if err := sess.Undo(); err != nil {
		t.Fatalf("悔棋失败: %v", err)
	}

	// 悔棋后轮次回到 AI：必须有新的应着把局面推回玩家。若卡死，这里会超时。
	waitPlayerTurn(t, sess, game.Black)

	if moves := conn.aiMovesSince(mark); len(moves) != 1 {
		t.Fatalf("悔棋后应恰好有 1 手 AI 应着，实际 %d 手: %+v\n"+
			"0 手 = 悔棋撤掉了 AI 的开局首手却没有重新触发应着（对局永久卡死）",
			len(moves), moves)
	}
}

// TestUndoAfterFullRoundReturnsTurnToHuman 人执红：玩家走一手、AI 应一手后悔棋，
// 应回退整整一个回合、轮到玩家，且不该再冒出 AI 应着（否则悔棋等于白悔）。
func TestUndoAfterFullRoundReturnsTurnToHuman(t *testing.T) {
	conn := &recConn{}
	sess := session.NewSession(session.ModeEngine, game.Red, 4, llm.DefaultConfig(), nil, engine.NewManager(""))
	sess.Join(conn)
	defer sess.Close()

	from, to := firstLegalMove(t, sess)
	if err := sess.ApplyPlayerMove(from, to); err != nil {
		t.Fatalf("玩家着法 %s%s 失败: %v", from, to, err)
	}
	waitPlayerTurn(t, sess, game.Red) // 等 AI 应着

	mark := conn.mark()
	if err := sess.Undo(); err != nil {
		t.Fatalf("悔棋失败: %v", err)
	}
	// 给「本不该出现的应着」充分的落地时间（应着有 600ms 保底时长）。
	time.Sleep(1200 * time.Millisecond)

	if moves := conn.aiMovesSince(mark); len(moves) != 0 {
		t.Errorf("一个回合后悔棋应轮到玩家，不该有 AI 应着，实际 %+v", moves)
	}
	if !humanTurn(sess.SnapshotFEN(), game.Red) {
		t.Errorf("悔棋后应轮到玩家（红），实际 FEN=%s", sess.SnapshotFEN())
	}
	if sess.IsBusy() {
		t.Error("悔棋后不应处于「思考中」—— 否则玩家走子会被拒")
	}
	// 悔棋后玩家应能正常落子（卡死时这里会返回「现在轮到对方行棋」）。
	from2, to2 := firstLegalMove(t, sess)
	if err := sess.ApplyPlayerMove(from2, to2); err != nil {
		t.Errorf("悔棋后玩家应能继续落子，实际被拒: %v", err)
	}
}

// TestUndoAfterDeviationRestoresSolutionCursor 残局：走对 → 走错 → 悔棋 → 再走对。
//
// 游标必须按「实际消费的正解手数」回退。按「撤了几手」硬减的话，游标会落到局面
// 之前，玩家重走正确着法反而被判「偏离正解」。
func TestUndoAfterDeviationRestoresSolutionCursor(t *testing.T) {
	st := loadPuzzles(t)
	p, ok := st.Get("cj-竹子涨棋-0003")
	if !ok {
		t.Skip("测试用关卡不在数据里")
	}
	if p.PlayerSide != "red" || len(p.Solution) < 5 {
		t.Fatalf("测试前提变了：playerSide=%q 解长=%d", p.PlayerSide, len(p.Solution))
	}

	conn := &recConn{}
	sess := session.NewSession(session.ModePuzzle, game.Red, 4, llm.DefaultConfig(), p, engine.NewManager(""))
	sess.Join(conn)
	defer sess.Close()

	// 走对第 1 手（玩家），等守方应正解第 2 手 ⇒ 已消费 2 手正解。
	if err := sess.ApplyPlayerMove(p.Solution[0][:2], p.Solution[0][2:]); err != nil {
		t.Fatalf("玩家着法 %s 失败: %v", p.Solution[0], err)
	}
	waitPlayerTurn(t, sess, game.Red)

	// 挑一个「合法但不是 sol[2]」的着法，制造偏离。
	want := p.Solution[2]
	cur, err := game.ParseFEN(sess.SnapshotFEN())
	if err != nil {
		t.Fatalf("解析局面失败: %v", err)
	}
	var wrong string
	for _, m := range cur.LegalMoves(cur.Turn) {
		if m.String() != want {
			wrong = m.String()
			break
		}
	}
	if wrong == "" {
		t.Skip("该局面除正解外没有其它合法着法，无法构造偏离")
	}
	if err := sess.ApplyPlayerMove(wrong[:2], wrong[2:]); err != nil {
		t.Fatalf("偏离着法 %s 失败: %v", wrong, err)
	}
	if !conn.hasEvent("deviate") {
		t.Fatal("走错时应下发 deviate 事件（构造失真，测试无判别力）")
	}
	waitPlayerTurn(t, sess, game.Red) // 等守方自由应着

	// 悔棋回退「守方自由应着 + 玩家的偏离手」⇒ 应回到 sol[0]/sol[1] 之后、轮玩家。
	if err := sess.Undo(); err != nil {
		t.Fatalf("悔棋失败: %v", err)
	}
	if sess.IsBusy() {
		t.Fatal("悔棋后不应处于「思考中」")
	}

	// 重走正解第 3 手：修复前游标被硬减到 0，这里会被误判「偏离正解」。
	mark := conn.mark()
	if err := sess.ApplyPlayerMove(want[:2], want[2:]); err != nil {
		t.Fatalf("重走正解 %s 失败: %v", want, err)
	}
	if n := conn.deviateCountSince(mark); n != 0 {
		t.Errorf("悔棋后重走正解 %s 被判「偏离正解」%d 次 —— 游标回退错误（被按手数硬减而非按消费的正解手数重算）",
			want, n)
	}
}
