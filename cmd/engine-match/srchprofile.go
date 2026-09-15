package main

// 搜索效率诊断：同一局面、同一固定深度下，逐层对比两个引擎的节点数。
//
// 目的：回答「为什么同样的节点数，皮卡鱼能搜得更深」——
// 也就是节点速度比（约 40%）与搜索深度比（约 22%）之间那个约 1.75 倍
// 的落差究竟来自哪里。这个落差是纯计算能力无法解释的，只能由剪枝效率解释。
//
// 核心指标是 EBF（有效分支因子）= nodes[d] / nodes[d-1]。
// 理想剪枝下 EBF 应接近 2-3；EBF 越高说明该层剪枝越失效。
// 两边 EBF 的差值就是「搜索效率差距」的直接量化。
//
// 对照口径：两边都从干净的置换表出发做完整迭代加深到目标深度。
//   内嵌：Searcher.Search(p, d) —— 内部先 Clear()，再迭代加深 1..d
//   皮卡鱼：先 ucinewgame 清表，再 go depth d
// 因此两边返回的 nodes 都是「本次搜索的累计节点数」，可直接相比。

import (
	"bufio"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/nnue"
	"github.com/IamAyang233/panda-xiangqi/internal/search"
)

// layerStat 是单个局面在某一深度下的搜索统计。
type layerStat struct {
	depth     int
	nodes     int64
	ebf       float64 // 相对上一层的节点增长倍数
	ttHits    int64   // 仅内嵌引擎有（UCI info 不暴露）
	nullMoves int64   // 仅内嵌引擎有
	best      string
}

// posProfile 是一个局面在两边各自的逐层统计。
type posProfile struct {
	name   string
	native []layerStat
	pika   []layerStat
}

// uciSession 是一个可复用的 UCI 会话，避免每个深度重启进程。
//
// 注意：不能给皮卡鱼传含中文的绝对路径（setoption EvalFile 会让引擎静默退出），
// 所以这里刻意不设置 cmd.Dir，让它沿用进程当前工作目录 —— 与既有 uciProbe 一致。
type uciSession struct {
	stdin io.WriteCloser
	rd    *bufio.Reader
	cmd   *exec.Cmd
}

func newUCISession(path string, skill int) (*uciSession, error) {
	cmd := exec.Command(path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	s := &uciSession{stdin: stdin, rd: bufio.NewReader(stdout), cmd: cmd}
	s.send("uci")
	s.send(fmt.Sprintf("setoption name Skill Level value %d", skill))
	s.send("isready")
	if err = s.waitFor("readyok", 20*time.Second); err != nil {
		cmd.Process.Kill()
		return nil, err
	}
	return s, nil
}

func (s *uciSession) send(line string) { fmt.Fprintln(s.stdin, line) }

func (s *uciSession) Close() {
	if s.cmd != nil && s.cmd.Process != nil {
		s.cmd.Process.Kill()
		_ = s.cmd.Wait()
	}
}

// waitFor 读到包含 prefix 的行为止，用于握手同步。
func (s *uciSession) waitFor(prefix string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	type res struct {
		err error
	}
	ch := make(chan res, 1)
	go func() {
		for {
			line, rerr := s.rd.ReadString('\n')
			if rerr != nil {
				ch <- res{fmt.Errorf("引擎输出流关闭：%v", rerr)}
				return
			}
			if strings.Contains(line, prefix) {
				ch <- res{nil}
				return
			}
		}
	}()
	select {
	case r := <-ch:
		return r.err
	case <-time.After(time.Until(deadline)):
		return fmt.Errorf("等待 %q 超时", prefix)
	}
}

// goDepth 让引擎在给定 FEN 上从干净状态迭代加深到 depth，返回累计节点数。
//
// ucinewgame 必须先发：它清空置换表与启发式，否则上一深度的残留会让
// 累计节点数失去可比性（这正是我们想比的「从零开始要到 depth 需要多少节点」）。
func (s *uciSession) goDepth(fen string, depth int, timeout time.Duration) (nodes int64, best string, err error) {
	s.send("ucinewgame")
	s.send("position fen " + fen)
	s.send(fmt.Sprintf("go depth %d", depth))

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		type lineRes struct {
			line string
			err  error
		}
		ch := make(chan lineRes, 1)
		go func() {
			l, rerr := s.rd.ReadString('\n')
			ch <- lineRes{l, rerr}
		}()

		var got lineRes
		select {
		case got = <-ch:
		case <-time.After(time.Until(deadline)):
			return nodes, best, fmt.Errorf("depth %d 超时", depth)
		}
		if got.err != nil {
			if got.err == io.EOF {
				return nodes, best, fmt.Errorf("引擎输出流关闭")
			}
			return nodes, best, got.err
		}

		line := strings.TrimSpace(got.line)
		if strings.HasPrefix(line, "info ") {
			// 只认 score depth 行：其它 info（ currline / string ）不带 depth 或带的是别的值。
			if strings.Contains(line, "score") {
				if n := fieldInt(line, "nodes"); int64(n) > nodes {
					nodes = int64(n)
				}
			}
			continue
		}
		if strings.HasPrefix(line, "bestmove") {
			if f := strings.Fields(line); len(f) >= 2 {
				best = f[1]
			}
			return nodes, best, nil
		}
	}
	return nodes, best, fmt.Errorf("depth %d 未收到 bestmove", depth)
}

// goNodes 让引擎在节点预算内做迭代加深，返回最终完成的深度与实际节点数。
//
// 对应 UCI 的 "go nodes N"。配额耗尽时引擎会立刻给出 bestmove，
// 最后一条 info depth 就是它完整跑完的最深一层。
func (s *uciSession) goNodes(fen string, budget int64, timeout time.Duration) (depth int, nodes int64, err error) {
	s.send("ucinewgame")
	s.send("position fen " + fen)
	s.send(fmt.Sprintf("go nodes %d", budget))

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		type lineRes struct {
			line string
			err  error
		}
		ch := make(chan lineRes, 1)
		go func() {
			l, rerr := s.rd.ReadString('\n')
			ch <- lineRes{l, rerr}
		}()

		var got lineRes
		select {
		case got = <-ch:
		case <-time.After(time.Until(deadline)):
			return depth, nodes, fmt.Errorf("nodes %d 超时", budget)
		}
		if got.err != nil {
			if got.err == io.EOF {
				return depth, nodes, fmt.Errorf("引擎输出流关闭")
			}
			return depth, nodes, got.err
		}

		line := strings.TrimSpace(got.line)
		if strings.HasPrefix(line, "info ") && strings.Contains(line, "score") {
			if d := fieldInt(line, "depth"); d > depth {
				depth = d
			}
			if n := fieldInt(line, "nodes"); int64(n) > nodes {
				nodes = int64(n)
			}
			continue
		}
		if strings.HasPrefix(line, "bestmove") {
			return depth, nodes, nil
		}
	}
	return depth, nodes, fmt.Errorf("nodes %d 未收到 bestmove", budget)
}

// runSearchProfile 逐层对比两个引擎的搜索统计。
func runSearchProfile(flatPath, uciPath string, maxDepth, skill int, fens []struct {
	name string
	fen  string
}) {
	w, err := nnue.Load(flatPath)
	if err != nil {
		fmt.Println("加载 NNUE 权重失败：", err)
		return
	}
	// 诊断只用单线程 Searcher：多线程 Pool 会因共享 TT 让节点数不确定，
	// 而我们要比的是算法本身的搜索树规模，必须是确定性结果。
	sr := search.New(w)

	fmt.Printf("=== 搜索效率诊断（逐层节点数对比）===\n")
	fmt.Printf("口径：两侧均从干净置换表迭代加深到目标深度，统计累计节点数\n")
	fmt.Printf("核心指标 EBF = nodes[d] / nodes[d-1]，越低说明剪枝越有效\n\n")

	var sumNRatio, sumEBFN, sumEBFP float64
	var cnt int

	for _, c := range fens {
		p, perr := game.ParseFEN(c.fen)
		if perr != nil {
			continue
		}

		var prof posProfile
		prof.name = c.name

		// 内嵌：每个深度都做一次干净的完整迭代加深。
		var prev int64
		for d := 1; d <= maxDepth; d++ {
			res := sr.Search(p, d)
			// 将杀局面会让搜索树坍缩，节点数失去代表性，跳过后续层。
			if isMateScore(res.Score) {
				break
			}
			var ebf float64
			if prev > 0 && res.Nodes > 0 {
				ebf = float64(res.Nodes) / float64(prev)
			}
			prof.native = append(prof.native, layerStat{
				depth:     d,
				nodes:     res.Nodes,
				ebf:       ebf,
				ttHits:    sr.TTHits(),
				nullMoves: sr.NullMoves(),
				best:      res.Best.String(),
			})
			prev = res.Nodes
		}

		// 皮卡鱼：复用同一个会话，每个深度前用 ucinewgame 清表。
		sess, serr := newUCISession(uciPath, skill)
		if serr != nil {
			fmt.Printf("%-20s 皮卡鱼会话建立失败：%v\n", c.name, serr)
			continue
		}
		prev = 0
		for d := 1; d <= maxDepth; d++ {
			nodes, best, gerr := sess.goDepth(c.fen, d, 120*time.Second)
			if gerr != nil {
				fmt.Printf("%-20s 皮卡鱼 depth %d 失败：%v\n", c.name, d, gerr)
				break
			}
			var ebf float64
			if prev > 0 && nodes > 0 {
				ebf = float64(nodes) / float64(prev)
			}
			prof.pika = append(prof.pika, layerStat{
				depth: d,
				nodes: nodes,
				ebf:   ebf,
				best:  best,
			})
			prev = nodes
		}
		sess.Close()

		printProfile(&prof, &sumNRatio, &sumEBFN, &sumEBFP, &cnt)
	}

	if cnt > 0 {
		fmt.Printf("\n=== 汇总（%d 个局面）===\n", cnt)
		fmt.Printf("节点数比值（均值）      内嵌/皮卡鱼 = %.2f\n", sumNRatio/float64(cnt))
		fmt.Printf("有效分支因子 EBF（均值）内嵌 %.2f    皮卡鱼 %.2f\n",
			sumEBFN/float64(cnt), sumEBFP/float64(cnt))
		fmt.Printf("\n判读：若 EBF 内嵌明显高于皮卡鱼，说明差距在剪枝/走法排序；\n")
		fmt.Printf("      若两者接近而节点速度低，则差距纯在计算，该做 SIMD。\n")
	}
}

// printProfile 打印单个局面的逐层对比。
func printProfile(prof *posProfile, sumNRatio, sumEBFN, sumEBFP *float64, cnt *int) {
	fmt.Printf("── %s ──\n", prof.name)
	fmt.Printf("%-6s %-14s %-8s %-14s %-8s %-10s\n",
		"depth", "内嵌节点", "EBF", "皮卡鱼节点", "EBF", "节点比")
	n, ok := 0, 0
	for i := range prof.native {
		if i >= len(prof.pika) {
			break
		}
		a, b := prof.native[i], prof.pika[i]
		ratio := float64(a.nodes) / float64(b.nodes) * 100
		fmt.Printf("d%-5d %-14d %-8.2f %-14d %-8.2f %-10.1f%%\n",
			a.depth, a.nodes, a.ebf, b.nodes, b.ebf, ratio)
		if a.ebf > 0 && b.ebf > 0 {
			*sumNRatio += float64(a.nodes) / float64(b.nodes)
			*sumEBFN += a.ebf
			*sumEBFP += b.ebf
			n++
		}
		ok++
	}
	// TT 命中率只内嵌有，作为自诊断信息输出。
	if len(prof.native) > 0 {
		last := prof.native[len(prof.native)-1]
		if last.nodes > 0 {
			fmt.Printf("内嵌 d%d：TT 命中 %d（%.1f%%）  空着 %d\n",
				last.depth, last.ttHits, float64(last.ttHits)/float64(last.nodes)*100, last.nullMoves)
		}
	}
	if ok > 0 {
		fmt.Println()
	}
	if n > 0 {
		*cnt++
	}
}

// isMateScore 判断分值是否已是将杀分。
func isMateScore(score int) bool {
	return score > search.MateScore-MaxProbePly || score < -search.MateScore+MaxProbePly
}
