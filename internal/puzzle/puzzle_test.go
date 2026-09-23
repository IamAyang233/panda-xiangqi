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

// TestFirstSideIndependentOfPlayerSide 起始轮走方（firstSide）与执子方（playerSide）
// 是两个独立维度，必须分别按 FEN 与 PlayerSide 计算，不能互相反推。
//
// 内置题目里就有一关是「执黑但红方先走」（黑方守和、红方先攻）；界面上显示
// 「红先/黑先」必须用 firstSide，用 playerSide 反推会把它标反。
func TestFirstSideIndependentOfPlayerSide(t *testing.T) {
	const fenRedFirst = "3k5/9/9/9/9/9/9/9/R8/4K4 w"
	const fenBlackFirst = "3k5/9/9/9/9/9/9/9/R8/4K4 b"
	cases := []struct {
		fen        string
		playerSide string
		wantFirst  string
	}{
		{fenRedFirst, "red", "red"},       // 红先、我执红（我先走）
		{fenBlackFirst, "black", "black"}, // 黑先、我执黑（我先走）
		{fenRedFirst, "black", "red"},     // 红先、我执黑（对手先走）
		{fenBlackFirst, "red", "black"},   // 黑先、我执红（对手先走）
	}
	for _, c := range cases {
		s := NewEmpty()
		p := &Puzzle{ID: "custom-x", Name: "x", FEN: c.fen, PlayerSide: c.playerSide, Difficulty: "自定义"}
		if err := s.Add(p); err != nil {
			t.Fatalf("Add 失败: %v", err)
		}
		pub := p.Public()
		if pub.FirstSide != c.wantFirst {
			t.Errorf("FEN=%q 执子=%s：FirstSide 期望 %s，实际 %s", c.fen, c.playerSide, c.wantFirst, pub.FirstSide)
		}
		if pub.PlayerSide != c.playerSide {
			t.Errorf("PlayerSide 应原样保留，期望 %s 实际 %s", c.playerSide, pub.PlayerSide)
		}
		// 列表视图也必须带 FirstSide（前端卡片标签用它）
		got := s.List("")[0].FirstSide
		if got != c.wantFirst {
			t.Errorf("List 的 FirstSide 期望 %s，实际 %s", c.wantFirst, got)
		}
	}
}

// TestEmbeddedFirstSideMatchesFEN 内置题库每一条的 firstSide 都必须与它的 FEN 一致。
func TestEmbeddedFirstSideMatchesFEN(t *testing.T) {
	st, err := Embedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range st.All() {
		pos, err := game.ParseFEN(p.FEN)
		if err != nil {
			continue // 加载时已被跳过，这里不重复报
		}
		want := "red"
		if pos.Turn == game.Black {
			want = "black"
		}
		if got := p.Public().FirstSide; got != want {
			t.Fatalf("%s: firstSide=%s 与 FEN 的轮走方 %s 不一致", p.ID, got, want)
		}
	}
}
