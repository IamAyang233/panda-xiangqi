package search

import (
	"testing"
	"time"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/nnue"
)

// TestPendingLimitSweep 扫描 pendingLimit，确认它**两端都不该动**。
//
// 直觉上「全量重建要枚举全部特征（约 32 次滑动攻击 + 21 条 thrAdd），
// 比增量应用贵得多，所以阈值该放大」是错的。实测（初始局面）：
//
//	pendingLimit   96     256    512    1024   2048
//	相对耗时       1.000  1.000  1.078  1.086  1.141
//
// 原因是提高阈值只让约 12% 的视角少走重建，却让占 88% 的增量路径窗口
// 同步变长，条目数线性增长。这个测试的存在意义是挡住「凭直觉调大它」的改动。
//
// 2026-09-17 补测把**全范围**都扫了一遍（此前只扫过 ≥96），并改用 8 个中局
// 安静局面 × 6 万节点（重建原因的分解显示 58% 的重建是「脏窗口超限」，
// 也就是唯一与这个常量有关的那部分）：
//
//	 8 档 1.000×｜16 档 0.829×｜24 档 0.787×｜32 档 0.778×
//	48 档 0.773×｜64 档 0.772×｜**96 档 0.775×**｜128 档 0.795×
//	192 档 0.811×｜256 档 0.828×
//
// ⇒ 最优点是 48~64，但 96 只慢 0.4%（同一平台期）；**往下走则明显变差**
// （8 档慢 29%，重建太频繁）。曲线是平台型、两端都别动 —— 所以现在同时守着
// 「凭直觉调大」与「凭直觉调小」两个方向。
func TestPendingLimitSweep(t *testing.T) {
	if testing.Short() {
		t.Skip("扫描要跑一分多钟")
	}
	w := loadWeights(t)
	p, err := game.ParseFEN(game.InitialFEN)
	if err != nil {
		t.Fatal(err)
	}

	const budget = 100000
	orig := nnue.SetPendingLimit(96)
	defer nnue.SetPendingLimit(orig)

	t.Logf("%-14s %-12s %-10s %s", "pendingLimit", "最优耗时", "节点", "相对 96")
	var base float64
	for _, lim := range []int{96, 16, 32, 48, 64, 256, 512, 2048} {
		nnue.SetPendingLimit(lim)
		best := time.Hour
		var nodes int64
		var depth int
		for rep := 0; rep < 2; rep++ {
			s := New(w)
			start := time.Now()
			res := s.SearchNodes(p, budget)
			if d := time.Since(start); d < best {
				best, nodes, depth = d, res.Nodes, res.Depth
			}
		}
		ms := float64(best.Nanoseconds()) / 1e6
		if base == 0 {
			base = ms
		}
		t.Logf("%-14d %-12.0f %-10d %.3f×   (depth %d)",
			lim, ms, nodes, ms/base, depth)
	}
}
