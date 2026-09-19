package nnue

import (
	"os"
	"sync"
	"unsafe"
)

// PSQ 累加器的 (桶, 镜像) 缓存 —— 换桶时不必整表重建。
//
// 起因：FeatureBucket 同时依赖双方将位与子力（见 position.go），所以「将挪一格」
// 或「最后一只车被吃掉」都会换桶。桶一变，这一视角**全部** PSQ 特征的权重行都换了
// 一片（PSQIndex 里的 `+ psqCount*bucket`），增量路径的前提「索引不变」就不成立，
// 于是原来的做法是把 31.6 行全部重算。实测触发原因分解：桶变 **65.3%**、
// 镜像变 **33.1%**，重建条目占 PSQ 全部行数的 **43%**，成本占全机 **6.4%**
// （20 个安静局面、固定 6 万结点/局面）。项目此前把这两类触发当成「设计代价」，
// 但皮卡鱼的 AccumulatorCaches 正相反：它按己方将位分 9 档再乘 attack bucket
// 缓存 PSQ 部分，刷新时只对「与缓存不同的那几颗子」做差集。
//
// 所以这里按 (桶, 镜像) 缓存一份 PSQ 累加器：它们是「偏置 + 每颗子一行」的**纯函数**，
// 只依赖 (桶, 镜像, 棋盘)。下次换到同一个桶，只需把缓存拷回来再补上棋盘之间的差集。
//
// ⚠️ 逐位正确性（不是近似，是可证的）：加法是**环绕加**（AVX2 用 VPADDW、
// 标量路径是 Go 的 int16 `+=`），逐元素取模 2^16，满足交换结合律 ⇒
// 「缓存 + 差集」与「全量重建」算的是同一个和，只是加法次序不同 ⇒ 逐位相同。
// 若哪天内核改成饱和加（PADDSW），这条论证失效，缓存路径必须重做。
//
// 缓存项是**自洽**的（值与其对应的棋盘一起存），所以不需要在任何地方做失效：
// 换了局、或者有测试走了 RefreshFromPosition 那条参考路径，差集仍会把目标累加器
// 对齐到当前局面（差的格子多一点而已，结果一样对）。

type psqCacheEntry struct {
	valid bool
	board [squareNB]byte
	acc   [L1]int16
	psqt  [PSQTBuckets]int32
}

type psqCache struct {
	// entries[视角][桶*2 + 镜像]
	entries [colorNB][]psqCacheEntry
}

// psqBucketSlotCount 是 FeatureBucket 可能返回的桶数（它把 attack bucket 的
// 4 档乘进了高位数）。从 kingBuckets 表算出来而不是写死常量 —— 表换了这里自动跟上。
//
// ⚠️ 必须惰性算，不能写成包级变量初始化：kingBuckets 是在 `init()` 里填的，
// 而 Go 的包级变量初始化**早于**所有 init() ⇒ 那时表还是全零，算出来是 1。
// （第一次就踩到了：桶 7 撞进长度 8 的切片，直接越界 panic。）
var (
	psqSlotsOnce sync.Once
	psqSlots     int
)

func psqBucketSlotCount() int {
	psqSlotsOnce.Do(func() {
		mx := 0
		for ksq := range kingBuckets {
			for oksq := range kingBuckets[ksq] {
				for mm := 0; mm < 2; mm++ {
					if b := int(kingBuckets[ksq][oksq][mm].bucket); b > mx {
						mx = b
					}
				}
			}
		}
		psqSlots = (mx + 1) * 4
	})
	return psqSlots
}

// ensurePSQCache 惰性分配：用不到（整局都是首帧）就不占内存。
// psqCacheOff 关掉 (桶, 镜像) 缓存，回到「每次整表重建」。
//
// 存在的意义是 A/B：同一份二进制里切换，不需要重新编译 —— 否则每次切换都会
// 触发重编译，而编译与基准争抢 CPU，会让同轮交替的量测失去意义（踩过）。
var psqCacheOff = os.Getenv("QIJING_PSQCACHE") == "off"

func (p *Position) ensurePSQCache() {
	if p.psqCache != nil {
		return
	}
	n := psqBucketSlotCount() * 2
	c := &psqCache{}
	for i := 0; i < colorNB; i++ {
		c.entries[i] = make([]psqCacheEntry, n)
	}
	p.psqCache = c
}

// refreshPSQ 把 a 的 PSQ 部分对齐到当前局面：命中缓存走差集，未命中整表重建。
//
// 它取代原来的 rebuildPSQ 调用 —— 代价从固定 31.6 行降到「与缓存相比变了的格子」
// 行数（搜索里通常是 2~4 行）。无论哪条路径，出口都会把当前值与棋盘写回缓存。
func (w *Weights) refreshPSQ(p *Position, a *Accumulator, c, bucket int, mirror bool) {
	p.ensurePSQCache()
	slot := bucket*2 + boolInt(mirror)
	if slot >= len(p.psqCache.entries[c]) {
		// 不该发生（长度是按 kingBuckets 表算的）。真发生了就退回全量重建：
		// 宁可慢一次，也不要越界把累加器写成垃圾 —— 那是不报错的静默错误。
		w.rebuildPSQ(p, a, c, bucket, mirror)
		return
	}
	e := &p.psqCache.entries[c][slot]

	if psqCacheOff {
		w.rebuildPSQ(p, a, c, bucket, mirror)
		return
	}
	if !e.valid {
		if diagOn {
			diagStats.PSQCacheMiss++
		}
		w.rebuildPSQ(p, a, c, bucket, mirror)
	} else {
		if diagOn {
			diagStats.PSQCacheHit++
		}
		copy(a.PsqAcc[c][:], e.acc[:])
		copy(a.PsqPsqt[c][:], e.psqt[:])
		// 每个变了的格子天然是「减旧 + 加新」一对 —— 与 applyPSQ 同一个形状，
		// 交给同一个两行合一的内核。第一遍只收集索引、不碰累加器，
		// 这样越界兜底可以安全地从头重来。
		var addIdx, subIdx [maxPSQPending]int32
		na, ns, feats := 0, 0, 0
		overflow := false
		for s := 0; s < squareNB && !overflow; s++ {
			old, cur := e.board[s], p.board[s]
			if old == cur {
				continue
			}
			if cur != 0 {
				if na >= maxPSQPending {
					overflow = true
					break
				}
				addIdx[na] = int32(PSQIndex(c, s, int(cur), bucket, mirror))
				na++
				feats++
			}
			if old != 0 {
				if ns >= maxPSQPending {
					overflow = true
					break
				}
				subIdx[ns] = int32(PSQIndex(c, s, int(old), bucket, mirror))
				ns++
				feats++
			}
		}
		if overflow {
			// 差得太多（正常不会：棋盘最多 32 颗子，两个方向各不会超过 32，
			// 而容量是 64）。真发生了就退回整表重建 —— 它会把 PsqAcc 重写成
			// biases + 全部棋子、并把 PsqPsqt 清零，所以不需要先手工复位。
			w.rebuildPSQ(p, a, c, bucket, mirror)
		} else {
			pair := pairCount(na, ns)
			for k := 0; k < pair; k++ {
				psqPairAddSub(a, w, c, int(addIdx[k]), int(subIdx[k]))
			}
			for k := pair; k < na; k++ {
				if diagOn {
					diagStats.SingleRows++
					diagStats.SingleAddRows++
				}
				psqAdd(a, w, c, int(addIdx[k]))
			}
			for k := pair; k < ns; k++ {
				if diagOn {
					diagStats.SingleRows++
					diagStats.SingleSubRows++
				}
				psqSub(a, w, c, int(subIdx[k]))
			}
			if diagOn {
				diagStats.RebuildPSQ++
				diagStats.RebuildPiece += int64(feats)
			}
		}
	}

	copy(e.acc[:], a.PsqAcc[c][:])
	copy(e.psqt[:], a.PsqPsqt[c][:])
	e.board = p.board
	e.valid = true
}

// sizeofPsqCacheEntry 供诊断打印（unsafe.Sizeof 的包内包装，避免测试里 import unsafe）。
func sizeofPsqCacheEntry() int { return int(unsafe.Sizeof(psqCacheEntry{})) }
