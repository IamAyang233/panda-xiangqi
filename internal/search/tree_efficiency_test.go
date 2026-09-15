package search

import (
	"math"
	"testing"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
)

// TestSearchTreeEfficiency 锁住「同样深度需要多少节点」，防止后续剪枝 / LMR
// 改动把树悄悄放大。
//
// 这类退化不会让任何功能性测试失败 —— 结果依然正确，只是每步棋想得更浅、
// 棋力慢慢变弱。唯一能抓到它的就是树大小本身。
//
// 判据用**有效分支因子**而不是绝对节点数：EBF = sqrt(N(d+2)/N(d))，它自缩放，
// 换机器、换局面集都不会失效，只有树的形状变了才会动。
//
// 参考值（本机，本文件这三个局面、depth 8→10）：当前 LMR 线性式 1.79；
// 换成经典对数式 1.85；换成 max(线性, 对数) 2.44。阈值定 2.0 —— 留出波动余量，
// 但树一旦放大到旧水平（或引入某个“更激进”的剪枝）就会失败。
func TestSearchTreeEfficiency(t *testing.T) {
	if testing.Short() {
		t.Skip("会跑十几秒")
	}
	w := loadWeights(t)

	// 选真正的对抗局面：残局里大量局面已将杀，树很小，量不出剪枝强度。
	fens := []string{
		game.InitialFEN,
		"r1ba1a3/4kn3/2n1b4/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1",
		"2bak1b2/4a4/4b4/p1p1p3p/6p2/2P6/P3P1P1P/1C2C4/9/RNBAKABNR w - - 0 1",
	}
	const shallow, deep = 8, 10

	var nShallow, nDeep int64
	for _, f := range fens {
		p, err := game.ParseFEN(f)
		if err != nil {
			t.Fatal(err)
		}
		s := New(w)
		nShallow += s.Search(p, shallow).Nodes
		s = New(w)
		nDeep += s.Search(p, deep).Nodes
	}
	ebf := math.Pow(float64(nDeep)/float64(nShallow), 1.0/(deep-shallow))
	t.Logf("depth %d→%d：节点 %d → %d，有效分支因子 %.2f", shallow, deep, nShallow, nDeep, ebf)

	if ebf > 2.0 {
		t.Errorf("有效分支因子 %.2f 超过 2.0：同样深度要多花 %.0f%% 的节点，剪枝或 LMR 被改弱了",
			ebf, (ebf/1.79-1)*100)
	}
}
