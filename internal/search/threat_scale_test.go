package search

import (
	"testing"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/nnue"
)

// TestThreatChainScale 量出威胁增量链路的真实规模。
//
// 背景：把 nnue.slidingAttackBoth 改成空实现后，固定 10 万节点的搜索从
// 4.6s 掉到 1.64s（−64%）。但它自身的直接开销远不足以解释这个量级 ——
// computeRay 段的 attacksBB 走的是同一个函数，空掉后 pendingThreats 不再增长，
// 下游的 add/sub 与 ThreatIndex 的 4.1MB 随机访存也一并消失。
// 那 64% 是整条链，不是单个函数。
//
// 这个测试把链条摊开，核心是回答「增量路径与全量重建谁在做主」：
//   - 两条路径都在做「每个特征/条目一次 1024 通道 add」，单位成本相同，
//     所以比较的是**规模**：重建枚举的特征数 vs 增量消费的条目数。
//   - 若重建的规模远超增量，说明 pendingLimit 这个阈值选得不合适 ——
//     它按「条目数」卡，但重建的成本取决于「特征数」，两者并不等价。
func TestThreatChainScale(t *testing.T) {
	w := loadWeights(t)
	p, err := game.ParseFEN(game.InitialFEN)
	if err != nil {
		t.Fatal(err)
	}
	s := New(w)

	const budget = 100000
	nnue.ResetDiag()
	nnue.EnableDiag(true)
	res := s.SearchNodes(p, budget)
	nnue.EnableDiag(false)
	st := nnue.DiagSnapshot()

	if st.ApplyCalls == 0 {
		t.Fatal("没有采集到 Apply 调用，统计未生效")
	}
	n := float64(res.Nodes)
	applyViews := float64(2*st.ApplyCalls) - float64(st.RebuildThreat)

	t.Logf("搜索：%d 节点 → depth %d", res.Nodes, res.Depth)
	t.Logf("")
	t.Logf("【调用密度】（每节点）")
	t.Logf("  slidingAttackBoth     %6.1f 次   （合计 %d）",
		float64(st.SlidingCalls)/n, st.SlidingCalls)
	t.Logf("  computeRay attacksBB  %6.1f 次   （合计 %d）",
		float64(st.RayAttackCall)/n, st.RayAttackCall)
	t.Logf("  Apply                 %6.3f 次   （合计 %d）",
		float64(st.ApplyCalls)/n, st.ApplyCalls)
	t.Logf("")
	t.Logf("【威胁窗口】")
	t.Logf("  平均窗口长度   %.1f 条", float64(st.ApplyWindow)/float64(st.ApplyCalls))
	t.Logf("  峰值窗口长度   %d 条（pendingLimit = %d）", st.MaxWindow, nnue.PendingLimit())
	t.Logf("")
	t.Logf("【两条路径的分工】（单位成本相同，比的是规模）")
	t.Logf("  路径          视角次数    平均规模      总特征/条目数")
	t.Logf("  重建·PSQ      %8d    %6.1f      %d",
		st.RebuildPSQ, div(st.RebuildPiece, st.RebuildPSQ), st.RebuildPiece)
	t.Logf("  增量·PSQ      %8.0f    %6.1f      %d",
		applyViews, div(st.ApplyPieces, int64(applyViews)), st.ApplyPieces)
	t.Logf("  重建·威胁     %8d    %6.1f      %d",
		st.RebuildThreat, st.AvgRebuildFeat(), st.RebuildFeat)
	t.Logf("  增量·威胁     %8.0f    %6.1f      %d",
		applyViews, div(st.ApplyEntries, int64(applyViews)), st.ApplyEntries)
	t.Logf("")
	t.Logf("  走全量重建的视角占比  %.1f%%", st.RebuildShare()*100)
	t.Logf("  重建规模 / 增量规模   威胁 %.2f 倍   PSQ %.2f 倍",
		ratio(st.RebuildFeat, st.ApplyEntries), ratio(st.RebuildPiece, st.ApplyPieces))
	t.Logf("")
	t.Logf("【按单条 30ns 估算的总成本】")
	t.Logf("  重建·威胁  %6.1f ms   （%.0f ns/节点）",
		float64(st.RebuildFeat)*30/1e6, float64(st.RebuildFeat)*30/n)
	t.Logf("  增量·威胁  %6.1f ms   （%.0f ns/节点）",
		float64(st.ApplyEntries)*30/1e6, float64(st.ApplyEntries)*30/n)
}

func div(a int64, b int64) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}

func ratio(a, b int64) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}
