package search

import (
	"math/rand"
	"runtime"
	"testing"
	"time"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
)

// TestRecommendedThreads 自动探测的线程数必须落在合法区间。
func TestRecommendedThreads(t *testing.T) {
	n := RecommendedThreads()
	if n < 1 || n > maxThreads {
		t.Fatalf("线程数 %d 超出 [1, %d]", n, maxThreads)
	}
	t.Logf("探测到 %d 线程（本机 CPU %d 核）", n, runtime.NumCPU())
}

// TestLevelTable 档位表必须单调：时间递增、随机池递减。
func TestLevelTable(t *testing.T) {
	prev := Level(1)
	for lv := 2; lv <= MaxLevel; lv++ {
		cur := Level(lv)
		if cur.Time < prev.Time {
			t.Errorf("%d 档时间 %v 少于 %d 档的 %v", lv, cur.Time, lv-1, prev.Time)
		}
		if cur.TopN > prev.TopN {
			t.Errorf("%d 档随机池 %d 大于 %d 档的 %d", lv, cur.TopN, lv-1, prev.TopN)
		}
		prev = cur
	}
	if Level(0) != Level(1) || Level(99) != Level(MaxLevel) {
		t.Error("越界档位未被夹到 [1, MaxLevel]")
	}
	if Level(MaxLevel).TopN != 1 {
		t.Error("最高档位应当总走最优着法")
	}
	if Level(1).TopN < 2 {
		t.Error("最低档位应当有随机挑选，否则和次低档没有区别")
	}
}

// TestPickMoveRandomness TopN=1 永远最优；TopN>1 会真的随机；Slack=0 时收敛回最优。
func TestPickMoveRandomness(t *testing.T) {
	best := game.Move{From: 1, To: 2}
	res := Result{
		Best: best,
		Roots: []RootMove{
			{Move: best, Score: 100},
			{Move: game.Move{From: 3, To: 4}, Score: 90},
			{Move: game.Move{From: 5, To: 6}, Score: 80},
		},
	}
	rng := rand.New(rand.NewSource(1))

	for i := 0; i < 20; i++ {
		if got := PickMove(res, LevelParams{TopN: 1}, rng); got != best {
			t.Fatalf("TopN=1 应总选最优，实际 %s", got)
		}
	}

	seen := map[game.Move]bool{}
	for i := 0; i < 100; i++ {
		seen[PickMove(res, LevelParams{TopN: 3, Slack: 1000}, rng)] = true
	}
	if len(seen) < 2 {
		t.Errorf("TopN=3 时 100 次挑选只出现 %d 种着法，随机未生效", len(seen))
	}

	seen = map[game.Move]bool{}
	for i := 0; i < 50; i++ {
		seen[PickMove(res, LevelParams{TopN: 3, Slack: 0}, rng)] = true
	}
	if len(seen) != 1 {
		t.Errorf("Slack=0 时应当只选最优，实际出现 %d 种", len(seen))
	}
}

// TestSearchInRespectsBudget 时间预算必须被遵守，且总能给出至少一层结果。
func TestSearchInRespectsBudget(t *testing.T) {
	w := loadWeights(t)
	p, err := game.ParseFEN(game.InitialFEN)
	if err != nil {
		t.Fatal(err)
	}

	for _, ms := range []int{200, 600} {
		s := New(w)
		start := time.Now()
		res := s.SearchIn(p, ms, 0)
		el := time.Since(start)

		budget := time.Duration(ms) * time.Millisecond
		if el > budget+budget/2+150*time.Millisecond {
			t.Errorf("%dms 预算实际用了 %v，超出过多", ms, el.Round(time.Millisecond))
		}
		if res.Depth < 1 {
			t.Errorf("%dms 预算下没有搜出任何深度", ms)
		}
		t.Logf("%dms 预算：depth=%d nodes=%d best=%s 实际 %v",
			ms, res.Depth, res.Nodes, res.Best, el.Round(time.Millisecond))
	}
}

// TestPoolSingleThreadDeterministic 单线程池必须与直接使用 Searcher 完全一致。
//
// 多线程结果本就不保证确定（调度与表竞争），但单线程路径必须可复现 ——
// 否则调试与回归都无从谈起。
//
// 对比对象用 Searcher.Search（迭代加深），与池的固定深度搜索语义一致；
// Searcher.SearchDepth 是「只搜一层」的对拍工具，语义不同。
func TestPoolSingleThreadDeterministic(t *testing.T) {
	w := loadWeights(t)
	p, err := game.ParseFEN(game.InitialFEN)
	if err != nil {
		t.Fatal(err)
	}

	pool := NewPool(w, 1, 16)
	a := pool.SearchDepth(p, 4)

	s := New(w)
	b := s.Search(p, 4)

	if a.Best != b.Best || a.Score != b.Score || a.Nodes != b.Nodes {
		t.Errorf("单线程池与直接搜索不一致：池 %s/%d nodes=%d，直搜 %s/%d nodes=%d",
			a.Best, a.Score, a.Nodes, b.Best, b.Score, b.Nodes)
	}
	t.Logf("迭代加深到 4 层，两者均为 %s（score=%d nodes=%d）✓", a.Best, a.Score, a.Nodes)
}

// TestPoolSearch 多线程搜索必须给出合法着法并遵守时间预算。
func TestPoolSearch(t *testing.T) {
	w := loadWeights(t)
	p, err := game.ParseFEN(game.InitialFEN)
	if err != nil {
		t.Fatal(err)
	}

	pool := NewPool(w, 0, 16)
	start := time.Now()
	res := pool.SearchIn(p, 600, 0)
	el := time.Since(start)

	if el > 600*time.Millisecond+300*time.Millisecond {
		t.Errorf("超出预算过多：%v", el.Round(time.Millisecond))
	}
	if res.Depth < 1 {
		t.Fatal("多线程搜索没有搜出深度")
	}
	if !p.IsLegal(res.Best) {
		t.Errorf("bestmove %s 非法", res.Best)
	}
	t.Logf("%d 线程：depth=%d best=%s score=%d nodes=%d 用时 %v",
		pool.Threads(), res.Depth, res.Best, res.Score, res.Nodes, el.Round(time.Millisecond))
}

// TestPoolSpeedsUp 多线程吞吐应当高于单线程 —— Lazy SMP 是否真的有效的唯一判据。
func TestPoolSpeedsUp(t *testing.T) {
	if RecommendedThreads() < 2 {
		t.Skip("单核机器，跳过并行加速验证")
	}
	w := loadWeights(t)
	p, err := game.ParseFEN(game.InitialFEN)
	if err != nil {
		t.Fatal(err)
	}

	single := New(w)
	start := time.Now()
	rs := single.SearchIn(p, 800, 0)
	elS := time.Since(start)

	pool := NewPool(w, 0, 16)
	start = time.Now()
	rp := pool.SearchIn(p, 800, 0)
	elP := time.Since(start)

	npsS := float64(rs.Nodes) / elS.Seconds()
	npsP := float64(rp.Nodes) / elP.Seconds()
	t.Logf("单线程 %.0f 节点/秒（depth %d）｜%d 线程 %.0f 节点/秒（depth %d）｜吞吐比 %.2f×",
		npsS, rs.Depth, pool.Threads(), npsP, rp.Depth, npsP/npsS)

	if npsP <= npsS {
		t.Errorf("多线程吞吐未超过单线程：%.0f vs %.0f 节点/秒", npsP, npsS)
	}
}

// TestSearchAtLevel 低档位应明显浅于高档位，且都给出合法着法。
func TestSearchAtLevel(t *testing.T) {
	w := loadWeights(t)
	p, err := game.ParseFEN(game.InitialFEN)
	if err != nil {
		t.Fatal(err)
	}
	pool := NewPool(w, 1, 16)
	rng := rand.New(rand.NewSource(7))

	mv1, res1 := pool.SearchAtLevel(p, 1, rng)
	mv16, res16 := pool.SearchAtLevel(p, 16, rng)

	if !p.IsLegal(mv1) || !p.IsLegal(mv16) {
		t.Fatalf("档位搜索给出了非法着法：1 档 %s，16 档 %s", mv1, mv16)
	}
	if res16.Depth <= res1.Depth {
		t.Errorf("16 档深度 %d 未超过 1 档的 %d", res16.Depth, res1.Depth)
	}
	t.Logf("1 档: %s depth=%d nodes=%d｜16 档: %s depth=%d nodes=%d",
		mv1, res1.Depth, res1.Nodes, mv16, res16.Depth, res16.Nodes)
}
