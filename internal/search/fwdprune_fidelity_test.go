package search

import (
	"fmt"
	"testing"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/nnue"
)

// TestForwardPruningFidelity 检验前向剪枝是否过度。
//
// 前向剪枝（reverse futility / futility / LMP）按静态评估推断
// 「这个分支不可能更好」并直接跳过，因此**会改变搜索结果** —— 这是它的设计目的，
// 用少量准确性换大量速度。风险在于余量给得太宽时会剪掉真正的最佳着法。
//
// 检验方法：同一深度下，把「全开」与「关掉前向剪枝、并关掉期望窗口」的结果
// 逐一对比 —— 参照组要尽量接近「信息最完整」的搜索。只关前向剪枝是不够的：
// 期望窗口同样会通过置换表改变搜索轨迹（原因见下方 off.DisableAspiration 处）。
//
// 判据用**分值差**而不是着法一致率：同一局面常存在若干近似等价的着法
// （分值差只有几十 centipawn），剪枝后选中另一个并不损害棋力。
// 真正有害的是「把将杀或关键着法剪没」，那会在分值上留下上千的缺口 ——
// 实测确实出现过一次：漏掉给对手将军的安静着法，把 depth 6 的将杀剪没了
// （4194 vs 524283）。修复办法是剪枝前先落子判断该步是否将军。
//
// 跑两组语料：5 个有代表性局面（覆盖初始 / 开局 / 中局 / 残局的结构性差异），
// 以及安静语料的前 20 个真实对局局面。只在前一组上通过说明不了什么，
// 那几个局面可能碰巧没被剪过头 —— 实测把反 futility 的余量从固定 120*depth
// 换成皮卡鱼的曲线后，第二组的平均分值差反而从 71 降到 56、最大从 419 降到 237，
// 正是这一组把「剪得更准」与「剪得更狠」区分开的。
//
// 深度取 6：无剪枝时 depth 8 的节点数会膨胀到难以承受。
func TestForwardPruningFidelity(t *testing.T) {
	w, err := nnue.Load(flatPath)
	if err != nil {
		t.Skip("未找到展开后的权重，跳过")
	}

	type sample struct{ name, fen string }
	const depth = 6

	named := []sample{
		{"初始局面", game.InitialFEN},
		{"开局（中炮对屏风马型）", "r1ba1a3/4kn3/2n1b4/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"},
		{"中局（子力互缠）", "2bak1b2/4a4/4b4/p1p1p3p/6p2/2P6/P3P1P1P/1C2C4/9/RNBAKABNR w - - 0 1"},
		{"中局（车炮对车马）", "3ak1b2/4a4/4b4/p1p1p3p/9/2P6/P3P1P1P/1C2C1R2/9/2BAKABN1 w - - 0 1"},
		{"残局（车兵对士象全）", "3ak4/4a4/4b4/9/9/9/4P4/9/4R4/4K4 w - - 0 1"},
	}

	quiet := loadQuietFENs(t)
	if len(quiet) > 20 {
		quiet = quiet[:20]
	}
	corpus := make([]sample, 0, len(quiet))
	for i, f := range quiet {
		corpus = append(corpus, sample{fmt.Sprintf("语料 %2d", i+1), f})
	}

	check := func(label string, set []sample) {
		same, total := 0, 0
		var sumAbsDiff, maxAbsDiff int
		var onNodes, offNodes int64
		for _, c := range set {
			p, perr := game.ParseFEN(c.fen)
			if perr != nil {
				continue
			}

			on := New(w)
			rOn := on.Search(p, depth)

			off := New(w)
			off.DisableForwardPruning()
			// 参照组同时关掉期望窗口：它会让重搜写入的置换表项参与后续搜索，
			// 从而扰动搜索轨迹。实测「中局（车炮对车马）」depth 6 上，开着期望窗口的
			// 参照搜索会漏掉一个 5 步杀（4283 vs 524283），depth 7 才重新看到 ——
			// 那个杀本来就依赖置换表在迭代加深中带出来（把置换表一起关掉同样漏杀，
			// 4283），换个轨迹就丢了。参照组要的是「信息最完整」，所以两个捷径都关掉。
			// 被它掩盖的风险由 aspiration_test.go 的三项守卫单独盯住。
			off.DisableAspiration()
			rOff := off.Search(p, depth)

			equal := rOn.Best == rOff.Best
			diff := rOn.Score - rOff.Score
			if diff < 0 {
				diff = -diff
			}
			sumAbsDiff += diff
			if diff > maxAbsDiff {
				maxAbsDiff = diff
			}
			if equal {
				same++
			}
			total++
			onNodes += rOn.Nodes
			offNodes += rOff.Nodes

			tag := "分歧"
			if equal {
				tag = "一致"
			}
			t.Logf("%-22s 有剪枝 %s/%d ｜ 无剪枝 %s/%d ｜ %s ｜ 分值差 %d ｜ 节点 %d vs %d",
				c.name, rOn.Best, rOn.Score, rOff.Best, rOff.Score,
				tag, diff, rOn.Nodes, rOff.Nodes)
		}
		if total == 0 {
			t.Fatalf("%s：没有可比较的局面", label)
		}
		avgDiff := sumAbsDiff / total
		t.Logf("%s：着法一致率 %d/%d，平均分值差 %d，最大 %d，节点 %.3f×\n",
			label, same, total, avgDiff, maxAbsDiff,
			float64(onNodes)/float64(offNodes))

		// 实测（5 个代表性局面）一致率 2/5、平均 68、最大 142；
		// 安静语料 20 局面平均 56、最大 237。
		if maxAbsDiff >= 1000 {
			t.Errorf("%s：最大分值差 %d，前向剪枝疑似剪掉关键着法（或漏杀）", label, maxAbsDiff)
		} else if avgDiff > 200 {
			t.Errorf("%s：平均分值差 %d，剪枝余量偏宽", label, avgDiff)
		}
	}
	check("代表性局面", named)
	check("安静语料 20 局面", corpus)
}
