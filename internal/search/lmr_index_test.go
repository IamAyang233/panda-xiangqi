package search

import (
	"os"
	"strings"
	"testing"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/nnue"
)

// TestLmrIndexSweep 扫描 `lmrMinIndex`（LMR 从第几个着法开始生效）。
//
// 动机：皮卡鱼用的是 `depth >= 2 && moveCount > 1`，即**第 2 个着法起**就削减；
// 本项目一直用 index >= 4（第 5 个起）。逐层对照（见 2026-09-19 日志）显示我们的
// 树在 depth 3~4 就开始比对手重，于是怀疑「前几个着法不削减」是原因之一。
//
// 目标函数 = 到 d12 为止的累计走子数（等名义深度下的工作量）。8 个局面。
//
//	lmrMinIndex   合计走子   相对基线
//	4（现状）      296,002      —        ← 最优
//	3              366,466    +23.8%
//	2              303,886     +2.7%
//	1              300,561     +1.5%
//
// ⇒ **现状就是最优**，「起始序号太保守」这个假设被证否（非单调：3 比 2 和 4 都差，
// 说明树形对这一个常量相当敏感，不是「越小越好」的单调关系）。
//
// 断言只守「现状不劣于最优 2%」——像所有扫描类测试一样，它守的是**方向**，
// 具体取值变动需要 A/B 才作数（见 `ENGINE-PITFALLS-measure.md`）。
func TestLmrIndexSweep(t *testing.T) {
	w, err := nnue.Load(flatPath)
	if err != nil {
		t.Skip("未找到展开后的权重，跳过")
	}
	raw, err := os.ReadFile("testdata/quiet_fens.txt")
	if err != nil {
		t.Fatal(err)
	}
	var fens []string
	for _, l := range strings.Split(string(raw), "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		fens = append(fens, l)
		if len(fens) == 6 {
			break
		}
	}
	// 补两个中局局面：quiet_fens 的子力分布较单一。
	fens = append(fens,
		"2bak1b2/4a4/4b4/p1p1p3p/6p2/2P6/P3P1P1P/1C2C4/9/RNBAKABNR w - - 0 1",
		"3ak1b2/4a4/4b4/p1p1p3p/9/2P6/P3P1P1P/1C2C1R2/9/2BAKABN1 w - - 0 1")

	orig := lmrMinIndex
	defer func() { lmrMinIndex = orig }()

	total := func() int64 {
		var s int64
		for _, f := range fens {
			p, perr := game.ParseFEN(f)
			if perr != nil {
				t.Fatal(perr)
			}
			s += New(w).Search(p, 12).Makes
		}
		return s
	}

	var base, best int64 = -1, 0
	for _, mi := range []int{4, 3, 2, 1} {
		lmrMinIndex = mi
		got := total()
		if mi == 4 {
			base = got
		}
		if best == 0 || got < best {
			best = got
		}
		t.Logf("lmrMinIndex=%-3d 合计走子 %9d  相对基线 %+7.1f%%", mi, got, 100*(float64(got)/float64(base)-1))
	}

	if float64(base) > float64(best)*1.02 {
		t.Errorf("现状 lmrMinIndex=4（%d）比最优（%d）差 %.1f%% —— 已超出 2%% 容差，请重新 A/B 后调整常量",
			base, best, 100*(float64(base)/float64(best)-1))
	}
}
