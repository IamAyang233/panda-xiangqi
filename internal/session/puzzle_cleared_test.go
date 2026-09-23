package session

// puzzleCleared 的穷举断言（2026-09-23 白盒补测）。
//
// 为什么值得单独测：这里的判定是「目标 × 结果 × 原因 × 执子方」的组合，而 2.0.3
// 出过一次事故 —— ReasonLongCheck 漏在白名单外，「靠长将取胜」的玩家被判挑战失败
// 还拿 0 星。这类漏项没有别的防法，只能把组合列全逐个钉住。

import (
	"testing"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/puzzle"
)

func TestPuzzleClearedMatrix(t *testing.T) {
	win := &puzzle.Puzzle{Goal: "win"}
	draw := &puzzle.Puzzle{Goal: "draw"}

	cases := []struct {
		name      string
		pz        *puzzle.Puzzle
		humanSide int
		result    string
		reason    string
		want      bool
	}{
		// 胜负关 · 玩家执红
		{"红先将死黑方", win, game.Red, game.ResultRedWin, game.ReasonCheckmate, true},
		{"红方困毙黑方", win, game.Red, game.ResultRedWin, game.ReasonStalemate, true},
		{"黑方长将判负", win, game.Red, game.ResultRedWin, game.ReasonLongCheck, true},
		{"红胜但原因不在白名单（认输）", win, game.Red, game.ResultRedWin, game.ReasonResign, false},
		{"红胜但三次重复", win, game.Red, game.ResultRedWin, game.ReasonRepetition, false},
		{"玩家输了", win, game.Red, game.ResultBlackWin, game.ReasonCheckmate, false},
		{"和棋不算通关（胜负关）", win, game.Red, game.ResultDraw, game.ReasonRepetition, false},

		// 胜负关 · 玩家执黑（同一批原因要按执子方镜像）
		{"黑先将死红方", win, game.Black, game.ResultBlackWin, game.ReasonCheckmate, true},
		{"黑方靠长将取胜", win, game.Black, game.ResultBlackWin, game.ReasonLongCheck, true},
		{"执黑却红方赢", win, game.Black, game.ResultRedWin, game.ReasonCheckmate, false},
		{"执黑和棋不算通关", win, game.Black, game.ResultDraw, game.ReasonInsufficient, false},

		// 和棋关：任何和棋原因都算通过
		{"和棋关走成三次重复", draw, game.Red, game.ResultDraw, game.ReasonRepetition, true},
		{"和棋关走成子力不足", draw, game.Red, game.ResultDraw, game.ReasonInsufficient, true},
		{"执黑和棋关走成和棋", draw, game.Black, game.ResultDraw, game.ReasonRepetition, true},
		{"和棋关玩家赢了不算通过", draw, game.Red, game.ResultRedWin, game.ReasonCheckmate, false},
		{"和棋关玩家输了不算通过", draw, game.Black, game.ResultRedWin, game.ReasonCheckmate, false},
	}

	for _, c := range cases {
		if got := puzzleCleared(c.pz, c.humanSide, c.result, c.reason); got != c.want {
			t.Errorf("%s：cleared = %v，期望 %v（目标=%s 结果=%s 原因=%s 执子=%v）",
				c.name, got, c.want, c.pz.Goal, c.result, c.reason, c.humanSide)
		}
	}
}
