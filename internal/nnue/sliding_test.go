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

// TestSlidingAttackDirMatchesBoth 保证「只算一个方向」与「四个方向全算」在
// 该方向上的取值逐位相同。
//
// 动机：computeRay 段只需要 s 所在的那一个方向（另外三个方向在
// before/after 的对称差里必然抵消），于是那里改调 slidingAttackDir。
// 这个等价关系是本项目里第二次「同一套逻辑拆成两个入口」，必须有可执行的
// 证据 —— 一旦两者分叉，症状会是 NNUE 特征悄悄偏移，而不是报错。
//
// 逐位比较方式：slidingAttackBoth 的另外三个方向落在与 rayBB[sq][di] 不相交的
// 格子上，所以把它与该射线求交就得到「该方向的贡献」。
func TestSlidingAttackDirMatchesBoth(t *testing.T) {
	check := func(name string, occ bitboard) {
		for sq := 0; sq < squareNB; sq++ {
			fullR, fullC := slidingAttackBoth(sq, occ)
			for d := 0; d < 4; d++ {
				ray := rayBB[sq][d]
				gotR, gotC := slidingAttackDir(sq, d, occ)
				wantR := fullR.and(ray)
				wantC := fullC.and(ray)
				if gotR != wantR {
					t.Fatalf("%s：车 sq=%d d=%d 不一致\n单方向 %v\n全方向∩射线 %v",
						name, sq, d, gotR, wantR)
				}
				if gotC != wantC {
					t.Fatalf("%s：炮 sq=%d d=%d 不一致\n单方向 %v\n全方向∩射线 %v",
						name, sq, d, gotC, wantC)
				}
			}
		}
	}

	check("空棋盘", bitboard{})

	var full bitboard
	for s := 0; s < squareNB; s++ {
		full.set(s)
	}
	check("满棋盘", full)

	rng := rand.New(rand.NewSource(20260916))
	// 稀疏占用必须单独覆盖：炮架距离只有 0/1/2 三种情形，稀疏时才会出现
	// 「空射线」「只有一个子」这些边界，密集棋盘几乎全是「立刻撞阻挡」。
	for _, pieces := range []int{1, 2, 3, 8, 24} {
		for iter := 0; iter < 200; iter++ {
			var occ bitboard
			for n := pieces; n > 0; n-- {
				occ.set(rng.Intn(squareNB))
			}
			check("随机棋盘", occ)
		}
	}

	// 同行/同列的单个子，覆盖炮架的各种间距。
	base := 4*9 + 4
	for d := 1; d <= 8; d++ {
		var occ bitboard
		if base+d < squareNB {
			occ.set(base + d)
			check("东侧单子", occ)
		}
		var occ2 bitboard
		if base+9*d < squareNB {
			occ2.set(base + 9*d)
			check("北侧单子", occ2)
		}
	}

	t.Logf("单方向版与合并版在全部 90 格 × 4 方向 × 多类占用上逐位一致 ✓")
}

// benchSink* 用于防止基准测试的调用被编译器整体消除。
//
// 这一点必须较真：原来写的是 `_, _ = slidingAttackBoth(sq, occ)` 且 sq/occ
// 都是常量 —— 函数结果未被使用，编译器完全可以把整个调用删掉。
// 那样量出来的数字是假的，会严重误导优化决策。
var (
	benchSinkRook bitboard
	benchSinkCann bitboard
)

// benchOccupancies 生成一组「像真实局面」的占用集：32 个子力散布在 90 格上。
//
// 固定用同一个 occupied 反复算，分支会被完美预测、rayBB 的索引恒为同一个，
// 测出的是乐观下限；真实搜索里 occupied 与格子每步都在变。
func benchOccupancies(n int) []bitboard {
	out := make([]bitboard, n)
	for i := range out {
		rng := rand.New(rand.NewSource(int64(i)*2654435761 + 7))
		for k := 0; k < 32; k++ {
			out[i].set(rng.Intn(squareNB))
		}
	}
	return out
}

func benchSquares(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i % squareNB
	}
	return out
}

// BenchmarkSlidingAttackBoth 测合并版滑动攻击。
//
// 分两个子基准：
//   - Fixed：同一 occupied 反复算 —— 分支完美预测、表全在 L1，乐观下限
//   - Varying：occupied 与格子都在变 —— 接近真实搜索的形态
//
// 两者之差就是分支预测失败与访存局部性的代价。只看 Fixed 会让人误判
// 「这段代码已经很便宜、不值得优化」。
func BenchmarkSlidingAttackBoth(b *testing.B) {
	occs := benchOccupancies(256)
	sqs := benchSquares(256)

	b.Run("Fixed", func(b *testing.B) {
		occ := occs[0]
		sq := 4*9 + 4
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			benchSinkRook, benchSinkCann = slidingAttackBoth(sq, occ)
		}
	})

	b.Run("Varying", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			j := i & 255
			benchSinkRook, benchSinkCann = slidingAttackBoth(sqs[j], occs[j])
		}
	})
}

// BenchmarkSlidingAttackSplit 是合并前的调用形态（车、炮各算一遍）。
//
// 必须调 slidingAttackRef 而不是 slidingAttack —— 后者现在也委托给合并版，
// 那样测出来的会是「合并版跑两遍」，量不到任何收益。
func BenchmarkSlidingAttackSplit(b *testing.B) {
	occs := benchOccupancies(256)
	sqs := benchSquares(256)

	b.Run("Fixed", func(b *testing.B) {
		occ := occs[0]
		sq := 4*9 + 4
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			benchSinkRook = slidingAttackRef(ptRook, sq, occ)
			benchSinkCann = slidingAttackRef(ptCannon, sq, occ)
		}
	})

	b.Run("Varying", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			j := i & 255
			benchSinkRook = slidingAttackRef(ptRook, sqs[j], occs[j])
			benchSinkCann = slidingAttackRef(ptCannon, sqs[j], occs[j])
		}
	})
}

// BenchmarkSlidingAttackDir 是 computeRay 段改用的形态：只算一个方向。
//
// 与 BenchmarkSlidingAttackBoth 对照，量「四方向 → 单方向」省了多少。
// 入参仍每轮都换（含方向），因为真实调用点是「候选子 psq + s 决定的方向」，
// 两者都在变 —— 固定方向会让分支与表索引被完美预测。
func BenchmarkSlidingAttackDir(b *testing.B) {
	occs := benchOccupancies(256)
	sqs := benchSquares(256)

	b.Run("Varying", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			j := i & 255
			benchSinkRook, benchSinkCann = slidingAttackDir(sqs[j], j&3, occs[j])
		}
	})
}
