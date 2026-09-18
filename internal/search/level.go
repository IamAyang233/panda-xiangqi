package search

import (
	"math/rand"
	"time"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
)

// LevelParams 描述一个难度档位的搜索参数。
type LevelParams struct {
	// MaxDepth 是搜索深度上限；0 表示不限（交给时间控制）。
	MaxDepth int
	// Time 是每步的时间预算。
	Time time.Duration
	// TopN 表示在根着法里分值最高的前 N 个中随机挑一个（1 = 总是选最优）。
	// 这是「人味失误」的来源：低档位不总是走最优着法，像人一样会漏看。
	TopN int
	// Slack 是分值容差（单位同评估值）。与最优分差超过 Slack 的着法不进随机池，
	// 免得最低档位走出白送车的着法。
	Slack int
}

// levels 是 16 档的参数表。
//
// 低档位主要靠 MaxDepth 限制（深度浅自然下得差，且在慢设备上耗时也可控），
// 再用 TopN/Slack 制造失误；高档位靠时间预算榨取深度，TopN = 1 走最优。
// 第 14 档起不限深度：此时唯一的约束是前台进程能给多少时间。
var levels = [16]LevelParams{
	{MaxDepth: 2, Time: 60 * time.Millisecond, TopN: 8, Slack: 400},
	{MaxDepth: 2, Time: 90 * time.Millisecond, TopN: 6, Slack: 340},
	{MaxDepth: 3, Time: 130 * time.Millisecond, TopN: 5, Slack: 280},
	{MaxDepth: 3, Time: 180 * time.Millisecond, TopN: 4, Slack: 220},
	{MaxDepth: 4, Time: 250 * time.Millisecond, TopN: 3, Slack: 170},
	{MaxDepth: 4, Time: 320 * time.Millisecond, TopN: 3, Slack: 130},
	{MaxDepth: 5, Time: 400 * time.Millisecond, TopN: 2, Slack: 100},
	{MaxDepth: 5, Time: 500 * time.Millisecond, TopN: 2, Slack: 80},
	{MaxDepth: 6, Time: 700 * time.Millisecond, TopN: 2, Slack: 60},
	{MaxDepth: 7, Time: 900 * time.Millisecond, TopN: 1},
	{MaxDepth: 8, Time: 1100 * time.Millisecond, TopN: 1},
	{MaxDepth: 9, Time: 1400 * time.Millisecond, TopN: 1},
	{MaxDepth: 10, Time: 1700 * time.Millisecond, TopN: 1},
	{MaxDepth: 0, Time: 2200 * time.Millisecond, TopN: 1},
	{MaxDepth: 0, Time: 2800 * time.Millisecond, TopN: 1},
	{MaxDepth: 0, Time: 3500 * time.Millisecond, TopN: 1},
}

// MaxLevel 是最高档位。
const MaxLevel = 16

// Level 返回档位参数；越界会被夹到 [1, MaxLevel]。
func Level(level int) LevelParams {
	if level < 1 {
		level = 1
	}
	if level > MaxLevel {
		level = MaxLevel
	}
	return levels[level-1]
}

// PickMove 按档位参数从搜索结果里挑一个着法。
//
// TopN <= 1 或没有根着法信息时直接返回最优着法。随机池取「按分值降序的前 TopN 个」
// 且与最优分差不超过 Slack —— Roots 已降序，所以一旦分差超限即可停止扫描。
func PickMove(res Result, lp LevelParams, rng *rand.Rand) game.Move {
	if len(res.Roots) == 0 {
		return res.Best
	}
	if lp.TopN <= 1 || rng == nil {
		return res.Roots[0].Move
	}

	best := res.Roots[0].Score
	n := 0
	for _, rm := range res.Roots {
		if n >= lp.TopN || best-rm.Score > lp.Slack {
			break
		}
		n++
	}
	if n <= 1 {
		return res.Roots[0].Move
	}
	return res.Roots[rng.Intn(n)].Move
}

// SearchAtLevel 按档位搜索并挑出着法，是上层（引擎/接口层）对接的入口。
//
// rng 为 nil 时不做随机挑选，总取最优着法。
func (p *Pool) SearchAtLevel(pos *game.Position, level int, rng *rand.Rand) (game.Move, Result) {
	lp := Level(level)
	res := p.SearchTime(pos, TimeLimit{Soft: lp.Time * 6 / 10, Hard: lp.Time}, lp.MaxDepth)
	return PickMove(res, lp, rng), res
}

// SearchAtLevelObserve 是 SearchAtLevel 带「迭代观察器」的版本，语义完全相同
// （同样过 PickMove，所以回调里看到的 Best 就是最终选中的着法）。
//
// 观察者只在本次搜索期间挂在主线程上，结束后摘掉 —— 池是共享的，
// 不摘会污染下一次调用。
func (p *Pool) SearchAtLevelObserve(pos *game.Position, level int, rng *rand.Rand, obs func(Result)) (game.Move, Result) {
	lp := Level(level)
	p.SetIterObserver(obs)
	defer p.SetIterObserver(nil)
	res := p.SearchTime(pos, TimeLimit{Soft: lp.Time * 6 / 10, Hard: lp.Time}, lp.MaxDepth)
	return PickMove(res, lp, rng), res
}

// SearchAtLevel 是单线程版本，语义同上（不含并行，结果确定）。
func (s *Searcher) SearchAtLevel(pos *game.Position, level int, rng *rand.Rand) (game.Move, Result) {
	lp := Level(level)
	res := s.SearchTime(pos, TimeLimit{Soft: lp.Time * 6 / 10, Hard: lp.Time}, lp.MaxDepth)
	return PickMove(res, lp, rng), res
}
