package search

import (
	"fmt"
	"testing"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/puzzle"
)

// 奇异延伸的守卫。
//
// 这套机制最危险的失效方式**不会表现为报错，也不会表现为节点数异常**：
// 验证搜索本该「拿掉 ttMove 再看这个局面有多好」，一旦排除没有真正生效
// （着法循环没有跳过它、或者按置换表截断了、或者按该表项的旧值自证），
// 验证搜索就会原样搜出 ttValue —— 于是「奇异」永远判不出来，机制静默失效。
//
// 更隐蔽的是另一半：排除模式下的分值被写回置换表。那不会让任何断言变红，
// 只会让后续搜索吃到「拿掉最好着法之后」的分值，棋力悄悄下降。
//
// 所以这两个不变量必须各自有一条**具备判别力**的断言 —— 把对应的实现删掉，
// 测试必须失败，而不是照样通过。

// uniqueMateInOne 从题库里挑一道**唯一解**的一步杀，返回 FEN 与那个着法。
//
// 必须唯一：如果还有别的杀着，那么「排除正解后仍报杀」就是正常现象，
// 断言会变成假阳性。唯一性用穷举合法着法直接判定，不靠搜索。
func uniqueMateInOne(t *testing.T) (string, game.Move) {
	t.Helper()
	store, err := puzzle.Embedded()
	if err != nil {
		t.Skipf("题库不可用：%v", err)
	}
	for _, pz := range store.All() {
		if pz.Goal != "win" || pz.PlayerSide != "red" || pz.ParMoves != 1 || len(pz.Solution) != 1 {
			continue
		}
		pos, err := game.ParseFEN(pz.FEN)
		if err != nil {
			continue
		}
		mates := 0
		for _, m := range pos.LegalMoves(pos.Turn) {
			pos.Make(m)
			if len(pos.LegalMoves(pos.Turn)) == 0 {
				mates++
			}
			pos.Unmake()
		}
		if mates != 1 {
			continue
		}
		mv, ok := game.MoveFromUCI(pz.Solution[0])
		if !ok {
			continue
		}
		return pz.FEN, mv
	}
	t.Skip("题库里没有唯一解的一步杀")
	return "", game.Move{}
}

func isMateScore(v int) bool { return v > MateScore-MaxPly || v < -(MateScore-MaxPly) }

// TestSingularExclusionActuallyExcludes 排除着法必须真的把它从着法循环里拿掉。
//
// 判别力：删掉着法循环里的 `if hasExcluded && m == excludedMove { continue }`，
// 排除搜索结果会与不排除时一样落入将杀区间，本测试立即失败。
func TestSingularExclusionActuallyExcludes(t *testing.T) {
	w := loadWeights(t)
	fen, mate := uniqueMateInOne(t)

	// 基准：不排除，一定要搜到这一步杀。
	p, err := game.ParseFEN(fen)
	if err != nil {
		t.Fatal(err)
	}
	base := New(w)
	base.prepare(p)
	v := base.alphaBeta(p, 4, -Infinity, Infinity, 0, false, true, false)
	if !isMateScore(v) {
		t.Fatalf("基准搜索没找到一步杀（分值 %d），用例本身失效", v)
	}

	// 排除那步杀：同一局面、同一深度，必须搜不到杀。
	p2, err := game.ParseFEN(fen)
	if err != nil {
		t.Fatal(err)
	}
	s := New(w)
	s.prepare(p2)
	s.excludedMove = mate
	v2 := s.alphaBeta(p2, 4, -Infinity, Infinity, 0, false, true, false)
	if isMateScore(v2) {
		t.Fatalf("排除了着法 %s（唯一解的一步杀）之后仍报杀分 %d —— "+
			"排除没有生效，验证搜索会自证 ttMove 是奇异着法", mate, v2)
	}
	if got := s.excludedMove; got.From != 0 || got.To != 0 {
		t.Errorf("排除标记没有被清空（残留 %v），会漏进子树", got)
	}
	t.Logf("%s 排除 %s：%d → %d（杀分消失 ✓）", fen, mate, v, v2)
}

// TestSingularExclusionDoesNotPolluteTT 排除模式下的分值绝不能写回置换表。
//
// 判别力：把存表处的 `if !hasExcluded` 去掉，本测试立即失败。
//
// 这里检查的是**当前局面自己的键**：子树写自己的条目是正确的（它们的
// excludedMove 已经清空），只有本局面这个键被覆盖才是污染。
func TestSingularExclusionDoesNotPolluteTT(t *testing.T) {
	w := loadWeights(t)
	fen, mate := uniqueMateInOne(t)

	p, err := game.ParseFEN(fen)
	if err != nil {
		t.Fatal(err)
	}
	s := New(w)
	s.prepare(p)
	// 先正常搜一次，把该局面的条目写进表。
	if res := s.SearchDepth(p, 6); res.Nodes == 0 {
		t.Fatal("基准搜索没有跑出节点")
	}
	before, ok := s.tt.probe(p.Key)
	if !ok {
		t.Fatalf("基准搜索后置换表里没有 %s 的条目，用例本身失效", fen)
	}

	// 再在排除模式下搜同一局面。
	s.excludedMove = mate
	_ = s.alphaBeta(p, 6, -Infinity, Infinity, 0, false, true, false)

	after, ok := s.tt.probe(p.Key)
	if !ok {
		t.Fatalf("排除搜索后条目消失了")
	}
	if before != after {
		t.Errorf("排除搜索污染了置换表：%+v → %+v\n"+
			"（写进去的是「拿掉最好着法之后」的分值，后续搜索会直接吃这个错误值）",
			before, after)
	}
}

// TestSingularExtensionWiring 开启开关后机制必须真的触发。
//
// 判别力：把触发条件里的任一项写错（例如 ttFlag 判断漏掉 ttExact、或者
// 压根没接上 s.excludedMove），触发数会变成 0，本测试立即失败。
// 「接上了但从不触发」正是这类机制最容易被漏掉的失效方式。
func TestSingularExtensionWiring(t *testing.T) {
	if !seEnabled {
		t.Skip("需要 QIJING_SE=on 才检查触发接线")
	}
	w := loadWeights(t)
	p, err := game.ParseFEN("2bak1b2/4a4/4b4/p1p1p3p/6p2/2P6/P3P1P1P/1C2C4/9/RNBAKABNR w - - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	s := New(w)
	res := s.SearchDepth(p, 10)
	if s.seTriggers == 0 {
		t.Fatalf("搜索了 %d 个节点但奇异延伸一次都没触发 —— 接线有问题", res.Nodes)
	}
	if s.seExtended == 0 {
		t.Error("触发了但没有一次判定为奇异，验证搜索可能没有真正排除 ttMove")
	}
	fmt.Printf("接线检查：触发 %d、延伸 %d、多切 %d、负延伸 %d（节点 %d）\n",
		s.seTriggers, s.seExtended, s.seMultiCut, s.seNegExt, res.Nodes)
}
