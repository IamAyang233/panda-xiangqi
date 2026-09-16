package game

// 合法性检查的快速路径。
//
// 原写法（见 movegen.go 的 LegalMoves）是对每个伪合法着法做一次
// Make → InCheck → Unmake。问题在于 Make 内部还会算一次「对方是否被将军」
// （长将判定用的 histEntry.check），那是**第二次完整的王安全扫描**，而合法性
// 判断只需要「己方王是否被攻击」。
//
// 实测（固定 6 万节点搜索）：每个节点平均 19.4 次王安全扫描，其中 14.5 次来自
// 这条路径 —— 占全部扫描的 75%。每次检验着法还要连带一次 Make/Unmake。
//
// 这里改成**完全不动局面**的试走判定：只按 from/to 调整占用集与被吃子，直接问
// 「试走之后 sq 是否被攻击」。语义与「Make 之后调 isAttackedBB」严格等价，
// 省掉一次扫描与整对 Make/Unmake。

// isAttackedAfter 判断「from 处的子走到 to、吃掉 captured」之后，sq 是否被 by 方攻击。
// 语义与「Make 之后调 isAttackedBB」严格等价。
//
// 与 isAttackedBB 的差别只有两处：
//   - 占用集用试走后的（from 清空、to 置位）—— 王走开可能放开一条射线，
//     或堵住一条射线；
//   - 若被吃的是敌子，把它从敌方的类型掩码里去掉 —— 否则「吃车解将」这一步会被
//     刚被吃掉的車误判成仍然受攻。
//
// 与 isAttackedBB 一样不覆盖仕/象：己方王在己方九宫里，对方的仕过不了河、
// 象也在半个棋盘之外，两者永远攻击不到它。
func (b *BB) isAttackedAfter(sq, by, from, to int, captured byte) bool {
	ts := by >> 3
	occ := b.occ
	occ.Clear(from)
	occ.Set(to)

	rooks := b.byType[Rook].And(b.byColor[ts])
	kings := b.byType[King].And(b.byColor[ts])
	cannons := b.byType[Cannon].And(b.byColor[ts])
	horses := b.byType[Horse].And(b.byColor[ts])
	pawns := b.byType[Pawn].And(b.byColor[ts])
	if captured != Empty && ColorOf(captured) == by {
		switch TypeOf(captured) {
		case Rook:
			rooks.Clear(to)
		case King:
			kings.Clear(to)
		case Cannon:
			cannons.Clear(to)
		case Horse:
			horses.Clear(to)
		case Pawn:
			pawns.Clear(to)
		}
	}

	if !rooks.IsEmpty() || !kings.IsEmpty() || !cannons.IsEmpty() {
		for d := 0; d < 4; d++ {
			fb := firstBlockerAt(occ, sq, d)
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
				if fb2 := firstBlockerAt(occ, fb, d); fb2 >= 0 && cannons.Test(fb2) {
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

	if !pawns.IsEmpty() && !pawnAttackers[ts][sq].And(pawns).IsEmpty() {
		return true
	}
	return false
}
