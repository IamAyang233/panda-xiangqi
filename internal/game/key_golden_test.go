package game

import "testing"

// Zobrist 键位回归（M2 验收）：下列 Key 取自改造前（mailbox 版）实现的实测值，
// 见 _panda_baseline 的取样。键位沿用 256 下标，改造后必须逐位相同，
// 否则历史存档、重复局面检测与置换表会全部失效。
var goldenKeys = []struct {
	fen string
	key uint64
}{
	{"rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1", 1315952085615690495},
	{"rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR b - - 0 1", 9639716432520828174},
	{"rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 7 12", 1315952085615690495},
	{"3k5/9/9/9/9/9/9/9/9/4K4 w - - 0 1", 7999804276756050922},
	{"4k4/9/9/9/4P4/9/9/9/9/3KC4 w - - 0 1", 11933073098252153370},
	{"r1ba1a3/4kn3/2n1b4/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1", 7385083764605045862},
	{"3k5/9/5Pn2/9/9/9/9/9/9/4K4 w - - 0 1", 14584598959501525213},
	{"9/9/9/9/9/9/9/9/9/4K3k w - - 0 1", 9427689889887463164},
	{"rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 99", 1315952085615690495},
	{"4k4/4a4/4b4/9/9/9/9/4B4/4A4/4K4 w - - 0 1", 13190700984596599955},
}

func TestZobristKeyUnchanged(t *testing.T) {
	for _, g := range goldenKeys {
		p, err := ParseFEN(g.fen)
		if err != nil {
			t.Fatalf("解析失败 %s: %v", g.fen, err)
		}
		if p.Key != g.key {
			t.Errorf("Key 与改造前不一致\nFEN: %s\n期望: %d\n实际: %d", g.fen, g.key, p.Key)
		}
	}
}
