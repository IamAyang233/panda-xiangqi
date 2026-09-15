package nnue

import (
	"math"
	"testing"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
)

// 本文件是 NNUE 前向推理的**验收测试**：与 C++ 皮卡鱼的 `eval` 输出逐桶比对。
//
// 比对方法：皮卡鱼 `eval` 的 "NNUE network contributions" 表会为 16 个 LayerStack
// 桶各输出一行 (PSQT, Positional, Total)。它的显示值经过 `to_cp` 缩放，系数
// a = win_rate_params(pos) 依赖局面但**不依赖桶**。所以同一局面的 16 个桶共用
// 同一个换算系数 —— 于是可以绕开 a 的具体取值，只要求
// `本实现值 / 皮卡鱼显示值` 在 16 个桶之间保持恒定。
//
// 复现 golden 的命令：
//   cd dist && printf 'position fen <FEN>\neval\nquit\n' | ./pikafish.exe
//
// 小值桶受皮卡鱼 2 位小数舍入影响大，按阈值跳过。

type pikafishCase struct {
	name string
	fen  string
	// psqt、positional 是皮卡鱼表格的两列，单位 pawns。
	psqt       []float64
	positional []float64
}

var pikafishCases = []pikafishCase{
	{
		name: "初始局面",
		fen:  game.InitialFEN,
		psqt: []float64{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
		positional: []float64{
			1.41, 0.07, 1.92, 0.03, 1.02, 0.11, 0.68, 0.13,
			0.61, 0.39, 0.09, 0.22, 1.75, -1.31, 2.47, -0.71,
		},
	},
	{
		name: "极简将帅局面",
		fen:  "3k5/9/9/9/9/9/9/9/9/4K4 w - - 0 1",
		psqt: []float64{
			0.04, 0.00, -0.03, -0.01, -0.00, 0.02, 0.04, 0.03,
			-0.21, -0.53, 0.11, -0.01, -0.05, 0.32, 0.20, 0.05,
		},
		positional: []float64{
			0.35, -0.01, 1.78, -0.08, -1.22, -0.07, 0.12, 0.20,
			0.31, 0.10, 1.63, -0.40, 3.11, -0.95, 6.81, -4.06,
		},
	},
	{
		name: "开局炮二平五后",
		fen:  "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C2C4/9/RNBAKABNR w - - 0 1",
		psqt: []float64{
			0.00, 0.07, 0.13, 0.07, 0.06, 0.06, 0.10, 0.09,
			0.13, 0.11, 0.14, 0.13, 0.11, 0.11, 0.02, 0.02,
		},
		positional: []float64{
			1.46, 0.29, 2.06, 0.11, 1.28, 0.23, 0.82, 0.29,
			1.07, 1.00, 0.63, 0.65, 1.88, -1.04, 3.07, 0.62,
		},
	},
	{
		name: "单兵对将",
		fen:  "3k5/9/9/9/9/9/9/9/4P4/4K4 w - - 0 1",
		psqt: []float64{
			0.04, 0.00, -0.03, -0.01, -0.00, 0.02, 0.04, 0.03,
			-0.21, -0.53, 0.11, -0.01, -0.05, 0.32, 0.20, 0.05,
		},
		positional: []float64{
			1.29, 0.07, 3.17, -0.04, -1.19, -0.05, 0.77, 1.05,
			3.00, 1.11, 3.94, 0.55, 2.24, 4.49, 6.34, -3.92,
		},
	},
}

// TestPSQTMatchesPikafish PSQT 路径验收：逐桶同号且比值恒定。
func TestPSQTMatchesPikafish(t *testing.T) {
	w, err := Load(flatPath)
	if err != nil {
		t.Skip("未找到展开后的权重，跳过")
	}
	for _, c := range pikafishCases {
		t.Run(c.name, func(t *testing.T) {
			b := boardFromFEN(t, c.fen)
			var a Accumulator
			w.Refresh(b, &a)

			lo, hi, n := math.Inf(1), math.Inf(-1), 0
			for bucket := 0; bucket < LayerStacks; bucket++ {
				psqt, _ := a.Transform(colorWhite, bucket)
				if math.Abs(c.psqt[bucket]) < 0.1 {
					continue
				}
				if psqt != 0 && (psqt > 0) != (c.psqt[bucket] > 0) {
					t.Errorf("bucket %d 符号相反：本实现 %d，皮卡鱼 %+.2f", bucket, psqt, c.psqt[bucket])
				}
				r := float64(psqt) / (c.psqt[bucket] * 16)
				lo, hi = math.Min(lo, r), math.Max(hi, r)
				n++
			}
			if n == 0 {
				t.Skip("该局面 psqt 全为 0，无法标定")
			}
			spread := (hi - lo) / ((hi + lo) / 2)
			if spread > 0.15 {
				t.Errorf("PSQT 比值离散度过大：%.3f（范围 %.0f ~ %.0f）", spread, lo, hi)
			}
			t.Logf("PSQT 路径一致：%d 个桶，比值 %.0f ~ %.0f（离散度 %.1f%%）", n, lo, hi, spread*100)
		})
	}
}

// TestPositionalMatchesPikafish Positional 路径验收：逐桶比值恒定，
// 且与同局面由 PSQT 标定的换算系数一致。
func TestPositionalMatchesPikafish(t *testing.T) {
	w, err := Load(flatPath)
	if err != nil {
		t.Skip("未找到展开后的权重，跳过")
	}
	for _, c := range pikafishCases {
		t.Run(c.name, func(t *testing.T) {
			b := boardFromFEN(t, c.fen)
			var a Accumulator
			w.Refresh(b, &a)

			lo, hi, n := math.Inf(1), math.Inf(-1), 0
			for bucket := 0; bucket < LayerStacks; bucket++ {
				_, feat := a.Transform(colorWhite, bucket)
				pos := w.Layers[bucket].propagate(&feat)
				if math.Abs(c.positional[bucket]) < 0.15 {
					continue
				}
				r := float64(pos) / c.positional[bucket]
				lo, hi = math.Min(lo, r), math.Max(hi, r)
				n++
				t.Logf("bucket %2d: pos=%9d 皮卡鱼=%+.2f 比值=%.2f", bucket, pos, c.positional[bucket], r)
			}
			if n < 8 {
				t.Fatalf("可用于比对的桶太少：%d", n)
			}
			spread := (hi - lo) / ((hi + lo) / 2)
			if spread > 0.5 {
				t.Errorf("Positional 比值离散度过大：%.3f（范围 %.0f ~ %.0f）", spread, lo, hi)
			}
			if k := calibrate(t, w, b, c); k > 0 {
				mean := (hi + lo) / 2
				d := math.Abs(mean-k) / k
				if d > 0.25 {
					t.Errorf("PSQT 标定系数 %.0f 与 Positional 比值均值 %.0f 相差 %.1f%%", k, mean, d*100)
				}
				t.Logf("PSQT 标定系数=%.0f，Positional 比值均值=%.0f（相差 %.1f%%）", k, mean, d*100)
			}
		})
	}
}

// calibrate 用同一局面的 PSQT 列标定换算系数（PSQT 路径已独立验收）。
//
// Transform 与 propagate 返回的都是未缩放的 int32，C++ 侧再除以 OutputScale 得到
// Value，显示时又除以 a。所以「int32 值 / 显示值」在两者上是同一个量（= OutputScale * a）。
// 返回 0 表示该局面的 psqt 全为 0、无法标定。
func calibrate(t *testing.T, w *Weights, b Board, c pikafishCase) float64 {
	t.Helper()
	var a Accumulator
	w.Refresh(b, &a)
	sum, n := 0.0, 0
	for bucket := 0; bucket < LayerStacks; bucket++ {
		if math.Abs(c.psqt[bucket]) < 0.1 {
			continue
		}
		psqt, _ := a.Transform(colorWhite, bucket)
		sum += float64(psqt) / c.psqt[bucket]
		n++
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

// TestTraceLayers 逐层打印前向推理中间值，供排查偏差使用。
func TestTraceLayers(t *testing.T) {
	w, err := Load(flatPath)
	if err != nil {
		t.Skip("未找到展开后的权重，跳过")
	}
	b := boardFromFEN(t, "3k5/9/9/9/9/9/9/9/9/4K4 w - - 0 1")
	var a Accumulator
	w.Refresh(b, &a)

	bucket := b.LayerStackBucket(colorWhite)
	psqt, feat := a.Transform(colorWhite, bucket)
	t.Logf("bucket=%d psqt=%d（皮卡鱼显示 bucket 1 被使用）", bucket, psqt)

	nz, mx := 0, 0
	for _, v := range feat {
		if v != 0 {
			nz++
		}
		if int(v) > mx {
			mx = int(v)
		}
	}
	t.Logf("features: 非零 %d/1024 最大 %d", nz, mx)

	ls := &w.Layers[bucket]
	var fc0 [FC0Out]int32
	for j := 0; j < FC0Out; j++ {
		base := j * FC0PaddedIn
		s := ls.FC0Bias[j]
		for i := 0; i < L1; i++ {
			s += int32(int8(ls.FC0W[base+i])) * int32(feat[i])
		}
		fc0[j] = s
	}
	var fc1in [FC1PaddedIn]byte
	for i := 0; i < FC0Out; i++ {
		fc1in[i] = sqrClippedReLU(fc0[i])
	}
	for i := 0; i < FC0Out-1; i++ {
		fc1in[FC0Out-1+i] = clippedReLU(fc0[i])
	}
	var fc1 [L3]int32
	for j := 0; j < L3; j++ {
		base := j * FC1PaddedIn
		s := ls.FC1Bias[j]
		for i := 0; i < FC1In; i++ {
			s += int32(int8(ls.FC1W[base+i])) * int32(fc1in[i])
		}
		fc1[j] = s
	}
	var fc2in [L3]byte
	for i := 0; i < L3; i++ {
		fc2in[i] = clippedReLU(fc1[i])
	}
	s := ls.FC2Bias[0]
	for i := 0; i < L3; i++ {
		s += int32(int8(ls.FC2W[i])) * int32(fc2in[i])
	}
	fwdOut := fc0[FC0Out-1] * (600 * outputScale) / (127 * (1 << weightScaleBits))

	t.Logf("fc0 输出 = %v", fc0)
	t.Logf("fc1 输入 = %v", fc1in[:30])
	t.Logf("fc1 输出 = %v", fc1[:8])
	t.Logf("fc2 输入 = %v", fc2in)
	t.Logf("fc2 输出=%d fwdOut=%d → positional=%d", s, fwdOut, s+fwdOut)
}
