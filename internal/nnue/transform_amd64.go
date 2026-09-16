//go:build amd64

package nnue

// transformHalfAVX2 由 transform_amd64.s 实现。
//
// 每个 ymm 处理 16 个 int16（低半段与高半段各 16 个，共 32 个元素产出一整个
// 32 字节 store）。要求各切片长度相同，且长度是 32 的倍数 —— 调用方负责把
// 尾部截出来走标量（见下方 transformHalf）。
//
//go:noescape
func transformHalfAVX2(out []byte, psqLo, thrLo, psqHi, thrHi []int16)

// transformHalf 计算一个视角的 512 个 byte。
//
// psqLo/thrLo 与 psqHi/thrHi 是同一份累加器的前后两半（长度各 L1/2），
// 逐对配成 reluMul 的两个参数。
func transformHalf(out []byte, psqLo, thrLo, psqHi, thrHi []int16) {
	if useAVX2 {
		// 内核按 32 个元素一组推进；尾部不足一组的走标量。
		// 生产路径上 len(out) 恒为 L1/2 = 512（32 的倍数），这里的尾部逻辑
		// 是为了让内核的前提（长度是 32 的倍数）不依赖调用方的自觉。
		const block = 32
		blocks := len(out) / block
		if blocks > 0 {
			n := blocks * block
			transformHalfAVX2(out[:n], psqLo[:n], thrLo[:n], psqHi[:n], thrHi[:n])
		}
		for i := blocks * block; i < len(out); i++ {
			out[i] = reluMul(psqLo[i], thrLo[i], psqHi[i], thrHi[i])
		}
		return
	}
	transformHalfScalar(out, psqLo, thrLo, psqHi, thrHi)
}
