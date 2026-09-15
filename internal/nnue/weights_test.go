package nnue

import (
	"os"
	"testing"
)

const flatPath = "../../engines/pikafish.nnue.flat"

// TestLayoutConstants 锁定布局常量。这些值是逆向 pikafish.nnue 得到的实测值，
// 改动它们意味着文件格式假设变了。
func TestLayoutConstants(t *testing.T) {
	cases := []struct {
		name string
		got  int
		want int
	}{
		{"StackBytes", StackBytes, 17640},
		{"FileBytes", FileBytes, 67941788},
		{"ThreatWBytes", ThreatWBytes, 45649 * 1024},
		{"WBytes", WBytes, 16536 * 1024},
		{"PsqtCount", PsqtCount, 994960},
		{"FC0WBytes", FC0WBytes, 16 * 1024},
		{"FC1WBytes", FC1WBytes, 32 * 32},
		{"FC2WBytes", FC2WBytes, 32},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %d，应为 %d", c.name, c.got, c.want)
		}
	}
}

func TestParseWeights(t *testing.T) {
	raw, err := os.ReadFile(flatPath)
	if err != nil {
		t.Skip("未找到展开后的权重，跳过（先运行 go run ./cmd/nnue-prepare）")
	}

	w, err := Parse(raw)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	// 各张量长度
	if n := len(w.FTBiases); n != L1 {
		t.Errorf("FTBiases 长度 = %d，应为 %d", n, L1)
	}
	if n := len(w.ThreatW); n != ThreatWBytes {
		t.Errorf("ThreatW 长度 = %d，应为 %d", n, ThreatWBytes)
	}
	if n := len(w.W); n != WBytes {
		t.Errorf("W 长度 = %d，应为 %d", n, WBytes)
	}
	if n := len(w.Psqt); n != PsqtCount {
		t.Errorf("Psqt 长度 = %d，应为 %d", n, PsqtCount)
	}
	if n := len(w.Layers); n != LayerStacks {
		t.Errorf("Layers 数量 = %d，应为 %d", n, LayerStacks)
	}
	for i, ls := range w.Layers {
		if n := len(ls.FC0Bias); n != FC0Out {
			t.Fatalf("Layers[%d].FC0Bias 长度 = %d", i, n)
		}
		if n := len(ls.FC0W); n != FC0WBytes {
			t.Fatalf("Layers[%d].FC0W 长度 = %d", i, n)
		}
		if n := len(ls.FC1Bias); n != L3 {
			t.Fatalf("Layers[%d].FC1Bias 长度 = %d", i, n)
		}
		if n := len(ls.FC1W); n != FC1WBytes {
			t.Fatalf("Layers[%d].FC1W 长度 = %d", i, n)
		}
		if n := len(ls.FC2Bias); n != 1 {
			t.Fatalf("Layers[%d].FC2Bias 长度 = %d", i, n)
		}
		if n := len(ls.FC2W); n != FC2WBytes {
			t.Fatalf("Layers[%d].FC2W 长度 = %d", i, n)
		}
	}
}

// TestGoldenSamples 用独立的 Python 读取同一文件取到的抽样值做跨语言一致性断言。
// 覆盖字节序、有符号性、各段偏移三件事。
func TestGoldenSamples(t *testing.T) {
	raw, err := os.ReadFile(flatPath)
	if err != nil {
		t.Skip("未找到展开后的权重，跳过")
	}
	w, err := Parse(raw)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	if got, want := w.FTBiases[:6], []int16{-50, -52, -52, -57, -57, -32}; !eq16(got, want) {
		t.Errorf("FTBiases[0:6] = %v，应为 %v", got, want)
	}
	if got, want := w.Psqt[:6], []int32{0, 0, 0, 0, 253, 161}; !eq32(got, want) {
		t.Errorf("Psqt[0:6] = %v，应为 %v", got, want)
	}
	if got, want := signed(w.ThreatW[:6]), []int8{2, -2, 7, -3, -10, -1}; !eq8(got, want) {
		t.Errorf("ThreatW[0:6] = %v，应为 %v", got, want)
	}
	if got, want := signed(w.W[:6]), []int8{-10, 0, 1, -12, -22, -29}; !eq8(got, want) {
		t.Errorf("W[0:6] = %v，应为 %v", got, want)
	}

	ls0 := w.Layers[0]
	if got, want := ls0.FC0Bias[:4], []int32{-4247, -2625, -3431, 783}; !eq32(got, want) {
		t.Errorf("Layers[0].FC0Bias[0:4] = %v，应为 %v", got, want)
	}
	if got, want := signed(ls0.FC0W[:6]), []int8{-8, 2, 1, -10, -30, -4}; !eq8(got, want) {
		t.Errorf("Layers[0].FC0W[0:6] = %v，应为 %v", got, want)
	}
	if got, want := ls0.FC1Bias[:4], []int32{-513, -2677, -3564, 1865}; !eq32(got, want) {
		t.Errorf("Layers[0].FC1Bias[0:4] = %v，应为 %v", got, want)
	}
	if got := ls0.FC2Bias[0]; got != 255 {
		t.Errorf("Layers[0].FC2Bias = %d，应为 255", got)
	}
	if got, want := signed(ls0.FC2W[:6]), []int8{54, -10, -102, -17, 25, 9}; !eq8(got, want) {
		t.Errorf("Layers[0].FC2W[0:6] = %v，应为 %v", got, want)
	}
	if got := w.Layers[15].FC2Bias[0]; got != 1059 {
		t.Errorf("Layers[15].FC2Bias = %d，应为 1059", got)
	}
}

// TestRejectCorrupt 篡改关键字段必须被拒绝，避免加载到错位的权重。
func TestRejectCorrupt(t *testing.T) {
	raw, err := os.ReadFile(flatPath)
	if err != nil {
		t.Skip("未找到展开后的权重，跳过")
	}

	t.Run("版本错", func(t *testing.T) {
		b := clone(raw)
		b[0] = 0x00
		if _, err := Parse(b); err == nil {
			t.Error("篡改版本号后仍解析成功")
		}
	})
	t.Run("FT哈希错", func(t *testing.T) {
		b := clone(raw)
		b[216] ^= 0xFF
		if _, err := Parse(b); err == nil {
			t.Error("篡改 FT 哈希后仍解析成功")
		}
	})
	t.Run("LayerStack哈希错", func(t *testing.T) {
		b := clone(raw)
		b[67659548] ^= 0xFF
		if _, err := Parse(b); err == nil {
			t.Error("篡改 LayerStack 哈希后仍解析成功")
		}
	})
	t.Run("截断", func(t *testing.T) {
		if _, err := Parse(raw[:len(raw)/2]); err == nil {
			t.Error("截断文件后仍解析成功")
		}
	})
	t.Run("多出尾巴", func(t *testing.T) {
		b := append(clone(raw), 0)
		if _, err := Parse(b); err == nil {
			t.Error("尾部多余字节未被发现")
		}
	})
}

func clone(b []byte) []byte {
	c := make([]byte, len(b))
	copy(c, b)
	return c
}

func signed(b []byte) []int8 {
	out := make([]int8, len(b))
	for i, v := range b {
		out[i] = int8(v)
	}
	return out
}

func eq16(a, b []int16) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func eq32(a, b []int32) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func eq8(a, b []int8) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
