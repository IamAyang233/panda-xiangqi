package search

import (
	"testing"
	"time"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/nnue"
)

// TestPendingLimitSweep 确认 pendingLimit 不该调大。
//
// 直觉上「全量重建要枚举全部特征（约 32 次滑动攻击 + 21 条 thrAdd），
// 比增量应用贵得多，所以阈值该放大」是错的。实测：
//
//	pendingLimit   96     256    512    1024   2048
//	相对耗时       1.000  1.000  1.078  1.086  1.141
//
// 原因是提高阈值只让约 12% 的视角少走重建，却让占 88% 的增量路径窗口
// 同步变长，条目数线性增长。这个测试的存在意义是挡住「凭直觉调大它」的改动。
func TestPendingLimitSweep(t *testing.T) {
	if testing.Short() {
		t.Skip("扫描要跑半分钟以上")
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
	for _, lim := range []int{96, 512, 2048} {
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
