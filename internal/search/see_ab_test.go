package search

import (
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/nnue"
	"github.com/IamAyang233/panda-xiangqi/internal/puzzle"
)

// TestSeeTacticsAB 配对比较 SEE 开/关在残局战术上的正解命中率。
//
// 为什么不用 cmd/engine-match 的 puzzle 模式：那边是多线程池、置换表跨题复用、
// 按固定时间收手，同一配置连跑四次命中率能差 30 个百分点，而时间预算还受机器
// 负载影响。这里改成**单线程 + 每题独立置换表 + 固定深度**：完全确定性
// （本测试开头会自检两次同配置结果一致），于是配对差只反映 SEE 本身。
//
// 选固定深度而不是固定节点：本实验问的是「剪枝质量」。固定深度让两边搜同样深，
// 差别只来自 SEE 丢弃了哪些吃子；固定节点会把 SEE 省下的预算折算成额外深度，
// 反而把效率因素混进来。
//
// 判据用配对计数而不是两个命中率之差：同一批题、同一深度，只有 SEE 不同，
// 于是「仅 SEE 对」与「仅非 SEE 对」直接给出方向，不受题目难易分布影响。
func TestSeeTacticsAB(t *testing.T) {
	if testing.Short() {
		t.Skip("会跑几分钟")
	}
	w := loadWeights(t)
	store, err := puzzle.Embedded()
	if err != nil {
		t.Skipf("题库不可用：%v", err)
	}

	// SEE 默认关闭，这里显式打开来做对照组。
	prevSee := seeEnabled
	seeEnabled = true
	defer func() { seeEnabled = prevSee }()

	limit := envInt("QIJING_SEE_AB_LIMIT", 80)
	depth := envInt("QIJING_SEE_AB_DEPTH", 9)

	var used, offHits, onHits int
	var bothHit, onlyOff, onlyOn, neitherHit int
	var nodesOff, nodesOn int64

	var first puzzle.Puzzle
	var firstOff, firstOn, firstOffAgain game.Move
	var firstNodesOff, firstNodesOn, firstNodesOffAgain int64

	start := time.Now()
	for _, pz := range store.All() {
		if used >= limit {
			break
		}
		if pz.Goal != "win" || len(pz.Solution) == 0 {
			continue
		}
		pos, err := game.ParseFEN(pz.FEN)
		if err != nil {
			continue
		}
		// 题面要求走子方与出题方一致，否则比的是对方的最佳着法。
		wantRed := pz.PlayerSide != "black"
		if (pos.Turn == game.Red) != wantRed {
			continue
		}

		resOff := searchFresh(t, w, pz.FEN, depth, false)
		resOn := searchFresh(t, w, pz.FEN, depth, true)
		used++
		nodesOff += resOff.Nodes
		nodesOn += resOn.Nodes

		okOff := resOff.Best.String() == pz.Solution[0]
		okOn := resOn.Best.String() == pz.Solution[0]
		if okOff {
			offHits++
		}
		if okOn {
			onHits++
		}
		switch {
		case okOff && okOn:
			bothHit++
		case okOff:
			onlyOff++
		case okOn:
			onlyOn++
		default:
			neitherHit++
		}

		// 首题再跑一遍同配置，验证搜索可复现 —— 不可复现的话配对比较就白做了。
		if used == 1 {
			again := searchFresh(t, w, pz.FEN, depth, false)
			first = *pz
			firstOff, firstOffAgain = resOff.Best, again.Best
			firstNodesOff, firstNodesOffAgain = resOff.Nodes, again.Nodes
			firstOn, firstNodesOn = resOn.Best, resOn.Nodes
		}
	}

	if used == 0 {
		t.Skip("没有可用的胜局题")
	}
	if firstOff != firstOffAgain || firstNodesOff != firstNodesOffAgain {
		t.Fatalf("搜索不可复现：同配置两次得 %s/%d 与 %s/%d\n局面 %s（%s）",
			firstOff, firstNodesOff, firstOffAgain, firstNodesOffAgain, first.FEN, first.ID)
	}

	rate := func(h int) float64 { return float64(h) / float64(used) * 100 }
	t.Logf("首题 %s：非 SEE 走 %s（%d 节点），SEE 走 %s（%d 节点）",
		first.ID, firstOff, firstNodesOff, firstOn, firstNodesOn)
	t.Logf("共 %d 题，固定深度 %d，单线程、每题独立置换表", used, depth)
	t.Logf("SEE 关：命中 %d/%d = %.1f%%   合计节点 %d", offHits, used, rate(offHits), nodesOff)
	t.Logf("SEE 开：命中 %d/%d = %.1f%%   合计节点 %d（%.1f%%）",
		onHits, used, rate(onHits), nodesOn, float64(nodesOn)/float64(nodesOff)*100)
	t.Logf("配对：都对 %d，仅 SEE 对 %d，仅非 SEE 对 %d，都错 %d",
		bothHit, onlyOn, onlyOff, neitherHit)
	t.Logf("用时 %v", time.Since(start).Round(time.Second))

	switch {
	case onlyOn == 0 && onlyOff == 0:
		t.Logf("结论：两者逐题完全一致，SEE 对这批局面没有可观测影响。")
	case onlyOn > onlyOff:
		t.Logf("结论：SEE 方向为正，多对 %d 题。", onlyOn-onlyOff)
	case onlyOff > onlyOn:
		t.Logf("结论：SEE 方向为负，少对 %d 题。", onlyOff-onlyOn)
	default:
		t.Logf("结论：方向持平，SEE 的取舍在这批题上无差别。")
	}
}

// searchFresh 每题都新建 Searcher（含全新置换表）并按固定深度搜一次。
func searchFresh(t *testing.T, w *nnue.Weights, fen string, depth int, see bool) Result {
	t.Helper()
	pos, err := game.ParseFEN(fen)
	if err != nil {
		t.Fatalf("FEN 解析失败 %q: %v", fen, err)
	}
	s := New(w)
	if !see {
		s.DisableSEE()
	}
	return s.Search(pos, depth)
}

// envInt 读整数环境变量，缺省或非法时用 def。
// 沿用仓库既有的 QIJING_* 约定，便于把诊断规模放大到全题库跑一轮。
func envInt(name string, def int) int {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}
