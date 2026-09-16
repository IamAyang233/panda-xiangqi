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
	t.Logf("\nprobe 命中率随深度：%.2f%% → %.2f%%", first, last)
	// 阈值只用于发现「置换表完全失效」，不追求高命中率：
	// 剪枝与置换表在降低有效分支因子上是相互替代的 —— 搜索树被前向剪枝
	// 剪小之后，不同路径撞上同一局面的机会自然减少，命中率随之下降。
	// 剪枝强化前实测 35~40%，安静 futility 对齐皮卡鱼后 15~20%，
	// 反 futility 的余量换成皮卡鱼曲线后又降到 7.7%（同一次改动让 depth 10
	// 的节点从 117966 降到 35529）。
	//
	// **这个数不是质量指标**：继续强化剪枝它还会往下掉，判断改动好坏要看
	// 固定节点预算下的深度（tree_efficiency_test.go）与保真度
	// （fwdprune_fidelity_test.go）。这里只做一件事 —— 在「几乎没有命中」
	// 时报出 store 被大面积拒绝。
	if last < 5 {
		t.Errorf("命中率仅 %.2f%%，置换表几乎未生效，需检查 store 是否被大面积拒绝。", last)
	} else {
		t.Logf("结论：命中率处于健康区间（剪枝强化后下降属正常）。")
	}
}
