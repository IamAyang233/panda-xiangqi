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
	Result  string // Result*（空 = 进行中）
	Reason  string
	InCheck bool // 当前方（轮走方）被将军
	Winner  string
	IsDraw  bool
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
		pc := p.Board[sq90]
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
// 搜索用的空着（null move）不入账：它不改变子力，不应参与重复判定。
func (p *Position) RepetitionCount() int {
	n := 1
	for _, h := range p.hist {
		if !h.null && h.key == p.Key {
			n++
		}
	}
	return n
}

// LongCheckWinner 检测三次重复的循环是否构成长将（一方在循环内每步都将军，
// 另一方至少一步不将军）。返回胜方（长将方的对手）与 true；双方长将或
// 非长将（一将一闲等）返回 false，由调用方按和棋处理。
//
// ⚠️ 空着（null move）必须整体排除，与 RepetitionCount 保持同一口径：
//
//   - 空着不改变子力，不是真实着法，它出现在历史里只是搜索的中间产物；
//   - null 条目的 `check` 恒为 false、`color` 被置成走子方 —— 一旦混进循环窗口，
//     会把「每步都将军」的那一方判成「没在将军」，方向性结论直接反转；
//   - 更隐蔽的是**窗口只剩单个条目**时：redAll/blackAll 的初值都是 true，
//     单条 null 条目（check=false）会让另一方「空洞地为真」，凭空判出胜负。
//
// 排除这一项的成本为零，而漏掉它的后果是「用完全错误的理由判对局胜负」。
func (p *Position) LongCheckWinner() (string, bool) {
	// 从后往前找当前局面键的上一次出现位置：该步之后的着法构成最近一圈循环。
	// 空着不算「出现」（它并不真的走到过这个局面）。
	i1 := -1
	for i := len(p.hist) - 1; i >= 0; i-- {
		if !p.hist[i].null && p.hist[i].key == p.Key {
			i1 = i
			break
		}
	}
	if i1 < 0 {
		return "", false
	}
	redAll, blackAll := true, true
	// 窗口内是否有真实着法：全为空着时两个 all 都保持初值 true，
	// 会落进「双方都长将」以外的分支，必须显式拒绝。
	hasReal := false
	for _, h := range p.hist[i1:] {
		if h.null {
			continue
		}
		hasReal = true
		if h.color == Red {
			if !h.check {
				redAll = false
			}
		} else if !h.check {
			blackAll = false
		}
	}
	if !hasReal {
		return "", false
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
