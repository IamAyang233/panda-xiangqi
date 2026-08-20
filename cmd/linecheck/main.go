// 临时工具：按记录的 UCI 着法序列走子，校验每一步合法性并打印终局判定。
// 用于验证 goal=draw（长将和）等 matesolve 无法证明的和棋残局。
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Println("usage: linecheck <fen> <solution.json>")
		os.Exit(2)
	}
	fen := os.Args[1]
	data, err := os.ReadFile(os.Args[2])
	if err != nil {
		fmt.Println("read solution:", err)
		os.Exit(2)
	}
	var line []string
	if err := json.Unmarshal(data, &line); err != nil {
		fmt.Println("parse solution:", err)
		os.Exit(2)
	}

	pos, err := game.ParseFEN(fen)
	if err != nil {
		fmt.Println("parse fen:", err)
		os.Exit(2)
	}
	fmt.Printf("FEN: %s\n", fen)
	fmt.Printf("root: turn=%s inCheck(red)=%v inCheck(black)=%v\n",
		sideName(pos.Turn), pos.InCheck(game.Red), pos.InCheck(game.Black))

	for i, uci := range line {
		m, ok := game.MoveFromUCI(uci)
		if !ok {
			fmt.Printf("  [%d] %s -> 非法 UCI\n", i, uci)
			os.Exit(1)
		}
		if !pos.IsLegal(m) {
			fmt.Printf("  [%d] %s -> 非法着法 (turn=%s)\n", i, uci, sideName(pos.Turn))
			if i == 1 {
				for _, t := range []string{"e9d8", "e9f8", "e9e8"} {
					tm, _ := game.MoveFromUCI(t)
					pos.Make(tm)
					fmt.Printf("    try %s: blackInCheck=%v\n", t, pos.InCheck(game.Black))
					pos.Unmake()
				}
			}
			lm := pos.LegalMoves(pos.Turn)
			fmt.Printf("    legal moves (%d): ", len(lm))
			for _, mv := range lm {
				fmt.Printf("%s ", mv.String())
			}
			fmt.Println()
			os.Exit(1)
		}
		pos.Make(m)
		st := pos.CheckStatus()
		fmt.Printf("  [%d] %s  -> %s  result=%q reason=%q rep=%d half=%d\n",
			i, uci, sideName(pos.Turn), st.Result, st.Reason, pos.RepetitionCount(), pos.Halfmove)
		if st.Result != "" {
			fmt.Printf("终局: result=%s reason=%s\n", st.Result, st.Reason)
			return
		}
	}
	fmt.Println("序列走完，未终局")
}

func sideName(c int) string {
	if c == game.Black {
		return "black"
	}
	return "red"
}
