package nnue

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
