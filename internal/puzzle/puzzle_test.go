package puzzle

import (
	"testing"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
)

// TestEmbeddedPuzzlesPlayable 内嵌残局全量复验（§4.5：CI 全量复跑）：
// 每关 FEN 可解析、轮走方与执子方一致、正解可完整回放、终局符合目标
// （胜负关判执子方将死/困毙；和棋关判三次重复）。支持红先/黑先、胜/和。
func TestEmbeddedPuzzlesPlayable(t *testing.T) {
	st, err := Embedded()
	if err != nil {
		t.Fatal(err)
	}
	if st.Count() == 0 {
		t.Fatal("内嵌残局为空")
	}
	for _, p := range st.All() {
		winner := game.Red
		if p.PlayerSide == "black" {
			winner = game.Black
		}

		pos, err := game.ParseFEN(p.FEN)
		if err != nil {
			t.Errorf("%s: FEN 非法: %v", p.ID, err)
			continue
		}
		// 红方执子必须红先；黑方执子允许红先（守方后手/opponent-first）或黑先。
		if p.PlayerSide == "red" && pos.Turn != game.Red {
			t.Errorf("%s: 红方执子必须红先（实际 %s）", p.ID, sideName(pos.Turn))
			continue
		}
		if len(p.Solution) == 0 {
			t.Errorf("%s: 残局缺少正解", p.ID)
			continue
		}

		// 完整回放正解（玩家+守方着法交替）。
		okPlay := true
		for i, uci := range p.Solution {
			m, ok := game.MoveFromUCI(uci)
			if !ok || !pos.IsLegal(m) {
				t.Errorf("%s: 正解第 %d 手 %s 非法", p.ID, i+1, uci)
				okPlay = false
				break
			}
			pos.Make(m)
		}
		if !okPlay {
			continue
		}

		st := pos.CheckStatus()
		switch p.Goal {
		case "draw":
			if !st.IsDraw && st.Result != game.ResultDraw {
				t.Errorf("%s: 和棋关正解走完未和棋（%s/%s）", p.ID, st.Result, st.Reason)
			}
		case "win":
			wantResult := game.ResultRedWin
			if winner == game.Black {
				wantResult = game.ResultBlackWin
			}
			if st.Result != wantResult ||
				(st.Reason != game.ReasonCheckmate && st.Reason != game.ReasonStalemate) {
				t.Errorf("%s: 胜负关正解走完未达成 %s 将死（%s/%s）",
					p.ID, sideName(winner), st.Result, st.Reason)
			}
		default:
			t.Errorf("%s: 未知 goal=%s", p.ID, p.Goal)
		}
	}
}

func sideName(c int) string {
	if c == game.Black {
		return "black"
	}
	return "red"
}

func TestListHidesSolution(t *testing.T) {
	st, err := Embedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range st.List("") {
		if p.ParMoves <= 0 {
			t.Errorf("%s: parMoves 异常", p.ID)
		}
	}
	if _, ok := st.Get("not-exist"); ok {
		t.Error("不存在的残局不应返回")
	}
}
