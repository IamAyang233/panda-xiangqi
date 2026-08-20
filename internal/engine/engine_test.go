package engine

import (
	"context"
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

// 引擎不得用"长将逼和"逃避败局：红车可 e2↔d2 长将黑王 e9↔d9（重复判红负），
// 但红方整体劣势（黑有双车）。引擎应拒绝长将线，选择其它着法。
func TestEngineAvoidsLongCheck(t *testing.T) {
	// 黑双车 g1/h1 即将成杀，红只有一车可 e2↔d2 循环将军黑王。
	p, err := game.ParseFEN("4k4/9/9/9/9/9/9/4R4/9/2r1K2r1 w - - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	e := NewSimpleEngine()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	mv, err := e.BestMove(ctx, p, 10)
	if err != nil {
		t.Fatal(err)
	}
	longCheck := (mv.String() == "e2d2" || mv.String() == "d2e2")
	if longCheck {
		t.Errorf("引擎选择了长将着法 %s（会被判负），应避开", mv)
	}
}
