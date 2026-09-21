package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
)

// 一步杀：d8/f8 双车看管黑将，红车 a0 一手到位（e 线或 9 线）即杀。
func TestSimpleFindsMateIn1(t *testing.T) {
	p, err := game.ParseFEN("4k4/3R1R3/9/9/9/9/9/9/9/R1K6 w - - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	e := NewSimpleEngine()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mv, err := e.BestMove(ctx, p, 4)
	if err != nil {
		t.Fatal(err)
	}
	p.Make(mv)
	st := p.CheckStatus()
	if st.Result != game.ResultRedWin || st.Reason != game.ReasonCheckmate {
		t.Errorf("一步杀未找到: %s 走后 result=%s/%s", mv, st.Result, st.Reason)
	}
}

// 必吃局面：黑车 a9 无根，红车 a0 应直接吃掉。
func TestSimpleTakesFreeRook(t *testing.T) {
	p, err := game.ParseFEN("r3k4/9/9/9/9/9/9/9/9/R2K5 w - - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	e := NewSimpleEngine()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mv, err := e.BestMove(ctx, p, 8)
	if err != nil {
		t.Fatal(err)
	}
	if mv.String() != "a0a9" {
		t.Errorf("必吃车未吃: got %s want a0a9", mv)
	}
}

// 双车对光将残局：深度 3 的红方应在 40 手内将死黑方（搜索 + 将杀判定端到端验证）。
func TestTwoRooksBeatLoneKing(t *testing.T) {
	p, err := game.ParseFEN("4k4/9/9/9/9/9/9/9/9/R2K3R1 w - - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	e := NewSimpleEngine()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var result string
	for ply := 0; ply < 40; ply++ {
		st := p.CheckStatus()
		if st.Result != "" {
			result = st.Result
			break
		}
		level := 4 // 红方深度 3
		if p.Turn == game.Black {
			level = 2
		}
		mv, err := e.BestMove(ctx, p, level)
		if err != nil {
			t.Fatalf("第 %d 手出错: %v", ply, err)
		}
		p.Make(mv)
	}
	if result != game.ResultRedWin {
		t.Errorf("双车对光将未在限内将死, result=%q", result)
	}
}

// 初始局面深度 4 搜索应在 1.2s 内完成（T5.2 验收的宽松版）。
func TestSearchSpeed(t *testing.T) {
	p := game.NewPosition()
	e := NewSimpleEngine()
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := e.BestMove(ctx, p, 5); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 1200*time.Millisecond {
		t.Errorf("深度 4 搜索耗时 %v 超预算", elapsed)
	}
}

// 引擎不得破坏调用方局面。
func TestBestMoveKeepsPosition(t *testing.T) {
	p := game.NewPosition()
	fen := p.FEN()
	e := NewSimpleEngine()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := e.BestMove(ctx, p, 6)
	if err != nil {
		t.Fatal(err)
	}
	if p.FEN() != fen {
		t.Error("BestMove 破坏了调用方局面")
	}
}

// 档位配置覆盖 1~16。
func TestLevelConfigs(t *testing.T) {
	for lv := 1; lv <= 16; lv++ {
		c := cfgOf(lv)
		if c.maxDepth < 1 || c.budget <= 0 {
			t.Errorf("档位 %d 配置非法: %+v", lv, c)
		}
	}
}

// TestEnginesReturnLegalMoveInTensePosition 在「红王被双车逼杀」的紧局面上做冒烟检查：
// 两条引擎路径都必须给出**合法**着法，且**不得改动调用方的局面**。
//
// ⚠️ 这个测试原来叫 TestEngineAvoidsLongCheck，断言「引擎不会选择长将着法
// e2d2/d2e2」。但那个局面里红王正被 c0 黑车将军，红方**只有 e0e1 一个合法着法**
// —— e2d2 压根不合法、永远不可能被选中，所以断言恒真、**零判别力**：
// 它从没验证过任何东西（2026-09-21 发现）。同理它的注释写「黑双车 g1/h1」，
// 而 FEN 里两车在 c0/h0，也不符。
//
// 「引擎会不会把长将当成和棋」这件事该由**确定性**的关卡把关，放在
// internal/search 的 TestRepetitionScoreLongCheck（直接断言重复分值必须是长将方
// 判负），而不是靠一个全输局面下的着法选择（那种局面里选哪步都不会更好，
// 断言「不选某步」在原理上就不成立）。这里只保留它本来真正测到的东西。
func TestEnginesReturnLegalMoveInTensePosition(t *testing.T) {
	const fen = "4k4/9/9/9/9/9/9/4R4/9/2r1K2r1 w - - 0 1"

	check := func(t *testing.T, tag string, mv game.Move, err error, p *game.Position, fen0 string) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s 出错: %v", tag, err)
		}
		if !p.IsLegal(mv) {
			t.Errorf("%s 给出非法着法 %s", tag, mv)
		}
		if got := p.FEN(); got != fen0 {
			t.Errorf("%s 改动了调用方的局面：前 %s，后 %s", tag, fen0, got)
		}
	}

	t.Run("simple", func(t *testing.T) {
		p, err := game.ParseFEN(fen)
		if err != nil {
			t.Fatal(err)
		}
		fen0 := p.FEN()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		mv, err := NewSimpleEngine().BestMove(ctx, p, 10)
		check(t, "降级引擎", mv, err, p, fen0)
	})

	t.Run("native", func(t *testing.T) {
		e := NewNativeEngine(requireWeights(t), 1)
		defer e.Close()
		p, err := game.ParseFEN(fen)
		if err != nil {
			t.Fatal(err)
		}
		fen0 := p.FEN()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		mv, err := e.BestMove(ctx, p, 10)
		check(t, "原生引擎", mv, err, p, fen0)
	})
}

// 坏引擎（非 UCI 程序）必须进诊断而不是静默失败：这是排查"皮卡鱼启动不了"的关键信息。
func TestManagerDiagnosticsOnBrokenEngine(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "pikafish")
	if err := os.WriteFile(p, []byte("definitely not a UCI engine\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	m := NewManager(p)
	if m.HasUCI() {
		t.Skip("测试文件意外可执行且通过握手，跳过")
	}
	diags := m.Diagnostics()
	if len(diags) == 0 {
		t.Fatal("坏引擎应产生诊断信息，实际为空")
	}
	found := false
	for _, d := range diags {
		if strings.Contains(d, "pikafish") && strings.Contains(d, "→") {
			found = true
			t.Logf("diag: %s", d)
		}
	}
	if !found {
		t.Fatalf("诊断应含候选路径与失败原因，实际: %v", diags)
	}
}

// 缺失路径不应进诊断（避免噪音），存在但启动失败的才进。
func TestManagerSkipsMissingCandidates(t *testing.T) {
	m := NewManager(filepath.Join(t.TempDir(), "no-such-engine-file"))
	if m.HasUCI() {
		t.Fatal("不存在路径不应启动引擎")
	}
	for _, d := range m.Diagnostics() {
		if strings.Contains(d, "no-such-engine-file") && strings.Contains(d, "→") {
			t.Fatalf("不存在路径不应产生诊断条目: %v", m.Diagnostics())
		}
	}
}

// candidatePaths 必须包含低指令集兜底名（老 CPU 设备的 avx2 版会 Illegal instruction 崩溃）。
func TestCandidatePathsIncludeLegacyFallbacks(t *testing.T) {
	list := candidatePaths("")
	joined := strings.Join(list, "\n")
	for _, want := range []string{"pikafish-sse41", "pikafish-noavx", "pikafish-legacy"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("候选列表缺少兜底名 %s", want)
		}
	}
}
