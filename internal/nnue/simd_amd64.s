#include "textflag.h"

// NaN-safe、无 AVX-SSE 转换惩罚：内核退出前清空 YMM 上半部。
// 只在叶子函数中使用向量指令，且都以 VZEROUPPER 收尾。

// func cpuid(eax, ecx uint32) (a, b, c, d uint32)
TEXT ·cpuid(SB), NOSPLIT, $0-24
	MOVL eax+0(FP), AX
	MOVL ecx+4(FP), CX
	CPUID
	MOVL AX, a+8(FP)
	MOVL BX, b+12(FP)
	MOVL CX, c+16(FP)
	MOVL DX, d+20(FP)
	RET

// func xgetbv(index uint32) (eax, edx uint32)
//
// 返回值必须按 8 字节对齐，所以 eax 落在 +8 而不是 +4（go vet 会检查这一点）。
TEXT ·xgetbv(SB), NOSPLIT, $0-16
	MOVL index+0(FP), CX
	XGETBV
	MOVL AX, eax+8(FP)
	MOVL DX, edx+12(FP)
	RET

// func addI16AVX2(acc *[L1]int16, w8 []byte)
//
// acc[i] += int16(int8(w8[i]))。VPMOVSXBW 从内存读 16 个 int8 并符号扩展为
// 16 个 int16（128bit → 256bit），再用 VPADDW 累加。L1 = 1024 恰好是 64 的倍数。
//
// **4 路展开（每轮 64 个元素）**：每轮 16 元素时，12 条向量指令之外还有 4 条
// 纯循环开销（ADDQ×2 + DECQ + JNZ），7 uops 里有 4 条是开销。展开到 64 元素后
// 变成 12 条向量 + 4 条开销 = 16 uops / 64 元素，每元素的 uop 数从 0.44 降到 0.25。
// 实测单次调用 26.2 → 20.8 ns（L1 = 1024，1.26×）。四条链互不依赖，正好吃满乱序窗口。
//
// 展开不改变运算顺序（同一批地址、同一串指令），所以结果与标量参考逐位一致；
// TestSIMDMatchesScalar 会盯住这一点。
TEXT ·addI16AVX2(SB), NOSPLIT, $0-32
	MOVQ acc+0(FP), DI
	MOVQ w8_base+8(FP), SI
	MOVQ $16, CX            // L1/64 = 1024/64
loop:
	VPMOVSXBW (SI), Y0
	VPMOVSXBW 16(SI), Y1
	VPMOVSXBW 32(SI), Y2
	VPMOVSXBW 48(SI), Y3

	VPADDW (DI), Y0, Y0
	VPADDW 32(DI), Y1, Y1
	VPADDW 64(DI), Y2, Y2
	VPADDW 96(DI), Y3, Y3

	VMOVDQU Y0, (DI)
	VMOVDQU Y1, 32(DI)
	VMOVDQU Y2, 64(DI)
	VMOVDQU Y3, 96(DI)

	ADDQ $64, SI           // 64 个 int8
	ADDQ $128, DI          // 64 个 int16
	DECQ CX
	JNZ  loop
	VZEROUPPER
	RET

// func subI16AVX2(acc *[L1]int16, w8 []byte)
//
// acc[i] -= int16(int8(w8[i]))。VPSUBW 的语义是 dst = src2 - src1，
// 所以要先取 acc 当被减数，否则方向会反。
//
// 与 addI16AVX2 一样 4 路展开（每轮 64 个元素），理由见上；
// 这里额外把 4 个 acc 块先读进寄存器再统一写回，读改写之间没有依赖。
TEXT ·subI16AVX2(SB), NOSPLIT, $0-32
	MOVQ acc+0(FP), DI
	MOVQ w8_base+8(FP), SI
	MOVQ $16, CX            // L1/64 = 1024/64
loop:
	VMOVDQU (DI), Y0
	VMOVDQU 32(DI), Y1
	VMOVDQU 64(DI), Y2
	VMOVDQU 96(DI), Y3

	VPMOVSXBW (SI), Y4
	VPSUBW    Y4, Y0, Y0
	VPMOVSXBW 16(SI), Y5
	VPSUBW    Y5, Y1, Y1
	VPMOVSXBW 32(SI), Y6
	VPSUBW    Y6, Y2, Y2
	VPMOVSXBW 48(SI), Y7
	VPSUBW    Y7, Y3, Y3

	VMOVDQU Y0, (DI)
	VMOVDQU Y1, 32(DI)
	VMOVDQU Y2, 64(DI)
	VMOVDQU Y3, 96(DI)

	ADDQ $64, SI           // 64 个 int8
	ADDQ $128, DI          // 64 个 int16
	DECQ CX
	JNZ  loop
	VZEROUPPER
	RET

// func maddubsProbe(a, b []byte, out []int16)
//
// 诊断用：把 VPMADDUBSW 的结果原样导出。
//
// 该指令的语义是「src1 无符号 × src2 有符号，相邻两对相加后饱和到 i16」，
// 但 Go 汇编（Plan9 语法）里操作数的书写顺序与 Intel 文档相反，
// 且哪个操作数被视为无符号不能靠猜 —— 弄反了不会报错，只会静默算错评估值。
// 这个探针让 TestMaddubsSemantics 能用已知输入把语义钉死。
//
// out[k] 对应 a[2k]*b[2k] + a[2k+1]*b[2k+1]（若首操作数无符号）。
TEXT ·maddubsProbe(SB), NOSPLIT, $0-72
	MOVQ a_base+0(FP), SI
	MOVQ b_base+24(FP), DI
	MOVQ out_base+48(FP), DX
	VMOVDQU (SI), Y0
	VPMADDUBSW (DI), Y0, Y1
	VMOVDQU Y1, (DX)
	VZEROUPPER
	RET

// func psqtAddAVX2(dst *[PSQTBuckets]int32, src []int32)
//
// dst[k] += src[k]，k = 0..15。一条特征除 1024 通道的累加器外还要维护这 16 个
// int32 的 PSQT 向量；16 个 int32 ＝ 64 字节 ＝ 恰好 2 个 YMM，所以一次调用
// 只需 2 条 VPADDD。
//
// int32 的环绕语义在向量与标量下完全一致（无饱和、无浮点舍入），所以这里
// 不需要像 VPMADDUBSW 那样担心语义差异，结果与 psqtAddScalar 逐位相同。
//
// 展开成直线代码而不是循环：只有 2 个块，循环开销比本体还大。
TEXT ·psqtAddAVX2(SB), NOSPLIT, $0-32
	MOVQ dst+0(FP), DI
	MOVQ src_base+8(FP), SI
	VMOVDQU (SI), Y0
	VPADDD  (DI), Y0, Y0
	VMOVDQU Y0, (DI)
	VMOVDQU 32(SI), Y1
	VPADDD  32(DI), Y1, Y1
	VMOVDQU Y1, 32(DI)
	VZEROUPPER
	RET

// func psqtSubAVX2(dst *[PSQTBuckets]int32, src []int32)
//
// dst[k] -= src[k]。VPSUBD 的语义是 dst = src2 - src1，所以作为被减数的
// 累加器要写在中间那个操作数上，否则方向会反（与 subI16AVX2 同一个坑）。
TEXT ·psqtSubAVX2(SB), NOSPLIT, $0-32
	MOVQ dst+0(FP), DI
	MOVQ src_base+8(FP), SI
	VMOVDQU (SI), Y2
	VMOVDQU (DI), Y0
	VPSUBD  Y2, Y0, Y0
	VMOVDQU Y0, (DI)
	VMOVDQU 32(SI), Y3
	VMOVDQU 32(DI), Y1
	VPSUBD  Y3, Y1, Y1
	VMOVDQU Y1, 32(DI)
	VZEROUPPER
	RET

// func addRows2I16AVX2(acc *[L1]int16, w1, w2 []byte, sub2 bool)
//
// 把两行权重合成一趟累加到同一片累加器上：acc += w1 ± w2。
//
// 起因是量出来的：单行版 addI16AVX2 稳定在 21.3ns/次（权重 L1 常驻的微基准）。
// 真实搜索里按「内核总耗时 ÷ 重量行数」摊，是 **33ns/行**：
// 20 局面 × 6 万结点的基准上，addI16+subI16 合计 1220ms、143.6 万结点，
// 每结点约 25 行（威胁增量 17.4 + PSQ 增量 5.5 + 重建 2.5，均由 diag 计数器实测）
// ⇒ 850ns/结点 ⇒ 33ns/行。所以这 30.6% 里约 **2/3 是执行吞吐、1/3 是访存**。
// 微基准里把两行合成一趟：56.8ns → 37.6ns（−34%）。
//
// 为什么单行版的每元素成本降不下来：每 64 个元素要 4 条 VPMOVSXBW、
// 4 条带内存操作数的 VPADDW、4 条 VMOVDQU 存储、再加 4 条循环开销 ——
// 前三条是数据的必经之路，能省的只有「把两行的数据先合起来」这一层：
// 合并后每 64 个元素变成 8 条 VPMOVSXBW + 4 条 VPADDW(合并) + 4 条 VPADDW(acc)
// + 4 条存储 + 4 条开销 = 25 条覆盖两行，单行摊到 12.5 条（原来 16 条）。
// 同步推进的两条独立数据流也让乱序窗口更满，实测收益大于纯 uop 账的 22%。
//
// ⚠️ 那 1/3 的访存成本**没有**被这里消掉，而且实测也**预取不掉**：整行预取
// （提前 2 条、一次 16 条 PREFETCHT0）连结构带预取净亏 5%；叠在配对循环里的
// 轻剂量版（每对 2 条指令、只预取行首）也是 1.003~1.012，等于没有。
// 行内是顺序访问、硬件预取器本来就在管，剩下那点停顿不在「行首那一条线」上。
//
// 逐位正确性与 PADDW 的环绕语义绑定：int16 加法模 2^16 满足交换结合律，
// 「先合两行再累加」与「分两次累加」算的是同一个和，只是次序不同。
// 若哪天内核换成饱和加（PADDSUW/PADDSW），这条论证失效。
TEXT ·addRows2I16AVX2(SB), NOSPLIT, $0-57
	MOVQ acc+0(FP), DI
	MOVQ w1_base+8(FP), SI
	MOVQ w2_base+32(FP), R8
	MOVBQZX sub2+56(FP), AX
	TESTQ AX, AX
	JNZ  subEntry

	MOVQ $16, CX            // L1/64 = 1024/64
addLoop:
	VPMOVSXBW (SI), Y0
	VPMOVSXBW 16(SI), Y1
	VPMOVSXBW 32(SI), Y2
	VPMOVSXBW 48(SI), Y3
	VPMOVSXBW (R8), Y4
	VPMOVSXBW 16(R8), Y5
	VPMOVSXBW 32(R8), Y6
	VPMOVSXBW 48(R8), Y7
	VPADDW Y4, Y0, Y0
	VPADDW Y5, Y1, Y1
	VPADDW Y6, Y2, Y2
	VPADDW Y7, Y3, Y3
	VPADDW (DI), Y0, Y0
	VPADDW 32(DI), Y1, Y1
	VPADDW 64(DI), Y2, Y2
	VPADDW 96(DI), Y3, Y3
	VMOVDQU Y0, (DI)
	VMOVDQU Y1, 32(DI)
	VMOVDQU Y2, 64(DI)
	VMOVDQU Y3, 96(DI)
	ADDQ $64, SI
	ADDQ $64, R8
	ADDQ $128, DI
	DECQ CX
	JNZ  addLoop
	VZEROUPPER
	RET

subEntry:
	MOVQ $16, CX
subLoop:
	VPMOVSXBW (SI), Y0
	VPMOVSXBW 16(SI), Y1
	VPMOVSXBW 32(SI), Y2
	VPMOVSXBW 48(SI), Y3
	VPMOVSXBW (R8), Y4
	VPMOVSXBW 16(R8), Y5
	VPMOVSXBW 32(R8), Y6
	VPMOVSXBW 48(R8), Y7
	VPSUBW Y4, Y0, Y0
	VPSUBW Y5, Y1, Y1
	VPSUBW Y6, Y2, Y2
	VPSUBW Y7, Y3, Y3
	VPADDW (DI), Y0, Y0
	VPADDW 32(DI), Y1, Y1
	VPADDW 64(DI), Y2, Y2
	VPADDW 96(DI), Y3, Y3
	VMOVDQU Y0, (DI)
	VMOVDQU Y1, 32(DI)
	VMOVDQU Y2, 64(DI)
	VMOVDQU Y3, 96(DI)
	ADDQ $64, SI
	ADDQ $64, R8
	ADDQ $128, DI
	DECQ CX
	JNZ  subLoop
	VZEROUPPER
	RET
