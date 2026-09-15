package nnue

import (
	"math/rand"
	"testing"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
)

// accumEqual 比较两份累加结果。缓存的特征列表不参与比较 ——
// 它们只是差集计算的依据，不影响累加器数值。
func accumEqual(a, b *Accumulator) bool {
	return a.PsqAcc == b.PsqAcc && a.PsqPsqt == b.PsqPsqt &&
		a.ThrAcc == b.ThrAcc && a.ThrPsqt == b.ThrPsqt
}

// boardOfPos 由 game 局面构造本包的视图。
func boardOfPos(p *game.Position) Board {
	var b Board
	for i := 0; i < squareNB; i++ {
		b[i] = PieceFromGame(p.Board[i])
	}
	return b
}

// TestSyncToMatchesRefresh 增量同步必须与全量重建逐位相同。
//
// 这是增量累加器唯一的正确性保证：算错不会报错，只会让评估值静默偏移，
// 进而让搜索选出错误的着法。所以必须逐局面、逐元素对拍。
func TestSyncToMatchesRefresh(t *testing.T) {
	w, err := Load(flatPath)
	if err != nil {
		t.Skip("未找到展开后的权重，跳过")
	}

	p, err := game.ParseFEN(game.InitialFEN)
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(0x5A17))

	var inc, full Accumulator
	b := boardOfPos(p)
	w.Refresh(b, &full)
	w.SyncTo(b, &inc) // 零值累加器首次同步相当于全量构建
	if !accumEqual(&inc, &full) {
		t.Fatal("首次同步结果与全量重建不同")
	}

	const steps = 300
	done := 0
	for i := 0; i < steps; i++ {
		moves := p.LegalMoves(p.Turn)
		if len(moves) == 0 {
			break
		}
		p.Make(moves[rng.Intn(len(moves))])
		b = boardOfPos(p)

		w.SyncTo(b, &inc)
		w.Refresh(b, &full)
		if !accumEqual(&inc, &full) {
			t.Fatalf("第 %d 步后增量与全量不一致\nFEN: %s", i+1, p.FEN())
		}
		done++
	}
	t.Logf("连续 %d 步增量同步与全量重建逐位一致 ✓", done)
}

// TestSyncToAcrossBacktrack 模拟搜索的 DFS：前进与回退都必须能正确同步。
//
// 搜索里累加器是随走子的先后顺序被反复推进与回退的，且中间会跳过大量
// 不评估的节点，所以「能跨局面连续同步」这一点必须单独验证。
func TestSyncToAcrossBacktrack(t *testing.T) {
	w, err := Load(flatPath)
	if err != nil {
		t.Skip("未找到展开后的权重，跳过")
	}

	p, err := game.ParseFEN(game.InitialFEN)
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(0xBACC))

	var inc, full Accumulator
	check := func(what string) {
		b := boardOfPos(p)
		w.SyncTo(b, &inc)
		w.Refresh(b, &full)
		if !accumEqual(&inc, &full) {
			t.Fatalf("%s 后增量与全量不一致\nFEN: %s", what, p.FEN())
		}
	}
	check("起始")

	const depth = 12
	var stack []game.Move
	for i := 0; i < depth; i++ {
		moves := p.LegalMoves(p.Turn)
		if len(moves) == 0 {
			break
		}
		m := moves[rng.Intn(len(moves))]
		p.Make(m)
		stack = append(stack, m)
		check("前进")
	}
	for len(stack) > 0 {
		p.Unmake()
		stack = stack[:len(stack)-1]
		check("回退")
	}
	t.Logf("前进 %d 步再逐层回退，增量同步全程与全量一致 ✓", depth)
}

func BenchmarkRefreshFull(b *testing.B) {
	w, err := Load(flatPath)
	if err != nil {
		b.Skip("未找到展开后的权重，跳过")
	}
	p, _ := game.ParseFEN(game.InitialFEN)
	board := boardOfPos(p)
	var a Accumulator
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w.Refresh(board, &a)
	}
}

func BenchmarkSyncToIncremental(b *testing.B) {
	w, err := Load(flatPath)
	if err != nil {
		b.Skip("未找到展开后的权重，跳过")
	}
	p, _ := game.ParseFEN(game.InitialFEN)
	rng := rand.New(rand.NewSource(1))
	boards := make([]Board, 0, 64)
	for i := 0; i < 64; i++ {
		moves := p.LegalMoves(p.Turn)
		if len(moves) == 0 {
			break
		}
		p.Make(moves[rng.Intn(len(moves))])
		boards = append(boards, boardOfPos(p))
	}
	var a Accumulator
	w.SyncTo(boards[0], &a)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w.SyncTo(boards[i%len(boards)], &a)
	}
}
