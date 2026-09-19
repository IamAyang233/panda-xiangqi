package nnue

import "os"

// useFuseRows 决定累加器更新走不走「两行合一」内核，由 QIJING_FUSEROWS=off 关闭。
//
// 开关留成运行时的，是为了 A/B 能在同一份二进制里切换 —— 切换触发重编译时，
// 编译与基准争抢 CPU 会让同轮交替的量测失去意义（这条在本项目已经踩过两次）。
var useFuseRows = useAVX2 && os.Getenv("QIJING_FUSEROWS") != "off"

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

// PSQT 行的标量参考实现（无 AVX2 时兜底）。
//
// 一条特征除 1024 通道的累加器外，还要更新 PSQTBuckets（16）个 int32 的
// PSQT 向量。Go 不做自动向量化，这段 16 次迭代的循环在汇编里是 64 条标量
// 指令（载入/加/存各 16）；而 16 个 int32 恰好是 64 字节 ＝ 2 个 YMM，
// 有 AVX2 时可用 2 条 VPADDD 做完。
//
// ⚠️ 汇编内核把「2 个 YMM = 16 个 int32」写死了，PSQTBuckets 一改就会静默
// 只算一半（不会越界、不会报错，只是评估值悄悄变偏）。下面两个零长数组
// 当编译期断言用：任一为负长度都会编译失败。
var (
	_ [PSQTBuckets - 16]struct{}
	_ [16 - PSQTBuckets]struct{}
)

func psqtAddScalar(dst *[PSQTBuckets]int32, src []int32) {
	for k := 0; k < PSQTBuckets; k++ {
		dst[k] += src[k]
	}
}

// psqtSubScalar 是 psqtAddScalar 的减版本。
func psqtSubScalar(dst *[PSQTBuckets]int32, src []int32) {
	for k := 0; k < PSQTBuckets; k++ {
		dst[k] -= src[k]
	}
}

// addRows2 把两行权重合成一趟累加到累加器上：acc += w1，sub2 为真时再 − w2。
//
// 数值上等价于连续调用 addI16/subI16 两次，**逐位相同** —— 环绕加满足交换
// 结合律，「先合两行再累加」与「分两次累加」是同一个和的两种次序。
// 这条论证与 PSQ 缓存用的是同一条（见 psqcache.go）：只要内核不做饱和加就成立。
//
// 动机是 profile 里 addI16/subI16 合计 30.6%（一次安静中局搜索、20 局面 ×
// 6 万节点）。按「内核总耗时 ÷ 重量行数」摊出来的单行成本是 **33ns**
// （1220ms / 143.6 万结点 / 约 25 行每结点），而权重 L1 常驻的微基准只有
// **21.3ns** ⇒ 约 **2/3 是执行吞吐、1/3 是访存**。两行合一消的是前者：
// 微基准 56.8 → 37.6ns（−34%）。后者预取不掉（整行版净亏、轻剂量版 1.003~1.012）。
// 两行合一内核的三种模式（决定 acc 怎么被这两行更新）。
//
// 模式 0 与 2 是后来补的：零头配对后，同方向的行也能成对了 ——
// 「两个加」走 0、「两个减」走 2，而「一加一减」（走一步棋最常见的形状）走 1。
// 缺了 2，零头里的「减」就只能单行走，而实测单行零头里减是加的 2.8 倍。
const (
	rowAddAdd uint8 = 0 // acc += w1 + w2
	rowAddSub uint8 = 1 // acc += w1 - w2
	rowSubSub uint8 = 2 // acc -= w1 + w2
)

func addRows2(acc *[L1]int16, w1, w2 []byte, mode uint8) {
	if useFuseRows {
		addRows2Fused(acc, w1, w2, mode)
		return
	}
	// 退化：退回两次单行调用，结果与合一版本逐位相同（环绕加可交换结合）。
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

// SetUseFuseRows 切换两行合一内核并返回原值。
//
// 供同一进程内交替测量使用 —— 这台机器有快慢相（同一份代码两次运行能差
// 3.7×），跨进程交替再ABBA也压不住；把 A/B 放进同一个循环里逐轮交替，
// 漂移就被差分掉了。
func SetUseFuseRows(on bool) bool {
	old := useFuseRows
	useFuseRows = on
	return old
}
