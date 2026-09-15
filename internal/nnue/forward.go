package nnue

import "fmt"

// 本文件实现 NNUE 的累加器与前向推理，对应 Pikafish-2026-01-02 的
// nnue_accumulator.h、nnue_feature_transformer.h 的 transform()，以及
// nnue_architecture.h / layers/*.h 的 propagate()。
//
// 权重布局约定：展开后的权重文件与 C++ 的读取顺序一致，即逻辑 row-major
// [OutputDimensions][PaddedInputDimensions]。C++ 用 get_weight_index 把它重排成
// SIMD 友好的 chunk-major 内存布局，但那只影响性能，数值等价。

// 量化常量（nnue_common.h）。
const (
	outputScale     = 16
	weightScaleBits = 6
)

// Accumulator 是特征转换器的累加结果。PSQ 与威胁两套特征各有一份，
// 两者共用同一组 biases 作为初值。
type Accumulator struct {
	PsqAcc  [colorNB][L1]int16
	PsqPsqt [colorNB][PSQTBuckets]int32
	ThrAcc  [colorNB][L1]int16
	ThrPsqt [colorNB][PSQTBuckets]int32

	// meta 记录累加器当前对应哪一组特征。增量更新（Apply）靠它判断能否走
	// 局部路径：PSQ 特征的索引含桶号，桶或镜像变了就必须重建；
	// 威胁特征的索引只含镜像，桶变不影响。
	psqBucket [colorNB]int8
	mirror    [colorNB]bool
	valid     [colorNB]bool

	// psqFeat/thrFeat 记录累加器当前对应的是哪一组特征（有序）。
	// 这是 SyncTo（全量枚举 + 差集，作为参考实现保留）用的缓存。
	psqFeat [colorNB][]int
	thrFeat [colorNB][]int

	// ready 标记累加器已带上初值（biases 等）。零值累加器直接同步会漏掉初值，
	// 所以 SyncTo/Apply 在未初始化时先自行重置一次。
	ready bool
}

// Invalidate 让累加器与任何局面脱钩：下次 Apply/SyncTo 会整体重建。
// 换局（新搜索开始、局面被外部改动）时必须调用，否则残留的 meta 会让
// 增量路径拿错误基准做差集。
func (a *Accumulator) Invalidate() {
	for c := 0; c < colorNB; c++ {
		a.valid[c] = false
	}
}

// Refresh 全量重建累加器。
//
// 实现上是「重置到空集合 + 同步一次」，与增量更新共用同一条代码路径：
// 保留两套独立逻辑容易产生分歧，这里刻意只留一条。
//
// 累加器初值与 psqt 布局的细节见 incremental.go 与 Reset 的注释。
func (w *Weights) Refresh(b Board, a *Accumulator) {
	w.Reset(a)
	w.SyncTo(b, a)
}

// Transform 对应 FeatureTransformer::transform，返回 PSQT 标量与 1024 字节特征向量。
func (a *Accumulator) Transform(sideToMove, bucket int) (int32, [L1]byte) {
	// 桶号由子力计数推出，取值只能是 0..15。越界意味着计数已被算错
	// （典型的根因是评估侧局面与搜索侧失配，见 Position.CheckCounts）。
	// 与其让后面以隐蔽的方式读错权重，不如在这里带着线索直接失败。
	if bucket < 0 || bucket >= LayerStacks {
		panic(fmt.Sprintf(
			"nnue: LayerStackBucket=%d 越界（side=%d）—— 子力计数已错，请用 Position.CheckCounts() 核对评估侧局面是否与搜索侧一致",
			bucket, sideToMove))
	}
	p0, p1 := sideToMove, sideToMove^1

	psqt := a.PsqPsqt[p0][bucket] - a.PsqPsqt[p1][bucket]
	psqt += a.ThrPsqt[p0][bucket] - a.ThrPsqt[p1][bucket]
	psqt /= 2

	var out [L1]byte
	persp := [2]int{p0, p1}
	for p := 0; p < 2; p++ {
		offset := (L1 / 2) * p
		psa, tha := &a.PsqAcc[persp[p]], &a.ThrAcc[persp[p]]
		// 4 路展开：clamp 带两个分支，展开能让四条独立链的
		// 分支互相穿插，减少预测失败造成的流水线空洞。
		for j := 0; j < L1/2; j += 4 {
			out[offset+j] = reluMul(psa[j], tha[j], psa[j+L1/2], tha[j+L1/2])
			out[offset+j+1] = reluMul(psa[j+1], tha[j+1], psa[j+1+L1/2], tha[j+1+L1/2])
			out[offset+j+2] = reluMul(psa[j+2], tha[j+2], psa[j+2+L1/2], tha[j+2+L1/2])
			out[offset+j+3] = reluMul(psa[j+3], tha[j+3], psa[j+3+L1/2], tha[j+3+L1/2])
		}
	}
	return psqt, out
}

// reluMul 是 Transform 内层的单元素运算：
// 先把两段和各自 clamp 到 [0,255]，再按 sum0*sum1/512 求积（右移代替除法）。
//
// 注：Transform 是每次评估都要走的路径（约 6µs 里占不小一块），
// 这里的 clamp16 两个分支在随机局面上很难预测，故调用方按 4 路展开。
func reluMul(psq0, thr0, psq1, thr1 int16) byte {
	s0 := uint32(clamp16(psq0+thr0, 0, 255))
	s1 := uint32(clamp16(psq1+thr1, 0, 255))
	return byte(s0 * s1 >> 9)
}

// Evaluate 算出某局面的 (PSQT, Positional)。两者相加即为 NNUE 的原始输出，
// 除以 outputScale 后才是常规 Value。
func (w *Weights) Evaluate(b Board, sideToMove int, a *Accumulator) (psqt, positional int32) {
	bucket := b.LayerStackBucket(sideToMove)
	psqt, feat := a.Transform(sideToMove, bucket)
	positional = w.Layers[bucket].propagate(&feat)
	return psqt, positional
}

// EvalValue 返回「走子方视角」的评估值，已按 OutputScale 缩放。
// 搜索层应使用此函数：NNUE 的 psqt 与 features 都是按走子方视角构造的。
func (w *Weights) EvalValue(b Board, sideToMove int, a *Accumulator) int32 {
	psqt, positional := w.Evaluate(b, sideToMove, a)
	return (psqt + positional) / outputScale
}

// EvaluateAt 是 Evaluate 的增量局面版本：桶号直接从 Position 读，不再扫全盘。
func (w *Weights) EvaluateAt(p *Position, a *Accumulator) (psqt, positional int32) {
	bucket := p.LayerStackBucket()
	psqt, feat := a.Transform(p.side, bucket)
	positional = w.Layers[bucket].propagate(&feat)
	return psqt, positional
}

// EvalValueAt 返回走子方视角的评估值。
func (w *Weights) EvalValueAt(p *Position, a *Accumulator) int32 {
	psqt, positional := w.EvaluateAt(p, a)
	return (psqt + positional) / outputScale
}

// propagate 对应 NetworkArchitecture::propagate。
//
// fc_0（16×1024 的 int8×uint8 乘加）是整段推理的瓶颈，占约 23.3%。
// 内层点积交给 fc0Block4：有 AVX2 时走汇编内核（VPMADDUBSW + VPMADDWD，
// 等效一次 32 路 int8 MAC），否则退回标量。
//
// 一次处理 4 个输出而不是逐个算：激活向量的 1024 字节在一轮内层循环里
// 只读一遍，四个输出共享，省下三份重复读取。
//
// 注：也曾试过利用输入稀疏性（1024 维里约 118 个非零）跳过零项，实测反而
// 慢 30%：稀疏索引破坏了 row 的顺序访问，cache 局部性损失超过省下的乘法。
func (ls *LayerStack) propagate(feat *[L1]byte) int32 {
	var fc0 [FC0Out]int32
	for j := 0; j < FC0Out; j += 4 {
		b0 := j * FC0PaddedIn
		w := ls.FC0W[b0 : b0+4*FC0PaddedIn]

		var s [4]int32
		fc0Block4(&s, w, feat)

		fc0[j] = s[0] + ls.FC0Bias[j]
		fc0[j+1] = s[1] + ls.FC0Bias[j+1]
		fc0[j+2] = s[2] + ls.FC0Bias[j+2]
		fc0[j+3] = s[3] + ls.FC0Bias[j+3]
	}

	// SqrClippedReLU 与 ClippedReLU 拼接成 fc_1 的输入。
	// 注意 C++ 里 memcpy 的起点是 FC_0_OUTPUTS，所以 sqr 的第 16 路被 clipped 覆盖。
	var fc1in [FC1PaddedIn]byte
	for i := 0; i < FC0Out; i++ {
		fc1in[i] = sqrClippedReLU(fc0[i])
	}
	for i := 0; i < FC0Out-1; i++ {
		fc1in[FC0Out-1+i] = clippedReLU(fc0[i])
	}

	// fc_1: 30 → L3
	var fc1 [L3]int32
	for j := 0; j < L3; j++ {
		base := j * FC1PaddedIn
		s := ls.FC1Bias[j]
		for i := 0; i < FC1In; i++ {
			s += int32(int8(ls.FC1W[base+i])) * int32(fc1in[i])
		}
		fc1[j] = s
	}

	var fc2in [L3]byte
	for i := 0; i < L3; i++ {
		fc2in[i] = clippedReLU(fc1[i])
	}

	// fc_2: L3 → 1
	s := ls.FC2Bias[0]
	for i := 0; i < L3; i++ {
		s += int32(int8(ls.FC2W[i])) * int32(fc2in[i])
	}

	// fc_0 多出的那一路由 1.0 的量纲折算后直接加到输出。
	fwdOut := fc0[FC0Out-1] * (600 * outputScale) / (127 * (1 << weightScaleBits))
	return s + fwdOut
}

// clippedReLU 对应 ClippedReLU::propagate。
func clippedReLU(v int32) byte {
	return byte(clamp32(v>>weightScaleBits, 0, 127))
}

// sqrClippedReLU 对应 SqrClippedReLU::propagate。
// 源码用 >> (2*WeightScaleBits + 7) 代替 /127 以求快，训练侧已补偿。
func sqrClippedReLU(v int32) byte {
	s := (int64(v) * int64(v)) >> (2*weightScaleBits + 7)
	if s > 127 {
		return 127
	}
	return byte(s)
}

// clamp16 把 v 截到 [lo, hi]。
//
// 用内置 min/max 而不是两个 if：两个比较在随机局面上都几乎不可预测，
// 而 Transform 每次评估要跑 1024 次（2 视角 × 512），分支预测失败的代价
// 会直接反映在评估耗时上。内置 min/max 对定长整数会编译成无分支序列。
func clamp16(v, lo, hi int16) int16 {
	return max(lo, min(v, hi))
}

// clamp32 把 v 截到 [lo, hi]。与 clamp16 同理，用无分支的内置 min/max。
func clamp32(v, lo, hi int32) int32 {
	return max(lo, min(v, hi))
}
