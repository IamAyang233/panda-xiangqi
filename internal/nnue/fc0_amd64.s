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

// func fc0Accum8(acc *[8][8]int32, w []byte, feat *[L1]byte)
//
// fc0Accum4 的 8 输出版本。动机与累加器那两条内核同源：**把共用的一趟摊销开**。
// 每轮 32 个激活字节由 8 个输出共用（原来 4 个），于是
//   每 256 次乘加 = 1 条激活载入 + 8×3 条乘加 + 3 条循环 = 28 条（原来 32 条）。
// FC0Out = 16 恰好整除 8，所以每次评估从 4 趟变成 2 趟。
//
// 寄存器账：8 个累加器 Y0..Y7 + 全 1 常量 Y8 + 激活 Y9 + 临时 Y10 = 11 个，够用。
// 再往上加输出就不行了（16 个 YMM 装不下 16 个累加器），所以 8 是上限。
//
// 数值上与 fc0Accum4 逐位相同：每个输出的乘加顺序、分组完全一致
// （都是 VPMADDUBSW 把相邻两对求和成 i16，再 VPMADDWD 两两相加成 i32），
// 只是多算了几个输出。由 TestFC0Block8MatchesScalar 对拍守护。
TEXT ·fc0Accum8(SB), NOSPLIT, $0-40
	MOVQ acc+0(FP), DI
	MOVQ w_base+8(FP), SI
	MOVQ feat+32(FP), R8

	VPXOR Y0, Y0, Y0
	VPXOR Y1, Y1, Y1
	VPXOR Y2, Y2, Y2
	VPXOR Y3, Y3, Y3
	VPXOR Y4, Y4, Y4
	VPXOR Y5, Y5, Y5
	VPXOR Y6, Y6, Y6
	VPXOR Y7, Y7, Y7

	// 全 1 的 i16 向量（0xFFFF >> 15 = 1），供 VPMADDWD 把相邻 i16 对相加成 i32。
	VPXOR    Y8, Y8, Y8
	VPCMPEQW Y8, Y8, Y8
	VPSRLW   $15, Y8, Y8

	MOVQ $32, CX                // L1/32 = 1024/32
loop:
	VMOVDQU (R8), Y9            // 32 个激活字节，八个输出共用

	VPMADDUBSW (SI), Y9, Y10
	VPMADDWD   Y8, Y10, Y10
	VPADDD     Y10, Y0, Y0

	VPMADDUBSW 1024(SI), Y9, Y10
	VPMADDWD   Y8, Y10, Y10
	VPADDD     Y10, Y1, Y1

	VPMADDUBSW 2048(SI), Y9, Y10
	VPMADDWD   Y8, Y10, Y10
	VPADDD     Y10, Y2, Y2

	VPMADDUBSW 3072(SI), Y9, Y10
	VPMADDWD   Y8, Y10, Y10
	VPADDD     Y10, Y3, Y3

	VPMADDUBSW 4096(SI), Y9, Y10
	VPMADDWD   Y8, Y10, Y10
	VPADDD     Y10, Y4, Y4

	VPMADDUBSW 5120(SI), Y9, Y10
	VPMADDWD   Y8, Y10, Y10
	VPADDD     Y10, Y5, Y5

	VPMADDUBSW 6144(SI), Y9, Y10
	VPMADDWD   Y8, Y10, Y10
	VPADDD     Y10, Y6, Y6

	VPMADDUBSW 7168(SI), Y9, Y10
	VPMADDWD   Y8, Y10, Y10
	VPADDD     Y10, Y7, Y7

	ADDQ $32, R8
	ADDQ $32, SI
	DECQ CX
	JNZ  loop

	VMOVDQU Y0, 0(DI)
	VMOVDQU Y1, 32(DI)
	VMOVDQU Y2, 64(DI)
	VMOVDQU Y3, 96(DI)
	VMOVDQU Y4, 128(DI)
	VMOVDQU Y5, 160(DI)
	VMOVDQU Y6, 192(DI)
	VMOVDQU Y7, 224(DI)
	VZEROUPPER
	RET

// func fc0Reduce8AVX2(out *[8]int32, acc *[8][8]int32)
//
// 把 fc0Accum8 留下的 8×8 个 i32 通道各自水平求和，写成 8 个标量。
//
// 存在的理由：原来这段在 Go 侧做（`for k := 0; k < 8; k++ { s += acc[j][k] }`），
// Go 不做自动向量化，64 次带边界检查的标量加法 + 256 字节中间数组的写入读出，
// 实测占全机 2.1%（profile 里 fc0Block4 的 flat）。搬进汇编后每个输出 7 条向量指令。
//
// 求和的**分组顺序与 Go 侧不同**，但结果逐位相同：int32 环绕加满足交换结合律，
// 且总量上限 1024×127×127 = 16.5M 远不溢出。由 TestFC0Block8MatchesScalar 守护。
//
// 每个输出：VPHADDD ×2 把 8 个通道折成「低 128 位 = 低 4 通道之和、
// 高 128 位 = 高 4 通道之和」，且各自在 128 位内**已经广播**；
// 再把两半相加得到总和，取低 32 位写出。
//
// ⚠️ 到这里必须停 —— 曾经在这后面又补了一组 `VPSHUFD $1` + `VPADDD`，
// 那是把已经广播好的值再和自己加了一遍，结果**恰好翻倍**。翻倍是好抓的，
// 但要记住它为什么错：VPHADDD 两次之后每个 128 位通道里已经是同一个值了。
TEXT ·fc0Reduce8AVX2(SB), NOSPLIT, $0-16
	MOVQ out+0(FP), DI
	MOVQ acc+8(FP), SI

	MOVQ $8, CX
	MOVQ $0, AX                 // 输出下标
redloop:
	VMOVDQU (SI), Y0            // 一个输出的 8 个 i32 通道
	VPHADDD Y0, Y0, Y0
	VPHADDD Y0, Y0, Y0
	VEXTRACTI128 $1, Y0, X1
	VPADDD  X1, X0, X0
	VMOVD   X0, (DI)(AX*4)

	ADDQ $32, SI
	INCQ AX
	DECQ CX
	JNZ  redloop

	VZEROUPPER
	RET
