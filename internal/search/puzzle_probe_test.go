package search

import (
	"os"
	"testing"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/puzzle"
)

// TestPuzzleFixedDepth 固定深度下的残局正解命中数与节点总数。
//
// 用途是评估**只影响搜索树大小**的改动（LMR 系数、剪枝强度）：同一深度下
// 命中数不降、节点数明显减少，才算净赚；若命中掉了就是剪过头。
//
// 必须固定深度而不是固定时间/节点：固定时间会被机器负载干扰，固定节点会把
// 「省下的节点」折算成额外深度，两个因素混在一起。固定深度让两边的搜索质量
// 可比，节点数则纯粹反映树的大小。
//
// 每题新建 Searcher（全新置换表）→ 完全确定性，改动前后可直接逐项对比。
func TestPuzzleFixedDepth(t *testing.T) {
	if testing.Short() {
		t.Skip("会跑几十秒")
	}
	w := loadWeights(t)
	store, err := puzzle.Embedded()
	if err != nil {
		t.Skipf("题库不可用：%v", err)
	}

	limit := envInt("QIJING_PZ_LIMIT", 200)
	depth := envInt("QIJING_PZ_DEPTH", 9)

	var used, hits int
	var nodes int64
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
		wantRed := pz.PlayerSide != "black"
		if (pos.Turn == game.Red) != wantRed {
			continue
		}
		res := searchFresh(t, w, pz.FEN, depth, false)
		used++
		nodes += res.Nodes
		if res.Best.String() == pz.Solution[0] {
			hits++
		}
	}
	if used == 0 {
		t.Skip("没有可用的胜局题")
	}
	t.Logf("固定深度 %d：命中 %d/%d = %.1f%%，合计节点 %d，平均 %d 节点/题",
		depth, hits, used, float64(hits)/float64(used)*100, nodes, nodes/int64(used))
}

// TestPuzzleFixedNodes 固定节点预算下的残局正解命中数与平均深度。
//
// 与 TestPuzzleFixedDepth 恰好互补，选哪个取决于被测改动作用于哪一环：
//   - 改「剪枝强度 / 搜索树形状」→ 固定深度（节点数是纯结果，命中可比）
//   - 改「搜索效率 / 每节点多有效」→ 固定节点（消去速度因素，看命中与深度）
//
// 用固定节点而不是固定时间：后者会被机器负载干扰，同一份代码换个负载能漂
// 十几个百分点；固定节点完全确定性，可直接逐项对比。
//
// 默认跳过（默认规模要跑十几分钟）。显式要求时运行：
//
//	QIJING_PZ_NODES=1 go test ./internal/search/ -run TestPuzzleFixedNodes -v
//
// 规模用 QIJING_PZ_LIMIT（题数，默认 400）与 QIJING_PZ_BUDGET（节点预算，默认 100000）。
func TestPuzzleFixedNodes(t *testing.T) {
	if testing.Short() {
		t.Skip("慢，需要显式要求")
	}
	if os.Getenv("QIJING_PZ_NODES") == "" {
		t.Skip("默认跳过：设 QIJING_PZ_NODES=1 才运行（默认规模约十几分钟）")
	}
	w := loadWeights(t)
	store, err := puzzle.Embedded()
	if err != nil {
		t.Skipf("题库不可用：%v", err)
	}

	limit := envInt("QIJING_PZ_LIMIT", 400)
	budget := int64(envInt("QIJING_PZ_BUDGET", 100000))

	type item struct{ fen, sol string }
	var items []item
	for _, pz := range store.All() {
		if len(items) >= limit {
			break
		}
		if pz.Goal != "win" || len(pz.Solution) == 0 {
			continue
		}
		pos, err := game.ParseFEN(pz.FEN)
		if err != nil {
			continue
		}
		if (pos.Turn == game.Red) != (pz.PlayerSide != "black") {
			continue
		}
		items = append(items, item{pz.FEN, pz.Solution[0]})
	}
	if len(items) == 0 {
		t.Skip("没有可用的胜局题")
	}

	var hits, sumDepth int
	for _, it := range items {
		pos, err := game.ParseFEN(it.fen)
		if err != nil {
			t.Fatal(err)
		}
		res := New(w).SearchNodes(pos, budget)
		sumDepth += res.Depth
		if res.Best.String() == it.sol {
			hits++
		}
	}
	t.Logf("固定 %d 节点：命中 %d/%d = %.1f%%，均深 %.2f",
		budget, hits, len(items), float64(hits)/float64(len(items))*100,
		float64(sumDepth)/float64(len(items)))
}
