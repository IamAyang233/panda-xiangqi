package game

import (
	"math/rand"
	"testing"
)

// 位棋盘攻击判定的交叉验证：与一份**独立的逐格步进参考实现**对拍。
// 参考实现刻意不使用 knightAttackers / pawnAttackers 等被测预计算表，
// 而是从 knightTbl 原始定义与坐标算术重新推导，避免"两边错得一样"。

// isAttackedRef 参考实现（逐格步进，sq90 坐标）。
func isAttackedRef(board *[90]byte, sq, by int) bool {
	f, r := bbFile(sq), bbRank(sq)
	for _, d := range stepDirs {
		to := step(sq, d[0], d[1])
		for to >= 0 && board[to] == Empty {
			to = step(to, d[0], d[1])
		}
		if to >= 0 {
			t := board[to]
			if ColorOf(t) == by {
				if TypeOf(t) == Rook {
					return true
				}
				if TypeOf(t) == King && d[1] != 0 {
					return true // 将帅照面（纵向）
				}
			}
			to = step(to, d[0], d[1])
			for to >= 0 && board[to] == Empty {
				to = step(to, d[0], d[1])
			}
			if to >= 0 && ColorOf(board[to]) == by && TypeOf(board[to]) == Cannon {
				return true // 隔一子翻山
			}
		}
	}
	// 马：从 knightTbl 原始定义反推能攻击 sq 的马位。
	// 马位 = sq - (df,dr)，蹩腿点 = 马位 + (lf,lr)（注意不能直接用 sq+(lf,lr)）。
	for _, m := range knightTbl {
		origin := bbSquare(f-m.df, r-m.dr)
		leg := bbSquare(f-m.df+m.lf, r-m.dr+m.lr)
		if origin < 0 || leg < 0 {
			continue
		}
		t := board[origin]
		if t != Empty && ColorOf(t) == by && TypeOf(t) == Horse && board[leg] == Empty {
			return true
		}
	}
	// 兵
	if by == Red {
		if r >= 1 && board[bbSquare(f, r-1)] == Piece(Red, Pawn) {
			return true
		}
		if r >= 5 { // 过河红兵可横吃
			if f > 0 && board[bbSquare(f-1, r)] == Piece(Red, Pawn) {
				return true
			}
			if f < 8 && board[bbSquare(f+1, r)] == Piece(Red, Pawn) {
				return true
			}
		}
	} else {
		if r <= 8 && board[bbSquare(f, r+1)] == Piece(Black, Pawn) {
			return true
		}
		if r <= 4 {
			if f > 0 && board[bbSquare(f-1, r)] == Piece(Black, Pawn) {
				return true
			}
			if f < 8 && board[bbSquare(f+1, r)] == Piece(Black, Pawn) {
				return true
			}
		}
	}
	return false
}

func TestBitboardBasics(t *testing.T) {
	var b Bitboard
	if !b.IsEmpty() {
		t.Fatal("新位棋盘应为空")
	}
	for _, sq := range []int{0, 63, 64, 89} {
		b.Set(sq)
		if !b.Test(sq) {
			t.Fatalf("Set/Test 失败 sq=%d", sq)
		}
	}
	if got := b.Count(); got != 4 {
		t.Fatalf("Count = %d, 期望 4", got)
	}
	if got := b.lsb(); got != 0 {
		t.Fatalf("lsb = %d, 期望 0", got)
	}
	if got := b.msb(); got != 89 {
		t.Fatalf("msb = %d, 期望 89", got)
	}
	want := []int{0, 63, 64, 89}
	for i, w := range want {
		if got := b.popLSB(); got != w {
			t.Fatalf("第 %d 次 popLSB = %d, 期望 %d", i, got, w)
		}
	}
	if !b.IsEmpty() {
		t.Fatal("popLSB 后应为空")
	}
	if b.popLSB() != -1 {
		t.Fatal("空盘 popLSB 应返回 -1")
	}
}

// scatter 随机摆放 n 个棋子（可覆盖无将/多子等极端布局）。
func scatter(rng *rand.Rand, n int) *Position {
	p := &Position{Turn: Red}
	for i := 0; i < n; i++ {
		sq := rng.Intn(90)
		typ := 1 + rng.Intn(7)
		color := Red
		if rng.Intn(2) == 0 {
			color = Black
		}
		p.clearPiece(sq) // 随机可能命中同一格，须先清旧子再放新子
		p.setPiece(sq, Piece(color, typ))
	}
	return p
}

// randomWalk 从初始局面随机走 plies 步，构造真实可达局面。
func randomWalk(rng *rand.Rand, plies int) *Position {
	p := NewPosition()
	for i := 0; i < plies; i++ {
		ms := p.LegalMoves(p.Turn)
		if len(ms) == 0 {
			break
		}
		p.Make(ms[rng.Intn(len(ms))])
	}
	return p
}

func compareAttacks(t *testing.T, p *Position, tag string) {
	t.Helper()
	for sq := 0; sq < 90; sq++ {
		for _, by := range [2]int{Red, Black} {
			want := isAttackedRef(&p.Board, sq, by)
			got := p.bb.isAttackedBB(sq, by)
			if want != got {
				t.Fatalf("%s: 格 %s 被 %d 攻击：参考=%v 位棋盘=%v\nFEN: %s",
					tag, SquareName(uint8(sq)), by, want, got, p.FEN())
			}
		}
	}
}

func TestAttackMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewSource(0x20260913))

	compareAttacks(t, NewPosition(), "初始局面")
	compareAttacks(t, scatter(rng, 2), "稀疏")
	compareAttacks(t, scatter(rng, 32), "稠密")

	const scatterN = 100000
	for i := 0; i < scatterN; i++ {
		compareAttacks(t, scatter(rng, 1+rng.Intn(30)), "随机摆放")
	}
	for i := 0; i < 2000; i++ {
		compareAttacks(t, randomWalk(rng, 1+rng.Intn(60)), "随机对局")
	}
}

func TestKnightSymmetry(t *testing.T) {
	for a := 0; a < 90; a++ {
		tmp := knightTargets[a]
		for !tmp.IsEmpty() {
			b := tmp.popLSB()
			if !knightTargets[b].Test(a) {
				t.Fatalf("马步不对称：%d -> %d 但不可反向", a, b)
			}
		}
	}
}

// TestBitboardInSync 位棋盘与 Board 数组必须始终一致（M2 验收）。
func TestBitboardInSync(t *testing.T) {
	rng := rand.New(rand.NewSource(0xC0FFEE))
	// 真实对局局面 + 随机摆放局面都要查：后者能抓到"只置位不清位"类的同步遗漏。
	cases := make([]*Position, 0, 4000)
	for i := 0; i < 2000; i++ {
		cases = append(cases, randomWalk(rng, 1+rng.Intn(60)))
	}
	for i := 0; i < 2000; i++ {
		cases = append(cases, scatter(rng, 1+rng.Intn(30)))
	}
	for _, p := range cases {
		var byColor, byType [8]Bitboard
		var occ Bitboard
		for sq := 0; sq < 90; sq++ {
			pc := p.Board[sq]
			if pc == Empty {
				continue
			}
			byColor[ColorOf(pc)>>3].Set(sq)
			byType[TypeOf(pc)].Set(sq)
			occ.Set(sq)
		}
		if byColor[0] != p.bb.byColor[0] || byColor[1] != p.bb.byColor[1] {
			t.Fatalf("byColor 不同步\nFEN: %s", p.FEN())
		}
		for typ := 1; typ <= 7; typ++ {
			if byType[typ] != p.bb.byType[typ] {
				t.Fatalf("byType[%d] 不同步\nFEN: %s", typ, p.FEN())
			}
		}
		if occ != p.bb.occ {
			t.Fatalf("occ 不同步\nFEN: %s", p.FEN())
		}
	}
}

// TestMakeUnmakeRestores Make/Unmake 后局面（含 Key 与位棋盘）必须完全还原。
func TestMakeUnmakeRestores(t *testing.T) {
	rng := rand.New(rand.NewSource(0xBEEF))
	for i := 0; i < 500; i++ {
		p := randomWalk(rng, 1+rng.Intn(40))
		before := *p
		beforeFEN := p.FEN()

		for _, m := range p.LegalMoves(p.Turn) {
			keyBefore := p.Key
			p.Make(m)
			p.Unmake()
			if p.Key != keyBefore {
				t.Fatalf("Key 未还原：着法 %s\nFEN: %s", m, beforeFEN)
			}
		}
		if p.FEN() != beforeFEN {
			t.Fatalf("FEN 未还原：\n前 %s\n后 %s", beforeFEN, p.FEN())
		}
		if p.bb.occ != before.bb.occ || p.bb.byColor != before.bb.byColor {
			t.Fatalf("位棋盘未还原\nFEN: %s", beforeFEN)
		}
		if p.Board != before.Board {
			t.Fatalf("Board 未还原\nFEN: %s", beforeFEN)
		}
	}
}
