#include "textflag.h"

// Transform 内层的 AVX2 内核：每个视角 512 个 int16 对 → 512 个 byte。
//
// 每轮处理 32 个元素：输入侧字节偏移 AX 从 0 走 2*len、步长 64（int16），
// 输出侧字节偏移 DI 从 0 走 len、步长 32（byte）。
//
//	Y0 = clamp(psqLo[i..i+15]   + thrLo[i..i+15],   0, 255)
//	Y1 = clamp(psqLo[i+16..i+31]+ thrLo[i+16..i+31], 0, 255)
//	Y2 = clamp(psqHi[i..i+15]   + thrHi[i..i+15],   0, 255)
//	Y3 = clamp(psqHi[i+16..i+31]+ thrHi[i+16..i+31], 0, 255)
//	P0 = (Y0*Y2) >> 9      P1 = (Y1*Y3) >> 9
//	out[i..i+31] = pack(P0, P1)
//
// 三个「必须逐位一致」的点（对拍见 transform_test.go）：
//  1. psq+thr 用 VPADDW，会像标量 int16 加法一样回绕 —— 这正是要的，参考实现也是
//     int16 加法，回绕出的值一样会被 clamp 掉。
//  2. 裁剪用 VPMINSW/VPMAXSW（**有符号**），不能用无符号版本：参考实现的 clamp16
//     就是有符号比较。
//  3. 乘积用 VPMULLW 只取低 16 位。两个操作数都已 ∈[0,255]，积 ≤ 65025，
//     低 16 位就是完整值；随后 VPSRLW（逻辑右移）9 位得 0..127，正好一个 byte。
//
// 打包那一对指令的原理：VPACKUSWB 按 128 位通道独立打包，于是
//
//	VPACKUSWB P1, P0, Y6  →  q0 = i..i+7      q1 = i+16..i+23
//	                          q2 = i+8..i+15    q3 = i+24..i+31
//
// 需要的是 q0,q2,q1,q3 —— VPERMQ 的 imm=0xD8 正是 [0,2,1,3]，换完就是连续 32 字节。
// 用一次 32 字节 store 而不是两次 8 字节，是因为后者还要多一条 VEXTRACTI128。
//
// func transformHalfAVX2(out []byte, psqLo, thrLo, psqHi, thrHi []int16)
//
// 调用方保证五个切片等长且长度是 32 的倍数（尾数在 Go 侧走标量）。
// 四组地址只按 (base)(AX*1) 形式取，AX 每轮加 64，两边同步推进。
//
// ⚠️ 只能用 caller-save 寄存器（AX/CX/DX/BX/SI/DI/R8~R11 与 YMM0~14）。
// Go 的寄存器 ABI 里 **R12/R13/R14/R15 是 callee-save**、R14 还是 g 指针，
// 不保存就改会让调用方拿到错的值 —— 症状是搜索里随机段错误（PC=0），
// 而 go vet 的 FP 偏移检查查不出这一类。
TEXT ·transformHalfAVX2(SB), NOSPLIT, $0-120
	MOVQ out_base+0(FP), R8
	MOVQ psqLo_base+24(FP), R9
	MOVQ thrLo_base+48(FP), R10
	MOVQ psqHi_base+72(FP), R11
	MOVQ thrHi_base+96(FP), SI

	// 轮数 = len/32（len 是元素数；每轮 32 个元素 = 64 字节）
	MOVQ psqLo_len+32(FP), CX
	SHRQ $5, CX

	// Y4 = 0，Y5 = 255：255 由全 1 逻辑右移 8 位得到
	VPXOR    Y4, Y4, Y4
	VPCMPEQW Y5, Y5, Y5
	VPSRLW   $8, Y5, Y5

	// AX = 输入字节偏移（每轮 32 个 int16 = 64 字节）
	// DI = 输出字节偏移（每轮 32 个 byte = 32 字节）
	// ⚠️ 两者必须分开：输入每个元素 2 字节、输出 1 字节，用同一个偏移会让
	// 第二轮把 32 字节写到 out 之外（越界），症状是「参考值看起来是错的」——
	// 因为越界写先改掉了参考数组所在的堆内存。
	XORQ AX, AX
	XORQ DI, DI
loop:
	VMOVDQU (R9)(AX*1), Y0
	VPADDW  (R10)(AX*1), Y0, Y0
	VPMINSW Y5, Y0, Y0
	VPMAXSW Y4, Y0, Y0

	VMOVDQU 32(R9)(AX*1), Y1
	VPADDW  32(R10)(AX*1), Y1, Y1
	VPMINSW Y5, Y1, Y1
	VPMAXSW Y4, Y1, Y1

	VMOVDQU (R11)(AX*1), Y2
	VPADDW  (SI)(AX*1), Y2, Y2
	VPMINSW Y5, Y2, Y2
	VPMAXSW Y4, Y2, Y2

	VMOVDQU 32(R11)(AX*1), Y3
	VPADDW  32(SI)(AX*1), Y3, Y3
	VPMINSW Y5, Y3, Y3
	VPMAXSW Y4, Y3, Y3

	VPMULLW Y2, Y0, Y0
	VPSRLW  $9, Y0, Y0
	VPMULLW Y3, Y1, Y1
	VPSRLW  $9, Y1, Y1

	VPACKUSWB Y1, Y0, Y6
	VPERMQ    $0xD8, Y6, Y6
	VMOVDQU   Y6, (R8)(DI*1)

	ADDQ $64, AX
	ADDQ $32, DI
	DECQ CX
	JNZ  loop

	VZEROUPPER
	RET
