//go:build !amd64

package nnue

// 非 amd64 架构（含 arm64）走标量实现。arm64 的 NEON 版本可后续按
// 同样方式补上（fc0_arm64.go + fc0_arm64.s），在此之前保持纯标量。

func fc0Block4(out *[4]int32, w []byte, feat *[L1]byte) {
	fc0Block4Scalar(out, w, feat)
}

// fc0Block8 在非 amd64 上直接走标量（没有对应内核）。
func fc0Block8(out *[8]int32, w []byte, feat *[L1]byte) {
	fc0Block8Scalar(out, w, feat)
}
