package game

// InitialFEN 标准初始局面（附录 B）。
const InitialFEN = "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"

// Move 着法：From/To 为 90 格索引（0~89，sq = rank*9+file）。
type Move struct {
	From, To uint8
}

// String 返回 UCI 坐标串，如 "h2e2"。
func (m Move) String() string { return SquareName(m.From) + SquareName(m.To) }

// SquareName 把 90 格索引转为 UCI 格子名（如 "h2"）。
func SquareName(sq uint8) string {
	return string(rune('a'+bbFile(int(sq)))) + string(rune('0'+bbRank(int(sq))))
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
	return uint8(bbSquare(f, r)), true
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
	move     Move
	captured byte
	key      uint64
	halfmove int
	fullmove int
	color    int  // 走子方 Red/Black
	check    bool // 走完后对方被将军（长将判定用）
	null     bool // 空着（搜索用），不计入重复检测
}

// Position 一局局面。非并发安全；上层需自行加锁。
//
// Board 按 90 格索引保存"格→棋子"，供取子与中文记谱 O(1) 使用；
// bb 是同一局面的位棋盘视图，供攻击判定与走法生成使用。两者由
// Make/Unmake 与 ParseFEN 同步维护，任何直接改写都必须成对更新。
type Position struct {
	Board    [90]byte
	bb       BB
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

// setPiece 在 sq 放置 pc 并同步位棋盘（调用前该格须已清空）。
func (p *Position) setPiece(sq int, pc byte) {
	p.Board[sq] = pc
	if pc == Empty {
		return
	}
	p.bb.byColor[ColorOf(pc)>>3].Set(sq)
	p.bb.byType[TypeOf(pc)].Set(sq)
	p.bb.occ.Set(sq)
}

// clearPiece 清空 sq 并同步位棋盘。
func (p *Position) clearPiece(sq int) {
	pc := p.Board[sq]
	if pc == Empty {
		return
	}
	p.bb.byColor[ColorOf(pc)>>3].Clear(sq)
	p.bb.byType[TypeOf(pc)].Clear(sq)
	p.bb.occ.Clear(sq)
	p.Board[sq] = Empty
}

// PieceAt90 按 90 格坐标取子，空返回 Empty。
func (p *Position) PieceAt90(sq90 int) byte { return p.Board[sq90] }

// PieceAt 取 sq（90 格索引）处棋子，空返回 Empty。
func (p *Position) PieceAt(sq int) byte { return p.Board[sq] }

// KingSquare 返回 color 方将帅的 90 格索引。
func (p *Position) KingSquare(color int) int { return p.kingSq[color>>3] }

// InCheck 判断 color 方是否被将军（含将帅照面）。
func (p *Position) InCheck(color int) bool {
	return p.bb.isAttackedBB(p.kingSq[color>>3], Opponent(color))
}

// Make 走一步（伪合法即可），压入历史栈。返回被吃子（可能为 Empty）。
func (p *Position) Make(m Move) byte {
	from, to := int(m.From), int(m.To)
	captured := p.Board[to]
	mover := p.Board[from]
	hi := histEntry{move: m, captured: captured, key: p.Key, halfmove: p.Halfmove, fullmove: p.Fullmove, color: ColorOf(mover)}

	// Zobrist 沿用 256 下标键位，保证与历史存档/置换表逐位兼容。
	kf, kt := mailbox256[from], mailbox256[to]
	p.Key ^= pieceKeys[mover][kf] ^ pieceKeys[mover][kt] ^ sideKey
	if captured != Empty {
		p.Key ^= pieceKeys[captured][kt]
	}
	p.clearPiece(to)
	p.clearPiece(from)
	p.setPiece(to, mover)
	if TypeOf(mover) == King {
		p.kingSq[ColorOf(mover)>>3] = to
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
	hi.check = p.InCheck(p.Turn) // 走完后新轮走方被将军 → 刚走的这步是将军
	p.hist = append(p.hist, hi)
	return captured
}

// Unmake 回退最后一步。
func (p *Position) Unmake() {
	n := len(p.hist) - 1
	hi := p.hist[n]
	p.hist = p.hist[:n]

	p.Turn = Opponent(p.Turn)
	from, to := int(hi.move.From), int(hi.move.To)
	mover := p.Board[to]
	p.clearPiece(to)
	p.setPiece(from, mover)
	if hi.captured != Empty {
		p.setPiece(to, hi.captured)
	}
	if TypeOf(mover) == King {
		p.kingSq[ColorOf(mover)>>3] = from
	}
	p.Key = hi.key
	p.Halfmove = hi.halfmove
	p.Fullmove = hi.fullmove
}

// MakeNull 走一步搜索用的"空着"：子力不动，只切换走子方并压栈。
//
// 供 null-move 剪枝使用。调用方必须先确认当前走子方未被将军 ——
// 否则空着后对方可一步吃将，局面语义不成立。中国象棋不允许一方连走，
// 所以空着只存在于搜索树中，绝不能进入真实对局记录。
func (p *Position) MakeNull() {
	hi := histEntry{
		key:      p.Key,
		halfmove: p.Halfmove,
		fullmove: p.Fullmove,
		color:    p.Turn,
		null:     true,
	}
	p.Key ^= sideKey
	p.Halfmove = 0 // 视作不可逆，避免 60 回合判和误伤
	if p.Turn == Black {
		p.Fullmove++
	}
	p.Turn = Opponent(p.Turn)
	p.hist = append(p.hist, hi)
}

// UnmakeNull 回退空着。
func (p *Position) UnmakeNull() {
	n := len(p.hist) - 1
	hi := p.hist[n]
	p.hist = p.hist[:n]

	p.Turn = Opponent(p.Turn)
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

// LastCaptured 返回最近一步的被吃子（Empty 表示无吃子）。需在 Unmake 前调用，
// 因为 Make 时已把真实被吃子压入历史栈（而非走完后的落点棋子——那才是走子方自己）。
func (p *Position) LastCaptured() byte {
	if len(p.hist) == 0 {
		return Empty
	}
	return p.hist[len(p.hist)-1].captured
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
