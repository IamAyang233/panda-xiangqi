package engine

import (
	"context"
	"testing"
	"time"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/search"
)

// TestBestMoveObserveFires 守卫「思考信息流」的接线：
// Manager → NativeEngine → Pool → Searcher 的迭代观察器必须真的回调出来。
//
// 这一条的价值在于**跨层**：搜索层的观察者契约由
// `search.TestIterObserverFiresPerDepth` 守着，但「上层能不能拿到」是另一回事
// （比如 Pool 忘了挂、或者 BestMove 走了没带观察器的分支）—— 这里钉死后者。
//
// ⚠️ 走 Manager 而不是直接用 NativeEngine：这样才覆盖「降级时不回调」的真相 ——
// 一旦内嵌引擎不可用，本测试会 Skip（没有权重）或退化，不会假绿。
func TestBestMoveObserveFires(t *testing.T) {
	e := NewNativeEngine(requireWeights(t), 1)
	m := NewManager("")
	m.native = e

	p, err := game.ParseFEN("2bak1b2/4a4/4b4/p1p1p3p/6p2/2P6/P3P1P1P/1C2C4/9/RNBAKABNR w - - 0 1")
	if err != nil {
		t.Fatal(err)
	}

	type obs struct {
		depth int
		nodes int64
		makes int64
	}
	var got []obs
	mv, err := m.BestMoveObserve(context.Background(), p.Clone(), 6, func(r search.Result) {
		got = append(got, obs{r.Depth, r.Nodes, r.Makes})
	})
	if err != nil {
		t.Fatalf("BestMoveObserve 失败: %v", err)
	}
	if mv == (game.Move{}) {
		t.Fatal("未给出着法")
	}
	if len(got) < 2 {
		t.Fatalf("只收到 %d 次回调，期望 ≥2（搜索层契约是每层一次）", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].depth <= got[i-1].depth {
			t.Errorf("第 %d 次深度 %d 未严格大于上一次 %d", i+1, got[i].depth, got[i-1].depth)
		}
		if got[i].makes < got[i-1].makes {
			t.Errorf("第 %d 次走子数 %d 小于上一次 %d", i+1, got[i].makes, got[i-1].makes)
		}
	}
	t.Logf("收到 %d 次回调，深度 %d→%d，末次 走子 %d / 结点 %d，着法 %s",
		len(got), got[0].depth, got[len(got)-1].depth,
		got[len(got)-1].makes, got[len(got)-1].nodes, mv.String())

	// 传 nil 观察器时必须完全等价于 BestMove，且不因并发而炸。
	mv2, err := m.BestMoveObserve(context.Background(), p.Clone(), 6, nil)
	if err != nil {
		t.Fatalf("obs=nil 时失败: %v", err)
	}
	if mv2 == (game.Move{}) {
		t.Fatal("obs=nil 时未给出着法")
	}
	_ = time.Now
}
