package search

import (
	"fmt"
	"testing"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
)

// TestCutoffTier 量「截断着法来自哪一档排序」，以及历史表的健康度。
//
// 目的：判断该补哪一项排序技术，而不是凭「皮卡鱼有、我们没有」就动手。
// 判读方式：
//   - ttMove 档占比高 ⇒ 置换表在干活，排序压力小
//   - killer 档占比高 ⇒ 杀手启发有效
//   - **history 两档（>0 / =0）合计占比高、且平均序号大** ⇒ 历史质量就是瓶颈
//   - 历史表若大量堆积在上限 ⇒ 等于无信息（这正是我们要查的）
func TestCutoffTier(t *testing.T) {
	w := loadWeights(t)
	fens := loadQuietFENs(t)
	if len(fens) > 20 {
		fens = fens[:20]
	}
	diagTier = true
	defer func() { diagTier = false }()

	// 混入几个典型局面，让样本覆盖开局/中局/残局。
	for _, f := range []string{
		game.InitialFEN,
		"2bak1b2/4a4/4b4/p1p1p3p/6p2/2P6/P3P1P1P/1C2C4/9/RNBAKABNR w - - 0 1",
		"3ak4/4a4/4b4/9/9/9/4P4/9/4R4/4K4 w - - 0 1",
	} {
		fens = append(fens, f)
	}

	var total, idxTotal, cutFirst int64
	var histMaxCount, histPosCount, histAll int
	var worstHistory [90][90]int
	for _, f := range fens {
		p, err := game.ParseFEN(f)
		if err != nil {
			continue
		}
		s := New(w)
		s.Search(p, 9)
		for i := range tierDiag {
			ti := &tierDiag[i]
			if ti.cnt == 0 {
				continue
			}
			fmt.Printf("  %-14s 截断 %6d（%5.1f%%）  平均序号 %5.2f\n",
				tierNames[i], ti.cnt, 100*float64(ti.cnt)/float64(s.cutoffs),
				float64(ti.idxSum)/float64(ti.cnt))
		}
		total += s.cutoffs
		idxTotal += s.cutoffIdxSum
		cutFirst += s.cutoffFirst
		// 历史表健康度：只看这一局最后一次搜索留下的表。
		for i := range s.history {
			for j := range s.history[i] {
				v := s.history[i][j]
				if v == historyMax {
					histMaxCount++
				}
				if v > 0 {
					histPosCount++
				}
				if v > worstHistory[i][j] {
					worstHistory[i][j] = v
				}
				histAll++
			}
		}
	}

	fmt.Printf("\n=== 汇总（%d 个局面，共 %d 次截断）===\n", len(fens), total)
	for i := range tierDiag {
		if tierDiag[i].cnt == 0 {
			continue
		}
		fmt.Printf("%-14s %6d 次 %5.1f%%  平均序号 %5.2f\n",
			tierNames[i], tierDiag[i].cnt, 100*float64(tierDiag[i].cnt)/float64(total),
			float64(tierDiag[i].idxSum)/float64(tierDiag[i].cnt))
	}
	fmt.Printf("首着截断率 %.2f%%   平均截断序号 %.2f\n",
		100*float64(cutFirst)/float64(total), float64(idxTotal)/float64(total))
	fmt.Printf("历史表：非零格 %d/%d（%.1f%%），**顶到上限(%d)的格 %d（占非零的 %.1f%%）**\n",
		histPosCount, histAll, 100*float64(histPosCount)/float64(histAll),
		historyMax, histMaxCount, 100*float64(histMaxCount)/float64(max(histPosCount, 1)))
}
