package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/nnue"
	"github.com/IamAyang233/panda-xiangqi/internal/search"
)

// throughput 量「单线程吞吐比」—— 两边的「走子/秒」之比。
//
// 为什么必须有这个模式：跨引擎比深度/节点都需要先把工作量对齐，而**对齐到同一个
// 走子量之后，两边各花了多少时间**才是「效率差距」的干净度量。旧记的「1.17×」
// 是在错误的计数器上算的（见 makes 的两次口径修正），必须重测。
//
// 设计要点：
//   - **等走子量**：先让内嵌引擎在固定结点预算下跑出它做了多少走子，再让对手
//     `go nodes <同一个数>` —— 皮卡鱼的 `nodes` 就是走子次数，与 `Searcher.Makes()`
//     同口径。于是两边做的是同一份工作量，比时间才有意义。
//   - **同轮交替 A/B**：跨轮的绝对秒数不可比（机器有快慢相位、被占用时会差 3 倍），
//     所以顺序每轮反转，只取同一轮内的比值。
//   - **单线程 + 每题 ucinewgame/Clear**：多线程与跨题的 TT 残留都会让数字不可复现。
//   - 对手的时间取**它自己 info 行里的 time 字段**（与同一行的 nodes 自源，不含
//     进程通信开销），另打一份墙钟做交叉核对。
func throughput(flatPath, uciPath string, budget int64, rounds int, fens []struct {
	name string
	fen  string
}) {
	w, err := nnue.Load(flatPath)
	if err != nil {
		fmt.Println("加载 NNUE 权重失败：", err)
		return
	}
	sr := search.New(w)

	sess, err := newUCISession(uciPath, 20) // 20 = 满强度，避免 Skill 削弱改变搜索量
	if err != nil {
		fmt.Println("皮卡鱼会话建立失败：", err)
		return
	}
	defer sess.Close()
	sess.send("setoption name Threads value 1")
	sess.send("setoption name Hash value 64")
	sess.send("isready")
	if err := sess.waitFor("readyok", 20*time.Second); err != nil {
		fmt.Println("皮卡鱼选项设置失败：", err)
		return
	}

	type job struct {
		name         string
		fen          string
		nodes, makes int64
	}
	var jobs []job
	fmt.Printf("=== 第一步：取我们的工作量（单线程，固定 %d 结点，每题清表）===\n", budget)
	for _, c := range fens {
		p, perr := game.ParseFEN(c.fen)
		if perr != nil {
			fmt.Printf("  跳过（FEN 非法）%s: %v\n", c.name, perr)
			continue
		}
		sr.Clear()
		res := sr.SearchNodes(p, budget)
		makes := sr.Makes()
		jobs = append(jobs, job{c.name, c.fen, res.Nodes, makes})
		fmt.Printf("  %-24s 结点 %-9d 走子 %-9d（走子/结点 %.2f）\n",
			c.name, res.Nodes, makes, float64(makes)/float64(res.Nodes))
	}
	if len(jobs) == 0 {
		return
	}
	fmt.Printf("\n=== 第二步：同轮交替 A/B（%d 轮，顺序反转）===\n", rounds)
	fmt.Printf("A = 我们，固定 %d 结点；B = 皮卡鱼，go nodes <我们的走子数>\n\n", budget)

	ourW := make([][]int64, len(jobs))
	ourD := make([][]time.Duration, len(jobs))
	itsW := make([][]int64, len(jobs))
	itsD := make([][]time.Duration, len(jobs))

	for r := 1; r <= rounds; r++ {
		order := []string{"A", "B"}
		if r%2 == 0 {
			order = []string{"B", "A"}
		}
		for _, who := range order {
			for i := range jobs {
				if who == "A" {
					p, perr := game.ParseFEN(jobs[i].fen)
					if perr != nil {
						continue
					}
					sr.Clear()
					t0 := time.Now()
					sr.SearchNodes(p, budget)
					el := time.Since(t0)
					ourW[i] = append(ourW[i], sr.Makes())
					ourD[i] = append(ourD[i], el)
					continue
				}
				_, n, eng, wall, gerr := sess.goNodesTimed(jobs[i].fen, jobs[i].makes, 600*time.Second)
				if gerr != nil {
					fmt.Printf("  第 %d 轮 对手失败（%s）：%v\n", r, jobs[i].name, gerr)
					continue
				}
				if eng <= 0 {
					eng = wall
				}
				itsW[i] = append(itsW[i], n)
				itsD[i] = append(itsD[i], eng)
			}
		}
	}

	fmt.Printf("%-24s %14s %14s %10s\n", "局面", "我们 走子/秒", "皮卡鱼 走子/秒", "比值")
	var tOurWork, tOurNs, tItsWork, tItsNs int64
	var ratios []float64
	for i := range jobs {
		ow := medianI64(ourW[i])
		od := medianDur(ourD[i])
		iw := medianI64(itsW[i])
		id := medianDur(itsD[i])
		if od <= 0 || id <= 0 {
			continue
		}
		ourRate := float64(ow) / od.Seconds()
		itsRate := float64(iw) / id.Seconds()
		rt := ourRate / itsRate
		ratios = append(ratios, rt)
		tOurWork += ow
		tOurNs += od.Nanoseconds()
		tItsWork += iw
		tItsNs += id.Nanoseconds()
		fmt.Printf("%-24s %14.0f %14.0f %9.3f×\n", jobs[i].name, ourRate, itsRate, rt)
	}
	if len(ratios) == 0 {
		fmt.Println("没有有效样本。")
		return
	}
	fmt.Printf("\n合计：我们 %d 走子 / %.2fs = %.0f 走子/秒\n",
		tOurWork, float64(tOurNs)/1e9, float64(tOurWork)/(float64(tOurNs)/1e9))
	fmt.Printf("      对手 %d 走子 / %.2fs = %.0f 走子/秒\n",
		tItsWork, float64(tItsNs)/1e9, float64(tItsWork)/(float64(tItsNs)/1e9))
	fmt.Printf("      ⇒ 吞吐比 **%.3f×**（逐局面中位 %.3f×）\n",
		float64(tOurWork)/float64(tItsWork)*float64(tItsNs)/float64(tOurNs), medianF(ratios))
	fmt.Println("\n⚠️ 比值 = 我们/它，<1 表示我们每秒做的走子更少。")
	fmt.Println("⚠️ 只看同一轮内交替得到的比值；绝对秒数跨轮/跨次不可比。")
}

// medianI64 / medianDur / medianF 取中位。耗时分布有长尾（一次被抢占就能翻倍），
// 所以耗时取中位而不是均值。
func medianI64(v []int64) int64 {
	if len(v) == 0 {
		return 0
	}
	c := append([]int64(nil), v...)
	sort.Slice(c, func(i, j int) bool { return c[i] < c[j] })
	return c[len(c)/2]
}

func medianDur(v []time.Duration) time.Duration {
	if len(v) == 0 {
		return 0
	}
	c := append([]time.Duration(nil), v...)
	sort.Slice(c, func(i, j int) bool { return c[i] < c[j] })
	return c[len(c)/2]
}

func medianF(v []float64) float64 {
	c := append([]float64(nil), v...)
	sort.Float64s(c)
	return c[len(c)/2]
}

// goNodesTimed 是 goNodes 的计时版本：返回对手自报的搜索耗时与墙钟耗时。
//
// 自报耗时取「与 nodes 同一行 info」的 time 字段 —— 两者同源，比值才自洽；
// 墙钟则用来发现管道/唤醒带来的额外开销。
func (s *uciSession) goNodesTimed(fen string, budget int64, timeout time.Duration) (depth int, nodes int64, eng, wall time.Duration, err error) {
	s.send("ucinewgame")
	s.send("position fen " + fen)
	t0 := time.Now()
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
			return depth, nodes, eng, time.Since(t0), fmt.Errorf("nodes %d 超时", budget)
		}
		if got.err != nil {
			return depth, nodes, eng, time.Since(t0), fmt.Errorf("引擎输出流关闭：%v", got.err)
		}

		line := got.line
		if strings.HasPrefix(line, "bestmove") {
			return depth, nodes, eng, time.Since(t0), nil
		}
		if !strings.HasPrefix(line, "info ") || !strings.Contains(line, " score ") {
			continue
		}
		if d := fieldInt(line, "depth"); d > depth {
			depth = d
		}
		if n := int64(fieldInt(line, "nodes")); n > nodes {
			nodes = n
			if ms := fieldInt(line, "time"); ms > 0 {
				eng = time.Duration(ms) * time.Millisecond
			}
		}
	}
	return depth, nodes, eng, time.Since(t0), fmt.Errorf("nodes %d 未收到 bestmove", budget)
}
