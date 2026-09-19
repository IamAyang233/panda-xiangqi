package game

import "math/bits"

// 位棋盘攻击表与反向攻击探测（M1）。
// 全部表在 init 中预计算，运行时只做位运算，不再按方向逐格步进。

// 四个正交方向。dirUp/dirRight 使格索引增大（取最低位即最近格），
// dirDown/dirLeft 使格索引减小（取最高位即最近格）。
const (
	dirUp = iota
	dirDown
	dirLeft
	dirRight
)

var (
	// rayMask[sq][dir] 从 sq 出发沿 dir 的全部格子（不含 sq，已裁剪到盘内）。
	rayMask [bbSquares][4]Bitboard

	// knightTargets[sq] 马从 sq 可到的全部格（未考虑蹩腿）。
	knightTargets [bbSquares]Bitboard
	// knightSteps[sq] 正向表：马从 sq 到 to 需要 leg 为空（to<0 表示表项未用）。
	knightSteps [bbSquares][8]struct{ to, leg int }
	// knightAttackers[sq] 能攻击 sq 的（马位, 蹩腿点），origin<0 表示表项未用。
	knightAttackers [bbSquares][8]struct{ origin, leg int }

	// kingMoves / advisorMoves / elephantSteps 按红黑分侧（九宫、河界不同）。
	kingMoves      [2][bbSquares]Bitboard
	advisorMoves   [2][bbSquares]Bitboard
	elephantSteps  [2][bbSquares][4]struct{ to, eye int }
	elephantTarget [2][bbSquares]Bitboard

	// pawnMoves[color][sq] color 方兵从 sq 可走的格。
	pawnMoves [2][bbSquares]Bitboard
	// pawnAttackers[color][sq] 能攻击 sq 的 color 方兵所在格。
	pawnAttackers [2][bbSquares]Bitboard
)

func init() {
	buildRays()
	buildKnight()
	buildPalace()
	buildElephant()
	buildPawn()
}

func buildRays() {
	for sq := 0; sq < bbSquares; sq++ {
		f, r := bbFile(sq), bbRank(sq)
		var m Bitboard
		for rr := r + 1; rr <= 9; rr++ {
			m.Set(bbSquare(f, rr))
		}
		rayMask[sq][dirUp] = m
		m = Bitboard{}
		for rr := r - 1; rr >= 0; rr-- {
			m.Set(bbSquare(f, rr))
		}
		rayMask[sq][dirDown] = m
		m = Bitboard{}
		for ff := f + 1; ff <= 8; ff++ {
			m.Set(bbSquare(ff, r))
		}
		rayMask[sq][dirRight] = m
		m = Bitboard{}
		for ff := f - 1; ff >= 0; ff-- {
			m.Set(bbSquare(ff, r))
		}
		rayMask[sq][dirLeft] = m
	}
}

func buildKnight() {
	for i := range knightAttackers {
		for j := range knightAttackers[i] {
			knightAttackers[i][j].origin = -1
			knightAttackers[i][j].leg = -1
			knightSteps[i][j].to = -1
			knightSteps[i][j].leg = -1
		}
	}
	for sq := 0; sq < bbSquares; sq++ {
		f, r := bbFile(sq), bbRank(sq)
		for _, m := range knightTbl {
			to := bbSquare(f+m.df, r+m.dr)
			if to < 0 {
				continue
			}
			leg := bbSquare(f+m.lf, r+m.lr)
			knightTargets[sq].Set(to)
			for k := range knightSteps[sq] {
				if knightSteps[sq][k].to < 0 {
					knightSteps[sq][k] = struct{ to, leg int }{to, leg}
					break
				}
			}
			// 反向登记：马在 sq、以 leg 为蹩腿点时可攻击 to。
			for k := range knightAttackers[to] {
				if knightAttackers[to][k].origin < 0 {
					knightAttackers[to][k] = struct{ origin, leg int }{sq, leg}
					break
				}
			}
		}
	}
}

func buildPalace() {
	for color := 0; color < 2; color++ {
		c := Red
		if color == 1 {
			c = Black
		}
		for sq := 0; sq < bbSquares; sq++ {
			f, r := bbFile(sq), bbRank(sq)
			var k, a Bitboard
			for _, d := range [4][2]int{{0, 1}, {0, -1}, {1, 0}, {-1, 0}} {
				to := bbSquare(f+d[0], r+d[1])
				if to >= 0 && inPalace(c, bbFile(to), bbRank(to)) {
					k.Set(to)
				}
			}
			for _, d := range [4][2]int{{1, 1}, {1, -1}, {-1, 1}, {-1, -1}} {
				to := bbSquare(f+d[0], r+d[1])
				if to >= 0 && inPalace(c, bbFile(to), bbRank(to)) {
					a.Set(to)
				}
			}
			kingMoves[color][sq] = k
			advisorMoves[color][sq] = a
		}
	}
}

func buildElephant() {
	for color := 0; color < 2; color++ {
		c := Red
		if color == 1 {
			c = Black
		}
		for sq := 0; sq < bbSquares; sq++ {
			f, r := bbFile(sq), bbRank(sq)
			for i := range elephantSteps[color][sq] {
				elephantSteps[color][sq][i].to = -1
				elephantSteps[color][sq][i].eye = -1
			}
			// 本表只收录「象站在合法位置时的着法」，所以起点与落点都要求
			// 在己方半场。少判起点的后果不只是脏数据：本表会被 see.go 的
			// attackersTo **反向**当「能攻击该格的象位」查，而只判落点会让
			// 正反两次查表不互为逆 —— 对岸的象带出己方半场落点，反向就查成
			// 「红象攻击黑方半场」的假攻击者（实测 8/1741 处与朴素 SEE 分歧）；
			// 同时又被摆到对岸的象仍会生成着法，两边都说不清。
			if !ownSide(c, r) {
				continue
			}
			for i, d := range [4][2]int{{2, 2}, {2, -2}, {-2, 2}, {-2, -2}} {
				to := bbSquare(f+d[0], r+d[1])
				if to < 0 || !ownSide(c, bbRank(to)) { // 象不过河
					continue
				}
				eye := bbSquare(f+d[0]/2, r+d[1]/2)
				elephantSteps[color][sq][i] = struct{ to, eye int }{to, eye}
				elephantTarget[color][sq].Set(to)
			}
		}
	}
}

func buildPawn() {
	for color := 0; color < 2; color++ {
		c := Red
		if color == 1 {
			c = Black
		}
		for sq := 0; sq < bbSquares; sq++ {
			f, r := bbFile(sq), bbRank(sq)
			var targets Bitboard
			// 前进一格
			dr := 1
			if c == Black {
				dr = -1
			}
			if to := bbSquare(f, r+dr); to >= 0 {
				targets.Set(to)
			}
			// 过河后可横走
			if !ownSide(c, r) {
				if to := bbSquare(f-1, r); to >= 0 {
					targets.Set(to)
				}
				if to := bbSquare(f+1, r); to >= 0 {
					targets.Set(to)
				}
			}
			pawnMoves[color][sq] = targets
			// 反向登记：兵在 sq 能攻击 t → 把 sq 记入 pawnAttackers[t]。
			tmp := targets
			for {
				t := tmp.popLSB()
				if t < 0 {
					break
				}
				pawnAttackers[color][t].Set(sq)
			}
		}
	}
}

// BB 位棋盘视图：按颜色/类型的子力分布与总占用。
type BB struct {
	byColor [2]Bitboard
	byType  [8]Bitboard
	occ     Bitboard
}

// newBBFromBoard 从 16×16 mailbox 构造位棋盘视图（M2 后改由 Position 直接维护）。
func newBBFromBoard(board *[256]byte) *BB {
	b := &BB{}
	for sq := 0; sq < bbSquares; sq++ {
		pc := board[mailbox256[sq]]
		if pc == Empty || pc == Edge {
			continue
		}
		b.byColor[ColorOf(pc)>>3].Set(sq)
		b.byType[TypeOf(pc)].Set(sq)
		b.occ.Set(sq)
	}
	return b
}

// firstBlockerAt 从 sq 沿 dir 遇到的第一个子；无子返回 -1。
// ray 是 rayMask[sq][dir]，由调用方传入 —— 它本来就要用，省掉一次二维索引。
//
// 三点写法上的讲究，都是为了让本函数**能被内联**（它在 9 个调用点上都没进得去，
// 而调用密度高达 44.65 次/节点，profile 里 flat 占比全项目第 4）：
//  1. 不写 `if m.IsEmpty() { return -1 }`：lsb/msb 在空盘时本来就返回 -1；
//  2. 手工展开 And，少一层中间变量；
//  3. `(dir+1)&2 == 0` 一次比较顶替 `dir == dirUp || dir == dirRight`
//     （dirUp=0/dirRight=3 为真，dirDown=1/dirLeft=2 为假）。
func firstBlockerAt(occ, ray Bitboard, dir int) int {
	lo, hi := ray[0]&occ[0], ray[1]&occ[1]
	if (dir+1)&2 == 0 {
		if lo != 0 {
			return bits.TrailingZeros64(lo)
		}
		if hi != 0 {
			return 64 + bits.TrailingZeros64(hi)
		}
		return -1
	}
	if hi != 0 {
		return 127 - bits.LeadingZeros64(hi)
	}
	return 63 - bits.LeadingZeros64(lo)
}

// rookAttacks 车从 sq 的攻击集（含可吃敌子，不含己方子）。
func rookAttacks(sq int, occ, own Bitboard) Bitboard {
	var out Bitboard
	for d := 0; d < 4; d++ {
		ray := rayMask[sq][d]
		fb := firstBlockerAt(occ, ray, d)
		if fb < 0 {
			out = out.Or(ray)
			continue
		}
		// rayMask[fb][d] 不含 fb 本身，故还需显式排除 fb。
		seg := ray.AndNot(rayMask[fb][d])
		seg.Clear(fb)
		out = out.Or(seg)
		if !own.Test(fb) {
			out.Set(fb) // 敌子可吃
		}
	}
	return out
}

// cannonAttacks 炮从 sq 的（平走空格集, 翻山可吃集）。
func cannonAttacks(sq int, occ, own Bitboard) (quiet, capture Bitboard) {
	for d := 0; d < 4; d++ {
		ray := rayMask[sq][d]
		fb := firstBlockerAt(occ, ray, d)
		if fb < 0 {
			quiet = quiet.Or(ray)
			continue
		}
		seg := ray.AndNot(rayMask[fb][d])
		seg.Clear(fb) // 炮架之前的空段，不含炮架本身
		quiet = quiet.Or(seg)
		if fb2 := firstBlockerAt(occ, rayMask[fb][d], d); fb2 >= 0 && !own.Test(fb2) {
			capture.Set(fb2) // 越炮架后第一个敌子
		}
	}
	return quiet, capture
}

// isAttackedBB sq 是否被 by 方攻击（语义与 mailbox 版 isAttacked 一致，
// 仅覆盖将帅安全所需的类型：车、纵向照面、炮、马含蹩腿、兵）。
func (b *BB) isAttackedBB(sq int, by int) bool {
	side := by >> 3 // Red=0 / Black=1
	return attackedBySet(b.occ, sq, side,
		b.byType[Rook].And(b.byColor[side]),
		b.byType[King].And(b.byColor[side]),
		b.byType[Cannon].And(b.byColor[side]),
		b.byType[Horse].And(b.byColor[side]),
		b.byType[Pawn].And(b.byColor[side]))
}

// attackedBySet 是 isAttackedBB 的底层形式：占用与五类攻击子的集合由调用方给出。
//
// 存在的理由见 Position.GivesCheck —— 那里要在**落子之前**回答「走这步之后对方
// 是否被将军」，办法是把占用与集合按落子后的样子预先构造出来，而不是真的落子再回滚。
//
// ⚠️ 五个集合必须是**该方**（side）对应兵种的集合，且与 occ 描述的是同一个局面。
func attackedBySet(occ Bitboard, sq, side int,
	rooks, kings, cannons, horses, pawns Bitboard) bool {
	if !rooks.IsEmpty() || !kings.IsEmpty() || !cannons.IsEmpty() {
		for d := 0; d < 4; d++ {
			// 直接调 firstBlockerAt（不要经包装函数）：包装函数会把 firstBlockerAt
			// 内联进去从而自己超出内联预算（cost 88 > 80），那两处就又变成真调用了。
			ray := rayMask[sq][d]
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

	if !horses.IsEmpty() {
		for _, ka := range knightAttackers[sq] {
			if ka.origin < 0 {
				break
			}
			if horses.Test(ka.origin) && !occ.Test(ka.leg) {
				return true
			}
		}
	}

	if !pawns.IsEmpty() && !pawnAttackers[side][sq].And(pawns).IsEmpty() {
		return true
	}
	return false
}
