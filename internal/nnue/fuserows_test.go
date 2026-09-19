package nnue

import (
	"math/rand"
	"testing"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
)

// 本文件是「两行合一」内核（addRows2I16AVX2）的正确性关卡。
//
// 融合改变的是同一片累加器上各权重行的**累加次序**：先在两行之间合并、
// 再累加一次，而不是分两次累加。逐位正确性完全依赖环绕加满足交换结合律
// （int16 加法模 2^16）。这条论证一旦被破坏 —— 比如内核被换成饱和加
// （PADDSUW/PADDSW）—— 评估值会静默偏移而不报任何错，搜索只会慢慢选错着法。
// 所以必须逐局面、逐元素把「融合」与「单行」两条路径对上。
//
// 分两层：内核层直接喂已知权重行比对；集成层走同一串局面、只差开关，
// 逐检查点比对累加器指纹。

// TestFuseRowsKernelMatchesScalar 直接对内核：acc += w1 ± w2 必须与
// 「两次单行调用」逐位相同。这一层不依赖搜索或 Apply 的任何状态，
// 所以不可能被上层逻辑掩盖成「碰巧相等」。
func TestFuseRowsKernelMatchesScalar(t *testing.T) {
	w, err := Load(flatPath)
	if err != nil {
		t.Skip("未找到展开后的权重，跳过")
	}
	if !useAVX2 {
		t.Skip("无 AVX2 内核，融合路径不存在（标量模式两侧本就同一条路径）")
	}

	rng := rand.New(rand.NewSource(0xF05E))
	const trials = 64
	rows := len(w.W) / L1
	// 与累加器的初值一样取真实量级，避免只在零点附近比较。
	seed := func(x *[L1]int16) {
		for i := range x {
			x[i] = int16(rng.Intn(4001) - 2000)
		}
	}

	for i := 0; i < trials; i++ {
		a1, a2 := rng.Intn(rows), rng.Intn(rows)
		r1 := w.W[a1*L1 : a1*L1+L1]
		r2 := w.W[a2*L1 : a2*L1+L1]

		var base [L1]int16
		seed(&base)

		for _, sub2 := range []bool{false, true} {
			got, ref := base, base
			addRows2Fused(&got, r1, r2, sub2)

			addI16(&ref, r1)
			if sub2 {
				subI16(&ref, r2)
			} else {
				addI16(&ref, r2)
			}

			if got != ref {
				for k := 0; k < L1; k++ {
					if got[k] != ref[k] {
						t.Fatalf("融合与单行不一致：行 %d/%d sub2=%v，首个差异 k=%d 融合=%d 单行=%d",
							a1, a2, sub2, k, got[k], ref[k])
					}
				}
			}
		}
	}
	t.Logf("内核层：%d 组随机权重行 × 加减两种组合，融合与单行逐位一致 ✓", trials)
}

// TestFuseRowsMatchesSingle 在集成层对拍：同一串局面走两遍，两遍只差
// useFuseRows，逐检查点比较累加器指纹。
//
// ⚠️ 这一层必须确认融合真的被走到过 —— 否则两侧走的是同一条路径，
// 测试会「因为没测到而通过」。所以最后断言配对次数大于 0。
func TestFuseRowsMatchesSingle(t *testing.T) {
	w, err := Load(flatPath)
	if err != nil {
		t.Skip("未找到展开后的权重，跳过")
	}
	if !useAVX2 {
		t.Skip("无 AVX2 内核，融合路径不存在")
	}

	fens := []string{
		game.InitialFEN,
		"3k5/9/9/9/9/9/9/9/9/4K4 w - - 0 1",
		"rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR b - - 0 1",
		"3k5/2P1P4/4b4/9/9/9/9/4p1p2/2p1p4/3K1C3 w - - 0 1",
		"2bak4/9/4c4/9/9/9/9/4C4/9/3AK4 w - - 0 1",
	}

	// 打开诊断计数（永久计数器，见 diag.go）：FusePairs 统计
	// applyThreats 里真正交给融合内核的配对数，用来证明这条路径不是空跑。
	EnableDiag(true)
	defer EnableDiag(false)

	// walk 用固定种子走一遍，返回每个检查点上的累加器指纹。
	walk := func(fuse bool) ([]uint64, int64) {
		old := useFuseRows
		useFuseRows = fuse
		defer func() { useFuseRows = old }()

		ResetDiag()
		var dig []uint64

		for _, fen := range fens {
			p, err := game.ParseFEN(fen)
			if err != nil {
				t.Fatalf("解析 %s 失败: %v", fen, err)
			}
			rng := rand.New(rand.NewSource(int64(len(fen)) * 7919))

			var pos Position
			pos.ResetFromGame(&p.Board, p.Turn>>3)
			var acc Accumulator

			record := func() {
				var h uint64 = 0xcbf29ce484222325
				mix := func(v int16) { h = (h ^ uint64(uint16(v))) * 0x100000001b3 }
				for c := 0; c < colorNB; c++ {
					for i := 0; i < L1; i++ {
						mix(acc.PsqAcc[c][i])
						mix(acc.ThrAcc[c][i])
					}
					for k := 0; k < PSQTBuckets; k++ {
						mix(int16(acc.PsqPsqt[c][k]))
						mix(int16(acc.ThrPsqt[c][k]))
					}
				}
				dig = append(dig, h)
			}

			w.Apply(&pos, &acc)
			record()

			depth := 0
			for round := 0; round < 40; round++ {
				adv := 1 + rng.Intn(3)
				for i := 0; i < adv; i++ {
					moves := p.LegalMoves(p.Turn)
					if len(moves) == 0 {
						break
					}
					m := moves[rng.Intn(len(moves))]
					p.Make(m)
					pos.Make(int(m.From), int(m.To))
					depth++
				}
				if depth > 0 {
					w.Apply(&pos, &acc)
					record()
				}
				for back := rng.Intn(3); back > 0 && depth > 0; back-- {
					p.Unmake()
					pos.Unmake()
					depth--
					w.Apply(&pos, &acc)
					record()
				}
			}
		}
		return dig, DiagSnapshot().FusePairs
	}

	digSingle, _ := walk(false)
	digFused, pairs := walk(true)

	if len(digSingle) != len(digFused) {
		t.Fatalf("两遍的检查点数不同：单行 %d，融合 %d", len(digSingle), len(digFused))
	}
	for i := range digSingle {
		if digSingle[i] != digFused[i] {
			t.Fatalf("第 %d 个检查点：融合与单行的累加器不同（单行 %#x，融合 %#x）",
				i, digSingle[i], digFused[i])
		}
	}
	if pairs == 0 {
		t.Fatal("融合内核一次都没被走到 —— 这个测试没有测到目标路径")
	}
	t.Logf("集成层：%d 个检查点，融合路径触发 %d 次配对，逐位一致 ✓", len(digSingle), pairs)
}
