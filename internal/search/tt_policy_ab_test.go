package search

import (
	"testing"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/nnue"
)

// TestTTReplacePolicyAB 用同一个搜索对比两种替换策略：
//
//	A = 现在的「深度优先替换」（深旧项不被浅新项覆盖）
//	B = 无条件覆盖
//
// 判据：如果 B 的命中率显著高于 A，说明深度优先策略把深层旧项
// 「锁死」在槽位里，导致绝大多数新项根本写不进表 —— 那 4% 就是这么来的。
func TestTTReplacePolicyAB(t *testing.T) {
	w, err := nnue.Load(flatPath)
	if err != nil {
		t.Skip("未找到展开后的权重，跳过")
	}
	p, err := game.ParseFEN(game.InitialFEN)
	if err != nil {
		t.Fatal(err)
	}
	const depth = 9

	type outcome struct {
		hitRate   float64
		nodes     int64
		best      string
		score     int
		filled    int
		notFilled int
	}
	run := func(always bool) outcome {
		ttAlwaysReplace = always
		defer func() { ttAlwaysReplace = false }()
		s := New(w)
		res := s.Search(p, depth)
		var filled int
		for i := range s.tt.entries {
			if s.tt.entries[i].flag != ttNone {
				filled++
			}
		}
		return outcome{
			hitRate:   float64(s.TTHits()) / float64(res.Nodes) * 100,
			nodes:     res.Nodes,
			best:      res.Best.String(),
			score:     res.Score,
			filled:    filled,
			notFilled: len(s.tt.entries) - filled,
		}
	}

	a := run(false)
	b := run(true)

	t.Logf("A 深度优先替换：节点 %d，命中率 %.2f%%，表内 %d 项，best %s(%d)",
		a.nodes, a.hitRate, a.filled, a.best, a.score)
	t.Logf("B 无条件覆盖  ：节点 %d，命中率 %.2f%%，表内 %d 项，best %s(%d)",
		b.nodes, b.hitRate, b.filled, b.best, b.score)

	if b.hitRate > a.hitRate*1.5 {
		t.Errorf("命中率 B(%.2f%%) 比 A(%.2f%%) 高 50%% 以上：替换策略过于保守，应改用分层/桶式替换",
			b.hitRate, a.hitRate)
	} else {
		t.Logf("两种策略命中率接近 —— 替换策略不是主因，需另找根因。")
	}
}
