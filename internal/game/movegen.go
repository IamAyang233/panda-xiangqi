package game

// 着法生成（A3）：只做几何规则判断（蹩腿/塞眼/九宫/过河/照面交由 A5 过滤）。
// 目标为空或敌子即压栈；buffer 复用以减少分配。

var rookDirs = [4]int{+1, -1, +16, -16}

// GenMoves 生成 side 方全部伪合法着法。
func (p *Position) GenMoves(side int) []Move {
	moves := make([]Move, 0, 48)
	for sq90 := 0; sq90 < 90; sq90++ {
		sq := mailbox256[sq90]
		pc := p.Board[sq]
		if pc == Empty || pc == Edge || ColorOf(pc) != side {
			continue
		}
		f, r := FileOf256(sq), RankOf256(sq)
		switch TypeOf(pc) {
		case King:
			for _, d := range rookDirs {
				to := sq + d
				if p.Board[to] != Edge && inPalace(side, FileOf256(to), RankOf256(to)) && !own(side, p.Board[to]) {
					moves = append(moves, Move{uint8(sq), uint8(to)})
				}
			}
		case Advisor:
			for _, d := range [4]int{+17, +15, -17, -15} {
				to := sq + d
				if p.Board[to] != Edge && inPalace(side, FileOf256(to), RankOf256(to)) && !own(side, p.Board[to]) {
					moves = append(moves, Move{uint8(sq), uint8(to)})
				}
			}
		case Elephant:
			for _, d := range [4]int{+34, +30, -30, -34} {
				to := sq + d
				if p.Board[to] == Edge || own(side, p.Board[to]) {
					continue
				}
				if !ownSide(side, RankOf256(to)) || p.Board[sq+d/2] != Empty { // 塞象眼 + 不过河
					continue
				}
				moves = append(moves, Move{uint8(sq), uint8(to)})
			}
		case Horse:
			for _, m := range knightTbl {
				tf, tr := f+m.df, r+m.dr
				if tf < 0 || tf > 8 || tr < 0 || tr > 9 {
					continue
				}
				to := SQ256(tf, tr)
				if p.Board[to] != Edge && !own(side, p.Board[to]) && p.Board[SQ256(f+m.lf, r+m.lr)] == Empty {
					moves = append(moves, Move{uint8(sq), uint8(to)})
				}
			}
		case Rook:
			for _, d := range rookDirs {
				for to := sq + d; ; to += d {
					t := p.Board[to]
					if t == Empty {
						moves = append(moves, Move{uint8(sq), uint8(to)})
						continue
					}
					if t != Edge && ColorOf(t) != side {
						moves = append(moves, Move{uint8(sq), uint8(to)})
					}
					break
				}
			}
		case Cannon:
			for _, d := range rookDirs {
				to := sq + d
				for p.Board[to] == Empty { // 平走
					moves = append(moves, Move{uint8(sq), uint8(to)})
					to += d
				}
				// to 处为第一个挡子（或哨兵），越过后找第二个子
				for to += d; p.Board[to] == Empty; to += d {
				}
				if t := p.Board[to]; t != Edge && ColorOf(t) != side { // 翻山吃
					moves = append(moves, Move{uint8(sq), uint8(to)})
				}
			}
		case Pawn:
			fwd := 16
			if side == Black {
				fwd = -16
			}
			to := sq + fwd
			if p.Board[to] != Edge && !own(side, p.Board[to]) {
				moves = append(moves, Move{uint8(sq), uint8(to)})
			}
			crossed := side == Red && r >= 5 || side == Black && r <= 4
			if crossed {
				for _, d := range [2]int{+1, -1} {
					to := sq + d
					if p.Board[to] != Edge && !own(side, p.Board[to]) {
						moves = append(moves, Move{uint8(sq), uint8(to)})
					}
				}
			}
		}
	}
	return moves
}

func own(side int, target byte) bool { return target != Empty && target != Edge && ColorOf(target) == side }

// LegalMoves 返回 side 方全部合法着法（A5：走完己方不被将军/不照面）。
func (p *Position) LegalMoves(side int) []Move {
	pseudo := p.GenMoves(side)
	legal := pseudo[:0]
	for _, m := range pseudo {
		p.Make(m)
		if !p.InCheck(side) {
			legal = append(legal, m)
		}
		p.Unmake()
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
