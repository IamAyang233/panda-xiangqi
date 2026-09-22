package engine

import (
	"context"
	"testing"
	"time"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
)

// SimpleEngine 必须响应 ctx 取消（2026-09-21 审查第六轮发现）。
//
// 它是「内嵌引擎/皮卡鱼都不可用」时的最后兜底，也是最可能出现在低配设备上的路径。
// 此前 BestMove 全文不读 ctx（`ctx` 只出现在签名里），于是上层的一切取消 ——
// 残局重开作废在飞应着、离开对局、悔棋 —— 对它统统无效，请求要等满整个档位预算
// （最高档 3.5s）才返回；`Hint` 更是拿 context.Background() 去调，完全不可中断。
//
// 判据用**耗时**而不是返回值：取消后应远早于预算返回。16 档预算 3.5s，
// 取消发生在 50ms，取 2s 作界限 —— 宽松到不怕机器慢，又紧到能抓住「跑满预算」。
func TestSimpleEngineHonorsContextCancel(t *testing.T) {
	pos, err := game.ParseFEN("rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	e := NewSimpleEngine()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	mv, err := e.BestMove(ctx, pos, 16) // 16 档预算 3.5s
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("取消不应导致报错（应返回一个可用着法）: %v", err)
	}
	if !pos.IsLegal(mv) {
		t.Fatalf("取消后返回的着法不合法: %s", mv.String())
	}
	if elapsed > 2*time.Second {
		t.Errorf("ctx 取消未被响应：耗时 %v（预算 3.5s，取消于 50ms）—— "+
			"重开/离开对局都取消不掉这次搜索", elapsed)
	}
}

// 已取消的 ctx 传入时应立即返回，不进入搜索。
func TestSimpleEngineAlreadyCancelledReturnsFast(t *testing.T) {
	pos, err := game.ParseFEN("rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	e := NewSimpleEngine()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 进来之前就已取消

	start := time.Now()
	if _, err := e.BestMove(ctx, pos, 16); err != nil {
		t.Fatalf("应返回可用着法而非报错: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("已取消的 ctx 应尽快返回，实际耗时 %v", elapsed)
	}
}
