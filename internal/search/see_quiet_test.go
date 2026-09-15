package search

import (
	"testing"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
)

// TestSEEQuietWiring 接线守卫：启用静的着法 SEE 剪枝后，节点数必须真的减少。
//
// 这一项存在的理由：SEE 剪枝曾被写成在 p.Make(m) **之后**调用 SeeGE，于是
// from 已空、to 上站着自己的子，SeeGE 的「第二层捷径」恒成立 —— 8.7 万次调用
// 剪掉 0 次，节点数逐位不变，剪枝形同虚设却不会有任何功能性测试失败。
// 所以必须有这么一条「它真的剪到了东西」的守卫。
//
// 局面：红车可以走到被黑兵看住的格（净亏一个车），SEE 剪枝应该把它剪掉。
func TestSEEQuietWiring(t *testing.T) {
	w := loadWeights(t)
	prev := seeQuietEnabled
	defer func() { seeQuietEnabled = prev }()

	// 取前 10 个对局局面、深度 6（阈值在此处够紧，剪枝才咬得住）。
	// 单个构造局面也能看出差异，但幅度只有千分之几，余量太薄。
	fens := loadQuietFENs(t)
	if len(fens) > 10 {
		fens = fens[:10]
	}
	const depth = 6

	nodes := map[bool]int64{}
	for _, v := range []bool{false, true} {
		seeQuietEnabled = v
		for _, fen := range fens {
			pos, err := game.ParseFEN(fen)
			if err != nil {
				t.Fatal(err)
			}
			nodes[v] += New(w).Search(pos, depth).Nodes
		}
	}
	if nodes[true] >= nodes[false] {
		t.Errorf("启用静的着法 SEE 剪枝后节点数没有减少（关 %d → 开 %d）：接线可能被改成在落子后调用，那样 SeeGE 恒为 true",
			nodes[false], nodes[true])
	}
	t.Logf("静的着法 SEE 剪枝：节点 %d → %d（%.3f×）", nodes[false], nodes[true],
		float64(nodes[true])/float64(nodes[false]))
}
