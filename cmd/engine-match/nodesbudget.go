package main

// 节点预算对比：给两边**同样多的节点**，看各自能完整搜到第几层。
//
// 这是排查搜索效率时唯一干净的口径。按时间对比时，两个引擎的
// 「单位时间能算多少」和「每个节点有多有效」混在一起，无法分离；
// 而把预算换成节点数后，计算速度被完全抵消，剩下的差值就是纯粹的
// 搜索效率差 —— 也就是前文那 1.75 倍落差的真实来源。

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
	sr := search.New(w)

	sess, err := newUCISession(uciPath, skill)
	if err != nil {
		fmt.Println("皮卡鱼会话建立失败：", err)
		return
	}
	defer sess.Close()

	fmt.Printf("=== 节点预算对比：同样的节点数，谁能搜得更深 ===\n")
	fmt.Printf("预算以节点计，与时间无关，因此消去了两侧的计算速度差异\n\n")

	for _, b := range budgets {
		fmt.Printf("── 预算 %d 节点 ──\n", b)
		fmt.Printf("%-22s %-16s %-16s %-10s\n", "局面", "内嵌 depth/节点", "皮卡鱼 depth/节点", "深度差")
		var sumN, sumU float64
		n := 0
		for _, c := range fens {
			p, perr := game.ParseFEN(c.fen)
			if perr != nil {
				continue
			}

			res := sr.SearchNodes(p, b)
			ud, un, uerr := sess.goNodes(c.fen, b, 180*time.Second)
			if uerr != nil {
				fmt.Printf("%-22s 皮卡鱼失败：%v\n", c.name, uerr)
				continue
			}
			if isMateScore(res.Score) {
				continue
			}
			fmt.Printf("%-22s d%-4d %-11d d%-4d %-11d %+d\n",
				c.name, res.Depth, res.Nodes, ud, un, res.Depth-ud)
			sumN += float64(res.Depth)
			sumU += float64(ud)
			n++
		}
		if n > 0 {
			fmt.Printf("均值：内嵌 depth %.2f    皮卡鱼 depth %.2f    深度比 %.0f%%\n\n",
				sumN/float64(n), sumU/float64(n), sumN/sumU*100)
		}
	}
}
