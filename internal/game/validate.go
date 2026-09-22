package game

// 局面静态合法性校验（A2 补强）。
//
// 存在的理由：`ParseFEN` 只做**语法**校验（行数、行长、字符合法、有两个将），
// 不校验**语义**。这本身是有意的 —— perft 对拍与离线工具需要构造「非行棋方
// 正被将军」这类边界局面。但一旦这样的局面进入**对局/残局**路径，整套合法性
// 判定就会失真：
//
//   - `LegalMoves` / `InCheck` 都建立在「非行棋方没有被将军」这一前提上
//     （见 position.go 里 GivesCheck 的前置条件说明）。前提被破坏时，
//     走子方可以「吃将」，而 `clearPiece` 从不更新 `kingSq` ⇒ 该方的 kingSq
//     永久悬空在一个空格上，之后的将军判定全是围绕幽灵格做的。
//   - 将帅照面（飞将）在开局摆位上就是非法的（正常对局里它由走子规则每次
//     拒绝，不可能出现在轮走方身上）。
//   - 士/象被摆在不可能的格子上，会让 `advisorMoves` / `elephantSteps` 这两张
//     共享表被反向使用（see.go 的 attackersTo）时产生假攻击者。
//
// 所以把「摆位合法性」与「非行棋方未被将军」做成显式校验，供 puzzle / session
// 这类**真实对局**入口调用；`ParseFEN` 保持宽松。
//
// 这套规则原先只存在于 cmd/import-canju（内容管线的防呆），此处下沉到 game 包
// 后由运行时统一复用，避免「导入时校验、运行时放行」的口径不一致。

import "fmt"

// 士的合法落点（红方坐标；黑方按 9-rank 镜像）。象的点位无从表里推导
// （elephantSteps 只收录了「站在合法点位」的着法，反向不能判定占用者本身
// 是否在点位上），故与士一样用显式表。
var (
	advisorPoints = map[[2]int]bool{
		{3, 0}: true, {5, 0}: true, {4, 1}: true, {3, 2}: true, {5, 2}: true,
	}
	elephantPoints = map[[2]int]bool{
		{2, 0}: true, {6, 0}: true, {0, 2}: true, {4, 2}: true, {8, 2}: true,
		{2, 4}: true, {6, 4}: true,
	}
)

// mirrorRank 把红方视角的 rank 映到黑方（9-rank）。
func mirrorRank(r int) int { return 9 - r }

// onPoint 判断 (f,r) 是否为 col 方的合法点位（用红方表 + 黑方镜像）。
func onPoint(points map[[2]int]bool, f, r, col int) bool {
	if col == Black {
		r = mirrorRank(r)
	}
	return points[[2]int{f, r}]
}

// ValidatePlacement 校验摆位合法性：将/士在九宫、士在九宫点位、象不过河且在
// 象位、将帅不照面。**不**检查「非行棋方是否被将军」（那取决于轮走方，见
// LegalPosition）。
func (p *Position) ValidatePlacement() error {
	for sq := 0; sq < bbSquares; sq++ {
		pc := p.Board[sq]
		if pc == Empty {
			continue
		}
		typ, col := TypeOf(pc), ColorOf(pc)
		f, r := bbFile(sq), bbRank(sq)
		switch typ {
		case King:
			if !inPalace(col, f, r) {
				return fmt.Errorf("将/帅出九宫(%s)", SquareName(uint8(sq)))
			}
		case Advisor:
			if !onPoint(advisorPoints, f, r, col) {
				return fmt.Errorf("士/仕不在九宫点位(%s)", SquareName(uint8(sq)))
			}
		case Elephant:
			if !ownSide(col, r) {
				return fmt.Errorf("相/象过河(%s)", SquareName(uint8(sq)))
			}
			if !onPoint(elephantPoints, f, r, col) {
				return fmt.Errorf("相/象不在象位(%s)", SquareName(uint8(sq)))
			}
		}
	}
	if ok, _ := p.FlyingGenerals(); ok {
		return fmt.Errorf("将帅照面（飞将）")
	}
	return nil
}

// LegalPosition 校验局面可安全进入对局路径：摆位合法，且**非行棋方没有被将军**。
//
// 「非行棋方被将军」意味着上一手留下了自己被将的局面（或 FEN 就是这么写的），
// 此时走子方可以吃将 —— 而吃将会让 kingSq 悬空、整套合法性判定失效。
func (p *Position) LegalPosition() error {
	if err := p.ValidatePlacement(); err != nil {
		return err
	}
	if p.InCheck(Opponent(p.Turn)) {
		return fmt.Errorf("非行棋方正被将军（非法局面）")
	}
	return nil
}

// FlyingGenerals 报告双方将帅是否在同一纵线上直接照面（中间无子）。
// 返回 (是否照面, 间距)；任一方无将时返回 false。
func (p *Position) FlyingGenerals() (bool, int) {
	rk, bk := p.kingSq[0], p.kingSq[1]
	if p.Board[rk] != Piece(Red, King) || p.Board[bk] != Piece(Black, King) {
		return false, 0
	}
	rf, bf := bbFile(rk), bbFile(bk)
	if rf != bf {
		return false, 0
	}
	lo, hi := bbRank(rk), bbRank(bk)
	if lo > hi {
		lo, hi = hi, lo
	}
	for r := lo + 1; r < hi; r++ {
		if p.Board[bbSquare(rf, r)] != Empty {
			return false, 0
		}
	}
	return true, hi - lo
}
