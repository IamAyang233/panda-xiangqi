package search

import (
	"testing"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
)

// TestSEEQuietWiring 接线守卫：启用静的着法 SEE 剪枝后，节点数必须**发生变化**。
//
// 这一项存在的理由：SEE 剪枝曾被写成在 p.Make(m) **之后**调用 SeeGE，于是
// from 已空、to 上站着自己的子，SeeGE 的「第二层捷径」恒成立 —— 8.7 万次调用
// 剪掉 0 次，**节点数逐位不变**，剪枝形同虚设却不会有任何功能性测试失败。
//
// 所以判据不是「节点必须减少」而是「必须不逐位相同」：剪枝之间会相互干扰，
// SEE 剪掉一步会改变搜索轨迹，其它地方的树可能随之变大 —— 实测旧的反 futility
// 余量下这一项是 0.888×，换成皮卡鱼曲线后变成 1.070×（反 futility 已经剪掉了
// 大部分 SEE 本来能剪的着法）。方向不确定，但「接线正确」一定意味着**有东西
// 被剪掉**，而「一个都没剪」恰好是那个 bug 的唯一特征。
//
// 局面：取前 10 个对局局面、深度 6（阈值在此处够紧，剪枝才咬得住）。
func TestSEEQuietWiring(t *testing.T) {
	w := loadWeights(t)
	prev := seeQuietEnabled
	defer func() { seeQuietEnabled = prev }()

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
	if nodes[true] == nodes[false] {
		t.Errorf("启用静的着法 SEE 剪枝后节点数逐位不变（%d）：接线可能被改成在落子后调用，那样 SeeGE 恒为 true，一次都不会剪",
			nodes[false])
	}
	t.Logf("静的着法 SEE 剪枝：节点 %d → %d（%.3f×）", nodes[false], nodes[true],
		float64(nodes[true])/float64(nodes[false]))
}
