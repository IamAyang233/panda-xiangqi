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
	ReasonRepetition   = "repetition"   // 三次重复局面（非长将，按和棋处理）
	ReasonLongCheck    = "long_check"   // 长将：一方连续将军形成循环，长将方判负
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

	// 三次重复局面：构成长将则长将方判负（P1），否则和
	if p.RepetitionCount() >= 3 {
		if winner, ok := p.LongCheckWinner(); ok {
			st.Result, st.Reason = winner, ReasonLongCheck
			st.Winner = winner
			return st
		}
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

// LongCheckWinner 检测三次重复的循环是否构成长将（一方在循环内每步都将军，
// 另一方至少一步不将军）。返回胜方（长将方的对手）与 true；双方长将或
// 非长将（一将一闲等）返回 false，由调用方按和棋处理。
func (p *Position) LongCheckWinner() (string, bool) {
	// 从后往前找当前局面键的上一次出现位置：该步之后的着法构成最近一圈循环
	i1 := -1
	for i := len(p.hist) - 1; i >= 0; i-- {
		if p.hist[i].key == p.Key {
			i1 = i
			break
		}
	}
	if i1 < 0 {
		return "", false
	}
	redAll, blackAll := true, true
	for _, h := range p.hist[i1:] {
		if h.color == Red {
			if !h.check {
				redAll = false
			}
		} else if !h.check {
			blackAll = false
		}
	}
	switch {
	case redAll && !blackAll:
		return ResultBlackWin, true // 红连续将军 → 红长将判负
	case blackAll && !redAll:
		return ResultRedWin, true // 黑连续将军 → 黑长将判负
	default:
		return "", false // 双方长将 / 非长将 → 和
	}
}
