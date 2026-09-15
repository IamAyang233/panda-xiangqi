package engine

import (
	"context"
	"testing"
	"time"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
)

// 连续自对弈：每一步都必须给出合法着法。
//
// 非法着法说明引擎内部残留了上一次搜索的状态（如评估侧局面没对齐、
// 置换表项跨局复用出错）。跑一局要一分多钟，所以默认跳过，
// 用 `-run TestNativeEngineSelfPlayLegality -count=1` 单独跑。
func TestNativeEngineSelfPlayLegality(t *testing.T) {
	if testing.Short() {
		t.Skip("自对弈耗时较长，-short 下跳过")
	}
	e := NewNativeEngine(testFlatPath, 8)
	defer e.Close()
	if err := e.Warmup(); err != nil {
		t.Fatal(err)
	}

	for g := 0; g < 4; g++ {
		p := game.NewPosition()
		for ply := 0; ply < 120; ply++ {
			moves := p.LegalMoves(p.Turn)
			if len(moves) == 0 {
				break
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			mv, err := e.BestMoveTimed(ctx, p, 200*time.Millisecond)
			cancel()
			if err != nil {
				t.Fatalf("第 %d 局 %d 步搜索出错：%v", g, ply, err)
			}
			if !p.IsLegal(mv) {
				t.Fatalf("第 %d 局 %d 步给出非法着法 %s，局面 %s", g, ply, mv, p.FEN())
			}
			p.Make(mv)
			if st := p.CheckStatus(); st.Result != game.ResultNone {
				break
			}
		}
	}
}
