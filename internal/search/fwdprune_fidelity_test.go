package search

import (
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
// 检验方法：同一深度下，把「全开」与「关掉前向剪枝」（搜索树完整，更可信）
// 的结果逐一对比。
//
// 判据用**分值差**而不是着法一致率：同一局面常存在若干近似等价的着法
// （分值差只有几十 centipawn），剪枝后选中另一个并不损害棋力。
// 真正有害的是「把将杀或关键着法剪没」，那会在分值上留下上千的缺口 ——
// 实测确实出现过一次：漏掉给对手将军的安静着法，把 depth 6 的将杀剪没了
// （4194 vs 524283）。修复办法是剪枝前先落子判断该步是否将军。
//
// 深度取 6：无剪枝时 depth 8 的节点数会膨胀到难以承受。
func TestForwardPruningFidelity(t *testing.T) {
	w, err := nnue.Load(flatPath)
	if err != nil {
		t.Skip("未找到展开后的权重，跳过")
	}

	fens := []struct{ name, fen string }{
		{"初始局面", game.InitialFEN},
		{"开局（中炮对屏风马型）", "r1ba1a3/4kn3/2n1b4/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"},
		{"中局（子力互缠）", "2bak1b2/4a4/4k4/p1p1p3p/6p2/2P6/P3P1P1P/1C2C4/9/RNBAKABNR w - - 0 1"},
		{"中局（车炮对车马）", "3ak1b2/4a4/4b4/p1p1p3p/9/2P6/P3P1P1P/1C2C1R2/9/2BAKABN1 w - - 0 1"},
		{"残局（车兵对士象全）", "3ak4/4a4/4b4/9/9/9/4P4/9/4R4/4K4 w - - 0 1"},
	}
	const depth = 6

	same, total := 0, 0
	var sumAbsDiff, maxAbsDiff int
	for _, c := range fens {
		p, perr := game.ParseFEN(c.fen)
		if perr != nil {
			continue
		}

		on := New(w)
		rOn := on.Search(p, depth)

		off := New(w)
		off.DisableForwardPruning()
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

		tag := "分歧"
		if equal {
			tag = "一致"
		}
		t.Logf("%-22s 有剪枝 %s/%d ｜ 无剪枝 %s/%d ｜ %s ｜ 分值差 %d ｜ 节点 %d vs %d",
			c.name, rOn.Best, rOn.Score, rOff.Best, rOff.Score,
			tag, diff, rOn.Nodes, rOff.Nodes)
	}

	if total == 0 {
		t.Fatal("没有可比较的局面")
	}
	avgDiff := sumAbsDiff / total
	t.Logf("\n着法一致率 %d/%d，平均分值差 %d，最大分值差 %d", same, total, avgDiff, maxAbsDiff)

	if maxAbsDiff >= 1000 {
		t.Errorf("最大分值差 %d，前向剪枝疑似剪掉关键着法（或漏杀）。", maxAbsDiff)
	} else if avgDiff > 200 {
		t.Errorf("平均分值差 %d，剪枝余量偏宽。", avgDiff)
	} else {
		t.Logf("结论：分值损失可接受，剪枝换来的速度是净赚。")
	}
}
