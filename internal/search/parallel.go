package search

import (
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/nnue"
)

// maxThreads 是搜索线程数的硬上限。
//
// 超过物理核数会因上下文切换与缓存抖动反而变慢，低功耗 NAS 上尤其明显；
// 8 已能覆盖主流设备，再高收益也会被内存带宽吃掉。
const maxThreads = 8

// RecommendedThreads 返回自动探测的搜索线程数。
//
// 规则：留一核给 HTTP/WS 服务，其余都给搜索，再夹到 [1, maxThreads]。
// 原版皮卡鱼跑单线程，所以多线程是我们补偿单核速度劣势的主要手段 ——
// 即便单核只有 C++ 的 50%，4 线程也能反超。
func RecommendedThreads() int {
	n := runtime.NumCPU()
	if n <= 1 {
		return 1
	}
	t := n - 1
	if t > maxThreads {
		t = maxThreads
	}
	return t
}

// Pool 是一组共享置换表的搜索线程（Lazy SMP）。
//
// Lazy SMP 让多个线程各自搜索同一局面，靠共享置换表互相启发：一个线程搜出的
// 结果会剪短其他线程的搜索。它不保证确定性（线程调度与表竞争都会影响结果），
// 但相比单线程通常能提升 1.5~2.5 倍有效算力。
//
// 关于置换表的无锁读写：这里**有意**不加锁。ttEntry 固定 16 字节，
// 撕裂读最坏的情况是 key 与其余字段不一致 —— 而 probe 会校验 key，
// 所以撕裂只会表现为「未命中」，不会返回错误分值。两个线程同时写同一 key
// 的同一槽位时可能出现字段混合，但两者写入的着法都合法，影响仅限搜索效率。
// 这也是 Stockfish / Pikafish 的做法。代价是不能对该表跑 -race 竞态检测。
type Pool struct {
	w         *nnue.Weights
	tt        *TranspositionTable
	stop      atomic.Bool
	searchers []*Searcher
	threads   int
}

// NewPool 构造线程池；threads <= 0 时自动探测，ttSizeMB <= 0 用默认值。
func NewPool(w *nnue.Weights, threads, ttSizeMB int) *Pool {
	if threads <= 0 {
		threads = RecommendedThreads()
	}
	if threads < 1 {
		threads = 1
	}
	if threads > maxThreads {
		threads = maxThreads
	}
	if ttSizeMB <= 0 {
		ttSizeMB = DefaultTTSizeMB
	}

	tt := NewTranspositionTable(ttSizeMB)
	p := &Pool{
		w:         w,
		tt:        tt,
		searchers: make([]*Searcher, threads),
		threads:   threads,
	}
	for i := range p.searchers {
		// 所有线程共享同一个置换表与中止标志，但各自持有启发式表与累加器。
		p.searchers[i] = &Searcher{w: w, tt: tt, stop: &p.stop}
	}
	return p
}

// Threads 返回线程数。
func (p *Pool) Threads() int { return p.threads }

// Stop 请求中止当前搜索（上层取消或超时）。下一个搜索开始时会自动清除。
func (p *Pool) Stop() { p.stop.Store(true) }

// TTLen 返回共享置换表容量（项数）。
func (p *Pool) TTLen() int { return p.tt.Len() }

// SearchTime 在时间预算内做多线程迭代加深搜索。
func (p *Pool) SearchTime(pos *game.Position, limit TimeLimit, maxDepth int) Result {
	return p.run(pos, limit, maxDepth)
}

// SearchIn 是 SearchTime 的便捷形式（按毫秒）。
func (p *Pool) SearchIn(pos *game.Position, ms int, maxDepth int) Result {
	d := time.Duration(ms) * time.Millisecond
	return p.run(pos, TimeLimit{Soft: d * 6 / 10, Hard: d}, maxDepth)
}

// SearchDepth 固定深度上界、无时间限制的多线程搜索（内部走迭代加深，
// 与 Searcher.Search 语义一致，不是 Searcher.SearchDepth 的「只搜一层」）。
// 用于对照单线程结果与测速。
func (p *Pool) SearchDepth(pos *game.Position, depth int) Result {
	return p.run(pos, TimeLimit{}, depth)
}

// Clear 清空共享置换表与各线程的启发式表。
//
// 只在「换局」或需要可复现结果时调用：正常连续对弈**不应**每步清表 ——
// 上一步的局面与搜索经验对下一步高度相关，清掉等于每步从头开始。
func (p *Pool) Clear() {
	p.tt.Clear()
	for _, s := range p.searchers {
		s.clearHeuristics()
	}
}

// run 是所有多线程搜索的公共入口。
//
// 注意这里**不**清置换表与启发式表：它们按局面/着法索引，跨步乃至跨局复用
// 都是正确的，且能显著提升棋力。需要重置时显式调 Clear()。
func (p *Pool) run(pos *game.Position, limit TimeLimit, maxDepth int) Result {
	p.stop.Store(false)
	for _, s := range p.searchers {
		s.resetStats()
		s.nodes = 0
	}

	if p.threads == 1 {
		return p.searchers[0].searchLoop(pos, limit, maxDepth, true)
	}

	// 必须在启动协程前就把局面复制好：主线程搜索时会不断 Make/Unmake
	// 同一份 Position，辅助线程同时读它会构成数据竞争。
	clones := make([]*game.Position, p.threads)
	for i := 1; i < p.threads; i++ {
		clones[i] = pos.Clone()
	}

	var wg sync.WaitGroup
	for i := 1; i < p.threads; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			p.worker(idx, clones[idx], maxDepth)
		}(i)
	}

	// 主线程负责计时与最终结果：只有它启用了计时器。
	res := p.searchers[0].searchLoop(pos, limit, maxDepth, true)

	p.stop.Store(true) // 主线程收工，通知辅助线程退出
	wg.Wait()

	var total int64
	for _, s := range p.searchers {
		total += s.nodes
	}
	res.Nodes = total
	return res
}

// worker 是辅助线程的搜索循环。
//
// 起始深度按线程号错开，让各线程在不同层上工作以减少重复劳动；之后逐层加深，
// 直到主线程置位中止标志。时间限制模式下 maxDepth 实际是「无限」，
// 所以辅助线程会一直工作到超时，不会提前空转。
func (p *Pool) worker(idx int, pos *game.Position, maxDepth int) {
	s := p.searchers[idx]
	if maxDepth <= 0 {
		maxDepth = MaxPly - 1
	}

	// 辅助线程不经过 searchLoop，必须在这里自己对齐评估侧局面与累加器。
	// 少了这一步，s.pos / s.acc 会停留在上一次搜索的位置（甚至是上一局的局面），
	// 增量差集的基准就错了：评估值静默偏移，子力计数一路累减到负数，
	// 最终在 LayerStackBucket 上越界 panic（实测 -4）。
	s.prepare(pos)

	start := 1 + idx
	if start > maxDepth {
		start = maxDepth
	}
	// 各线程各自维护期望窗口的上一层分值：窗口是每线程私有的搜索策略，
	// 共享的只有置换表。
	prev, hasPrev := -Infinity, false
	for d := start; d <= maxDepth; d++ {
		if s.stopped() {
			return
		}
		roots, ok := s.searchRootAspiration(pos, d, prev, hasPrev)
		if !ok || len(roots) == 0 || s.stopped() {
			return
		}
		prev, hasPrev = roots[0].Score, true
		s.ageHistory()
	}
}
