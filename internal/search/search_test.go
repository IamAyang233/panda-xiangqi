package search

import (
	"testing"
	"time"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/nnue"
)

const flatPath = "../../engines/pikafish.nnue.flat"

func loadWeights(t *testing.T) *nnue.Weights {
	t.Helper()
	w, err := nnue.Load(flatPath)
	if err != nil {
		t.Skip("未找到展开后的权重，跳过（先运行 make nnue-prepare）")
	}
	return w
}

// TestDepthOneIsArgmax 搜索框架自洽性：深度 1 必须选中「走一步后静态搜索最优」的着法。
//
// 这是对 negamax 符号、视角、静态搜索三点同时成立的交叉验证 ——
// 任何一处搞反都会让选中的着法与枚举结果不一致。
// 枚举侧用同一 quiesce 且给全窗口，因此返回的是精确值，可比。
func TestDepthOneIsArgmax(t *testing.T) {
	w := loadWeights(t)
	for _, fen := range []string{
		game.InitialFEN,
		"rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C2C4/9/RNBAKABNR w - - 0 1",
		"3k5/9/9/9/9/9/9/9/4P4/4K4 w - - 0 1",
	} {
		p, err := game.ParseFEN(fen)
		if err != nil {
			t.Fatal(err)
		}
		s := New(w)
		res := s.SearchDepth(p, 1)
		if res.Nodes == 0 {
			t.Fatalf("局面 %s 无合法着法", fen)
		}

		best, bestScore := game.Move{}, -Infinity
		// 枚举前把评估侧局面对齐到根局面：quiesce 依赖它取评估值。
		s.prepare(p)
		for _, m := range p.LegalMoves(p.Turn) {
			p.Make(m)
			s.pos.Make(int(m.From), int(m.To))
			sc := -s.quiesce(p, -Infinity, Infinity, 1)
			p.Unmake()
			s.pos.Unmake()
			if sc > bestScore {
				bestScore, best = sc, m
			}
		}
		if res.Score != bestScore {
			t.Errorf("%s: 搜索分值 %d 与枚举最优 %d 不一致", fen, res.Score, bestScore)
		}
		if res.Best != best {
			t.Errorf("%s: 搜索选 %s（%d），枚举最优 %s（%d）",
				fen, res.Best, res.Score, best, bestScore)
		}
		t.Logf("%s: depth1 best=%s score=%d ✓", fen, res.Best, res.Score)
	}
}

// TestSearchFindsMateInOne 一步制胜必须被找到，且分值达到将杀区间。
//
// 局面：黑将 e9；红马 c7 控 d9/e8，红马 g7 控 f9/e8；红车 d1。
// 红方至少有两个一步制胜着法：车 d1d9（将死）与马 c7d9（占住逃路后困毙）。
// 中国象棋困毙判负，所以两者都算杀 —— 这里不锁定具体着法，
// 而是验证「选中的着法走完对方确实无着法可走」。
func TestSearchFindsMateInOne(t *testing.T) {
	w := loadWeights(t)
	p, err := game.ParseFEN("4k4/9/2N3N2/9/9/9/9/9/3R5/3K5 w - - 0 1")
	if err != nil {
		t.Fatal(err)
	}

	s := New(w)
	res := s.SearchDepth(p, 3)
	if res.Score < MateScore-MaxPly {
		t.Errorf("一步制胜的分值应落入将杀区间（>= %d），实际 %d", MateScore-MaxPly, res.Score)
	}

	p.Make(res.Best)
	reply := p.LegalMoves(p.Turn)
	p.Unmake()
	if len(reply) != 0 {
		t.Errorf("best=%s 并未一步制胜，对方仍有 %d 个着法", res.Best, len(reply))
	}
	t.Logf("depth 3: best=%s score=%d nodes=%d（一步制胜）✓", res.Best, res.Score, res.Nodes)
}

// TestSearchAvoidsPoisonedPawn 静态搜索必须看到吃子链：吃有保护的子会被反吃。
//
// 局面：红车 a0、红帅 d0；黑车 a5、黑车 b5、黑将 e9。
// a0a5 吃掉一车看似大赚，但黑方 b5a5 立刻吃回，净交换为 0。
// 若没有静态搜索，depth 1 会误以为白得一车。
func TestSearchAvoidsPoisonedPawn(t *testing.T) {
	w := loadWeights(t)
	p, err := game.ParseFEN("4k4/9/9/9/rr7/9/9/9/9/R2K5 w - - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	s := New(w)

	// 吃 a5 车后黑方用 b5 车反吃，交换后红方并不占优。分值不该出现「白得一车」的量级。
	for _, m := range p.LegalMoves(p.Turn) {
		if m.String() != "a0a5" {
			continue
		}
		p.Make(m)
		sc := -s.quiesce(p, -Infinity, Infinity, 1)
		p.Unmake()
		if sc > 500 {
			t.Errorf("静搜未看到反吃：a0a5 得分为 %d，应接近均势", sc)
		}
		t.Logf("a0a5 静搜得分 %d（已被反吃修正）✓", sc)
		return
	}
	t.Fatal("局面中没有 a0a5 着法")
}

// TestSearchDeterministic 同一局面连续搜索两次必须给出相同结果：
// 置换表与启发式表都必须在新搜索开始时被清理干净。
func TestSearchDeterministic(t *testing.T) {
	w := loadWeights(t)
	fen := "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"

	p, err := game.ParseFEN(fen)
	if err != nil {
		t.Fatal(err)
	}
	s := New(w)
	a := s.SearchDepth(p, 3)
	b := s.SearchDepth(p, 3)
	if a.Best != b.Best || a.Score != b.Score || a.Nodes != b.Nodes {
		t.Errorf("两次搜索不一致：\n第一次 %s %d nodes=%d\n第二次 %s %d nodes=%d",
			a.Best, a.Score, a.Nodes, b.Best, b.Score, b.Nodes)
	}
	t.Logf("depth 3 两次均为 %s（score=%d nodes=%d）✓", a.Best, a.Score, a.Nodes)
}

// TestPruningEffectiveness 剪枝与缓存必须真正生效且不改变结果。
//
// 对照「全开」与「关掉置换表 + 空着剪枝」，两者应给出同一最优着法与分值；
// 同时全开的节点数应明显更少，且两个机制都确实被触发过
// —— 否则就是「写了但没接上」这类静默失效。
func TestPruningEffectiveness(t *testing.T) {
	w := loadWeights(t)
	p, err := game.ParseFEN(game.InitialFEN)
	if err != nil {
		t.Fatal(err)
	}

	full := New(w)
	rFull := full.SearchDepth(p, 4)

	none := New(w)
	none.DisableTT()
	none.DisableNullMove()
	rNone := none.SearchDepth(p, 4)

	if rFull.Best != rNone.Best || rFull.Score != rNone.Score {
		t.Errorf("剪枝改变了结果：全开 %s/%d，全关 %s/%d",
			rFull.Best, rFull.Score, rNone.Best, rNone.Score)
	}
	if full.TTHits() == 0 {
		t.Error("置换表一次未命中，等于没生效")
	}
	if full.NullMoves() == 0 {
		t.Error("空着剪枝一次未触发，等于没生效")
	}
	if rFull.Nodes >= rNone.Nodes {
		t.Errorf("剪枝未减少节点：全开 %d，全关 %d", rFull.Nodes, rNone.Nodes)
	}
	t.Logf("depth 4: 全开 %s/%d nodes=%d（TT 命中 %d，空着 %d）｜全关 nodes=%d｜省 %.0f%%",
		rFull.Best, rFull.Score, rFull.Nodes, full.TTHits(), full.NullMoves(),
		rNone.Nodes, (1-float64(rFull.Nodes)/float64(rNone.Nodes))*100)
}

// TestSearchWinsFreePiece 战术验证：白吃无保护的车时，深度 2 必须选吃子着法。
//
// 局面：红帅 d0、红车 a0；黑将 e9、黑车 a5（a 列无阻挡且黑车无保护）。
// 红方走 a0a5 白得一车。（红帅放 d0 而非 e0，避免与黑将 e9 照面。）
func TestSearchWinsFreePiece(t *testing.T) {
	w := loadWeights(t)
	p, err := game.ParseFEN("4k4/9/9/9/r8/9/9/9/9/R2K5 w - - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	s := New(w)
	res := s.SearchDepth(p, 2)

	wantMove := game.Move{From: uint8(sq(0, 0)), To: uint8(sq(0, 5))}
	if res.Best != wantMove {
		t.Errorf("depth 2 应选 a0a5 白吃车，实际选 %s（score=%d）", res.Best, res.Score)
	}
	if res.Score < 300 {
		t.Errorf("白吃一车后分值应显著为正，实际 %d", res.Score)
	}
	t.Logf("depth 2: best=%s score=%d nodes=%d ✓", res.Best, res.Score, res.Nodes)
}

// TestSearchInitial 初始局面逐层搜索，观察 bestmove 收敛与节点吞吐。
func TestSearchInitial(t *testing.T) {
	w := loadWeights(t)
	p, err := game.ParseFEN(game.InitialFEN)
	if err != nil {
		t.Fatal(err)
	}
	s := New(w)
	for d := 1; d <= 4; d++ {
		start := time.Now()
		res := s.SearchDepth(p, d)
		el := time.Since(start)
		t.Logf("depth %d: best=%s score=%d nodes=%d 用时 %v (%.0f 节点/秒)",
			d, res.Best, res.Score, res.Nodes, el.Round(time.Millisecond),
			float64(res.Nodes)/el.Seconds())
	}
}

// TestEvaluateSpeed 单独测量评估吞吐，用于判断瓶颈。
func TestEvaluateSpeed(t *testing.T) {
	w := loadWeights(t)
	p, err := game.ParseFEN(game.InitialFEN)
	if err != nil {
		t.Fatal(err)
	}
	s := New(w)

	const n = 200
	start := time.Now()
	for i := 0; i < n; i++ {
		s.evaluate(p)
	}
	el := time.Since(start)
	t.Logf("单次评估 %.1f 微秒（%d 次共 %v）", float64(el.Microseconds())/n, n, el.Round(time.Millisecond))
}

// sq 是测试用的 file/rank → 90 格索引。
func sq(f, r int) int { return r*9 + f }
