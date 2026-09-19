package nnue

import "os"

// useFC0Block8 决定 fc_0 走 8 输出版（一次算 8 个输出）还是 4 输出版。
//
// 由 QIJING_FC08=off 关闭：既给同一进程内的交替 A/B 提供两态（这台机器有快慢相，
// 跨进程比较不可靠），也给运维留一把一刀 —— 怀疑新内核在某个网络上算错时，
// 关掉它看现象是否消失。
var useFC0Block8 = os.Getenv("QIJING_FC08") != "off"

// SetFC0Block8 切换 fc_0 的输出版本并返回原值，供测试与基准使用。
func SetFC0Block8(on bool) bool {
	old := useFC0Block8
	useFC0Block8 = on
	return old
}

// fc_0 的点积内核：int8 权重 × uint8 激活。
//
// 这是整个 NNUE 求值里最重的一段（profile 中 propagate 占 23.3%）。
// 平台相关部分分在 fc0_amd64.go / fc0_generic.go，这里只放标量参考实现。
//
// 输出通道数恰好是 4 的倍数（FC0Out = 16），所以按 4 个输出一组处理，
// 这样激活向量在一次内层循环里只需读取一遍。

// fc0Block4Scalar 是标量参考实现，同时是无 AVX2 时的运行时兜底。
//
// 累加用 int32：单个乘积最大 127*127 = 16129，1024 项之和远在 int32 范围内。
func fc0Block4Scalar(out *[4]int32, w []byte, feat *[L1]byte) {
	for j := 0; j < 4; j++ {
		base := j * L1
		var s int32
		for i := 0; i < L1; i++ {
			s += int32(int8(w[base+i])) * int32(feat[i])
		}
		out[j] = s
	}
}

// fc0Block8Scalar 是 8 输出版本的标量参考实现。
//
// 与 fc0Block4Scalar 逐式相同，只是多算几个输出 —— 8 输出的汇编内核必须与它
// 逐位一致（TestFC0Block8MatchesScalar）。每个输出单独累加，所以「多算几个输出」
// 本身不改变任何一个输出的求和顺序，这也是两个版本能逐位相同的原因。
func fc0Block8Scalar(out *[8]int32, w []byte, feat *[L1]byte) {
	for j := 0; j < 8; j++ {
		base := j * L1
		var s int32
		for i := 0; i < L1; i++ {
			s += int32(int8(w[base+i])) * int32(feat[i])
		}
		out[j] = s
	}
}
