package engine

import (
	"context"
	"runtime"
	"testing"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/search"
)

// 本文件是「被取消的那次搜索，其迟到中止打到下一次搜索」的回归哨兵。
//
// 现象：watchCancel 的监听协程若在调用方返回**之后**才执行 `pool.Stop()`，而下一次
// 搜索开头会 `run()` 里的 `stop.Store(false)`，那次迟到的 Stop 就打在新搜索上 ——
// 表现为「点了取消、立刻重下，新的一步却几乎没算」。
//
// ⚠️ 「取消的时刻」必须卡准，否则测不到东西（第一版就踩了这个坑）：
//
//	取消发生在搜索**进行中**：ctx.Done() 正是把搜索停下来的原因 ⇒ 监听协程早就
//	  执行完 Stop 并退出了，根本不存在「迟到」。实测这种写法即使把修复去掉也全过。
//	取消发生在搜索**自然结束的那一瞬间**：此时 done 已关闭、ctx 又刚被取消，
//	  监听协程的 select 两个分支同时就绪 ⇒ 才可能选中 ctx.Done() 并迟到。
//
// 所以这里让搜索用**深度上限**自然结束（1 档是 MaxDepth=2，远早于它的 60ms 时间
// 预算），并在它返回的同一瞬间取消。
//
// 判定同 timemgr 的哨兵：第二次搜索用**深度上限**、不计时 ⇒ 必然跑满，判据不依赖
// 机器快慢。⚠️ 早期用时间做判据踩过坑：-race 下引擎慢一个数量级，60ms 只到 d5，
// 「低于下限」就只说明预算不够、不说明被打断。
//
// ⚠️ 与 timemgr 的哨兵同属「要靠调度运气」的一类，不是确定性复现：用
// GOMAXPROCS(1) + 多轮重复把概率堆上去。判别力是实测过的：把 watchCancel 里的
// 等待去掉，本测试会抓到「第二次搜索只到 d1/d2」。
func TestCancelledSearchDoesNotStopNextSearch(t *testing.T) {
	if testing.Short() {
		t.Skip("竞态哨兵要跑上百轮搜索，-short 下跳过")
	}
	e := NewNativeEngine(requireWeights(t), 1)
	defer e.Close()
	if err := e.Warmup(); err != nil {
		t.Fatalf("加载权重失败: %v", err)
	}

	p, err := game.ParseFEN("rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1")
	if err != nil {
		t.Fatal(err)
	}

	// 单 P：协程切换点更集中，撞上窗口的概率更高。
	oldProcs := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(oldProcs)

	const (
		rounds = 300
		// 第二次搜索的深度上限。无时间限制 ⇒ 必然跑满；被打断则只到 d1/d2。
		secondDepth = 8
	)

	for i := 0; i < rounds; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = e.BestMove(ctx, p, 1) // 1 档 MaxDepth=2 ⇒ 自然结束
			// 搜索已经返回（内部 stop() 已 close(done)）的同一瞬间取消 ——
			// 让监听协程面对「done 已关 + ctx 刚取消」的两难。
			cancel()
		}()
		<-done

		// 第二次：不计时、跑到 d8 为止。若上一次的中止迟到，这里会立刻被打断。
		// 直接走池（而不是 e.SearchTimed）：要的就是「不经引擎层任何清理、
		// 紧接着再来一次搜索」这个最苛刻的顺序。
		res := e.pool.SearchTime(p, search.TimeLimit{}, secondDepth)
		if res.Depth != secondDepth {
			t.Fatalf("第 %d 轮：第二次搜索只到 d%d（应跑满 d%d）—— "+
				"上一次被取消的搜索，它的迟到中止打到了这一次", i, res.Depth, secondDepth)
		}
	}
}
