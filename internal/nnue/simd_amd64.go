//go:build amd64

package nnue

import "os"

// AVX2 内核的 Go 侧声明。实现在 simd_amd64.s，由 go build 内置的汇编器处理。
//
//go:noescape
func addI16AVX2(acc *[L1]int16, w8 []byte)

//go:noescape
func subI16AVX2(acc *[L1]int16, w8 []byte)

//go:noescape
func addRows2I16AVX2(acc *[L1]int16, w1, w2 []byte, sub2 bool)

//go:noescape
func psqtAddAVX2(dst *[PSQTBuckets]int32, src []int32)

//go:noescape
func psqtSubAVX2(dst *[PSQTBuckets]int32, src []int32)

//go:noescape
func cpuid(eax, ecx uint32) (a, b, c, d uint32)

//go:noescape
func xgetbv(index uint32) (eax, edx uint32)

// forcedScalar 记录是否由环境变量强制关闭，仅用于区分诊断输出里的两种原因。
var forcedScalar = func() bool {
	v, ok := os.LookupEnv("QIJING_SIMD")
	return ok && (v == "off" || v == "0")
}()

// useAVX2 在包初始化时确定一次，之后各处的分派只读它。
var useAVX2 = detectAVX2()

// detectAVX2 判定当前 CPU 与操作系统是否都支持 AVX2。
//
// 四项缺一不可，少任何一项都会在真实硬件上踩坑：
//  1. CPUID.1:ECX.OSXSAVE —— 说明 OS 启用了 XSAVE，否则无法用 XGETBV
//  2. CPUID.1:ECX.AVX     —— CPU 有 AVX
//  3. XGETBV(0) 的 bit1/bit2 —— OS 实际保存 XMM 与 YMM 状态
//  4. CPUID.7:EBX.AVX2    —— 有 AVX2 指令
//
// 只查第 4 项是不够的：某些虚拟化环境或旧内核下 CPU 支持 AVX2 但 OS
// 不保存 YMM 状态，执行 AVX2 指令会触发 #UD。
//
// 另外支持用环境变量 QIJING_SIMD=off 强制降级：既便于在异常设备上排查
// （关掉 SIMD 看问题是否消失），也让标量兜底路径本身可以被测试覆盖。
//
// 这里自己读 CPUID 而不是用 golang.org/x/sys/cpu，是为了守住「零第三方依赖」。
func detectAVX2() bool {
	if forcedScalar {
		return false
	}
	_, _, c1, _ := cpuid(1, 0)
	const (
		osxsaveBit = 1 << 27
		avxBit     = 1 << 28
	)
	if c1&osxsaveBit == 0 || c1&avxBit == 0 {
		return false
	}
	// XCR0 的 bit1(XMM) 与 bit2(YMM) 都必须置位。
	if eax, _ := xgetbv(0); eax&0x6 != 0x6 {
		return false
	}
	_, b7, _, _ := cpuid(7, 0)
	return b7&(1<<5) != 0
}

// UsesAVX2 报告当前是否走 AVX2 内核（供测试与诊断使用）。
func UsesAVX2() bool { return useAVX2 }

// SIMDStatus 返回人类可读的 SIMD 状态，供启动诊断输出。
func SIMDStatus() string {
	if useAVX2 {
		return "AVX2"
	}
	if forcedScalar {
		return "标量（由 QIJING_SIMD 强制关闭）"
	}
	return "标量（CPU 或系统不支持 AVX2）"
}

// addI16 把 int8 权重符号扩展后累加到 int16 累加器。
func addI16(acc *[L1]int16, w8 []byte) {
	if useAVX2 {
		addI16AVX2(acc, w8)
		return
	}
	addI16Scalar(acc, w8)
}

// subI16 是 addI16 的减版本。
func subI16(acc *[L1]int16, w8 []byte) {
	if useAVX2 {
		subI16AVX2(acc, w8)
		return
	}
	subI16Scalar(acc, w8)
}

// addRows2Fused 走两行合一的 AVX2 内核（acc += w1，sub2 为真时再 − w2）。
func addRows2Fused(acc *[L1]int16, w1, w2 []byte, sub2 bool) {
	addRows2I16AVX2(acc, w1, w2, sub2)
}

// psqtAdd 把一条特征的 PSQT 向量累加到累加器的同名向量上。
func psqtAdd(dst *[PSQTBuckets]int32, src []int32) {
	if useAVX2 {
		psqtAddAVX2(dst, src)
		return
	}
	psqtAddScalar(dst, src)
}

// psqtSub 是 psqtAdd 的减版本。
func psqtSub(dst *[PSQTBuckets]int32, src []int32) {
	if useAVX2 {
		psqtSubAVX2(dst, src)
		return
	}
	psqtSubScalar(dst, src)
}
