package session_test

// SnapshotRecord（导出棋谱）的三条边界。
//
// 「只存下完的局」这条口径在代码里就落在这个返回值上：未终局导不出 →
// 保存端点 409 → 界面上结算弹窗还没出现、也就没有保存按钮。所以这里要把
// 「终局前 / 终局后 / 残局模式」三种情形都钉住。

import (
	"testing"

	"github.com/IamAyang233/panda-xiangqi/internal/engine"
	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/llm"
	"github.com/IamAyang233/panda-xiangqi/internal/puzzle"
	"github.com/IamAyang233/panda-xiangqi/internal/session"
)

func newLocalSession(t *testing.T) *session.Session {
	t.Helper()
	return session.NewSession(session.ModeLocal, game.Red, 4, llm.DefaultConfig(), nil, engine.NewManager(""))
}

func TestSnapshotRecordRequiresFinish(t *testing.T) {
	sess := newLocalSession(t)
	defer sess.Close()

	if _, ok := sess.SnapshotRecord(); ok {
		t.Fatal("未终局不该能导出棋谱")
	}
	if err := sess.ApplyPlayerMove("e3", "e4"); err != nil {
		t.Fatalf("走子失败: %v", err)
	}
	if _, ok := sess.SnapshotRecord(); ok {
		t.Fatal("只走了一手仍不是终局，不该能导出")
	}
	if err := sess.Resign(); err != nil {
		t.Fatalf("认输失败: %v", err)
	}
	rec, ok := sess.SnapshotRecord()
	if !ok {
		t.Fatal("认输也算终局，应能导出棋谱")
	}
	if rec.ID != sess.ID {
		t.Fatalf("棋谱 id 应等于对局 id（幂等保存的前提），实际 %q / %q", rec.ID, sess.ID)
	}
	if rec.Mode != session.ModeLocal || rec.HumanSide != "red" {
		t.Fatalf("模式/执子方不对: %+v", rec)
	}
	if rec.Result == "" || rec.Reason == "" {
		t.Fatalf("终局结果与原因都要带上: %+v", rec)
	}
	if _, err := game.ParseFEN(rec.StartFEN); err != nil {
		t.Fatalf("起始局面必须可解析（回放的前提）: %v", err)
	}
	if len(rec.Moves) != 1 || rec.Moves[0].UCI != "e3e4" || rec.Moves[0].CN == "" {
		t.Fatalf("着法记录不对: %+v", rec.Moves)
	}
	if rec.Name == "" || rec.Created == "" {
		t.Fatalf("名字与时间要自动填好（用户不用起名）: %+v", rec)
	}
}

// TestSnapshotRecordCapturesCaptured 记录被吃子 —— 棋谱要能标出「吃了什么」。
func TestSnapshotRecordCapturesCaptured(t *testing.T) {
	sess := newLocalSession(t)
	defer sess.Close()
	// 红兵 e3→e4、黑卒 e6→e5、红兵 e4×e5：最后一手是吃子
	for _, mv := range [][2]string{{"e3", "e4"}, {"e6", "e5"}, {"e4", "e5"}} {
		if err := sess.ApplyPlayerMove(mv[0], mv[1]); err != nil {
			t.Fatalf("走子 %v 失败: %v", mv, err)
		}
	}
	if err := sess.Resign(); err != nil {
		t.Fatal(err)
	}
	rec, ok := sess.SnapshotRecord()
	if !ok {
		t.Fatal("应能导出")
	}
	if len(rec.Moves) != 3 {
		t.Fatalf("应记录 3 手，实际 %d", len(rec.Moves))
	}
	if rec.Moves[2].Captured != "p" {
		t.Fatalf("最后一手应记录被吃的黑卒（FEN 字符 p），实际 %q", rec.Moves[2].Captured)
	}
	if rec.Moves[0].Captured != "" {
		t.Fatalf("没吃子的手不该有 captured，实际 %q", rec.Moves[0].Captured)
	}
}

// TestSnapshotRecordSkipsPuzzle 残局（含自定义残局）不进棋谱 —— 那是关卡，不是对局记录。
func TestSnapshotRecordSkipsPuzzle(t *testing.T) {
	p := &puzzle.Puzzle{ID: "pzl-x", Name: "测试关", Difficulty: "入门",
		PlayerSide: "red", Goal: "win", FEN: "3k5/9/9/9/9/9/9/9/4R4/4K4 w"}
	sess := session.NewSession(session.ModePuzzle, game.Red, 4, llm.DefaultConfig(), p, engine.NewManager(""))
	defer sess.Close()
	if err := sess.Resign(); err != nil {
		t.Fatal(err)
	}
	if _, ok := sess.SnapshotRecord(); ok {
		t.Fatal("残局对局不该产出棋谱")
	}
}
