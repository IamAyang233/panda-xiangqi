package game

import "os"

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

// GivesCheck 返回「走 m 之后对方是否被将军」—— **不改变局面**。
//
// 存在的理由：调用方要在**落子之前**知道这一步是否将军，好把「将军豁免」写进剪枝
// 条件（否则被剪掉的马法也要白付一次 Make/Unmake；本项目实测那占 d12 搜索的 9.5%）。
// 皮卡鱼的 `pos.gives_check(move)` 同样是落子前算的。
//
// 语义严格等于「Make(m) 之后 InCheck(p.Turn)」，由 TestGivesCheckMatchesInCheck
// 逐局面逐着法守护（3600+ 局面、12 万+ 着法，其中 1700+ 个真将军）。
//
// ⚠️ **前置条件：局面合法** —— 即「非行棋方没有被将军」。这不是额外要求，
// 而是合法局面的定义，也是本实现能快起来的关键：
//
//	搜索只走 LegalMoves（它保证走子后自己的将不被攻击）⇒ 上一步的走子方
//	走完之后自己的将不可能被攻击 ⇒ 任何节点上「非行棋方」都不会被将军。
//
// 有了它，就只需要考虑「落子后会新出现的将军」，而这只有两种来源：
//
//	① 移动的那颗子到了 to 之后能打到 ksq（马、兵与占用无关或不在射线上，单独判）；
//	② 我方**其它子**的攻击因为占用变了而新通 —— 占用只在 from、to 两格变化，
//	   所以只有**经过 from 或 to 的射线**才可能变化；其余射线上的一切
//	   （占用、棋子集合）都与落子前逐位相同，而落子前必为否 ⇒ 可以整条跳过。
//
// 旧实现是把占用与五类攻击子按落子后的样子全部构造出来，再对四条射线做一次
// 完整查询（4 次 firstBlockerAt + 至多 8 项马循环 + 兵查询）。
//
// ⚠️ 收益没有想象中大，别照着小标题估：profile 里 `game.attackedBySet` 占 3.3%
// （2.1% flat），但**它是 InCheck 与 GivesCheck 共用的**（InCheck 在每节点与
// qsearch 里各调一次），能算在 GivesCheck 头上的只是一部分；而「跳过不含
// from/to 的射线」本身要先付两次 `ray.Test`（16 字节掩码的载入+测试），
// 与跳过省下的 firstBlockerAt 大致相抵。同进程交替 A/B 六轮实得 **+0.7%**
// （中位 0.993，6 轮里 5 轮为快）。
//
// 换言之：这条快路径的价值更多在「结构更直白、少构造 5 个集合」，
// 而不是一个可观的百分比 —— 若要再拿，得先把射线跳过那两次 Test 换成更便宜
// 的对齐判定（按行/列/斜线的 line id 查表），但那是另一件事。
//
// ⚠️ 三条跳过规则都必须与「合法局面」这条前提一起看，缺一条就会漏将军：
//   - 跳过不含 from/to 的射线：靠前提（落子前必为否）；
//   - 跳过兵：兵的攻击与占用无关，落子前后同一批兵 ⇒ 靠前提；
//   - 跳过腿格未变的马：同理。
//
// GivesCheck 若被用在**不合法**的局面（例如自己捏一个对方已被将军的 FEN 去调），
// 结果可能偏 false。搜索与守卫测试都不在这个前提之外使用它。
func (p *Position) GivesCheck(m Move) bool {
	if bruteGivesCheck {
		return p.givesCheckBrute(m)
	}
	from, to := int(m.From), int(m.To)
	moverType := TypeOf(p.Board[from])
	ourIdx := p.Turn >> 3
	oppIdx := 1 - ourIdx
	ksq := p.kingSq[oppIdx]

	occ := p.bb.occ
	occ.Clear(from)
	occ.Set(to)

	// ---- ① 兵 ----
	// 兵的攻击只取决于自己的位置，与占用无关。
	if moverType == Pawn {
		if pawnAttackers[ourIdx][ksq].Test(to) {
			return true
		}
	}

	// ---- ② 从 ksq 出发的四条射线 ----
	// 集合按「落子后」的样子取：只有移动的那一类要改（from 清位、to 置位）。
	our := p.bb.byColor[ourIdx]
	rooks := p.bb.byType[Rook].And(our)
	kings := p.bb.byType[King].And(our)
	cannons := p.bb.byType[Cannon].And(our)
	switch moverType {
	case Rook:
		rooks.Clear(from)
		rooks.Set(to)
	case King:
		kings.Clear(from)
		kings.Set(to)
	case Cannon:
		cannons.Clear(from)
		cannons.Set(to)
	}
	if !rooks.IsEmpty() || !kings.IsEmpty() || !cannons.IsEmpty() {
		for d := 0; d < 4; d++ {
			ray := rayMask[ksq][d]
			// 这条射线上没有任何东西变过 ⇒ 落子前必为否，整条跳过。
			if !ray.Test(from) && !ray.Test(to) {
				continue
			}
			// 直接调 firstBlockerAt（不要经包装函数）：包装函数会把 firstBlockerAt
			// 内联进去从而自己超出内联预算（cost 88 > 80）。
			fb := firstBlockerAt(occ, ray, d)
			if fb < 0 {
				continue
			}
			if rooks.Test(fb) {
				return true
			}
			if (d == dirUp || d == dirDown) && kings.Test(fb) {
				return true // 将帅照面
			}
			if !cannons.IsEmpty() {
				if fb2 := firstBlockerAt(occ, rayMask[fb][d], d); fb2 >= 0 && cannons.Test(fb2) {
					return true // 隔一子翻山
				}
			}
		}
	}

	// ---- ③ 马 ----
	// 马不在射线上，单独判。落子后可能出现的新将军只有两种：
	//   - 起点就是 to（移动的那颗子是马，它到了新位置）；
	//   - 腿格是 from（原来挡着我方某匹马的正是这颗走掉的子，现在通了）。
	// 其余情形（起点与腿格都没变）与落子前逐位相同 ⇒ 靠前提必为否，跳过。
	//
	// ⚠️ 腿格是 to 的马反而被堵死，不可能形成将军，所以不需要单独处理。
	horses := p.bb.byType[Horse].And(our)
	if !horses.IsEmpty() || moverType == Horse {
		for _, ka := range knightAttackers[ksq] {
			if ka.origin < 0 {
				break
			}
			if ka.origin == from {
				// from 上的子已经走了：horses 是按落子前取的，这一项是陈旧的。
				continue
			}
			if ka.leg != from && ka.origin != to {
				continue
			}
			hasHorse := moverType == Horse && ka.origin == to
			if ka.origin != to {
				hasHorse = horses.Test(ka.origin)
			}
			if hasHorse && !occ.Test(ka.leg) {
				return true
			}
		}
	}
	return false
}

// bruteGivesCheck 由 QIJING_GCBRUTE=on 置位，把 GivesCheck 切回旧的全量实现。
//
// 留着的理由有三个：
//  1. **配对 A/B**：这台机器有快慢相，跨进程比较不可靠，必须在同一份二进制里
//     逐轮切换两态（与 QIJING_PSQCACHE / QIJING_FUSEROWS 同一个用法）。
//  2. **差分守卫**：TestGivesCheckMatchesInCheck 除了与「Make 之后 InCheck」
//     这条真值对拍，还让两条实现互相印证 —— 两种形状各错一次还刚好错得一样的
//     概率很低。
//  3. **运维排查**：怀疑新路径在某个局面漏了将军时，一行开关换回旧路径看现象
//     是否消失。
var bruteGivesCheck = func() bool {
	v, ok := os.LookupEnv("QIJING_GCBRUTE")
	return ok && (v == "on" || v == "1")
}()

// SetBruteGivesCheck 切换两条实现并返回原值，供测试与基准使用。
func SetBruteGivesCheck(on bool) bool {
	old := bruteGivesCheck
	bruteGivesCheck = on
	return old
}

// givesCheckBrute 是旧的全量实现：把占用与五类攻击子按落子后的样子全部构造出来，
// 再对四条射线做一次完整查询，由 attackedBySet 回答。
//
// 它不依赖「合法局面」这条前提，因此在任何局面下都直接可用 —— 这正是留着它
// 当参考的价值：新实现的三条跳过规则全都建立在那条前提上（见 GivesCheck 的说明）。
func (p *Position) givesCheckBrute(m Move) bool {
	from, to := int(m.From), int(m.To)
	moverType := TypeOf(p.Board[from])
	ourIdx := p.Turn >> 3
	oppIdx := 1 - ourIdx

	occ := p.bb.occ
	occ.Clear(from)
	occ.Set(to)

	setOf := func(t int) Bitboard {
		b := p.bb.byType[t].And(p.bb.byColor[ourIdx])
		b.Clear(from)
		if t == moverType {
			b.Set(to)
		}
		return b
	}
	return attackedBySet(occ, p.kingSq[oppIdx], ourIdx,
		setOf(Rook), setOf(King), setOf(Cannon), setOf(Horse), setOf(Pawn))
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
