package game

// isAttacked 反向攻击探测（A4）：从被攻击点向外找，一次遍历覆盖全部攻击类型。
// 注意：仕/相无法进入对方九宫，故不参与对将帅的攻击检测；本函数仅用于将帅安全判断。
func (p *Position) isAttacked(sq int, by int) bool {
	// 1) 照面 / 车 / 帅：正交方向第一个子；2) 炮：越过第一个子后的第二个子。
	for _, d := range rookDirs {
		to := sq + d
		for p.Board[to] == Empty {
			to += d
		}
		if t := p.Board[to]; t != Edge && ColorOf(t) == by {
			tt := TypeOf(t)
			if tt == Rook {
				return true
			}
			if tt == King && (d == 16 || d == -16) { // 将帅照面（纵向）
				return true
			}
		}
		// 越过挡子继续找炮
		for to += d; p.Board[to] == Empty; to += d {
		}
		if t := p.Board[to]; t != Edge && ColorOf(t) == by && TypeOf(t) == Cannon {
			return true
		}
	}
	// 3) 马：反向马攻表 + 蹩腿检查。
	for _, atk := range horseAttack[mailbox90[sq]] {
		if p.Board[atk.origin] != Edge && p.Board[atk.origin] != Empty &&
			ColorOf(p.Board[atk.origin]) == by && TypeOf(p.Board[atk.origin]) == Horse &&
			p.Board[atk.leg] == Empty {
			return true
		}
	}
	// 4) 兵：正前一点 / 过河兵横吃。
	f, r := FileOf256(sq), RankOf256(sq)
	if by == Red {
		if r >= 1 {
			if t := p.Board[SQ256(f, r-1)]; t != Empty && t != Edge && t == Piece(Red, Pawn) {
				return true
			}
		}
		if r >= 5 { // 只有过河红兵才能横吃 sq（sq 在黑方半场）
			for _, df := range [2]int{-1, +1} {
				nf := f + df
				if nf >= 0 && nf <= 8 {
					if t := p.Board[SQ256(nf, r)]; t == Piece(Red, Pawn) {
						return true
					}
				}
			}
		}
	} else {
		if r <= 8 {
			if t := p.Board[SQ256(f, r+1)]; t == Piece(Black, Pawn) {
				return true
			}
		}
		if r <= 4 { // 只有过河黑卒才能横吃 sq（sq 在红方半场）
			for _, df := range [2]int{-1, +1} {
				nf := f + df
				if nf >= 0 && nf <= 8 {
					if t := p.Board[SQ256(nf, r)]; t == Piece(Black, Pawn) {
						return true
					}
				}
			}
		}
	}
	return false
}
