package game

import "testing"

// perft 标准数据（初始局面，Xiangqi perft 基准）——任何不匹配即规则引擎回归。
func TestPerftInitial(t *testing.T) {
	p := NewPosition()
	want := []uint64{44, 1920, 79666, 3290240}
	for d, w := range want {
		if got := Perft(p, d+1); got != w {
			t.Errorf("perft(%d) = %d, want %d", d+1, got, w)
		}
	}
}

func TestFENRoundTrip(t *testing.T) {
	p := NewPosition()
	if got, want := p.FEN(), InitialFEN; got != want {
		t.Fatalf("FEN round-trip 失败:\n got %s\nwant %s", got, want)
	}
	for _, uci := range []string{"h2e2", "h9g7", "h0g2", "i9h7"} {
		m, ok := MoveFromUCI(uci)
		if !ok {
			t.Fatalf("非法 UCI %s", uci)
		}
		p.Make(m)
	}
	if _, err := ParseFEN(p.FEN()); err != nil {
		t.Fatalf("中途局面 FEN 解析失败: %v", err)
	}
}

func TestFENParseErrors(t *testing.T) {
	bad := []string{
		"9/9/9/9/9/9/9/9/9/9 w - - 0 1",                    // 缺将帅
		"rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR x - - 0 1", // 轮走方非法
	}
	for i, fen := range bad {
		if _, err := ParseFEN(fen); err == nil {
			t.Errorf("case %d: 期望解析失败但成功了: %s", i, fen)
		}
	}
}

// 随机局面 FEN 往返一致性（A2 验收）。
func TestFENRoundTripRandomPositions(t *testing.T) {
	p := NewPosition()
	seed := uint64(42)
	for i := 0; i < 200; i++ {
		moves := p.LegalMoves(p.Turn)
		if len(moves) == 0 {
			break
		}
		m := moves[splitmix64(&seed)%uint64(len(moves))]
		p.Make(m)
		q, err := ParseFEN(p.FEN())
		if err != nil {
			t.Fatalf("第 %d 手后 FEN 解析失败: %v", i, err)
		}
		if q.FEN() != p.FEN() {
			t.Fatalf("第 %d 手后 FEN 往返不一致", i)
		}
		if q.Key != p.Key {
			t.Fatalf("第 %d 手后 Zobrist 键不一致", i)
		}
	}
}

func TestMakeUnmakeConsistency(t *testing.T) {
	p := NewPosition()
	var walk func(depth int)
	walk = func(depth int) {
		if depth == 0 {
			return
		}
		fen, key := p.FEN(), p.Key
		for _, m := range p.GenMoves(p.Turn) {
			p.Make(m)
			walk(depth - 1)
			p.Unmake()
			if p.FEN() != fen || p.Key != key {
				t.Fatalf("make/unmake 破坏局面: fen=%s move=%s", fen, m)
			}
		}
	}
	walk(3)
}

func TestChineseNotation(t *testing.T) {
	cases := []struct {
		fen string
		uci string
		cn  string
	}{
		{InitialFEN, "h2e2", "炮二平五"},
		{InitialFEN, "b2e2", "炮八平五"},
		{InitialFEN, "h9g7", "马8进7"},
		{InitialFEN, "b9c7", "马2进3"},
		{InitialFEN, "e3e4", "兵五进一"},
		{InitialFEN, "e6e5", "卒5进1"},
		// 双车同线（e4 与 e0），前车（e4）退二
		{"3k5/9/9/9/9/4R4/9/9/9/3KR4 w - - 0 1", "e4e2", "前车退二"},
	}
	for _, c := range cases {
		p, err := ParseFEN(c.fen)
		if err != nil {
			t.Fatalf("FEN 解析失败 %s: %v", c.fen, err)
		}
		m, ok := MoveFromUCI(c.uci)
		if !ok {
			t.Fatalf("非法 UCI %s", c.uci)
		}
		if got := p.MoveToChinese(m); got != c.cn {
			t.Errorf("%s in %s: got %q want %q", c.uci, c.fen, got, c.cn)
		}
	}
}

func TestCheckmate(t *testing.T) {
	// 黑将 e9：被 e0 车纵向将军；d9/f9 被 d8/f8 车看管，e8 同被 rank8 双车控制。
	p, err := ParseFEN("4k4/3R1R3/9/9/9/9/9/9/9/1K2R4 b - - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	st := p.CheckStatus()
	if st.Result != ResultRedWin || st.Reason != ReasonCheckmate {
		t.Errorf("期望黑方被将死（红胜），得到 result=%s reason=%s inCheck=%v", st.Result, st.Reason, st.InCheck)
	}
	if !st.InCheck {
		t.Error("期望被将军标记")
	}
}

func TestStalemate(t *testing.T) {
	// 黑将 e9 无子可动但未被将军 → 困毙，黑负（红胜）
	p, err := ParseFEN("4k4/R8/9/9/9/9/9/9/9/K2R1R3 b - - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	st := p.CheckStatus()
	if st.Result != ResultRedWin || st.Reason != ReasonStalemate {
		t.Errorf("期望黑方困毙（红胜），得到 result=%s reason=%s inCheck=%v", st.Result, st.Reason, st.InCheck)
	}
	if st.InCheck {
		t.Error("困毙不应有将军标记")
	}
}

func TestInsufficientMaterial(t *testing.T) {
	// 双方只剩帅仕 → 无进攻子力判和
	p, err := ParseFEN("3aka3/9/9/9/9/9/9/9/9/3KA4 w - - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	st := p.CheckStatus()
	if st.Result != ResultDraw || st.Reason != ReasonInsufficient {
		t.Errorf("期望缺少进攻子力判和，得到 %s/%s", st.Result, st.Reason)
	}
}

func TestRepetition(t *testing.T) {
	// 车与将来回移动还原局面 2 轮 → 三次重复判和
	p, err := ParseFEN("4k4/9/9/9/9/9/9/9/9/3K1R3 w - - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	seq := []string{"f0g0", "e9e8", "g0f0", "e8e9"}
	for round := 0; round < 2; round++ {
		for _, uci := range seq {
			m, ok := MoveFromUCI(uci)
			if !ok {
				t.Fatalf("非法 UCI %s", uci)
			}
			if !p.IsLegal(m) {
				t.Fatalf("%s 不合法", uci)
			}
			p.Make(m)
		}
	}
	if got := p.RepetitionCount(); got != 3 {
		t.Errorf("重复计数 = %d, want 3", got)
	}
	st := p.CheckStatus()
	if st.Result != ResultDraw || st.Reason != ReasonRepetition {
		t.Errorf("期望三次重复判和，得到 %s/%s", st.Result, st.Reason)
	}
}

func TestFlyingGeneral(t *testing.T) {
	// 红帅 e1、黑将 e9，e4 车遮挡。车横移离线会形成照面 → 非法；沿线前进合法。
	p, err := ParseFEN("4k4/9/9/9/9/4R4/9/9/4K4/9 w - - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	m, _ := MoveFromUCI("e4g4")
	if p.IsLegal(m) {
		t.Error("形成照面的着法应被过滤")
	}
	m2, _ := MoveFromUCI("e4e8")
	if !p.IsLegal(m2) {
		t.Error("沿 e 线前进应合法")
	}
}

func TestIsAttackedScenarios(t *testing.T) {
	e8 := uint8(SQ256(4, 8))
	// 黑马 g7 攻 e8 需要蹩腿点 f7 为空；f7 有红兵则不攻击
	p, err := ParseFEN("3k5/9/5Pn2/9/9/9/9/9/9/4K4 w - - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	if p.isAttacked(int(e8), Black) {
		t.Error("蹩马腿后 e8 不应被黑马攻击")
	}
	p2, err := ParseFEN("3k5/9/6n2/9/9/9/9/9/9/4K4 w - - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	if !p2.isAttacked(int(e8), Black) {
		t.Error("无蹩腿时 e8 应被黑马攻击")
	}
}

func TestCannonCheckDetection(t *testing.T) {
	// 红炮 e0 隔红兵 e5 将军黑将 e9
	p, err := ParseFEN("4k4/9/9/9/4P4/9/9/9/9/3KC4 w - - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	if !p.InCheck(Black) {
		t.Error("隔山炮应将军黑将")
	}
}

func TestLongCheckRedLoses(t *testing.T) {
	// 红长将：红车 e1（开局已将军 e9）→ e2 → d2 交替将军，黑王 e9↔d9 应将。
	// 循环 4 步（红将、黑应、红将、黑应），重复 3 次后红长将判负（黑胜）。
	p, err := ParseFEN("4k4/9/9/9/9/9/9/9/4R4/4K4 w - - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	seq := []struct{ from, to string }{
		{"e1", "e2"}, // 红车将军 e9
		{"e9", "d9"}, // 黑王应将
		{"e2", "d2"}, // 红车将军 d9
		{"d9", "e9"}, // 黑王应将
		{"d2", "e2"},
		{"e9", "d9"},
		{"e2", "d2"},
		{"d9", "e9"},
		{"d2", "e2"},
		{"e9", "d9"},
		{"e2", "d2"},
		{"d9", "e9"},
	}
	for i, s := range seq {
		f, ok1 := SquareFromName(s.from)
		t2, ok2 := SquareFromName(s.to)
		if !ok1 || !ok2 {
			t.Fatalf("bad square %s-%s", s.from, s.to)
		}
		p.Make(Move{From: f, To: t2})
		if i == 7 && p.RepetitionCount() != 2 {
			t.Fatalf("第 8 步后期望重复计数 2, 实际 %d", p.RepetitionCount())
		}
	}
	if got := p.RepetitionCount(); got != 3 {
		t.Fatalf("期望三次重复, 实际 %d", got)
	}
	if w, ok := p.LongCheckWinner(); !ok || w != ResultBlackWin {
		t.Fatalf("红长将应判黑胜, 实际 winner=%q ok=%v", w, ok)
	}
	st := p.CheckStatus()
	if st.Result != ResultBlackWin || st.Reason != ReasonLongCheck {
		t.Fatalf("CheckStatus 应判 black_win/long_check, 实际 result=%q reason=%q", st.Result, st.Reason)
	}
}

func TestLongCheckBlackLoses(t *testing.T) {
	// 黑长将：黑车 e2（开局已将军 e0）→ e3 → f3 交替将军，红帅 e0↔f0 应将。
	// 重复 3 次后黑长将判负（红胜）。
	p, err := ParseFEN("4k4/9/4r4/9/9/9/9/9/9/4K4 b - - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	seq := []struct{ from, to string }{
		{"e2", "e3"}, // 黑车将军 e0
		{"e0", "f0"}, // 红帅应将
		{"e3", "f3"}, // 黑车将军 f0
		{"f0", "e0"}, // 红帅应将
		{"f3", "e3"},
		{"e0", "f0"},
		{"e3", "f3"},
		{"f0", "e0"},
		{"f3", "e3"},
		{"e0", "f0"},
		{"e3", "f3"},
		{"f0", "e0"},
	}
	for _, s := range seq {
		f, ok1 := SquareFromName(s.from)
		t2, ok2 := SquareFromName(s.to)
		if !ok1 || !ok2 {
			t.Fatalf("bad square %s-%s", s.from, s.to)
		}
		p.Make(Move{From: f, To: t2})
	}
	if w, ok := p.LongCheckWinner(); !ok || w != ResultRedWin {
		t.Fatalf("黑长将应判红胜, 实际 winner=%q ok=%v", w, ok)
	}
	st := p.CheckStatus()
	if st.Result != ResultRedWin || st.Reason != ReasonLongCheck {
		t.Fatalf("CheckStatus 应判 red_win/long_check, 实际 result=%q reason=%q", st.Result, st.Reason)
	}
}

func TestRepetitionNonCheckDraw(t *testing.T) {
	// 非长将的三次重复（双方走马闲步循环）不受影响，仍判和。
	p, err := ParseFEN("n3k4/9/9/9/9/9/9/9/9/N3K4 w - - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	seq := []struct{ from, to string }{
		{"b0", "c2"}, // 红马闲步
		{"b9", "c7"}, // 黑马闲步
		{"c2", "b0"},
		{"c7", "b9"},
		{"b0", "c2"},
		{"b9", "c7"},
		{"c2", "b0"},
		{"c7", "b9"},
		{"b0", "c2"},
		{"b9", "c7"},
		{"c2", "b0"},
		{"c7", "b9"},
	}
	for _, s := range seq {
		f, ok1 := SquareFromName(s.from)
		t2, ok2 := SquareFromName(s.to)
		if !ok1 || !ok2 {
			t.Fatalf("bad square %s-%s", s.from, s.to)
		}
		p.Make(Move{From: f, To: t2})
	}
	if _, ok := p.LongCheckWinner(); ok {
		t.Fatal("非长将重复不应判定单方胜负")
	}
	st := p.CheckStatus()
	if !st.IsDraw || st.Result != ResultDraw || st.Reason != ReasonRepetition {
		t.Fatalf("非长将重复应和, 实际 result=%q reason=%q draw=%v", st.Result, st.Reason, st.IsDraw)
	}
}
