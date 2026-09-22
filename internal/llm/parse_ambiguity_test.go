package llm

import (
	"testing"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
)

// 本文件钉住「解说里提到别的着法时不得静默误走」（2026-09-22）。
//
// 背景：中文着法匹配原用 `strings.Contains` 并按 **pool 枚举顺序**返回第一个命中。
// 而 GenMoves 的顺序是 帅→仕→相→马→兵→车→炮，于是只要模型在解说里先提到马步、
// 结论才是炮步，就会选中那个马步 —— 不报错、不降级，静默走出一手非其本意的棋。
// 回退到 reasoning 解析时更危险：整条思维链天然会枚举并否定多个着法。
//
// 现在改为收集全部命中，按**文本出现位置**取最后出现的（模型通常在结尾给结论）。

const openingFEN = "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"

// 开局局面上「马八进七」= b0c2、「炮二平五」= h2e2，两者都合法。
// ⚠️ pool 枚举序里马在炮之前（GenMoves 按 帅仕相马车炮兵），所以旧实现必然选到马步。
func TestParseChinesePicksLastMentioned(t *testing.T) {
	pos, err := game.ParseFEN(openingFEN)
	if err != nil {
		t.Fatal(err)
	}
	pool := pos.LegalMoves(pos.Turn)

	// 先确认前提：两个着法都在 pool 内，且马步确实排在炮步之前
	idxHorse, idxCannon := -1, -1
	var cannonMove game.Move
	for i, m := range pool {
		switch pos.MoveToChinese(m) {
		case "马八进七":
			idxHorse = i
		case "炮二平五":
			idxCannon, cannonMove = i, m
		}
	}
	if idxHorse < 0 || idxCannon < 0 {
		t.Fatalf("前提失效：pool 里找不到这两个着法（马=%d 炮=%d）", idxHorse, idxCannon)
	}
	if idxHorse > idxCannon {
		t.Fatalf("前提失效：期望马步排在炮步之前（马=%d 炮=%d），否则本用例无判别力", idxHorse, idxCannon)
	}

	// 模型先对比马步、结论才是炮步 ⇒ 必须选炮步
	resp := "我对比了马八进七与炮二平五，最终选择炮二平五，因为中路更稳。"
	mv, _, ok := parseMove(resp, pos, pool)
	if !ok {
		t.Fatal("应能解析出着法")
	}
	if mv != cannonMove {
		t.Errorf("模型结论是「炮二平五」(%s)，却解析成了 %s —— 按 pool 顺序命中了先提到的那个",
			cannonMove, pos.MoveToChinese(mv))
	}
}

// TestParseChineseSingleMention 只有一个着法被提及时仍要正确命中（基线不许被改坏）。
func TestParseChineseSingleMention(t *testing.T) {
	pos, err := game.ParseFEN(openingFEN)
	if err != nil {
		t.Fatal(err)
	}
	pool := pos.LegalMoves(pos.Turn)
	mv, _, ok := parseMove("我走 炮二平五！", pos, pool)
	if !ok || mv.String() != "h2e2" {
		t.Errorf("单命中应解析为 h2e2，实际 ok=%v mv=%s", ok, mv)
	}
}

// TestParseUCIPicksLastMentioned UCI 串同理：解说里先提到别的着手时取最后出现的那个。
//
// ⚠️ 必须用**空格分隔**的写法才有判别力：旧实现按 `strings.Fields` 切词，而中文逗号
// 不是空白，「不走 h2e2，而走 b2e2」会被切成 "h2e2，而走" 与 "b2e2" —— 前者解析失败，
// 于是旧实现碰巧也选中了 b2e2。空格分隔才是旧实现真正会误选的情形。
func TestParseUCIPicksLastMentioned(t *testing.T) {
	pos, err := game.ParseFEN(openingFEN)
	if err != nil {
		t.Fatal(err)
	}
	pool := pos.LegalMoves(pos.Turn)

	for _, resp := range []string{
		"我不走 h2e2 而走 b2e2 更好。", // 空格分隔：旧实现会误选 h2e2
		"我不走 h2e2，而走 b2e2 更好。", // 中文逗号：回归保护
	} {
		mv, _, ok := parseMove(resp, pos, pool)
		if !ok {
			t.Fatalf("应能解析出着法: %q", resp)
		}
		if mv.String() != "b2e2" {
			t.Errorf("结论是 b2e2，却解析成了 %s(%s) —— 输入 %q",
				mv, pos.MoveToChinese(mv), resp)
		}
	}
}

// TestParseUCIWholeStringStillWorks 整段就是一个 UCI 串（最常见形态）不能被改坏。
func TestParseUCIWholeStringStillWorks(t *testing.T) {
	pos, err := game.ParseFEN(openingFEN)
	if err != nil {
		t.Fatal(err)
	}
	pool := pos.LegalMoves(pos.Turn)
	mv, _, ok := parseMove("h2e2", pos, pool)
	if !ok || mv.String() != "h2e2" {
		t.Errorf("裸 UCI 串应解析为 h2e2，实际 ok=%v mv=%s", ok, mv)
	}
}

// TestParseChineseFromReasoningChain 回退解析思考过程时最容易撞上多命中：
// 思维链会枚举并否定多个着法，必须取结论（最后出现的）而非最先提到的。
func TestParseChineseFromReasoningChain(t *testing.T) {
	pos, err := game.ParseFEN(openingFEN)
	if err != nil {
		t.Fatal(err)
	}
	pool := pos.LegalMoves(pos.Turn)

	reasoning := "让我想想。马八进七可以发展子力，但炮二平五更直接。" +
		"马八进七之后再炮二平五也是常见次序。综合考虑，我最终决定走炮二平五。"
	reply := ChatReply{Content: "", Reasoning: reasoning, FinishReason: "stop"}
	mv, _, ok := parseMove(reply.Text(), pos, pool)
	if !ok {
		t.Fatal("应能从思考过程里解析出着法")
	}
	if mv.String() != "h2e2" {
		t.Errorf("思考链结论是炮二平五(h2e2)，实际解析为 %s(%s)",
			mv, pos.MoveToChinese(mv))
	}
}
