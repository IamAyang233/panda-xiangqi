package nnue

import (
	"fmt"
	"os"
)

// 本文件实现评估侧的增量局面 Position。
//
// 为什么不直接吃 internal/game.Position：game 包不该引入 NNUE 语义
// （攻击关系、中线镜像编码、按类型/颜色的子力计数），而这些恰好是
// 「特征枚举增量化」必须维护的信息。所以评估侧自己维护一套位棋盘，
// 由搜索层用同一步走法同步驱动（搜索层走一步就 Make、回溯就 Unmake）。
//
// 增量维护带来两处收益：
//  1. FeatureBucket / LayerStackBucket 从「每次扫 90 格」降为 O(1)；
//  2. 走子时顺带算出「哪些特征变了」，累加器只对这些做 add/sub，
//     不再需要每次评估都枚举全部特征（约 96 个）。
//
// 脏信息的算法与调用顺序完全照抄 Pikafish-2026-01-02 的 position.cpp
// （do_move 与 update_piece_threats）。顺序不能随意改：discovered 分支依赖
// 中间态的占用集（例如「移除被吃子」发生在「移动棋子」之前），换了顺序会
// 得到不同的差集。照抄已被验证的实现，风险最小。

// dirtyPiece 记录一次走子在某格上造成的棋子变化。
// oldPc/newPc 用皮的棋子编码，0 表示空。
type dirtyPiece struct {
	sq           int
	oldPc, newPc byte
}

// dirtyThreat 记录一条攻击关系 (attacker, from, to, attacked) 的增减。
type dirtyThreat struct {
	attacker, attacked byte
	from, to           int
	add                bool
}

// posMark 是每层的回滚标记：pending 列表的截断位置 + 恢复局面的必要信息。
type posMark struct {
	pieces, threats int
	from, to        int
	captured        byte

	// replayStart/replayEnd 是本层 Make 产生的威胁条目在 replayBuf 里的副本区间；
	// segStart/segEnd 是同一批条目在 replaySeg 里占的段边界记录区间。
	// captured != 0 的层不收集（吃子后换子那两段的条目与回滚段不构成取反关系，
	// 见 Unmake 的说明），此时两对字段相等、区间为空。
	replayStart, replayEnd int
	segStart, segEnd       int
}

// Position 是评估侧的增量局面。
type Position struct {
	board   [squareNB]byte
	occ     bitboard
	byType  [ptTypeNB]bitboard
	byColor [colorNB]bitboard
	kingSq  [colorNB]int
	cnt     [colorNB][ptTypeNB]int
	midEnc  [colorNB]uint64
	side    int

	// pendingPieces/pendingThreats 累积「自累加器上次更新以来」的全部变化。
	//
	// 必须是累积而不是只看最后一步：搜索中途可能跳过很多次评估
	// （置换表命中、剪枝），累加器停留在更早的局面。
	pendingPieces  []dirtyPiece
	pendingThreats []dirtyThreat
	marks          []posMark

	// stale 表示累加器相对当前局面已失效，下次更新必须走全量重建。
	stale bool

	// psqCache 见 psqcache.go：桶/镜像变化时按 (桶, 镜像) 复用 PSQ 累加器，
	// 避免把 31.6 行整表重算。随局面走（自洽缓存，无需失效）。
	psqCache *psqCache

	// replayBuf / replaySeg / replayOn：Unmake 复用 Make 的威胁条目。
	//
	// 依据是 Make 与 Unmake 的 updateThreats 调用**逐段配对**：
	//   Make  movePiece(from→to)：先在「from 有子」时记 from 段，再在「to 有子」时记 to 段
	//   Unmake movePiece(to→from)：先在「to 有子」时记 to 段，再在「from 有子」时记 from 段
	// 同一段的 occupied 完全相同、只有 put 相反，而条目内容只取决于 (occupied, s, pc)，
	// 所以**Unmake 要枚举出的条目，恰好是 Make 那批逐条取反**（顺序相反，但环绕加可交换）。
	// 于是不必重算：Make 时留一份副本，Unmake 时翻转 add 直接追加。
	//
	// 为什么值得：Unmake 占 updateThreats 调用的近一半，而枚举（slidingAttackBoth +
	// 射线/穿透循环）是这条链最贵的一段；实测把它换掉，固定节点基准快 10% 以上。
	//
	// replaySeg 是段结束位置（在 replayBuf 空间里）的栈：Unmake 按**倒序**取段。
	// 吃子走子不收集 —— 它的回滚是 swapPiece(to, captured) + putPiece(from, moved)，
	// 两段的 pc 与正向的 swapPiece(to, moved)/removePiece(from) 不同，不构成取反。
	replayBuf  []dirtyThreat
	replaySeg  []int
	replayOn   bool
	replayBase int
	replaySegs []int
	replayPick int

	// collectReplay 非零时 updateThreats 把本次枚举的条目留一份副本进 replayBuf。
	// 只在 Make 的 movePiece 段置位（吃子路径与回滚路径都不收集）。
	collectReplay bool

	// suppressDirty 非零时 updateThreats 直接返回（局面照常更新）。
	// 用于回滚路径：常规回退产生的脏信息会被 pending 截断丢弃，
	// 白算一遍；只有「累加器正好停在回滚前局面」时才需要留下它。
	suppressDirty int

	// version 是当前局面的版本号（每 Make +1、每 Unmake −1）；
	// pendingBase 是「累积脏信息所对应的起始版本」，也就是累加器缓存的那个局面的版本。
	//
	// 这两个数是为了处理「回退越过累加器」这一情形：若搜索评估完某节点后回退
	// 到更早的位置，累积脏信息会被截断成空，但累加器其实停在更晚的局面 ——
	// 此时必须标记失效，否则下次评估会误以为「没有变化」。
	version     int
	pendingBase int
}

// ResetFromGame 用 internal/game 的棋盘重建局面。
// squares 是 game 的 90 格棋盘（含 0x00 空格），side 为走子方（0 红 / 1 黑）。
func (p *Position) ResetFromGame(squares *[squareNB]byte, side int) {
	p.occ = bitboard{}
	p.byType = [ptTypeNB]bitboard{}
	p.byColor = [colorNB]bitboard{}
	p.kingSq = [colorNB]int{-1, -1}
	p.cnt = [colorNB][ptTypeNB]int{}
	p.midEnc = [colorNB]uint64{}
	p.pendingPieces = p.pendingPieces[:0]
	p.pendingThreats = p.pendingThreats[:0]
	p.marks = p.marks[:0]
	// 回放缓冲同样清空：不清也自洽（下标都按 len 记），但会白留一份上一个局面的内存。
	p.replayBuf = p.replayBuf[:0]
	p.replaySeg = p.replaySeg[:0]
	p.side = side
	p.stale = false
	p.version = 0
	p.pendingBase = 0
	p.suppressDirty = 0

	for s := 0; s < squareNB; s++ {
		pc := PieceFromGame(squares[s])
		if pc == 0 {
			// 必须显式清空：Position 会被连续多次搜索复用，遗忘的格子会把
			// 上一个局面的棋子留在棋盘上，而 cnt 已归零 —— 两者立刻失配，
			// 且会一路污染后续增量（计数变负 → LayerStackBucket 越界）。
			p.board[s] = 0
			continue
		}
		p.board[s] = byte(pc)
		p.occ.set(s)
		p.byType[pieceType(pc)].set(s)
		p.byColor[pieceColor(pc)].set(s)
		p.cnt[pieceColor(pc)][pieceType(pc)]++
		p.midEnc[pieceColor(pc)] += midMirrorEncoding[pc][s]
		if pieceType(pc) == ptKing {
			p.kingSq[pieceColor(pc)] = s
		}
	}
}

// SideToMove 返回走子方（0 红 / 1 黑）。
func (p *Position) SideToMove() int { return p.side }

// Version 返回版本号（每步走子 +1），供诊断与测试使用。
func (p *Position) Version() int { return p.version }

// PendingBase 返回累积脏信息对应的起始版本，供诊断与测试使用。
func (p *Position) PendingBase() int { return p.pendingBase }

// useThreatReplay 决定 Unmake 是否复用 Make 的威胁条目（下面 replayBuf 段的起点）。
//
// 由 QIJING_THRREPLAY=off 关闭：既给同一进程内的交替 A/B 提供两态，
// 也给运维留一把一刀 —— 怀疑回放路径在某类局面算错时，关掉它看现象是否消失。
var useThreatReplay = os.Getenv("QIJING_THRREPLAY") != "off"

// SetThreatReplay 切换威胁条目回放并返回原值，供测试与基准使用。
func SetThreatReplay(on bool) bool {
	old := useThreatReplay
	useThreatReplay = on
	return old
}

// pendingMax 是累积脏信息的硬上限。超过说明这条搜索路径长时间没有评估过
// （理论上不该发生，因为每个叶子都会评估），此时丢弃累积并让累加器整体重建。
const pendingMax = 4096

// clearPending 丢弃累积的脏信息，并把它对应的起始版本推进到当前局面
// （调用方必须已确认累加器与当前局面一致）。
func (p *Position) clearPending() {
	p.pendingPieces = p.pendingPieces[:0]
	p.pendingThreats = p.pendingThreats[:0]
	p.pendingBase = p.version
}

// Make 走一步 from→to 并累积本次走子的特征变化。
// 调用方必须保证这是合法着法，且与 internal/game.Position 的同一步走法配对。
func (p *Position) Make(from, to int) {
	thrStart := len(p.pendingThreats)
	segStart := len(p.replaySeg)
	replayStart := len(p.replayBuf)

	p.marks = append(p.marks, posMark{
		pieces:      len(p.pendingPieces),
		threats:     thrStart,
		replayStart: replayStart,
		segStart:    segStart,
		from:        from,
		to:          to,
		captured:    p.board[to],
	})

	moved := p.board[from]
	captured := p.board[to]

	// 顺序与分支照抄皮的 do_move：
	//   无吃子 → move_piece（离开 from 与到达 to 都要重算射线）
	//   有吃子 → remove_piece(from) + swap_piece(to, moved)
	// 后者在 to 处是「同一格换子」，射线不受影响，所以用 computeRay=false 的路径。
	//
	// 无吃子时顺带把本层产生的条目留一份副本（collectReplay），供 Unmake 翻转复用；
	// 吃子路径的两段与回滚段不构成取反关系，不收集。
	if captured != 0 {
		p.removePiece(from)
		p.swapPiece(to, moved)
	} else {
		p.collectReplay = true
		p.movePiece(from, to)
		p.collectReplay = false
	}

	p.pendingPieces = append(p.pendingPieces,
		dirtyPiece{sq: from, oldPc: moved, newPc: 0},
		dirtyPiece{sq: to, oldPc: captured, newPc: moved})
	p.side ^= 1
	p.version++

	// 记录本层的副本区间（超限丢弃时会走上面的分支，区间自动变成空的）。
	k := len(p.marks) - 1
	if len(p.pendingThreats) > pendingMax {
		// 累积过长：丢弃脏信息并把各层回滚标记归零，同时标记累加器失效
		// （它相对当前局面已经不确定差了哪些特征）。
		p.clearPending()
		for i := range p.marks {
			p.marks[i].pieces, p.marks[i].threats = 0, 0
		}
		p.stale = true
		// 副本对应的脏信息已经不存在了，一起丢弃 —— 本层退回枚举路径。
		p.replayBuf = p.replayBuf[:replayStart]
		p.replaySeg = p.replaySeg[:segStart]
	}
	p.marks[k].replayEnd = len(p.replayBuf)
	p.marks[k].segEnd = len(p.replaySeg)
}

// Unmake 回滚上一步。
//
// 回滚过程本身会产出脏信息，而它们描述的恰好是「从回滚前的局面退回来」的变化。
// 若累加器正停在那一步之后的局面（搜索里「评估完再回退」很常见），这段正是
// 它需要的，直接留用即可 —— 不必把累加器整体重建，那样增量的收益会被抵消。
func (p *Position) Unmake() {
	n := len(p.marks) - 1
	m := p.marks[n]
	p.marks = p.marks[:n]

	moved := p.board[m.to]

	// 回滚段只有「累加器停在回滚前局面」时才用得上（见下面的 pendingBase 分支）；
	// 常规回退会把它连同正向段一起截掉，那时没必要收集，省掉这一整轮计算。
	newVersion := p.version - 1
	keepRollback := p.pendingBase > newVersion
	if diagOn && keepRollback {
		diagStats.UtUnmakeKept++
	}

	// 需要回滚段、且本层留了副本（无吃子走子）时，走回放替代枚举。
	// 取段顺序是倒序：Unmake 的 movePiece(to→from) 先碰 to，而 Make 的
	// movePiece(from→to) 先碰 from —— 恰好把两段的次序反过来。
	segs := p.replaySeg[m.segStart:m.segEnd]
	replay := keepRollback && useThreatReplay && m.captured == 0 && len(segs) == 2
	if replay {
		p.replayOn = true
		p.replayBase = m.replayStart
		p.replaySegs = segs
		p.replayPick = len(segs) - 1
	} else if !keepRollback {
		p.suppressDirty++
	}

	// 本层的副本与段记录用完即弃（三个出口都要走到，用 defer 统一）。
	defer func() {
		p.replayBuf = p.replayBuf[:m.replayStart]
		p.replaySeg = p.replaySeg[:m.segStart]
	}()

	// 回滚路径必须与 Make 的分支结构镜像，否则两边的脏信息不会精确抵消：
	//   无吃子：Make 是 move_piece(from→to)，回滚是 move_piece(to→from)
	//   有吃子：Make 是 remove_piece(from) + swap_piece(to, moved)，
	//           回滚是 swap_piece(to, captured) + put_piece(from, moved)
	// 尤其注意 swap 走的是 computeRay=false 路径，回滚若改用 move_piece（true），
	// 两边产出的关系集合不同，残留就会累积成错误。
	if m.captured != 0 {
		p.swapPiece(m.to, m.captured)
		p.putPiece(m.from, moved)
	} else {
		p.movePiece(m.to, m.from)
	}
	p.replayOn = false
	p.replaySegs = nil
	if !keepRollback {
		p.suppressDirty--
	}
	p.side ^= 1
	p.version = newVersion

	if p.pendingBase > p.version {
		// 累加器停在更晚的局面（本次回退越过了它）。
		//
		// 已有条目都是相对那个局面算的，方向仍然正确，所以不能清空 ——
		// 连续回退时每一步的回滚段要一路累积起来才是「累加器局面 → 当前」的完整变化。
		// 威胁段由回滚过程自然追加（见 movePiece），PSQ 段要手工补：
		// 走子与回滚都不追加 dirtyPiece，只有 Make 会。
		p.pendingPieces = append(p.pendingPieces,
			dirtyPiece{sq: m.to, oldPc: moved, newPc: m.captured},
			dirtyPiece{sq: m.from, oldPc: 0, newPc: moved})
		return
	}

	// 常规回退：正向变化与刚产生的回滚段成对，一起丢掉即等价于撤销这一步。
	//
	// 长度必须显式检查：Go 的 s[:n] 只看容量而不看长度，若标记长度大于当前长度
	// （说明中途有 Apply 清空过累积），切片会「恢复」底层数组里的陈旧条目，
	// 再应用一次就错了。
	if m.pieces <= len(p.pendingPieces) && m.threats <= len(p.pendingThreats) {
		p.pendingPieces = p.pendingPieces[:m.pieces]
		p.pendingThreats = p.pendingThreats[:m.threats]
		return
	}
	p.clearPending()
	p.stale = true
}

// MakeNull 走一步空着：只切走子方。
//
// 不需要产生脏信息 —— 镜像与特征桶只看棋盘，走子方只是 LayerStack 的
// 运行时参数，不影响累加器内容。
func (p *Position) MakeNull() { p.side ^= 1 }

// UnmakeNull 撤销空着。
func (p *Position) UnmakeNull() { p.side ^= 1 }

// ---- 局面查询（全部 O(1)）----

// FeatureBucket 对应 HalfKAv2_hm::make_feature_bucket，返回 (桶号, 是否镜像)。
// 与 Board 版本语义相同，但用增量维护的将位、中线编码与子力计数，不再扫全盘。
func (p *Position) FeatureBucket(perspective int) (bucket int, mirror bool) {
	ksq, oksq := p.kingSq[perspective], p.kingSq[perspective^1]
	if ksq < 0 || oksq < 0 {
		return 0, false
	}
	me, opp := p.midEnc[perspective], p.midEnc[perspective^1]
	need := me&(1<<63) != 0 && opp&(1<<63) != 0 &&
		(me < balanceEncoding || (me == balanceEncoding && opp < balanceEncoding))
	slot := kingBuckets[ksq][oksq][boolInt(need)]

	ab := 0
	if p.cnt[perspective][ptRook] > 0 {
		ab += 2
	}
	if p.cnt[perspective][ptKnight]+p.cnt[perspective][ptCannon] > 0 {
		ab++
	}
	return int(slot.bucket)*4 + ab, slot.mirror
}

// LayerStackBucket 对应 HalfKAv2_hm::make_layer_stack_bucket，取值 0..15。
func (p *Position) LayerStackBucket() int {
	us, them := p.side, p.side^1
	return layerStackBucketValue(
		p.cnt[us][ptRook], p.cnt[them][ptRook],
		p.cnt[us][ptKnight]+p.cnt[us][ptCannon],
		p.cnt[them][ptKnight]+p.cnt[them][ptCannon])
}

// CheckCounts 比对增量维护的子力计数与棋盘实际内容，一致时返回空串。
//
// 增量维护一旦算错不会立刻报错，只会让评估值静默偏移；子力计数是最先
// 露出破绽的地方 —— 它负数化之后会让 LayerStackBucket 越界。测试与
// 崩溃现场诊断都用它。
func (p *Position) CheckCounts() string {
	var real [colorNB][ptTypeNB]int
	for s := 0; s < squareNB; s++ {
		if pc := p.board[s]; pc != 0 {
			real[pieceColor(int(pc))][pieceType(int(pc))]++
		}
	}
	for c := 0; c < colorNB; c++ {
		for pt := 0; pt < ptTypeNB; pt++ {
			if real[c][pt] != p.cnt[c][pt] {
				us, them := p.side, p.side^1
				return fmt.Sprintf(
					"一方 %d 的类型 %d 计数 %d 实际 %d；车(%d/%d) 马炮(%d/%d) 桶号 %d",
					c, pt, p.cnt[c][pt], real[c][pt],
					p.cnt[us][ptRook], p.cnt[them][ptRook],
					p.cnt[us][ptKnight]+p.cnt[us][ptCannon],
					p.cnt[them][ptKnight]+p.cnt[them][ptCannon],
					p.LayerStackBucket())
			}
		}
	}
	return ""
}

// NonPawnCount 返回某方车马炮的总数（空着剪枝的 zugzwang 保护要用）。
func (p *Position) NonPawnCount(c int) int {
	return p.cnt[c][ptRook] + p.cnt[c][ptCannon] + p.cnt[c][ptKnight]
}

// ---- 局面变更 ----

// placeRaw 只更新局面状态，不产生脏信息（初始化与回滚用）。
func (p *Position) placeRaw(s int, pc byte) {
	c, pt := pieceColor(int(pc)), pieceType(int(pc))
	p.board[s] = pc
	p.occ.set(s)
	p.byType[pt].set(s)
	p.byColor[c].set(s)
	p.cnt[c][pt]++
	p.midEnc[c] += midMirrorEncoding[pc][s]
	if pt == ptKing {
		p.kingSq[c] = s
	}
}

// removeRaw 只清除局面状态，不产生脏信息。
func (p *Position) removeRaw(s int) {
	pc := p.board[s]
	c, pt := pieceColor(int(pc)), pieceType(int(pc))
	p.board[s] = 0
	p.occ.clear(s)
	p.byType[pt].clear(s)
	p.byColor[c].clear(s)
	p.cnt[c][pt]--
	p.midEnc[c] -= midMirrorEncoding[pc][s]
}

// putPiece 放置一个子。照抄皮的 put_piece：先落子（占用集随即包含 s），
// 再记录由此产生的威胁变化。
func (p *Position) putPiece(s int, pc byte) {
	p.placeRaw(s, pc)
	p.updateThreats(true, pc, s, true)
}

// removePiece 移除 s 上的子。照抄皮的 remove_piece：先记录威胁变化
// （此时占用集仍包含 s），再从局面中清掉。
func (p *Position) removePiece(s int) {
	pc := p.board[s]
	p.updateThreats(false, pc, s, true)
	p.removeRaw(s)
}

// movePiece 把子从 from 移到 to。照抄皮的 move_piece：
// 离开 from 的威胁变化在移动前记录，到达 to 的威胁变化在移动后记录。
func (p *Position) movePiece(from, to int) {
	pc := p.board[from]
	p.updateThreats(false, pc, from, true)

	c, pt := pieceColor(int(pc)), pieceType(int(pc))
	fromTo := bbOf(from, to)
	p.occ = p.occ.xor(fromTo)
	p.byType[pt] = p.byType[pt].xor(fromTo)
	p.byColor[c] = p.byColor[c].xor(fromTo)
	p.board[from] = 0
	p.board[to] = pc
	if pt == ptKing {
		p.kingSq[c] = to
	}
	p.midEnc[c] -= midMirrorEncoding[pc][from]
	p.midEnc[c] += midMirrorEncoding[pc][to]

	p.updateThreats(true, pc, to, true)
}

// swapPiece 把 s 上的子换成 pc —— 吃子走子的第二步。
//
// 照抄皮的 swap_piece：同一格上换子不会改变任何射线，所以走 computeRay=false
// 路径（不跑射线/穿透分支，改由 incoming 覆盖滑子「指向 s」的威胁）。
// 调用前 s 上的原棋子应已从局面中移除。
func (p *Position) swapPiece(s int, pc byte) {
	old := p.board[s]
	p.removeRaw(s)
	p.updateThreats(false, old, s, false)
	p.placeRaw(s, pc)
	p.updateThreats(true, pc, s, false)
}

// ---- 威胁增量（对应 Position::update_piece_threats<PutPiece, true>）----

// lsbOf 返回 lo/hi 两半组成的最低置位格，空则返回 -1。
//
// 语义与 bitboard.lsb 相同，但输入是两个 uint64：热路径上把它们拆开后
// 可以一直留在 GPR 里，不必为了查一下最低位就构造一个 16 字节的 bitboard
// （本函数汇编里 MOVUPS 曾占 17.8%，见 updateThreats 的注释）。
func lsbOf(lo, hi uint64) int {
	if lo != 0 {
		return trailingZeros64(lo)
	}
	if hi != 0 {
		return 64 + trailingZeros64(hi)
	}
	return -1
}

// drainBit 弹出 lo/hi 里的最低位置，返回其索引与剩余两字（已空时返回 -1）。
//
// 用返回值而不是传指针：取地址会让这两个变量被迫落回内存，把 lo/hi 的好处全丢掉。
func drainBit(lo, hi uint64) (int, uint64, uint64) {
	if lo != 0 {
		return trailingZeros64(lo), lo & (lo - 1), hi
	}
	if hi != 0 {
		return 64 + trailingZeros64(hi), 0, hi & (hi - 1)
	}
	return -1, 0, 0
}

// testLoHi 测试 sq 是否在 lo/hi 位集里 —— 等价于 bitboard.test，
// 但不构造 16 字节的值（热路径上用它代替 b.test(sq)）。
func testLoHi(lo, hi uint64, sq int) bool {
	if sq >= 64 {
		return hi&(1<<uint(sq-64)) != 0
	}
	return lo&(1<<uint(sq)) != 0
}

// updateThreats 记录「pc 在 s 上」这一事实的加入（put）或移除（!put）
// 所引起的全部攻击关系变化。
//
// computeRay 对应皮的模板参数：为 true 时比较「s 空/有子」两种状态下的射程差异，
// 为 false 时跳过。后者用于同一格换子（swapPiece）—— 换子前后 s 都有子，
// 射线不变，若仍按「空 vs 有子」比较就会凭空造出变化。
func (p *Position) updateThreats(put bool, pc byte, s int, computeRay bool) {
	if p.suppressDirty > 0 {
		if diagOn {
			diagStats.UtSuppressed++
		}
		return
	}
	if p.replayOn {
		// 直接搬 Make 那一段的条目、翻转方向即可 —— 两条路径产出的条目集合完全相同，
		// 只是顺序不同，而环绕加满足交换结合律，所以累加器逐位相同（树不变）。
		if p.replayPick >= 0 {
			i := p.replayPick
			start := p.replayBase
			if i > 0 {
				start = p.replaySegs[i-1]
			}
			// 整体追加再原地翻转：逐个 append 是这条路径的主要成本
			// （append 每次都要做容量检查），批量追加走的是 memmove。
			old := len(p.pendingThreats)
			p.pendingThreats = append(p.pendingThreats, p.replayBuf[start:p.replaySegs[i]]...)
			for k := old; k < len(p.pendingThreats); k++ {
				p.pendingThreats[k].add = !p.pendingThreats[k].add
			}
			p.replayPick--
		}
		if diagOn {
			diagStats.UtReplayed++
		}
		return
	}
	if diagOn {
		diagStats.UtEnumerated++
	}
	before := len(p.pendingThreats)
	occupied := p.occ
	pt := pieceType(int(pc))

	// 车与炮的射线、阻挡完全相同，一次算全，省掉一半的重复计算。
	rAttacks, cAttacks := slidingAttackBoth(s, occupied)

	// ---- 该子发出的威胁 ----
	var threatened bitboard
	switch pt {
	case ptPawn:
		threatened = pseudoAttacks[pawnSlot(pieceColor(int(pc)))][s]
	case ptRook:
		threatened = rAttacks
	case ptCannon:
		threatened = cAttacks
	default:
		threatened = attacksBB(pt, s, occupied)
	}
	threatened = threatened.and(occupied)
	if diagOn {
		diagStats.ThreatOut += int64(threatened.count())
	}
	for tLo, tHi := threatened[0], threatened[1]; tLo|tHi != 0; {
		var t int
		t, tLo, tHi = drainBit(tLo, tHi)
		p.pendingThreats = append(p.pendingThreats,
			dirtyThreat{pc, p.board[t], s, t, put})
	}

	// ---- 指向 s 的威胁 ----
	// 非滑子靠反向查表；滑子（车与炮）也在这里一并覆盖：
	// 车不攻击 s 时不在 rAttacks 里，炮需要炮架所以它可能在 cAttacks 而不在 rAttacks，
	// 两个都与射线分支的取值互斥，因此不会重复。
	//
	// 攻击 s 的车/炮这两批同时被下面的 computeRay 段用到（那里按候选遍历），
	// 所以在这里算一次共用。
	rookAttackers := rAttacks.and(p.byType[ptRook])
	cannonAttackers := cAttacks.and(p.byType[ptCannon])

	// 十六进制不说谎：本函数汇编里 MOVUPS 占 17.8%、而真正的位运算只占 5%。
	// 原因是 8 个 bitboard（各 16 字节）同时存活，amd64 的 16 个 GPR 装不下，
	// 中间值被溢出到栈，每次位运算都要多付一次 load/store。
	// 把 incoming 段改写成 lo/hi 两个 uint64 —— 各占 1 个寄存器，可以留在 GPR 里。
	wPawns := p.byColor[colorWhite].and(p.byType[ptPawn])
	bPawns := p.byColor[colorBlack].and(p.byType[ptPawn])
	wAtt, bAtt := pseudoAttacks[paPawnToWhite][s], pseudoAttacks[paPawnToBlack][s]
	inALo, inAHi := wAtt[0]&wPawns[0], wAtt[1]&wPawns[1]
	inALo, inAHi = inALo|bAtt[0]&bPawns[0], inAHi|bAtt[1]&bPawns[1]

	knight := p.knightAttackers(s)
	bishop := lameLeaperAttack(ptBishop, s, occupied)
	inBLo, inBHi := knight[0]&p.byType[ptKnight][0], knight[1]&p.byType[ptKnight][1]
	inBLo, inBHi = inBLo|bishop[0]&p.byType[ptBishop][0], inBHi|bishop[1]&p.byType[ptBishop][1]

	// 士与将只在九宫内活动，两张表在九宫外恒为空 —— 与 buildPseudoAttacks 里
	// 填充它们时的条件（palaceBB.test(s)）严格一致，所以跳过是等价的。
	if palaceBB.test(s) {
		advisor, king := pseudoAttacks[ptAdvisor][s], pseudoAttacks[ptKing][s]
		inALo, inAHi = inALo|advisor[0]&p.byType[ptAdvisor][0], inAHi|advisor[1]&p.byType[ptAdvisor][1]
		inBLo, inBHi = inBLo|king[0]&p.byType[ptKing][0], inBHi|king[1]&p.byType[ptKing][1]
	}

	inLo, inHi := inALo|inBLo, inAHi|inBHi
	inLo, inHi = inLo|rookAttackers[0], inHi|rookAttackers[1]
	inLo, inHi = inLo|cannonAttackers[0], inHi|cannonAttackers[1]
	incoming := bitboard{inLo, inHi}
	if diagOn {
		diagStats.ThreatIn += int64(incoming.count())
	}
	for inLo|inHi != 0 {
		var src int
		src, inLo, inHi = drainBit(inLo, inHi)
		p.pendingThreats = append(p.pendingThreats,
			dirtyThreat{p.board[src], pc, src, s, put})
	}

	// ---- 射线与穿透（仅 computeRay）----
	//
	// 用预计算表 rayPassBB / leaperPassBB（对应皮卡 attacks.h 的同名表）直接取
	// 「从候选子看，越过 s 之后的那一格」，不必为每个候选重算两侧攻击集。
	//
	// 旧实现是「算出 s 为空 / s 被占两种情况下各候选的攻击集，取对称差」——
	// 语义等价但贵得多：每个炮候选要跑 2 次含阻挡搜索的滑动攻击、每个马象候选
	// 要跑 2 次全量攻击计算。实测（中局安静局面）候选迭代 **10.75 次/节点**
	// （车 3.15｜炮 6.39｜马象 1.22），把这一整段短路能让固定节点基准快 7~9%。
	// 另有两点顺带收益：① 车只遍历「确实攻击 s」的那些（有阻挡在中间的车原本
	// 也要算一遍、结果必然抵消）；② 炮拆成「攻击 s / 与 s 无阻挡」两批各查表。
	//
	// ⚠️ 与皮的一处**必要差异**：皮在 ComputeRay 分支里还会对每个攻击 s 的车/炮
	// 补一条「该子威胁 s 上的 pc」。我们**不补** —— 那批关系已由上面的 incoming
	// 段（`rAttacks & 车`、`cAttacks & 炮`）覆盖，补了会重复计数。旧实现同样
	// 显式排除 s（`.andNot(bbOf(s))`），语义一致。
	if !computeRay {
		return
	}
	if diagOn {
		diagStats.RayCalls++
	}

	// 三段共用的三个操作数拆成 lo/hi —— 与 incoming 段同一个理由（见那里）：
	// 拆开后每半占 1 个 GPR，循环体里不必反复构造/搬运 16 字节值。
	rLo, rHi := rAttacks[0], rAttacks[1]
	cLo, cHi := cAttacks[0], cAttacks[1]
	occLo, occHi := occupied[0], occupied[1]

	// ① 攻击 s 的车：s 从空变占（或反之）会让它的射线停在 s 或继续越过 s，
	//    于是「越过 s 之后那一格」上的关系随之反转。
	if diagOn {
		diagStats.CandRook += int64(rookAttackers.count())
	}
	for raLo, raHi := rookAttackers[0], rookAttackers[1]; raLo|raHi != 0; {
		var psq int
		psq, raLo, raHi = drainBit(raLo, raHi)
		pass := rayPassBB[psq][s]
		if tq := lsbOf(pass[0]&rLo&occLo, pass[1]&rHi&occHi); tq >= 0 {
			p.pendingThreats = append(p.pendingThreats,
				dirtyThreat{p.board[psq], p.board[tq], psq, tq, !put})
		}
	}

	// ② 攻击 s 的炮（恰好一个炮架在中间）：同上。
	if diagOn {
		diagStats.CandCannon += int64(cannonAttackers.count())
	}
	for caLo, caHi := cannonAttackers[0], cannonAttackers[1]; caLo|caHi != 0; {
		var psq int
		psq, caLo, caHi = drainBit(caLo, caHi)
		pass := rayPassBB[psq][s]
		if tq := lsbOf(pass[0]&rLo&occLo, pass[1]&rHi&occHi); tq >= 0 {
			p.pendingThreats = append(p.pendingThreats,
				dirtyThreat{p.board[psq], p.board[tq], psq, tq, !put})
		}
	}

	// ③ 与 s 对齐、但中间无子的炮：s 从空变占时它会改以 s 为炮架（反之失去），
	//    同时「越过 s 的第一格」的角色在炮架与目标之间对调 —— 两个子情形方向相反，
	//    所以各查一次表、各记一条。
	cannonOnRookRay := rAttacks.and(p.byType[ptCannon])
	if diagOn {
		diagStats.CandCannon += int64(cannonOnRookRay.count())
	}
	for corLo, corHi := cannonOnRookRay[0], cannonOnRookRay[1]; corLo|corHi != 0; {
		var psq int
		psq, corLo, corHi = drainBit(corLo, corHi)
		pass := rayPassBB[psq][s]
		if tq := lsbOf(pass[0]&rLo&occLo, pass[1]&rHi&occHi); tq >= 0 {
			p.pendingThreats = append(p.pendingThreats,
				dirtyThreat{p.board[psq], p.board[tq], psq, tq, put})
		}
		if tq := lsbOf(pass[0]&cLo&occLo, pass[1]&cHi&occHi); tq >= 0 {
			p.pendingThreats = append(p.pendingThreats,
				dirtyThreat{p.board[psq], p.board[tq], psq, tq, !put})
		}
	}

	// ④ 以 s 为腿（马）或象眼（象）的子：s 被占即被堵住，越过 s 的落点关系反转。
	//    马可能有两个落点（表里是集合），所以这里用内层循环而不是单个 popLSB。
	leapers := p.leaperThroughS(s)
	if diagOn {
		diagStats.CandLeaper += int64(leapers.count())
	}
	for lpLo, lpHi := leapers[0], leapers[1]; lpLo|lpHi != 0; {
		var psq int
		psq, lpLo, lpHi = drainBit(lpLo, lpHi)
		pass := leaperPassBB[psq][s]
		for tLo, tHi := pass[0]&occLo, pass[1]&occHi; tLo|tHi != 0; {
			var tq int
			tq, tLo, tHi = drainBit(tLo, tHi)
			p.pendingThreats = append(p.pendingThreats,
				dirtyThreat{p.board[psq], p.board[tq], psq, tq, !put})
		}
	}

	// 留一份副本给 Unmake 翻转复用。只统计本次调用新增的那一段，
	// 并按段记录结束位置（Unmake 按段倒序取用）。
	//
	// computeRay=false 的提前返回不收集 —— 那条路径只在吃子走子的 swapPiece 里出现，
	// 而 collectReplay 只在无吃子的 movePiece 期间置位，两者不会同时成立。
	if p.collectReplay {
		p.replayBuf = append(p.replayBuf, p.pendingThreats[before:]...)
		p.replaySeg = append(p.replaySeg, len(p.replayBuf))
	}
}

// knightAttackers 返回「能马步攻击 s 且腿位为空」的马所在格。
// 对应 bitboard.h 的 attacks_bb<KNIGHT_TO>(s, occupied)。
func (p *Position) knightAttackers(s int) bitboard {
	occLo, occHi := p.occ[0], p.occ[1]
	var lo, hi uint64
	for i := 0; i < 8; i++ {
		from := knightToFrom[s][i]
		if from < 0 {
			break
		}
		if testLoHi(occLo, occHi, knightToLeg[s][i]) {
			continue
		}
		if from >= 64 {
			hi |= 1 << uint(from-64)
		} else {
			lo |= 1 << uint(from)
		}
	}
	return bitboard{lo, hi}
}

// leaperThroughS 返回「以 s 为腿（马）或象眼（象）」的己方子所在格。
// 对应 update_piece_threats 里的
// (unconstrained_attacks_bb<KING>(s) & KNIGHT) | (unconstrained_attacks_bb<ADVISOR>(s) & BISHOP)。
func (p *Position) leaperThroughS(s int) bitboard {
	k := pseudoAttacks[paKingUnc][s]
	b := pseudoAttacks[paAdvisorUnc][s]
	kl, kh := p.byType[ptKnight][0], p.byType[ptKnight][1]
	bl, bh := p.byType[ptBishop][0], p.byType[ptBishop][1]
	return bitboard{k[0]&kl | b[0]&bl, k[1]&kh | b[1]&bh}
}

// ---- 攻击辅助表（bitboard.h 的 RayPassBB / LeaperPassBB）----

var (
	// rayPassBB[s1][s2]：s1 与 s2 正交对齐时，从 s1 越过 s2 之后的格子集合。
	// 构建方式与皮一致：占位集只有 s2 时炮从 s1 的攻击集。
	rayPassBB [squareNB][squareNB]bitboard

	// leaperPassBB[s1][s2]：马/象在 s1、s2 是其腿/象眼时，
	// 越过 s2 之后能打到的目标格。
	leaperPassBB [squareNB][squareNB]bitboard

	// knightToFrom[s][i] 是能马步攻击 s 的第 i 个马位（-1 表示表项用尽）；
	// knightToLeg[s][i] 是对应的腿位。
	knightToFrom [squareNB][8]int
	knightToLeg  [squareNB][8]int
)

// ⚠️ 量「候选循环值不值得换表」时**语料会决定结论**（踩过）：
//
//	残局题库（子力少）     候选迭代  0.60 次/节点  ⇒ 换表值不到 1%，看着不值得做
//	中局安静局面（子力多） 候选迭代 **10.75 次/节点**（车 3.15｜炮 6.39｜马象 1.22）
//
// 差的 18 倍来自「同行列的滑子数」随在场子数增长 —— 而实战与搜索基准都是中局。
// **别拿残局语料判断中局机制的量级。**
func buildAttackPassTables() {
	for s1 := 0; s1 < squareNB; s1++ {
		for s2 := 0; s2 < squareNB; s2++ {
			if pseudoAttacks[ptRook][s1].test(s2) {
				rayPassBB[s1][s2] = slidingAttack(ptCannon, s1, bbOf(s2))
			}
			var l bitboard
			if pseudoAttacks[paKingUnc][s1].test(s2) {
				l = l.or(pseudoAttacks[ptKnight][s1].and(pseudoAttacks[paAdvisorUnc][s2]))
			}
			if pseudoAttacks[paAdvisorUnc][s1].test(s2) {
				l = l.or(pseudoAttacks[ptBishop][s1].and(pseudoAttacks[paAdvisorUnc][s2]))
			}
			leaperPassBB[s1][s2] = l
		}
	}

	for s := 0; s < squareNB; s++ {
		for i := range knightToFrom[s] {
			knightToFrom[s][i] = -1
			knightToLeg[s][i] = -1
		}
	}
	for from := 0; from < squareNB; from++ {
		for targets := pseudoAttacks[ptKnight][from]; !targets.isEmpty(); {
			to := targets.popLSB()
			leg := lameLeaperPath(ptKnight, to-from, from)
			for i := 0; i < 8; i++ {
				if knightToFrom[to][i] < 0 {
					knightToFrom[to][i] = from
					knightToLeg[to][i] = leg
					break
				}
			}
		}
	}
}
