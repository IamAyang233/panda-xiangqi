package search

import (
	"testing"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/nnue"
)

// 期望窗口的三项守卫。都不依赖生产代码里的诊断计数器 —— 需要观测的量
// （节点数、分值）从 Result 就能拿到，这样守卫不会因为「计数器被删」而失效。
//
// 语料用 testdata/quiet_fens.txt（真实对局局面）。**不能用战术题库**：
// 那里的分值常在某层突然跳到将杀分，任何有效的窗口都必然反复失败，
// 结论会完全反过来（实测 fail high 的平均幅度达 6000~20000 centipawn）。

// aspirationTestDepth 是勘误/机制类守卫用的浅深度（跑得快，够验证接线）。
const aspirationTestDepth = 8

// aspirationBenefitDepth 是「收益守卫」用的深度，取真实工作深度附近。
//
// 不能用浅深度：期望窗口的收益**随深度增长**（深层的层更大、窄窗口剪掉的枝更多，
// 而重搜的固定代价被摊薄）。实测同一批局面：depth 8 只省 3.1%，depth 10 省 16.3%。
// 在浅深度上按「省得多」来断言，会得出错误的结论。
const aspirationBenefitDepth = 10

// aspRun 以指定的窗口参数与深度跑一遍，返回总节点数与逐局面结果。
// 权重由调用方传入 —— nnue.Load 会读 68MB，不能在循环里反复调用。
func aspRun(t *testing.T, w *nnue.Weights, minDepth, delta, depth int, fens []string) (int64, []Result) {
	t.Helper()
	prevMin, prevDelta := aspirationMinDepth, aspirationDelta
	aspirationMinDepth, aspirationDelta = minDepth, delta
	defer func() { aspirationMinDepth, aspirationDelta = prevMin, prevDelta }()

	var nodes int64
	out := make([]Result, 0, len(fens))
	for _, fen := range fens {
		pos, err := game.ParseFEN(fen)
		if err != nil {
			t.Fatalf("语料里的 FEN 解析失败 %q: %v", fen, err)
		}
		res := New(w).Search(pos, depth)
		nodes += res.Nodes
		out = append(out, res)
	}
	return nodes, out
}

// TestAspirationFaithfulWhenWide 窗口宽到覆盖所有可达分值时，结果必须与全窗逐位相同。
//
// 这一项把两类原因分开：若此处不同，就是窗口/写表标志的接线有 bug；若此处相同而
// 窄窗口时不同，差异只可能来自「重搜写入的置换表项影响了后续搜索」。
func TestAspirationFaithfulWhenWide(t *testing.T) {
	if testing.Short() {
		t.Skip("要跑几十秒")
	}
	w := loadWeights(t)
	fens := loadQuietFENs(t)

	baseNodes, base := aspRun(t, w, 999, 300, aspirationTestDepth, fens)
	wideNodes, wide := aspRun(t, w, aspirationMinDepth, 600000, aspirationTestDepth, fens)

	if wideNodes > baseNodes+baseNodes/50 {
		t.Errorf("宽窗口的节点数 %d 明显多于全窗 %d（相差 %.1f%%）：说明窗口被收窄了，δ 没有真正覆盖全范围",
			wideNodes, baseNodes, float64(wideNodes-baseNodes)/float64(baseNodes)*100)
	}
	for i := range base {
		if base[i].Score != wide[i].Score {
			t.Fatalf("第 %d 个局面分值不同：全窗 %d vs 宽窗口 %d —— 窗口或写表标志的接线有 bug",
				i, base[i].Score, wide[i].Score)
		}
		if base[i].Best != wide[i].Best {
			t.Fatalf("第 %d 个局面最佳着法不同：全窗 %s vs 宽窗口 %s", i, base[i].Best, wide[i].Best)
		}
	}
	t.Logf("宽窗口（δ=600000）与全窗逐位一致，节点 %d vs %d", wideNodes, baseNodes)
}

// TestAspirationWindowIsApplied 极窄窗口必须让节点数明显上升。
//
// 窗口真的在用时，窄窗口会让绝大多数层 fail low/high 并触发重搜，重搜的代价
// 直接体现在节点数上。若这条断言不成立，说明窗口被忽略了（例如接线改成了
// 恒传 ±∞），期望窗口形同虚设却不会有任何测试失败。
func TestAspirationWindowIsApplied(t *testing.T) {
	if testing.Short() {
		t.Skip("要跑几十秒")
	}
	w := loadWeights(t)
	fens := loadQuietFENs(t)

	wideNodes, _ := aspRun(t, w, 999, 300, aspirationTestDepth, fens) // 全窗基线
	narrowNodes, _ := aspRun(t, w, 4, 1, aspirationTestDepth, fens)   // 极窄窗口
	if narrowNodes <= wideNodes+wideNodes/8 {                         // 要求至少高 12.5%
		t.Errorf("δ=1 时节点 %d 未明显高于全窗 %d（%.1f%%）：窗口似乎没有生效，重搜没有发生",
			narrowNodes, wideNodes, float64(narrowNodes-wideNodes)/float64(wideNodes)*100)
	}
	t.Logf("δ=1 的节点 %d 为全窗 %d 的 %.2f×，窗口确实在生效", narrowNodes, wideNodes,
		float64(narrowNodes)/float64(wideNodes))
}

// TestAspirationReducesQuietNodes 生产参数（δ=300）必须在安静局面上换来节点节省。
//
// 这是「收益守卫」：它同时能挡住两类退化 —— 把 aspirationDelta 误调成极大
// （等于关闭期望窗口）、以及后续改动把窗口的剪枝收益吃掉。
//
// 同时检查「是否见过将杀」两侧一致：截断值被当成结果会立刻表现为将杀判定翻转。
func TestAspirationReducesQuietNodes(t *testing.T) {
	if testing.Short() {
		t.Skip("要跑几十秒")
	}
	w := loadWeights(t)
	fens := loadQuietFENs(t)

	baseNodes, base := aspRun(t, w, 999, 300, aspirationBenefitDepth, fens) // 关闭期望窗口
	aspNodes, asp := aspRun(t, w, aspirationMinDepth, aspirationDelta, aspirationBenefitDepth, fens)

	ratio := float64(aspNodes) / float64(baseNodes)
	if ratio > 0.92 {
		t.Errorf("期望窗口的节点比 %.3f 未达到 0.92 以内（δ=%d），收益被改没了",
			ratio, aspirationDelta)
	}
	isMate := func(v int) bool { return v > MateScore-MaxPly || v < -(MateScore-MaxPly) }
	for i := range base {
		if isMate(base[i].Score) != isMate(asp[i].Score) {
			t.Fatalf("第 %d 个局面的将杀判定在两侧不一致：全窗 %d vs 期望窗口 %d",
				i, base[i].Score, asp[i].Score)
		}
	}
	t.Logf("深度 %d、δ=%d：节点 %d vs 全窗 %d = %.3f×", aspirationBenefitDepth,
		aspirationDelta, aspNodes, baseNodes, ratio)
}
