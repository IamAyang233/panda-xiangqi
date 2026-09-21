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
		return Result{Nodes: s.nodes, Makes: s.makes}
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
		if s.onIter != nil {
			// ⚠️ Nodes/Makes 平时是循环结束后才赋的 —— 回调要的是「当时」的累计值，
			// 必须先补上再报，否则 UI 看到的节点数恒为 0。
			res.Nodes, res.Makes = s.nodes, s.makes
			s.onIter(res)
		}
	}
	res.Nodes = s.nodes
	res.Makes = s.makes
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
		return Result{Nodes: s.nodes, Makes: s.makes}
	}
	res := Result{Best: roots[0].Move, Score: roots[0].Score, Depth: 1, Roots: roots}
	s.ageHistory()
	if s.onIter != nil {
		// 第 1 层也要报：UI 才有「已经在思考」的即时反馈。
		s.onIter(Result{Best: res.Best, Score: res.Score, Depth: 1, Nodes: s.nodes, Makes: s.makes})
	}

	if maxDepth == 1 {
		res.Nodes = s.nodes
		res.Makes = s.makes
		return res
	}

	start := time.Now()

	// 只有在给了硬限、且由本线程负责计时时才启动计时器。
	// 没有硬限（固定深度搜索）就一直搜到 maxDepth。
	if withTimer && limit.Hard > 0 {
		done := make(chan struct{})
		finished := make(chan struct{})
		go func() {
			defer close(finished)
			t := time.NewTimer(limit.Hard)
			defer t.Stop()
			select {
			case <-t.C:
				s.Stop()
			case <-done:
			}
		}()
		// ⚠️ 必须等计时协程**真正退出**再返回，不能只 close(done) 就走。
		//
		// 否则存在这样一条路径：硬限恰好在搜索结束的瞬间到期，`t.C` 与 `done`
		// 同时就绪，select 随机选中 `t.C` ⇒ 这个 Stop() 落在本函数返回**之后**，
		// 而调用方（SearchTime / Pool.run）会在下一次搜索开始时 `stop.Store(false)`。
		// 于是这一次迟到的 Stop() 打在下一次搜索身上 —— 表现为偶发「某一步搜得
		// 异常浅」，窗口只有纳秒级，是最难排查的那类。
		//
		// 等协程退出后，无论它走了哪个分支，Stop() 都必然发生在返回之前；
		// 下一次搜索的 Store(false) 只可能在它之后，顺序就固定了。
		// 代价是返回路径多一次 channel 接收（纳秒级）。
		//
		// ⚠️ 这里**没有**配套的单元守卫，是权衡后的决定而不是遗漏：
		// 触发它需要「t.C 恰在循环最后一次 stopped() 检查与 close(done) 之间就绪」
		// 这种纳秒级 + 依赖调度的交错，写不出可靠复现。试过两版靠随机硬限 +
		// 多轮重复的哨兵，都不成立：
		//   - 用「同局面同预算的深度基准」判定被打断会**误报**：上一次被中断的
		//     搜索会污染置换表，下一次同预算的搜索本来就会浅一截（实测基准 d11、
		//     实际 d7，与是否修复无关，且因随机种子固定而每次必现）。
		//   - 用绝对深度下限则会被 -race 的减速打穿（60ms 只到 d5，「低于下限」
		//     只说明预算不够）。
		// 同类机制（watchCancel 的迟到 Stop，修法相同）有一条**判别力已验证**的
		// 守卫：internal/engine 的 TestCancelledSearchDoesNotStopNextSearch ——
		// 它用「深度上限 + 不计时」做判据（必然跑满，不受机器快慢影响），
		// 去掉等待会稳定抓到，加回等待稳定通过。
		defer func() {
			close(done)
			<-finished
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
		if s.onIter != nil {
			// ⚠️ Nodes/Makes 平时是循环结束后才赋的 —— 回调要的是「当时」的累计值，
			// 必须先补上再报，否则 UI 看到的节点数恒为 0。
			res.Nodes, res.Makes = s.nodes, s.makes
			s.onIter(res)
		}
	}
	res.Nodes = s.nodes
	res.Makes = s.makes
	return res
}
