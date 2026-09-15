package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/nnue"
	"github.com/IamAyang233/panda-xiangqi/internal/search"
)

// parseInts 解析逗号分隔的整数列表，非法项忽略；全部非法时用 def。
func parseInts(s string, def []int) []int {
	var out []int
	for _, t := range strings.Split(s, ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(t)); err == nil && n > 0 {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return def
	}
	return out
}

// runSelfProbe 只测内嵌引擎自己的搜索效率，用于调 LMR / 剪枝参数。
//
// 主指标是**固定深度下的节点总数**（树的大小），而不是「固定节点预算能到第几层」：
// 后者每局面的深度是整数，五个局面平均后粒度 0.2 层，树缩小三成也只动 0.3~0.5 层，
// 会被取整吃掉；节点总数是连续量，对同样的改动灵敏得多。
//
// 单线程、每个局面开搜前清空置换表与启发式表，因此完全可复现。
func runSelfProbe(flatPath string, depths []int, fens []struct {
	name string
	fen  string
}) {
	w, err := nnue.Load(flatPath)
	if err != nil {
		fmt.Println("加载 NNUE 权重失败：", err)
		return
	}
	// 多线程会因共享 TT 与抢占让节点数不可复现，调参必须单线程。
	s := search.New(w)

	fmt.Printf("=== 内嵌引擎自身：固定深度 → 节点数（单线程、可复现） ===\n\n")
	for _, d := range depths {
		var total int64
		n := 0
		fmt.Printf("── 深度 %d ──\n", d)
		for _, c := range fens {
			p, perr := game.ParseFEN(c.fen)
			if perr != nil {
				continue
			}
			s.Clear()
			res := s.Search(p, d)
			fmt.Printf("%-22s 节点 %-10d 最优 %s\n", c.name, res.Nodes, res.Best)
			total += res.Nodes
			n++
		}
		if n > 0 {
			fmt.Printf("合计 %d 节点（%d 个局面，均值 %d）\n\n", total, n, total/int64(n))
		}
	}
}
