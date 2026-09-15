package game

// 本文件实现静态交换评估（SEE）：给定一个吃子着法，估算「双方轮流吃回」之后
// 的净收益，用于丢弃明显亏损的吃子（坏吃子剪枝）与着法排序。
//
// 结构对照皮卡鱼 Position::see_ge。中国象棋有两处与国际象棋不同，都显式处理：
//   - 将帅照面：把将视为沿纵线可攻击对方将的「车」；
//   - 炮：攻击依赖炮架，移除任何子都可能改变炮的攻击集，故 nonCannons 与
//     cannons 分开维护，每次移除后重算。
//
// 与皮卡鱼一致的简化：只做「移除的子原本挡着的线路」这类增量更新，不做完全
// 重算；马/象的蹩腿与象眼只在移除仕时重查（这是皮卡鱼的做法，不是遗漏）。

// seeValue 是静态交换评估用的子力价值，索引为棋子类型（King..Pawn）。
// 与搜索层着法排序用的那张表同值 —— 两处必须一致，否则 SEE 的取舍与排序的
// 取舍会自相矛盾。
var seeValue = [8]int{
	0,     // 0：空
	10000, // King
	200,   // Advisor
	200,   // Elephant
	400,   // Horse
	900,   // Rook
	450,   // Cannon
	100,   // Pawn
}

// attackersTo 返回所有能攻击 sq 的格子（不分颜色）。
//
// occ 是「当前占用」而不是 b.occ —— SEE 会随交换推进逐步从中移除已被吃掉的子，
// 而 b.byType / b.byColor 始终反映棋子的原始归属（谁被吃掉了由调用方从结果里剔）。
func (b *BB) attackersTo(sq int, occ Bitboard) Bitboard {
	var out Bitboard

	// 车：车的攻击是对射对称的，所以「从 sq 正交可达的格」上的车就是攻击者。
	out = out.Or(rookAttacks(sq, occ, Bitboard{}).And(b.byType[Rook]))
	// 炮：攻击同样对称（两侧看同一个炮架），越炮架后第一个子上的炮即攻击者。
	_, cannonCap := cannonAttacks(sq, occ, Bitboard{})
	out = out.Or(cannonCap.And(b.byType[Cannon]))
	// 象：以 sq 为落点反查象位，象眼须为空。
	//
	// 这张表是按「起点 → 落点」正向构建的，这里靠关系的对称性反向使用
	// （象走两格对角，正反互为象步，象眼也同一个格）。因此有两条前提：
	// sq 必须在 c 方半场（象不过河，不可能攻击对岸的格），且攻击者必须是
	// c 方自己的象。两条都不是多余的 —— buildElephant 曾漏掉起点侧的
	// 半场判断，导致大象根本站不上的格也带出过河落点，反向查到就成了
	// 「红象攻击黑方半场」的假攻击者，SEE 会据此把亏损吃子判成不亏。
	for c := 0; c < 2; c++ {
		if !ownSide(c, bbRank(sq)) {
			continue
		}
		ownElephants := b.byType[Elephant].And(b.byColor[c])
		for _, es := range elephantSteps[c][sq] {
			if es.to >= 0 && !occ.Test(es.eye) && ownElephants.Test(es.to) {
				out.Set(es.to)
			}
		}
	}
	// 马：带蹩腿。knightAttackers 给的是「能马步到 sq 的位置」，必须再确认
	// 那个位置上确实是马 —— 漏掉这一步会让任何子（尤其是恰好落在马步位的兵）
	// 都被当成马攻击者，SEE 于是凭空多出交换链，把盈利的吃子判成亏损。
	for _, ka := range knightAttackers[sq] {
		if ka.origin < 0 {
			break
		}
		if !occ.Test(ka.leg) && b.byType[Horse].Test(ka.origin) {
			out.Set(ka.origin)
		}
	}
	// 仕、将、兵：这几张表都是**分色**的（第一维为颜色），所以除了类型，
	// 还必须同时匹配颜色 —— 只查类型会让「红兵能攻击 sq 的位置上恰好站着
	// 一个黑卒」被当成红兵攻击者，SEE 据此凭空多出一条反击线。
	//
	// 另一处陷阱：kingMoves/advisorMoves 构建时只检查了「目标格在宫殿内」
	// （即攻击者所在的位置），没检查起点也在宫殿内。所以对宫殿外的 sq，
	// 它们会给出「将/仕从宫里走出来吃子」这种非法着法，必须先确认 sq
	// 位于本方宫殿。走法生成（movegen.go）也用这两个表，但那里传入的必然
	// 是将在的位置、天然在宫殿内，因此不受这个语义缺口影响 —— 这也是
	// 没去改共享表的原因。
	//
	// 车/炮/象/马那几张表是不分色的（单表公用），类型检查就够了。
	sf, sr := bbFile(sq), bbRank(sq)
	for c := 0; c < 2; c++ {
		color := Red
		if c == 1 {
			color = Black
		}
		own := b.byColor[c]
		if inPalace(color, sf, sr) {
			out = out.Or(advisorMoves[c][sq].And(own).And(b.byType[Advisor]))
			out = out.Or(kingMoves[c][sq].And(own).And(b.byType[King]))
		}
		out = out.Or(pawnAttackers[c][sq].And(own).And(b.byType[Pawn]))
	}
	return out.And(occ)
}

// SeeGE 判断吃子 from→to 的静态交换净收益是否不低于 threshold。
//
// 返回 true 表示「吃子方在最坏情况下不吃亏」。剪枝用 threshold=0，
// 排序可用负值挑出更值得先搜的吃子。语义与皮卡鱼 Position::see_ge 一致。
//
// from 上必须是棋子，to 上可以为空（那就不算吃子，此时只有 threshold<=0 才返回 true）。
func (p *Position) SeeGE(from, to, threshold int) bool {
	if from < 0 || from >= bbSquares || to < 0 || to >= bbSquares {
		return false
	}
	b := &p.bb

	// 第一层：被吃子的价值减去门槛。为负说明连吃子本身都不够门槛。
	swap := seeValue[TypeOf(p.Board[to])] - threshold
	if swap < 0 {
		return false
	}
	// 第二层：我方动用的子若已不比 swap 大，吃完不亏，直接成立。
	swap = seeValue[TypeOf(p.Board[from])] - swap
	if swap <= 0 {
		return true
	}

	occupied := b.occ
	occupied.Clear(from)
	occupied.Clear(to)

	attackers := b.attackersTo(to, occupied)

	// 飞将：将沿纵线可「吃」照面的对方将，等同于车的攻击，需单独补一次。
	flying := !attackers.And(b.byType[King]).IsEmpty()
	// rookKing 是「移除子后需要一并重查的滑子集」——出现过飞将时把将算进来。
	rookKing := b.byType[Rook]
	if flying {
		attackers = attackers.Or(rookAttacks(to, occupied, Bitboard{}).And(b.byType[King]))
		rookKing = rookKing.Or(b.byType[King])
	}

	nonCannons := attackers.AndNot(b.byType[Cannon])
	cannons := attackers.And(b.byType[Cannon])

	side := ColorOf(p.Board[from]) >> 3
	res := 1

	recalc := func() {
		nonCannons = nonCannons.Or(rookAttacks(to, occupied, Bitboard{}).And(rookKing))
		_, cc := cannonAttacks(to, occupied, Bitboard{})
		cannons = cc.And(b.byType[Cannon])
		attackers = nonCannons.Or(cannons)
	}

	for {
		side ^= 1
		attackers = attackers.And(occupied)
		stm := attackers.And(b.byColor[side])
		if stm.IsEmpty() {
			break
		}
		res ^= 1

		// 按皮卡鱼的顺序挑下一个攻击者：兵 → 象 → 仕 → 炮 → 马 → 车 → 将。
		// 顺序会影响提前退出的时机，故照抄而不再自行排序。
		ip := stm.And(b.byType[Pawn])
		ie := stm.And(b.byType[Elephant])
		ia := stm.And(b.byType[Advisor])
		ic := stm.And(b.byType[Cannon])
		ih := stm.And(b.byType[Horse])
		ir := stm.And(b.byType[Rook])

		switch {
		case !ip.IsEmpty():
			if swap = seeValue[Pawn] - swap; swap < res {
				return res == 1
			}
			occupied.Clear(ip.lsb())
			recalc()
		case !ie.IsEmpty():
			if swap = seeValue[Elephant] - swap; swap < res {
				return res == 1
			}
			occupied.Clear(ie.lsb())
		case !ia.IsEmpty():
			if swap = seeValue[Advisor] - swap; swap < res {
				return res == 1
			}
			occupied.Clear(ia.lsb())
			// 仕可能是马的蹩腿点，移除后要补一次马。
			nonCannons = nonCannons.Or(knightAttackersBB(b, to, occupied))
			attackers = nonCannons.Or(cannons)
		case !ic.IsEmpty():
			if swap = seeValue[Cannon] - swap; swap < res {
				return res == 1
			}
			occupied.Clear(ic.lsb())
			_, cc := cannonAttacks(to, occupied, Bitboard{})
			cannons = cc.And(b.byType[Cannon])
			attackers = nonCannons.Or(cannons)
		case !ih.IsEmpty():
			if swap = seeValue[Horse] - swap; swap < res {
				return res == 1
			}
			occupied.Clear(ih.lsb())
		case !ir.IsEmpty():
			swap = seeValue[Rook] - swap
			occupied.Clear(ir.lsb())
			recalc()
		default:
			// 将吃：若对方仍有攻击者，将不能停在被攻击的格，结果反转。
			if !attackers.AndNot(b.byColor[side]).IsEmpty() {
				res ^= 1
			}
			return res == 1
		}
	}
	return res == 1
}

// knightAttackersBB 返回能马步攻击 to 的马所在格（要求蹩腿点为空）。
// 与 BB.attackersTo 里那段同源，单独抽出来是为了在 SEE 的增量更新里复用。
func knightAttackersBB(b *BB, to int, occ Bitboard) Bitboard {
	var out Bitboard
	for _, ka := range knightAttackers[to] {
		if ka.origin < 0 {
			break
		}
		if !occ.Test(ka.leg) && b.byType[Horse].Test(ka.origin) {
			out.Set(ka.origin)
		}
	}
	return out
}
