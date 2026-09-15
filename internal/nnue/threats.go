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
// 用预计算的射线表 + 位扫描找第一个阻挡，不做逐格步进 —— 这是每次走子都会
// 调用的热点（updateThreats 里要算车、炮各一套）。
//
// 语义与逐格版一致：车打到阻挡子为止（含阻挡子本身）；
// 炮在越过炮架后把余下的格子全部纳入（是否真有子由调用方按占用集过滤）。
func slidingAttack(pt, sq int, occupied bitboard) bitboard {
	var attack bitboard
	for di := 0; di < 4; di++ {
		ray := rayBB[sq][di]
		blockers := ray.and(occupied)
		if blockers.isEmpty() {
			// 整条射线都没子：车可以直接走到底；炮没有炮架，打不到任何格。
			if pt == ptRook {
				attack = attack.or(ray)
			}
			continue
		}
		// 北/东向格号递增取最低置位，南/西向递减取最高置位。
		var fb int
		if di == 0 || di == 2 {
			fb = blockers.lsb()
		} else {
			fb = blockers.msb()
		}
		// sq 到第一个阻挡之间的空段。
		forward := rayBB[fb][di].or(bbOf(fb))
		if pt == ptRook {
			// 车：把空段与阻挡子本身都算进去。
			attack = attack.or(ray.andNot(forward))
			attack.set(fb)
			continue
		}
		// 炮：炮架之前一格都不算（hurdle 未越过），
		// 越过 fb 之后一直算到下一个子（含）为止。
		second := rayBB[fb][di]
		after := second.and(occupied)
		if after.isEmpty() {
			attack = attack.or(second)
			continue
		}
		var nb int
		if di == 0 || di == 2 {
			nb = after.lsb()
		} else {
			nb = after.msb()
		}
		attack = attack.or(second.andNot(rayBB[nb][di]))
	}
	return attack
}

var bishopDirections = [4]int{
	2 * dirNorthEast, 2 * dirSouthEast, 2 * dirSouthWest, 2 * dirNorthWest,
}

var knightDirections = [8]int{
	2*dirSouth + dirWest, 2*dirSouth + dirEast, dirSouth + 2*dirWest, dirSouth + 2*dirEast,
	dirNorth + 2*dirWest, dirNorth + 2*dirEast, 2*dirNorth + dirWest, 2*dirNorth + dirEast,
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
func lameLeaperAttack(pt, s int, occupied bitboard) bitboard {
	var b bitboard
	dirs := knightDirections[:]
	if pt == ptBishop {
		dirs = bishopDirections[:]
	}
	for _, d := range dirs {
		to := s + d
		if !okSquare(to) || chebyshev(s, to) >= 3 {
			continue
		}
		if leg := lameLeaperPath(pt, d, s); leg >= 0 && occupied.test(leg) {
			continue
		}
		b.set(to)
	}
	if pt == ptBishop {
		side := 0
		if rankOf(s) > 4 {
			side = 1
		}
		b = b.and(halfBB[side])
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
