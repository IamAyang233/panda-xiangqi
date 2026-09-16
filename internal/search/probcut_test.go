package search

import (
	"testing"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
)

// TestProbCutIsWired ProbCut 的接线守卫。
//
// 背景：ProbCut 默认关闭（实测净亏损，见 alphaBeta 里的注释），所以下面这些
// 「启用后才成立」的行为断言必须显式打开开关来测 —— 否则代码烂掉也不会有人发现。
//
// 这一项盯的是**机制静默失效**这一类失败，它们的共同特征是「一切看起来都正常、
// 结果完全正确，只是那段代码根本没执行」。本项目已经踩过两次：
//
//   - 静的着法 SEE 剪枝写成在落子后调用 SeeGE → 8.7 万次调用剪掉 0 次，节点数
//     逐位不变（守卫：TestSEEQuietWiring）。
//   - ProbCut 的候选筛选最初写成「遇到第一个安静着法就 break」，而排序分里
//     TT 着法（1<<24）高于一切吃子 —— TT 着法恰是安静着法时它排在首位，
//     一进循环就 break，ProbCut 整个被跳过。
//
// 所以判据用「节点数必须真的变化」而不是某个效应量的大小：效应量会随其它剪枝
// 浮动（实测 ProbCut 只值 0.992×，很轻），但「一个都没执行」永远是逐位相同。
//
// 第二个断言盯 SEE 筛选是否被真正读取 —— 删掉筛选（考虑全部吃子）后安静局面
// 从 0.992× 变成 1.039×，是本项目里 SEE 唯一测出正向贡献的地方，值得有条守卫。
//
// ⚠️ 写这类守卫的第一道坎：`probCutEnabled` 是**环境变量门控、默认关**的包级变量，
// 所以「开」必须显式置位，不能只靠不调 DisableProbCut()。我第一版就是只关了
// 「关」那侧的实例，结果三组配置全是关，节点数逐位相同 —— 看起来像「机制静默
// 失效」，其实是测试自己没打开开关。见下方 prevPC 处。
func TestProbCutIsWired(t *testing.T) {
	w := loadWeights(t)
	fens := loadQuietFENs(t)
	// 取前 30 个局面、深度 10：实测这个规模下 ProbCut 的效应是 0.971×，
	// 信号足够强；更浅的深度（8）只有 0.993×，余量太薄，容易被无关改动压成 0。
	if len(fens) > 30 {
		fens = fens[:30]
	}
	const depth = 10

	prevPC := probCutEnabled
	defer func() { probCutEnabled = prevPC }()

	run := func(on, noSeeFilter bool) int64 {
		probCutEnabled = on
		var sum int64
		for _, fen := range fens {
			pos, err := game.ParseFEN(fen)
			if err != nil {
				t.Fatal(err)
			}
			s := New(w)
			if noSeeFilter {
				s.DisableProbCutSeeFilter()
			}
			sum += s.Search(pos, depth).Nodes
		}
		return sum
	}

	off := run(false, false)
	on := run(true, false)
	onNoSee := run(true, true)

	if on == off {
		t.Errorf("启用 ProbCut 后节点数逐位不变（%d）：机制完全没有生效（判断条件恒假、"+
			"剪枝块被删、或开关没真正打开）。注意这一项只能挡住「完全没执行」——"+
			"实测把候选筛选写成「遇到第一个安静着法就 break」（TT 着法恰为安静着法时"+
			"会跳过整个列表）只值 0.013%%，节点数看不出，靠代码注释里的说明防。", on)
	}
	if onNoSee == on {
		t.Errorf("关掉 SEE 筛选后节点数逐位不变（%d）：筛选没有被真正读取（阈值可能被改成"+
			"恒真，或 SeeGE 的调用时机错了）。实测去掉筛选会让安静局面从 0.971× 变成"+
			"1.090×，所以两者本该有明显差异。", on)
	}
	t.Logf("ProbCut 关 %d ｜ 开 %d（%.3f×）｜ 开但去掉 SEE 筛选 %d（%.3f×）",
		off, on, float64(on)/float64(off), onNoSee, float64(onNoSee)/float64(off))
}
