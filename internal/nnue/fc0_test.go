package nnue

import (
	"math/rand"
	"testing"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
)

// fc0AVX2 直接跑 AVX2 内核并完成水平求和，绕开 fc0Block4 的运行时分发，
// 以便在测试里与标量实现做无歧义的对拍。
func fc0AVX2(out *[4]int32, w []byte, feat *[L1]byte) {
	var acc [4][8]int32
	fc0Accum4(&acc, w, feat)
	for j := 0; j < 4; j++ {
		var s int32
		for k := 0; k < 8; k++ {
			s += acc[j][k]
		}
		out[j] = s
	}
}

// TestFC0MatchesScalar 是 fc_0 内核的正确性关卡。
//
// 激活取 0..127：这**不是随意选的范围**，而是 VPMADDUBSW 不发生饱和的
// 必要条件（见 TestActivationRangeSafe）。权重取 int8 全范围，
// 专门盯住符号：写成无符号扩展会让负权重变成大正数。
func TestFC0MatchesScalar(t *testing.T) {
	if !UsesAVX2() {
		t.Skip("当前 CPU/OS 不支持 AVX2，跳过")
	}

	rng := rand.New(rand.NewSource(20260915))
	w := make([]byte, 4*L1)
	for i := range w {
		w[i] = byte(int8(rng.Intn(256) - 128))
	}
	// 塞入符号与边界极值，避免随机数正好绕开危险点。
	w[0], w[1], w[L1], w[L1+1] = 0x80, 0x7f, 0xff, 0x00

	feat := new([L1]byte)
	for i := range feat {
		feat[i] = byte(rng.Intn(128)) // 0..127
	}
	feat[0], feat[1], feat[L1-1] = 127, 0, 127

	var got, want [4]int32
	fc0AVX2(&got, w, feat)
	fc0Block4Scalar(&want, w, feat)

	for j := 0; j < 4; j++ {
		if got[j] != want[j] {
			t.Errorf("输出 %d 不一致：AVX2=%d 标量=%d", j, got[j], want[j])
		}
	}
	if t.Failed() {
		return
	}
	t.Logf("4 个输出全部一致：%v", got)
}

// TestActivationRangeSafe 验证激活值不会让 VPMADDUBSW 饱和。
//
// 该指令把相邻两个乘积相加后**饱和到 i16**：激活 a 与权重 w 的取值满足
// (a[2k]*w[2k] + a[2k+1]*w[2k+1]) 超出 [-32768, 32767] 就会被截断，
// 而标量实现不会 —— 于是两者静默分叉，评估值偏移且无任何报错。
//
// 最坏情况是 a 取 255、w 取 ±127：255*127 + 255*127 = 64770，远超 32767。
// 所以激活必须满足 a ≤ 127（此时 127*127*2 = 32258 < 32767，留有余量）。
//
// Transform 里 out = (clamp(s0,0,255) * clamp(s1,0,255)) >> 9，上界是
// 65025>>9 = 126，理论上安全。这里用真实权重与真实局面把这一点钉住：
// 若将来有人改动 Transform 的量化方式，本测试会立刻失败。
func TestActivationRangeSafe(t *testing.T) {
	w, err := Load(flatPath)
	if err != nil {
		t.Skip("未找到展开后的权重，跳过")
	}

	fens := []string{
		game.InitialFEN,
		"2bak1b2/4a4/4b4/p1p1p3p/6p2/2P6/P3P1P1P/1C2C4/9/RNBAKABNR w - - 0 1",
		"3ak4/4a4/4b4/9/9/9/4P4/9/4R4/4K4 w - - 0 1",
	}
	maxSeen := byte(0)
	for _, f := range fens {
		p, perr := game.ParseFEN(f)
		if perr != nil {
			continue
		}
		var pos Position
		pos.ResetFromGame(&p.Board, p.Turn>>3)
		var acc Accumulator
		w.Apply(&pos, &acc)
		_, feat := acc.Transform(pos.SideToMove(), pos.LayerStackBucket())
		for _, v := range feat {
			if v > maxSeen {
				maxSeen = v
			}
		}
	}

	t.Logf("实测激活最大值 %d（VPMADDUBSW 的安全上限是 127）", maxSeen)
	if maxSeen > 127 {
		t.Errorf("激活值达到 %d，会让 VPMADDUBSW 饱和、与标量实现分叉；"+
			"需改用不饱和的路径（如 VPMOVSXBW + VPMADDWD，代价是每次只处理 16 路）", maxSeen)
	}
}

func benchFC0(b *testing.B, fn func(*[4]int32, []byte, *[L1]byte)) {
	rng := rand.New(rand.NewSource(3))
	w := make([]byte, 4*L1)
	for i := range w {
		w[i] = byte(int8(rng.Intn(256) - 128))
	}
	feat := new([L1]byte)
	for i := range feat {
		feat[i] = byte(rng.Intn(128))
	}
	var out [4]int32
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fn(&out, w, feat)
	}
}

func BenchmarkFC0Scalar(b *testing.B) { benchFC0(b, fc0Block4Scalar) }

func BenchmarkFC0AVX2(b *testing.B) {
	if !UsesAVX2() {
		b.Skip("无 AVX2")
	}
	benchFC0(b, fc0AVX2)
}

// fc0AVX2x8 直接跑 8 输出内核（累加 + 汇编里的水平求和），绕开运行时分发。
func fc0AVX2x8(out *[8]int32, w []byte, feat *[L1]byte) {
	var acc [8][8]int32
	fc0Accum8(&acc, w, feat)
	fc0Reduce8AVX2(out, &acc)
}

// TestFC0Block8MatchesScalar 是 8 输出内核的正确性关卡。
//
// 与 4 输出版同样盯三件事：符号扩展方向（写成无符号会让负权重变大正数）、
// VPMADDUBSW 的饱和边界（激活必须 ≤127）、以及**水平求和分组顺序的变化
// 不影响结果**（int32 环绕加满足交换结合律，且上界 16.5M 远不溢出）。
func TestFC0Block8MatchesScalar(t *testing.T) {
	if !UsesAVX2() {
		t.Skip("当前 CPU/OS 不支持 AVX2，跳过")
	}

	rng := rand.New(rand.NewSource(20260920))
	w := make([]byte, 8*L1)
	for i := range w {
		w[i] = byte(int8(rng.Intn(256) - 128))
	}
	// 符号与边界极值：-128 / +127 / -1 / 0 各来一个。
	w[0], w[1], w[L1], w[L1+1] = 0x80, 0x7f, 0xff, 0x00
	w[7*L1], w[7*L1+L1-1] = 0x80, 0x7f

	feat := new([L1]byte)
	for i := range feat {
		feat[i] = byte(rng.Intn(128)) // 0..127
	}
	feat[0], feat[1], feat[L1-1] = 127, 0, 127

	var got, want [8]int32
	fc0AVX2x8(&got, w, feat)
	fc0Block8Scalar(&want, w, feat)

	for j := 0; j < 8; j++ {
		if got[j] != want[j] {
			t.Errorf("输出 %d 不一致：AVX2=%d 标量=%d", j, got[j], want[j])
		}
	}
	if t.Failed() {
		return
	}
	t.Logf("8 个输出全部一致：%v", got)
}
