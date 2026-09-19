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

		// 三种模式都要过：两个加 / 一加一减 / 两个减。
		for _, mode := range []uint8{rowAddAdd, rowAddSub, rowSubSub} {
			got, ref := base, base
			addRows2Fused(&got, r1, r2, mode)

			switch mode {
			case rowSubSub:
				subI16(&ref, r1)
				subI16(&ref, r2)
			case rowAddAdd:
				addI16(&ref, r1)
				addI16(&ref, r2)
			default:
				addI16(&ref, r1)
				subI16(&ref, r2)
			}

			if got != ref {
				for k := 0; k < L1; k++ {
					if got[k] != ref[k] {
						t.Fatalf("融合与单行不一致：行 %d/%d mode=%d，首个差异 k=%d 融合=%d 单行=%d",
							a1, a2, mode, k, got[k], ref[k])
					}
				}
			}
		}
	}
	t.Logf("内核层：%d 组随机权重行 × 加减两种组合，融合与单行逐位一致 ✓", trials)
}

// TestFuseRowsMatchesSingle 在集成层对拍：同一串局面走四遍，四遍只差
// 「配对开关 × 融合开关」，逐检查点比较累加器指纹。
//
// 四个组合必须两两逐位相同 —— 因为 (关配对, 关融合) 是「全部走单行」的
// 原始形态，而 (开配对, 开融合) 是「尽量把行合成一趟」的形态；两者结果一致
// 就等于证明了这一整条改造（配对重排 + 两行合一内核）没有改变任何数值。
//
// ⚠️ 这一层必须确认配对路径真的被走到过 —— 否则四个组合走的其实都是单行，
// 测试会「因为没测到而通过」。所以最后断言配对数大于 0。
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
	walk := func(fuse, pair bool) ([]uint64, int64) {
		oldFuse, oldPair := useFuseRows, useRowPairing
		useFuseRows, useRowPairing = fuse, pair
		defer func() { useFuseRows, useRowPairing = oldFuse, oldPair }()

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

	combos := []struct {
		name       string
		fuse, pair bool
	}{
		{"全单行(原始形态)", false, false},
		{"只配对不融合", true, false},
		{"只融合不配对", false, true},
		{"配对+融合", true, true},
	}
	base, _ := walk(combos[0].fuse, combos[0].pair)
	pairs := int64(0)
	for i, c := range combos {
		if i == 0 {
			continue
		}
		dig, p := walk(c.fuse, c.pair)
		if p > pairs {
			pairs = p
		}
		if len(dig) != len(base) {
			t.Fatalf("%s：检查点数不同（%d vs %d）", c.name, len(dig), len(base))
		}
		for k := range base {
			if dig[k] != base[k] {
				t.Fatalf("%s：第 %d 个检查点与「全单行」不同（%#x vs %#x）",
					c.name, k, dig[k], base[k])
			}
		}
	}
	if pairs == 0 {
		t.Fatal("配对路径一次都没被走到 —— 这个测试没有测到目标路径")
	}
	t.Logf("集成层：%d 个检查点 × %d 个开关组合，逐位一致；配对最多触发 %d 次 ✓",
		len(base), len(combos), pairs)
}
