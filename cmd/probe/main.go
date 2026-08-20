// probe 用皮卡鱼对单个 FEN 求主变 PV（调试用）。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/IamAyang233/panda-xiangqi/internal/engine"
	"github.com/IamAyang233/panda-xiangqi/internal/game"
)

func main() {
	mt := flag.Int("mt", 6000, "movetime ms")
	engPath := flag.String("engine", "dist-engines/pikafish-avx2.exe", "引擎路径")
	flag.Parse()
	if flag.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "用法: probe [-mt 6000] <FEN>")
		os.Exit(2)
	}
	fen := flag.Arg(0)
	eng, err := engine.NewUCIEngine(*engPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "引擎启动失败:", err)
		os.Exit(1)
	}
	defer eng.Close()
	pos, err := game.ParseFEN(fen)
	if err != nil {
		fmt.Fprintln(os.Stderr, "FEN 非法:", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*mt*4+8000)*time.Millisecond)
	defer cancel()
	pv, cp, mate, err := eng.BestLine(ctx, pos, *mt)
	if err != nil {
		fmt.Println("err:", err)
	}
	strs := make([]string, len(pv))
	for i, m := range pv {
		strs[i] = m.String()
	}
	fmt.Printf("cp=%d mate=%d plies=%d\n", cp, mate, len(pv))
	fmt.Printf("turn=%v humanPV=%v\n", pos.Turn, strings.Join(strs, " "))
}
