package nnue

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

// pendingLimit 是走增量路径的脏条目上限。超过它时全量重建反而更便宜
// （全量要枚举约 96 个特征，每个特征一次 1024 通道累加）。
const pendingLimit = 96

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

	stale := p.stale
	for c := 0; c < colorNB; c++ {
		bucket, mirror := p.FeatureBucket(c)

		if !stale && a.valid[c] && a.psqBucket[c] == int8(bucket) && a.mirror[c] == mirror &&
			len(p.pendingPieces) <= pendingLimit {
			w.applyPSQ(p, a, c, bucket, mirror)
		} else {
			w.rebuildPSQ(p, a, c, bucket, mirror)
		}

		if !stale && a.valid[c] && a.mirror[c] == mirror &&
			len(p.pendingThreats) <= pendingLimit {
			w.applyThreats(p, a, c, mirror)
		} else {
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
func (w *Weights) applyPSQ(p *Position, a *Accumulator, c, bucket int, mirror bool) {
	for _, d := range p.pendingPieces {
		if d.oldPc != 0 {
			psqSub(a, w, c, PSQIndex(c, d.sq, int(d.oldPc), bucket, mirror))
		}
		if d.newPc != 0 {
			psqAdd(a, w, c, PSQIndex(c, d.sq, int(d.newPc), bucket, mirror))
		}
	}
}

// applyThreats 按脏条目更新威胁累加器（索引只含镜像）。
func (w *Weights) applyThreats(p *Position, a *Accumulator, c int, mirror bool) {
	for _, t := range p.pendingThreats {
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
	for i := 0; i < L1; i++ {
		a.PsqAcc[c][i] = w.FTBiases[i]
	}
	for k := 0; k < PSQTBuckets; k++ {
		a.PsqPsqt[c][k] = 0
	}
	for s := 0; s < squareNB; s++ {
		if pc := p.board[s]; pc != 0 {
			psqAdd(a, w, c, PSQIndex(c, s, int(pc), bucket, mirror))
		}
	}
}

func (w *Weights) rebuildThreats(p *Position, a *Accumulator, c int, mirror bool) {
	for i := 0; i < L1; i++ {
		a.ThrAcc[c][i] = 0
	}
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
	for k := 0; k < PSQTBuckets; k++ {
		a.PsqPsqt[c][k] += w.Psqt[base+k]
	}
}

func psqSub(a *Accumulator, w *Weights, c, idx int) {
	base := idx * L1
	subI16(&a.PsqAcc[c], w.W[base:base+L1])
	base = psqPsqtBase + idx*PSQTBuckets
	for k := 0; k < PSQTBuckets; k++ {
		a.PsqPsqt[c][k] -= w.Psqt[base+k]
	}
}

func thrAdd(a *Accumulator, w *Weights, c, idx int) {
	base := idx * L1
	addI16(&a.ThrAcc[c], w.ThreatW[base:base+L1])
	base = idx * PSQTBuckets
	for k := 0; k < PSQTBuckets; k++ {
		a.ThrPsqt[c][k] += w.Psqt[base+k]
	}
}

func thrSub(a *Accumulator, w *Weights, c, idx int) {
	base := idx * L1
	subI16(&a.ThrAcc[c], w.ThreatW[base:base+L1])
	base = idx * PSQTBuckets
	for k := 0; k < PSQTBuckets; k++ {
		a.ThrPsqt[c][k] -= w.Psqt[base+k]
	}
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
		for i := 0; i < L1; i++ {
			a.PsqAcc[c][i] = w.FTBiases[i]
			a.ThrAcc[c][i] = 0
		}
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
