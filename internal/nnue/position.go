package nnue

import "fmt"

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
	p.marks = append(p.marks, posMark{
		pieces:   len(p.pendingPieces),
		threats:  len(p.pendingThreats),
		from:     from,
		to:       to,
		captured: p.board[to],
	})

	moved := p.board[from]
	captured := p.board[to]

	// 顺序与分支照抄皮的 do_move：
	//   无吃子 → move_piece（离开 from 与到达 to 都要重算射线）
	//   有吃子 → remove_piece(from) + swap_piece(to, moved)
	// 后者在 to 处是「同一格换子」，射线不受影响，所以用 computeRay=false 的路径。
	if captured != 0 {
		p.removePiece(from)
		p.swapPiece(to, moved)
	} else {
		p.movePiece(from, to)
	}

	p.pendingPieces = append(p.pendingPieces,
		dirtyPiece{sq: from, oldPc: moved, newPc: 0},
		dirtyPiece{sq: to, oldPc: captured, newPc: moved})
	p.side ^= 1
	p.version++

	if len(p.pendingThreats) > pendingMax {
		// 累积过长：丢弃脏信息并把各层回滚标记归零，同时标记累加器失效
		// （它相对当前局面已经不确定差了哪些特征）。
		p.clearPending()
		for i := range p.marks {
			p.marks[i].pieces, p.marks[i].threats = 0, 0
		}
		p.stale = true
	}
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
	if !keepRollback {
		p.suppressDirty++
	}

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

// updateThreats 记录「pc 在 s 上」这一事实的加入（put）或移除（!put）
// 所引起的全部攻击关系变化。
//
// computeRay 对应皮的模板参数：为 true 时比较「s 空/有子」两种状态下的射程差异，
// 为 false 时跳过。后者用于同一格换子（swapPiece）—— 换子前后 s 都有子，
// 射线不变，若仍按「空 vs 有子」比较就会凭空造出变化。
func (p *Position) updateThreats(put bool, pc byte, s int, computeRay bool) {
	if p.suppressDirty > 0 {
		return
	}
	occupied := p.occ
	pt := pieceType(int(pc))

	// 车与炮的射线、阻挡完全相同，一次算全，省掉一半的重复计算。
	if diagOn {
		diagStats.SlidingCalls++
	}
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
	for threatened = threatened.and(occupied); !threatened.isEmpty(); {
		t := threatened.popLSB()
		p.pendingThreats = append(p.pendingThreats,
			dirtyThreat{pc, p.board[t], s, t, put})
	}

	// ---- 指向 s 的威胁 ----
	// 非滑子靠反向查表；滑子（车与炮）也在这里一并覆盖：
	// 车不攻击 s 时不在 rAttacks 里，炮需要炮架所以它可能在 cAttacks 而不在 rAttacks，
	// 两个都与射线分支的取值互斥，因此不会重复。
	var incoming bitboard
	incoming = incoming.or(pseudoAttacks[paPawnToWhite][s].and(p.byColor[colorWhite].and(p.byType[ptPawn])))
	incoming = incoming.or(pseudoAttacks[paPawnToBlack][s].and(p.byColor[colorBlack].and(p.byType[ptPawn])))
	incoming = incoming.or(p.knightAttackers(s).and(p.byType[ptKnight]))
	incoming = incoming.or(lameLeaperAttack(ptBishop, s, occupied).and(p.byType[ptBishop]))
	incoming = incoming.or(pseudoAttacks[ptAdvisor][s].and(p.byType[ptAdvisor]))
	incoming = incoming.or(pseudoAttacks[ptKing][s].and(p.byType[ptKing]))
	incoming = incoming.or(rAttacks.and(p.byType[ptRook]))
	incoming = incoming.or(cAttacks.and(p.byType[ptCannon]))
	for !incoming.isEmpty() {
		src := incoming.popLSB()
		p.pendingThreats = append(p.pendingThreats,
			dirtyThreat{p.board[src], pc, src, s, put})
	}

	// ---- 射线与穿透（仅 computeRay）----
	//
	// 这里不去照抄「滑子与 s 之间无阻挡」那套分支：炮以更远的子为炮架时，
	// s 的占用变化同样会改它越过炮架后的第一个目标，而那种情形不在
	// 「与 s 无阻挡」的筛选范围内，会漏掉变化。
	//
	// 改成直接对比「s 为空」与「s 被占」两种情况下各候选子的目标集，
	// 差集就是真实变化 —— 既不会漏，也不会多算。
	if !computeRay {
		return
	}

	// occWith 含 s、occWithout 不含 —— 无论 put 与否，当前 occupied 都含 s
	// （put 时 placeRaw 已执行，!put 时 removeRaw 尚未执行）。
	// put 表示状态从「空」变为「有子」，所以 before/after 的对应关系随 put 反转。
	occWith := occupied
	occWithout := occupied
	occWithout.clear(s)
	occBefore, occAfter := occWithout, occWith
	if !put {
		occBefore, occAfter = occWith, occWithout
	}

	// 候选：与 s 同行列的滑子（车/炮），以及以 s 为腿或象眼的马/象。
	candidates := pseudoAttacks[ptRook][s].and(p.byType[ptRook].or(p.byType[ptCannon]))
	candidates = candidates.or(p.leaperThroughS(s))

	for !candidates.isEmpty() {
		psq := candidates.popLSB()
		cpt := pieceType(int(p.board[psq]))
		// 排除 s 本身：指向 s 的关系已由上面处理。
		if diagOn {
			diagStats.RayAttackCall += 2
		}
		before := attacksBB(cpt, psq, occBefore).and(occupied).andNot(bbOf(s))
		after := attacksBB(cpt, psq, occAfter).and(occupied).andNot(bbOf(s))
		for t := before.andNot(after); !t.isEmpty(); {
			tt := t.popLSB()
			p.pendingThreats = append(p.pendingThreats,
				dirtyThreat{p.board[psq], p.board[tt], psq, tt, false})
		}
		for t := after.andNot(before); !t.isEmpty(); {
			tt := t.popLSB()
			p.pendingThreats = append(p.pendingThreats,
				dirtyThreat{p.board[psq], p.board[tt], psq, tt, true})
		}
	}
}

// knightAttackers 返回「能马步攻击 s 且腿位为空」的马所在格。
// 对应 bitboard.h 的 attacks_bb<KNIGHT_TO>(s, occupied)。
func (p *Position) knightAttackers(s int) bitboard {
	var b bitboard
	for i := 0; i < 8; i++ {
		from := knightToFrom[s][i]
		if from < 0 {
			break
		}
		if !p.occ.test(knightToLeg[s][i]) {
			b.set(from)
		}
	}
	return b
}

// leaperThroughS 返回「以 s 为腿（马）或象眼（象）」的己方子所在格。
// 对应 update_piece_threats 里的
// (unconstrained_attacks_bb<KING>(s) & KNIGHT) | (unconstrained_attacks_bb<ADVISOR>(s) & BISHOP)。
func (p *Position) leaperThroughS(s int) bitboard {
	k := pseudoAttacks[paKingUnc][s].and(p.byType[ptKnight])
	b := pseudoAttacks[paAdvisorUnc][s].and(p.byType[ptBishop])
	return k.or(b)
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
