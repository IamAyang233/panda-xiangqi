package search

import (
	"testing"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
)

// TestSearchTreeEfficiency 锁住「同样节点预算能搜多深」，防止后续剪枝 / LMR
// 改动把搜索树悄悄放大。
//
// 这类退化不会让任何功能性测试失败 —— 结果依然正确，只是每步棋想得更浅、
// 棋力慢慢变弱。唯一能抓到它的就是「每个节点值多少深度」。
//
// **为什么不用有效分支因子**（EBF = sqrt(N(d+2)/N(d))，本文件早先的写法）：
// 它是**比值**，当剪枝把浅层的树削得更小时，比值反而升高 —— 实测把安静 futility
// 的余量对齐皮卡鱼之后，depth 8 的节点少了 17%、depth 10 基本持平（268076 vs
// 266575），EBF 却从 1.90 升到 2.09，看起来像退化。用比值判「剪枝变强/变弱」会
// **系统性误报**。真正决定棋力的是固定节点预算下能搜多深，它不随树的形状置换失真。
//
// 阈值底下是「不能比现在差」，所以引擎正常变强时这里不会挡路；一旦有人把剪枝
// 削弱到明显更浅，它会失败。维护方式：确认变强后把阈值往上提。
func TestSearchTreeEfficiency(t *testing.T) {
	if testing.Short() {
		t.Skip("会跑十几秒")
	}
	w := loadWeights(t)

	// 用安静局面语料：残局里的局面大多已将杀，树很小，量不出剪枝强度。
	fens := loadQuietFENs(t)
	if len(fens) > 20 {
		fens = fens[:20]
	}
	const budget = 60000

	var sumDepth int
	for _, f := range fens {
		p, err := game.ParseFEN(f)
		if err != nil {
			t.Fatal(err)
		}
		sumDepth += New(w).SearchNodes(p, budget).Depth
	}
	mean := float64(sumDepth) / float64(len(fens))
	t.Logf("固定 %d 节点、%d 个安静局面：平均深度 %.2f", budget, len(fens), mean)

	// 实测 14.70（本机，固定 6 万节点、20 个安静局面）。测量完全确定性
	// （单线程 + 固定节点预算），所以阈值可以贴得比较紧。
	// 这个数一路上涨：12.30（反 futility 余量对齐前）→ 14.55（对齐后）→
	// 14.70（加入 IIR 后），阈值跟着提到 14.0 也就顺手锁住了这两项 ——
	// 谁把它们改回去，这里就会失败。
	// 维护方式：确认某次改动真的让引擎变强后，把阈值往上提。
	const floor = 14.0
	if mean < floor {
		t.Errorf("固定 %d 节点只能搜到平均 %.2f 层（下限 %.1f）：剪枝或 LMR 被改弱了，每个节点值不到应有的深度",
			budget, mean, floor)
	}
}
