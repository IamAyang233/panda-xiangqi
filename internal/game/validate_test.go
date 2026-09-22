package game

import "testing"

// TestLegalPositionRejectsKingCapture 锁定「非行棋方被将军」这一类非法局面的判定。
//
// 背景：这类局面下走子方可以直接吃将（GenMoves 不禁止吃将），而 Make 后
// clearPiece 从不更新 kingSq ⇒ 被吃将那方的 kingSq 永久悬空在一个空格上，
// InCheck / LegalMoves / CheckStatus 全部围绕幽灵格判定，静默给出任意结果。
//
// `4k4/9/9/9/4P4/9/9/9/9/3KC4 w` 是仓库自己的 perft 对拍用例，红炮 e0 隔红兵 e4
// 正对黑将 e9，红方可走 e0e9 吃将 —— 因此 ParseFEN 必须保持宽松（对拍依赖它），
// 语义校验落在 LegalPosition 上，由题库加载与对局入口调用。
func TestLegalPositionRejectsKingCapture(t *testing.T) {
	const fen = "4k4/9/9/9/4P4/9/9/9/9/3KC4 w - - 0 1"
	pos, err := ParseFEN(fen) // 语法合法
	if err != nil {
		t.Fatalf("ParseFEN 应接受该局面（对拍依赖宽松解析）: %v", err)
	}
	// 确认这确实是「可吃将」的局面，否则测试前提失效、断言无判别力。
	canEatKing := false
	for _, m := range pos.LegalMoves(pos.Turn) {
		if TypeOf(pos.PieceAt(int(m.To))) == King {
			canEatKing = true
			break
		}
	}
	if !canEatKing {
		t.Fatal("测试前提失效：该局面已不是「可直接吃将」的局面")
	}
	if err := pos.LegalPosition(); err == nil {
		t.Error("LegalPosition 应拒绝「非行棋方被将军」的非法局面")
	}
}

// TestLegalPositionAcceptsRealPuzzles 正向用例：正常与残局常见的合法局面必须放行，
// 免得校验写过头把整个题库挡在门外。
func TestLegalPositionAcceptsRealPuzzles(t *testing.T) {
	fens := []string{
		InitialFEN,
		"3k5/9/9/9/9/9/9/9/9/4K4 w - - 0 1", // 残局：单将
		"r1ba1a3/4kn3/2n1b4/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1", // 中局
		"3k5/9/5Pn2/9/9/9/9/9/9/4K4 b - - 0 1",                                   // 黑先
		"4k4/4a4/4b4/9/9/9/9/4B4/4A4/4K4 w - - 0 1",                              // 士象
		"3k2b2/4P4/2c6/2N6/9/9/9/9/4K4/9 w - - 0 1",                              // 内置题库用例
	}
	for _, f := range fens {
		pos, err := ParseFEN(f)
		if err != nil {
			t.Errorf("ParseFEN 失败 %s: %v", f, err)
			continue
		}
		if err := pos.LegalPosition(); err != nil {
			t.Errorf("合法局面被误拒 %s: %v", f, err)
		}
	}
}

// TestValidatePlacementRejectsIllegalPlacement 摆位维度：象过河、士出九宫、将帅照面。
func TestValidatePlacementRejectsIllegalPlacement(t *testing.T) {
	cases := []struct{ fen, why string }{
		// 红相被放在黑方半场（rank 6 过河）
		{"3k5/9/9/9/9/9/2B6/9/9/4K4 w - - 0 1", "象过河"},
		// 红仕被放在九宫之外（d3）
		{"3k5/9/9/3A5/9/9/9/9/9/4K4 w - - 0 1", "士不在九宫点位"},
		// 将帅同线照面（e 线中间无子）
		{"4k4/9/9/9/9/9/9/9/9/4K4 w - - 0 1", "将帅照面"},
	}
	for _, c := range cases {
		pos, err := ParseFEN(c.fen)
		if err != nil {
			// 照面那种局面语法也可能被接受；解析失败也算「拒绝」，但要区分。
			t.Logf("%s: ParseFEN 直接拒绝（%v）", c.why, err)
			continue
		}
		if err := pos.ValidatePlacement(); err == nil {
			t.Errorf("%s：ValidatePlacement 应报错，实际通过（FEN: %s）", c.why, c.fen)
		}
	}
}
