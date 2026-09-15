package search

import (
	"testing"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/nnue"
)

// TestMoveOrderingQuality 测量走法排序质量。
//
// 指标是首着截断率 = 「beta 截断发生在排序后第一个着法上」/「总 beta 截断次数」。
//
// 为什么看这个：alpha-beta 的剪枝强度完全取决于「能不能尽早搜到好着法」。
// 第一个着法一旦确立 alpha，后续着法就更容易被剪掉；若好着法排在第五、
// 第六位，前面几个着法都要完整展开，而且 LMR 会按序号加重削减，
// 排序差 → 剪枝失效 → 有效分支因子（EBF）居高不下。这正是 4.16 vs 1.88 的成因。
//
// 健康区间：配合置换表着法，良好引擎通常在 85%~95%。
func TestMoveOrderingQuality(t *testing.T) {
	w, err := nnue.Load(flatPath)
	if err != nil {
		t.Skip("未找到展开后的权重，跳过")
	}
	s := New(w)

	fens := []struct{ name, fen string }{
		{"初始局面", game.InitialFEN},
		{"开局（中炮对屏风马型）", "r1ba1a3/4kn3/2n1b4/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"},
		{"中局（子力互缠）", "2bak1b2/4a4/4b4/p1p1p3p/6p2/2P6/P3P1P1P/1C2C4/9/RNBAKABNR w - - 0 1"},
		{"残局（车兵对士象全）", "3ak4/4a4/4b4/9/9/9/4P4/9/4R4/4K4 w - - 0 1"},
	}

	diagMoveOrder = true
	defer func() { diagMoveOrder = false }()

	t.Logf("%-22s %-8s %-9s %-8s %-8s %-9s %-9s",
		"局面", "截断数", "首着截断率", "平均序号", "TT可用", "真排首位", "着法非法")
	var sumRate, sumIdx float64
	var avail, first, illeg int64
	n := 0

	for _, c := range fens {
		p, perr := game.ParseFEN(c.fen)
		if perr != nil {
			continue
		}
		s.Search(p, 8)
		if s.cutoffs == 0 {
			continue
		}
		rate := float64(s.cutoffFirst) / float64(s.cutoffs) * 100
		avgIdx := float64(s.cutoffIdxSum) / float64(s.cutoffs)
		t.Logf("%-22s %-8d %-9.2f%% %-8.2f %-8d %-9d %-9d",
			c.name, s.cutoffs, rate, avgIdx, s.ttMoveAvail, s.ttMoveFirst, s.ttMoveIlleg)
		sumRate += rate
		sumIdx += avgIdx
		avail += s.ttMoveAvail
		first += s.ttMoveFirst
		illeg += s.ttMoveIlleg
		n++
	}

	if n == 0 {
		t.Fatal("没有采集到截断样本")
	}
	avgRate := sumRate / float64(n)
	t.Logf("\n均值：首着截断率 %.2f%%    平均截断序号 %.2f", avgRate, sumIdx/float64(n))
	if avail > 0 {
		t.Logf("TT 着法：可用 %d，真排首位 %d（%.1f%%），不在着法列表 %d（%.1f%%）",
			avail, first, float64(first)/float64(avail)*100,
			illeg, float64(illeg)/float64(avail)*100)
		t.Logf("（两者之和 < 可用数，差额 %d 是被空着剪枝等提前 return 打断的）", avail-first-illeg)
	}
	// 判据说明：启用 reverse futility / futility / LMP 之后，首着截断率会下降 ——
	// 大量节点在进入着法循环之前就被直接 return 了，根本不产生「截断」，
	// 剩下的截断多是排序没能提前命中的硬骨头。所以这里只监控「严重退化」，
	// 而不是追求高数值。真正该看的是 EBF（用 -mode nodes 对比）。
	if avgRate < 55 {
		t.Errorf("首着截断率仅 %.2f%%，排序退化严重（参考：剪枝强化后约 70%%）。", avgRate)
	} else {
		t.Logf("结论：排序质量未出现严重退化。")
	}
}
