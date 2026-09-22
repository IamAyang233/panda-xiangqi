package session_test

// Manager 的容量与回收约束（2026-09-21 审查第六轮发现）：
// Put 原先完全没有上限，而 GC 要求「创建满 2 小时且零连接」——
// 「连上但不玩」的会话靠心跳保活就永不回收，只建不连的会话可以无限堆高。
// 这里钉住两条：上限触发淘汰、淘汰只挑空闲会话。

import (
	"testing"

	"github.com/IamAyang233/panda-xiangqi/internal/engine"
	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/llm"
	"github.com/IamAyang233/panda-xiangqi/internal/session"
)

// sharedEngines 全用例共用一个 Engine Manager。
//
// ⚠️ 别改成「每个会话新建一个」：`NewManager` 会做完整的引擎探测（PATH 查找、
// 权重候选路径的存在性检查），实测单次约 80ms —— 建 200 个会话就是 16s，
// 而容量用例本来就需要几百个会话。共享它对被测行为没有影响（Session 不会改
// Manager 的状态）。
var sharedEngines = engine.NewManager("")

func newSession(t *testing.T) *session.Session {
	t.Helper()
	return session.NewSession(session.ModeEngine, game.Red, 4, llm.DefaultConfig(), nil, sharedEngines)
}

// 建满上限后继续 Put：总数不应无限增长（会淘汰最旧的空闲会话）。
func TestManagerEvictsWhenAtCapacity(t *testing.T) {
	m := session.NewManager()
	// 上限 200 + 多投 20 个触发淘汰。⚠️ 别把数字调大：每个会话都会建一份
	// 局面与连接表，投 400 个时本用例要跑 30s+（实测）。
	const over = 20

	for i := 0; i < 200+over; i++ {
		m.Put(newSession(t))
	}

	if got := m.Count(); got > 200 {
		t.Errorf("会话数应被限制在上限 200 以内，实际 %d", got)
	}
	// ⚠️ 不断言「具体是哪一个被淘汰」：会话在同一毫秒内批量创建，`Created()` 的
	// 粒度不足以区分先后，挑中的目标由 map 遍历顺序决定。这里钉的是**容量约束**
	// 与「腾出位置」这两条，淘汰哪一个由实现自行选择。
	if got := m.Count(); got != 200 {
		t.Errorf("投了 %d 个会话后应恰好剩上限个数 200，实际 %d", 200+over, got)
	}
}

// 全部会话都有连接时不应误杀（宁可短暂超限，也不能清掉用户在用的对局）。
func TestManagerDoesNotEvictActiveSessions(t *testing.T) {
	m := session.NewManager()
	active := make([]*session.Session, 0, 205)
	for i := 0; i < 205; i++ {
		s := newSession(t)
		s.Join(&recConn{}) // 每个都挂一个连接 ⇒ 全部「在用」
		active = append(active, s)
		m.Put(s)
	}
	// 全部在用：一个都不该被淘汰。
	for _, s := range active {
		if _, ok := m.Get(s.ID); !ok {
			t.Fatalf("会话 %s 仍有用连接，不该被淘汰", s.ID)
		}
	}
	// 但一解除连接，下一次 Put 就会腾位置。
	// （这里只验证「不误杀」这一条，容量本身在上一个用例里已钉住。）
}
