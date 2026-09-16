//go:build amd64

package nnue

// fc1Accum32 由 fc1_amd64.s 实现。
//
//go:noescape
func fc1Accum32(acc *[L3][8]int32, w []byte, act *[FC1PaddedIn]byte)

// fc1Forward 计算 fc_1 的 32 个输出（含 bias）。
//
// AVX2 内核只做热循环、每个输出留下 8 个 i32 通道，水平求和（每输出 7 次加法）
// 在 Go 侧完成 —— 与 fc0Block4 同一套约定：汇编保持短小、便于逐条核对。
//
// 水平求和**不放进汇编**是有依据的：VPHADDD 是端口 5 的单发射指令，
// 32 个输出要 64 次；而 Go 侧那 224 次普通 i32 加法可以 4 路并行，反而更快。
func fc1Forward(out *[L3]int32, w []byte, bias []int32, act *[FC1PaddedIn]byte) {
	if useAVX2 {
		var acc [L3][8]int32
		fc1Accum32(&acc, w, act)
		// 展开成一条表达式而不是内层循环：循环版每个输出要多 2 次比较与
		// 索引运算，32 个输出就是上百条多余指令 —— 实测这一段能占内核
		// 总耗时的大部分。展开后只剩 7 次加法，且 acc 的索引全是常量。
		for j := 0; j < L3; j++ {
			a := &acc[j]
			out[j] = bias[j] + a[0] + a[1] + a[2] + a[3] + a[4] + a[5] + a[6] + a[7]
		}
		return
	}
	fc1ScalarRef(out, w, bias, act)
}
