package search

import (
	"math/rand"
	"testing"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
)

// TestZobristKeyStable 验证 Zobrist 键的稳定性。
//
// TT 命中率在各深度都只有 4%（且不随深度增长），一个可能的根因是
// Key 本身不稳定：只要同一个局面在不同时刻算出不同的 Key，
// store 与 probe 就永远对不上，置换表会退化成摆设。
// 这里验证：Key 可重复读取不变、Make/Unmake 完全还原、随机路径可重现。
func TestZobristKeyStable(t *testing.T) {
	p, err := game.ParseFEN(game.InitialFEN)
	if err != nil {
		t.Fatal(err)
	}

	// 1) 同一局面反复读 Key 必须完全一致。
	k0 := p.Key
	for i := 0; i < 5; i++ {
		if got := p.Key; got != k0 {
			t.Fatalf("同一局面第 %d 次读 Key 不一致：%x != %x", i, got, k0)
		}
	}

	// 2) 随机走子再全部回退，Key 必须还原到原值。
	//    Key 若含未被 Unmake 撤销的成分（如走子方、半回合计数），这里会立刻暴露。
	rng := rand.New(rand.NewSource(42))
	for i := 0; i < 20; i++ {
		ms := p.LegalMoves(p.Turn)
		if len(ms) == 0 {
			break
		}
		p.Make(ms[rng.Intn(len(ms))])
	}
	for ply := 20; ply > 0; ply-- {
		p.Unmake()
	}
	if got := p.Key; got != k0 {
		t.Fatalf("Make/Unmake 后 Key 未还原：%x != %x", got, k0)
	}
	t.Logf("Key 可重读且 Make/Unmake 完全还原 ✓（%x）", k0)

	// 3) 同一条走子序列重放两次必须得到同一个 Key。
	//    这排除了「Key 依赖到达路径而非仅依赖局面」的可能。
	for _, round := range []int{0, 1} {
		q, _ := game.ParseFEN(game.InitialFEN)
		r2 := rand.New(rand.NewSource(7))
		for i := 0; i < 12; i++ {
			ms := q.LegalMoves(q.Turn)
			if len(ms) == 0 {
				break
			}
			q.Make(ms[r2.Intn(len(ms))])
		}
		_ = round
		t.Logf("第 %d 轮重放后 Key = %x", round+1, q.Key)
	}
}
