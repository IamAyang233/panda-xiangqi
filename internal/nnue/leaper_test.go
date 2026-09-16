package nnue

import "testing"

// lameLeaperAttackRef 是「查表改造」之前的原实现，作为对拍参考保留在测试里。
//
// 与 slidingAttackRef 同理：改造必须逐位等价，而这个等价关系要有可执行的证据 ——
// 不能只靠「同一个函数名、看代码觉得一样」。参考实现不参与生产路径。
func lameLeaperAttackRef(pt, s int, occupied bitboard) bitboard {
	var b bitboard
	dirs := knightDirections[:]
	if pt == ptBishop {
		dirs = bishopDirections[:]
	}
	for _, d := range dirs {
		to := s + d
		if !okSquare(to) || chebyshev(s, to) >= 3 {
			continue
		}
		if leg := lameLeaperPath(pt, d, s); leg >= 0 && occupied.test(leg) {
			continue
		}
		b.set(to)
	}
	if pt == ptBishop {
		side := 0
		if rankOf(s) > 4 {
			side = 1
		}
		b = b.and(halfBB[side])
	}
	return b
}

// TestLameLeaperAttackMatchesRef 保证查表版与逐方向现算版逐位等价。
//
// 覆盖全部 90 格 × 象/马两兵种 × 空盘与 64 种随机占用。空盘这一档必须单独测：
// pseudoAttacks[ptBishop] / [ptKnight] 就是拿空占用集建出来的，一旦查表版在
// 空盘上算错，整张 pseudoAttacks 会静默变错，而那时**任何功能性测试都不会报错**，
// 只会表现为威胁特征整体偏移。
func TestLameLeaperAttackMatchesRef(t *testing.T) {
	occs := append([]bitboard{{}}, benchOccupancies(64)...)

	for _, tc := range []struct {
		name string
		pt   int
	}{
		{"象", ptBishop},
		{"马", ptKnight},
	} {
		for s := 0; s < squareNB; s++ {
			for oi, occ := range occs {
				got := lameLeaperAttack(tc.pt, s, occ)
				want := lameLeaperAttackRef(tc.pt, s, occ)
				if got != want {
					t.Fatalf("%s：格 %d、占用样本 %d 不一致 —— 查表版 %v，参考版 %v",
						tc.name, s, oi, got, want)
				}
			}
		}
	}
}

// BenchmarkLameLeaperAttack 对照查表版与逐方向现算版。
//
// 保留「旧实现」这一侧的理由与 BenchmarkSlidingAttackSplit 相同：让「改造带来
// 多少」这个数字有可复现的来源，而不是只留在提交信息里。
func BenchmarkLameLeaperAttack(b *testing.B) {
	occs := benchOccupancies(256)
	sqs := benchSquares(256)

	run := func(name string, fn func(pt, s int, occ bitboard) bitboard, pt int) {
		b.Run(name, func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				j := i & 255
				benchSinkLeaper = fn(pt, sqs[j], occs[j])
			}
		})
	}
	run("Bishop-New", lameLeaperAttack, ptBishop)
	run("Bishop-Ref", lameLeaperAttackRef, ptBishop)
	run("Knight-New", lameLeaperAttack, ptKnight)
	run("Knight-Ref", lameLeaperAttackRef, ptKnight)
}

var benchSinkLeaper bitboard

// TestLeaperTablesCoverAllDirections 守住落点表的完整性：
// 「有效落点的象眼/腿位一定不是 -1」是差分测试之外的结构性检查 ——
// 若将来有人改动表的构建条件，象眼/腿位算成 -1 会让 occupied.test(-1) 越界 panic
// 或静默读到错误格。
func TestLeaperTablesCoverAllDirections(t *testing.T) {
	for s := 0; s < squareNB; s++ {
		for i := 0; i < 4; i++ {
			if bishopTo[s][i] >= 0 && bishopEye[s][i] < 0 {
				t.Fatalf("格 %d 方向 %d：落点 %d 有效但象眼为 -1", s, i, bishopTo[s][i])
			}
		}
		for i := 0; i < 8; i++ {
			if knightTo[s][i] >= 0 && knightLeg[s][i] < 0 {
				t.Fatalf("格 %d 方向 %d：落点 %d 有效但腿位为 -1", s, i, knightTo[s][i])
			}
		}
	}
}
