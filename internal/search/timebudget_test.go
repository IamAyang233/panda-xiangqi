package search

import (
	"fmt"
	"os"
	"testing"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/puzzle"
)

// TestTimeBudget 评估「提前收手 + 把省下的节点分给硬局面」这个时间管理方案。
//
// 动因：我们的时间管理是「每步固定 Soft=0.6T / Hard=T」，简单局面与关键局面花
// 一样多的时间。实测答案几乎在预算的极早阶段就定型（中位数 1.2% 预算），而
// 朴素判据「连续 2 档答案相同就停」只用 13.6% 节点、正确率却掉 4.1 个百分点。
//
// 做法：**一次采集、事后模拟多种判据**。每档记录 (最佳着法, 分值, 次佳分值)，
// 然后离线扫阈值组合 —— 一次 4 分钟的跑覆盖所有变体。
//
// 最后一节是**决定性问题**：等总节点下，「提前收手 + 把省下的给硬局面」能否
// 赢过均匀分配？不能赢的话，早停只是省时间（UX），不是涨棋力。
//
// 语料用**长杀题**（解答 ≥5 步）：短杀题太容易（定型点在 1% 预算处），会给出
// 虚假的乐观上界。
// 默认跳过（要跑约 3.5 分钟）。显式要求时：
//
//	QIJING_TB=1 go test ./internal/search/ -run TestTimeBudget -v
func TestTimeBudget(t *testing.T) {
	if os.Getenv("QIJING_TB") == "" {
		t.Skip("默认跳过：设 QIJING_TB=1 才运行（约 3.5 分钟）")
	}
	w := loadWeights(t)
	store, err := puzzle.Embedded()
	if err != nil {
		t.Skipf("题库不可用：%v", err)
	}

	type item struct{ fen, sol string }
	var items []item
	for _, pz := range store.All() {
		if len(items) >= 120 {
			break
		}
		if pz.Goal != "win" || len(pz.Solution) < 5 {
			continue
		}
		pos, perr := game.ParseFEN(pz.FEN)
		if perr != nil {
			continue
		}
		if (pos.Turn == game.Red) != (pz.PlayerSide != "black") {
			continue
		}
		items = append(items, item{pz.FEN, pz.Solution[0]})
	}
	if len(items) == 0 {
		t.Skip("没有可用的长杀题")
	}

	budgets := []int64{1000, 2000, 5000, 10000, 20000, 40000, 80000, 160000}
	nT := len(budgets)
	mv := make([][]string, len(items))
	gap := make([][]int, len(items)) // 最佳 − 次佳
	for i, it := range items {
		mv[i] = make([]string, nT)
		gap[i] = make([]int, nT)
		for j, b := range budgets {
			pos, _ := game.ParseFEN(it.fen)
			res := New(w).SearchNodes(pos, b)
			mv[i][j] = res.Best.String()
			if len(res.Roots) >= 2 {
				gap[i][j] = res.Roots[0].Score - res.Roots[1].Score
			} else {
				gap[i][j] = -1 // 只有一个着法：信号不可用
			}
		}
	}
	maxB := budgets[nT-1]
	baselineNodes := maxB * int64(len(items))
	baseHits := 0
	for i := range mv {
		if mv[i][nT-1] == items[i].sol {
			baseHits++
		}
	}

	type predictor struct {
		name    string
		minTier int // 至少搜到第几档才允许停
		stableN int // 需要连续几档答案相同
		minGap  int // 次佳差距阈值（-1 表示不看）
	}
	preds := []predictor{
		{"连续2档稳定", 0, 2, -1},
		{"连续3档稳定", 0, 3, -1},
		{"连续2档稳定 + 至少到10k档", 3, 2, -1},
		{"连续3档稳定 + 至少到10k档", 3, 3, -1},
		{"连续2档稳定 + 差距>=100", 0, 2, 100},
		{"连续2档稳定 + 差距>=300", 1, 2, 300},
		{"连续3档稳定 + 差距>=100 + 至少到10k档", 3, 3, 100},
	}

	// stopTier 按判据算出停在哪一档。
	stopTier := func(p predictor, i int) int {
		for j := 1; j < nT; j++ {
			if j < p.minTier {
				continue
			}
			ok := true
			for k := 1; k < p.stableN; k++ {
				if j-k < 0 || mv[i][j] != mv[i][j-k] {
					ok = false
					break
				}
			}
			if !ok {
				continue
			}
			if p.minGap >= 0 && gap[i][j] < p.minGap {
				continue
			}
			return j
		}
		return nT - 1
	}

	fmt.Printf("\n=== 长杀题 %d 道（解答 >=5 步），满预算 %d 节点 ===\n", len(items), maxB)
	fmt.Printf("基准（满预算）：正确 %d/%d = %.1f%%，共 %d 节点\n\n",
		baseHits, len(items), 100*float64(baseHits)/float64(len(items)), baselineNodes)
	fmt.Printf("%-44s %9s %9s %9s\n", "判据", "节点占比", "正确率", "与基准一致")

	for _, p := range preds {
		var nodes int64
		hits, agree := 0, 0
		for i := range mv {
			st := stopTier(p, i)
			nodes += budgets[st]
			if mv[i][st] == items[i].sol {
				hits++
			}
			if mv[i][st] == mv[i][nT-1] {
				agree++
			}
		}
		fmt.Printf("%-44s %8.1f%% %8.1f%% %8.1f%%\n", p.name,
			100*float64(nodes)/float64(baselineNodes),
			100*float64(hits)/float64(len(items)),
			100*float64(agree)/float64(len(items)))
	}

	// ---- 决定性问题：等总节点下，方案 D 能否赢过均匀分配 ----
	best := preds[len(preds)-1]
	var dNodes int64
	dHits, hard := 0, 0
	for i := range mv {
		st := stopTier(best, i)
		if st == nT-1 {
			hard++
		}
		dNodes += budgets[st]
		if mv[i][st] == items[i].sol {
			dHits++
		}
	}
	fmt.Printf("\n=== 等总节点对比（决定性）===\n")
	fmt.Printf("方案 D（%s）：总节点 %d（%.1f%%），正确 %d/%d = %.1f%%，其中 %d 道未提前停\n",
		best.name, dNodes, 100*float64(dNodes)/float64(baselineNodes),
		dHits, len(items), 100*float64(dHits)/float64(len(items)), hard)
	fmt.Printf("等节点的均匀分配（每题约 %.0f 节点）：\n", float64(dNodes)/float64(len(items)))
	for j := 0; j < nT; j++ {
		tot := budgets[j] * int64(len(items))
		h := 0
		for i := range mv {
			if mv[i][j] == items[i].sol {
				h++
			}
		}
		mark := ""
		if float64(tot) <= float64(dNodes)*1.05 && float64(tot) >= float64(dNodes)*0.95 {
			mark = "  <== 与方案 D 等节点"
		}
		fmt.Printf("  %7d 节点/题：总 %9d，正确 %3d = %.1f%%%s\n",
			budgets[j], tot, h, 100*float64(h)/float64(len(items)), mark)
	}
}
