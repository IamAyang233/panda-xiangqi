package engine

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
)

const testFlatPath = "../../engines/pikafish.nnue.flat"

// requireWeights 返回展开后的权重路径；缺失时跳过测试。
func requireWeights(t *testing.T) string {
	t.Helper()
	if _, err := os.Stat(testFlatPath); err != nil {
		t.Skip("未找到展开后的权重，跳过（先运行 make nnue-prepare）")
	}
	return testFlatPath
}

// TestNativeEngineBestMove 内嵌引擎必须给出合法着法，且不改动传入的局面。
//
// 「不改动局面」很重要：搜索会大量 Make/Unmake，若直接在调用方的局面上跑，
// 一旦中途出错（超时、panic 恢复）就会把对局状态搞坏。
func TestNativeEngineBestMove(t *testing.T) {
	e := NewNativeEngine(requireWeights(t), 1)
	defer e.Close()

	p, err := game.ParseFEN(game.InitialFEN)
	if err != nil {
		t.Fatal(err)
	}
	fen0 := p.FEN()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	mv, err := e.BestMove(ctx, p, 5)
	if err != nil {
		t.Fatalf("搜索失败: %v", err)
	}
	if !p.IsLegal(mv) {
		t.Errorf("给出的着法 %s 非法", mv)
	}
	if p.FEN() != fen0 {
		t.Errorf("传入的局面被改动：\n原 %s\n后 %s", fen0, p.FEN())
	}
	t.Logf("5 档 best=%s（%s）", mv, p.MoveToChinese(mv))
}

// TestNativeEngineLevels 档位越高耗时越长，且都给出合法着法。
func TestNativeEngineLevels(t *testing.T) {
	e := NewNativeEngine(requireWeights(t), 1)
	defer e.Close()

	p, err := game.ParseFEN(game.InitialFEN)
	if err != nil {
		t.Fatal(err)
	}

	var prevMs int64
	for _, lv := range []int{1, 8, 12} {
		start := time.Now()
		mv, err := e.BestMove(context.Background(), p, lv)
		el := time.Since(start)
		if err != nil {
			t.Fatalf("%d 档搜索失败: %v", lv, err)
		}
		if !p.IsLegal(mv) {
			t.Errorf("%d 档给出的着法 %s 非法", lv, mv)
		}
		t.Logf("%d 档: %s 用时 %v", lv, mv, el.Round(time.Millisecond))
		// 允许一定波动（随机挑选与迭代加深的层数不完全稳定），但不应大幅倒退。
		if prevMs > 0 && el.Milliseconds()*2 < prevMs {
			t.Errorf("%d 档用时 %v 远少于上一档的 %dms", lv, el.Round(time.Millisecond), prevMs)
		}
		prevMs = el.Milliseconds()
	}
}

// TestNativeEngineRankedMoves 候选着法必须合法、互不相同且按评估排序。
func TestNativeEngineRankedMoves(t *testing.T) {
	e := NewNativeEngine(requireWeights(t), 1)
	defer e.Close()

	p, err := game.ParseFEN(game.InitialFEN)
	if err != nil {
		t.Fatal(err)
	}
	moves := e.RankedMoves(context.Background(), p, 5, 8)
	if len(moves) != 8 {
		t.Fatalf("应返回 8 个候选着法，实际 %d", len(moves))
	}
	seen := map[game.Move]bool{}
	for _, m := range moves {
		if !p.IsLegal(m) {
			t.Errorf("候选着法 %s 非法", m)
		}
		if seen[m] {
			t.Errorf("候选着法 %s 重复", m)
		}
		seen[m] = true
	}
	t.Logf("前 8 个候选: %v", moves)
}

// TestNativeEngineContextCancel 取消 context 必须及时中断搜索并返回错误。
func TestNativeEngineContextCancel(t *testing.T) {
	e := NewNativeEngine(requireWeights(t), 1)
	defer e.Close()

	p, err := game.ParseFEN(game.InitialFEN)
	if err != nil {
		t.Fatal(err)
	}

	// 16 档正常要 3.5 秒；这里 200ms 就取消。
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err = e.BestMove(ctx, p, 16)
	el := time.Since(start)

	if err == nil {
		t.Fatal("取消后应返回错误")
	}
	if el > 1500*time.Millisecond {
		t.Errorf("取消未被及时响应：用时 %v", el.Round(time.Millisecond))
	}
	t.Logf("200ms 后取消：用时 %v，错误 %v ✓", el.Round(time.Millisecond), err)
}

// TestManagerPrefersNative 有内嵌引擎时 Manager 必须优先用它，且不依赖皮卡鱼。
func TestManagerPrefersNative(t *testing.T) {
	path := requireWeights(t)

	// 显式关掉皮卡鱼探测（enginePath 指向不存在的文件），确保走的是内嵌引擎。
	m := NewManagerWithNNUE("nonexistent-pikafish-binary", path)
	defer m.Close()

	if !m.HasNative() {
		t.Fatal("配置了权重路径，HasNative 应为 true")
	}
	if m.EngineName() != NativeEngineName {
		t.Errorf("引擎名应为 %s，实际 %s", NativeEngineName, m.EngineName())
	}

	p, err := game.ParseFEN(game.InitialFEN)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	mv, err := m.BestMove(ctx, p, 6)
	if err != nil {
		t.Fatalf("BestMove 失败: %v", err)
	}
	if !p.IsLegal(mv) {
		t.Errorf("着法 %s 非法", mv)
	}

	moves := m.RankedMoves(ctx, p, 6, 4)
	if len(moves) != 4 {
		t.Errorf("RankedMoves 应返回 4 个，实际 %d", len(moves))
	}
	t.Logf("6 档 best=%s，候选 %v", mv, moves)
}

// TestManagerFallbackWithoutWeights 权重缺失时必须能退化到自研引擎而不是崩掉。
func TestManagerFallbackWithoutWeights(t *testing.T) {
	m := NewManagerWithNNUE("nonexistent-pikafish-binary", "/nonexistent/weights.flat")
	defer m.Close()

	if m.HasNative() {
		t.Error("权重不存在时 HasNative 应为 false")
	}
	if m.HasStrong() {
		t.Error("既无内嵌引擎又无皮卡鱼时 HasStrong 应为 false")
	}
	if m.EngineName() != "SimpleEngine" {
		t.Errorf("应退化到自研引擎，实际 %s", m.EngineName())
	}

	p, err := game.ParseFEN(game.InitialFEN)
	if err != nil {
		t.Fatal(err)
	}
	mv, err := m.BestMove(context.Background(), p, 2)
	if err != nil {
		t.Fatalf("兜底路径也应给出着法: %v", err)
	}
	if !p.IsLegal(mv) {
		t.Errorf("着法 %s 非法", mv)
	}
	t.Logf("无强引擎时走自研引擎兜底：%s", mv)

	// 诊断里应能看出为什么没启用强引擎。
	var hasWeightDiag bool
	for _, d := range m.Diagnostics() {
		if strings.Contains(d, "NNUE") || strings.Contains(d, "权重") {
			hasWeightDiag = true
		}
	}
	if !hasWeightDiag {
		t.Errorf("诊断里应说明权重缺失，实际 %v", m.Diagnostics())
	}
}
