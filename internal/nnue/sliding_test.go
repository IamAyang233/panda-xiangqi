package nnue

import (
	"math/rand"
	"testing"
)

// slidingAttackRef 是合并前的原实现，作为对拍参考保留在这里。
//
// 之所以要在测试里留一份副本而不是「用 git 历史对照」：合并版与分开调用
// 必须逐位等价，而这个等价关系要有可执行的证据。参考实现不参与生产路径，
// 只在测试中被调用，因此不会拖慢任何东西。
func slidingAttackRef(pt, sq int, occupied bitboard) bitboard {
	var attack bitboard
	for di := 0; di < 4; di++ {
		ray := rayBB[sq][di]
		blockers := ray.and(occupied)
		if blockers.isEmpty() {
			if pt == ptRook {
				attack = attack.or(ray)
			}
			continue
		}
		var fb int
		if di == 0 || di == 2 {
			fb = blockers.lsb()
		} else {
			fb = blockers.msb()
		}
		forward := rayBB[fb][di].or(bbOf(fb))
		if pt == ptRook {
			attack = attack.or(ray.andNot(forward))
			attack.set(fb)
			continue
		}
		second := rayBB[fb][di]
		after := second.and(occupied)
		if after.isEmpty() {
			attack = attack.or(second)
			continue
		}
		var nb int
		if di == 0 || di == 2 {
			nb = after.lsb()
		} else {
			nb = after.msb()
		}
		attack = attack.or(second.andNot(rayBB[nb][di]))
	}
	return attack
}

// TestSlidingAttackBothMatchesRef 保证合并版与分开调用逐位等价。
//
// 合并的动机是热路径（updateThreats）总要车与炮两套结果，而两者的射线与
// 阻挡完全相同。这里覆盖三类容易出问题的占用形态：
//   - 空棋盘：车可以走到底、炮一无所得（两者在该分支行为不同）
//   - 密集棋盘：几乎每个方向都立刻撞上阻挡
//   - 随机棋盘：包含各种炮架距离
//
// 遍历全部 90 个格子 × 两个兵种，逐位比较。
func TestSlidingAttackBothMatchesRef(t *testing.T) {
	check := func(name string, occ bitboard) {
		for sq := 0; sq < squareNB; sq++ {
			gotR, gotC := slidingAttackBoth(sq, occ)
			wantR := slidingAttackRef(ptRook, sq, occ)
			wantC := slidingAttackRef(ptCannon, sq, occ)
			if gotR != wantR {
				t.Fatalf("%s：车在 %d 格不一致\n合并 %v\n参考 %v", name, sq, gotR, wantR)
			}
			if gotC != wantC {
				t.Fatalf("%s：炮在 %d 格不一致\n合并 %v\n参考 %v", name, sq, gotC, wantC)
			}
		}
	}

	check("空棋盘", bitboard{})

	var full bitboard
	for s := 0; s < squareNB; s++ {
		full.set(s)
	}
	check("满棋盘", full)

	rng := rand.New(rand.NewSource(20260915))
	for iter := 0; iter < 300; iter++ {
		var occ bitboard
		for n := rng.Intn(24); n > 0; n-- {
			occ.set(rng.Intn(squareNB))
		}
		check("随机棋盘", occ)
	}

	// 单个子放在同行/同列的不同距离上，专门覆盖炮架的各种间距。
	for d := 1; d <= 8; d++ {
		var occ bitboard
		base := 4*9 + 4 // 棋盘中部，四个方向都有足够空间
		if base+d < squareNB {
			occ.set(base + d)
		}
		check("东侧单子", occ)

		var occ2 bitboard
		if base+9*d < squareNB {
			occ2.set(base + 9*d)
		}
		check("北侧单子", occ2)
	}

	t.Logf("合并版与参考实现在全部 90 格、两个兵种上逐位一致 ✓")
}

func BenchmarkSlidingAttackBoth(b *testing.B) {
	var occ bitboard
	rng := rand.New(rand.NewSource(5))
	for i := 0; i < 20; i++ {
		occ.set(rng.Intn(squareNB))
	}
	sq := 4*9 + 4
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = slidingAttackBoth(sq, occ)
	}
}

// BenchmarkSlidingAttackSplit 是合并前的调用形态（车、炮各算一遍）。
//
// 必须调 slidingAttackRef 而不是 slidingAttack —— 后者现在也委托给合并版，
// 那样测出来的会是「合并版跑两遍」，量不到任何收益。
func BenchmarkSlidingAttackSplit(b *testing.B) {
	var occ bitboard
	rng := rand.New(rand.NewSource(5))
	for i := 0; i < 20; i++ {
		occ.set(rng.Intn(squareNB))
	}
	sq := 4*9 + 4
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = slidingAttackRef(ptRook, sq, occ)
		_ = slidingAttackRef(ptCannon, sq, occ)
	}
}
