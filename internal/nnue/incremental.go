package nnue

import "os"

// 本文件实现累加器的增量同步：只对「特征集合的差集」做 add/sub，
// 避免每次评估都重算全部特征。
//
// 实测（初始局面，96 个激活特征）全量 Refresh 要 75µs，其中约 90% 花在
// 1024 通道的累加循环上，特征枚举只占约 10µs。走一步通常只改变几个特征，
// 所以差量更新能把这段开销压掉大半。
//
// 正确性由「增量结果必须与全量 Refresh 逐位相同」的测试保证 ——
// 增量更新一旦算错，评估值会静默偏移而不报错，必须靠对拍兜住。

// psqPsqtBase 是 psqt 合并数组里 PSQ 段的起点：文件里 psqt 是
// 「威胁段在前、PSQ 段在后」，与 C++ 的 combinedPsqtWeights 一致。
const psqPsqtBase = ThreatInputs * PSQTBuckets

// pendingLimit 是走增量路径的脏条目上限，超过它时改走全量重建。
//
// 写成变量而非常量，是为了能在一轮基准里扫描取值（见
// search.TestPendingLimitSweep）。两边都不能凭直觉走：
//
//	pendingLimit   96     256    512    1024   2048
//	相对耗时       1.000  1.000  1.078  1.086  1.141
//
// 直觉上「全量重建要枚举全部特征（约 32 次滑动攻击 + 21 条 thrAdd），
// 比增量贵得多，所以阈值该放大」是错的：提高阈值只让约 12% 的视角少走重建，
// 却让占 88% 的增量路径窗口同步变长，条目数线性增长，净效果是变慢。
//
// **往下同样有个下界**（2026-09-17 补扫到全范围，16 档反而慢 6%：重建太频繁）。
// 曲线是平台型，最优落在 32~64；2026-09-18 因为 `copy`/`clear` 刚把
// rebuildPSQ / rebuildThreats 改便宜 32~48%、阈值两侧的相对代价变过，又复扫一次
// （两次扫描都指向 32~64，且形状与 09-17 记录一致），并按平台中点把它从
// **96 下调到 48**：
//
//	扫描（安静中局 8 局面 × 6 万节点）  96→1.000×｜32→0.978×｜48→0.976×｜64→0.980×｜128→1.034×
//	A/B 裁决（15 轮交替，两批）         13/15 方向一致，两批中位比 **+2.4% / +2.0%**
//
// ⚠️ 单次扫描在 2% 噪声下不足以定这种事（同一份代码两次扫描能差 2%＋），
// 所以最终是**用交替 A/B 定的**；扫描只用来定方向。
// ⚠️ 这是**不改搜索树**的旋钮：增量与全量重建产出的累加器逐位相同
// （`TestApplyMatchesRefresh` 守着），所以改它前后逐位行为守卫完全不动。
var pendingLimit = 48

// SetPendingLimit 调整阈值并返回原值，供基准扫描使用。
func SetPendingLimit(n int) int {
	old := pendingLimit
	pendingLimit = n
	return old
}

// maxThreatPending 是 applyThreats 里栈上索引数组的容量。
//
// 正常路径不会逼近它：Apply 只在 len(pendingThreats) <= pendingLimit（48）时才
// 走增量，超过就改全量重建。64 只是一点余量 —— 真超了会退回逐条处理，
// 结果一致、只是慢一点。
const maxThreatPending = 64

// maxPSQPending 同上，用于 applyPSQ 的栈上索引数组。
const maxPSQPending = 64

// Apply 把累加器更新到局面 p 当前的特征集合。
//
// 这条路径不再枚举全盘特征，而是直接用走子过程中累积的脏信息做 add/sub。
// 只有在「累加器缓存的桶/镜像与当前不一致」或「脏信息过多/已被丢弃」时
// 才回退到全量重建。
//
// 正确性依赖两点，测试里逐条对拍：
//  1. 脏信息完整记录了「从累加器缓存的那个局面到当前局面」的全部攻击关系变化；
//  2. add/sub 是线性的，所以条目顺序不影响结果（前提是增删配对）。
func (w *Weights) Apply(p *Position, a *Accumulator) {
	if !a.ready {
		w.Reset(a)
	}

	if diagOn {
		diagStats.ApplyCalls++
		n := len(p.pendingThreats)
		diagStats.ApplyWindow += int64(n)
		if n > diagStats.MaxWindow {
			diagStats.MaxWindow = n
		}
	}

	stale := p.stale
	for c := 0; c < colorNB; c++ {
		bucket, mirror := p.FeatureBucket(c)

		if !stale && a.valid[c] && a.psqBucket[c] == int8(bucket) && a.mirror[c] == mirror &&
			len(p.pendingPieces) <= pendingLimit {
			w.applyPSQ(p, a, c, bucket, mirror)
		} else {
			if diagOn {
				rebPSQ(stale, a, c, bucket, mirror)
			}
			// 不是整表重建：桶/镜像变过的地方按 (桶, 镜像) 缓存，只补差集。
			w.refreshPSQ(p, a, c, bucket, mirror)
		}

		if !stale && a.valid[c] && a.mirror[c] == mirror &&
			len(p.pendingThreats) <= pendingLimit {
			w.applyThreats(p, a, c, mirror)
		} else {
			if diagOn {
				rebThreat(stale, a, c, mirror)
			}
			w.rebuildThreats(p, a, c, mirror)
		}

		a.psqBucket[c] = int8(bucket)
		a.mirror[c] = mirror
		a.valid[c] = true
	}
	p.clearPending()
	p.stale = false
}

// RefreshFromPosition 全量重建累加器（参考实现，供对拍与首帧使用）。
// 不清空 p 的累积脏信息 —— 调用方可能仍在同一条增量路径上。
func (w *Weights) RefreshFromPosition(p *Position, a *Accumulator) {
	for c := 0; c < colorNB; c++ {
		bucket, mirror := p.FeatureBucket(c)
		w.rebuildPSQ(p, a, c, bucket, mirror)
		w.rebuildThreats(p, a, c, mirror)
		a.psqBucket[c] = int8(bucket)
		a.mirror[c] = mirror
		a.valid[c] = true
	}
	a.ready = true
}

// applyPSQ 按脏格子更新 PSQ 累加器。索引含桶号，所以调用前必须确认桶与镜像未变。
//
// 一条脏棋子会产生「减旧行 + 加新行」两行，而**走一步天然产生「离开格(减) +
// 到达格(加)」这一对**（见 position.go 的 Make）。所以这里把两个方向分别收进
// 两列，再把两列一一配对交给两行合一的内核 —— 不吃子的走法就从 2 次单行调用
// 变成 1 次。「同一格换子」（吃子后落子那格）同时产生一加一减，也落进这两列。
//
// ⚠️ 第一遍**只收集索引、不碰累加器**：越界兜底要能从头重来，若第一遍带了
// 副作用，兜底就会把已经算过的条目再算一次。
// psqt 那 16 个 int32 不参与合并：它与累加器是两片独立数组，合并没有收益。
func (w *Weights) applyPSQ(p *Position, a *Accumulator, c, bucket int, mirror bool) {
	if diagOn {
		diagStats.ApplyPieces += int64(len(p.pendingPieces))
	}

	var addIdx, subIdx [maxPSQPending]int32
	na, ns := 0, 0
	for _, d := range p.pendingPieces {
		if d.newPc != 0 {
			if na >= maxPSQPending {
				w.applyPSQSimple(p, a, c, bucket, mirror)
				return
			}
			addIdx[na] = int32(PSQIndex(c, d.sq, int(d.newPc), bucket, mirror))
			na++
		}
		if d.oldPc != 0 {
			if ns >= maxPSQPending {
				w.applyPSQSimple(p, a, c, bucket, mirror)
				return
			}
			subIdx[ns] = int32(PSQIndex(c, d.sq, int(d.oldPc), bucket, mirror))
			ns++
		}
	}

	pair := pairCount(na, ns)
	for k := 0; k < pair; k++ {
		psqPairAddSub(a, w, c, int(addIdx[k]), int(subIdx[k]))
	}
	for k := pair; k < na; k++ {
		if diagOn {
			diagStats.SingleRows++
		}
		psqAdd(a, w, c, int(addIdx[k]))
	}
	for k := pair; k < ns; k++ {
		if diagOn {
			diagStats.SingleRows++
		}
		psqSub(a, w, c, int(subIdx[k]))
	}
}

// applyPSQSimple 是逐条处理的兜底路径，只在脏窗口超出栈上索引数组容量时使用。
func (w *Weights) applyPSQSimple(p *Position, a *Accumulator, c, bucket int, mirror bool) {
	for _, d := range p.pendingPieces {
		if d.oldPc != 0 {
			psqSub(a, w, c, PSQIndex(c, d.sq, int(d.oldPc), bucket, mirror))
		}
		if d.newPc != 0 {
			psqAdd(a, w, c, PSQIndex(c, d.sq, int(d.newPc), bucket, mirror))
		}
	}
}

// psqPairAddSub 把「加一条 PSQ 特征、减一条 PSQ 特征」合成一趟。
func psqPairAddSub(a *Accumulator, w *Weights, c, addIdx, subIdx int) {
	rowsPairAddSub(&a.PsqAcc[c],
		w.W[addIdx*L1:addIdx*L1+L1], w.W[subIdx*L1:subIdx*L1+L1])
	psqtSub(&a.PsqPsqt[c], w.Psqt[psqPsqtBase+subIdx*PSQTBuckets:psqPsqtBase+(subIdx+1)*PSQTBuckets])
	psqtAdd(&a.PsqPsqt[c], w.Psqt[psqPsqtBase+addIdx*PSQTBuckets:psqPsqtBase+(addIdx+1)*PSQTBuckets])
}

// applyThreats 按脏条目更新威胁累加器（索引只含镜像）。
//
// 这条路径是全项目最热的一段：一次安静中局搜索里 addI16/subI16 合计占
// 30.6%，其中 applyThreats 自己占 27.6%（cum）；每结点约 25 行权重
// （威胁增量 17.4 + PSQ 增量 5.5 + 重建 2.5，均实测）。
//
// 条目天然是成对出现的 —— 走一步会产生「这些关系没了」和「那些关系有了」——
// 正好喂给两行合一的内核（见 addRows2）。所以这里先把条目按方向分成两列，
// 再逐对交给内核，剩下的零头走单行。
//
// 重排条目的累加次序**不改变结果**：累加器是环绕加，和的次序可以任意重排
// （与 psqcache.go 用的是同一条论证）。落表的顺序变了也没有影响 ——
// 差值表逐位相同，界面看到的评估值就一样。
func (w *Weights) applyThreats(p *Position, a *Accumulator, c int, mirror bool) {
	ents := p.pendingThreats
	if diagOn {
		diagStats.ApplyEntries += int64(len(ents))
	}

	var addIdx, subIdx [maxThreatPending]int32
	na, ns := 0, 0
	for _, t := range ents {
		idx := ThreatIndex(c, int(t.attacker), t.from, t.to, int(t.attacked), mirror)
		if idx >= ThreatInputs {
			continue
		}
		if t.add {
			if na >= maxThreatPending {
				w.applyThreatsSimple(ents, a, c, mirror)
				return
			}
			addIdx[na] = int32(idx)
			na++
		} else {
			if ns >= maxThreatPending {
				w.applyThreatsSimple(ents, a, c, mirror)
				return
			}
			subIdx[ns] = int32(idx)
			ns++
		}
	}

	pair := pairCount(na, ns)
	for k := 0; k < pair; k++ {
		thrPairAddSub(a, w, c, int(addIdx[k]), int(subIdx[k]))
	}
	// 零头只剩同一个方向。两个「加」还能再凑一对（内核的 add+add 模式）；
	// 两个「减」凑不了 —— 那需要内核再加一个 `acc -= w1 + w2` 的循环体。
	//
	// 注意关掉配对开关时 twoAdd 必须是 0，否则零头会被「配对」成两次单行调用，
	// 与 on 态只差内核形状、量不出配对的真实收益（这正是这个开关要区分的东西）。
	twoAdd := 0
	if useRowPairing {
		twoAdd = (na - pair) / 2
	}
	for k := 0; k < twoAdd; k++ {
		thrPairSameAdd(a, w, c, int(addIdx[pair+2*k]), int(addIdx[pair+2*k+1]))
	}
	for k := pair + 2*twoAdd; k < na; k++ {
		if diagOn {
			diagStats.SingleRows++
		}
		thrAdd(a, w, c, int(addIdx[k]))
	}
	for k := pair; k < ns; k++ {
		if diagOn {
			diagStats.SingleRows++
		}
		thrSub(a, w, c, int(subIdx[k]))
	}
}

// thrPairAddSub 把「加一条威胁特征、减一条威胁特征」合成一趟。
func thrPairAddSub(a *Accumulator, w *Weights, c, addIdx, subIdx int) {
	if diagOn {
		diagStats.FusePairs++
	}
	rowsPairAddSub(&a.ThrAcc[c],
		w.ThreatW[addIdx*L1:addIdx*L1+L1], w.ThreatW[subIdx*L1:subIdx*L1+L1])
	psqtSub(&a.ThrPsqt[c], w.Psqt[subIdx*PSQTBuckets:(subIdx+1)*PSQTBuckets])
	psqtAdd(&a.ThrPsqt[c], w.Psqt[addIdx*PSQTBuckets:(addIdx+1)*PSQTBuckets])
}

// thrPairSameAdd 把两条「加」合成一趟（acc += w1 + w2）：内核的 add+add 模式。
func thrPairSameAdd(a *Accumulator, w *Weights, c, idx1, idx2 int) {
	if diagOn {
		diagStats.FusePairs++
	}
	if useFuseRows {
		addRows2(&a.ThrAcc[c],
			w.ThreatW[idx1*L1:idx1*L1+L1], w.ThreatW[idx2*L1:idx2*L1+L1], false)
	} else {
		addI16(&a.ThrAcc[c], w.ThreatW[idx1*L1:idx1*L1+L1])
		addI16(&a.ThrAcc[c], w.ThreatW[idx2*L1:idx2*L1+L1])
	}
	psqtAdd(&a.ThrPsqt[c], w.Psqt[idx1*PSQTBuckets:(idx1+1)*PSQTBuckets])
	psqtAdd(&a.ThrPsqt[c], w.Psqt[idx2*PSQTBuckets:(idx2+1)*PSQTBuckets])
}

// applyThreatsSimple 是逐条处理的兜底路径，只在脏窗口超出栈上索引数组容量时
// 使用（正常不会走到：Apply 只在 len(pendingThreats) <= pendingLimit 时才走
// 增量，超了就改全量重建）。结果与配对路径逐位相同。
func (w *Weights) applyThreatsSimple(ents []dirtyThreat, a *Accumulator, c int, mirror bool) {
	for _, t := range ents {
		idx := ThreatIndex(c, int(t.attacker), t.from, t.to, int(t.attacked), mirror)
		if idx >= ThreatInputs {
			continue
		}
		if t.add {
			thrAdd(a, w, c, idx)
		} else {
			thrSub(a, w, c, idx)
		}
	}
}

func (w *Weights) rebuildPSQ(p *Position, a *Accumulator, c, bucket int, mirror bool) {
	if diagOn {
		diagStats.RebuildPSQ++
	}
	// 必须用 copy 而不是手写循环：Go 编译器不做自动向量化，手写的
	// `for i := 0; i < L1; i++ { a.PsqAcc[c][i] = w.FTBiases[i] }` 在汇编里是
	// 1024 次「带边界检查的 16 位载入+存储」，实测占本函数 4.9%（全项目第 4）；
	// copy 会落到 runtime.memmove（AVX2/ERMS）。
	//
	// 长度不是隐患：Parse 里 FTBiases 恒为 make([]int16, L1)，读取越界会让
	// Parse 失败而不是留下短表。
	copy(a.PsqAcc[c][:], w.FTBiases)
	for k := 0; k < PSQTBuckets; k++ {
		a.PsqPsqt[c][k] = 0
	}
	for s := 0; s < squareNB; s++ {
		if pc := p.board[s]; pc != 0 {
			if diagOn {
				diagStats.RebuildPiece++
			}
			psqAdd(a, w, c, PSQIndex(c, s, int(pc), bucket, mirror))
		}
	}
}

func (w *Weights) rebuildThreats(p *Position, a *Accumulator, c int, mirror bool) {
	if diagOn {
		diagStats.RebuildThreat++
	}
	clear(a.ThrAcc[c][:]) // 同 rebuildPSQ：手写 1024 次清零循环实测占 1.4%
	for k := 0; k < PSQTBuckets; k++ {
		a.ThrPsqt[c][k] = 0
	}
	p.forEachThreat(c, mirror, func(idx int) { thrAdd(a, w, c, idx) })
}

// ---- 单条特征的加减。权重是 int8 存为 byte，必须先转 int8 再转 int16，
// 否则 int16(byte) 会做无符号扩展，负权重变成 +192 这类大正数。
//
// 内层 1024 通道循环交给 addI16 / subI16；它们在有 AVX2 时走汇编内核
// （一次 16 路），否则退回 4 路展开的标量实现，见 simd*.go。

func psqAdd(a *Accumulator, w *Weights, c, idx int) {
	base := idx * L1
	addI16(&a.PsqAcc[c], w.W[base:base+L1])
	base = psqPsqtBase + idx*PSQTBuckets
	psqtAdd(&a.PsqPsqt[c], w.Psqt[base:base+PSQTBuckets])
}

func psqSub(a *Accumulator, w *Weights, c, idx int) {
	base := idx * L1
	subI16(&a.PsqAcc[c], w.W[base:base+L1])
	base = psqPsqtBase + idx*PSQTBuckets
	psqtSub(&a.PsqPsqt[c], w.Psqt[base:base+PSQTBuckets])
}

func thrAdd(a *Accumulator, w *Weights, c, idx int) {
	base := idx * L1
	addI16(&a.ThrAcc[c], w.ThreatW[base:base+L1])
	base = idx * PSQTBuckets
	psqtAdd(&a.ThrPsqt[c], w.Psqt[base:base+PSQTBuckets])
}

func thrSub(a *Accumulator, w *Weights, c, idx int) {
	base := idx * L1
	subI16(&a.ThrAcc[c], w.ThreatW[base:base+L1])
	base = idx * PSQTBuckets
	psqtSub(&a.ThrPsqt[c], w.Psqt[base:base+PSQTBuckets])
}

// useRowPairing 决定要不要把成对的行合并成一次调用。
//
// 它是**测量用开关**：关掉之后配对结构仍在，只是每对退回两次单行调用 ——
// 于是「配对 + 融合」与「只融合」能在同一份二进制里背靠背交替，直接量出配对
// 本身值多少（跨进程比较两批 A/B 的中位数分辨不出 1% 量级的差别）。
var useRowPairing = os.Getenv("QIJING_ROWPAIR") != "off"

// SetRowPairing 切换配对并返回原值，供同一进程内交替 A/B 使用。
func SetRowPairing(on bool) bool {
	old := useRowPairing
	useRowPairing = on
	return old
}

// pairCount 返回两列里实际能配对的数量；关掉配对开关时恒为 0。
func pairCount(na, ns int) int {
	if !useRowPairing {
		return 0
	}
	if ns < na {
		return ns
	}
	return na
}

// rowsPairAddSub 把「加一行、减一行」合成一趟：acc += wAdd − wSub。
//
// 关掉融合开关时退回两次单行调用 —— 结果逐位相同（环绕加可交换结合），
// 只是慢一点。A/B 要靠这个开关在同一份二进制里切两态。
func rowsPairAddSub(acc *[L1]int16, wAdd, wSub []byte) {
	if useFuseRows {
		addRows2(acc, wAdd, wSub, true)
		return
	}
	addI16(acc, wAdd)
	subI16(acc, wSub)
}

// forEachThreat 枚举某视角下所有激活的威胁特征索引。
// 对应 FullThreats::append_active_indices，但用增量维护的位棋盘。
func (p *Position) forEachThreat(perspective int, mirror bool, fn func(idx int)) {
	occ := p.occ
	for bb := occ; !bb.isEmpty(); {
		from := bb.popLSB()
		attacker := int(p.board[from])
		pt := pieceType(attacker)
		var attacks bitboard
		if pt == ptPawn {
			attacks = pseudoAttacks[pawnSlot(pieceColor(attacker))][from]
		} else {
			attacks = attacksBB(pt, from, occ)
		}
		for t := attacks.and(occ); !t.isEmpty(); {
			to := t.popLSB()
			if idx := ThreatIndex(perspective, attacker, from, to, int(p.board[to]), mirror); idx < ThreatInputs {
				if diagOn {
					diagStats.RebuildFeat++
				}
				fn(idx)
			}
		}
	}
}

// Reset 把累加器恢复到「不含任何特征」的初值状态。
//
// 初值不是零：PSQ 累加器以 biases 起算，威胁累加器从 0 起算。两者若都用
// biases，Transform 里的 clamp(psqAcc + thrAcc) 会把 biases 计入两次，
// 导致输出系统性偏大、偏正。
func (w *Weights) Reset(a *Accumulator) {
	for c := 0; c < colorNB; c++ {
		copy(a.PsqAcc[c][:], w.FTBiases)
		clear(a.ThrAcc[c][:])
		for k := 0; k < PSQTBuckets; k++ {
			a.PsqPsqt[c][k] = 0
			a.ThrPsqt[c][k] = 0
		}
		a.psqFeat[c] = a.psqFeat[c][:0]
		a.thrFeat[c] = a.thrFeat[c][:0]
		// 重置后累加器不对应任何真实局面，标记失效让 Apply 走全量重建。
		a.valid[c] = false
	}
	a.ready = true
}

// SyncTo 把累加器从「它当前缓存的特征集合」增量更新到局面 b 的特征集合。
//
// 累加器自己记住它对应的是哪一组特征，因此可以跨任意多个局面连续同步 ——
// 中间跳过多少次评估（置换表命中、剪枝）都不影响正确性，
// 因为差集是相对「缓存的特征集合」而不是相对「上一个评估的局面」算的。
//
// 零值累加器（从未 Reset 过）会先被自动重置，避免漏掉 biases 初值。
func (w *Weights) SyncTo(b Board, a *Accumulator) {
	if !a.ready {
		w.Reset(a)
	}

	for c := 0; c < colorNB; c++ {
		// ---- PSQ 特征 ----
		cur := b.ActivePSQ(c)
		sortInts(cur)
		old := a.psqFeat[c]

		i, j := 0, 0
		for i < len(old) && j < len(cur) {
			switch {
			case old[i] < cur[j]:
				idx := old[i]
				base := idx * L1
				for k := 0; k < L1; k++ {
					a.PsqAcc[c][k] -= int16(int8(w.W[base+k]))
				}
				base = psqPsqtBase + idx*PSQTBuckets
				for k := 0; k < PSQTBuckets; k++ {
					a.PsqPsqt[c][k] -= w.Psqt[base+k]
				}
				i++
			case old[i] > cur[j]:
				idx := cur[j]
				base := idx * L1
				for k := 0; k < L1; k++ {
					a.PsqAcc[c][k] += int16(int8(w.W[base+k]))
				}
				base = psqPsqtBase + idx*PSQTBuckets
				for k := 0; k < PSQTBuckets; k++ {
					a.PsqPsqt[c][k] += w.Psqt[base+k]
				}
				j++
			default:
				i++
				j++
			}
		}
		for ; i < len(old); i++ {
			idx := old[i]
			base := idx * L1
			for k := 0; k < L1; k++ {
				a.PsqAcc[c][k] -= int16(int8(w.W[base+k]))
			}
			base = psqPsqtBase + idx*PSQTBuckets
			for k := 0; k < PSQTBuckets; k++ {
				a.PsqPsqt[c][k] -= w.Psqt[base+k]
			}
		}
		for ; j < len(cur); j++ {
			idx := cur[j]
			base := idx * L1
			for k := 0; k < L1; k++ {
				a.PsqAcc[c][k] += int16(int8(w.W[base+k]))
			}
			base = psqPsqtBase + idx*PSQTBuckets
			for k := 0; k < PSQTBuckets; k++ {
				a.PsqPsqt[c][k] += w.Psqt[base+k]
			}
		}
		a.psqFeat[c] = append(a.psqFeat[c][:0], cur...)

		// ---- 威胁特征 ----
		cur = b.ActiveThreats(c)
		sortInts(cur)
		old = a.thrFeat[c]

		i, j = 0, 0
		for i < len(old) && j < len(cur) {
			switch {
			case old[i] < cur[j]:
				idx := old[i]
				base := idx * L1
				for k := 0; k < L1; k++ {
					a.ThrAcc[c][k] -= int16(int8(w.ThreatW[base+k]))
				}
				base = idx * PSQTBuckets
				for k := 0; k < PSQTBuckets; k++ {
					a.ThrPsqt[c][k] -= w.Psqt[base+k]
				}
				i++
			case old[i] > cur[j]:
				idx := cur[j]
				base := idx * L1
				for k := 0; k < L1; k++ {
					a.ThrAcc[c][k] += int16(int8(w.ThreatW[base+k]))
				}
				base = idx * PSQTBuckets
				for k := 0; k < PSQTBuckets; k++ {
					a.ThrPsqt[c][k] += w.Psqt[base+k]
				}
				j++
			default:
				i++
				j++
			}
		}
		for ; i < len(old); i++ {
			idx := old[i]
			base := idx * L1
			for k := 0; k < L1; k++ {
				a.ThrAcc[c][k] -= int16(int8(w.ThreatW[base+k]))
			}
			base = idx * PSQTBuckets
			for k := 0; k < PSQTBuckets; k++ {
				a.ThrPsqt[c][k] -= w.Psqt[base+k]
			}
		}
		for ; j < len(cur); j++ {
			idx := cur[j]
			base := idx * L1
			for k := 0; k < L1; k++ {
				a.ThrAcc[c][k] += int16(int8(w.ThreatW[base+k]))
			}
			base = idx * PSQTBuckets
			for k := 0; k < PSQTBuckets; k++ {
				a.ThrPsqt[c][k] += w.Psqt[base+k]
			}
		}
		a.thrFeat[c] = append(a.thrFeat[c][:0], cur...)

		// 同步完成后累加器已正确对应 b 的特征集合，更新 meta 供 Apply 判断。
		bucket, mirror := b.FeatureBucket(c)
		a.psqBucket[c] = int8(bucket)
		a.mirror[c] = mirror
		a.valid[c] = true
	}
}

// sortInts 对近有序的小数组做插入排序。
//
// 特征索引按格子递增生成，只在换棋子类型时跳变，所以基本有序；
// 插入排序在近有序数据上接近线性，且没有 sort.Ints 的接口调用开销。
func sortInts(a []int) {
	for i := 1; i < len(a); i++ {
		v := a[i]
		j := i - 1
		for j >= 0 && a[j] > v {
			a[j+1] = a[j]
			j--
		}
		a[j+1] = v
	}
}
