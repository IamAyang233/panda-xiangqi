package main

// 按「杀着长度」分段的命中率对比：把「节点效率 70%」这个总数拆开，看差额
// 集中在短杀还是长杀 —— 这是「战术弱项到底弱在哪」的第一刀。
//
// 为什么这么切：`-mode curve` 只给总分（我们每节点值皮卡鱼的 70%）。但战术题
// 的长度分布很宽（1 步杀到 12 步以上），不同长度对搜索的要求完全不同：
//   - 短杀靠静态搜索与着法排序就能找到；
//   - 长杀必须真的把一条强制线搜到深处，吃 EBF 与延伸机制。
// 如果差额集中在长杀，方向就是「深算/延伸」；如果各段均匀，方向就是「每节点
// 普遍更贵」。两种结论指向完全不同的优化，所以必须先切开再谈。
//
// ⚠️ 两侧**实际节点归一化**（我们的固定节点会超支 1.3~1.7×，见 2026-09-17 日志）。
//
//	go build -o em.exe ./cmd/engine-match
//	./em.exe -mode bylen -mgbudget 100000 -bylenlimit 400 \
//	         -uci dist/pikafish.exe -flat engines/pikafish.nnue.flat -data internal/puzzle/data

import (
	"fmt"
	"time"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/nnue"
	"github.com/IamAyang233/panda-xiangqi/internal/search"
)

type lenBucket struct {
	label     string
	used      int
	ourHit    int
	pikHit    int
	ourDepSum int
	pikDepSum int
	ourNodes  int64
	pikNodes  int64
}

// byLength 按杀着长度分段比较两套引擎的命中率。
func byLength(flatPath, uciPath string, skill int, dir string, limit int, budget int64) {
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
	fmt.Printf("仲裁者（对手）自报：%s\n\n", probeNNUE(sess))

	fmt.Printf("=== 命中率按杀着长度分段（同实际节点）===\n")
	fmt.Printf("题目 %d 道｜我们名义预算 %d 节点（皮卡鱼按我们的**实际**节点给量）\n\n",
		len(ps), budget)

	// 桶：1~4 合并、5~8、9~12、≥13。按 Solution 的 plies 数分组。
	type key struct{ lo, hi int }
	keys := []key{{1, 4}, {5, 8}, {9, 12}, {13, 1 << 30}}
	label := map[key]string{
		{1, 4}: "1~4 步", {5, 8}: "5~8 步", {9, 12}: "9~12 步", {13, 1 << 30}: "≥13 步",
	}
	buckets := map[key]*lenBucket{}
	for _, k := range keys {
		buckets[k] = &lenBucket{label: label[k]}
	}

	skipped := 0
	for _, pz := range ps {
		pos, perr := game.ParseFEN(pz.FEN)
		if perr != nil {
			skipped++
			continue
		}
		wantRed := pz.PlayerSide != "black"
		if (pos.Turn == game.Red) != wantRed {
			skipped++
			continue
		}
		var k key
		k = keys[len(keys)-1]
		for _, c := range keys {
			if len(pz.Solution) >= c.lo && len(pz.Solution) <= c.hi {
				k = c
				break
			}
		}

		res := search.New(w).SearchNodes(pos.Clone(), budget)
		pik, pd, pn, uerr := sess.goNodesBest(pz.FEN, res.Nodes, 300*time.Second)
		if uerr != nil {
			skipped++
			continue
		}
		b := buckets[k]
		b.used++
		b.ourNodes += res.Nodes
		b.pikNodes += pn
		b.ourDepSum += res.Depth
		b.pikDepSum += pd
		if res.Best.String() == pz.Solution[0] {
			b.ourHit++
		}
		if pik == pz.Solution[0] {
			b.pikHit++
		}
	}

	fmt.Printf("%-10s %5s %14s %14s %8s %12s %12s\n",
		"杀着长度", "题数", "我们 命中/深度", "皮卡鱼 命中/深度", "命中差", "我们节点/题", "它节点/题")
	var tu, th, tp int
	for _, c := range keys {
		b := buckets[c]
		if b.used == 0 {
			continue
		}
		tu += b.used
		th += b.ourHit
		tp += b.pikHit
		fmt.Printf("%-10s %5d %14s %14s %7dpp %12d %12d\n",
			b.label, b.used,
			fmt.Sprintf("%3d/%3d d%d", b.ourHit, b.used, b.ourDepSum/b.used),
			fmt.Sprintf("%3d/%3d d%d", b.pikHit, b.used, b.pikDepSum/b.used),
			100*(b.ourHit-b.pikHit)/b.used,
			b.ourNodes/int64(b.used), b.pikNodes/int64(b.used))
	}
	if tu > 0 {
		fmt.Printf("%-10s %5d  合计 我们 %d/%d = %.1f%% ｜ 皮卡鱼 %d/%d = %.1f%%（差 %+.1fpp）\n",
			"总计", tu, th, tu, 100*float64(th)/float64(tu),
			tp, tu, 100*float64(tp)/float64(tu),
			100*float64(th-tp)/float64(tu))
	}
	if skipped > 0 {
		fmt.Printf("（跳过 %d 道）\n", skipped)
	}
	fmt.Printf("\n读法：差额若集中在「长杀」，方向是深算/延伸；若各段均匀，方向是\n")
	fmt.Printf("「每节点普遍更贵」，与着法排序、剪枝余量一类更相关。\n")
}
