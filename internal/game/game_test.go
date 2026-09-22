package game

import (
	"strings"
	"testing"
)

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

// TestLongCheckWindowIgnoresNullEntries 白盒回归：混进重复窗口的空着（null move）
// 不得参与长将判定。
//
// 背景：`RepetitionCount` 显式过滤 `h.null`，但 `LongCheckWinner` 曾**不过滤**。
// 空着不改变子力，它出现在历史里只是搜索的中间产物；而 null 条目 `check` 恒为
// false、`color` 被置成走子方 —— 一旦落进「最近一圈循环」的扫描窗口，会把
// 「每步都将军」的那一方判成「没在将军」，方向性结论直接反转。
//
// 这里用白盒构造（直接改 hist）而不是靠自然走子去碰：自然路径要把空着恰好放进
// 窗口，需要空着后走偶数手回到同一个键，构造困难且易在实现变动后失效；白盒直接
// 钉住「窗口里出现 null 时该函数的行为」这一契约。
func TestLongCheckWindowIgnoresNullEntries(t *testing.T) {
	// 红长将循环（与 TestLongCheck 同源局面）。
	p, err := ParseFEN("5k3/9/9/9/9/9/9/4R4/9/3K5 w - - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	seq := []struct{ from, to string }{
		{"e2", "f2"}, {"f9", "e9"}, {"f2", "e2"}, {"e9", "f9"},
		{"e2", "f2"}, {"f9", "e9"}, {"f2", "e2"}, {"e9", "f9"},
	}
	playLegalSeq(t, p, seq)

	wantWinner, wantOK := p.LongCheckWinner()
	if !wantOK || wantWinner != ResultBlackWin {
		t.Fatalf("基线（无空着）应判红长将负 ⇒ 黑胜，实际 winner=%q ok=%v", wantWinner, wantOK)
	}

	// 定位当前键上次出现的位置（即窗口起点），在其后插入一条 null 条目。
	i1 := -1
	for i := len(p.hist) - 1; i >= 0; i-- {
		if !p.hist[i].null && p.hist[i].key == p.Key {
			i1 = i
			break
		}
	}
	if i1 < 0 {
		t.Fatal("没有找到窗口起点")
	}

	// 注入位置取窗口正中间：一定落在 [i1:] 内，且不改动 i1 之前的内容。
	at := i1 + (len(p.hist)-i1)/2
	// 空着的关键特征：check=false、color=走子方（红）、key 是一个不相干的键。
	// key 用不相干值是为了让它不成为新的「上次出现位置」而干扰 i1 的定位。
	inj := histEntry{key: 0xdeadbeefdeadbeef, color: Red, check: false, null: true}
	p.hist = append(p.hist[:at], append([]histEntry{inj}, p.hist[at:]...)...)

	// 注入后 i1 可能因切片变化而移动一位，重新定位。
	i1b := -1
	for i := len(p.hist) - 1; i >= 0; i-- {
		if !p.hist[i].null && p.hist[i].key == p.Key {
			i1b = i
			break
		}
	}
	nullsInWindow := 0
	for _, h := range p.hist[i1b:] {
		if h.null {
			nullsInWindow++
		}
	}
	if nullsInWindow == 0 {
		t.Fatal("构造失败：空着没有落进扫描窗口，测试无判别力")
	}

	gotWinner, gotOK := p.LongCheckWinner()
	if gotWinner != wantWinner || gotOK != wantOK {
		t.Errorf("窗口内混入 %d 条空着后长将判定被污染\n  无空着=(%q,%v)\n  有空着=(%q,%v)",
			nullsInWindow, wantWinner, wantOK, gotWinner, gotOK)
	}
}

// TestChineseNotationFourOnSameFile 4+ 子同线的序号：必须与前/后构成连续编号。
//
// 旧实现把中间子的序号写成 numStr(color, i)，下标 0 已被"前"占用 ⇒ 输出变成
// "前/一/二/后"，与注释声明的"前/二/三/后"差一位。3 子分支（前/中/后）本来就对，
// 所以只有 4 子及以上受影响，属极端排局但确实可达（残局库/自定义题目）。
func TestChineseNotationFourOnSameFile(t *testing.T) {
	// 黑方 f 线（file 5）上 4 个卒，中间留空以便各自前进一步。
	// ⚠️ FEN 首行是 rank 9，往下递减：所以 "5p3/9/…" 的卒落在 rank 8、6、4、2。
	const fen = "3k5/5p3/9/5p3/9/5p3/9/5p3/9/4K4 b - - 0 1"
	p, err := ParseFEN(fen)
	if err != nil {
		t.Fatalf("FEN 解析失败: %v", err)
	}
	if p.Turn != Black {
		t.Fatal("测试前提：应为黑方走子")
	}

	// 自"前"到"后"：黑方向下为进（rank 减小），故"前"是 rank 最小的卒。
	// 黑方"前"= 靠近红方底线 = rank 最小者 ⇒ rank 2 是"前"，rank 8 是"后"。
	// 黑方的序号用阿拉伯数字（红方才用汉字），见 notation.go 的 numStr。
	wantPrefix := map[int]string{2: "前", 4: "2", 6: "3", 8: "后"}
	for _, r := range []int{2, 4, 6, 8} {
		from := bbSquare(5, r)
		to := bbSquare(5, r-1)
		if p.Board[to] != Empty {
			t.Fatalf("rank %d 的落点被占，构造有误", r)
		}
		m := Move{From: uint8(from), To: uint8(to)}
		if !p.IsLegal(m) {
			t.Fatalf("rank %d 的卒进一不合法（构造有误）", r)
		}
		got := p.MoveToChinese(m)
		want := wantPrefix[r] + "卒进1"
		if got != want {
			t.Errorf("rank %d 的卒：got %q want %q（旧实现会给出 前/1/2/后 —— 中间子序号少一位）", r, got, want)
		}
	}
}

// TestNormalizeCNTraditional 繁体输出必须能归一化到简体（否则 LLM 着法静默失配）。
func TestNormalizeCNTraditional(t *testing.T) {
	cases := []struct{ in, want string }{
		{"馬二進三", "马二进三"},
		{"帥五進一", "帅五进一"},
		{"後車退二", "后车退二"},
		{"車二平五", "车二平五"},
		{"將五進一", "将五进一"},
		{" 馬 二 進 三 ", "马二进三"}, // 去空白
		{"馬２進３", "马2进3"},      // 全角数字
	}
	for _, c := range cases {
		if got := NormalizeCN(c.in); got != c.want {
			t.Errorf("NormalizeCN(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestNormalizeCNMatchesGeneratedNotation 归一化后的繁体串必须能匹配 MoveToChinese
// 生成的简体串 —— 这正是 LLM 生成-匹配解析（llm/player.go）所依赖的等价关系。
func TestNormalizeCNMatchesGeneratedNotation(t *testing.T) {
	p, err := ParseFEN(InitialFEN)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := MoveFromUCI("b9c7") // 马2进3
	cn := p.MoveToChinese(m)
	if cn != "马2进3" {
		t.Fatalf("前提变了：MoveToChinese 给出 %q", cn)
	}
	// 模型回繁体
	trad := "馬２進３"
	if !strings.Contains(NormalizeCN(trad), NormalizeCN(cn)) {
		t.Errorf("繁体 %q 归一化后应包含简体着法 %q（归一化结果 %q）",
			trad, cn, NormalizeCN(trad))
	}
}
