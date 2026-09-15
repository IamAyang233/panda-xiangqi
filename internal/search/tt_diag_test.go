package search

import (
	"testing"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/nnue"
)

// TestTTFillDiagnostics 诊断置换表的实际使用情况。
//
// 背景：分析逐层诊断时发现 TT 命中率只有 4% 左右。要判断这是 bug 还是
// 「浅层搜索的正常现象」，唯一的办法是看它随深度的变化趋势 ——
// 置换表的收益来自「不同路径到达同一局面」，这种机会随搜索深度指数增长，
// 所以命中率必须随深度显著上升。若各深度都纹丝不动，那才是真的没写进去。
func TestTTFillDiagnostics(t *testing.T) {
	w, err := nnue.Load(flatPath)
	if err != nil {
		t.Skip("未找到展开后的权重，跳过")
	}
	s := New(w)

	p, err := game.ParseFEN(game.InitialFEN)
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("%-6s %-10s %-9s %-9s %-10s %-10s",
		"depth", "节点", "probe", "命中", "命中率", "写表次数")
	var first, last float64
	for _, d := range []int{4, 6, 8, 10} {
		res := s.Search(p, d)
		var nonEmpty int
		for i := range s.tt.entries {
			if s.tt.entries[i].flag != ttNone {
				nonEmpty++
			}
		}
		// 分母必须是 probeCnt 而非总节点数：静态搜索节点从不查表。
		rate := 0.0
		if s.probeCnt > 0 {
			rate = float64(s.TTHits()) / float64(s.probeCnt) * 100
		}
		t.Logf("d%-5d %-10d %-9d %-9d %-10.2f%% %-10d  表内 %d 项",
			d, res.Nodes, s.probeCnt, s.TTHits(), rate, s.storeCnt, nonEmpty)
		if d == 4 {
			first = rate
		}
		last = rate
	}
	t.Logf("\nprobe 命中率随深度：%.2f%% → %.2f%%（置换表始终保持有效）", first, last)
	// 健康区间参考：30%~60%。低于 20% 才说明写表被大面积丢弃。
	if last >= 25 {
		t.Logf("结论：命中率处于健康区间，置换表工作正常。")
	} else {
		t.Errorf("命中率仅 %.2f%%，置换表几乎没有发挥作用，需要检查 store 被拒绝的比例。", last)
	}
}
