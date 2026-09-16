package nnue

// Transform 的内层（「配对累加 → 裁剪到 [0,255] → 相乘 → 右移 9」）在
// forward.go 的 reluMul 里定义；本文件放它的标量整段实现与分派说明。
//
// 形状：每个视角 1024 个 int16 分成前后两半，逐对配成 s0/s1 求积，产出 512 个 byte。
// 两个视角共 1024 个 byte，也就是 L1。
//
// 为什么值得单独写内核：`Transform` 在 profile 里占 **10.6% cum**（reluMul 5.07% +
// clamp16 2.82% + 自身 2.68%），而它的算式是纯逐元素整数运算，适合向量化。
// AVX2 版一次处理 32 个元素（VPMULLW 的结果正好够一整个 32 字节的 store）。

// transformHalfScalar 是 transformHalf 的标量实现。
//
// 它同时是 AVX2 内核的**对拍基准**：内核必须与它逐位一致（TestTransformMatchesScalar）。
// 别删——它是规格，不是重复代码。
func transformHalfScalar(out []byte, psqLo, thrLo, psqHi, thrHi []int16) {
	for i := range out {
		out[i] = reluMul(psqLo[i], thrLo[i], psqHi[i], thrHi[i])
	}
}
