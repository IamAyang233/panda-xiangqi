package nnue

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
)

// TestApplyMatchesRefresh 是增量累加器的核心正确性关卡。
//
// 与 SyncTo（全量枚举 + 差集）不同，Apply 直接用走子时累积的脏信息做
// add/sub，不再枚举全盘特征。算错不会报错，只会让评估值静默偏移，
// 所以必须逐局面、逐元素对拍。
//
// 覆盖三类容易出错的情形：普通走子与吃子（含滑子射线开通/遮挡）、
// 将的移动（特征桶变化 → 触发全量重建）、回溯后继续走（脏信息被截断）。
func TestApplyMatchesRefresh(t *testing.T) {
	w, err := Load(flatPath)
	if err != nil {
		t.Skip("未找到展开后的权重，跳过")
	}

	fens := []string{
		game.InitialFEN,
		"3k5/9/9/9/9/9/9/9/9/4K4 w - - 0 1", // 只剩双方将帅：将可自由移动，覆盖桶变化
		"rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR b - - 0 1",
		"3k5/2P1P4/4b4/9/9/9/9/4p1p2/2p1p4/3K1C3 w - - 0 1", // 子力交错，容易出现吃子与炮架变化
		"2bak4/9/4c4/9/9/9/9/4C4/9/3AK4 w - - 0 1",
	}

	total := 0
	for _, fen := range fens {
		p, err := game.ParseFEN(fen)
		if err != nil {
			t.Fatalf("解析 %s 失败: %v", fen, err)
		}
		rng := rand.New(rand.NewSource(int64(len(fen)) * 7919))

		var pos Position
		pos.ResetFromGame(&p.Board, p.Turn>>3)
		var inc, ref Accumulator

		check := func(what string) {
			t.Helper()
			prePieces, preThreats := len(pos.pendingPieces), len(pos.pendingThreats)
			preVer, preBase, preStale := pos.version, pos.pendingBase, pos.stale
			w.Apply(&pos, &inc)
			w.RefreshFromPosition(&pos, &ref)
			if !accumEqual(&inc, &ref) {
				detail := ""
				for c := 0; c < colorNB && detail == ""; c++ {
					for i := 0; i < L1; i++ {
						if inc.PsqAcc[c][i] != ref.PsqAcc[c][i] {
							detail = fmt.Sprintf("PsqAcc c=%d i=%d inc=%d ref=%d", c, i, inc.PsqAcc[c][i], ref.PsqAcc[c][i])
							break
						}
					}
					for k := 0; k < PSQTBuckets; k++ {
						if inc.PsqPsqt[c][k] != ref.PsqPsqt[c][k] {
							detail = fmt.Sprintf("PsqPsqt c=%d k=%d inc=%d ref=%d", c, k, inc.PsqPsqt[c][k], ref.PsqPsqt[c][k])
							break
						}
					}
					for i := 0; i < L1; i++ {
						if inc.ThrAcc[c][i] != ref.ThrAcc[c][i] {
							detail = fmt.Sprintf("ThrAcc c=%d i=%d inc=%d ref=%d", c, i, inc.ThrAcc[c][i], ref.ThrAcc[c][i])
							break
						}
					}
					for k := 0; k < PSQTBuckets; k++ {
						if inc.ThrPsqt[c][k] != ref.ThrPsqt[c][k] {
							detail = fmt.Sprintf("ThrPsqt c=%d k=%d inc=%d ref=%d", c, k, inc.ThrPsqt[c][k], ref.ThrPsqt[c][k])
							break
						}
					}
				}
				t.Fatalf("%s 后增量与全量不一致\n  pending 前: pieces=%d threats=%d version=%d base=%d stale=%v\n  差异: %s\n起始 FEN: %s\n当前 FEN: %s",
					what, prePieces, preThreats, preVer, preBase, preStale, detail, fen, p.FEN())
			}
		}
		check("起始")

		depth := 0
		for round := 0; round < 60; round++ {
			adv := 1 + rng.Intn(3)
			for i := 0; i < adv; i++ {
				moves := p.LegalMoves(p.Turn)
				if len(moves) == 0 {
					break
				}
				m := moves[rng.Intn(len(moves))]
				p.Make(m)
				pos.Make(int(m.From), int(m.To))
				depth++
				total++
				// 只在部分节点评估，模拟搜索里跳过节点：
				// 累加器会停在更早的局面，脏信息必须能跨多步累积。
				if diagCheckGap <= 1 || total%diagCheckGap == 0 {
					check("前进")
				}
			}
			for back := rng.Intn(3); back > 0 && depth > 0; back-- {
				p.Unmake()
				pos.Unmake()
				depth--
				check("回退")
			}
			if depth == 0 {
				// 全部退回起点，换个起点重来意义不大，直接继续往前。
				continue
			}
		}
		check("最终")
	}
	t.Logf("5 个局面随机前进/回退共 %d 步，增量累加器与全量重建全程逐位一致 ✓", total)
}

// TestApplyHandlesKingMove 单独覆盖「将移动」：此时特征桶（king bucket）
// 变化，PSQ 特征的索引整体改变，必须走全量重建而不是局部 add/sub。
func TestApplyHandlesKingMove(t *testing.T) {
	w, err := Load(flatPath)
	if err != nil {
		t.Skip("未找到展开后的权重，跳过")
	}
	p, err := game.ParseFEN("3k5/9/9/9/9/9/9/9/9/4K4 w - - 0 1")
	if err != nil {
		t.Fatal(err)
	}

	var pos Position
	pos.ResetFromGame(&p.Board, p.Turn>>3)
	var inc, ref Accumulator
	w.Apply(&pos, &inc)

	// 双方轮流挪将，每一步都跨过不同的 king bucket。
	for i := 0; i < 20; i++ {
		moves := p.LegalMoves(p.Turn)
		if len(moves) == 0 {
			t.Fatalf("第 %d 步无合法着法", i)
		}
		// 只挑将的着法，确保覆盖桶切换。
		var kingMoves []game.Move
		for _, m := range moves {
			if game.TypeOf(p.PieceAt90(int(m.From))) == game.King {
				kingMoves = append(kingMoves, m)
			}
		}
		if len(kingMoves) == 0 {
			break
		}
		m := kingMoves[i%len(kingMoves)]
		p.Make(m)
		pos.Make(int(m.From), int(m.To))

		w.Apply(&pos, &inc)
		w.RefreshFromPosition(&pos, &ref)
		if !accumEqual(&inc, &ref) {
			t.Fatalf("第 %d 步（%s）后将移动后增量与全量不一致\nFEN: %s", i+1, m, p.FEN())
		}
	}
	t.Logf("将连续移动 20 步（跨多个特征桶），增量与全量一致 ✓")
}

// BenchmarkApplyIncremental 测「走一步后增量更新」的耗时，
// 对照 BenchmarkRefreshFull（全量重建）。
func BenchmarkApplyIncremental(b *testing.B) {
	w, err := Load(flatPath)
	if err != nil {
		b.Skip("未找到展开后的权重，跳过")
	}
	p, _ := game.ParseFEN(game.InitialFEN)
	rng := rand.New(rand.NewSource(7))

	var pos Position
	pos.ResetFromGame(&p.Board, p.Turn>>3)
	var a Accumulator
	w.Apply(&pos, &a)

	type step struct{ from, to int }
	var steps []step
	for i := 0; i < 64; i++ {
		moves := p.LegalMoves(p.Turn)
		if len(moves) == 0 {
			break
		}
		m := moves[rng.Intn(len(moves))]
		steps = append(steps, step{int(m.From), int(m.To)})
		p.Make(m)
		pos.Make(int(m.From), int(m.To))
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s := steps[i%len(steps)]
		pos.Make(s.from, s.to)
		w.Apply(&pos, &a)
		pos.Unmake()
	}
}

// BenchmarkMakeCapture 单独测「吃子走子」这条路径 —— 它一次要跑 3 次
// updateThreats（removePiece + swapPiece 里的两趟），是威胁增量维护里最贵的分支。
// 与 BenchmarkApplyIncremental 的差值即这条路线的额外开销。
func BenchmarkMakeCapture(b *testing.B) {
	// 用一个人为构造的吃子局面：黑方有吃子着法，且吃子后射线会开通。
	fen := "r1bakabnr/9/1cn2c3/p1p1p1p1p/9/9/P1P1P1P1P/1C2R2C1/9/RNBAKABN1 b - - 0 1"
	q, err := game.ParseFEN(fen)
	if err != nil {
		b.Skip("构造局面解析失败")
	}

	var pos Position
	pos.ResetFromGame(&q.Board, q.Turn>>3)

	// 找黑方的吃子着法。
	var pick game.Move
	found := false
	for _, m := range q.LegalMoves(q.Turn) {
		if q.Board[m.To] != 0 {
			pick, found = m, true
			break
		}
	}
	if !found {
		b.Skip("构造局面没有吃子着法")
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pos.Make(int(pick.From), int(pick.To))
		pos.Unmake()
	}
}

// BenchmarkPropagate 单独测前向推理（fc_0 是主体），用于量化优化效果。
func BenchmarkPropagate(b *testing.B) {
	w, err := Load(flatPath)
	if err != nil {
		b.Skip("未找到展开后的权重，跳过")
	}
	p, _ := game.ParseFEN(game.InitialFEN)
	var pos Position
	pos.ResetFromGame(&p.Board, p.Turn>>3)
	var a Accumulator
	w.Apply(&pos, &a)
	bucket := pos.LayerStackBucket()
	_, feat := a.Transform(pos.SideToMove(), bucket)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = w.Layers[bucket].propagate(&feat)
	}
}

// transformSink 防止基准测试的调用被编译器消除 —— 结果未被使用时，
// 整个调用都可能被删掉，量出来的就是假数字。
var transformSink [L1]byte

// BenchmarkTransform 测 Transform 内层：512 次 clamp + 相乘 + 右移。
//
// 单独列出来是因为 pprof 显示这块占搜索时间约 18.6%（reluMul 8.27% +
// clamp16 5.80% + 本体 4.56%），是 slidingAttackBoth 之外最大的一块。
// 它的两个 clamp 分支在随机局面上不可预测，改成无分支实现是本轮的目标。
func BenchmarkTransform(b *testing.B) {
	w, err := Load(flatPath)
	if err != nil {
		b.Skip("未找到展开后的权重，跳过")
	}
	p, _ := game.ParseFEN(game.InitialFEN)
	var pos Position
	pos.ResetFromGame(&p.Board, p.Turn>>3)
	var a Accumulator
	w.Apply(&pos, &a)
	bucket := pos.LayerStackBucket()
	side := pos.SideToMove()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, transformSink = a.Transform(side, bucket)
	}
}

// BenchmarkFeatureBucket 测 O(1) 版特征桶（对照 Board 版的全盘遍历）。
func BenchmarkFeatureBucket(b *testing.B) {
	p, _ := game.ParseFEN(game.InitialFEN)
	var pos Position
	pos.ResetFromGame(&p.Board, p.Turn>>3)
	board := boardOfPos(p)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = pos.FeatureBucket(0)
	}
	b.StopTimer()
	_ = board
}
