package nnue

import (
	"math/rand"
	"testing"
)

// TestFC1MatchesScalar 逐位对拍 fc_1 内核与标量参考实现。
//
// 这一项是必要的：fc1 的输出直接进入 fc_2 与最终评估值，错了不会 panic，
// 只会让棋力静默下降。而现有的 TestPositionalMatchesPikafish 只比对「比值
// 的离散度」（容差 0.5），挡不住细微的数值错误 —— 必须有逐位对拍。
//
// 两组用例的差别在**权重行的填充字节**（每行末两字节）：
//   - 填充为 0：与权重文件的实际情形一致
//   - 填充为非零：证明「内核把整 32 字节都乘进去」这件事不依赖填充内容 ——
//     因为 act 的末两字节恒为 0，乘任何权重都得 0。生产代码里那个数组是
//     零值新建的，这条不变式一旦被破坏，本组会立刻失败。
func TestFC1MatchesScalar(t *testing.T) {
	rng := rand.New(rand.NewSource(20260916))

	for _, tc := range []struct {
		name      string
		padNonzro bool
	}{
		{"权重填充为 0", false},
		{"权重填充为非零", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for trial := 0; trial < 64; trial++ {
				w := make([]byte, L3*FC1PaddedIn)
				for i := range w {
					if !tc.padNonzro && i%FC1PaddedIn >= FC1In {
						continue // 填充字节留 0
					}
					w[i] = byte(rng.Intn(256))
				}
				bias := make([]int32, L3)
				for j := range bias {
					bias[j] = int32(rng.Intn(20001) - 10000)
				}
				var act [FC1PaddedIn]byte
				for i := 0; i < FC1In; i++ {
					act[i] = byte(rng.Intn(128)) // ClippedReLU 的取值域是 0..127
				}
				// act[FC1In]、act[FC1In+1] 保持 0，与生产路径一致。

				var got, want [L3]int32
				fc1Forward(&got, w, bias, &act)
				fc1ScalarRef(&want, w, bias, &act)
				for j := 0; j < L3; j++ {
					if got[j] != want[j] {
						t.Fatalf("第 %d 次试验、输出 %d 不一致：内核 %d，参考 %d",
							trial, j, got[j], want[j])
					}
				}
			}
		})
	}
}

// TestFC1MatchesScalarExtremes 覆盖乘积幅值的两个极端。
//
// 内核用 VPMADDUBSW，把相邻两对乘积之和**饱和**到 i16。激活上限 127 是
// ClippedReLU 保证的，所以单对最坏是 127*127 + 127*127 = 32258 < 32767，
// 不会饱和；但这条边界必须实测钉住 —— 一旦将来激活域放宽，这里会先失败。
func TestFC1MatchesScalarExtremes(t *testing.T) {
	fill := func(b byte) []byte {
		w := make([]byte, L3*FC1PaddedIn)
		for i := range w {
			w[i] = b
		}
		return w
	}
	bias := make([]int32, L3)

	for _, tc := range []struct {
		name string
		wb   byte
		ab   byte
	}{
		{"权重 127、激活 127", 127, 127},
		{"权重 -128、激活 127", 0x80, 127},
		{"权重 -128、激活 0", 0x80, 0},
		{"权重 127、激活 0", 127, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := fill(tc.wb)
			var act [FC1PaddedIn]byte
			for i := 0; i < FC1In; i++ {
				act[i] = tc.ab
			}

			var got, want [L3]int32
			fc1Forward(&got, w, bias, &act)
			fc1ScalarRef(&want, w, bias, &act)
			if got != want {
				t.Fatalf("不一致：内核 %v，参考 %v", got, want)
			}
			t.Logf("输出 = %d", got[0])
		})
	}
}

// BenchmarkFC1 对照内核与标量实现。
//
// 保留标量侧的理由与 BenchmarkSlidingAttackSplit 相同：让「改造带来多少」
// 有可复现的来源，而不是只留在提交信息里。
func BenchmarkFC1(b *testing.B) {
	w, err := Load(flatPath)
	if err != nil {
		b.Skip("未找到展开后的权重，跳过")
	}
	ls := w.Layers[0]
	var act [FC1PaddedIn]byte
	for i := 0; i < FC1In; i++ {
		act[i] = byte(i*5 + 7)
	}
	bias := ls.FC1Bias

	b.Run("Kernel", func(b *testing.B) {
		var out [L3]int32
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			fc1Forward(&out, ls.FC1W, bias, &act)
		}
		fc1Sink = out
	})
	b.Run("ScalarRef", func(b *testing.B) {
		var out [L3]int32
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			fc1ScalarRef(&out, ls.FC1W, bias, &act)
		}
		fc1Sink = out
	})
}

var fc1Sink [L3]int32
