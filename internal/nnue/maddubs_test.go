//go:build amd64

package nnue

import "testing"

//go:noescape
func maddubsProbe(a, b []byte, out []int16)

// TestMaddubsSemantics 用已知输入把 VPMADDUBSW 的语义钉死。
//
// 这条指令是 fc_0 内核的基础（int8 权重 × uint8 激活）。它有两点容易搞错，
// 且错了都不会报错、只会让评估值静默偏移：
//  1. 两个操作数一个被视为无符号、一个被视为有符号，谁是谁必须确认；
//  2. Go 汇编（Plan9）的操作数书写顺序与 Intel 文档相反。
//
// 所以不靠推断，直接拿「大值放在不同位置」对照：取值 200 时，
// 按无符号解释是 +200，按有符号解释是 -56，结果一眼可分。
func TestMaddubsSemantics(t *testing.T) {
	if !UsesAVX2() {
		t.Skip("无 AVX2，跳过")
	}

	run := func(a0 byte, b0 byte) int16 {
		a := make([]byte, 32)
		b := make([]byte, 32)
		a[0] = a0
		b[0] = b0
		out := make([]int16, 16)
		maddubsProbe(a, b, out)
		return out[0]
	}

	// 第一个操作数：200 若按无符号是 200，按有符号是 -56。
	gotA := run(200, 1)
	switch gotA {
	case 200:
		t.Logf("首操作数（a）按【无符号】解释：200×1 = %d ✓", gotA)
	case -56:
		t.Logf("首操作数（a）按【有符号】解释：-56×1 = %d", gotA)
	default:
		t.Fatalf("首操作数语义不符预期：200×1 得到 %d", gotA)
	}

	// 第二个操作数：同样用 200 探。
	gotB := run(1, 200)
	switch gotB {
	case 200:
		t.Logf("次操作数（b）按【无符号】解释：1×200 = %d", gotB)
	case -56:
		t.Logf("次操作数（b）按【有符号】解释：1×(-56) = %d", gotB)
	default:
		t.Fatalf("次操作数语义不符预期：1×200 得到 %d", gotB)
	}

	// fc_0 需要的是「uint8 激活 × int8 权重」，即首操作数无符号。
	// 把结论固定下来：若哪天换了写法或换了工具链，这里会立刻失败。
	if gotA != 200 || gotB != -56 {
		t.Errorf("VPMADDUBSW 语义与 fc_0 需要的不符：期望 a 无符号、b 有符号，"+
			"实测 a=%d（期望 200）、b=%d（期望 -56）", gotA, gotB)
	} else {
		t.Logf("结论：写成 VPMADDUBSW(权重, 激活, dst) 时，激活被当作无符号、权重被当作有符号 —— 正是 fc_0 所需")
	}
}
