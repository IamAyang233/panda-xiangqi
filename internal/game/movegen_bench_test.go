package game

import "testing"

// M3 性能基准：位运算版走法生成 vs 逐格步进版（genMovesRef）。
// 这是本次重构的直接收益度量。

// perftRef 逐格版 perft（与 genMovesRef 配套，仅用于基准对比）。
func perftRef(p *Position, depth int) uint64 {
	if depth == 0 {
		return 1
	}
	moves := genMovesRef(p, p.Turn)
	// 参考实现给的是伪合法着法，需过滤自将
	legal := moves[:0]
	for _, m := range moves {
		p.Make(m)
		if !p.InCheck(Opponent(p.Turn)) {
			legal = append(legal, m)
		}
		p.Unmake()
	}
	if depth == 1 {
		return uint64(len(legal))
	}
	var n uint64
	for _, m := range legal {
		p.Make(m)
		n += perftRef(p, depth-1)
		p.Unmake()
	}
	return n
}

func BenchmarkPerftBenchmark(b *testing.B) {
	pos, err := ParseFEN(InitialFEN)
	if err != nil {
		b.Fatal(err)
	}
	b.Run("bitboard/depth4", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			p := pos.Clone()
			if n := Perft(p, 4); n != 3290240 {
				b.Fatalf("节点数错误: %d", n)
			}
		}
	})
	b.Run("stepwise/depth4", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			p := pos.Clone()
			if n := perftRef(p, 4); n != 3290240 {
				b.Fatalf("节点数错误: %d", n)
			}
		}
	})
}

func BenchmarkGenMoves(b *testing.B) {
	pos, err := ParseFEN("r1ba1a3/4kn3/2n1b4/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1")
	if err != nil {
		b.Fatal(err)
	}
	b.Run("bitboard", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = pos.GenMoves(Red)
		}
	})
	b.Run("stepwise", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = genMovesRef(pos, Red)
		}
	})
}
