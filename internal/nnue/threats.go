package nnue

import "math/bits"

// 本文件对应 Pikafish-2026-01-02 的 src/bitboard.h 攻击部分与
// src/nnue/features/full_threats.cpp。
//
// 皮用 magic bitboard 查表，这里按其 C++ 参考实现直接逐格计算 —— 这些表只在
// 包初始化时构建一次，可读性优先。M11 性能阶段若需要再换查表法。

func bitsOnesCount64(x uint64) int    { return bits.OnesCount64(x) }
func trailingZeros64(x uint64) int    { return bits.TrailingZeros64(x) }
func bitsLeadingZeros64(x uint64) int { return bits.LeadingZeros64(x) }

// safeDestination 对应 Bitboards::safe_destination：单步到达的格子。
// chebyshev <= 2 同时容许正交步与斜步。
func safeDestination(s, step int) bitboard {
	to := s + step
	if okSquare(to) && chebyshev(s, to) <= 2 {
		return bbOf(to)
	}
	return bitboard{}
}

// shiftBB 把位棋盘沿方向平移一格，越界或跨列的格子丢弃。
func shiftBB(b bitboard, d int) bitboard {
	var out bitboard
	for w := 0; w < 2; w++ {
		for m := b[w]; m != 0; {
			s := w*64 + bits.TrailingZeros64(m)
			m &= m - 1
			t := s + d
			if okSquare(t) && chebyshev(s, t) == 1 {
				out.set(t)
			}
		}
	}
	return out
}

// pawnAttacks 对应 pawn_attacks_bb<C>：兵的走法（过河后可横走）。
func pawnAttacks(c, s int) bitboard {
	b := bbOf(s)
	var attack bitboard
	if c == colorWhite {
		attack = shiftBB(b, dirNorth)
	} else {
		attack = shiftBB(b, dirSouth)
	}
	if (c == colorWhite && rankOf(s) > 4) || (c == colorBlack && rankOf(s) < 5) {
		attack = attack.or(shiftBB(b, dirWest)).or(shiftBB(b, dirEast))
	}
	return attack
}

// pawnAttacksTo 对应 pawn_attacks_to_bb<C>：能走到 s 的兵所在格。
func pawnAttacksTo(c, s int) bitboard {
	b := bbOf(s)
	var attack bitboard
	if c == colorWhite {
		attack = shiftBB(b, dirSouth)
	} else {
		attack = shiftBB(b, dirNorth)
	}
	if (c == colorWhite && rankOf(s) > 4) || (c == colorBlack && rankOf(s) < 5) {
		attack = attack.or(shiftBB(b, dirWest)).or(shiftBB(b, dirEast))
	}
	return attack
}

// slidingAttack 对应 Bitboards::sliding_attack<pt>（pt 为车或炮）。
//
// 语义与逐格版一致：车打到阻挡子为止（含阻挡子本身）；
// 炮在越过炮架后把余下的格子全部纳入（是否真有子由调用方按占用集过滤）。
//
// 实现委托给 slidingAttackBoth，只取需要的一半 —— 保持单一逻辑来源，
// 避免两个版本各自演化后分叉。初始化期建表时用它，
// 每次走子的热路径则直接用 slidingAttackBoth 一次拿全。
func slidingAttack(pt, sq int, occupied bitboard) bitboard {
	rook, cannon := slidingAttackBoth(sq, occupied)
	if pt == ptRook {
		return rook
	}
	return cannon
}

// slidingAttackBoth 一次算出同一格上车与炮的攻击集。
//
// 热路径（updateThreats）总是两个都要，而两者的射线与阻挡完全相同 ——
// 分开调用会把「取射线、与占用集求交、找第一个阻挡」重复做一遍。
// 合并后这些只算一次，四个方向的循环也只走一遍。
//
// **全程没有任何与棋盘数据相关的分支**：原来每个方向有两次 isEmpty 判断
// （射线是否全空、炮架之后是否还有子），实测那两处占了本函数自身耗时的 38%
// —— 分支结果取决于占用集，预测器学不到规律。现在改成把「第一个阻挡的格号」
// 用位运算直接编码成查表下标（见 incIndex / decIndex），空射线也有对应的
// 表项（空集），于是两个分支一起消失。
//
// 实测（本机）：
//   - 微基准 Varying（每次换 (sq, occ)，即真实搜索的形态）：109.8 → 71.3 ns/次
//   - 端到端固定 10 万节点搜索（initial 局面，5 次取中位数）：1651 → 1479 ms
//     即整机吞吐 +11.6%；节点数逐位未变（1040484 / 1100668 / 深度 14.70 三个
//     基准值与改动前完全一致），确认是纯速度改动。
func slidingAttackBoth(sq int, occupied bitboard) (rook, cannon bitboard) {
	// 计数放在函数内而不是调用点：computeRay 段经 attacksBB 转发进来的调用同样
	// 要算，只在 updateThreats 里统计会把总量低估四分之三。
	if diagOn {
		diagStats.SlidingCalls++
	}
	occLo, occHi := occupied[0], occupied[1]
	for di := 0; di < 4; di++ {
		ray := rayBB[sq][di]
		inc := di == 0 || di == 2 // 北/东格号递增，南/西递减

		var fb bitboard
		if inc {
			fb = beyondInc[di][incIndex(ray[0]&occLo, ray[1]&occHi)]
		} else {
			fb = beyondDec[di][decIndex(ray[0]&occLo, ray[1]&occHi)]
		}
		// 射线表本身不含 sq，所以 ray 去掉 fb（阻挡之后的射线）剩下的正好是
		// 「sq 到 fb（含 fb）」，不必再拼 bbOf(fb) 后取补。
		rook = rook.or(ray.andNot(fb))

		// 炮：炮架之前一格都不算（hurdle 未越过），越过 fb 之后一直算到下一个子（含）。
		// fb 为空（本方向没有子）时下面整段自然产出空集。
		var fb2 bitboard
		if inc {
			fb2 = beyondInc[di][incIndex(fb[0]&occLo, fb[1]&occHi)]
		} else {
			fb2 = beyondDec[di][decIndex(fb[0]&occLo, fb[1]&occHi)]
		}
		cannon = cannon.or(fb.andNot(fb2))
	}
	return rook, cannon
}

// beyondOf 返回「沿方向 d 越过 set 中最近的那个子之后」的格子集合。
//
// 入参 set 必须是某个格子的射线（即 rayBB[x][d]）或它的子集，两者都满足
// 「沿 d 递增/递减排列的连续区间」；beyondInc/beyondDec 的下标编码正是据此
// 把「找最近置位」变成一次查表。set 与 occ 无交集时返回空集
// （递增方向落到下标 128、递减方向落到下标 0，两张表的这两档都是空集）。
func beyondOf(set, occ bitboard, d int) bitboard {
	lo, hi := set[0]&occ[0], set[1]&occ[1]
	if d == 0 || d == 2 {
		return beyondInc[d][incIndex(lo, hi)]
	}
	return beyondDec[d][decIndex(lo, hi)]
}

// slidingAttackDir 只算方向 d 上的滑动攻击 —— 车集与炮集。
//
// 与 slidingAttackBoth 的关系：后者四个方向都要，供 updateThreats 的主路径用；
// 这里只算一个方向，供 computeRay 段用 —— 那里的候选子 psq 与 s 同行或同列，
// 而 s 只落在 psq 四条射线中的一条上，另外三条在「s 空 / s 有子」两种占用下
// 完全一致（调用处取对称差，它们会自然抵消）。四个方向算一个，省掉四分之三。
func slidingAttackDir(sq, d int, occ bitboard) (rook, cannon bitboard) {
	ray := rayBB[sq][d]
	fb := beyondOf(ray, occ, d)
	rook = ray.andNot(fb)
	// 炮：越过炮架后到下一个子（含）。fb 为空时 beyondOf 得空集，整体为空。
	cannon = fb.andNot(beyondOf(fb, occ, d))
	return rook, cannon
}

// incIndex 把「递增方向（北/东）上第一个阻挡所在格」编码成 beyondInc 的下标。
//
// 做法：先看低位字（格号 0~63）有没有子，有就直接取它的最低置位；
// 没有才看高位字。两个分支都用算式代替 —— math/bits 在入参为 0 时返回 64，
// 正好让两条路径的结果能拼在一个表达式里：
//
//	lo 非零 → a（0~63）；否则 64 + b（64~128，两个都空时得 128 = 空集表项）
func incIndex(lo, hi uint64) int {
	nz := (lo | -lo) >> 63           // lo 非零 → 1，否则 0
	a := uint64(trailingZeros64(lo)) // 空 → 64
	b := uint64(trailingZeros64(hi)) // 空 → 64
	m := -nz                         // 全 1 或全 0，用来无分支二选一
	return int((a & m) | ((64 + b) &^ m))
}

// decIndex 把「递减方向（南/西）上第一个阻挡所在格」编码成 beyondDec 的下标，
// 编码比递增方向多偏移 1（表里第 0 项留给「射线全空」）。
//
// 递减方向要取最高置位，所以先看高位字；用 bits.LeadingZeros64 的 0 → 64 性质
// 把两条路径拼进一个表达式：
//
//	hi 非零 → 128-a（65~128）；否则 64-b（1~64，两个都空时得 0 = 空集表项）
func decIndex(lo, hi uint64) int {
	nz := (hi | -hi) >> 63
	a := uint64(bitsLeadingZeros64(hi))
	b := uint64(bitsLeadingZeros64(lo))
	m := -nz
	return int(((128 - a) & m) | ((64 - b) &^ m))
}

var bishopDirections = [4]int{
	2 * dirNorthEast, 2 * dirSouthEast, 2 * dirSouthWest, 2 * dirNorthWest,
}

var knightDirections = [8]int{
	2*dirSouth + dirWest, 2*dirSouth + dirEast, dirSouth + 2*dirWest, dirSouth + 2*dirEast,
	dirNorth + 2*dirWest, dirNorth + 2*dirEast, 2*dirNorth + dirWest, 2*dirNorth + dirEast,
}

// 象与马的「正向」落点表：某格上的子朝各方向能走到的格子与对应的象眼/腿位。
// 落点越界时两者都是 -1。
//
// 注意与 position.go 的 knightToFrom / knightToLeg 区分：那两张是**反向**表
// （「哪些马能攻击 s」，供 isAttacked 与 computeRay 的候选集用），这两张是
// **正向**表（「s 上的马能攻击哪些格」）。
//
// 之所以要预计算：原来每个方向都在热路径上现算 lameLeaperPath，里面有
// fileOf/rankOf/abs/取模/除法加一串分支，一次调用要算 4~8 遍。
var (
	bishopTo  [squareNB][4]int
	bishopEye [squareNB][4]int
	knightTo  [squareNB][8]int
	knightLeg [squareNB][8]int
)

// buildLeaperForwardTables 填上面四张表。
//
// **必须在 buildPseudoAttacks 之前调用**：那一步会拿「空占用集」调
// lameLeaperAttack 来建 pseudoAttacks[ptBishop] / [ptKnight]，表还没填就会
// 静默建出全空的攻击集 —— 与 rayBB 必须最先建是同一类坑（错也不报，只是
// 整个威胁特征集偏移）。
func buildLeaperForwardTables() {
	for s := 0; s < squareNB; s++ {
		for i, d := range bishopDirections {
			to := s + d
			if !okSquare(to) || chebyshev(s, to) >= 3 {
				bishopTo[s][i], bishopEye[s][i] = -1, -1
				continue
			}
			bishopTo[s][i] = to
			// 上一步的 chebyshev < 3 严格强于 lameLeaperPath 内部的 > 3，
			// 所以这里必然拿到一个有效象眼（不会是 -1）。
			bishopEye[s][i] = lameLeaperPath(ptBishop, d, s)
		}
		for i, d := range knightDirections {
			to := s + d
			if !okSquare(to) || chebyshev(s, to) >= 3 {
				knightTo[s][i], knightLeg[s][i] = -1, -1
				continue
			}
			knightTo[s][i] = to
			knightLeg[s][i] = lameLeaperPath(ptKnight, d, s)
		}
	}
}

// lameLeaperPath 对应 lame_leaper_path<pt>(d, s)：走到 (s+d) 时被蹩的那个格。
// 返回 -1 表示该方向无有效落点。pt 只支持象与马。
func lameLeaperPath(pt, d, s int) int {
	to := s + d
	if !okSquare(to) || chebyshev(s, to) > 3 {
		return -1
	}
	if pt == ptKnightTo {
		s, to = to, s
		d = -d
	}
	dr := dirSouth
	if d > 0 {
		dr = dirNorth
	}
	m := d % dirNorth
	if absInt(m) >= dirNorth/2 {
		m = -m
	}
	df := dirEast
	if m < 0 {
		df = dirWest
	}
	switch diff := absInt(fileOf(to)-fileOf(s)) - absInt(rankOf(to)-rankOf(s)); {
	case diff > 0:
		s += df
	case diff < 0:
		s += dr
	default:
		s += df + dr
	}
	return s
}

// lameLeaperAttack 对应 lame_leaper_attack<pt>：象或马在给定占用下的攻击集。
//
// 落点与象眼/腿位都取自预计算表（见 bishopTo / knightTo 的说明），热路径上
// 只剩「查表 + 判腿位是否被占 + 置位」。原实现每个方向现算 lameLeaperPath，
// 是这段的主要开销。
func lameLeaperAttack(pt, s int, occupied bitboard) bitboard {
	var b bitboard
	if pt == ptBishop {
		for i := 0; i < 4; i++ {
			to := bishopTo[s][i]
			if to < 0 {
				continue
			}
			if occupied.test(bishopEye[s][i]) {
				continue
			}
			b.set(to)
		}
		side := 0
		if rankOf(s) > 4 {
			side = 1
		}
		return b.and(halfBB[side])
	}
	for i := 0; i < 8; i++ {
		to := knightTo[s][i]
		if to < 0 {
			continue
		}
		if occupied.test(knightLeg[s][i]) {
			continue
		}
		b.set(to)
	}
	return b
}

// PseudoAttacks 的槽位（bitboard.h 用 PIECE_TYPE_NB+3 的空间）。
const (
	paPawnWhite   = 0  // NO_PIECE_TYPE：红兵走法
	paAdvisorUnc  = 3  // ADVISOR+1：仕的无约束斜步（与 CANNON 槽重合，炮不占此槽）
	paPawnBlack   = 4  // PAWN：黑兵走法
	paPawnToWhite = 8  // PAWN_TO-1：红兵反向
	paPawnToBlack = 9  // PAWN_TO：黑兵反向
	paKingUnc     = 10 // KING+3：将的无约束正交步
	paSlots       = 14
)

var pseudoAttacks [paSlots][squareNB]bitboard

// pawnSlot 返回某颜色兵在 PseudoAttacks 中对应的槽位。
func pawnSlot(c int) int {
	if c == colorWhite {
		return paPawnWhite
	}
	return paPawnBlack
}

func buildPseudoAttacks() {
	for s := 0; s < squareNB; s++ {
		pseudoAttacks[paPawnWhite][s] = pawnAttacks(colorWhite, s)
		pseudoAttacks[paPawnBlack][s] = pawnAttacks(colorBlack, s)
		pseudoAttacks[paPawnToWhite][s] = pawnAttacksTo(colorWhite, s)
		pseudoAttacks[paPawnToBlack][s] = pawnAttacksTo(colorBlack, s)

		pseudoAttacks[ptRook][s] = slidingAttack(ptRook, s, bitboard{})
		pseudoAttacks[ptBishop][s] = lameLeaperAttack(ptBishop, s, bitboard{})
		pseudoAttacks[ptKnight][s] = lameLeaperAttack(ptKnight, s, bitboard{})

		for _, step := range [4]int{dirNorth, dirSouth, dirWest, dirEast} {
			if palaceBB.test(s) {
				pseudoAttacks[ptKing][s] = pseudoAttacks[ptKing][s].or(safeDestination(s, step).and(palaceBB))
			}
			pseudoAttacks[paKingUnc][s] = pseudoAttacks[paKingUnc][s].or(safeDestination(s, step))
		}
		for _, step := range [4]int{dirNorthWest, dirNorthEast, dirSouthWest, dirSouthEast} {
			if palaceBB.test(s) {
				pseudoAttacks[ptAdvisor][s] = pseudoAttacks[ptAdvisor][s].or(safeDestination(s, step).and(palaceBB))
			}
			pseudoAttacks[paAdvisorUnc][s] = pseudoAttacks[paAdvisorUnc][s].or(safeDestination(s, step))
		}
	}
}

// validPairs 对应 full_threats.cpp 的 ValidPairs，索引为 [attacker][attacked]。
var validPairs = [pieceNB][pieceNB]bool{
	/* _ */ {false, false, false, false, false, false, false, false, false, false, false, false, false, false, false, false},
	/* R */ {false, true, true, true, true, true, true, true, false, true, true, true, true, true, true, false},
	/* A */ {false, true, true, true, false, true, false, true, false, true, false, true, true, true, false, false},
	/* C */ {false, true, true, true, true, true, true, true, false, true, true, true, true, true, true, false},
	/* P */ {false, false, false, true, true, true, true, false, false, false, true, true, true, true, true, false},
	/* N */ {false, true, true, true, true, true, true, true, false, true, true, true, true, true, true, false},
	/* B */ {false, true, false, true, true, true, true, true, false, true, false, true, true, true, false, false},
	/* K */ {false, false, true, true, false, true, true, false, false, false, false, true, true, true, false, false},
	/* _ */ {false, false, false, false, false, false, false, false, false, false, false, false, false, false, false, false},
	/* r */ {false, true, true, true, true, true, true, false, false, true, true, true, true, true, true, true},
	/* a */ {false, true, false, true, true, true, false, false, false, true, true, true, false, true, false, true},
	/* c */ {false, true, true, true, true, true, true, false, false, true, true, true, true, true, true, true},
	/* p */ {false, false, true, true, true, true, true, false, false, false, false, true, true, true, true, false},
	/* n */ {false, true, true, true, true, true, true, false, false, true, true, true, true, true, true, true},
	/* b */ {false, true, false, true, true, true, false, false, false, true, false, true, true, true, true, true},
	/* k */ {false, false, false, true, true, true, false, false, false, false, true, true, false, true, true, false},
}

var (
	// threatOffsets[attacker][from][to][attacked] 是威胁特征的偏移；
	// 无效组合保持为 Dimensions。
	threatOffsets [pieceNB][squareNB][squareNB][pieceNB]uint16
	// threatCount 是分配出的偏移总数，必须等于 FullThreats::Dimensions。
	threatCount int
)

func buildThreatOffsets() {
	for i := range threatOffsets {
		for j := range threatOffsets[i] {
			for k := range threatOffsets[i][j] {
				for l := range threatOffsets[i][j][k] {
					threatOffsets[i][j][k][l] = uint16(ThreatInputs)
				}
			}
		}
	}

	off := 0
	for _, attacker := range allPieceList {
		pt := pieceType(attacker)
		for from := 0; from < squareNB; from++ {
			if !validBB[attacker].test(from) {
				continue
			}
			var attacks bitboard
			switch pt {
			case ptPawn:
				attacks = pseudoAttacks[pawnSlot(pieceColor(attacker))][from]
			case ptCannon:
				// 炮的威胁按「正交一步都是炮架」的固定占用集计算，只依赖 from。
				attacks = slidingAttack(ptCannon, from, pseudoAttacks[paKingUnc][from])
			default:
				attacks = pseudoAttacks[pt][from]
			}

			for _, attacked := range allPieceList {
				if !validPairs[attacker][attacked] {
					continue
				}
				targets := attacks.and(validBB[attacked])
				for !targets.isEmpty() {
					to := targets.popLSB()
					// 同类型棋子之间的威胁是双向的，只保留一个方向。
					enemy := pieceColor(attacker) != pieceColor(attacked)
					semiExcluded := pt == pieceType(attacked) &&
						(pt != ptPawn || (enemy && fileOf(from) == fileOf(to)) ||
							(!enemy && rankOf(from) == rankOf(to))) &&
						pt != ptKnight
					if !semiExcluded || from > to {
						threatOffsets[attacker][from][to][attacked] = uint16(off)
						off++
					}
				}
			}
		}
	}
	threatCount = off
}
