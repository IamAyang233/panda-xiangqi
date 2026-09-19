package game

import "testing"

// gcStats 统计等价性检查的覆盖面。
type gcStats struct {
	moves      int
	trueCount  int
	falseCount int
	positions  int
}

// TestGivesCheckMatchesInCheck 守护 `GivesCheck` 的语义：
//
//	GivesCheck(m)  ==  ( Make(m); InCheck(p.Turn); Unmake() )
//
// 这是 `GivesCheck` 唯一的职责定义 —— 它存在的意义只是「把同一个问题提前问」，
// 任何一处语义差异都会让剪枝的「将军豁免」漏判（历史教训：把 depth 6 的将杀剪没）。
//
// ⚠️ 覆盖面要求两条，缺一条这个测试就等于没测：
//   - 局面要多（初始局面起做 3 层着法游走 + 若干残局），**含闪露将**；
//   - `GivesCheck` 为真的样本要够多，否则「恒返回 false」也能全过。
func TestGivesCheckMatchesInCheck(t *testing.T) {
	stats := &gcStats{}

	fens := []string{
		InitialFEN,
		"2bak1b2/4a4/4b4/p1p1p3p/6p2/2P6/P3P1P1P/1C2C4/9/RNBAKABNR w - - 0 1",
		"3ak1b2/4a4/4b4/p1p1p3p/9/2P6/P3P1P1P/1C2C1R2/9/2BAKABN1 w - - 0 1",
		"3ak4/4a4/4b4/9/9/9/4P4/9/4R4/4K4 w - - 0 1",
		"4k4/4a4/9/9/9/9/9/9/2R1R4/3K5 w - - 0 1",
		"3k5/9/9/9/4r4/9/9/9/3NR4/3K5 w - - 0 1",
		"3ak4/4a4/4b4/9/9/9/9/4N4/3C5/4K4 w - - 0 1",
		"3ak4/4a4/4b4/9/9/9/4P4/9/2C1C4/3K1A3 w - - 0 1",
	}
	for _, f := range fens {
		p, err := ParseFEN(f)
		if err != nil {
			t.Fatalf("FEN 解析失败 %q: %v", f, err)
		}
		walkGives(t, p, 0, 3, stats)
	}

	t.Logf("覆盖：%d 个局面、%d 个着法（将军 %d ／ 非将军 %d）",
		stats.positions, stats.moves, stats.trueCount, stats.falseCount)

	if stats.moves == 0 {
		t.Fatal("没跑到任何着法，测试是空的")
	}
	if stats.trueCount < 200 {
		t.Errorf("将军样本只有 %d 个（<200）—— 覆盖不足，「恒返回 false」也能过，这个测试失去判别力",
			stats.trueCount)
	}
	if stats.falseCount < 500 {
		t.Errorf("非将军样本只有 %d 个（<500）—— 覆盖不足", stats.falseCount)
	}
}

// walkGives 从 p 出发游走 depth 层，对沿途每个局面的每个合法着法做等价性检查。
func walkGives(t *testing.T, p *Position, cur, depth int, stats *gcStats) {
	if cur >= depth {
		return
	}
	moves := p.LegalMoves(p.Turn)
	for _, m := range moves {
		want := p.GivesCheck(m)
		p.Make(m)
		got := p.InCheck(p.Turn)
		p.Unmake()

		stats.moves++
		if want {
			stats.trueCount++
		} else {
			stats.falseCount++
		}
		if want != got {
			t.Fatalf("GivesCheck 与 InCheck 不一致（第 %d 个着法）：%v → GivesCheck=%v 而落子后 InCheck=%v",
				stats.moves, m, want, got)
		}
		if stats.moves > 200000 {
			return
		}
	}
	stats.positions++
	for _, m := range moves {
		p.Make(m)
		walkGives(t, p, cur+1, depth, stats)
		p.Unmake()
		if stats.moves > 200000 {
			return
		}
	}
}
