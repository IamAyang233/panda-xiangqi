//go:build !amd64

package nnue

// 非 amd64 架构（含 arm64）直接使用标量实现。
//
// arm64 的 NEON 版本可以后续按同样方式补上（simd_arm64.go + simd_arm64.s），
// 在此之前这里保持纯标量，行为正确只是慢一些。

// UsesAVX2 在非 amd64 上恒为 false。
func UsesAVX2() bool { return false }

// SIMDStatus 说明当前架构没有汇编内核，统一走标量实现。
func SIMDStatus() string { return "标量（当前架构无 SIMD 内核，如 arm64）" }

func addI16(acc *[L1]int16, w8 []byte) { addI16Scalar(acc, w8) }

func subI16(acc *[L1]int16, w8 []byte) { subI16Scalar(acc, w8) }

func psqtAdd(dst *[PSQTBuckets]int32, src []int32) { psqtAddScalar(dst, src) }

func psqtSub(dst *[PSQTBuckets]int32, src []int32) { psqtSubScalar(dst, src) }

// addRows2Fused 在非 amd64 上退回两次标量单行调用（没有对应内核）。
func addRows2Fused(acc *[L1]int16, w1, w2 []byte, sub2 bool) {
	addI16(acc, w1)
	if sub2 {
		subI16(acc, w2)
	} else {
		addI16(acc, w2)
	}
}
