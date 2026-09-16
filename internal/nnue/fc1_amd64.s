#include "textflag.h"

// fc_1 的 AVX2 内核：int8 权重 × uint8 激活，32 个输出。
//
// 与 fc0Accum4 的区别：fc_0 的内层有 1024 字节要累加，所以那 32 轮循环是主体；
// 这里输入只有 32 字节（FC1PaddedIn），**每个输出恰好是「一条 VPMADDUBSW +
// 一条 VPMADDWD」**，没有内层循环，于是 32 个输出就是 32 条独立的点积。
//
// 每个输出留下 8 个 i32 通道（不做水平求和），交给 Go 侧 —— 与 fc0Accum4 同一
// 套约定：汇编只负责热循环。这里这么做还有性能理由：VPHADDD 是端口 5 的单发射
// 指令，32 个输出要 64 次；Go 侧那 224 次普通 i32 加法反而更快。
//
// 操作数顺序与 fc0Accum4 一致（见 maddubs_test.go 的实测结论）：
// 第 1 个操作数（此处是内存里的权重）按【有符号】解释，
// 第 2 个（寄存器里的激活）按【无符号】解释。
//
// 不会饱和：激活是 ClippedReLU 输出（0..127），故单对最大
// 127*127 + 127*127 = 32258 < 32767。
//
// func fc1Accum32(acc *[L3][8]int32, w []byte, act *[FC1PaddedIn]byte)
//
// w 是 32 个输出 × FC1PaddedIn(32) 字节的行主序权重，共 1024 字节；
// acc 收 32 个输出 × 8 个 i32 部分和，共 1024 字节。地址只按 (SI)(AX*1) 形式
// 取，AX 从 0 走到 1024，两边同步推进。
TEXT ·fc1Accum32(SB), NOSPLIT, $0-40
	MOVQ acc+0(FP), DI
	MOVQ w_base+8(FP), SI
	MOVQ act+32(FP), R8

	VMOVDQU (R8), Y4            // 激活的 32 字节，32 个输出共用

	// 生成全 1 的 i16 向量，供 VPMADDWD 把相邻 i16 对相加成 i32：
	// 全 0xFFFF 逻辑右移 15 位得 1。
	VPXOR    Y7, Y7, Y7
	VPCMPEQW Y7, Y7, Y7
	VPSRLW   $15, Y7, Y7

	MOVQ $8, CX                 // 32 个输出，每轮 4 个
	XORQ AX, AX
loop:
	VPMADDUBSW (SI)(AX*1), Y4, Y5
	VPMADDWD   Y7, Y5, Y5
	VMOVDQU    Y5, (DI)(AX*1)

	VPMADDUBSW 32(SI)(AX*1), Y4, Y6
	VPMADDWD   Y7, Y6, Y6
	VMOVDQU    Y6, 32(DI)(AX*1)

	VPMADDUBSW 64(SI)(AX*1), Y4, Y5
	VPMADDWD   Y7, Y5, Y5
	VMOVDQU    Y5, 64(DI)(AX*1)

	VPMADDUBSW 96(SI)(AX*1), Y4, Y6
	VPMADDWD   Y7, Y6, Y6
	VMOVDQU    Y6, 96(DI)(AX*1)

	ADDQ $128, AX
	DECQ CX
	JNZ  loop

	VZEROUPPER
	RET
