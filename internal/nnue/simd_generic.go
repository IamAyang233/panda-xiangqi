//go:build !amd64

package nnue

// 非 amd64 架构（含 arm64）直接使用标量实现。
//
// arm64 的 NEON 版本可以后续按同样方式补上（simd_arm64.go + simd_arm64.s），
// 在此之前这里保持纯标量，行为正确只是慢一些。

// useAVX2 在非 amd64 上恒为 false。
//
// ⚠️ 它必须与 simd_amd64.go 的同名变量**成对存在**：共用的 simd.go 里
// `useFuseRows = useAVX2 && ...` 引用了它，缺了这个定义 arm64 交叉编译会直接报
// undefined（本项目的 arm 包因此挂过一次，而本地 amd64 构建完全看不出来）。
// 加平台相关变量时，记得两边都定义 —— 每次打包都要真的过一遍 arm 交叉编译。
const useAVX2 = false

// UsesAVX2 在非 amd64 上恒为 false。
func UsesAVX2() bool { return false }

// SIMDStatus 说明当前架构没有汇编内核，统一走标量实现。
func SIMDStatus() string { return "标量（当前架构无 SIMD 内核，如 arm64）" }

func addI16(acc *[L1]int16, w8 []byte) { addI16Scalar(acc, w8) }

func subI16(acc *[L1]int16, w8 []byte) { subI16Scalar(acc, w8) }

func psqtAdd(dst *[PSQTBuckets]int32, src []int32) { psqtAddScalar(dst, src) }

func psqtSub(dst *[PSQTBuckets]int32, src []int32) { psqtSubScalar(dst, src) }

// addRows2Fused 在非 amd64 上退回两次标量单行调用（没有对应内核）。
func addRows2Fused(acc *[L1]int16, w1, w2 []byte, mode uint8) {
	switch mode {
	case rowSubSub:
		subI16(acc, w1)
		subI16(acc, w2)
	case rowAddAdd:
		addI16(acc, w1)
		addI16(acc, w2)
	default:
		addI16(acc, w1)
		subI16(acc, w2)
	}
}
