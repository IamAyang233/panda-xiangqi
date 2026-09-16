package game

import (
	"math/rand"
	"strings"
	"testing"
)

// sparseFEN 生成「双王 + 0~6 个随机子」的局面。
//
// 随机对局里将军、牵制、蹩腿这些形态相当稀有，而危险格筛选的推导恰恰依赖它们；
// 稀疏摆放能把这类配置密集地造出来（摆放位置不必合法 —— 走法生成按位置查表，
// 摆在不该在的地方只是少几个着法，不影响两个实现对拍）。
func sparseFEN(rng *rand.Rand) string {
	var g [10][9]byte // g[rank][file]
	g[rng.Intn(3)][3+rng.Intn(3)] = 'K'
	g[7+rng.Intn(3)][3+rng.Intn(3)] = 'k'

	reds := []byte{'A', 'B', 'N', 'R', 'C', 'P'}
	blacks := []byte{'a', 'b', 'n', 'r', 'c', 'p'}
	for i, n := 0, rng.Intn(7); i < n; i++ {
		ch := reds[rng.Intn(len(reds))]
		if rng.Intn(2) == 0 {
			ch = blacks[rng.Intn(len(blacks))]
		}
		if rank, file := rng.Intn(10), rng.Intn(9); g[rank][file] == 0 {
			g[rank][file] = ch
		}
	}

	var sb strings.Builder
	for r := 9; r >= 0; r-- {
		if r < 9 {
			sb.WriteByte('/')
		}
		empty := 0
		for f := 0; f < 9; f++ {
			if g[r][f] == 0 {
				empty++
				continue
			}
			if empty > 0 {
				sb.WriteByte(byte('0' + empty))
				empty = 0
			}
			sb.WriteByte(g[r][f])
		}
		if empty > 0 {
			sb.WriteByte(byte('0' + empty))
		}
	}
	if rng.Intn(2) == 0 {
		sb.WriteString(" w - - 0 1")
	} else {
		sb.WriteString(" b - - 0 1")
	}
	return sb.String()
}

// TestLegalMovesSparseRandom 在稀疏随机局面上对拍合法着法生成。
//
// 与 TestLegalMovesMatchRef 互补：那个走真实对局（形态自然但覆盖稀），
// 这个密集覆盖九宫附近的将军/牵制/蹩腿配置 —— 危险格筛选的推导全靠这些形态。
func TestLegalMovesSparseRandom(t *testing.T) {
	rng := rand.New(rand.NewSource(0x9a17e2026))
	var positions, inCheckPos int
	for i := 0; i < 6000; i++ {
		p, err := ParseFEN(sparseFEN(rng))
		if err != nil {
			t.Fatalf("生成的第 %d 个局面解析失败: %v", i, err)
		}
		positions++
		for _, side := range [2]int{Red, Black} {
			if p.InCheck(side) {
				inCheckPos++
			}
			got := p.LegalMoves(side)
			want := legalMovesRef(p, side)
			if !sameMoveSet(got, want) {
				t.Fatalf("稀疏局面 side=%d：合法着法集不同（%d vs %d 个）\nFEN: %s",
					side, len(got), len(want), p.FEN())
			}
		}
	}
	t.Logf("对拍 %d 个稀疏局面（双向共 %d 次判定，其中被将军 %d 次）",
		positions, positions*2, inCheckPos)
	if inCheckPos < 500 {
		t.Errorf("被将军的判定只有 %d 次，覆盖不足 —— 将军分支不能被危险格筛选走", inCheckPos)
	}
}

// TestKingSafetyInCheckMatches kingSafety 顺带给出的「是否被将军」必须与 InCheck
// 完全一致 —— 它是危险格筛选的开关，一旦误判成「未被将军」，筛选会被错误启用、
// 一次放过大量非法着法（这个 bug 真的发生过：漏了马的将军）。
func TestKingSafetyInCheckMatches(t *testing.T) {
	rng := rand.New(rand.NewSource(0x1c0ffee))
	var checked, inCheckCnt int

	check := func(p *Position, tag string) {
		for _, side := range [2]int{Red, Black} {
			checked++
			got, _ := p.bb.kingSafety(p.kingSq[side>>3], side)
			want := p.InCheck(side)
			if got {
				inCheckCnt++
			}
			if got != want {
				t.Fatalf("%s side=%d：kingSafety 的将军判定 %v 与 InCheck %v 不一致\nFEN: %s",
					tag, side, got, want, p.FEN())
			}
		}
	}

	for i := 0; i < 3000; i++ {
		p, err := ParseFEN(sparseFEN(rng))
		if err != nil {
			t.Fatal(err)
		}
		check(p, "稀疏")
	}
	for gameNo := 0; gameNo < 20; gameNo++ {
		p := NewPosition()
		for ply := 0; ply < 60; ply++ {
			check(p, "对局")
			moves := p.LegalMoves(p.Turn)
			if len(moves) == 0 {
				break
			}
			p.Make(moves[rng.Intn(len(moves))])
		}
	}
	t.Logf("对比 %d 次将军判定（其中判为被将军 %d 次）", checked, inCheckCnt)
}
