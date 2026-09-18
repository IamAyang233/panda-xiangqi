package game

import "math/bits"

// 着法生成（位运算版，M3）：全部走法来自预计算攻击表 + 位棋盘集合运算，
// 不再逐格步进。几何规则（蹩腿/塞眼/九宫/过河）已烘焙进攻击表，
// 照面等合法性仍由 LegalMoves 过滤（走一步后判己方是否被将军）。

var stepDirs = [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}}

// step 从 sq 沿 (df,dr) 走一步；越界返回 -1。
// 位运算版不再使用，保留给测试中的逐格参考实现。
func step(sq, df, dr int) int {
	f := bbFile(sq) + df
	r := bbRank(sq) + dr
	if f < 0 || f > 8 || r < 0 || r > 9 {
		return -1
	}
	return bbSquare(f, r)
}

// GenMoves 生成 side 方全部伪合法着法。
//
// 所有枚举都走 **lo/hi 两个 uint64** 而不是 bitboard 值：后者的 `IsEmpty` +
// `popLSB`（内含 `lsb` + `Clear`）每一步要碰三次 16 字节内存，而 lo/hi 能一直
// 留在寄存器里。成因与 nnue 包那几处 lo/hi 改造完全相同（`tools/asm_share.py`
// 数出本函数汇编里 MOVUPS 有 206 条，是 game 包内最多）。
func (p *Position) GenMoves(side int) []Move {
	moves := make([]Move, 0, 48)
	sideIdx := side >> 3
	ownPieces := p.bb.byColor[sideIdx]
	if ownPieces.IsEmpty() {
		return moves
	}
	occLo, occHi := p.bb.occ[0], p.bb.occ[1]
	ownLo, ownHi := ownPieces[0], ownPieces[1]
	// free = 空格或敌子。注意要按 bbAll 裁剪，不能直接取反 —— 取反会把
	// 90..127 那些无效位也置 1，于是「目标格」里会混进不存在的格子。
	freeLo, freeHi := bbAll[0]&^ownLo, bbAll[1]&^ownHi

	// 帅、仕：九宫表已按侧裁剪，直接与 free 求交。
	for lo, hi := p.bb.byType[King][0]&ownLo, p.bb.byType[King][1]&ownHi; lo|hi != 0; {
		var sq int
		sq, lo, hi = drain(lo, hi)
		m := kingMoves[sideIdx][sq]
		moves = appendTargets(moves, sq, m[0]&freeLo, m[1]&freeHi)
	}
	for lo, hi := p.bb.byType[Advisor][0]&ownLo, p.bb.byType[Advisor][1]&ownHi; lo|hi != 0; {
		var sq int
		sq, lo, hi = drain(lo, hi)
		m := advisorMoves[sideIdx][sq]
		moves = appendTargets(moves, sq, m[0]&freeLo, m[1]&freeHi)
	}
	// 象：表已含"不过河"，逐目标查塞象眼。
	for lo, hi := p.bb.byType[Elephant][0]&ownLo, p.bb.byType[Elephant][1]&ownHi; lo|hi != 0; {
		var sq int
		sq, lo, hi = drain(lo, hi)
		for _, es := range elephantSteps[sideIdx][sq] {
			if es.to < 0 || bitTest(occLo, occHi, es.eye) || bitTest(ownLo, ownHi, es.to) {
				continue
			}
			moves = append(moves, Move{uint8(sq), uint8(es.to)})
		}
	}
	// 马：逐目标查蹩腿。
	for lo, hi := p.bb.byType[Horse][0]&ownLo, p.bb.byType[Horse][1]&ownHi; lo|hi != 0; {
		var sq int
		sq, lo, hi = drain(lo, hi)
		for _, ks := range knightSteps[sq] {
			if ks.to < 0 {
				break
			}
			if bitTest(occLo, occHi, ks.leg) || bitTest(ownLo, ownHi, ks.to) {
				continue
			}
			moves = append(moves, Move{uint8(sq), uint8(ks.to)})
		}
	}
	// 兵：前进 + 过河横走（已烘焙进表）。
	for lo, hi := p.bb.byType[Pawn][0]&ownLo, p.bb.byType[Pawn][1]&ownHi; lo|hi != 0; {
		var sq int
		sq, lo, hi = drain(lo, hi)
		m := pawnMoves[sideIdx][sq]
		moves = appendTargets(moves, sq, m[0]&freeLo, m[1]&freeHi)
	}
	// 车、炮：射线取首个阻挡。
	for lo, hi := p.bb.byType[Rook][0]&ownLo, p.bb.byType[Rook][1]&ownHi; lo|hi != 0; {
		var sq int
		sq, lo, hi = drain(lo, hi)
		t := rookAttacks(sq, p.bb.occ, ownPieces)
		moves = appendTargets(moves, sq, t[0], t[1])
	}
	for lo, hi := p.bb.byType[Cannon][0]&ownLo, p.bb.byType[Cannon][1]&ownHi; lo|hi != 0; {
		var sq int
		sq, lo, hi = drain(lo, hi)
		quiet, capture := cannonAttacks(sq, p.bb.occ, ownPieces)
		moves = appendTargets(moves, sq, (quiet[0]|capture[0])&freeLo, (quiet[1]|capture[1])&freeHi)
	}
	return moves
}

// drain 弹出 lo/hi 里的最低位置，返回其索引与剩余两字（已空时返回 -1）。
//
// 用返回值而不是传指针：取地址会让这两个变量被迫落回内存，
// 那就把 lo/hi 的好处全丢了。
func drain(lo, hi uint64) (int, uint64, uint64) {
	if lo != 0 {
		return bits.TrailingZeros64(lo), lo & (lo - 1), hi
	}
	if hi != 0 {
		return 64 + bits.TrailingZeros64(hi), 0, hi & (hi - 1)
	}
	return -1, 0, 0
}

// appendTargets 把 lo/hi 位集里的每个目标格按 (from, to) 追加进 moves。
func appendTargets(moves []Move, from int, lo, hi uint64) []Move {
	for lo|hi != 0 {
		var to int
		to, lo, hi = drain(lo, hi)
		moves = append(moves, Move{uint8(from), uint8(to)})
	}
	return moves
}

// bitTest 测试 sq 是否在 lo/hi 位集里（等价于 Bitboard.Test，但不构造 16 字节值）。
func bitTest(lo, hi uint64, sq int) bool {
	if sq >= 64 {
		return hi&(1<<uint(sq-64)) != 0
	}
	return lo&(1<<uint(sq)) != 0
}

func own(side int, target byte) bool { return target != Empty && ColorOf(target) == side }

// LegalMoves 返回 side 方全部合法着法（走完己方不被将军/不照面）。
//
// 用「不动局面的试走判定」（isAttackedAfter）而不是 Make → InCheck → Unmake：
// 后者每次检验要做两次王安全扫描（Make 内部还算了一次「对方是否被将军」），
// 实测占全部扫描的 75%。语义严格等价，由 TestLegalMovesMatchRef 逐局面对拍
// 与 perft 的黄金计数共同守住。
//
// 未被将军时再叠一层「危险格」预筛（见 kingSafety）：非王的着法若 from 与 to
// 都不在危险格里，就不可能让王陷入被攻，直接判合法 —— 省掉绝大多数扫描。
// 被将军时必须走完整扫描（危险格那套推导的前提不成立），但这种节点很少。
func (p *Position) LegalMoves(side int) []Move {
	pseudo := p.GenMoves(side)
	legal := pseudo[:0]
	them := Opponent(side)
	ksq := p.kingSq[side>>3]
	inCheck, danger := p.bb.kingSafety(ksq, side)
	for _, m := range pseudo {
		from, to := int(m.From), int(m.To)
		if from != ksq && !inCheck && !danger.Test(from) && !danger.Test(to) {
			legal = append(legal, m)
			continue
		}
		sq := ksq
		if from == ksq {
			sq = to // 走的是王，判定点跟着移
		}
		if !p.bb.isAttackedAfter(sq, them, from, to, p.Board[to]) {
			legal = append(legal, m)
		}
	}
	return legal
}

// IsLegal 判断 m 是否为当前轮走方的合法着法。
func (p *Position) IsLegal(m Move) bool {
	for _, mm := range p.LegalMoves(p.Turn) {
		if mm == m {
			return true
		}
	}
	return false
}

// perft 走法树节点计数（规则引擎金标准）。
func perft(p *Position, depth int) uint64 {
	if depth == 0 {
		return 1
	}
	moves := p.LegalMoves(p.Turn)
	if depth == 1 {
		return uint64(len(moves))
	}
	var n uint64
	for _, m := range moves {
		p.Make(m)
		n += perft(p, depth-1)
		p.Unmake()
	}
	return n
}

// Perft 对外入口。
func Perft(p *Position, depth int) uint64 { return perft(p, depth) }

// PerftDiv 按根着法拆分计数（调试用）。
func PerftDiv(p *Position, depth int) map[Move]uint64 {
	out := map[Move]uint64{}
	for _, m := range p.LegalMoves(p.Turn) {
		p.Make(m)
		out[m] = perft(p, depth-1)
		p.Unmake()
	}
	return out
}
