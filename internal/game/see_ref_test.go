package game

import (
	"math/rand"
	"testing"
)

// 本文件用「显然正确的朴素实现」对拍 SeeGE。
//
// seeNaive 递归枚举 to 格上所有可能的吃子并用 negamax 取最优 —— 慢，但逻辑
// 直白到不可能错。它是 SeeGE 的裁判：两者在**符号**上分歧，就意味着 SEE 会
// 剪掉盈利的吃子。
//
// 这个对拍不是可选的。SeeGE 的增量更新依赖好几处「表语义假设」，都可以静默
// 出错；两轮排查一共踩了四个：
//  1. 马分支漏了 byType[Horse] 检查 —— knightAttackers 给的是「位置」，
//     不确认那位置上确实是马，兵也会被当成马攻击者；
//  2. kingMoves/advisorMoves 构建时只看目标格在宫殿内，没看起点 ——
//     于是「将/仕从宫里走出来吃子」被当成合法攻击者；
//  3. pawnAttackers 是分色表，只查类型不查颜色 —— 「红兵能攻击 sq 的位置上
//     站着黑卒」被当成红兵攻击者；
//  4. elephantSteps 反向查表时，红象被当成黑方半场的攻击者 —— 根因是
//     buildElephant 只检查落点不过河、没检查起点也在己方半场，于是
//     「象根本站不上的格」也带出过河落点，而 attackersTo 是按「起点 → 落点」
//     的表反向查的。前三个修完仍有 8/1741 分歧，正是这一条。
//
// 表现是 321 个吃子着法里 61 处分歧，在搜索里则表现为「节点反而变多 + 漏杀」
// （中局同一局面 3459 → 5957 节点，且 depth 6 的将杀被判成分值 4283）。
// 没有朴素版对拍，这些只会以「棋力莫名下降」的形式暴露。
//
// 另外，基座局面必须先校验合法性：曾有两处 FEN 在 rank7 误写成 "4k4"（本意
// 是象），一盘棋两个将，kingSq 只记得住后扫到的那个，合法性过滤随之全线错乱，
// 对拍出来的分歧全是假的。

// seeNaive 是「显然正确」的朴素 SEE：递归枚举 to 格上所有可能的吃子，
// 用 negamax 取最优。
//
// 返回「轮到 p.Turn 的一方，在 to 格上发起交换能拿到的最大净收益」。
func seeNaive(p *Position, to int) int {
	best := 0
	for _, m := range p.LegalMoves(p.Turn) {
		if int(m.To) != to {
			continue
		}
		victim := seeValue[TypeOf(p.Board[int(m.To)])]
		p.Make(m)
		rest := seeNaive(p, to)
		p.Unmake()
		if gain := victim - rest; gain > best {
			best = gain
		}
	}
	return best
}

// seeForMove 返回执行吃子 m 之后的净收益（对方会最优反击）。
func seeForMove(p *Position, m Move) int {
	victim := seeValue[TypeOf(p.Board[int(m.To)])]
	p.Make(m)
	rest := seeNaive(p, int(m.To))
	p.Unmake()
	return victim - rest
}

// compareSee 在当前局面上逐个吃子着法对拍 SeeGE 与朴素 SEE。
func compareSee(t *testing.T, p *Position, checked, mism int) (int, int) {
	t.Helper()
	fen := p.FEN()
	for _, m := range p.LegalMoves(p.Turn) {
		if p.PieceAt90(int(m.To)) == Empty {
			continue
		}
		q, err := ParseFEN(fen)
		if err != nil {
			t.Fatal(err)
		}
		exact := seeForMove(q, m)

		r, err := ParseFEN(fen)
		if err != nil {
			t.Fatal(err)
		}
		got := r.SeeGE(int(m.From), int(m.To), 0)
		checked++
		if got != (exact >= 0) {
			mism++
			if mism <= 12 {
				// 只记录不失败：判别力交给调用方的「分歧率上限」断言，
				// 这样既能逐个看到分歧样例，又不会因为已知缺口一直红。
				t.Logf("分歧 局面 %s\n  %s（%d→%d）：SeeGE=%v 但朴素 SEE=%d，应判 %v",
					fen, m, m.From, m.To, got, exact, exact >= 0)
			}
		}
	}
	return checked, mism
}

// TestSeeGEMatchesNaive 用朴素递归对拍 SeeGE。
//
// 判据是**符号**而不是精确值：SeeGE 只回答亏不亏（内部在 swap<res 时提前
// 退出），本来就不产出精确值，但它绝不能与朴素版在符号上分歧 ——
// 那意味着它会剪掉盈利的吃子。
//
// 局面必须包含中局：静止的开局/残局几乎没有吃子，对拍不到实质内容
// （第一版只比到 3 个着法，全是 0 分歧，等于没测）。这里用随机走子
// 生成大量中局局面再逐个对拍。
func TestSeeGEMatchesNaive(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	bases := []string{
		InitialFEN,
		"r1ba1a3/4kn3/2n1b4/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1",
		"3ak1b2/4a4/4b4/p1p1p3p/9/2P6/P3P1P1P/1C2C1R2/9/2BAKABN1 w - - 0 1",
		"2bak1b2/4a4/4b4/p1p1p3p/6p2/2P6/P3P1P1P/1C2C4/9/RNBAKABNR w - - 0 1",
		"rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR b - - 0 1",
		"3k5/4a4/4b4/p1p1p3p/9/2P6/P3P1P1P/1C2C4/4A4/2BAK1B2 w - - 0 1",
		"2bak4/4a4/4b4/9/9/9/4P4/4C4/9/3AKAB2 w - - 0 1",
	}

	var checked, mism int
	for _, fen := range bases {
		p, err := ParseFEN(fen)
		if err != nil {
			t.Fatal(err)
		}
		// 局面必须合法：每方恰好一个将。曾经有两处基座 FEN 在 rank7 误写成
		// "4k4"（本意是象），于是整盘棋有两个黑将 —— kingSq 只记得住后扫到的
		// 那个，合法性过滤随后全线错乱（17 个伪合法着法里只剩 1 个"合法"），
		// 对拍出来的分歧全是假的。
		for _, c := range []int{Red, Black} {
			if n := p.bb.byType[King].And(p.bb.byColor[c>>3]).Count(); n != 1 {
				t.Fatalf("基座局面非法：%s 方的将有 %d 个\n%s", map[int]string{Red: "红", Black: "黑"}[c], n, fen)
			}
		}
		for step := 0; step < 200; step++ {
			moves := p.LegalMoves(p.Turn)
			if len(moves) == 0 {
				break
			}
			p.Make(moves[rng.Intn(len(moves))])
			checked, mism = compareSee(t, p, checked, mism)
		}
	}
	rate := float64(mism) / float64(checked) * 100
	t.Logf("共对比 %d 个吃子着法，与朴素 SEE 分歧 %d 个（%.2f%%）", checked, mism, rate)

	// 当前实测 0。允许极少量分歧是因为 SeeGE 是增量式近似（照皮卡鱼的结构，
	// 不做「被牵制的子不能吃」那层过滤），个别被牵制局面可能算出不同符号；
	// 但两个真实 bug 分别产生过 61 处和 8 处（19% 与 0.46%），所以超过个位数
	// 就一定意味着表语义或增量更新出了问题，不是近似误差。
	if mism > 2 {
		t.Errorf("与朴素 SEE 的分歧 %d 处（%.2f%%），远超近似误差范围，SEE 判定被改坏了", mism, rate)
	}
}
