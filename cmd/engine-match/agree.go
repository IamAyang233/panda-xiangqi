package main

// 着法级度量：与「超深仲裁」的一致率。
//
// 上一轮（`mgquality.go`）证明了**评分级**度量没有分辨力 —— 让仲裁者给「走完一步
// 之后的局面」打分，分辨不出差 3 层的两个着法（对照组建不起来）。本轮换层级：
// 不问「你的着法通向多好的局面」，而问「**你选的着法跟超深仲裁是不是同一个**」。
//
//	position P（S 行棋）
//	  ├ 仲裁者用 N× 预算取它的最佳着法 A（以及前 K 着 A₁..A_K）
//	  ├ 我们@B      → M_ours   ；命中 ⟺ M_ours ∈ {A₁..A_K}
//	  └ 皮卡鱼@B    → M_pik    ；命中 ⟺ M_pik  ∈ {A₁..A_K}
//
// ⚠️ 已知偏差方向：仲裁者是皮卡鱼，所以它对「皮卡鱼风格的着法」有偏好，皮卡鱼@B
// 的一致率天然偏高（它和仲裁者共享同一套评估与剪枝）。**这是需要记住的偏差，
// 不是可以忽略的噪声。** 读「我们 vs 皮卡鱼」的差时要把它算进去 —— 真实的差距
// 只可能比这个差值更大（我们是在客场）。
//
// ⚠️⚠️ **判别力自检（必须）**：对照组 = 同一引擎「预算 B vs 预算 B/8」。两者差约
// 3 层搜索，是**确定存在**的强弱差。若本度量连它都分辨不出，实验组的数字就不能读。
//
// ⚠️ 为什么要报「前 K 着命中」：中局常有多个**近似等价**的着法，只比「首选是否
// 相同」会低估。K=5 那一档给出的是「有没有落在仲裁的候选集里」，对等价着法更宽容。
// 两档一起看：top-1 差异大而 top-K 差异小 ⇒ 差在「同样好的着法里挑了不同的一个」，
// 而不是「挑错了」。
//
// ---------------------------------------------------------------------------
// ⚠️⚠️ **实测结论（2026-09-17）：这个度量只在「决定锋利」的语料上成立。**
//
// 第一版用 `testdata/live_game_fens.txt` 全部 300 条（40 条采样），**对照组反向**：
// 分歧子集内我们@100k 16.7% vs 我们@12.5k 33.3%（−16.7pp）。按纪律，实验组的
// 数字不可读。**但这一次根因被量了出来，不是"度量又坏了"**：
//
//	仲裁者的「决定有多锋利」（8M 节点，MultiPV=5）：
//	  首选与次选平均分差 44.4 分，但分布极偏
//	  次选已在 20 分内的局面            32/40 = 80%
//	  前 5 着全在 20 分内（谁选都一样）  16/40 = 40%
//	仲裁者自己的自洽性 8M → 32M 只有 **75%**
//
// 在 40% 的局面里，「有没有撞上仲裁首选」本来就是掷硬币，与引擎强弱无关。
// 把这些局面混进来，等于往信号里灌 40% 纯噪声 —— 而**参考值自身只有 75% 稳定度**
// 直接给度量设了个 75% 的天花板，谁也上不去。
//
// **验证性实验**：先用 `-mode sharpen` 把语料按「首选与次选的分差」降序重排，
// 再取最锋利的 60 个局面重跑（8M 仲裁）：
//
//	前 5 着全在 20 分内      40%  →  **2%**
//	首选与次选平均分差      44.4  →  **135.6 分**
//	仲裁者自洽性 8M→32M      75%  →  **100%（12/12）**   ← 天花板被解除
//	对照组（分歧子集）  −16.7pp  →  **+100pp（5:0）**    ← 分辨力恢复
//
// ⇒ **着法级度量可用，但前提是语料必须先筛到决定锋利**；这一步不是可选优化，
//   是让度量成立的必要条件（不筛必然得出反号结论）。
//
// 全语料（300 条，1M 节点扫描）的锋利度分布 —— 这个数字本身也很有信息量：
//
//	首选与次选分差 0~20 分（谁选都一样）  219 = **73.0%**
//	                 21~50 分              39 =  13.0%
//	                 51~100 分             16 =   5.3%
//	                101~200 分             13 =   4.3%
//	                 >200 分（极锋利）      13 =   4.3%
//
// **中局有四分之三的局面里「哪个是最佳着法」近乎退化。** 这同时解释了上一轮的
// 评分级度量为什么失败（局面评估值也近乎相同）—— 两个层级败于同一个事实。
//
// ⚠️ **但「取分差最大的前 N 个」是错的**：那些是**近杀局**（首选是强制杀）。
// 实测取前 150 个时平均分差 **1418.7 分**，两套引擎都命中 **96.7%**、彼此一致
// 94%、差异 **0.0pp** —— 样本饱和，什么也测不出，而且越依赖深度的局面越不利于
// 弱的一方（我们节点效率只有对手的 70%）。必须取**分差带**（`-mgapmin/-mgapmax`）。
//
// ---------------------------------------------------------------------------
// **正式测量（2026-09-17，分差带 50~300 分）**
//
// 语料：从 40 局快棋对局采集 1200 个中局局面 → 1M 扫描 → 取 gap1∈[50,300] 的
// 241 个，用前 200 个。两侧单线程、**实际节点归一化**（皮卡鱼按我们的实际节点给量，
// 比值 1.00×）、每题独立置换表、仲裁 MultiPV=5。
//
//	锋利度（4M）：前 5 着全在 20 分内 0/200；首选与次选平均分差 417.8 分
//	仲裁者自洽性 4M→16M：**100.0%（10/10）**            ← 天花板已解除
//
//	对照组 我们@100k vs 我们@12.5k（已知差约 2.4 层）：
//	  两档分歧 31/200；分歧子集内 top-1 **21/31 = 67.7% vs 5/31 = 16.1%**
//	  ⇒ **+51.6pp，McNemar 精确 p ≈ 0.0025** —— 度量确实有分辨力 ✓
//
//	实验组 我们@100k vs 皮卡鱼@同实际节点：
//	  整体 top-1 **92.5% vs 93.5%（−1.0pp）**；top-K 98.5% vs 98.0%
//	  分歧子集 20/200：top-1 7/20 vs 9/20（差 −10pp，McNemar p ≈ 0.80）
//	  ⇒ **同等实际节点下，中局走子质量与皮卡鱼统计上无差别。**
//
// 怎么读这个结论：
//   - 两个引擎都接近 92~93%，而 4× 深的仲裁者是 100% —— 剩下约 7% 才是可分辨区。
//     所以该结论的强度是「**差异大到与 2.4 层搜索相当就能测出，而我们没测出差异**」
//     （对照组已证明这个灵敏度），而不是「完全相同」。
//   - 与既有口径并不矛盾：`-mode curve` 量出的「节点效率 70%」用的是**残局题**，
//     那类局面依赖深度、我们吃亏；本度量用的是**中局有明确答案的局面**，不吃亏。
//     ⇒ **我们的差距集中在战术/深算，不在中局判断。** 这解释了实战「能守不能攻」：
//       守得住（判断不吃亏），攻不进（需要深算时不如人）。
//
// ---------------------------------------------------------------------------
// ⚠️⚠️ **2026-09-18 口径纠正：公平归一化从「等节点」改为「等走子量」。**
//
// 上面那次正式测量的 `fair` 是把皮卡鱼的预算设成**我们的实际节点数**
// （`oursRes.Nodes`）。核对源码后发现两边数的不是同一个量：
//
//	皮卡鱼 `++nodes`  记在 `do_move` 里（src/search.cpp:628）＝ **走子次数**
//	我们   `Nodes`     记的是 **结点进入次数**（一次进入里往往走好几步）
//
// 比值随深度变化（同一中局局面固定深度迭代加深）：
//
//	depth    我们结点     我们走子     结点:走子
//	d1           82          37         2.22
//	d6         8414       13462         0.63
//	d12      170483      393067         0.43
//
// ⇒ 旧口径下皮卡鱼**只拿到我们一半多一点的算力**，两边根本不是等工作量。
//   已加 `Searcher.Makes()`（走子次数）并在 `Result` 里带出，本模式的 `fair`
//   现在设为 `oursRes.Makes`。**重新在那套语料上跑之前，别引用上面 92.5% vs
//   93.5% 那个数字** —— 它是在错误的归一化下测的（方向：对手拿得少 ⇒ 我们被高估）。
//
// 同时记下同工具的第二个偏差：`goNodes*` 取对手最后一条 info，而皮卡鱼被节点
// 限制打断时 depth 是**未完成迭代**的层号，我们返回的是最后**完整跑完**的一层
// ⇒ 报 depth 时对手被系统性 +1 层。要看 depth 请用固定 depth（`-mode profile`）。
//
// ---------------------------------------------------------------------------
// 复现命令：
//
//	go build -o em.exe ./cmd/engine-match
//	# 1) 采集语料：go build 后跑对局导出局面（-mgdump），得到 1200 条
//	# 2) 筛选分差带：
//	./em.exe -mode sharpen -corpus ./big_fens.txt -mgl 1200 \
//	         -mgarbiter 1000000 -mtopk 5 -mgapmin 50 -mgapmax 300 -mout ./band_sel.txt \
//	         -uci dist/pikafish.exe -flat engines/pikafish.nnue.flat
//	# 3) 正式测量：
//	./em.exe -mode agree -corpus ./band_sel.txt -mgl 200 \
//	         -mgbudget 100000 -mgarbiter 4000000 -mtopk 5 -mcalib 10 \
//	         -uci dist/pikafish.exe -flat engines/pikafish.nnue.flat

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/nnue"
	"github.com/IamAyang233/panda-xiangqi/internal/search"
)

// pvLine 是仲裁者给出的一条候选线。
type pvLine struct {
	move  string
	score int
}

// goNodesTopPVLines 在给定节点预算下搜索，返回仲裁者的有序候选（含着法与分值）。
//
// 用 MultiPV 取多个候选：中局里"最佳"往往是若干近似等价的着法之一，只取首选会
// 把「同样好但选了另一个」也算成错。
func (s *uciSession) goNodesTopPVLines(fen string, budget int64, k int, timeout time.Duration) ([]pvLine, error) {
	s.send("ucinewgame")
	s.send("position fen " + fen)
	s.send(fmt.Sprintf("go nodes %d", budget))

	best := map[int]pvLine{} // multipv 序号 → 该线（取最后一次迭代，即最深的那次）
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
			return nil, fmt.Errorf("仲裁搜索 %d 节点超时", budget)
		}
		if got.err != nil {
			if got.err == io.EOF {
				return nil, fmt.Errorf("引擎输出流关闭")
			}
			return nil, got.err
		}
		line := strings.TrimSpace(got.line)
		if strings.HasPrefix(line, "info ") {
			idx := fieldInt(line, "multipv")
			if idx == 0 {
				idx = 1
			}
			mv := firstPVMove(line)
			sc, ok := parseUCIScore(line)
			if mv != "" && ok {
				best[idx] = pvLine{move: mv, score: sc}
			}
			continue
		}
		if strings.HasPrefix(line, "bestmove") {
			break
		}
	}

	out := make([]pvLine, 0, k)
	for i := 1; i <= k; i++ {
		if l, ok := best[i]; ok {
			out = append(out, l)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("仲裁搜索 %d 节点未取到 pv", budget)
	}
	return out, nil
}

// goNodesTopPV 只要着法，兼容旧调用点。
func (s *uciSession) goNodesTopPV(fen string, budget int64, k int, timeout time.Duration) ([]string, error) {
	lines, err := s.goNodesTopPVLines(fen, budget, k, timeout)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, l.move)
	}
	return out, nil
}

// firstPVMove 从 info 行里取 "pv" 后的第一个着法。
func firstPVMove(line string) string {
	f := strings.Fields(line)
	for i, t := range f {
		if t == "pv" && i+1 < len(f) {
			return f[i+1]
		}
	}
	return ""
}

// probeNNUE 让引擎自报它的权重加载情况。
//
// ⚠️ 时机很讲究：皮卡鱼（同 SF）在 **`Engine::go()` 里才调 `verify_network()`**
// （主源码 engine.cpp:130），`uci` 握手阶段并不会打印这行 —— 所以「发一条 uci
// 去核对」和「等 handshake 捞」都是抓不到的，必须**真跑一次搜索**。
//
// ⚠️ 失败形态也和我们原先以为的不同：`verify_network()` 发现网络没载入时会
// **直接 `exit(EXIT_FAILURE)` 并打 `ERROR: ... was not loaded successfully`**，
// 不会静默地拿零网络去评估。所以这条的价值是「确认它真的活着、真的用了权重」，
// 而不是防「悄悄变弱」。
func probeNNUE(sess *uciSession) string {
	sess.send("position startpos")
	sess.send("go depth 1")
	deadline := time.Now().Add(20 * time.Second)
	found := ""
	for time.Now().Before(deadline) {
		type lineRes struct {
			line string
			err  error
		}
		ch := make(chan lineRes, 1)
		go func() {
			l, rerr := sess.rd.ReadString('\n')
			ch <- lineRes{l, rerr}
		}()
		var got lineRes
		select {
		case got = <-ch:
		case <-time.After(time.Until(deadline)):
			return "⚠️ 首次搜索超时 —— 数字不可信"
		}
		if got.err != nil {
			return fmt.Sprintf("⚠️ 首次搜索时引擎退出（%v）—— 很可能权重载入失败", got.err)
		}
		line := strings.TrimSpace(got.line)
		if found == "" && strings.Contains(line, "NNUE evaluation") {
			found = line
		}
		// ⚠️ 必须读到 bestmove 才停：提前 return 会把该次搜索剩下的行留在管道里，
		// 下一次查询就会立刻读到**残留的 bestmove**，之后每一条都返回同一着法。
		if strings.HasPrefix(line, "bestmove") {
			if found != "" {
				return found
			}
			return "⚠️ 首次搜索未打印 NNUE 加载行 —— 数字不可信"
		}
	}
	return "⚠️ 未读到 NNUE 加载行 —— 数字不可信"
}

// inSet 判断着法是否落在候选集里。
func inSet(mv string, set []string) bool {
	for _, s := range set {
		if s == mv {
			return true
		}
	}
	return false
}

// agreeStat 累计一组的命中情况。
type agreeStat struct {
	hit1, hitK, n int
}

func (a *agreeStat) add(mv string, arb []string, k int) {
	a.n++
	if len(arb) > 0 && mv == arb[0] {
		a.hit1++
	}
	if inSet(mv, arb[:minInt(k, len(arb))]) {
		a.hitK++
	}
}

func (a agreeStat) rate(x int) string {
	if a.n == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%5.1f%%", 100*float64(x)/float64(a.n))
}

func (a agreeStat) report(label, tag string) {
	fmt.Printf("  %-30s top-1 %s ｜ top-K %s   (n=%d)\n", label, a.rate(a.hit1), a.rate(a.hitK), a.n)
	if tag != "" {
		fmt.Printf("      %s\n", tag)
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// sharpStat 度量「仲裁者的决定有多锋利」。
//
// 这是整个着法级度量的**前置条件**：若前 K 个候选着法几乎同分，那「谁撞上了
// 仲裁者的首选」等于掷硬币 —— 一致率差异反映的是噪声而不是棋力。
// ⚠️ 阈值 20 分是约定的「近似等价」线（本项目评估里 20 分≈0.2 兵的观感差异）。
type sharpStat struct {
	n                int
	gap1Sum, gapKSum int
	flat1, flatK     int
}

func (s sharpStat) report(topK int, budget int64) {
	if s.n == 0 {
		return
	}
	fmt.Printf("\n--- 仲裁决定的锋利度（%d 个局面，%d 节点，MultiPV=%d）---\n", s.n, budget, topK)
	fmt.Printf("  首选与次选的平均分差 %.1f 分；与末选的平均分差 %.1f 分\n",
		float64(s.gap1Sum)/float64(s.n), float64(s.gapKSum)/float64(s.n))
	fmt.Printf("  次选已在 20 分内的局面 %d/%d = %.0f%%\n",
		s.flat1, s.n, 100*float64(s.flat1)/float64(s.n))
	fmt.Printf("  **前 %d 着全在 20 分内**（谁选都一样）的局面 %d/%d = %.0f%%\n",
		topK, s.flatK, s.n, 100*float64(s.flatK)/float64(s.n))
	fmt.Printf("  ⇒ 后一档越高，「选中仲裁首选」越接近掷硬币，着法级一致率的可比性越差。\n")
}

// pairDiff 统计「同一引擎两档预算」的分歧子集。
//
// ⚠️ 必须单独拆出来：若两档在大多数局面上选的**根本是同一着**，那么整体的
// 一致率差就被稀释成噪声。对照组真正的信息只在「两档选了不同着法」的那些
// 局面上 —— 那里才有「谁更常撞上仲裁者的答案」可比。
type pairDiff struct {
	same, n int
	// 分歧子集内各自的命中数
	aHit1, aHitK, bHit1, bHitK int
}

func (p *pairDiff) add(a, b string, arb []string, k int) {
	p.n++
	if a == b {
		p.same++
		return
	}
	kk := minInt(k, len(arb))
	if len(arb) > 0 && a == arb[0] {
		p.aHit1++
	}
	if len(arb) > 0 && b == arb[0] {
		p.bHit1++
	}
	if inSet(a, arb[:kk]) {
		p.aHitK++
	}
	if inSet(b, arb[:kk]) {
		p.bHitK++
	}
}

func (p pairDiff) reportDiff(labelA, labelB string) {
	d := p.n - p.same
	fmt.Printf("  两档分歧 %d/%d 个局面（%.0f%%）；**只有这 %d 个有信息**\n",
		d, p.n, 100*float64(d)/float64(maxInt(p.n, 1)), d)
	if d == 0 {
		fmt.Printf("    → 分歧为 0，对照组完全无信息\n")
		return
	}
	fmt.Printf("    分歧子集内 top-1：%s %d/%d = %.1f%% ｜ %s %d/%d = %.1f%%  （差 %+.1fpp）\n",
		labelA, p.aHit1, d, 100*float64(p.aHit1)/float64(d),
		labelB, p.bHit1, d, 100*float64(p.bHit1)/float64(d),
		100*float64(p.aHit1-p.bHit1)/float64(d))
	fmt.Printf("    分歧子集内 top-K：%s %d/%d = %.1f%% ｜ %s %d/%d = %.1f%%  （差 %+.1fpp）\n",
		labelA, p.aHitK, d, 100*float64(p.aHitK)/float64(d),
		labelB, p.bHitK, d, 100*float64(p.bHitK)/float64(d),
		100*float64(p.aHitK-p.bHitK)/float64(d))
}

// moveAgreement 跑着法级一致率对比（含判别力对照组与仲裁者自洽性校准）。
func moveAgreement(flatPath, uciPath, corpus string, limit int, compareBudget, arbiterBudget int64, topK int, fair bool, calibN int) {
	w, err := nnue.Load(flatPath)
	if err != nil {
		fmt.Println("加载 NNUE 权重失败：", err)
		return
	}
	fens, err := readFENFile(corpus)
	if err != nil {
		fmt.Printf("读取语料 %s 失败：%v\n", corpus, err)
		return
	}
	if limit > 0 && len(fens) > limit {
		fens = fens[:limit]
	}
	if len(fens) == 0 {
		fmt.Println("语料为空")
		return
	}

	sess, err := newUCISession(uciPath, 20)
	if err != nil {
		fmt.Println("皮卡鱼会话建立失败：", err)
		return
	}
	defer sess.Close()
	sess.send(fmt.Sprintf("setoption name MultiPV value %d", topK))
	sess.send("isready")
	if err := sess.waitFor("readyok", 20*time.Second); err != nil {
		fmt.Println("MultiPV 设置失败：", err)
		return
	}
	fmt.Printf("仲裁者自报：%s\n\n", probeNNUE(sess))

	fmt.Printf("=== 着法级一致率（与超深仲裁比）===\n")
	if fair {
		fmt.Printf("语料 %s｜%d 个局面｜我们 %d **结点**｜仲裁 %d 结点（%.0f×，MultiPV=%d）\n",
			corpus, len(fens), compareBudget, arbiterBudget,
			float64(arbiterBudget)/float64(compareBudget), topK)
		fmt.Printf("⚠️ 皮卡鱼按**我们的实际走子数**给量（同口径）—— 它报的 nodes 就是走子次数，\n")
		fmt.Printf("   而我们的预算是结点数，两者比值随深度变化（中局 ≈1:2.3）。\n\n")
	} else {
		fmt.Printf("语料 %s｜%d 个局面｜对比预算 %d 结点｜仲裁预算 %d 结点（%.0f×，MultiPV=%d）\n",
			corpus, len(fens), compareBudget, arbiterBudget,
			float64(arbiterBudget)/float64(compareBudget), topK)
		fmt.Printf("⚠️ 未做归一化：我们超支（计数停在结点边界），这个模式只作对照。\n\n")
	}

	var ours, pik, weak agreeStat
	var ctrlPair, expPair pairDiff
	var sharp sharpStat
	var usedFens, arbTop1 []string
	sameAsPik, skipped := 0, 0
	var ourNodes, ourMakes, pikNodes int64
	for i, fen := range fens {
		pos, perr := game.ParseFEN(fen)
		if perr != nil {
			skipped++
			continue
		}
		oursRes := search.New(w).SearchNodes(pos.Clone(), compareBudget)
		oursMV := oursRes.Best.String()
		weakMV := search.New(w).SearchNodes(pos.Clone(), compareBudget/8).Best.String()

		// ⚠️⚠️ 公平归一化必须用**走子数（Makes）**，不能用节点数（Nodes）。
		//
		// 皮卡鱼的 `go nodes N` 数的是 `do_move` 的调用次数（src/search.cpp:628），
		// 即**走子次数**；我们的 `Nodes` 是**结点进入次数**（一次进入里往往走好几步）。
		// 两者比值随深度变化（中局固定深度实测 d12 ≈ 1:2.3），所以拿 Nodes 当预算
		// 等于给皮卡鱼**一倍多**的算力 —— 这正是 2026-09-18 之前所有「等节点」
		// 跨引擎结论失真的根因（详见 `-mode nodes` 的对照表）。
		//
		// `Makes` 与皮卡鱼同口径，所以「我们做 M 次走子 ⇒ 皮卡鱼也做 M 次」才是
		// 真正的等工作量。
		pikBudget := compareBudget // -公平归一化时的名义值（皮卡鱼按它自己的节点数算）
		if fair {
			pikBudget = oursRes.Makes
		}
		pikMV, _, pikUsed, uerr := sess.goNodesBest(fen, pikBudget, 300*time.Second)
		if uerr != nil {
			skipped++
			continue
		}
		arbLines, aerr := sess.goNodesTopPVLines(fen, arbiterBudget, topK, 600*time.Second)
		if aerr != nil {
			skipped++
			continue
		}
		arb := make([]string, 0, len(arbLines))
		for _, l := range arbLines {
			arb = append(arb, l.move)
		}
		// 记录仲裁候选的分值跨度：这是「局面有多锋利」的度量。
		// 若前 K 着几乎同分，则「有没有选中同一个」本身就是掷硬币，任何着法级
		// 度量都会被这种局面稀释成噪声 —— 必须先把这个比例量出来再读一致率。
		if len(arbLines) >= 2 {
			gap1 := absInt(arbLines[0].score - arbLines[1].score)
			gapK := absInt(arbLines[0].score - arbLines[len(arbLines)-1].score)
			sharp.gap1Sum += gap1
			sharp.gapKSum += gapK
			sharp.n++
			if gapK <= 20 {
				sharp.flatK++
			}
			if gap1 <= 20 {
				sharp.flat1++
			}
		}
		ours.add(oursMV, arb, topK)
		pik.add(pikMV, arb, topK)
		weak.add(weakMV, arb, topK)
		// 对照组的分歧子集：我们@B vs 我们@B/8（同一引擎，真实强弱差已知）
		ctrlPair.add(oursMV, weakMV, arb, topK)
		// 实验组的分歧子集：我们@B vs 皮卡鱼@同实际节点
		expPair.add(oursMV, pikMV, arb, topK)
		usedFens = append(usedFens, fen)
		arbTop1 = append(arbTop1, arb[0])
		ourNodes += oursRes.Nodes
		ourMakes += oursRes.Makes
		pikNodes += pikUsed
		if oursMV == pikMV {
			sameAsPik++
		}
		if (i+1)%5 == 0 {
			fmt.Printf("  ...已跑 %d/%d\n", i+1, len(fens))
		}
	}

	fmt.Printf("\n有效局面 %d（跳过 %d）｜我们与皮卡鱼着法相同 %d/%d = %.0f%%\n",
		len(fens)-skipped, skipped, sameAsPik, ours.n,
		100*float64(sameAsPik)/float64(maxInt(ours.n, 1)))
	fmt.Printf("\n工作量口径：%s\n", map[bool]string{
		true:  "**已归一化 —— 皮卡鱼的预算 = 我们的实际走子数（同口径）**",
		false: "按名义预算（我们超支，占皮卡鱼便宜）",
	}[fair])
	if ourMakes > 0 {
		fmt.Printf("  我们：结点 %d ｜ **走子 %d**（结点:走子 = %.2f）\n",
			ourNodes, ourMakes, float64(ourNodes)/float64(ourMakes))
	}
	if pikNodes > 0 {
		fmt.Printf("  皮卡鱼：走子 %d（它的 `go nodes N` 数的就是走子次数）\n", pikNodes)
	}
	if ourMakes > 0 && pikNodes > 0 {
		fmt.Printf("  ⇒ 走子口径之比 = **%.2f×**（越接近 1.00 越接近真等工作量）\n",
			float64(ourMakes)/float64(pikNodes))
	}

	ours.report("实验组 我们@"+fmt.Sprint(compareBudget), "")
	pik.report("实验组 皮卡鱼@"+fmt.Sprint(compareBudget), "（⚠️ 它与仲裁者同源，一致率天然偏高）")
	weak.report("对照组 我们@"+fmt.Sprint(compareBudget/8), "")
	sharp.report(topK, arbiterBudget)

	fmt.Printf("\n--- 判别力自检（对照组必须成立，否则实验组数字不可读）---\n")
	ctrlPair.reportDiff("我们@"+fmt.Sprint(compareBudget), "我们@"+fmt.Sprint(compareBudget/8))
	if ours.n == 0 || weak.n == 0 {
		fmt.Printf("  样本不足，无法判断\n")
		return
	}
	d1 := 100 * float64(ctrlPair.aHit1-ctrlPair.bHit1) / float64(maxInt(ctrlPair.n-ctrlPair.same, 1))
	dK := 100 * float64(ctrlPair.aHitK-ctrlPair.bHitK) / float64(maxInt(ctrlPair.n-ctrlPair.same, 1))
	fmt.Printf("  ⇒ 分歧子集内 top-1 %+.1fpp，top-K %+.1fpp（**应当为正且明显**）\n", d1, dK)

	fmt.Printf("\n--- 实验组的分歧子集（同样只看两档不同着的局面）---\n")
	expPair.reportDiff("我们@"+fmt.Sprint(compareBudget), "皮卡鱼@同走子量")

	dP1 := 100 * float64(ours.hit1-pik.hit1) / float64(ours.n)
	dPK := 100 * float64(ours.hitK-pik.hitK) / float64(ours.n)
	fmt.Printf("\n  整体一致率之差（我们@B − 皮卡鱼@B）：top-1 %+.1fpp，top-K %+.1fpp\n", dP1, dPK)
	fmt.Printf("  注意方向：皮卡鱼与仲裁者同源，上行的负值**高估**了我们的落后幅度；\n")
	fmt.Printf("  真实差距只可能更大（我们在客场）。反向的结论不能下。\n")

	// 仲裁者自洽性：同一个仲裁者，预算 ×4 之后还选不选同一着？
	//
	// 这是**参考值本身的天花板**：若 8M 与 32M 只有 80% 一致，那么任何引擎
	// 与 8M 的一致率都**不可能超过约 80%** —— 超过部分测的是噪声，不是棋力。
	if calibN > 0 && len(usedFens) > 0 {
		n := minInt(calibN, len(usedFens))
		agree, done := 0, 0
		fmt.Printf("\n--- 仲裁者自洽性校准（%d 个局面，%d → %d 节点）---\n",
			n, arbiterBudget, arbiterBudget*4)
		for i := 0; i < n; i++ {
			deep, err := sess.goNodesTopPV(usedFens[i], arbiterBudget*4, 1, 1200*time.Second)
			if err != nil {
				continue
			}
			done++
			if deep[0] == arbTop1[i] {
				agree++
			}
		}
		if done > 0 {
			fmt.Printf("  同着法 %d/%d = %.1f%% ⇒ 这是本度量的**分辨力天花板**\n",
				agree, done, 100*float64(agree)/float64(done))
			fmt.Printf("  与它比：我们 %.1f%%、皮卡鱼 %.1f%%（都相对 %d 节点仲裁）\n",
				100*float64(ours.hit1)/float64(maxInt(ours.n, 1)),
				100*float64(pik.hit1)/float64(maxInt(pik.n, 1)), arbiterBudget)
		}
	}
}
