package search

import (
	"math/rand"
	"testing"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
)

// TestLevelTableMonotonic 档位参数表必须随档位单调。
//
// 这是「档位越高越强」这个承诺在配置层面的保证，而且是确定性的 ——
// 比用墙钟时间断言可靠得多：引擎一旦变快，时间断言就会失效，
// 而参数表的单调性与速度无关。
//
// 它同时补上了原来只在 internal/engine 里用耗时测的东西：那边之所以
// 测不准，是因为 1 档（MaxDepth=2）与 8 档（MaxDepth=5）都被深度提前
// 终止，时间预算差 8 倍却完全用不上。
func TestLevelTableMonotonic(t *testing.T) {
	if len(levels) != MaxLevel {
		t.Fatalf("档位表有 %d 项，MaxLevel 是 %d", len(levels), MaxLevel)
	}

	for i := 0; i+1 < MaxLevel; i++ {
		a, b := Level(i+1), Level(i+2)
		if b.Time < a.Time {
			t.Errorf("%d 档时间预算 %v 小于 %d 档的 %v", i+2, b.Time, i+1, a.Time)
		}
		if b.TopN > a.TopN {
			t.Errorf("%d 档随机池 TopN=%d 大于 %d 档的 %d（高档位应更少随机）",
				i+2, b.TopN, i+1, a.TopN)
		}
		// MaxDepth=0 表示不限深度，只在两者都有限时比较。
		if a.MaxDepth > 0 && b.MaxDepth > 0 && b.MaxDepth < a.MaxDepth {
			t.Errorf("%d 档深度上限 %d 小于 %d 档的 %d", i+2, b.MaxDepth, i+1, a.MaxDepth)
		}
	}

	weak, strong := Level(1), Level(MaxLevel)
	if weak.Time >= strong.Time {
		t.Errorf("1 档时间预算 %v 不应大于等于 %d 档的 %v", weak.Time, MaxLevel, strong.Time)
	}
	if strong.TopN != 1 {
		t.Errorf("%d 档应固定走最优着法，实际 TopN=%d", MaxLevel, strong.TopN)
	}
	if strong.MaxDepth != 0 {
		t.Errorf("%d 档应不限深度（交给时间预算），实际 MaxDepth=%d", MaxLevel, strong.MaxDepth)
	}
}

// TestPickMoveSemantics 档位「人味失误」的挑选语义。
//
// 用纯函数测，不走搜索：搜索受置换表冷热状态影响（首次 TT 冷、后续热），
// 同一局面重复调用就可能给出不同着法 —— 拿它去测确定性的挑选逻辑，
// 只会得到一个易碎的测试。挑选逻辑本身是纯函数，可以直接钉死。
func TestPickMoveSemantics(t *testing.T) {
	mv := func(from, to uint8) game.Move { return game.Move{From: from, To: to} }
	res := Result{
		Best: mv(0, 1),
		Roots: []RootMove{
			{Move: mv(0, 1), Score: 100},
			{Move: mv(0, 2), Score: 96},
			{Move: mv(0, 3), Score: 92},
			{Move: mv(0, 4), Score: 88}, // 分差 12 ≤ Slack，但超出 TopN=3
			{Move: mv(0, 5), Score: 40}, // 分差 60 > Slack
		},
	}
	rng := rand.New(rand.NewSource(1))

	// TopN=1：永远最优。
	single := LevelParams{TopN: 1}
	for i := 0; i < 100; i++ {
		if got := PickMove(res, single, rng); got != res.Best {
			t.Fatalf("TopN=1 时选中 %v，应为最优 %v", got, res.Best)
		}
	}

	// rng 为 nil 时退化为总取最优（上层不传随机源的情形）。
	for i := 0; i < 10; i++ {
		if got := PickMove(res, LevelParams{TopN: 8, Slack: 400}, nil); got != res.Best {
			t.Fatalf("rng=nil 时选中 %v，应为最优 %v", got, res.Best)
		}
	}

	// TopN=3 / Slack=50：随机池恰为前 3 个。
	// 第 4 个虽然分差在 Slack 内，但被 TopN 挡住；第 5 个超出 Slack。
	limited := LevelParams{TopN: 3, Slack: 50}
	seen := map[string]int{}
	for i := 0; i < 400; i++ {
		seen[PickMove(res, limited, rng).String()]++
	}
	if seen[mv(0, 4).String()] > 0 {
		t.Errorf("TopN=3 却选到了池外的第 4 个着法")
	}
	if seen[mv(0, 5).String()] > 0 {
		t.Errorf("选到了超出 Slack 的第 5 个着法")
	}
	for _, want := range []game.Move{mv(0, 1), mv(0, 2), mv(0, 3)} {
		if seen[want.String()] == 0 {
			t.Errorf("随机池内的着法 %v 一次都没被选中，池子缩得比 TopN 还小", want)
		}
	}

	// Slack 收紧到 3：第 2 个的分差是 4，池子收缩到只剩最优。
	tight := LevelParams{TopN: 8, Slack: 3}
	for i := 0; i < 100; i++ {
		if got := PickMove(res, tight, rng); got != res.Best {
			t.Fatalf("Slack=3 时选中 %v，随机池应只剩最优", got)
		}
	}

	// 没有根着法信息时退回 Best（搜索结果缺 Roots 的情形）。
	empty := Result{Best: mv(1, 2)}
	if got := PickMove(empty, LevelParams{TopN: 8, Slack: 400}, rng); got != empty.Best {
		t.Errorf("Roots 为空时选中 %v，应退回 Best %v", got, empty.Best)
	}
}
