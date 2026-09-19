//go:build amd64

package nnue

// fc0Accum4 由 fc0_amd64.s 实现。
//
//go:noescape
func fc0Accum4(acc *[4][8]int32, w []byte, feat *[L1]byte)

//go:noescape
func fc0Accum8(acc *[8][8]int32, w []byte, feat *[L1]byte)

//go:noescape
func fc0Reduce8AVX2(out *[8]int32, acc *[8][8]int32)

// fc0Block8 计算 8 个相邻输出的点积（AVX2 内核 + 汇编里的水平求和）。
//
// 没有 AVX2 时退回标量参考实现；FC0Out 不是 8 的倍数时调用方走 fc0Block4。
func fc0Block8(out *[8]int32, w []byte, feat *[L1]byte) {
	if useAVX2 {
		var acc [8][8]int32
		fc0Accum8(&acc, w, feat)
		fc0Reduce8AVX2(out, &acc)
		return
	}
	fc0Block8Scalar(out, w, feat)
}

// fc0Block4 计算 4 个相邻输出的点积。
//
// AVX2 内核只做热循环并留下 8 个 i32 通道，水平求和（32 次加法）在这里完成：
// 相比 32 轮内层循环，这点开销可忽略，而把求和移出来能让汇编保持短小、
// 更容易逐条核对正确性。
func fc0Block4(out *[4]int32, w []byte, feat *[L1]byte) {
	if useAVX2 {
		var acc [4][8]int32
		fc0Accum4(&acc, w, feat)
		for j := 0; j < 4; j++ {
			var s int32
			for k := 0; k < 8; k++ {
				s += acc[j][k]
			}
			out[j] = s
		}
		return
	}
	fc0Block4Scalar(out, w, feat)
}
