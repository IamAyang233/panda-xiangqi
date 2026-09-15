package engine

import (
	"strings"
	"testing"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
)

// 锁住发给引擎的 position 命令里**不含 moves 尾巴**。
//
// 曾经发的是 `position fen <FEN> moves <全历史>`：FEN 已含走子方与着数，
// 再附 moves 等于把整局重放一遍，而那些着法在 FEN 局面上多半非法
// （实测：FEN 已写 b（黑走），却还附带红方的 b2b5）。皮卡鱼会静默忽略
// 非法的 moves、仍按 FEN 作答，所以症状是偶发的非法着法而非稳定报错，
// 靠对局结果很难定位 —— 因此这里直接断言命令串。
func TestPositionCommandHasNoMovesTail(t *testing.T) {
	cases := []struct {
		name  string
		setup func(p *game.Position)
	}{
		{"初始局面", func(p *game.Position) {}},
		{"走一步后", func(p *game.Position) {
			ms := p.LegalMoves(p.Turn)
			p.Make(ms[0])
		}},
		{"走五步后", func(p *game.Position) {
			for i := 0; i < 5; i++ {
				ms := p.LegalMoves(p.Turn)
				if len(ms) == 0 {
					break
				}
				p.Make(ms[i%len(ms)])
			}
		}},
	}

	for _, c := range cases {
		p := game.NewPosition()
		c.setup(p)

		cmd := positionCommand(p)
		if !strings.HasPrefix(cmd, "position fen ") {
			t.Errorf("%s：命令前缀不对：%q", c.name, cmd)
		}
		if strings.Contains(cmd, " moves ") {
			t.Errorf("%s：命令混入了 moves 尾巴（会在 FEN 局面上重放历史）：%q", c.name, cmd)
		}
		// FEN 本身要被完整带上：字段数应为 6（棋盘 走子方 - - 半回合 全回合）。
		fen := strings.TrimPrefix(cmd, "position fen ")
		if n := len(strings.Fields(fen)); n != 6 {
			t.Errorf("%s：FEN 字段数应为 6，实际 %d：%q", c.name, n, fen)
		}
	}
}
