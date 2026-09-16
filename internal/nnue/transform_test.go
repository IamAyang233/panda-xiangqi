package nnue

import (
	"math"
	"math/rand"
	"testing"
)

// Transform 内层的 AVX2 内核必须与标量参考实现**逐位一致**。
//
// 这类逐元素整数内核的风险点不在「大概对不对」，而在几个边界行为上：
//   - psq+thr 是 int16 加法，**溢出回绕**，回绕出的值随后被 clamp 掉；
//   - 裁剪必须是有符号比较（VPMINSW/VPMAXSW），不是无符号；
//   - 乘积只取低 16 位、再逻辑右移 9 位。
//
// 上面任何一条写错，都只会让评估值偏一点点 —— 搜索照跑、棋力悄悄下降，
// 没有任何功能性测试会失败。所以这里既跑随机值，也做边界穷举。

// normHalf 用同一个生成器造出长度 n 的四段（低半段/高半段各两路）。
func normHalf(rng *rand.Rand, n int, gen func() int16) (out []byte, psqLo, thrLo, psqHi, thrHi []int16) {
	out = make([]byte, n)
	psqLo, thrLo, psqHi, thrHi = make([]int16, n), make([]int16, n), make([]int16, n), make([]int16, n)
	for i := 0; i < n; i++ {
		psqLo[i], thrLo[i], psqHi[i], thrHi[i] = gen(), gen(), gen(), gen()
	}
	return
}

func TestTransformMatchesScalar(t *testing.T) {
	if !useAVX2 {
		t.Skip("本机未启用 AVX2（或 QIJING_SIMD=off），内核不会被走到，对拍无意义")
	}
	rng := rand.New(rand.NewSource(0x20260916))

	regimes := []struct {
		name string
		gen  func() int16
	}{
		{"小值（0..255，不触发裁剪）", func() int16 { return int16(rng.Intn(256)) }},
		{"±3000（累加器的真实量级）", func() int16 { return int16(rng.Intn(6001) - 3000) }},
		{"全 int16 域（大量回绕与裁剪）", func() int16 { return int16(rng.Intn(1 << 16)) }},
	}
	for _, r := range regimes {
		for _, n := range []int{32, 64, 512, 33, 100, 1000} {
			out, pl, tl, ph, th := normHalf(rng, n, r.gen)
			want := make([]byte, n)
			transformHalfScalar(want, pl, tl, ph, th)
			transformHalf(out, pl, tl, ph, th)
			if string(out) != string(want) {
				for i := range out {
					if out[i] != want[i] {
						t.Fatalf("%s、长度 %d：第 %d 个元素 内核=%d 标量=%d（psqLo=%d thrLo=%d psqHi=%d thrHi=%d）",
							r.name, n, i, out[i], want[i], pl[i], tl[i], ph[i], th[i])
					}
				}
			}
		}
	}
}

// TestTransformMatchesScalarBoundaries 把「有意思的」取值两两组合穷举一遍。
//
// 关键在于加法回绕：psq+thr 一旦超出 int16 就会绕回负数，而绕回后的值仍然要
// 落在 clamp 的同一侧 —— 内核用的是 VPADDW（回绕）而不是先扩展到 int32 再相加，
// 这一条只有靠 max/min 附近的值才测得出来。
func TestTransformMatchesScalarBoundaries(t *testing.T) {
	if !useAVX2 {
		t.Skip("本机未启用 AVX2（或 QIJING_SIMD=off），内核不会被走到，对拍无意义")
	}
	vals := []int16{
		math.MinInt16, math.MinInt16 + 1, -32767, -512, -256, -255, -2, -1,
		0, 1, 2, 127, 128, 254, 255, 256, 510, 511, 512,
		math.MaxInt16 - 1, math.MaxInt16,
	}
	const n = 32 // 正好一组
	out := make([]byte, n)
	want := make([]byte, n)
	psqLo, thrLo, psqHi, thrHi := make([]int16, n), make([]int16, n), make([]int16, n), make([]int16, n)
	for _, a := range vals {
		for _, b := range vals {
			for i := 0; i < n; i++ {
				psqLo[i], thrLo[i], psqHi[i], thrHi[i] = a, b, b, a
			}
			transformHalfScalar(want, psqLo, thrLo, psqHi, thrHi)
			transformHalf(out, psqLo, thrLo, psqHi, thrHi)
			if string(out) != string(want) {
				t.Fatalf("a=%d b=%d：内核=%v 标量=%v", a, b, out[:4], want[:4])
			}
		}
	}
}

// ---- 基准 ----
//
// 入参每轮都换（Varying）：真实的累加器内容每个节点都不同。

func benchTransformHalf() ([]byte, []int16, []int16, []int16, []int16) {
	rng := rand.New(rand.NewSource(7))
	const n = L1 / 2
	out := make([]byte, n)
	pl, tl, ph, th := make([]int16, n), make([]int16, n), make([]int16, n), make([]int16, n)
	for i := 0; i < n; i++ {
		pl[i] = int16(rng.Intn(4097) - 2048)
		tl[i] = int16(rng.Intn(4097) - 2048)
		ph[i] = int16(rng.Intn(4097) - 2048)
		th[i] = int16(rng.Intn(4097) - 2048)
	}
	return out, pl, tl, ph, th
}

var benchByteSink byte

func BenchmarkTransformHalf(b *testing.B) {
	out, pl, tl, ph, th := benchTransformHalf()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		transformHalf(out, pl, tl, ph, th)
	}
	benchByteSink = out[0]
}

func BenchmarkTransformHalfScalarRef(b *testing.B) {
	out, pl, tl, ph, th := benchTransformHalf()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		transformHalfScalar(out, pl, tl, ph, th)
	}
	benchByteSink = out[0]
}
