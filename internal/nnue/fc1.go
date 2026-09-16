package nnue

// fc_1 的点积内核：int8 权重 × uint8 激活，FC1In(=30) → L3(=32)。
//
// 平台相关部分分在 fc1_amd64.go / fc1_generic.go，这里只放标量参考实现。
//
// 与 fc_0 的内核形状完全不同：fc_0 是「16 个输出 × 1024 输入」，内层循环很长、
// 值得按 4 个输出一组摊开；而这里**输入只有 32 字节**（FC1PaddedIn），
// 所以每个输出恰好对应一条 VPMADDUBSW —— 没有内层循环，瓶颈变成 32 次
// 水平求和。实测 fc1 占 propagate 的 66%（522ns / 790ns），因此值得单独做内核。

// fc1ScalarRef 是标量参考实现，同时是无 AVX2 时的运行时兜底。
//
// 只累加前 FC1In（=30）项：权重行按 FC1PaddedIn（=32）对齐，末两字节是填充。
// SIMD 版会把整 32 字节都乘进去，两者的等价性依赖「act 的末两字节恒为 0」
// —— propagate 里那个数组每次都是零值新建的，所以填充字节乘什么都不影响。
// TestFC1MatchesScalar 把这条钉死：它专门用非零的填充权重验证结果不变。
func fc1ScalarRef(out *[L3]int32, w []byte, bias []int32, act *[FC1PaddedIn]byte) {
	for j := 0; j < L3; j++ {
		base := j * FC1PaddedIn
		s := bias[j]
		for i := 0; i < FC1In; i++ {
			s += int32(int8(w[base+i])) * int32(act[i])
		}
		out[j] = s
	}
}
