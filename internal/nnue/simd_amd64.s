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
// acc[i] += int16(int8(w8[i]))，一次处理 16 个元素。
// VPMOVSXBW 从内存读 16 个 int8 并符号扩展为 16 个 int16（128bit → 256bit），
// 再用 VPADDW 累加。L1 = 1024 恰好是 16 的倍数，无需处理尾巴。
TEXT ·addI16AVX2(SB), NOSPLIT, $0-32
	MOVQ acc+0(FP), DI
	MOVQ w8_base+8(FP), SI
	MOVQ $64, CX            // L1/16 = 1024/16
loop:
	VPMOVSXBW (SI), Y0
	VPADDW    (DI), Y0, Y0
	VMOVDQU   Y0, (DI)
	ADDQ      $16, SI       // 16 个 int8
	ADDQ      $32, DI       // 16 个 int16
	DECQ      CX
	JNZ       loop
	VZEROUPPER
	RET

// func subI16AVX2(acc *[L1]int16, w8 []byte)
//
// acc[i] -= int16(int8(w8[i]))。VPSUBW 的语义是 dst = src2 - src1，
// 所以要先取 acc 当被减数，否则方向会反。
TEXT ·subI16AVX2(SB), NOSPLIT, $0-32
	MOVQ acc+0(FP), DI
	MOVQ w8_base+8(FP), SI
	MOVQ $64, CX
loop:
	VMOVDQU   (DI), Y1
	VPMOVSXBW (SI), Y0
	VPSUBW    Y0, Y1, Y1
	VMOVDQU   Y1, (DI)
	ADDQ      $16, SI
	ADDQ      $32, DI
	DECQ      CX
	JNZ       loop
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
