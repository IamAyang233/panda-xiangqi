// nnue-prepare 把 pikafish.nnue 展开成定长权重文件。
//
// 原文件是双层压缩：外层 zstd，内层对 biases 与 psqt 用 COMPRESSED_LEB128。
// 展开后服务端只顺序读定长数组，不需要任何解压依赖。
//
// 输出保留原始结构（文件头、各张量组前的 4 字节哈希、张量顺序），
// 只把两处 LEB128 换成定长数组，因此加载端可与 C++ 的
// Network::read_parameters 逐行对照。
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/klauspost/compress/zstd"
)

// 网络架构常量，对应 Pikafish-2026-01-02 的 src/nnue。
const (
	fileVersion = 0x7AF32F20 // nnue_common.h: Version

	l1 = 1024 // nnue_architecture.h: TransformedFeatureDimensionsBig
	l2 = 15   // nnue_architecture.h: L2Big（fc_0 输出维度为 l2+1）
	l3 = 32   // nnue_architecture.h: L3Big

	psqtBuckets  = 16 // nnue_architecture.h: PSQTBuckets
	layerStacks  = 16 // nnue_architecture.h: LayerStacks
	maxSimdWidth = 32 // nnue_common.h: MaxSimdWidth

	threatInputs = 45649                    // features/full_threats.h: Dimensions
	psqInputs    = 62185 - threatInputs     // 62185 来自 UCI 描述的总维度
	totalInputs  = threatInputs + psqInputs // 62185

	// 解压后长度，作为「解析起点正确」的硬断言。
	rawNetSize = 65946814

	// 两处哈希，用于逐段校验偏移是否正确。
	ftHashValue = 0x0D17B900 // FullThreats::HashValue ^ (l1 * 2)
	stackHash   = 0x63336A4A // NetworkArchitecture::get_hash_value()

	lebMagic = "COMPRESSED_LEB128"
)

// ceilToMultiple 对应 nnue_common.h 的 ceil_to_multiple。
func ceilToMultiple(n, base int) int { return (n + base - 1) / base * base }

// 各张量字节数。bias 是 int32：affine_transform.h 内 `using BiasType = OutputType`
// 覆盖了 nnue_common.h 的全局 int16 定义。
var (
	fc0Out  = l2 + 1                        // 16
	fc1In   = l2 * 2                        // 30（ac_sqr_0 与 ac_0 拼接）
	biases  = l1 * 2                        // FeatureTransformer biases: int16
	threatW = threatInputs * l1             // threatWeights: int8
	weights = psqInputs * l1                // weights: int8
	psqt    = totalInputs * psqtBuckets * 4 // combined psqtWeights: int32
	stack   = 4 + fc0Out*4 + ceilToMultiple(l1, maxSimdWidth)*fc0Out +
		l3*4 + ceilToMultiple(fc1In, maxSimdWidth)*l3 +
		1*4 + ceilToMultiple(l3, maxSimdWidth)*1
	layersBytes = stack * layerStacks
)

type bucket struct {
	name string
	off  int
	n    int
}

func main() {
	in := flag.String("in", "engines/pikafish.nnue", "输入的 pikafish.nnue（zstd 压缩）")
	out := flag.String("out", "engines/pikafish.nnue.flat", "输出的展开权重文件")
	flag.Parse()

	if stack != 17640 {
		fatalf("LayerStack 步长推导有误：得到 %d，实测应为 17640", stack)
	}

	raw, err := decompress(*in)
	if err != nil {
		fatalf("%v", err)
	}
	if len(raw) != rawNetSize {
		fatalf("解压后长度 %d，预期 %d", len(raw), rawNetSize)
	}

	flat, report, err := expand(raw)
	if err != nil {
		fatalf("%v", err)
	}

	if err := os.WriteFile(*out, flat, 0o644); err != nil {
		fatalf("写入失败: %v", err)
	}

	fmt.Printf("输入  %s\n", *in)
	fmt.Printf("输出  %s  (%d 字节)\n\n", *out, len(flat))
	fmt.Printf("%-22s %10s %10s\n", "张量", "偏移", "字节")
	for _, b := range report {
		fmt.Printf("%-22s %10d %10d\n", b.name, b.off, b.n)
	}
	fmt.Printf("\n全部校验通过\n")
}

func fatalf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "错误: "+format+"\n", a...)
	os.Exit(1)
}

func decompress(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	d, err := zstd.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("创建 zstd 解码器: %w", err)
	}
	defer d.Close()

	data, err := io.ReadAll(d)
	if err != nil {
		return nil, fmt.Errorf("zstd 解压: %w", err)
	}
	return data, nil
}

// expand 按 C++ 的读取顺序重排为定长布局。
func expand(raw []byte) ([]byte, []bucket, error) {
	p := 0
	take := func(n int) []byte {
		b := raw[p : p+n]
		p += n
		return b
	}

	// Network::read_header：version + hash + descSize + desc
	if v := binary.LittleEndian.Uint32(raw[0:4]); v != fileVersion {
		return nil, nil, fmt.Errorf("版本不匹配: 0x%08X", v)
	}
	header := take(12)
	descSize := int(binary.LittleEndian.Uint32(header[8:12]))
	desc := take(descSize)

	// Detail::read_parameters(featureTransformer)
	if h := binary.LittleEndian.Uint32(raw[p : p+4]); h != ftHashValue {
		return nil, nil, fmt.Errorf("FeatureTransformer 哈希不匹配: 0x%08X", h)
	}
	ftHash := take(4)

	// FeatureTransformer::read_parameters
	ftBiases, np, err := readLEB128(raw, p, l1, 16)
	if err != nil {
		return nil, nil, fmt.Errorf("解码 biases: %w", err)
	}
	p = np
	tw := take(threatW)
	w := take(weights)
	psqtVals, np2, err := readLEB128(raw, p, totalInputs*psqtBuckets, 32)
	if err != nil {
		return nil, nil, fmt.Errorf("解码 psqt: %w", err)
	}
	p = np2
	layers := take(layersBytes)

	if p != len(raw) {
		return nil, nil, fmt.Errorf("解析未消费完输入：剩余 %d 字节", len(raw)-p)
	}
	for k := 0; k < layerStacks; k++ {
		off := k * stack
		if h := binary.LittleEndian.Uint32(layers[off : off+4]); h != stackHash {
			return nil, nil, fmt.Errorf("第 %d 个 LayerStack 哈希不匹配: 0x%08X", k, h)
		}
	}

	// 组装：结构不变，仅把 LEB128 段换成定长数组。
	var out []byte
	var report []bucket
	add := func(name string, b []byte) {
		report = append(report, bucket{name, len(out), len(b)})
		out = append(out, b...)
	}
	add("header", header)
	add("description", desc)
	add("ft_hash", ftHash)
	add("ft_biases", packInt16(ftBiases))
	add("threat_weights", tw)
	add("weights", w)
	add("psqt", packInt32(psqtVals))
	add("layer_stacks", layers)
	return out, report, nil
}

// readLEB128 解出 count 个有符号变长整数，返回新偏移。
// 格式：[17 字节魔数][uint32 压缩长度][变长数据]，对应 nnue_common.h: read_leb_128。
func readLEB128(src []byte, pos, count, bits int) ([]int64, int, error) {
	if pos+17 > len(src) || string(src[pos:pos+17]) != lebMagic {
		return nil, 0, fmt.Errorf("魔数不匹配 @%d", pos)
	}
	pos += 17
	if pos+4 > len(src) {
		return nil, 0, fmt.Errorf("长度字段越界 @%d", pos)
	}
	n := int(binary.LittleEndian.Uint32(src[pos:]))
	pos += 4
	if pos+n > len(src) {
		return nil, 0, fmt.Errorf("数据段越界：需要 %d，剩余 %d", n, len(src)-pos)
	}
	data := src[pos : pos+n]
	pos += n

	out := make([]int64, count)
	q := 0
	for i := range out {
		var result int64
		shift := 0
		for {
			if q >= len(data) {
				return nil, 0, fmt.Errorf("数据提前耗尽（第 %d 个值）", i)
			}
			b := data[q]
			q++
			result |= int64(b&0x7F) << uint(shift)
			shift += 7

			if b&0x80 == 0 {
				if bits <= shift || b&0x40 == 0 {
					break // 非负，无需符号扩展
				}
				result |= ^((int64(1) << uint(shift)) - 1)
				break
			}
			if shift >= bits {
				break
			}
		}
		out[i] = result
	}
	if q != len(data) {
		return nil, 0, fmt.Errorf("数据未完全消费：剩余 %d 字节", len(data)-q)
	}
	return out, pos, nil
}

func packInt16(vals []int64) []byte {
	b := make([]byte, len(vals)*2)
	for i, v := range vals {
		binary.LittleEndian.PutUint16(b[i*2:], uint16(v))
	}
	return b
}

func packInt32(vals []int64) []byte {
	b := make([]byte, len(vals)*4)
	for i, v := range vals {
		binary.LittleEndian.PutUint32(b[i*4:], uint32(v))
	}
	return b
}
