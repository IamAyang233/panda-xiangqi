package nnue

import (
	"math/rand"
	"testing"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
)

// TestSIMDMatchesScalar 是 AVX2 内核的正确性关卡。
//
// 汇编写错不会报错，只会算出悄悄偏移的评估值 —— 和之前那几个污染评估的
// bug 同一类后果，所以必须逐元素与标量实现对拍。
// 权重覆盖 int8 全范围（含 -128 与负值），专门盯住符号扩展：
// 用无符号扩展会让负权重变成 +192 这类大正数。
func TestSIMDMatchesScalar(t *testing.T) {
	if !UsesAVX2() {
		t.Skip("当前 CPU/OS 不支持 AVX2，跳过（标量兜底路径另行由其他测试覆盖）")
	}

	rng := rand.New(rand.NewSource(20260915))
	w8 := make([]byte, L1)
	for i := range w8 {
		w8[i] = byte(int8(rng.Intn(256) - 128)) // -128..127
	}
	// 确保两个极端值都出现。
	w8[0], w8[1], w8[2], w8[3] = 0x80, 0x7f, 0xff, 0x00

	var base [L1]int16
	for i := range base {
		base[i] = int16(rng.Intn(4001) - 2000)
	}

	t.Run("add", func(t *testing.T) {
		got, want := base, base
		addI16AVX2(&got, w8)
		addI16Scalar(&want, w8)
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("第 %d 项不一致：AVX2=%d 标量=%d（权重 %d）",
					i, got[i], want[i], int8(w8[i]))
			}
		}
	})

	t.Run("sub", func(t *testing.T) {
		got, want := base, base
		subI16AVX2(&got, w8)
		subI16Scalar(&want, w8)
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("第 %d 项不一致：AVX2=%d 标量=%d（权重 %d）",
					i, got[i], want[i], int8(w8[i]))
			}
		}
	})

	// add 后再 sub 必须回到原值：这条同时验证了两者方向没有互相搞反
	// （VPSUBW 的语义是 dst = src2 - src1，很容易写反）。
	t.Run("add-then-sub-roundtrip", func(t *testing.T) {
		got := base
		addI16AVX2(&got, w8)
		subI16AVX2(&got, w8)
		for i := range got {
			if got[i] != base[i] {
				t.Fatalf("第 %d 项往返后未还原：%d != %d", i, got[i], base[i])
			}
		}
	})
}

// TestDetectAVX2Sanity 确认检测函数本身能给出结论（本机应当支持 AVX2）。
func TestDetectAVX2Sanity(t *testing.T) {
	t.Logf("useAVX2 = %v（本机 13th Gen Intel，应当为 true）", UsesAVX2())
}

func benchAdd(b *testing.B, fn func(*[L1]int16, []byte)) {
	w8 := make([]byte, L1)
	for i := range w8 {
		w8[i] = byte(int8(i%127 - 63))
	}
	var acc [L1]int16
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fn(&acc, w8)
	}
}

func BenchmarkAddScalar(b *testing.B) { benchAdd(b, addI16Scalar) }

func BenchmarkAddAVX2(b *testing.B) {
	if !UsesAVX2() {
		b.Skip("无 AVX2")
	}
	benchAdd(b, addI16AVX2)
}

// BenchmarkApplyIncrementalSIMD 端到端对比：整条累加器增量更新的耗时。
func BenchmarkApplyIncrementalSIMD(b *testing.B) {
	w, err := Load(flatPath)
	if err != nil {
		b.Skip("未找到展开后的权重，跳过")
	}
	type step struct{ from, to int }
	p, _ := game.ParseFEN(game.InitialFEN)
	rng := rand.New(rand.NewSource(11))

	var pos Position
	pos.ResetFromGame(&p.Board, p.Turn>>3)
	var a Accumulator
	w.Apply(&pos, &a)

	var steps []step
	for i := 0; i < 64; i++ {
		moves := p.LegalMoves(p.Turn)
		if len(moves) == 0 {
			break
		}
		m := moves[rng.Intn(len(moves))]
		steps = append(steps, step{int(m.From), int(m.To)})
		p.Make(m)
		pos.Make(int(m.From), int(m.To))
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s := steps[i%len(steps)]
		pos.Make(s.from, s.to)
		w.Apply(&pos, &a)
		pos.Unmake()
	}
}
