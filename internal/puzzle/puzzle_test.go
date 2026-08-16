package puzzle

import (
	"testing"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
)

// TestEmbeddedPuzzlesPlayable 内嵌残局全量复验（§4.5：CI 全量复跑）：
// 每关 FEN 可解析、红方行棋、正解可完整回放、终局为红方将死。
func TestEmbeddedPuzzlesPlayable(t *testing.T) {
	st, err := Embedded()
	if err != nil {
		t.Fatal(err)
	}
	if st.Count() == 0 {
		t.Fatal("内嵌残局为空")
	}
	for _, p := range st.All() {
		pos, err := game.ParseFEN(p.FEN)
		if err != nil {
			t.Errorf("%s: FEN 非法: %v", p.ID, err)
			continue
		}
		if pos.Turn != game.Red {
			t.Errorf("%s: 残局应红先", p.ID)
			continue
		}
		if p.Goal == "win" {
			if len(p.Solution) == 0 {
				t.Errorf("%s: 胜利目标残局缺少正解", p.ID)
				continue
			}
			for i, uci := range p.Solution {
				m, ok := game.MoveFromUCI(uci)
				if !ok || !pos.IsLegal(m) {
					t.Errorf("%s: 正解第 %d 手 %s 非法", p.ID, i+1, uci)
					return
				}
				pos.Make(m)
			}
			st := pos.CheckStatus()
			if st.Result != game.ResultRedWin || st.Reason != game.ReasonCheckmate {
				t.Errorf("%s: 正解走完未将死（%s/%s）", p.ID, st.Result, st.Reason)
			}
		}
	}
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
