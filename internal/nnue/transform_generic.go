//go:build !amd64

package nnue

// 非 amd64 架构（含 arm64）走标量实现。
// arm64 的 NEON 版本可后续按同样方式补上（transform_arm64.go + transform_arm64.s）。

func transformHalf(out []byte, psqLo, thrLo, psqHi, thrHi []int16) {
	transformHalfScalar(out, psqLo, thrLo, psqHi, thrHi)
}
