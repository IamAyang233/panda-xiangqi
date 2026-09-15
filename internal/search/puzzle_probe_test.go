package search

import (
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
