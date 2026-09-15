package nnue

// 累加器的 SIMD 内核分派。
//
// 这里的两个函数（addI16 / subI16）是 psqAdd / psqSub / thrAdd / thrSub 的
// 内层循环：把 1024 个 int8 权重逐字节符号扩展成 int16，再累加到累加器上。
// 它是 NNUE 求值最热的一段（profile 里四个 add/sub 合计约 46%）。
//
// 平台相关的实现分在三个文件里：
//   simd_amd64.go   —— 运行时检测 AVX2，有则走汇编内核
//   simd_amd64.s    —— AVX2 内核（vpmovsxbw + vpaddw/vpsubw）与 CPUID 检测
//   simd_generic.go —— 其余架构（含 arm64）直接使用下面的标量实现
//
// 全部是 Go 自带工具链能处理的东西：.s 由 go build 内置的汇编器汇编，
// 与 .go 一起进同一个静态二进制，不引入任何外部工具链或运行期依赖，
// CGO_ENABLED=0 与交叉编译都照常可用。
//
// 标量实现必须保留：AVX2 只在部分 x86 CPU 上可用，缺失时（老 Atom/Celeron
// 一类）要能自动降级运行，而不是执行到非法指令崩溃。

// addI16Scalar 是标量参考实现，同时也是无 AVX2 时的运行时兜底。
// 4 路展开：Go 编译器不做 SIMD 自动向量化，单累加链也吃不满乱序窗口。
func addI16Scalar(acc *[L1]int16, w8 []byte) {
	for i := 0; i < L1; i += 4 {
		acc[i] += int16(int8(w8[i]))
		acc[i+1] += int16(int8(w8[i+1]))
		acc[i+2] += int16(int8(w8[i+2]))
		acc[i+3] += int16(int8(w8[i+3]))
	}
}

// subI16Scalar 是 addI16Scalar 的减版本。
func subI16Scalar(acc *[L1]int16, w8 []byte) {
	for i := 0; i < L1; i += 4 {
		acc[i] -= int16(int8(w8[i]))
		acc[i+1] -= int16(int8(w8[i+1]))
		acc[i+2] -= int16(int8(w8[i+2]))
		acc[i+3] -= int16(int8(w8[i+3]))
	}
}
