package game

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
func (p *Position) GenMoves(side int) []Move {
	moves := make([]Move, 0, 48)
	sideIdx := side >> 3
	ownPieces := p.bb.byColor[sideIdx]
	if ownPieces.IsEmpty() {
		return moves
	}
	occ := p.bb.occ
	free := bbAll.AndNot(ownPieces) // 空格或敌子

	// 帅、仕：九宫表已按侧裁剪，直接与 free 求交。
	for b := p.bb.byType[King].And(ownPieces); !b.IsEmpty(); {
		sq := b.popLSB()
		for t := kingMoves[sideIdx][sq].And(free); !t.IsEmpty(); {
			moves = append(moves, Move{uint8(sq), uint8(t.popLSB())})
		}
	}
	for b := p.bb.byType[Advisor].And(ownPieces); !b.IsEmpty(); {
		sq := b.popLSB()
		for t := advisorMoves[sideIdx][sq].And(free); !t.IsEmpty(); {
			moves = append(moves, Move{uint8(sq), uint8(t.popLSB())})
		}
	}
	// 象：表已含"不过河"，逐目标查塞象眼。
	for b := p.bb.byType[Elephant].And(ownPieces); !b.IsEmpty(); {
		sq := b.popLSB()
		for _, es := range elephantSteps[sideIdx][sq] {
			if es.to < 0 || occ.Test(es.eye) || ownPieces.Test(es.to) {
				continue
			}
			moves = append(moves, Move{uint8(sq), uint8(es.to)})
		}
	}
	// 马：逐目标查蹩腿。
	for b := p.bb.byType[Horse].And(ownPieces); !b.IsEmpty(); {
		sq := b.popLSB()
		for _, ks := range knightSteps[sq] {
			if ks.to < 0 {
				break
			}
			if occ.Test(ks.leg) || ownPieces.Test(ks.to) {
				continue
			}
			moves = append(moves, Move{uint8(sq), uint8(ks.to)})
		}
	}
	// 兵：前进 + 过河横走（已烘焙进表）。
	for b := p.bb.byType[Pawn].And(ownPieces); !b.IsEmpty(); {
		sq := b.popLSB()
		for t := pawnMoves[sideIdx][sq].And(free); !t.IsEmpty(); {
			moves = append(moves, Move{uint8(sq), uint8(t.popLSB())})
		}
	}
	// 车、炮：射线取首个阻挡。
	for b := p.bb.byType[Rook].And(ownPieces); !b.IsEmpty(); {
		sq := b.popLSB()
		for t := rookAttacks(sq, occ, ownPieces); !t.IsEmpty(); {
			moves = append(moves, Move{uint8(sq), uint8(t.popLSB())})
		}
	}
	for b := p.bb.byType[Cannon].And(ownPieces); !b.IsEmpty(); {
		sq := b.popLSB()
		quiet, capture := cannonAttacks(sq, occ, ownPieces)
		for t := quiet.Or(capture).And(free); !t.IsEmpty(); {
			moves = append(moves, Move{uint8(sq), uint8(t.popLSB())})
		}
	}
	return moves
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
