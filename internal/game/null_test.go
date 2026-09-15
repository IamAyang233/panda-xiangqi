package game

import (
	"math/rand"
	"testing"
)

// TestMakeNullRoundTrip 空着必须可完全还原：FEN、Key、轮次、步数计数。
func TestMakeNullRoundTrip(t *testing.T) {
	p, err := ParseFEN(InitialFEN)
	if err != nil {
		t.Fatal(err)
	}
	key0, fen0 := p.Key, p.FEN()
	turn0, half0, full0 := p.Turn, p.Halfmove, p.Fullmove

	p.MakeNull()
	if p.Turn == turn0 {
		t.Error("空着后轮次应切换")
	}
	if p.Key == key0 {
		t.Error("空着后 Key 应变化（异或 sideKey）")
	}

	p.UnmakeNull()
	if p.Key != key0 || p.FEN() != fen0 {
		t.Errorf("空着未还原：\n原 %s\n后 %s", fen0, p.FEN())
	}
	if p.Turn != turn0 || p.Halfmove != half0 || p.Fullmove != full0 {
		t.Errorf("状态未还原：Turn %d/%d Halfmove %d/%d Fullmove %d/%d",
			p.Turn, turn0, p.Halfmove, half0, p.Fullmove, full0)
	}
	if p.MoveCount() != 0 {
		t.Errorf("历史栈应清空，实际 %d 条", p.MoveCount())
	}
}

// TestMakeNullMixedWithMoves 空着与真实着法交错时，逐层回退必须精确还原。
func TestMakeNullMixedWithMoves(t *testing.T) {
	rng := rand.New(rand.NewSource(0x11))
	p, err := ParseFEN(InitialFEN)
	if err != nil {
		t.Fatal(err)
	}
	key0, fen0 := p.Key, p.FEN()

	type step struct{ wasNull bool }
	var steps []step

	for i := 0; i < 60; i++ {
		if !p.InCheck(p.Turn) && rng.Intn(3) == 0 {
			p.MakeNull()
			steps = append(steps, step{true})
			continue
		}
		moves := p.LegalMoves(p.Turn)
		if len(moves) == 0 {
			break
		}
		p.Make(moves[rng.Intn(len(moves))])
		steps = append(steps, step{})
	}

	for i := len(steps) - 1; i >= 0; i-- {
		if steps[i].wasNull {
			p.UnmakeNull()
		} else {
			p.Unmake()
		}
	}
	if p.Key != key0 || p.FEN() != fen0 {
		t.Errorf("交错走后未还原：\n原 %s\n后 %s", fen0, p.FEN())
	}
	if p.MoveCount() != 0 {
		t.Errorf("历史栈应清空，实际 %d 条", p.MoveCount())
	}
}

// TestRepetitionIgnoresNull 空着不参与重复计数：走空着再回来不应被当成重复局面。
func TestRepetitionIgnoresNull(t *testing.T) {
	p, err := ParseFEN(InitialFEN)
	if err != nil {
		t.Fatal(err)
	}
	if n := p.RepetitionCount(); n != 1 {
		t.Fatalf("初始局面重复数应为 1，实际 %d", n)
	}
	p.MakeNull()
	p.UnmakeNull()
	if n := p.RepetitionCount(); n != 1 {
		t.Errorf("空着往返后重复数仍应为 1，实际 %d", n)
	}
}
