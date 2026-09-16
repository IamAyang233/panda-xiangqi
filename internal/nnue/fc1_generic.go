//go:build !amd64

package nnue

// 非 amd64 架构（含 arm64）走标量实现。
// arm64 的 NEON 版本可后续按同样方式补上（fc1_arm64.go + fc1_arm64.s）。

func fc1Forward(out *[L3]int32, w []byte, bias []int32, act *[FC1PaddedIn]byte) {
	fc1ScalarRef(out, w, bias, act)
}
