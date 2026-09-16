package search

import (
	"os"
	"strings"
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

// BenchmarkSearchFixedNodes 固定节点预算的搜索。
//
// 相比固定深度，它排除「节点数变化」的干扰 —— 做「把某段计算替换成空实现」
// 这类性能上界实验时，搜索行为会变、节点数也会变，只有锁死预算才能比出
// 那段计算的真实占比。
func BenchmarkSearchFixedNodes(b *testing.B) {
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
		s.SearchNodes(p, 100000)
	}
}

// quietFENsForBench 读取安静局面语料（与 loadQuietFENs 同源，但可用于基准）。
func quietFENsForBench(b *testing.B, n int) []string {
	b.Helper()
	raw, err := os.ReadFile("testdata/quiet_fens.txt")
	if err != nil {
		b.Skip("未找到安静局面语料，跳过")
	}
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// BenchmarkQuietFixedNodes 与 TestSearchTreeEfficiency 同口径：20 个中局安静局面 ×
// 固定 6 万节点，每个局面用全新的 Searcher。
//
// 与 BenchmarkSearchFixedNodes（初始局面）**配对使用** —— 同一个改动在两套语料上的
// 收益可以差近一倍，只在初始局面上量会系统性高估。实测（把 computeRay 的滑动攻击
// 从「四方向」改成「只算 s 所在的那一个方向」，各 8~9 样本交替 A/B、两组区间都
// 完全不重叠）：
//
//	初始局面（32 子在场）  1083.8 → 919.0 ms/10 万节点  = +17.9%
//	中局 20 局面（子力已减） 9519.3 → 8625.2 ms/120 万节点 = +10.4%
//
// 原因是威胁链路（computeRay 的候选子数）随在场子数增长，初始局面最重。
func BenchmarkQuietFixedNodes(b *testing.B) {
	w, err := nnue.Load(flatPath)
	if err != nil {
		b.Skip("未找到展开后的权重，跳过")
	}
	fens := quietFENsForBench(b, 20)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, f := range fens {
			p, err := game.ParseFEN(f)
			if err != nil {
				b.Fatal(err)
			}
			New(w).SearchNodes(p, 60000)
		}
	}
}
