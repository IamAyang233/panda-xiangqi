#include "textflag.h"

// fc_0 的 AVX2 内核：int8 权重 × uint8 激活的点积。
//
// 为什么用 VPMADDUBSW 而不是 VNNI 的 VPDPBUSD：
// 后者只有 Alder Lake 及以后的 AVX-VNNI 或 AVX512-VNNI 才支持，而大量
// NAS 用的 Atom/Celeron 没有。VPMADDUBSW 只需 AVX2，覆盖面与现有内核一致 ——
// 在「所有设备都能用」的前提下，不该为极限性能牺牲覆盖面。
//
// 单条 VPMADDUBSW 处理 32 组字节乘法并把相邻两对求和成 i16，配合 VPMADDWD
// 再两两相加成 i32，等效于一次 32 路的 int8 MAC。
//
// 不会饱和：激活来自 Transform，是 ClippedReLU 输出（0..127），
// 故单对最大 127*127+127*127 = 32258 < 32767。

// func fc0Accum4(acc *[4][8]int32, w []byte, feat *[L1]byte)
//
// 计算 4 个相邻输出的部分和，每个输出留下 8 个 i32 通道（未做水平求和，
// 交给 Go 侧完成 —— 汇编只负责热循环，可验证性更重要）。
//
// w 是这 4 个输出的权重，按输出逐个排列，每个 L1(=1024) 字节。
//
// 操作数顺序说明：fc0Block4 的语义是「uint8 激活 × int8 权重」，
// 而 VPMADDUBSW 要求第 2 个操作数无符号、第 1 个有符号（见
// TestMaddubsSemantics 的实测结论），所以激活放在第 2 位、权重放在第 1 位。
TEXT ·fc0Accum4(SB), NOSPLIT, $0-40
	MOVQ acc+0(FP), DI
	MOVQ w_base+8(FP), SI
	MOVQ feat+32(FP), R8

	VPXOR Y0, Y0, Y0            // 输出 0 的累加器
	VPXOR Y1, Y1, Y1            // 输出 1
	VPXOR Y2, Y2, Y2            // 输出 2
	VPXOR Y3, Y3, Y3            // 输出 3

	// 生成全 1 的 i16 向量，供 VPMADDWD 把相邻 i16 对相加成 i32：
	// 全 0xFFFF >> 15 = 1。
	VPXOR    Y7, Y7, Y7
	VPCMPEQW Y7, Y7, Y7
	VPSRLW   $15, Y7, Y7

	MOVQ $32, CX                // L1/32 = 1024/32
loop:
	VMOVDQU (R8), Y4            // 激活的 32 个字节，四个输出共用

	VPMADDUBSW (SI), Y4, Y5     // 输出 0：+0
	VPMADDWD   Y7, Y5, Y5
	VPADDD     Y5, Y0, Y0

	VPMADDUBSW 1024(SI), Y4, Y6 // 输出 1：+1024
	VPMADDWD   Y7, Y6, Y6
	VPADDD     Y6, Y1, Y1

	VPMADDUBSW 2048(SI), Y4, Y5 // 输出 2：+2048
	VPMADDWD   Y7, Y5, Y5
	VPADDD     Y5, Y2, Y2

	VPMADDUBSW 3072(SI), Y4, Y6 // 输出 3：+3072
	VPMADDWD   Y7, Y6, Y6
	VPADDD     Y6, Y3, Y3

	ADDQ $32, R8
	ADDQ $32, SI
	DECQ CX
	JNZ  loop

	VMOVDQU Y0, 0(DI)
	VMOVDQU Y1, 32(DI)
	VMOVDQU Y2, 64(DI)
	VMOVDQU Y3, 96(DI)
	VZEROUPPER
	RET
