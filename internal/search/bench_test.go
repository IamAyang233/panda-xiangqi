package search

import (
	"testing"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/nnue"
)

// BenchmarkSearchFixedDepth 固定深度的完整搜索，用于 profile 搜索层的成本分布。
//
// 加它的原因：把 NNUE 评估优化到 6.3 倍之后，端到端节点数只涨了 11.8%，
// 说明评估在搜索总时间里占比有限。要决定下一步优化哪里，必须先量出
// 「走法生成 / 局面操作 / 评估」各占多少，而不是继续猜。
func BenchmarkSearchFixedDepth(b *testing.B) {
	w, err := nnue.Load(flatPath)
	if err != nil {
		b.Skip("未找到展开后的权重，跳过")
	}
	p, err := game.ParseFEN(game.InitialFEN)
	if err != nil {
		b.Fatal(err)
	}
	s := New(w)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Search(p, 6) // Search 内部会 Clear，每次都是干净的迭代加深
	}
}

// BenchmarkMoveGen 单独测走法生成，用来和搜索总耗时对照。
func BenchmarkMoveGen(b *testing.B) {
	p, err := game.ParseFEN(game.InitialFEN)
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = p.LegalMoves(p.Turn)
	}
}
