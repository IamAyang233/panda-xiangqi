// puzzle-check 残局批量校验工具（计划书 A16）：
// 引擎自对弈（红=全力档, 黑=全力档）验证"红先胜"成立，并把主变写回 JSON。
//
// 用法：go run ./cmd/puzzle-check -in puzzles.json [-out puzzles.json] [-level 13]
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
	out := flag.String("out", "", "输出文件（默认写回输入文件）")
	level := flag.Int("level", 13, "验证引擎档位（自研引擎深度档）")
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
		if p.Goal != "win" {
			fmt.Printf("%-8s %-12s 跳过（goal=%s，和棋排局需人工复核）\n", p.ID, p.Name, p.Goal)
			continue
		}
		line, ok, err := verifyWin(eng, p, *level)
		if err != nil {
			fmt.Printf("%-8s %-12s 错误: %v\n", p.ID, p.Name, err)
			continue
		}
		if !ok {
			p.Verified = false
			fmt.Printf("%-8s %-12s ✗ 未能按预期验证（请人工检查）\n", p.ID, p.Name)
			continue
		}
		p.Solution = line
		p.ParMoves = (len(line) + 1) / 2
		p.Verified = true
		okCount++
		fmt.Printf("%-8s %-12s ✓ 红先胜 %d 步: %v\n", p.ID, p.Name, p.ParMoves, line)
	}

	if *out == "" {
		*out = *in
	}
	blob, err := json.MarshalIndent(puzzles, "", "  ")
	if err != nil {
		fatal(err)
	}
	if err := os.WriteFile(*out, append(blob, '\n'), 0o644); err != nil {
		fatal(err)
	}
	fmt.Printf("\n完成：%d 关验证通过，已写入 %s\n", okCount, *out)
}

// verifyWin 引擎自对弈：红黑双方同档全力，红在 maxRedMoves 手内将死即验证通过。
func verifyWin(eng *engine.SimpleEngine, p *puzzle.Puzzle, level int) ([]string, bool, error) {
	pos, err := game.ParseFEN(p.FEN)
	if err != nil {
		return nil, false, fmt.Errorf("FEN 非法: %w", err)
	}
	maxRed := p.ParMoves
	if maxRed <= 0 {
		maxRed = 6
	}
	// 宽限 2 步：主变可能比预期长一点，仍算可解
	maxRed += 2

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
		lv := level
		if pos.Turn == game.Black {
			lv = level // 守方同档全力抵抗
		}
		mv, err := eng.BestMove(ctx, pos, lv)
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

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "puzzle-check:", err)
	os.Exit(1)
}
