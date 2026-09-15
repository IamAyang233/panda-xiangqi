// Package nnue 读取由 cmd/nnue-prepare 展开后的权重文件。
//
// 文件布局与 Pikafish 的 Network::read_parameters 一一对应：文件头、
// 特征转换器、16 个 LayerStack，每个张量组前的 4 字节哈希保留用于校验。
// 与 C++ 的唯一区别是两处 COMPRESSED_LEB128 已展开成定长数组。
package nnue

import (
	"encoding/binary"
	"fmt"
	"os"
)

// 架构常量，对应 Pikafish-2026-01-02 的 src/nnue。
const (
	Version = 0x7AF32F20 // nnue_common.h: Version

	L1 = 1024 // nnue_architecture.h: TransformedFeatureDimensionsBig
	L2 = 15   // L2Big，fc_0 的输出维度为 L2+1
	L3 = 32   // L3Big

	PSQTBuckets  = 16 // PSQTBuckets
	LayerStacks  = 16 // LayerStacks
	MaxSimdWidth = 32 // nnue_common.h: MaxSimdWidth

	ThreatInputs = 45649                // features/full_threats.h: Dimensions
	PSQInputs    = 62185 - ThreatInputs // features/half_ka_v2_hm.h: Dimensions
	TotalInputs  = ThreatInputs + PSQInputs

	FTHash    = 0x0D17B900 // FeatureTransformer::get_hash_value()
	StackHash = 0x63336A4A // NetworkArchitecture::get_hash_value()
)

// 层的输入输出维度（nnue_architecture.h 的 NetworkArchitecture）。
const (
	FC0Out = L2 + 1 // fc_0 输出；多出的 1 路是 fwdOut
	FC1In  = L2 * 2 // fc_1 输入；SqrClippedReLU 与 ClippedReLU 拼接
)

// 权重按 MaxSimdWidth 对齐后每行的宽度。
const (
	FC0PaddedIn = L1 // ceil(L1, 32)
	FC1PaddedIn = 32 // ceil(FC1In=30, 32)
	FC2PaddedIn = 32 // ceil(L3=32, 32)
)

// 各段字节数。affine 层的 bias 是 int32 而非 int16：affine_transform.h 内的
// `using BiasType = OutputType` 覆盖了 nnue_common.h 的全局 int16 定义。
const (
	HeaderBytes  = 12
	DescBytes    = 204
	BiasesBytes  = L1 * 2
	ThreatWBytes = ThreatInputs * L1
	WBytes       = PSQInputs * L1
	PsqtCount    = TotalInputs * PSQTBuckets
	PsqtBytes    = PsqtCount * 4

	FC0BiasBytes = FC0Out * 4
	FC0WBytes    = FC0PaddedIn * FC0Out
	FC1BiasBytes = L3 * 4
	FC1WBytes    = FC1PaddedIn * L3
	FC2BiasBytes = 4
	FC2WBytes    = FC2PaddedIn

	// StackBytes 是单个 LayerStack 的长度：哈希 + 三个 affine 层（ClippedReLU 无参数）。
	StackBytes = 4 + FC0BiasBytes + FC0WBytes + FC1BiasBytes + FC1WBytes + FC2BiasBytes + FC2WBytes

	// FileBytes 是展开后文件的精确长度。
	FileBytes = HeaderBytes + DescBytes + 4 + BiasesBytes + ThreatWBytes + WBytes +
		PsqtBytes + StackBytes*LayerStacks
)

// Weights 是展开后的网络权重。各字段是同一块只读缓冲的切片视图，
// 大张量不复制；只要 Weights 存活，整块缓冲就常驻内存。
type Weights struct {
	Desc string

	FTBiases []int16 // L1，特征转换器偏置
	ThreatW  []byte  // ThreatInputs × L1，有符号 int8，取值用 int8(b[i])
	W        []byte  // PSQInputs × L1，有符号 int8
	Psqt     []int32 // TotalInputs × PSQTBuckets，威胁特征段在前

	Layers []LayerStack
}

// LayerStack 是三段全连接层。权重为 int8，按 nnue 的置换序存储，
// 使用时的下标映射由推理层负责。
type LayerStack struct {
	FC0Bias []int32 // FC0Out
	FC0W    []byte  // FC0Out × L1
	FC1Bias []int32 // L3
	FC1W    []byte  // L3 × FC1PaddedIn
	FC2Bias []int32 // 1
	FC2W    []byte  // FC2PaddedIn
}

// Load 读取并校验展开后的权重文件。
func Load(path string) (*Weights, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	w, err := Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return w, nil
}

// Parse 解析内存中的展开权重，校验失败时返回错误。
func Parse(raw []byte) (*Weights, error) {
	r := reader{src: raw}
	if v := r.u32(); v != Version {
		return nil, fmt.Errorf("版本不匹配 0x%08X（若为 zstd 压缩的原文件，请先用 nnue-prepare 展开）", v)
	}
	r.skip(4) // 网络哈希
	descSize := int(r.u32())
	if descSize < 0 || descSize > 4096 {
		return nil, fmt.Errorf("描述串长度异常: %d", descSize)
	}
	desc := string(r.bytes(descSize))

	if h := r.u32(); h != FTHash {
		return nil, fmt.Errorf("FeatureTransformer 哈希不匹配 0x%08X", h)
	}

	w := &Weights{Desc: desc}
	w.FTBiases = r.int16s(L1)
	w.ThreatW = r.bytes(ThreatWBytes)
	w.W = r.bytes(WBytes)
	w.Psqt = r.int32s(PsqtCount)

	w.Layers = make([]LayerStack, 0, LayerStacks)
	for i := 0; i < LayerStacks; i++ {
		if h := r.u32(); h != StackHash {
			return nil, fmt.Errorf("第 %d 个 LayerStack 哈希不匹配 0x%08X", i, h)
		}
		w.Layers = append(w.Layers, LayerStack{
			FC0Bias: r.int32s(FC0Out),
			FC0W:    r.bytes(FC0WBytes),
			FC1Bias: r.int32s(L3),
			FC1W:    r.bytes(FC1WBytes),
			FC2Bias: r.int32s(1),
			FC2W:    r.bytes(FC2WBytes),
		})
	}

	if err := r.err(); err != nil {
		return nil, err
	}
	if r.pos != len(raw) {
		return nil, fmt.Errorf("解析未消费完文件：剩余 %d 字节", len(raw)-r.pos)
	}
	return w, nil
}

// reader 顺序读取小端数据，出错后置位并跳过后续读取。
type reader struct {
	src []byte
	pos int
	bad error
}

func (r *reader) bytes(n int) []byte {
	if r.bad != nil {
		return nil
	}
	if n < 0 || r.pos+n > len(r.src) {
		r.bad = fmt.Errorf("越界读取：需要 %d 字节，剩余 %d", n, len(r.src)-r.pos)
		return nil
	}
	b := r.src[r.pos : r.pos+n]
	r.pos += n
	return b
}

func (r *reader) skip(n int) { r.bytes(n) }

func (r *reader) u32() uint32 {
	b := r.bytes(4)
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint32(b)
}

func (r *reader) int16s(n int) []int16 {
	b := r.bytes(n * 2)
	if b == nil {
		return nil
	}
	out := make([]int16, n)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(b[i*2:]))
	}
	return out
}

func (r *reader) int32s(n int) []int32 {
	b := r.bytes(n * 4)
	if b == nil {
		return nil
	}
	out := make([]int32, n)
	for i := range out {
		out[i] = int32(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return out
}

func (r *reader) err() error { return r.bad }
