package search

import (
	"testing"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
)

// 这一组局面与 internal/game/game_test.go 的长将测试**同源**，但那里走的是
// `Make` 直接落子，这里要顺手断言每一步真的合法 —— 因为 `Make` 只要求伪合法：
// 起点是空格、或将帅照面，它都不报错（见下面的 playSeq）。

const (
	// 红长将：红车 e2↔f2 追击，黑王 f9↔e9 应将；红帅在 d0（与黑王不同线）。
	redLongCheckFEN = "5k3/9/9/9/9/9/9/4R4/9/3K5 w - - 0 1"
	// 黑长将：黑车 f7↔e7 追击，红帅 e0↔f0 应将；黑王在 d9。
	blackLongCheckFEN = "3k5/9/5r3/9/9/9/9/9/9/4K4 b - - 0 1"
	// 非长将的三次重复：双方走马闲步。马在 a0/a9（FEN 首尾字段就是 "n3k4"/"N3K4"），
	// 走法必须是真正的马步 a0↔b2 / a9↔b7。
	quietRepetitionFEN = "n3k4/9/9/9/9/9/9/9/9/N2K5 w - - 0 1"
)

// 走满 12 步（三圈）后轮到红方，红是长将方 ⇒ 按规则红负。
var redLongCheckSeq = []string{
	"e2f2", "f9e9", "f2e2", "e9f9",
	"e2f2", "f9e9", "f2e2", "e9f9",
	"e2f2", "f9e9", "f2e2", "e9f9",
}

// 走满 12 步后轮到黑方，黑是长将方 ⇒ 按规则黑负。
var blackLongCheckSeq = []string{
	"f7e7", "e0f0", "e7f7", "f0e0",
	"f7e7", "e0f0", "e7f7", "f0e0",
	"f7e7", "e0f0", "e7f7", "f0e0",
}

// 8 步 = 两圈 ⇒ 初始局面出现 3 次，且双方都没有将军。
var quietRepetitionSeq = []string{
	"a0b2", "a9b7", "b2a0", "b7a9",
	"a0b2", "a9b7", "b2a0", "b7a9",
}

// playSeq 依次走出一串着法，并断言每一步「起点有子且着法合法」。
//
// ⚠️ 这两个断言是必须的：`Make` 只要求**伪合法**，起点是空格照样会 XOR Zobrist
// 键（键历史被污染 ⇒ 出现假重复），自将/照面也照走不误。少了断言，坐标写错时
// 测试会静默退化成一个空测试 —— 这在 internal/game 里真实发生过三次
// （`TestRepetitionNonCheckDraw` 与两条长将测试），所以这里一并守住。
func playSeq(t *testing.T, fen string, seq []string) *game.Position {
	t.Helper()
	p, err := game.ParseFEN(fen)
	if err != nil {
		t.Fatalf("FEN 解析失败 %q: %v", fen, err)
	}
	for _, s := range seq {
		m, ok := game.MoveFromUCI(s)
		if !ok {
			t.Fatalf("着法解析失败 %q", s)
		}
		if p.PieceAt(int(m.From)) == game.Empty {
			t.Fatalf("着法 %q 的起点 %s 没有棋子（坐标写错了？）—— "+
				"这种「空走子」不会报错，但会污染 Zobrist 键历史并制造假重复",
				s, game.SquareName(m.From))
		}
		if !p.IsLegal(m) {
			t.Fatalf("着法 %q 在当前局面不合法（自将 / 将帅照面 / 蹩腿等）", s)
		}
		p.Make(m)
	}
	return p
}

// TestRepetitionScoreLongCheck 是「搜索的重复估值必须与规则一致」的关卡。
//
// 规则：三次重复且构成单方长将 ⇒ 长将方判负（status.go 的 ReasonLongCheck）。
// 搜索此前把任何重复都算 0（和棋），于是会把「自己长将被判负」的线当成和棋
// 主动走进去，也会错失「对方长将被判负」的胜机 —— 两者都与对局裁决矛盾。
//
// ⚠️ 判别力：把 repetitionScore 改回恒返回 0，本测试立刻红。
func TestRepetitionScoreLongCheck(t *testing.T) {
	// 显式打开开关：这个测试测的是规则映射本身，不该依赖环境默认值
	// （否则 `QIJING_LC=off` 一开，整个包测试就红了 —— 而那是给 A/B 与运维用的开关）。
	defer SetLongCheck(SetLongCheck(true))

	// ---- 红长将：12 步后轮到红方，红是长将方 ⇒ 红（当前走子方）得负分 ----
	redChecker := playSeq(t, redLongCheckFEN, redLongCheckSeq)
	if n := redChecker.RepetitionCount(); n != 4 {
		t.Fatalf("红长将局面：期望重复 4 次，实际 %d", n)
	}
	if redChecker.Turn != game.Red {
		t.Fatalf("红长将局面：期望轮到红方，实际 %d", redChecker.Turn)
	}
	if w, ok := redChecker.LongCheckWinner(); !ok || w != game.ResultBlackWin {
		t.Fatalf("红长将局面：规则应判黑胜，实际 winner=%q ok=%v", w, ok)
	}

	ResetLongCheckTriggers()
	got := repetitionScore(redChecker, 1)
	if got >= 0 {
		t.Errorf("红长将、轮到红方：期望负分（红判负），实际 %d —— "+
			"搜索若返回 0 就会把「自己长将被判负」当成和棋走进去", got)
	}

	// ---- 黑长将：轮到黑方，黑是长将方 ⇒ 同样得负分 ----
	blackChecker := playSeq(t, blackLongCheckFEN, blackLongCheckSeq)
	if n := blackChecker.RepetitionCount(); n != 4 {
		t.Fatalf("黑长将局面：期望重复 4 次，实际 %d", n)
	}
	if blackChecker.Turn != game.Black {
		t.Fatalf("黑长将局面：期望轮到黑方，实际 %d", blackChecker.Turn)
	}
	if got := repetitionScore(blackChecker, 1); got >= 0 {
		t.Errorf("黑长将、轮到黑方：期望负分，实际 %d", got)
	}

	// ---- 反方向：轮到长将方的**对手**时必须是正分（对方长将 ⇒ 我胜）----
	// 走 9 步（奇数）⇒ 轮到黑方，而黑正是红长将的受害者。
	redVictim := playSeq(t, redLongCheckFEN, redLongCheckSeq[:9])
	if redVictim.Turn != game.Black {
		t.Fatalf("9 步后应轮到黑方，实际 %d", redVictim.Turn)
	}
	if n := redVictim.RepetitionCount(); n < 3 {
		t.Fatalf("9 步后应已构成三次重复，实际 %d", n)
	}
	if got := repetitionScore(redVictim, 1); got <= 0 {
		t.Errorf("红长将、轮到黑方（受害方）：期望正分（黑胜），实际 %d", got)
	}

	if longCheckTriggers == 0 {
		t.Fatal("长将分支一次都没被走到 —— 这个测试没有测到目标路径")
	}

	// ---- 非长将的三次重复必须仍是 0（和棋）----
	quiet := playSeq(t, quietRepetitionFEN, quietRepetitionSeq)
	if n := quiet.RepetitionCount(); n != 3 {
		t.Fatalf("非长将重复局面：期望重复 3 次，实际 %d", n)
	}
	if _, ok := quiet.LongCheckWinner(); ok {
		t.Fatal("非长将重复不应判定单方胜负")
	}
	if got := repetitionScore(quiet, 1); got != 0 {
		t.Errorf("非长将的三次重复应给和棋分 0，实际 %d", got)
	}

	// ---- 阈值必须正好是 3：只有 2 次重复时仍按和棋分 ----
	// （搜索惯用「二次重复即判和」，这次改动只该在规则真正生效的那一档上叠加。）
	fourFold := playSeq(t, redLongCheckFEN, redLongCheckSeq[:4])
	if n := fourFold.RepetitionCount(); n != 2 {
		t.Fatalf("4 步（一圈）后只应重复 2 次，实际 %d", n)
	}
	if got := repetitionScore(fourFold, 1); got != 0 {
		t.Errorf("只有 2 次重复时应仍按和棋分 0（阈值是 3），实际 %d", got)
	}
	// 8 步（两圈）⇒ 3 次重复，已到阈值，此时就该判长将负。
	eightPly := playSeq(t, redLongCheckFEN, redLongCheckSeq[:8])
	if n := eightPly.RepetitionCount(); n != 3 {
		t.Fatalf("8 步（两圈）后应重复 3 次，实际 %d", n)
	}
	if got := repetitionScore(eightPly, 1); got >= 0 {
		t.Errorf("8 步后已达三次重复阈值，红长将应判负（负分），实际 %d", got)
	}
}

// TestRepetitionScoreSwitchOff 验证开关两态：开着按规则判负、关掉回到旧行为（0）。
//
// 这是交替 A/B 两态成立的前提，也保证关掉后能一键回到改动前的估值。
// ⚠️ 这里显式设置两态而不依赖环境默认 —— 否则 `QIJING_LC=off` 下整个包测试会红。
func TestRepetitionScoreSwitchOff(t *testing.T) {
	p := playSeq(t, redLongCheckFEN, redLongCheckSeq)

	// 开：按规则判长将方负
	defer SetLongCheck(SetLongCheck(true))
	if got := repetitionScore(p, 1); got >= 0 {
		t.Fatalf("开关打开时：期望负分，实际 %d", got)
	}

	// 关：回到旧行为，且不再累计触发计数
	old := SetLongCheck(false)
	defer SetLongCheck(old)

	before := LongCheckTriggers()
	if got := repetitionScore(p, 1); got != 0 {
		t.Errorf("关掉开关后应回到旧行为 0，实际 %d", got)
	}
	if LongCheckTriggers() != before {
		t.Error("关掉开关后不该再累计触发计数")
	}
}
