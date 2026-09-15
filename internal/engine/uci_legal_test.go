package engine

import (
	"context"
	"testing"
	"time"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
)

// 让皮卡鱼在**有历史**的局面上作决策，着法必须合法。
//
// 曾经把 `position fen <FEN> moves <全历史>` 一起发过去：FEN 已经完整描述了
// 当前局面，再附上 moves 等于把整局历史重放一遍，引擎算的是另一个局面，
// 给出的着法在真实局面上非法（对局工具里表现为 "illegal:" 中断）。
func TestUCIBestMoveOnPositionWithHistory(t *testing.T) {
	e, err := NewUCIEngine("../../dist/pikafish.exe")
	if err != nil {
		t.Skipf("皮卡鱼不可用，跳过：%v", err)
	}
	defer e.Close()

	p := game.NewPosition()
	played := 0
	for ply := 0; ply < 24; ply++ {
		moves := p.LegalMoves(p.Turn)
		if len(moves) == 0 {
			break
		}
		// 先随意走几步，让局面带上历史栈
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		mv, err := e.BestMoveTimed(ctx, p, 200*time.Millisecond, 20)
		cancel()
		if err != nil {
			t.Fatalf("第 %d 步搜索出错：%v", ply, err)
		}
		if !p.IsLegal(mv) {
			t.Fatalf("第 %d 步（历史长度 %d）给出非法着法 %s，局面 %s",
				ply, p.MoveCount(), mv, p.FEN())
		}
		p.Make(mv)
		played++
		if st := p.CheckStatus(); st.Result != game.ResultNone {
			break
		}
	}
	if played < 10 {
		t.Fatalf("只走了 %d 步，测试未真正展开", played)
	}
}
