package game

import (
	"math/rand"
	"testing"
)

// genMovesRef 逐格步进参考实现（M2 版逻辑）。与位运算版完全独立：
// 它遍历 90 格、按方向逐格步进、依九宫/河界算术判定，不使用任何预计算攻击表。
// 两版着法集合必须完全一致——这是走法生成的交叉验证。
func genMovesRef(p *Position, side int) []Move {
	moves := make([]Move, 0, 48)
	for sq := 0; sq < 90; sq++ {
		pc := p.Board[sq]
		if pc == Empty || ColorOf(pc) != side {
			continue
		}
		r := bbRank(sq)
		switch TypeOf(pc) {
		case King:
			for _, d := range stepDirs {
				to := step(sq, d[0], d[1])
				if to < 0 {
					continue
				}
				if inPalace(side, bbFile(to), bbRank(to)) && !own(side, p.Board[to]) {
					moves = append(moves, Move{uint8(sq), uint8(to)})
				}
			}
		case Advisor:
			for _, d := range [4][2]int{{1, 1}, {1, -1}, {-1, 1}, {-1, -1}} {
				to := step(sq, d[0], d[1])
				if to < 0 {
					continue
				}
				if inPalace(side, bbFile(to), bbRank(to)) && !own(side, p.Board[to]) {
					moves = append(moves, Move{uint8(sq), uint8(to)})
				}
			}
		case Elephant:
			for _, d := range [4][2]int{{2, 2}, {2, -2}, {-2, 2}, {-2, -2}} {
				to := step(sq, d[0], d[1])
				if to < 0 {
					continue
				}
				if !ownSide(side, bbRank(to)) {
					continue
				}
				eye := step(sq, d[0]/2, d[1]/2)
				if eye < 0 || p.Board[eye] != Empty {
					continue
				}
				if !own(side, p.Board[to]) {
					moves = append(moves, Move{uint8(sq), uint8(to)})
				}
			}
		case Horse:
			for _, m := range knightTbl {
				to := step(sq, m.df, m.dr)
				if to < 0 {
					continue
				}
				leg := step(sq, m.lf, m.lr)
				if leg < 0 || p.Board[leg] != Empty {
					continue
				}
				if !own(side, p.Board[to]) {
					moves = append(moves, Move{uint8(sq), uint8(to)})
				}
			}
		case Rook:
			for _, d := range stepDirs {
				for to := step(sq, d[0], d[1]); to >= 0; to = step(to, d[0], d[1]) {
					t := p.Board[to]
					if t == Empty {
						moves = append(moves, Move{uint8(sq), uint8(to)})
						continue
					}
					if ColorOf(t) != side {
						moves = append(moves, Move{uint8(sq), uint8(to)})
					}
					break
				}
			}
		case Cannon:
			for _, d := range stepDirs {
				for to := step(sq, d[0], d[1]); to >= 0; to = step(to, d[0], d[1]) {
					if p.Board[to] != Empty {
						break
					}
					moves = append(moves, Move{uint8(sq), uint8(to)})
				}
				screen := -1
				for to := step(sq, d[0], d[1]); to >= 0; to = step(to, d[0], d[1]) {
					if p.Board[to] != Empty {
						screen = to
						break
					}
				}
				if screen < 0 {
					continue
				}
				for to := step(screen, d[0], d[1]); to >= 0; to = step(to, d[0], d[1]) {
					if p.Board[to] == Empty {
						continue
					}
					if ColorOf(p.Board[to]) != side {
						moves = append(moves, Move{uint8(sq), uint8(to)})
					}
					break
				}
			}
		case Pawn:
			dr := 1
			if side == Black {
				dr = -1
			}
			if to := step(sq, 0, dr); to >= 0 && !own(side, p.Board[to]) {
				moves = append(moves, Move{uint8(sq), uint8(to)})
			}
			crossed := side == Red && r >= 5 || side == Black && r <= 4
			if crossed {
				for _, df := range [2]int{1, -1} {
					if to := step(sq, df, 0); to >= 0 && !own(side, p.Board[to]) {
						moves = append(moves, Move{uint8(sq), uint8(to)})
					}
				}
			}
		}
	}
	return moves
}

func countMoves(moves []Move) map[Move]int {
	out := make(map[Move]int, len(moves))
	for _, m := range moves {
		out[m]++
	}
	return out
}

func compareMovegen(t *testing.T, p *Position, tag string) {
	t.Helper()
	for _, side := range [2]int{Red, Black} {
		want := countMoves(genMovesRef(p, side))
		got := countMoves(p.GenMoves(side))
		for m, c := range got {
			if c > 1 {
				t.Fatalf("%s: 位运算版生成重复着法 %s ×%d\nFEN: %s", tag, m, c, p.FEN())
			}
		}
		if len(want) != len(got) {
			t.Fatalf("%s: side=%d 着法数不同 参考=%d 位运算=%d\nFEN: %s",
				tag, side, len(want), len(got), p.FEN())
		}
		for m, c := range want {
			if got[m] != c {
				t.Fatalf("%s: side=%d 缺少着法 %s（参考 %d 次，位运算 %d 次）\nFEN: %s",
					tag, side, m, c, got[m], p.FEN())
			}
		}
	}
}

func TestGenMovesMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewSource(0x6D3A1))

	compareMovegen(t, NewPosition(), "初始局面")
	compareMovegen(t, scatter(rng, 4), "稀疏")
	compareMovegen(t, scatter(rng, 32), "稠密")

	// 随机摆放：覆盖无将、多子、边界等极端布局
	for i := 0; i < 20000; i++ {
		compareMovegen(t, scatter(rng, 1+rng.Intn(32)), "随机摆放")
	}
	// 真实对局局面
	for i := 0; i < 3000; i++ {
		compareMovegen(t, randomWalk(rng, 1+rng.Intn(60)), "随机对局")
	}
}
