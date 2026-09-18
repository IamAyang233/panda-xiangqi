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
			fb := firstBlockerAt(occ, rayMask[sq][d], d)
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

	if !pawns.IsEmpty() && !pawnAttackers[ts][sq].And(pawns).IsEmpty() {
		return true
	}
	return false
}

// bbSingle 只含 sq 一个格的位棋盘。
func bbSingle(sq int) Bitboard {
	var b Bitboard
	b.Set(sq)
	return b
}

// kingSafety 一次扫描同时给出「us 方王是否被将军」与「危险格集合」。
//
// 危险格 = **改动该格占用就可能改变 ksq 安危**的格。未被将军时，一个非王的着法
// 只要 from 与 to 都不在危险格里，就不可能让己方王陷入被攻 —— 于是合法着法生成
// 可以把绝大多数着法的完整攻击扫描省掉（实测约 33 次扫描/调用 → 约 5 次）。
//
// 为什么危险格这么小（只到前两个阻挡为止）：如果要把 ksq 的攻击状态变坏，
// 单步改动只能做两件事 —— 抽掉一个阻挡，或落下一个阻挡。逐类推：
//
//   - 车/将帅照面：攻击 ksq 当且仅当「它到 ksq 之间没有子」。抽掉一个阻挡只在
//     原本只剩 1 个阻挡时才可能暴露它 → 那个子就是第一个阻挡 p1，且威胁子必须
//     正好是 p2。抽掉 p2 或更远的子都还剩 p1 挡着。落子只会增加阻挡 ✓ 无害。
//   - 炮：攻击当且仅当「它到 ksq 之间恰好 1 个子」。抽掉一个阻挡让计数减一，
//     于是只有原本恰好 2 个阻挡的炮会被造出来 → 该炮在 p3，抽 p1 或 p2 都会命中。
//     落子让计数加一，只有原本 0 个阻挡的炮（即该炮就是 p1）会被造出来 ——
//     此时往 ksq 与 p1 之间任意空格落子都会让它够到王。
//   - 马：攻击与否只看蹩腿格 → 敌方马的腿位都可能因为被抽空而成攻。
//   - 兵：攻击与占用无关（只可能被吃，那只会变安全）→ 不贡献危险格。
//   - 仕/象：过不了河/出不了九宫之外，够不到对方王 → 不参与。
//
// 这套推导建立在「当前未被将军」之上：被将军时上面的「原本只剩 1 个阻挡」等
// 前提都不成立，所以调用方必须只在未被将军时使用危险格筛选。
func (b *BB) kingSafety(ksq, us int) (inCheck bool, danger Bitboard) {
	them := Opponent(us)
	ts := them >> 3
	tside := b.byColor[ts]
	rooks := b.byType[Rook].And(tside)
	kings := b.byType[King].And(tside)
	cannons := b.byType[Cannon].And(tside)
	horses := b.byType[Horse].And(tside)
	pawns := b.byType[Pawn].And(tside)

	for d := 0; d < 4; d++ {
		ray := rayMask[ksq][d]
		inc := d == dirUp || d == dirRight // 格索引递增的方向用 lsb
		// ⚠️ 别把 inc 当「纵向」：inc 说的是格索引增减（北/东），而照面只在
		// 纵线（北/南）上成立。两者只有一个方向重合，混用会漏掉照面将军。
		vertical := d == dirUp || d == dirDown
		// 取该射线上最靠 ksq 的三个子（p1/p2/p3），越界为 -1。
		m := ray.And(b.occ)
		var p [3]int
		for i := range p {
			p[i] = -1
			if m.IsEmpty() {
				continue
			}
			if inc {
				p[i] = m.lsb()
			} else {
				p[i] = m.msb()
			}
			m.Clear(p[i])
		}
		p1, p2, p3 := p[0], p[1], p[2]
		if p1 < 0 {
			continue
		}
		if rooks.Test(p1) {
			inCheck = true
		}
		// 将帅照面只在纵线上成立。
		if vertical && kings.Test(p1) {
			inCheck = true
		}
		if p2 >= 0 && cannons.Test(p2) {
			inCheck = true // 恰好 1 个炮架 → 隔山打王
		}

		if cannons.Test(p1) {
			// 0 个炮架的炮：往它与王之间落子会造成将军。
			danger = danger.Or(ray.AndNot(rayMask[p1][d]).AndNot(bbSingle(p1)))
		}
		if p2 < 0 {
			continue
		}
		rookLike2 := rooks.Test(p2) || (vertical && kings.Test(p2))
		cannon3 := p3 >= 0 && cannons.Test(p3)
		if rookLike2 || cannon3 {
			danger.Set(p1)
		}
		if cannon3 {
			danger.Set(p2)
		}
	}

	// 马：腿位为空时它正在将军；腿位被占时，抽空那个子就会让将军成真。
	// 两种情况都要处理 —— 前者属于「是否被将军」，后者属于危险格。
	// ⚠️ 漏掉前者的症状是「被马的将军误判成未被将军」，于是危险格筛选被错误启用、
	// 一次放过大量非法着法（TestLegalMovesMatchRef 当场抓到过）。
	for _, ka := range knightAttackers[ksq] {
		if ka.origin < 0 {
			break
		}
		if horses.Test(ka.origin) {
			if !b.occ.Test(ka.leg) {
				inCheck = true
			}
			danger.Set(ka.leg)
		}
	}

	if !pawns.IsEmpty() && !pawnAttackers[ts][ksq].And(pawns).IsEmpty() {
		inCheck = true
	}
	return inCheck, danger
}
