package search

import (
	"testing"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/nnue"
)

// TestFeatureChainStructure 打印「特征累加器链路」的结构占比。
//
// 为什么需要它：单线程 nps 与对手差 5.9×（我们 132k / 皮卡鱼 779k，同局面实测），
// 而**这台机器上的时间有 76% 花在 evaluate + nnue.Make/Unmake**（见下面的分解），
// 也就是说「搜索」本身几乎不花钱，钱全在特征维护上。任何提速尝试都必须先看这张图，
// 否则很容易去改一个只占 1% 的东西。
//
// 2026-09-17 实测（下表已写死为参考值，超时环境可只跑 -short 跳过）：
//
//	alphaBeta 的子项               占全机
//	  evaluate                     39.2%   （Apply 32.3% + EvalValueAt 6.7%）
//	  nnue.Position.Make           22.8%   （movePiece 19.4%）
//	  nnue.Position.Unmake         21.5%
//	  quiesce                      11.7%
//	  game.Position.Make            3.5%
//	  LegalMoves                    3.5%
//	Apply 内部
//	  applyThreats                 13.4%   ← 单个最大叶子
//	  rebuildPSQ / rebuildThreats   6.7% / 6.0%   ← 全量重建合计 12.7%
//	  applyPSQ                      5.9%
//	前向传播（transformHalfAVX2 + fc0Accum4）合计约 3.7%，已是 AVX2 汇编
//
// 运行：go test ./internal/search/ -run TestFeatureChainStructure -v
func TestFeatureChainStructure(t *testing.T) {
	if testing.Short() {
		t.Skip("会跑十几秒")
	}
	nnue.EnableDiag(true)
	nnue.ResetDiag()
	t.Cleanup(func() { nnue.EnableDiag(false) })

	w := loadWeights(t)
	fen := "1nbak2nr/4a4/2c1b2c1/p1p1p1p1p/9/5NP2/P1P1P3P/2N1C2C1/9/2BAKAB1R b - - 0 1"
	p, err := game.ParseFEN(fen)
	if err != nil {
		t.Fatal(err)
	}
	res := New(w).SearchNodes(p.Clone(), 400000)
	d := nnue.DiagSnapshot()
	n := float64(res.Nodes)

	t.Logf("节点 %d", res.Nodes)
	t.Logf("—— 威胁维护（computeRay 候选循环）——")
	t.Logf("slidingAttackBoth %.2f 次/节点｜进入 computeRay %.2f 次/节点",
		float64(d.SlidingCalls)/n, float64(d.RayCalls)/n)
	t.Logf("候选迭代 %.2f 次/节点（车 %.2f｜炮 %.2f｜马象 %.2f）",
		float64(d.CandRook+d.CandCannon+d.CandLeaper)/n,
		float64(d.CandRook)/n, float64(d.CandCannon)/n, float64(d.CandLeaper)/n)
	t.Logf("  威胁条目：发出 %.2f 条/节点｜指向 s %.2f 条/节点",
		float64(d.ThreatOut)/n, float64(d.ThreatIn)/n)
	t.Logf("—— 累加器 Apply ——")
	t.Logf("Apply %.2f 次/节点｜脏窗口平均 %.2f 条（上限 %d）｜峰值 %d 条",
		float64(d.ApplyCalls)/n, float64(d.ApplyWindow)/float64(maxI64(d.ApplyCalls, 1)),
		nnue.PendingLimit(), d.MaxWindow)
	t.Logf("**全量重建占视角的 %.1f%%**（重建时平均枚举威胁 %.0f 条 / PSQ %.0f 条）",
		100*d.RebuildShare(), d.AvgRebuildFeat(),
		float64(d.RebuildPiece)/float64(maxI64(d.RebuildPSQ, 1)))
	t.Logf("增量消费威胁条目 %.2f 条/节点（每条约 2 次 1KB 随机行 add/sub）",
		float64(d.ApplyEntries)/n)
	// 重建占 12.7% 全机，但只有「窗口超限」那一档才可能通过 pendingLimit 调参改善。
	t.Logf("重建原因：%s", d.RebuildReasonReport())

	// 守卫：这些量级本身是「该不该动手」的判据，飘了就该重新评估
	if c := float64(d.CandRook+d.CandCannon+d.CandLeaper) / n; c > 3 {
		t.Logf("⚠️ computeRay 候选数涨到 %.2f 次/节点 —— 若超过 3，「换 pass 表」就重新变得值得做", c)
	}
	if d.RebuildShare() > 0.25 {
		t.Logf("⚠️ 全量重建占比涨到 %.1f%% —— 超过 25%% 值得重新查失效原因", 100*d.RebuildShare())
	}
}

func maxI64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
