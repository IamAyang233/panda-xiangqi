// reclassify：仅重算 canju_*.json 的 difficulty 字段（按 source 映射），
// 不改正解/par/verified，避免重新跑皮卡鱼求解。用于补齐「大师」档等难度优化。
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/IamAyang233/panda-xiangqi/internal/puzzle"
)

// classifyDifficulty 与 cmd/import-canju 保持一致（单一来源见该处）。
func classifyDifficulty(source string) string {
	s := source
	switch {
	case strings.Contains(s, "初级") || strings.Contains(s, "第1阶段") || strings.Contains(s, "（初）") || strings.Contains(s, "（一）"):
		return "入门"
	case strings.Contains(s, "中级") || strings.Contains(s, "第2阶段") || strings.Contains(s, "（中）") || strings.Contains(s, "（二）"):
		return "初级"
	case strings.Contains(s, "高级") || strings.Contains(s, "第3阶段") || strings.Contains(s, "（高）"):
		return "中级"
	case strings.Contains(s, "连将杀一至四"):
		return "中级"
	case strings.Contains(s, "连将杀五至七"):
		return "大师"
	case strings.Contains(s, "象棋杀着大全"):
		return "高级"
	case strings.Contains(s, "陈松顺"):
		return "高级"
	case strings.Contains(s, "屠景明"):
		return "中级"
	case strings.Contains(s, "竹子涨棋"):
		return "初级"
	}
	return "中级"
}

func main() {
	dir := "internal/puzzle/data"
	files, _ := filepath.Glob(filepath.Join(dir, "canju_*.json"))
	total, changed := 0, 0
	perDiff := map[string]int{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			panic(err)
		}
		var list []*puzzle.Puzzle
		if err := json.Unmarshal(data, &list); err != nil {
			panic(fmt.Errorf("%s: %w", f, err))
		}
		for _, p := range list {
			total++
			nd := classifyDifficulty(p.Source)
			if nd != p.Difficulty {
				p.Difficulty = nd
				changed++
			}
			perDiff[p.Difficulty]++
		}
		out, err := json.MarshalIndent(list, "", "  ")
		if err != nil {
			panic(err)
		}
		if err := os.WriteFile(f, append(out, '\n'), 0o644); err != nil {
			panic(err)
		}
		fmt.Printf("%-40s %d 题\n", filepath.Base(f), len(list))
	}
	fmt.Printf("\n总题数=%d  难度变化=%d\n", total, changed)
	for _, k := range []string{"入门", "初级", "中级", "高级", "大师"} {
		fmt.Printf("  %s: %d\n", k, perDiff[k])
	}
}
