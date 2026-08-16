package game

// 局面状态判定（A6）。

// 结果与原因。
const (
	ResultNone     = "" // 对局进行中
	ResultRedWin   = "red_win"
	ResultBlackWin = "black_win"
	ResultDraw     = "draw"

	ReasonCheckmate    = "checkmate"    // 将死
	ReasonStalemate    = "stalemate"    // 困毙（无合法着法且未被将军，判无着方负）
	ReasonResign       = "resign"       // 认输
	ReasonRepetition   = "repetition"   // 三次重复局面（P0 按和棋处理）
	ReasonSixtyMoves   = "60_moves"     // 连续 60 回合未吃子
	ReasonInsufficient = "insufficient" // 双方均无进攻子力
)

// Status 描述当前局面判定结果（在每手走完后调用）。
type Status struct {
	Result   string // Result*（空 = 进行中）
	Reason   string
	InCheck  bool // 当前方（轮走方）被将军
	Winner   string
	IsDraw   bool
}

// CheckStatus 判定局面状态。
func (p *Position) CheckStatus() Status {
	side := p.Turn
	st := Status{InCheck: p.InCheck(side)}

	moves := p.LegalMoves(side)
	if len(moves) == 0 {
		// 将死与困毙均判无着方负（中国象棋规则）
		if st.InCheck {
			st.Result, st.Reason = winnerAgainst(side), ReasonCheckmate
		} else {
			st.Result, st.Reason = winnerAgainst(side), ReasonStalemate
		}
		st.Winner = st.Result
		return st
	}

	// 三次重复局面 → 和（P0；长打判负为 P1，见 A15）
	if p.RepetitionCount() >= 3 {
		st.Result, st.Reason, st.IsDraw = ResultDraw, ReasonRepetition, true
		return st
	}
	// 连续 60 回合未吃子 → 和
	if p.Halfmove >= 120 {
		st.Result, st.Reason, st.IsDraw = ResultDraw, ReasonSixtyMoves, true
		return st
	}
	// 双方均无进攻子力（车马炮兵）→ 和
	if !p.hasAttacking(Red) && !p.hasAttacking(Black) {
		st.Result, st.Reason, st.IsDraw = ResultDraw, ReasonInsufficient, true
		return st
	}
	return st
}

func winnerAgainst(side int) string {
	if side == Red {
		return ResultBlackWin
	}
	return ResultRedWin
}

func (p *Position) hasAttacking(color int) bool {
	for sq90 := 0; sq90 < 90; sq90++ {
		pc := p.Board[mailbox256[sq90]]
		if pc == Empty || ColorOf(pc) != color {
			continue
		}
		switch TypeOf(pc) {
		case Rook, Cannon, Horse, Pawn:
			return true
		}
	}
	return false
}

// RepetitionCount 统计当前局面键在历史中出现的次数（含当前局面）。
func (p *Position) RepetitionCount() int {
	n := 1
	for _, h := range p.hist {
		if h.key == p.Key {
			n++
		}
	}
	return n
}
