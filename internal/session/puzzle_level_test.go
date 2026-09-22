package session

import (
	"testing"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/llm"
	"github.com/IamAyang233/panda-xiangqi/internal/puzzle"
)

// newPuzzleSession 构造一个残局会话（不 Join 连接，仅用于断言内部档位计算）。
func newPuzzleSession(pz *puzzle.Puzzle, level int) *Session {
	return NewSession(ModePuzzle, game.Red, level, llm.DefaultConfig(), pz, nil)
}

// TestPuzzleLevelBuiltinMapping 内置五档难度的守方档位映射必须保持原样。
// 这条是回归底线：为自摆残局改 puzzleLevel 时不能把既有映射一起改坏。
func TestPuzzleLevelBuiltinMapping(t *testing.T) {
	cases := []struct {
		diff string
		want int
	}{
		{"入门", 2}, {"初级", 3}, {"中级", 5}, {"高级", 7}, {"大师", 9},
	}
	for _, c := range cases {
		pz := &puzzle.Puzzle{ID: "p-" + c.diff, Difficulty: c.diff, FEN: "3k5/9/9/9/9/9/9/9/R8/4K4 w"}
		// 玩家选的档位刻意与期望不同：内置难度必须按难度映射，不受玩家选择影响。
		s := newPuzzleSession(pz, 16)
		if got := s.puzzleLevel(); got != c.want {
			t.Errorf("难度 %s 期望档位 %d，实际 %d", c.diff, c.want, got)
		}
	}
}

// TestPuzzleLevelCustomUsesPlayerLevel 自摆残局（难度为「自定义」等非内置值）的
// 守方档位必须取玩家在开局时选的档位。
//
// 原先 default 分支固定返回 9，会让玩家在摆局时选的难度完全失效 —— 选第 1 档和
// 选第 16 档打起来一样强。这里用两个不同 Level 断言，回退到固定值会立刻失败。
func TestPuzzleLevelCustomUsesPlayerLevel(t *testing.T) {
	for _, lv := range []int{1, 4, 12, 16} {
		pz := &puzzle.Puzzle{
			ID: "custom-x", Difficulty: "自定义", PlayerSide: "red", Goal: "win",
			FEN: "3k5/9/9/9/9/9/9/9/R8/4K4 w", ParMoves: 0,
		}
		s := newPuzzleSession(pz, lv)
		if got := s.puzzleLevel(); got != lv {
			t.Errorf("自摆残局 玩家档位=%d 应被采用，实际 %d", lv, got)
		}
	}
	// 空难度（理论上不该出现）同样落到玩家档位，而不是某个魔法数字。
	pz := &puzzle.Puzzle{ID: "custom-empty", FEN: "3k5/9/9/9/9/9/9/9/R8/4K4 w"}
	if got := newPuzzleSession(pz, 6).puzzleLevel(); got != 6 {
		t.Errorf("空难度应取玩家档位 6，实际 %d", got)
	}
}

// TestPuzzleLevelNonPuzzleMode 非残局会话（pz == nil）直接用玩家档位。
func TestPuzzleLevelNonPuzzleMode(t *testing.T) {
	s := NewSession(ModeEngine, game.Red, 7, llm.DefaultConfig(), nil, nil)
	if got := s.puzzleLevel(); got != 7 {
		t.Fatalf("非残局模式应取玩家档位 7，实际 %d", got)
	}
}
