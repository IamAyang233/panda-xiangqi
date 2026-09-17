package search

import (
	"testing"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/puzzle"
)

// TestQuiesceStructure 量静态搜索的结构占比（QIJING_QDIAG=on 时才有计数）。
//
// 用途：回答「战术弱项是不是出在静态搜索」。靠读数不靠对照源码猜 ——
// 2026-09-17 实测结论是 **没有空间**（见 diagQ 的注释），保留测试让后人不必重做。
//
//	go test ./internal/search/ -run TestQuiesceStructure -v   (需 QIJING_QDIAG=on)
func TestQuiesceStructure(t *testing.T) {
	if !diagQ {
		t.Skip("需要 QIJING_QDIAG=on 才有计数")
	}
	w := loadWeights(t)
	store, err := puzzle.Embedded()
	if err != nil {
		t.Skipf("题库不可用：%v", err)
	}
	limit := envInt("QIJING_PZ_LIMIT", 150)
	nodes := int64(envInt("QIJING_QNODES", 100000))

	used := 0
	for _, pz := range store.All() {
		if used >= limit {
			break
		}
		if pz.Goal != "win" || len(pz.Solution) == 0 {
			continue
		}
		pos, perr := game.ParseFEN(pz.FEN)
		if perr != nil {
			continue
		}
		wantRed := pz.PlayerSide != "black"
		if (pos.Turn == game.Red) != wantRed {
			continue
		}
		New(w).SearchNodes(pos.Clone(), nodes)
		used++
	}
	if used == 0 {
		t.Skip("没有可用的胜局题")
	}

	total := qdAbNodes + qdQNodes
	pct := func(a, b int64) float64 {
		if b == 0 {
			return 0
		}
		return 100 * float64(a) / float64(b)
	}
	t.Logf("残局题 %d 道 × %d 节点", used, nodes)
	t.Logf("alphaBeta %d ｜ qsearch %d（**%.1f%%**）", qdAbNodes, qdQNodes, pct(qdQNodes, total))
	t.Logf("qsearch 中被将节点 %d（占 qsearch %.1f%%）—— 要展开全部着法，最贵的路径",
		qdInCheck, pct(qdInCheck, qdQNodes))
	t.Logf("delta 剪枝整批返回 %d 次（占 qsearch %.1f%%）",
		qdDeltaCut, pct(qdDeltaCut, qdQNodes))
	if pct(qdQNodes, total) > 40 {
		t.Logf("⚠️ qsearch 占比超过 40%% —— 此时「改 qsearch」才有量级，值得重新评估")
	}
}
