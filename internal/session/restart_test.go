package session_test

// 残局模式的两个「静默错判」关卡（2026-09-21 审查第五轮发现）：
//   1) 重开不取消正在进行的应着 ⇒ 旧局面的着法落到新局面上；黑先关还会直接卡死
//   2) 关卡正解走不出来时仍然推进游标 ⇒ 玩家走对也被判「偏离正解」
// 两者都只在残局模式下出现，且都不会崩溃、只会静默给出错误结论 —— 所以要用测试钉住。

import (
	"testing"
	"time"

	"github.com/IamAyang233/panda-xiangqi/internal/engine"
	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/llm"
	"github.com/IamAyang233/panda-xiangqi/internal/puzzle"
	"github.com/IamAyang233/panda-xiangqi/internal/session"
)

// ---------------------------------------------------------------- recConn 追加能力
//
// recConn 本体定义在 puzzle_flow_test.go（同包）。这里只加「按位置切片」的读取，
// 用来区分「重开之前」与「重开之后」下发的消息。

func (c *recConn) mark() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.msgs)
}

// moveMsg 一条走子消息。
type moveMsg struct {
	From, To string
	ByHuman  bool
}

// movesSince 返回第 n 条消息之后的全部走子消息。
//
// ⚠️ 走子消息的类型按模式分三种：`engine_move`（人机/残局）、`llm_move`、
// `move`（双人）。只认其中一种会得到一个「恒为空」的假断言（本轮踩过）。
func (c *recConn) movesSince(n int) []moveMsg {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []moveMsg
	for _, v := range c.msgs[n:] {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		switch m["type"] {
		case "engine_move", "llm_move", "move":
			f, _ := m["from"].(string)
			to, _ := m["to"].(string)
			byHum, _ := m["byHuman"].(bool)
			out = append(out, moveMsg{From: f, To: to, ByHuman: byHum})
		}
	}
	return out
}

// aiMovesSince 只保留非玩家（AI/守方）的走子。
func (c *recConn) aiMovesSince(n int) []moveMsg {
	var out []moveMsg
	for _, m := range c.movesSince(n) {
		if !m.ByHuman {
			out = append(out, m)
		}
	}
	return out
}

// hasEvent 报告是否下发过指定种类的 puzzle_event。
func (c *recConn) hasEvent(kind string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, v := range c.msgs {
		m, ok := v.(map[string]any)
		if !ok || m["type"] != "puzzle_event" {
			continue
		}
		if m["event"] == kind {
			return true
		}
	}
	return false
}

// lastState 返回最后一条全量状态消息。
func (c *recConn) lastState() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.msgs) - 1; i >= 0; i-- {
		if m, ok := c.msgs[i].(map[string]any); ok && m["type"] == "state" {
			return m
		}
	}
	return nil
}

// thinkingCount 已收到的引擎思考提示条数。
func (c *recConn) thinkingCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, v := range c.msgs {
		if m, ok := v.(map[string]any); ok && m["type"] == "engine_thinking" {
			n++
		}
	}
	return n
}

// waitThinking 等到引擎思考提示比 before 多一条。
//
// 用途：`engine_thinking` 是 aiReply **读完局面状态之后**才广播的，所以收到它
// 就能确定那次应着已经把 solUCI / pzStep / 局面读进本地变量了。此时再去重开，
// 才真正构成「在飞的应着」—— 否则可能撞上调度竞态：goroutine 如果在重开之后
// 才开始执行，它读到的就是新局面的状态，行为恰好正确，注入退化也抓不到。
func waitThinking(t *testing.T, c *recConn, before int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for c.thinkingCount() <= before {
		if time.Now().After(deadline) {
			t.Fatalf("等待引擎思考提示超时（已有 %d 条，期望 > %d）", c.thinkingCount(), before)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func loadPuzzles(t *testing.T) *puzzle.Store {
	t.Helper()
	st, err := puzzle.LoadDir("../puzzle/data")
	if err != nil {
		t.Skipf("未找到残局数据目录：%v", err)
	}
	return st
}

// ---------------------------------------------------------------- 1) 重开

func TestRestartOnBlackFirstPuzzleReplaysFromStart(t *testing.T) {
	st := loadPuzzles(t)
	// 这一关 playerSide=black 但 FEN 是红先 ⇒ 由 AI（红）先走，属于「黑先关」。
	const id = "cj-屠景明-实用残局(一)-0215"
	p, ok := st.Get(id)
	if !ok {
		t.Fatalf("内置残局里找不到 %s", id)
	}
	if p.PlayerSide != "black" {
		t.Fatalf("该关不再是黑先关（playerSide=%q）—— 测试前提变了，请另选一关", p.PlayerSide)
	}
	if len(p.Solution) < 4 {
		t.Fatalf("该关解太短（%d 手），不足以构造「思考中重开」", len(p.Solution))
	}

	conn := &recConn{}
	sess := session.NewSession(session.ModePuzzle, game.Black, 4, llm.DefaultConfig(), p, engine.NewManager(""))
	sess.Join(conn)
	defer sess.Close()

	sess.StartIfAIToMove()              // api.go 创建会话后就是这样触发的
	waitPlayerTurn(t, sess, game.Black) // 等 AI 走完第 1 手

	// 玩家走一手把 AI 带进思考（应着有 600ms 保底时长），**立刻**重开：
	// 那次在飞的应着算的是重开前的局面。
	if err := sess.ApplyPlayerMove(p.Solution[1][:2], p.Solution[1][2:]); err != nil {
		t.Fatalf("玩家着法 %s 失败: %v", p.Solution[1], err)
	}
	// 等这次应着读完局面状态（engine_thinking 在其之后广播）再重开 ——
	// 否则不构成「在飞的应着」：goroutine 若在重开之后才被调度，它读到的就是
	// 新局面的状态，行为恰好正确，连注入退化都抓不到（本轮踩过）。
	waitThinking(t, conn, 1)
	mark := conn.mark()
	if err := sess.Restart(); err != nil {
		t.Fatalf("重开失败: %v", err)
	}

	// 重开后回到初始局面（红先 ⇒ 仍归 AI 先走），必须在超时前走出**正解第一手**：
	//   - 重开没重新触发应着 ⇒ 局面卡死（玩家一动就被拒「现在轮到对方行棋」）
	//   - 没作废在飞的应着 ⇒ 落到新局面上的是 sol[2]，而不是 sol[0]
	waitPlayerTurn(t, sess, game.Black)

	moves := conn.aiMovesSince(mark)
	if len(moves) != 1 {
		t.Fatalf("重开后应恰好有 1 手 AI 应着，实际 %d 手: %+v（0 手=重开没触发应着；2 手=旧应着也被执行了）",
			len(moves), moves)
	}
	want := moveMsg{From: p.Solution[0][:2], To: p.Solution[0][2:]}
	if moves[0].From != want.From || moves[0].To != want.To {
		t.Errorf("重开后 AI 第一手应是正解 %s→%s，实际 %s→%s —— 重开前的残留着法落到了新局面上",
			want.From, want.To, moves[0].From, moves[0].To)
	}
}

// TestRestartOnRedFirstPuzzleIsSafe 固定普通（红先）关卡的重开行为：
// 重开后轮到玩家，不该有任何 AI 应着冒出来（旧应在飞的 goroutine 必须被作废）。
func TestRestartOnRedFirstPuzzleIsSafe(t *testing.T) {
	st := loadPuzzles(t)
	p, ok := st.Get("cj-竹子涨棋-0003")
	if !ok {
		t.Skip("测试用关卡不在数据里")
	}
	if p.PlayerSide != "red" || len(p.Solution) < 4 {
		t.Fatalf("测试前提变了：playerSide=%q 解长=%d", p.PlayerSide, len(p.Solution))
	}

	conn := &recConn{}
	sess := session.NewSession(session.ModePuzzle, game.Red, 4, llm.DefaultConfig(), p, engine.NewManager(""))
	sess.Join(conn)
	defer sess.Close()

	if err := sess.ApplyPlayerMove(p.Solution[0][:2], p.Solution[0][2:]); err != nil {
		t.Fatalf("玩家着法 %s 失败: %v", p.Solution[0], err)
	}
	mark := conn.mark()
	if err := sess.Restart(); err != nil {
		t.Fatalf("重开失败: %v", err)
	}
	// 给在飞的应着充分的落地时间（应着有 600ms 保底时长）
	time.Sleep(1200 * time.Millisecond)

	if moves := conn.aiMovesSince(mark); len(moves) != 0 {
		t.Errorf("红先关重开后轮到玩家，不该有 AI 应着，实际 %+v", moves)
	}
	if got := sess.SnapshotFEN(); got != p.FEN {
		t.Errorf("重开后局面应回到关卡初始 FEN\n  期望 %s\n  实际 %s", p.FEN, got)
	}
	if sess.IsBusy() {
		t.Error("重开后不应仍处于「思考中」—— 否则玩家走子会被拒")
	}
}

// ---------------------------------------------------------------- 2) 正解不可用

func TestBrokenSolutionMarksFailInsteadOfDesyncingCursor(t *testing.T) {
	st := loadPuzzles(t)
	base, ok := st.Get("cj-竹子涨棋-0003")
	if !ok {
		t.Skip("测试用关卡不在数据里")
	}
	if len(base.Solution) < 5 {
		t.Fatalf("测试前提变了：解长 %d", len(base.Solution))
	}

	// 复制一关，把守方第 1 手换成一个「坐标解析得出来、但在当前局面非法」的着法
	// （i9 是空格）⇒ 守方只能走替代着法。这正是外置残局目录可能出现的数据。
	bad := *base
	bad.Solution = append([]string(nil), base.Solution...)
	bad.Solution[1] = "i9i8"
	bad.ID = base.ID + "-broken"

	conn := &recConn{}
	sess := session.NewSession(session.ModePuzzle, game.Red, 4, llm.DefaultConfig(), &bad, engine.NewManager(""))
	sess.Join(conn)
	defer sess.Close()

	if err := sess.ApplyPlayerMove(bad.Solution[0][:2], bad.Solution[0][2:]); err != nil {
		t.Fatalf("玩家着法 %s 失败: %v", bad.Solution[0], err)
	}
	waitPlayerTurn(t, sess, game.Red) // 等守方应着（实际走的是替代着法）

	if !conn.hasEvent("solution_broken") {
		t.Error("正解着法走不出来时应下发 solution_broken 事件 —— 否则玩家走对也会被判「偏离正解」且毫无提示")
	}
	// ⚠️ state 只在 Join / BroadcastState / 重开时下发，走子消息里不带 ——
	// 直接读「最后一条 state」会读到 Join 时那份（那时还没失败），必须显式广播一次。
	sess.BroadcastState()
	last := conn.lastState()
	if last == nil {
		t.Fatal("没有收到全量状态消息")
	}
	// ⚠️ 残局字段嵌在 state 消息的 "puzzle" 子对象里，不在顶层。
	pzInfo, _ := last["puzzle"].(map[string]any)
	if pzInfo == nil {
		t.Fatal("状态消息里没有 puzzle 子对象")
	}
	if failed, _ := pzInfo["failed"].(bool); !failed {
		t.Error("正解不可用时应把关卡标记为「已失效」（pzFail）—— state 消息的 puzzle.failed 应为 true")
	}
}
