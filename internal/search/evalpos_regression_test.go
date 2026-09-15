package search

// 本文件锁住两个「评估侧局面与搜索侧失配」的回归。两者症状都很隐蔽：
// 不报错，只是评估值静默偏移，棋力整体下滑；极端时子力计数变负，
// 在 LayerStackBucket 上越界 panic。

import (
	"testing"
	"time"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
)

// regressionFENs 覆盖开局、中局与各类残局，用于暴露跨搜索的状态残留。
var regressionFENs = []struct{ name, fen string }{
	{"初始局面", game.InitialFEN},
	{"中局（车炮对车马）", "3ak1b2/4a4/4b4/p1p1p3p/9/2P6/P3P1P1P/1C2C1R2/9/2BAKABN1 w - - 0 1"},
	{"残局（车兵对士象全）", "3ak4/4a4/4b4/9/9/9/4P4/9/4R4/4K4 w - - 0 1"},
	{"残局（双车对车士）", "4k4/4a4/9/9/9/9/9/9/2R1R4/3K5 w - - 0 1"},
	{"残局（车马对车）", "3k5/9/9/9/4r4/9/9/9/3NR4/3K5 w - - 0 1"},
	{"残局（炮兵对士象）", "3ak4/4a4/4b4/9/9/9/9/4N4/3C5/4K4 w - - 0 1"},
}

// TestEvalPosNoCrossSearchLeak 锁住 ResetFromGame 必须显式清空空格。
//
// 曾经漏掉这一步：Position 被连续多次搜索复用，上一个局面的棋子残留在
// 棋盘上，而子力计数已归零。后果是初始局面会被搜成 b0c2/5600 而非
// b2e2/40 —— 评估完全失真，而任何断言「搜索不报错」的测试都发现不了。
func TestEvalPosNoCrossSearchLeak(t *testing.T) {
	w := loadWeights(t)

	for _, c := range regressionFENs {
		gp, err := game.ParseFEN(c.fen)
		if err != nil {
			t.Fatalf("FEN 解析失败 %s: %v", c.fen, err)
		}

		fresh := New(w)
		want := fresh.SearchDepth(gp, 3)

		// 同一个 Searcher 先搜一圈别的局面，再回到目标局面。
		reused := New(w)
		for _, other := range regressionFENs {
			op, _ := game.ParseFEN(other.fen)
			reused.SearchDepth(op, 2)
		}
		got := reused.SearchDepth(gp, 3)

		if got.Best != want.Best || got.Score != want.Score {
			t.Errorf("%s：复用 Searcher 得 %s/%d，全新 Searcher 得 %s/%d（跨搜索状态残留）",
				c.name, got.Best, got.Score, want.Best, want.Score)
		}
	}
}

// TestPoolEvalPosAligned 检查线程池每次搜索后，各线程的评估侧局面都与传入
// 局面一致。
//
// 曾经漏掉 worker 路径的 prepare：辅助线程不经过 searchLoop，于是它们的
// 评估侧局面停留在上一次搜索的位置，增量差集的基准就错了 —— 子力计数一路
// 累减到负数，最终在 LayerStackBucket 上越界 panic（实测 -4）。
func TestPoolEvalPosAligned(t *testing.T) {
	w := loadWeights(t)
	p := NewPool(w, 8, 16)

	for round := 0; round < 3; round++ {
		for _, c := range regressionFENs {
			gp, err := game.ParseFEN(c.fen)
			if err != nil {
				t.Fatalf("FEN 解析失败 %s: %v", c.fen, err)
			}
			p.SearchTime(gp, TimeLimit{Soft: 30 * time.Millisecond, Hard: 60 * time.Millisecond}, 2)

			for i, s := range p.searchers {
				if bad := s.pos.CheckCounts(); bad != "" {
					t.Fatalf("第 %d 轮 %s 后线程 %d 评估局面失配：%s", round, c.name, i, bad)
				}
				if s.pos.SideToMove() != gp.Turn>>3 {
					t.Fatalf("第 %d 轮 %s 后线程 %d 走子方不符：%d vs %d",
						round, c.name, i, s.pos.SideToMove(), gp.Turn>>3)
				}
			}
		}
	}
}
