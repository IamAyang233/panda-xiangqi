package main

// 节点预算对比：给两边**同样多的工作量**，看各自能完整搜到第几层。
//
// 这是排查搜索效率时唯一干净的口径。按时间对比时，两个引擎的
// 「单位时间能算多少」和「每个节点有多有效」混在一起，无法分离；
// 而把预算换成工作量后，计算速度被完全抵消，剩下的差值就是纯粹的
// 搜索效率差。
//
// ---------------------------------------------------------------------------
// ⚠️⚠️ **2026-09-18 纠正：两边报的「节点数」根本不是同一个东西。**
//
//	皮卡鱼：`++nodes` 在 `do_move` 里（src/search.cpp:628）—— 每走一步算一个。
//	我们  ：结点**进入**次数（alphaBeta / quiesce 各一次），一次进入里往往要走好几步。
//
// 两者比值随深度变化，本项目实测（中局局面、固定深度迭代加深）：
//
//	depth    我们结点     我们走子     结点:走子
//	d1           82          37         2.22
//	d6         8414       13462         0.63
//	d12      170483      393067         0.43
//
// ⇒ **旧表的「同预算」实际让皮卡鱼少做了一倍多的工作**（d12 时它 2 万走子 vs
//   我们 39 万走子），于是「等节点下我们浅 2~4 层」这个结论里，混进了这个偏差。
//
// 本表因此同时给两个口径，**判读只看 `等走子量` 那一列**：
//	对手@同结点  —— 旧口径，保留作对照（它给对手的工作量偏少，会让我们显得更浅）
//	对手@同走子  —— 把它的预算设成我们实际的走子数，两边工作量真正相等
//
// 注意「等走子量」仍然只是**近似**可比：一次走子在两边做的增量工作不完全相同
// （皮卡鱼还维护 continuationCorrectionHistory 等）。但它是现有工具里最接近
// 「同一单位」的口径 —— 比拿结点数比走子数好一个数量级。

import (
	"fmt"
	"time"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/nnue"
	"github.com/IamAyang233/panda-xiangqi/internal/search"
)

// nodesBudget 对比在给定节点预算下两边各自达到的深度。
func nodesBudget(flatPath, uciPath string, skill int, budgets []int64, fens []struct {
	name string
	fen  string
}) {
	w, err := nnue.Load(flatPath)
	if err != nil {
		fmt.Println("加载 NNUE 权重失败：", err)
		return
	}
	// 单线程：多线程会因共享 TT 与抢占让节点数不可复现。
	// `SearchNodes` 内部会 Clear()，所以每题都是干净的表 —— 与皮卡鱼每题
	// 先 `ucinewgame` 对齐。
	sr := search.New(w)

	sess, err := newUCISession(uciPath, skill)
	if err != nil {
		fmt.Println("皮卡鱼会话建立失败：", err)
		return
	}
	defer sess.Close()

	fmt.Printf("=== 工作量对比：同样的工作量，谁能搜得更深 ===\n")
	fmt.Printf("⚠️ 判读只看「等走子量」：皮卡鱼的 nodes = 走子数，我们的 makes = 走子数，\n")
	fmt.Printf("   而我们的 nodes 是结点进入数（d12 时 1 结点 ≈ 2.3 走子）。\n\n")

	for _, b := range budgets {
		fmt.Printf("── 预算 %d 结点（我们的口径；对手按工作量换算）──\n", b)
		fmt.Printf("%-22s %-24s %-14s %-14s %-12s\n",
			"局面", "我们 depth/结点/走子", "对手@同结点", "对手@同走子", "深度差(等走子)")
		var sumN, sumU, sumW float64
		n := 0
		for _, c := range fens {
			p, perr := game.ParseFEN(c.fen)
			if perr != nil {
				continue
			}

			res := sr.SearchNodes(p, b)
			makes := sr.Makes()

			ud, _, uerr := sess.goNodes(c.fen, b, 180*time.Second)
			if uerr != nil {
				fmt.Printf("%-22s 皮卡鱼失败：%v\n", c.name, uerr)
				continue
			}
			// 等走子量：把它的预算设成我们实际的走子数。
			wd, _, werr := sess.goNodes(c.fen, makes, 180*time.Second)
			if werr != nil {
				fmt.Printf("%-22s 皮卡鱼失败（等走子量）：%v\n", c.name, werr)
				continue
			}
			if isMateScore(res.Score) {
				continue
			}
			fmt.Printf("%-22s d%-3d %7d/%7d  d%-4d %-10s d%-4d %-10s %+d\n",
				c.name, res.Depth, res.Nodes, makes, ud, "", wd, "", res.Depth-wd)
			sumN += float64(res.Depth)
			sumU += float64(ud)
			sumW += float64(wd)
			n++
		}
		if n > 0 {
			fmt.Printf("均值：我们 depth %.2f ｜ 对手@同结点 %.2f ｜ 对手@同走子 %.2f\n",
				sumN/float64(n), sumU/float64(n), sumW/float64(n))
			fmt.Printf("      → 旧口径深度比 %.0f%%（偏乐观，别用）｜ **等走子量深度比 %.0f%%**\n\n",
				sumN/sumU*100, sumN/sumW*100)
		}
	}
}
