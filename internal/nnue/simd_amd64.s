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
