package search

import (
	"time"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
)

// TimeLimit 是一次搜索的时间预算。
//
// 软限用于「不再开始新的迭代层」，硬限用于「立即中断当前层」。两者分开是
// 因为迭代加深的每一层耗时可能相差数倍，只设一个限会导致要么频繁丢弃跑不完的
// 整层（白白浪费），要么大幅超出用户能接受的等待。
type TimeLimit struct {
	Soft time.Duration
	Hard time.Duration
}

// SearchNodes 在节点预算内做迭代加深，返回最后「完整跑完」的一层。
//
// 这是诊断搜索效率的专用入口：把思考时间换成固定的节点预算后，
// 两个引擎间的计算速度差异就被消除了，剩下的纯粹是搜索效率的差距
// （同样的树规模谁能挖得更深）。排查速度以外的问题时应优先用它。
func (s *Searcher) SearchNodes(p *game.Position, maxNodes int64) Result {
	s.stop.Store(false)
	s.nodes = 0
	s.Clear()
	return s.searchLoopNodes(p, maxNodes)
}

func (s *Searcher) searchLoopNodes(p *game.Position, maxNodes int64) Result {
	s.prepare(p)

	roots, ok := s.rootSearch(p, 1, -Infinity, Infinity)
	if !ok || len(roots) == 0 {
		return Result{Nodes: s.nodes}
	}
	res := Result{Best: roots[0].Move, Score: roots[0].Score, Depth: 1, Roots: roots}
	s.ageHistory()

	// 每层结束才检查预算：半途而废的层分值不可信，与 searchLoop 一样
	// 只采用完整跑完的层。
	for d := 2; d < MaxPly-1; d++ {
		if s.nodes >= maxNodes {
			break
		}
		roots, ok := s.searchRootAspiration(p, d, res.Score, res.Depth > 0)
		if !ok || len(roots) == 0 || s.stopped() {
			break
		}
		res.Score, res.Best, res.Depth, res.Roots = roots[0].Score, roots[0].Move, d, roots
		s.ageHistory()
	}
	res.Nodes = s.nodes
	return res
}

// SearchTime 在时间预算内做迭代加深搜索（单线程）。
func (s *Searcher) SearchTime(p *game.Position, limit TimeLimit, maxDepth int) Result {
	s.stop.Store(false)
	s.nodes = 0
	s.Clear()
	return s.searchLoop(p, limit, maxDepth, true)
}

// SearchIn 是 SearchTime 的便捷形式：给定毫秒预算，硬限为预算、软限为预算的 60%。
//
// 软限取 60% 是常见经验值：留出余量让最后一层有机会跑完，
// 否则某层恰好跨过软限就白算了。
func (s *Searcher) SearchIn(p *game.Position, ms int, maxDepth int) Result {
	d := time.Duration(ms) * time.Millisecond
	return s.SearchTime(p, TimeLimit{Soft: d * 6 / 10, Hard: d}, maxDepth)
}

// searchLoop 是迭代加深主体。
//
// 它**不**重置中止标志、也**不**清理置换表与启发式表 —— 这些由调用方负责，
// 因为多线程下它们是共享状态，每个线程各清一次会互相破坏。
//
// withTimer 控制是否自行启动计时器：线程池里只有主线程需要，
// 辅助线程由主线程的中止信号统一收尾。
func (s *Searcher) searchLoop(p *game.Position, limit TimeLimit, maxDepth int, withTimer bool) Result {
	if maxDepth <= 0 {
		maxDepth = MaxPly - 1
	}

	// 对齐评估侧局面与累加器。放在这里是因为所有搜索路径（单线程、线程池）
	// 最终都会走到它，漏掉对齐会让增量累加器拿错误基准做差集。
	s.prepare(p)

	// 第 1 层不计时：代价极小，但没有它调用方可能拿不到任何着法。
	roots, ok := s.rootSearch(p, 1, -Infinity, Infinity)
	if !ok || len(roots) == 0 {
		return Result{Nodes: s.nodes}
	}
	res := Result{Best: roots[0].Move, Score: roots[0].Score, Depth: 1, Roots: roots}
	s.ageHistory()

	if maxDepth == 1 {
		res.Nodes = s.nodes
		return res
	}

	start := time.Now()

	// 只有在给了硬限、且由本线程负责计时时才启动计时器。
	// 没有硬限（固定深度搜索）就一直搜到 maxDepth。
	if withTimer && limit.Hard > 0 {
		done := make(chan struct{})
		defer close(done)
		go func() {
			t := time.NewTimer(limit.Hard)
			defer t.Stop()
			select {
			case <-t.C:
				s.Stop()
			case <-done:
			}
		}()
	}

	for d := 2; d <= maxDepth; d++ {
		// 软限：已用时间达到软限就不开新层，避免开了又被打断。
		if limit.Soft > 0 && time.Since(start) >= limit.Soft {
			break
		}
		roots, ok := s.searchRootAspiration(p, d, res.Score, res.Depth > 0)
		if !ok || len(roots) == 0 || s.stopped() {
			break
		}
		res.Score, res.Best, res.Depth, res.Roots = roots[0].Score, roots[0].Move, d, roots
		s.ageHistory()
	}
	res.Nodes = s.nodes
	return res
}
