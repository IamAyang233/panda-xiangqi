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
	fmt.Printf("%8s %14s %14s %7s %7s\n",
		"名义预算", "内嵌 命中/均深", "皮卡鱼 命中/均深", "命中差", "节点比")
	fmt.Printf("  皮卡鱼按**我们的实际节点数**给量（逐题），两侧工作量完全相同\n")
	fmt.Printf("  「节点比」应为 1.00×，用来自检归一化真的生效了\n\n")

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
			// ⚠️ 关键：皮卡鱼用**我们这一步的实际节点数**，不是同一个名义预算
			// —— 我们的 SearchNodes 停在结点边界上，实际会超支 1.3~1.7×。
			best, d, un, uerr := sess.goNodesBest(pz.FEN, res.Nodes, 180*time.Second)
			if uerr != nil {
				continue
			}
			ourHit += boolInt(res.Best.String() == pz.Solution[0])
			ourDep += res.Depth
			ourNodes += res.Nodes
			uciHit += boolInt(best == pz.Solution[0])
			uciDep += d
			uciNodes += un
			ok++
		}
		if ok == 0 {
			continue
		}
		pts = append(pts, point{b, ourHit, uciHit, ourDep / ok, uciDep / ok, ourNodes, uciNodes})
		fmt.Printf("%8d %14s %14s %6dpp %6.2fx\n", b,
			fmt.Sprintf("%d/%d d%d", ourHit, ok, ourDep/ok),
			fmt.Sprintf("%d/%d d%d", uciHit, ok, uciDep/ok),
			100*(ourHit-uciHit)/ok,
			float64(ourNodes)/float64(uciNodes))
	}

	// 节点效率：同实际节点下的命中数之比。
	//
	// ⚠️⚠️ 这一节曾经算错，是值得单独记住的教训（2026-09-17 修正）：
	// 原实现算出「归一命中 = 命中数 ÷ 节点比」，再除以皮卡鱼的命中数，得到
	// 「我们每节点只值 **70%**」。**那个归一化是无效的。** 命中数是**有上限**的
	// 量（≤ 题量）且随预算**饱和**：除以节点比等于按线性外推去惩罚多搜的节点，
	// 惩罚远超真实影响。同一批数据用正确口径重算，两侧基本相同。
	//
	// 正确做法就是上面那个循环：**把皮卡鱼的预算设成我们的实际节点数**，然后
	// 直接比命中数。这是唯一能在「有上限、会饱和」的指标上做的公平对比。
	fmt.Printf("\n--- 节点效率（同实际节点）---\n")
	var sumEff float64
	for _, p := range pts {
		eff := float64(p.ourHit) / float64(p.uciHit)
		sumEff += eff
		fmt.Printf("%8d 名义（我们实际 %d 节点/题）：我们 %d vs 皮卡鱼 %d ⇒ %.0f%%\n",
			p.budget, p.ourNodes/int64(len(ps)), p.ourHit, p.uciHit, 100*eff)
	}
	avg := sumEff / float64(len(pts))
	fmt.Printf("\n平均：同实际节点下我们的命中数是皮卡鱼的 **%.0f%%**。\n", 100*avg)
	fmt.Printf("⚠️ 「命中数之比」在接近满分时会饱和，绝对差只有几题，百分比波动大。\n")
	fmt.Printf("   要下「谁更强」的结论请用 `-mode bylen`（按杀着长度分段 + 绝对差）。\n")
	fmt.Printf("（命中率粒度 1/%d 题 ≈ %.1f 个百分点）\n", len(ps), 100.0/float64(len(ps)))
	fmt.Printf("对照：8 线程的节点速度是皮卡鱼单线程的约 65%%（即需要 1.54× 时间）。\n")
}
