package search

import (
	"testing"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/nnue"
)

// TestSearchTreeFingerprint 对两个固定局面做「逐层工作量指纹」：
// 迭代加深到 d12，逐层比对**累计走子数**的整条级联，外加末层节点数。
//
// 存在的理由：现有的逐位守卫（`1040484` / `0.945×` / `14.70` / `68.62%` / SEE 计数）
// 都是**聚合量**，对「只影响部分局面的小改动」有盲区。实例：2026-09-19 发现
// `TestForwardPruningFidelity` 的「最大分值差」从 235 变成 237，而上面那四个聚合
// 守卫一个都没动 —— 说明中间某个提交改了树而没人察觉。这条守的是**级联形状**，
// 比「固定节点下的平均深度」敏感得多（一条 12 项的序列，任一项变了都会红）。
//
// 它的定位是「行为保持类改动的验收闸」：重构/提速若声称不动搜索树，就必须让它保持
// 原值；**确认是刻意改了树**时，再连同实测数据一起更新这里的期望值。
func TestSearchTreeFingerprint(t *testing.T) {
	w, err := nnue.Load(flatPath)
	if err != nil {
		t.Skip("未找到展开后的权重，跳过")
	}
	cases := []struct {
		name     string
		fen      string
		makes    []int64 // d1..d12 的累计走子数
		lastNode int64   // d12 的结点进入次数
	}{
		{
			name:     "中局（子力互缠）",
			fen:      "2bak1b2/4a4/4b4/p1p1p3p/6p2/2P6/P3P1P1P/1C2C4/9/RNBAKABNR w - - 0 1",
			makes:    []int64{37, 139, 309, 1206, 1705, 5960, 8051, 14631, 27944, 41786, 69896, 131377},
			lastNode: 170483,
		},
		{
			name:     "中局（车炮对车马）",
			fen:      "3ak1b2/4a4/4b4/p1p1p3p/9/2P6/P3P1P1P/1C2C1R2/9/2BAKABN1 w - - 0 1",
			makes:    []int64{33, 122, 253, 390, 664, 911, 1000, 1088, 1211, 1408, 1737, 2380},
			lastNode: 2776,
		},
	}

	for _, c := range cases {
		p, perr := game.ParseFEN(c.fen)
		if perr != nil {
			t.Fatal(perr)
		}
		s := New(w)
		var got []Result
		s.SetIterObserver(func(r Result) { got = append(got, r) })
		res := s.Search(p, 12)

		if len(got) != len(c.makes) {
			t.Fatalf("%s：回调 %d 次，期望 %d 次（迭代加深每层一次）", c.name, len(got), len(c.makes))
		}
		for i := range got {
			if got[i].Makes != c.makes[i] {
				t.Errorf("%s：d%d 的累计走子 %d，期望 %d —— **搜索树变了**。\n"+
					"若这是刻意的行为改动，请连同实测数据一起更新本测试的期望值；\n"+
					"若你声称改动「不动搜索树」，那它没有做到。",
					c.name, i+1, got[i].Makes, c.makes[i])
			}
		}
		if res.Nodes != c.lastNode {
			t.Errorf("%s：d12 结点 %d，期望 %d", c.name, res.Nodes, c.lastNode)
		}
	}
}
