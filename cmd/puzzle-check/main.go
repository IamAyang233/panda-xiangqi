// puzzle-check 残局批量校验工具（计划书 A16）：
// 回放每关记录的"正解"主变，校验终局符合目标——胜负关判执子方将死/困毙，
// 和棋关判三次重复（或 60 回合 / 子力不足）。支持红先/黑先、胜/和。
//
// 默认仅校验、不写回；用 -out 指定输出文件才写回（通常用于 -regen 生成主变后）。
// 用 -regen 可对"缺少正解"的胜负关用引擎自对弈生成主变（仅支持红先胜）。
//
// 用法：
//   go run ./cmd/puzzle-check -in internal/puzzle/data/endgames.json
//   go run ./cmd/puzzle-check -in internal/puzzle/data/endgames_hard.json
//   go run ./cmd/puzzle-check -in puzzles.json -out puzzles.json -regen
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/IamAyang233/panda-xiangqi/internal/engine"
	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/puzzle"
)

func main() {
	in := flag.String("in", "internal/puzzle/data/puzzles.json", "输入残局 JSON")
	out := flag.String("out", "", "输出文件（默认不写回，仅校验）")
	level := flag.Int("level", 13, "验证引擎档位（自研引擎深度档）")
	regen := flag.Bool("regen", false, "对缺少正解的胜负关用引擎自对弈生成主变（仅支持红先胜）")
	flag.Parse()

	data, err := os.ReadFile(*in)
	if err != nil {
		fatal(err)
	}
	var puzzles []*puzzle.Puzzle
	if err := json.Unmarshal(data, &puzzles); err != nil {
		fatal(err)
	}

	eng := engine.NewSimpleEngine()
	okCount := 0
	for _, p := range puzzles {
		pos, err := game.ParseFEN(p.FEN)
		if err != nil {
			fmt.Printf("%-8s %-12s ✗ FEN 非法: %v\n", p.ID, p.Name, err)
			p.Verified = false
			continue
		}
		wantTurn := turnOf(p.PlayerSide)
		if pos.Turn != wantTurn {
			fmt.Printf("%-8s %-12s ✗ 轮走方与执子方不一致（期望 %s）\n", p.ID, p.Name, sideName(wantTurn))
			p.Verified = false
			continue
		}

		switch p.Goal {
		case "draw":
			if len(p.Solution) == 0 {
				fmt.Printf("%-8s %-12s ⊘ 和棋关缺少主变，需人工复核\n", p.ID, p.Name)
				continue
			}
			if verifyDrawLine(p) {
				p.Verified = true
				okCount++
				fmt.Printf("%-8s %-12s ✓ 和棋（三次重复）\n", p.ID, p.Name)
			} else {
				p.Verified = false
				fmt.Printf("%-8s %-12s ✗ 和棋主变未达成三次重复\n", p.ID, p.Name)
			}
		case "win":
			if len(p.Solution) == 0 {
				if *regen {
					if p.PlayerSide == "black" {
						fmt.Printf("%-8s %-12s ⊘ 黑先胜暂不支持自对弈生成\n", p.ID, p.Name)
						continue
					}
					line, ok, err := verifyWin(eng, p, *level)
					if err != nil {
						fmt.Printf("%-8s %-12s 错误: %v\n", p.ID, p.Name, err)
						continue
					}
					if !ok {
						p.Verified = false
						fmt.Printf("%-8s %-12s ✗ 未能按预期验证\n", p.ID, p.Name)
						continue
					}
					p.Solution = line
					p.ParMoves = (len(line) + 1) / 2
					p.Verified = true
					okCount++
					fmt.Printf("%-8s %-12s ✓ 红先胜 %d 步: %v\n", p.ID, p.Name, p.ParMoves, line)
				} else {
					fmt.Printf("%-8s %-12s ⊘ 缺少正解（用 -regen 生成）\n", p.ID, p.Name)
				}
				continue
			}
			if verifyWinLine(p) {
				p.Verified = true
				okCount++
				fmt.Printf("%-8s %-12s ✓ 主变达成 %s 将死\n", p.ID, p.Name, sideName(winnerOf(p.PlayerSide)))
			} else {
				p.Verified = false
				fmt.Printf("%-8s %-12s ✗ 主变未达成 %s 将死\n", p.ID, p.Name, sideName(winnerOf(p.PlayerSide)))
			}
		default:
			fmt.Printf("%-8s %-12s ✗ 未知 goal=%s\n", p.ID, p.Name, p.Goal)
			p.Verified = false
		}
	}

	if *out != "" {
		blob, err := json.MarshalIndent(puzzles, "", "  ")
		if err != nil {
			fatal(err)
		}
		if err := os.WriteFile(*out, append(blob, '\n'), 0o644); err != nil {
			fatal(err)
		}
		fmt.Printf("已写回 %s\n", *out)
	}
	fmt.Printf("\n完成：%d/%d 关验证通过\n", okCount, len(puzzles))
}

// verifyWinLine 回放记录的正解，校验终局为执子方将死/困毙。
func verifyWinLine(p *puzzle.Puzzle) bool {
	pos, err := game.ParseFEN(p.FEN)
	if err != nil {
		return false
	}
	for _, uci := range p.Solution {
		m, ok := game.MoveFromUCI(uci)
		if !ok || !pos.IsLegal(m) {
			return false
		}
		pos.Make(m)
	}
	st := pos.CheckStatus()
	if st.Result != winResultOf(winnerOf(p.PlayerSide)) {
		return false
	}
	return st.Reason == game.ReasonCheckmate || st.Reason == game.ReasonStalemate
}

// verifyDrawLine 回放记录的和棋主变，校验终局为和棋（三次重复/60 回合/子力不足）。
func verifyDrawLine(p *puzzle.Puzzle) bool {
	pos, err := game.ParseFEN(p.FEN)
	if err != nil {
		return false
	}
	for _, uci := range p.Solution {
		m, ok := game.MoveFromUCI(uci)
		if !ok || !pos.IsLegal(m) {
			return false
		}
		pos.Make(m)
	}
	st := pos.CheckStatus()
	return st.IsDraw || st.Result == game.ResultDraw
}

// verifyWin 引擎自对弈：红黑双方同档全力，红在 maxRedMoves 手内将死即验证通过。
// 仅用于 -regen 生成红先胜主变（保持原有行为）。
func verifyWin(eng *engine.SimpleEngine, p *puzzle.Puzzle, level int) ([]string, bool, error) {
	pos, err := game.ParseFEN(p.FEN)
	if err != nil {
		return nil, false, fmt.Errorf("FEN 非法: %w", err)
	}
	maxRed := p.ParMoves
	if maxRed <= 0 {
		maxRed = 6
	}
	maxRed += 2 // 宽限 2 步

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var line []string
	for redMoves := 0; redMoves < maxRed; {
		st := pos.CheckStatus()
		if st.Result != "" {
			if st.Result == game.ResultRedWin && st.Reason == game.ReasonCheckmate {
				return line, true, nil
			}
			return nil, false, nil
		}
		mv, err := eng.BestMove(ctx, pos, level)
		if err != nil {
			return nil, false, err
		}
		pos.Make(mv)
		line = append(line, mv.String())
		if pos.Turn == game.Red {
			redMoves++
		}
	}
	st := pos.CheckStatus()
	if st.Result == game.ResultRedWin && st.Reason == game.ReasonCheckmate {
		return line, true, nil
	}
	return nil, false, nil
}

func turnOf(playerSide string) int {
	if playerSide == "black" {
		return game.Black
	}
	return game.Red
}

func winnerOf(playerSide string) int { return turnOf(playerSide) }

func sideName(c int) string {
	if c == game.Black {
		return "black"
	}
	return "red"
}

func winResultOf(winner int) string {
	if winner == game.Black {
		return game.ResultBlackWin
	}
	return game.ResultRedWin
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "puzzle-check:", err)
	os.Exit(1)
}
