package game

import (
	"math/rand"
	"sort"
	"testing"
)

// legalMovesRef 是「试走判定」之前的实现：对每个伪合法着法做
// Make → InCheck → Unmake。它慢得多，只留在这里当规格。
func legalMovesRef(p *Position, side int) []Move {
	pseudo := p.GenMoves(side)
	legal := make([]Move, 0, len(pseudo))
	for _, m := range pseudo {
		p.Make(m)
		if !p.InCheck(side) {
			legal = append(legal, m)
		}
		p.Unmake()
	}
	return legal
}

func moveKey(m Move) int { return int(m.From)<<8 | int(m.To) }

func sortedMoveKeys(ms []Move) []int {
	out := make([]int, 0, len(ms))
	for _, m := range ms {
		out = append(out, moveKey(m))
	}
	sort.Ints(out)
	return out
}

func sameMoveSet(got, want []Move) bool {
	g, w := sortedMoveKeys(got), sortedMoveKeys(want)
	if len(g) != len(w) {
		return false
	}
	for i := range g {
		if g[i] != w[i] {
			return false
		}
	}
	return true
}

// TestLegalMovesMatchRef 用随机对局走出一批局面，在**每个**局面上对比快速
// 合法着法生成与 make/unmake 参考实现。
//
// 两个方向都比（轮走方与非轮走方）：LegalMoves 是导出方法，status/记谱会拿
// 非轮走方调它，两条路径都必须一致。
func TestLegalMovesMatchRef(t *testing.T) {
	rng := rand.New(rand.NewSource(0x20260916))
	var positions, inCheckPos int
	for gameNo := 0; gameNo < 60; gameNo++ {
		p := NewPosition()
		for ply := 0; ply < 80; ply++ {
			positions++
			if p.InCheck(p.Turn) {
				inCheckPos++
			}
			for _, side := range [2]int{Red, Black} {
				got := p.LegalMoves(side)
				want := legalMovesRef(p, side)
				if !sameMoveSet(got, want) {
					t.Fatalf("第 %d 局第 %d 手 side=%d：合法着法集不同（%d vs %d 个）\nFEN: %s",
						gameNo, ply, side, len(got), len(want), p.FEN())
				}
			}
			moves := p.LegalMoves(p.Turn)
			if len(moves) == 0 {
				break
			}
			p.Make(moves[rng.Intn(len(moves))])
		}
	}
	t.Logf("对拍 %d 个局面（其中被将军 %d 个）", positions, inCheckPos)
	// 将军局面是高危分支（解将着法必须全部正确），覆盖不足就说明这个测试没在
	// 该测的地方起作用。
	if inCheckPos < 20 {
		t.Errorf("被将军的局面只有 %d 个，覆盖不足", inCheckPos)
	}
}

// TestIsAttackedAfterMatchesMake 直接对比 isAttackedAfter 与
// 「Make 之后调 isAttackedBB」—— 前者是后者的等价改写，逐着法、逐局面核对。
func TestIsAttackedAfterMatchesMake(t *testing.T) {
	rng := rand.New(rand.NewSource(0x5eed2026))
	var cases, attacked int
	for gameNo := 0; gameNo < 40; gameNo++ {
		p := NewPosition()
		for ply := 0; ply < 60; ply++ {
			side := p.Turn
			them := Opponent(side)
			ksq := p.kingSq[side>>3]
			for _, m := range p.GenMoves(side) {
				from, to := int(m.From), int(m.To)
				sq := ksq
				if from == ksq {
					sq = to
				}
				got := p.bb.isAttackedAfter(sq, them, from, to, p.Board[to])

				p.Make(m)
				want := p.bb.isAttackedBB(p.kingSq[side>>3], them)
				p.Unmake()

				cases++
				if got {
					attacked++
				}
				if got != want {
					t.Fatalf("isAttackedAfter 与 Make+isAttackedBB 不一致：%v vs %v\n"+
						"着法 %d->%d，吃 %d\nFEN: %s",
						got, want, from, to, p.Board[to], p.FEN())
				}
			}
			moves := p.LegalMoves(side)
			if len(moves) == 0 {
				break
			}
			p.Make(moves[rng.Intn(len(moves))])
		}
	}
	t.Logf("对拍 %d 个「着法 × 局面」组合（其中判定为受攻 %d 个）", cases, attacked)
}
