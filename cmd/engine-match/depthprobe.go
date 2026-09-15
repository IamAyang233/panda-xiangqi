package main

// 深度探针：同一局面、同一思考时间下，分别问两个引擎「你搜到了多深、看了多少节点」。
// 皮卡鱼走 UCI info 行自报 depth/nodes；内嵌引擎由 Pool 的 Result 直接给出。
// 这是「棋力差距有多大」最直观的量化口径。

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/IamAyang233/panda-xiangqi/internal/engine"
	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/search"
)

// uciProbe 直接跑一个 UCI 会话，抓 info 行里的 depth 与 nodes。
func uciProbe(path, fen string, mt time.Duration, skill int) (depth, nodes int, best string, err error) {
	cmd := exec.Command(path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return
	}
	if err = cmd.Start(); err != nil {
		return
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()

	send := func(s string) { fmt.Fprintln(stdin, s) }
	send("uci")
	send(fmt.Sprintf("setoption name Skill Level value %d", skill))
	send("isready")
	send("ucinewgame")
	send("position fen " + fen)
	send(fmt.Sprintf("go movetime %d", mt.Milliseconds()))

	rd := bufio.NewReader(stdout)
	deadline := time.Now().Add(mt + 8*time.Second)
	for time.Now().Before(deadline) {
		line, rerr := rd.ReadString('\n')
		if rerr != nil {
			if rerr == io.EOF {
				return depth, nodes, best, fmt.Errorf("引擎输出流关闭")
			}
			return depth, nodes, best, rerr
		}
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "info ") {
			for _, tok := range strings.Fields(line) {
				_ = tok
			}
			if d := fieldInt(line, "depth"); d > depth {
				depth = d
			}
			if n := fieldInt(line, "nodes"); n > nodes {
				nodes = n
			}
		}
		if strings.HasPrefix(line, "bestmove") {
			f := strings.Fields(line)
			if len(f) >= 2 {
				best = f[1]
			}
			return depth, nodes, best, nil
		}
	}
	return depth, nodes, best, fmt.Errorf("超时未收到 bestmove")
}

func fieldInt(line, key string) int {
	f := strings.Fields(line)
	for i, t := range f {
		if t == key && i+1 < len(f) {
			n := 0
			for _, c := range f[i+1] {
				if c < '0' || c > '9' {
					return n
				}
				n = n*10 + int(c-'0')
			}
			return n
		}
	}
	return 0
}

func runDepthProbe(native *engine.NativeEngine, uciPath string, mt time.Duration, skill int) {
	fmt.Printf("=== 同题同时间：搜索深度对比（每步 %v）===\n", mt)
	fmt.Printf("%-24s %-28s %-28s\n", "局面", "内嵌引擎 (depth/nodes)", "皮卡鱼 (depth/nodes)")

	var nd, nn, ud, un int
	n := 0
	for _, c := range matchFENs {
		p, perr := game.ParseFEN(c.fen)
		if perr != nil {
			continue
		}
		res, rerr := native.SearchTimed(context.Background(), p, mt)
		if rerr != nil {
			fmt.Printf("%-24s 内嵌引擎出错: %v\n", c.name, rerr)
			continue
		}
		d, nodes, _, uerr := uciProbe(uciPath, c.fen, mt, skill)
		if uerr != nil {
			fmt.Printf("%-24s 皮卡鱼出错: %v\n", c.name, uerr)
			continue
		}
		// 找到将杀时自报的 depth 没有意义（引擎直接把深度推到上限），
		// 这类局面不参与平均，否则会把均值拉得毫无参考价值。
		mate := res.Score > search.MateScore-MaxProbePly || res.Score < -search.MateScore+MaxProbePly
		if mate {
			fmt.Printf("%-24s %-28s %-28s  (已见将杀，不参与均值)\n", c.name,
				fmt.Sprintf("d%d %d", res.Depth, res.Nodes), fmt.Sprintf("d%d %d", d, nodes))
			continue
		}
		n++
		nd += res.Depth
		nn += int(res.Nodes)
		ud += d
		un += nodes
		fmt.Printf("%-24s d%-3d %-22d d%-3d %-22d\n", c.name, res.Depth, res.Nodes, d, nodes)
	}
	if n > 0 {
		fmt.Printf("\n%d 个可比较局面，平均：内嵌 depth %.1f / %d 节点   皮卡鱼 depth %.1f / %d 节点\n",
			n, float64(nd)/float64(n), nn/n, float64(ud)/float64(n), un/n)
		fmt.Printf("深度比 %.1f%%   节点速度比 %.2f%%\n",
			float64(nd)/float64(ud)*100, float64(nn)/float64(un)*100)
	}
}

// MaxProbePly 用于判断结果里是否已含将杀分（与 search.MaxPly 同量级）。
const MaxProbePly = 128
