package game

// InitialFEN 标准初始局面（附录 B）。
const InitialFEN = "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"

// Move 着法：From/To 为 16×16 mailbox 下标（0~255）。
type Move struct {
	From, To uint8
}

// String 返回 UCI 坐标串，如 "h2e2"。
func (m Move) String() string { return SquareName(m.From) + SquareName(m.To) }

// SquareName 把 256 下标转为 UCI 格子名（如 "h2"）。
func SquareName(sq uint8) string {
	return string(rune('a'+FileOf256(int(sq)))) + string(rune('0'+RankOf256(int(sq))))
}

// SquareFromName 解析 UCI 格子名，非法返回 false。
func SquareFromName(s string) (uint8, bool) {
	if len(s) != 2 {
		return 0, false
	}
	f := int(s[0] - 'a')
	r := int(s[1] - '0')
	if f < 0 || f > 8 || r < 0 || r > 9 {
		return 0, false
	}
	return uint8(SQ256(f, r)), true
}

// MoveFromUCI 解析 "h2e2" 形式着法。
func MoveFromUCI(s string) (Move, bool) {
	if len(s) != 4 {
		return Move{}, false
	}
	from, ok1 := SquareFromName(s[:2])
	to, ok2 := SquareFromName(s[2:])
	if !ok1 || !ok2 {
		return Move{}, false
	}
	return Move{From: from, To: to}, true
}

type histEntry struct {
	move      Move
	captured  byte
	key       uint64
	halfmove  int
	fullmove  int
}

// Position 一局局面。非并发安全；上层需自行加锁。
type Position struct {
	Board    [256]byte
	Turn     int // Red / Black
	Key      uint64
	Halfmove int // 距上一吃子的半回合数
	Fullmove int // 完整回合数（黑走完后 +1）
	kingSq   [2]int
	hist     []histEntry
}

// NewPosition 返回初始局面。
func NewPosition() *Position {
	p, err := ParseFEN(InitialFEN)
	if err != nil {
		panic("game: bad built-in FEN: " + err.Error())
	}
	return p
}

// Clone 深拷贝局面（含历史栈）。
func (p *Position) Clone() *Position {
	q := *p
	q.hist = append([]histEntry(nil), p.hist...)
	return &q
}

// PieceAt90 按 90 格坐标取子，空返回 Empty。
func (p *Position) PieceAt90(sq90 int) byte { return p.Board[mailbox256[sq90]] }

// KingSquare 返回 color 方将帅的 256 下标。
func (p *Position) KingSquare(color int) int { return p.kingSq[color>>3] }

// InCheck 判断 color 方是否被将军（含将帅照面）。
func (p *Position) InCheck(color int) bool {
	return p.isAttacked(p.kingSq[color>>3], Opponent(color))
}

// Make 走一步（伪合法即可），压入历史栈。返回被吃子（可能为 Empty）。
func (p *Position) Make(m Move) byte {
	captured := p.Board[m.To]
	mover := p.Board[m.From]
	hi := histEntry{move: m, captured: captured, key: p.Key, halfmove: p.Halfmove, fullmove: p.Fullmove}

	p.Key ^= pieceKeys[mover][m.From] ^ pieceKeys[mover][m.To] ^ sideKey
	if captured != Empty {
		p.Key ^= pieceKeys[captured][m.To]
	}
	p.Board[m.To] = mover
	p.Board[m.From] = Empty
	if TypeOf(mover) == King {
		p.kingSq[ColorOf(mover)>>3] = int(m.To)
	}
	if captured != Empty {
		p.Halfmove = 0
	} else {
		p.Halfmove++
	}
	if p.Turn == Black {
		p.Fullmove++
	}
	p.Turn = Opponent(p.Turn)
	p.hist = append(p.hist, hi)
	return captured
}

// Unmake 回退最后一步。
func (p *Position) Unmake() {
	n := len(p.hist) - 1
	hi := p.hist[n]
	p.hist = p.hist[:n]

	p.Turn = Opponent(p.Turn)
	mover := p.Board[hi.move.To]
	p.Board[hi.move.From] = mover
	p.Board[hi.move.To] = hi.captured
	if TypeOf(mover) == King {
		p.kingSq[ColorOf(mover)>>3] = int(hi.move.From)
	}
	p.Key = hi.key
	p.Halfmove = hi.halfmove
	p.Fullmove = hi.fullmove
}

// LastMove 返回最近一步；无历史返回 false。
func (p *Position) LastMove() (Move, bool) {
	if len(p.hist) == 0 {
		return Move{}, false
	}
	return p.hist[len(p.hist)-1].move, true
}

// MoveCount 已走着数（半回合）。
func (p *Position) MoveCount() int { return len(p.hist) }

// KeyHistory 返回历史局面键（含当前），用于重复检测。
func (p *Position) KeyHistory() []uint64 {
	keys := make([]uint64, 0, len(p.hist)+1)
	for _, h := range p.hist {
		keys = append(keys, h.key)
	}
	keys = append(keys, p.Key)
	return keys
}

// HistoryMoves 返回已走着法序列（UCI 顺序）。
func (p *Position) HistoryMoves() []Move {
	out := make([]Move, 0, len(p.hist))
	for _, h := range p.hist {
		out = append(out, h.move)
	}
	return out
}
