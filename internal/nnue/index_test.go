package nnue

import (
	"testing"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
)

// boardFromFEN 借棋规包解析 FEN，再转成本包的视图。
func boardFromFEN(t *testing.T, fen string) Board {
	t.Helper()
	p, err := game.ParseFEN(fen)
	if err != nil {
		t.Fatalf("解析 FEN 失败: %v", err)
	}
	return BoardFromGame(p.Board)
}

func assertInRange(t *testing.T, name string, idx []int, limit int) {
	t.Helper()
	for _, v := range idx {
		if v < 0 || v >= limit {
			t.Fatalf("%s 索引越界: %d（上限 %d）", name, v, limit)
		}
	}
}

func assertUnique(t *testing.T, name string, idx []int) {
	t.Helper()
	seen := make(map[int]bool, len(idx))
	for _, v := range idx {
		if seen[v] {
			t.Fatalf("%s 索引重复: %d", name, v)
		}
		seen[v] = true
	}
}

// TestPSQIndicesInitial 初始局面：每子一个特征，索引落在 24 个桶内。
func TestPSQIndicesInitial(t *testing.T) {
	b := boardFromFEN(t, game.InitialFEN)
	for _, side := range []int{colorWhite, colorBlack} {
		idx := b.ActivePSQ(side)
		if len(idx) != 32 {
			t.Errorf("视角 %d 的 PSQ 特征数 = %d，初始局面应有 32 子", side, len(idx))
		}
		assertInRange(t, "PSQ", idx, psqCount*24)
		assertUnique(t, "PSQ", idx)
	}
}

// TestPSQIndicesEmpty 空盘（仅双方将帅）也应有 2 个特征，且不越界。
func TestPSQIndicesEmpty(t *testing.T) {
	b := boardFromFEN(t, "3k5/9/9/9/9/9/9/9/9/4K4 w - - 0 1")
	idx := b.ActivePSQ(colorWhite)
	if len(idx) != 2 {
		t.Errorf("PSQ 特征数 = %d，应为 2", len(idx))
	}
	assertInRange(t, "PSQ", idx, psqCount*24)
}

// TestThreatIndices 威胁特征：索引范围、唯一性、以及随局面变化。
func TestThreatIndices(t *testing.T) {
	fens := []string{
		game.InitialFEN,
		"3k5/9/9/9/9/9/9/9/9/4K4 w - - 0 1",
		"4k4/9/9/9/4P4/9/9/9/9/3KC4 w - - 0 1",
		"rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1",
	}
	for _, fen := range fens {
		b := boardFromFEN(t, fen)
		for _, side := range []int{colorWhite, colorBlack} {
			idx := b.ActiveThreats(side)
			assertInRange(t, "威胁", idx, ThreatInputs)
			assertUnique(t, "威胁", idx)
		}
	}
}

// TestThreatCountNonZero 初始局面棋子互相阻挡，必然产生威胁特征。
func TestThreatCountNonZero(t *testing.T) {
	b := boardFromFEN(t, game.InitialFEN)
	if got := len(b.ActiveThreats(colorWhite)); got == 0 {
		t.Error("初始局面威胁特征数为 0，说明攻击或过滤逻辑有误")
	}
}

// TestLayerStackBucket 档位桶取值 0..15，且随车数变化。
func TestLayerStackBucket(t *testing.T) {
	b := boardFromFEN(t, game.InitialFEN)
	for _, us := range []int{colorWhite, colorBlack} {
		if v := b.LayerStackBucket(us); v < 0 || v > 15 {
			t.Errorf("LayerStackBucket = %d，应在 0..15", v)
		}
	}
	// 多车对无车的局面必须落在 14/15 两个「车数不平等」桶里。
	b2 := boardFromFEN(t, "3k5/9/9/9/9/9/9/9/9/RR2K4 w - - 0 1")
	if v := b2.LayerStackBucket(colorWhite); v != 14 {
		t.Errorf("红方双车对无车 = %d，应为 14", v)
	}
}

// TestMirrorSymmetry 初始局面左右对称，红方视角是否镜像应只取决于中线规则。
func TestMirrorSymmetry(t *testing.T) {
	b := boardFromFEN(t, game.InitialFEN)
	// 双方帅将都在 e 列（中线），且局面均衡，不应触发中线镜像。
	if b.requiresMidMirror(colorWhite) {
		t.Error("初始局面不应触发中线镜像")
	}
	_, mirror := b.FeatureBucket(colorWhite)
	if mirror {
		t.Error("初始局面红方视角不应水平镜像")
	}

	// 把红帅移到 d0（离开中线），中线规则应被触发或桶号改变。
	b2 := boardFromFEN(t, "3k5/9/9/9/9/9/9/9/9/3K5 w - - 0 1")
	if !b2.requiresMidMirror(colorWhite) {
		t.Error("双方帅将不在中线时应触发中线镜像")
	}
}

// TestPieceFromGame 校验两套棋子编码的映射。
func TestPieceFromGame(t *testing.T) {
	// 项目编码：Red=0, Black=8；类型 King=1,Advisor=2,Elephant=3,Horse=4,Rook=5,Cannon=6,Pawn=7
	cases := []struct {
		in   byte
		want int
	}{
		{1, makePiece(colorWhite, ptKing)},
		{2, makePiece(colorWhite, ptAdvisor)},
		{3, makePiece(colorWhite, ptBishop)},
		{4, makePiece(colorWhite, ptKnight)},
		{5, makePiece(colorWhite, ptRook)},
		{6, makePiece(colorWhite, ptCannon)},
		{7, makePiece(colorWhite, ptPawn)},
		{1 | 8, makePiece(colorBlack, ptKing)},
		{7 | 8, makePiece(colorBlack, ptPawn)},
		{0x00, 0},
		{0xFF, 0},
	}
	for _, c := range cases {
		if got := PieceFromGame(c.in); got != c.want {
			t.Errorf("PieceFromGame(0x%02X) = %d，应为 %d", c.in, got, c.want)
		}
	}
}
