package main

// 效率曲线：同样的节点预算下，两套引擎各能答对多少残局题。
//
// 为什么需要这条曲线：节点速度差本身不是棋力结论 —— 真正要问的是
// 「我们的节点能不能换成同样的棋力」。把两个引擎的「命中率 vs 节点」画在
// 同一张表上，就能读出**换算倍率**：
//
//	换算倍率 ≈ 1  ⇒ 我们的节点和皮卡鱼一样值钱，差距只在速度
//	换算倍率 ≫ 1  ⇒ 我们的节点被浪费了，问题在树形状
//
// 口径必须完全对称：两侧都是**单线程 + 每题独立置换表 + 固定节点**。
// 皮卡鱼侧用 `ucinewgame` 清表（SF 系在这里清置换表），我们侧每题 New 一个
// Searcher。绝不能用固定时间 —— 那会把实现速度混进来，正是要分离的东西。
//
// ⚠️ 「固定节点」两侧并不等值：我们的 SearchNodes 停在结点边界上、会**超支**，
// 所以必须把两侧的**实际节点数**都记下来做归一化，否则会误判成我们「白赚」。
// 这是本工具第一次跑就踩到的坑。

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/nnue"
	"github.com/IamAyang233/panda-xiangqi/internal/search"
)

// goNodesBest 与 goNodes 相同，但额外取回 bestmove 与实际节点数。
// 命中率对比必须用 bestmove，公平对比必须用实际节点数。
func (s *uciSession) goNodesBest(fen string, budget int64, timeout time.Duration) (string, int, int64, error) {
	s.send("ucinewgame")
	s.send("position fen " + fen)
	s.send(fmt.Sprintf("go nodes %d", budget))

	deadline := time.Now().Add(timeout)
	depth := 0
	var nodes int64
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
			return "", depth, nodes, fmt.Errorf("nodes %d 超时", budget)
		}
		if got.err != nil {
			if got.err == io.EOF {
				return "", depth, nodes, fmt.Errorf("引擎输出流关闭")
			}
			return "", depth, nodes, got.err
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
			f := strings.Fields(line)
			if len(f) >= 2 {
				return f[1], depth, nodes, nil
			}
			return "", depth, nodes, fmt.Errorf("bestmove 行格式异常：%s", line)
		}
	}
	return "", depth, nodes, fmt.Errorf("nodes %d 未收到 bestmove", budget)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// effCurve 跑「节点预算 → 残局命中率」曲线，两侧口径完全对称。
func effCurve(flatPath, uciPath string, skill int, dir string, limit int, budgets []int64) {
	w, err := nnue.Load(flatPath)
	if err != nil {
		fmt.Println("加载 NNUE 权重失败：", err)
		return
	}
	ps := loadPuzzles(dir, limit)
	if len(ps) == 0 {
		fmt.Println("没找到可用题目")
		return
	}
	sess, err := newUCISession(uciPath, skill)
	if err != nil {
		fmt.Println("皮卡鱼会话建立失败：", err)
		return
	}
	defer sess.Close()

	fmt.Printf("=== 节点预算 → 残局命中率（两侧同为单线程 + 每题独立置换表 + 固定节点）===\n")
	fmt.Printf("题目 %d 道（win 局、取正解首着）｜皮卡鱼 Skill %d\n\n", len(ps), skill)
	fmt.Printf("%8s %14s %14s %7s %7s %9s %9s\n",
		"节点预算", "内嵌 命中/均深", "皮卡鱼 命中/均深", "命中差", "节点比", "归一命中", "皮卡鱼原值")
	fmt.Printf("  「节点比」= 内嵌实际节点 ÷ 皮卡鱼实际节点（我们的固定节点会超支）\n")
	fmt.Printf("  「归一命中」= 命中数 ÷ 节点比，把我们多搜的节点折算掉之后的命中数\n\n")

	type point struct {
		budget             int64
		ourHit, uciHit     int
		ourDep, uciDep     int
		ourNodes, uciNodes int64
	}
	var pts []point

	for _, b := range budgets {
		var ourHit, uciHit, ourDep, uciDep, ok int
		var ourNodes, uciNodes int64
		for _, pz := range ps {
			pos, perr := game.ParseFEN(pz.FEN)
			if perr != nil {
				continue
			}
			res := search.New(w).SearchNodes(pos.Clone(), b)
			ourHit += boolInt(res.Best.String() == pz.Solution[0])
			ourDep += res.Depth
			ourNodes += res.Nodes

			best, d, un, uerr := sess.goNodesBest(pz.FEN, b, 180*time.Second)
			if uerr != nil {
				continue
			}
			uciHit += boolInt(best == pz.Solution[0])
			uciDep += d
			uciNodes += un
			ok++
		}
		if ok == 0 {
			continue
		}
		pts = append(pts, point{b, ourHit, uciHit, ourDep / ok, uciDep / ok, ourNodes, uciNodes})
		ratio := float64(ourNodes) / float64(uciNodes)
		fmt.Printf("%8d %14s %14s %6dpp %6.2fx %9.1f %9d\n", b,
			fmt.Sprintf("%d/%d d%d", ourHit, ok, ourDep/ok),
			fmt.Sprintf("%d/%d d%d", uciHit, ok, uciDep/ok),
			100*(ourHit-uciHit)/ok, ratio,
			float64(ourHit)/ratio, uciHit)
	}

	// 节点效率：同样多的实际节点下，我们的命中率是皮卡鱼的百分之多少。
	// 换算倍率 = 1/效率比，即「要拿到同样的棋力，我们得搜多少倍节点」。
	//
	// 用「归一命中」而不是原始命中：我们固定节点会超支（1.3~1.7×），
	// 直接比原始命中会得出「我们在每个档位都赢」的假象 —— 第一次跑就踩了。
	fmt.Printf("\n--- 节点效率（同样多的**实际**节点下）---\n")
	var sumEff float64
	for i, p := range pts {
		ratio := float64(p.ourNodes) / float64(p.uciNodes)
		norm := float64(p.ourHit) / ratio
		eff := norm / float64(p.uciHit)
		sumEff += eff
		fmt.Printf("%8d 节点：我们归一命中 %.1f vs 皮卡鱼 %d ⇒ 效率 %.0f%%，换算倍率 %.2f×（节点比 %.2f×）\n",
			p.budget, norm, p.uciHit, 100*eff, 1/eff, ratio)
		_ = i
	}
	avg := sumEff / float64(len(pts))
	fmt.Printf("\n平均：我们每节点约值皮卡鱼的 **%.0f%%**，即需要 **%.2f×** 的节点才能达到同样的残局命中率。\n", 100*avg, 1/avg)
	fmt.Printf("对照：8 线程的节点速度是皮卡鱼单线程的约 65%%（即需要 1.54× 时间）。\n")
	fmt.Printf("      两者相乘 ≈ 我们同时间的有效算力占 %.0f%%。\n", 100*avg*0.65)
	fmt.Printf("（命中率粒度 1/%d 题 ≈ %.1f 个百分点，故单档误差约 ±%.0f%%）\n",
		len(ps), 100.0/float64(len(ps)), 100.0/float64(len(ps))/60*100)
}
