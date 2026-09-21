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
		"9/9/9/9/9/9/9/9/9/9 w - - 0 1",                                         // 缺将帅
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
	e8 := bbSquare(4, 8)
	// 黑马 g7 攻 e8 需要蹩腿点 f7 为空；f7 有红兵则不攻击
	p, err := ParseFEN("3k5/9/5Pn2/9/9/9/9/9/9/4K4 w - - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	if p.bb.isAttackedBB(e8, Black) {
		t.Error("蹩马腿后 e8 不应被黑马攻击")
	}
	p2, err := ParseFEN("3k5/9/6n2/9/9/9/9/9/9/4K4 w - - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	if !p2.bb.isAttackedBB(e8, Black) {
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
	// 红长将：红车 e2↔f2 追击（车随黑王的文件换线），黑王 f9↔e9 应将；
	// 红帅在 d0 —— 与黑王永不在同一纵线，所以不存在「王走回去造成照面」的非法着法。
	//
	// ⚠️ 旧版本用的 FEN 是 "4k4/.../4R4/4K4" + 序列 e1e2/e9d9/...：那里双方帅在
	// e 线照面，黑王走回 e9 的那几步（第 4/8/12 步）其实**非法**；而 `Make` 只要求
	// 伪合法，于是测试照跑。现在换成下面这条**每一步都合法**的序列，并由
	// playLegalSeq 逐着断言。
	p, err := ParseFEN("5k3/9/9/9/9/9/9/4R4/9/3K5 w - - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	seq := []struct{ from, to string }{
		{"e2", "f2"}, // 红车将军 f9
		{"f9", "e9"}, // 黑王应将
		{"f2", "e2"}, // 红车将军 e9
		{"e9", "f9"}, // 黑王应将
		{"e2", "f2"},
		{"f9", "e9"},
		{"f2", "e2"},
		{"e9", "f9"},
		{"e2", "f2"},
		{"f9", "e9"},
		{"f2", "e2"},
		{"e9", "f9"},
	}
	playLegalSeq(t, p, seq)
	// 12 步 = 三圈 ⇒ 初始局面出现 4 次（第 0/4/8/12 步）。
	if n := p.RepetitionCount(); n != 4 {
		t.Fatalf("12 步后初始局面应出现 4 次，实际 %d", n)
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
	// 黑长将：黑车 f7↔e7 追击（车随红帅的文件换线），红帅 e0↔f0 应将；
	// 黑王在 d9 —— 与红帅永不在同一纵线。
	//
	// ⚠️ 旧版本有两处错，而且**测试照样通过**，2026-09-21 修正：
	//   - 序列写的是 e2e3/e3f3（起点是空格！），黑车其实在 e7、一步没动。
	//   - 于是 12 步的 history 里**没有任何黑方条目** —— 因为 ColorOf(Empty) == Red，
	//     空走子被记成红方走子。LongCheckWinner 里 `blackAll` 于是空洞地为真，
	//     直接返回 ResultRedWin。「用完全错误的理由断言了正确的值」。
	// 现在子真的在动（黑车 e7↔f7），每一步也都合法。
	p, err := ParseFEN("3k5/9/5r3/9/9/9/9/9/9/4K4 b - - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	seq := []struct{ from, to string }{
		{"f7", "e7"}, // 黑车将军 e0
		{"e0", "f0"}, // 红帅应将
		{"e7", "f7"}, // 黑车将军 f0
		{"f0", "e0"}, // 红帅应将
		{"f7", "e7"},
		{"e0", "f0"},
		{"e7", "f7"},
		{"f0", "e0"},
		{"f7", "e7"},
		{"e0", "f0"},
		{"e7", "f7"},
		{"f0", "e0"},
	}
	playLegalSeq(t, p, seq)
	if n := p.RepetitionCount(); n != 4 {
		t.Fatalf("12 步后初始局面应出现 4 次，实际 %d", n)
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
	//
	// ⚠️ 这个测试此前是**因为错误的原因**通过的，2026-09-21 修正：
	//   - 两个马在 **a0 / a9**（FEN 首尾字段是 "n3k4"/"N3K4"），原来写的是 b0/b9 ——
	//     那两格是空的，于是 12 步全在空格上「走子」，马一步没动。而 `Make`
	//     从空格走子**不报错**，它照样 XOR Zobrist 键，键序列每 4 步重复一次
	//     ⇒ RepetitionCount 照样涨上去 ⇒ 测试照样「通过」。
	//   - 原来的 FEN 还把双方将帅放在同一条纵线上（照面），初始就「被将军」。
	//     现在把红帅放到 d0，两个王的文件不同 ⇒ 局面干净、着法全合法。
	//   - 还有一处**FEN 与着法序列自相矛盾**：FEN 把马放在 a0/a9，而序列按
	//     b0/b9 写（`b0→c2` 才是马步；`a0→c2` 是斜线，压根不是马步）。
	//     现按 FEN 把走法改成 a0↔b2 / a9↔b7。
	// 下面每一步都断言「起点有子 + 着法合法」，坐标再写错就会当场失败。
	p, err := ParseFEN("n3k4/9/9/9/9/9/9/9/9/N2K5 w - - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	seq := []struct{ from, to string }{
		{"a0", "b2"}, // 红马闲步
		{"a9", "b7"}, // 黑马闲步
		{"b2", "a0"},
		{"b7", "a9"},
		{"a0", "b2"},
		{"a9", "b7"},
		{"b2", "a0"},
		{"b7", "a9"},
	}
	playLegalSeq(t, p, seq)
	// 8 步 = 两圈 ⇒ 初始局面出现 3 次（第 0/4/8 步）。
	if n := p.RepetitionCount(); n != 3 {
		t.Fatalf("8 步后初始局面应出现 3 次，实际 %d", n)
	}
	if _, ok := p.LongCheckWinner(); ok {
		t.Fatal("非长将重复不应判定单方胜负")
	}
	st := p.CheckStatus()
	if !st.IsDraw || st.Result != ResultDraw || st.Reason != ReasonRepetition {
		t.Fatalf("非长将重复应和, 实际 result=%q reason=%q draw=%v", st.Result, st.Reason, st.IsDraw)
	}
}

// playLegalSeq 依次走出一串着法，并断言每一步「起点有子且着法合法」。
//
// ⚠️ 这两个断言都不能省：`Make` 只要求**伪合法**，对「起点是空格」与「自将/照面」
// 都不报错。少一个断言，写错坐标或写错照面的测试就会静默退化成一个空测试
// —— 这在本文件里真的发生过两次（TestRepetitionNonCheckDraw 与
// TestLongCheckBlackLoses）。
func playLegalSeq(t *testing.T, p *Position, seq []struct{ from, to string }) {
	t.Helper()
	for _, s := range seq {
		f, ok1 := SquareFromName(s.from)
		t2, ok2 := SquareFromName(s.to)
		if !ok1 || !ok2 {
			t.Fatalf("bad square %s-%s", s.from, s.to)
		}
		if p.PieceAt(int(f)) == Empty {
			t.Fatalf("着法 %s-%s 的起点没有棋子（坐标写错了？）—— 空走子不会报错，"+
				"但会污染 Zobrist 键历史并制造假重复", s.from, s.to)
		}
		m := Move{From: f, To: t2}
		if !p.IsLegal(m) {
			t.Fatalf("着法 %s-%s 在当前局面不合法（自将 / 将帅照面 / 蹩腿等）", s.from, s.to)
		}
		p.Make(m)
	}
}
