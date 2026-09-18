package search

import (
	"testing"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/nnue"
)

// TestIterObserverFiresPerDepth 守卫「思考信息流」回调（Searcher/Pool.SetIterObserver）。
//
// UI 要实时显示「深度/分值/节点数」就得靠它，所以它的契约必须钉死：
//   - **每完成一层报一次**（不是只在结束时报一次，也不是每层报多次）
//   - 深度**严格递增**：否则说明把「被放弃的重搜」也报出来了
//   - 节点/走子**单调不减**：它们都是累计值
//   - 最后一次的深度 = 搜索结果的深度
//
// ⚠️ 用固定节点预算（SearchNodes）而不是时间预算：后者在慢机器上层数不定，
// 会让「报了几次」变成噪声 —— 本项目的测量必须是确定的。
func TestIterObserverFiresPerDepth(t *testing.T) {
	w, err := nnue.Load(flatPath)
	if err != nil {
		t.Skip("未找到展开后的权重，跳过")
	}
	p, err := game.ParseFEN("2bak1b2/4a4/4b4/p1p1p3p/6p2/2P6/P3P1P1P/1C2C4/9/RNBAKABNR w - - 0 1")
	if err != nil {
		t.Fatal(err)
	}

	type obs struct {
		depth   int
		nodes   int64
		makes   int64
		hasMove bool
	}
	var got []obs
	s := New(w)
	s.SetIterObserver(func(r Result) {
		got = append(got, obs{r.Depth, r.Nodes, r.Makes, r.Best != game.Move{}})
	})
	res := s.SearchNodes(p.Clone(), 200000)
	s.SetIterObserver(nil)

	if len(got) < 3 {
		t.Fatalf("只收到 %d 次回调，期望 ≥3（每层一次）", len(got))
	}
	for i, o := range got {
		if !o.hasMove {
			t.Errorf("第 %d 次回调没有最佳着法（UI 会显示空）", i+1)
		}
		if i > 0 && o.depth <= got[i-1].depth {
			t.Errorf("第 %d 次深度 %d 未严格大于上一次 %d", i+1, o.depth, got[i-1].depth)
		}
		if i > 0 && o.nodes < got[i-1].nodes {
			t.Errorf("第 %d 次节点数 %d 小于上一次 %d（应为累计值）", i+1, o.nodes, got[i-1].nodes)
		}
		if i > 0 && o.makes < got[i-1].makes {
			t.Errorf("第 %d 次走子数 %d 小于上一次 %d（应为累计值）", i+1, o.makes, got[i-1].makes)
		}
	}
	for i, o := range got {
		if o.nodes <= 0 || o.makes <= 0 {
			t.Errorf("第 %d 次回调的节点/走子为 %d/%d —— 回调必须给当时的累计值", i+1, o.nodes, o.makes)
		}
	}
	last := got[len(got)-1]
	if last.depth != res.Depth {
		t.Errorf("最后一次回调深度 %d ≠ 结果深度 %d", last.depth, res.Depth)
	}

	// 取消后不应再回调：否则 UI 会在搜索结束后继续被刷新。
	got = got[:0]
	s2 := New(w)
	s2.SetIterObserver(func(r Result) { got = append(got, obs{r.Depth, r.Nodes, r.Makes, true}) })
	s2.SearchNodes(p.Clone(), 200000)
	s2.SetIterObserver(nil)
	n1 := len(got)
	s2.SearchNodes(p.Clone(), 200000)
	if len(got) != n1 {
		t.Errorf("取消观察者后又收到 %d 次回调（应为 0）", len(got)-n1)
	}
	t.Logf("收到 %d 次回调，深度 %d→%d，末次 走子 %d / 结点 %d",
		len(got), got[0].depth, last.depth, last.makes, last.nodes)
}
