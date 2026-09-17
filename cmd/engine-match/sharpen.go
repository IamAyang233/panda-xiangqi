package main

// 语料筛选：把「仲裁者的决定是锋利的」局面挑出来。
//
// 起因（见 agree.go 顶部的实测结论）：着法级一致率在**全部**中局局面上的对照组
// 失败了，而且原因被量了出来 ——
//
//	首选与次选的平均分差 44.4 分，但**分布极偏**：
//	  次选已在 20 分内的局面        32/40 = 80%
//	  前 5 着全在 20 分内（谁选都一样）16/40 = 40%
//	仲裁者自己的自洽性 8M→32M 只有 75%
//
// 也就是说：在 40% 的局面里，「有没有选中仲裁者的首选」**本来就是个掷硬币**，
// 与引擎强弱无关。把这些局面混进去做统计，等于往信号里灌 40% 的纯噪声。
//
// 本模式的假设：**把语料筛到「次选明显更差」的局面后，对照组应当恢复分辨力。**
// 这是一个可以直接证伪的实验 —— 若筛过之后对照组仍然不成立，那么着法级度量
// 就整条作废，只剩结果级（续弈）可用。
//
// ⚠️⚠️ **只按 gap1 降序取前 N 个会掉进另一个坑（实测踩到）**：分差最大的那些
// 局面其实是**近杀局**（首选是强制杀，gap1 直接到杀分，实测看到 29974 分）。
// 那类局面测的是「找不找得到杀」= 纯战术，而且对我们**不利**（我们节点效率
// 只有皮卡鱼的 70%，越依赖深度的局面越吃亏）。用它代表「中局质量」是错的。
//
// 所以真正该取的是**一个分差带**：够锋利（选择有意义）但没到强制杀。
// 用 `-mgapmin` / `-mgapmax` 控制，默认 50~300 分。
//
// 用法：先扫描并按锋利度排序写出新语料，再用 `-mode agree` 跑它。
//
//	go build -o em.exe ./cmd/engine-match
//	./em.exe -mode sharpen -corpus internal/search/testdata/live_game_fens.txt \
//	         -mgl 300 -mgarbiter 1000000 -mtopk 5 -mgapmin 50 -mgapmax 300 \
//	         -mout ./sharp_fens.txt \
//	         -uci dist/pikafish.exe -flat engines/pikafish.nnue.flat
//	./em.exe -mode agree -corpus ./sharp_fens.txt -mgl 60 \
//	         -mgbudget 100000 -mgarbiter 8000000 -mtopk 5 -mcalib 12 \
//	         -uci dist/pikafish.exe -flat engines/pikafish.nnue.flat

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
)

type sharpRow struct {
	fen  string
	gap1 int // 首选与次选的分差
	gapK int // 首选与末选的分差
}

// sharpenCorpus 扫描语料，按「决定锋利度」降序写出新语料。
func sharpenCorpus(uciPath, corpus string, limit int, scanBudget int64, topK int, outPath string, gapMin, gapMax int) {
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

	fmt.Printf("=== 语料锋利度扫描 ===\n")
	fmt.Printf("语料 %s｜扫描 %d 个局面｜扫描预算 %d 节点｜MultiPV=%d\n",
		corpus, len(fens), scanBudget, topK)
	fmt.Printf("分差带 %d~%d 分（下界=选择有意义，上界=排除近杀局）\n\n", gapMin, gapMax)

	var rows []sharpRow
	skipped := 0
	for i, fen := range fens {
		if _, perr := game.ParseFEN(fen); perr != nil {
			skipped++
			continue
		}
		lines, aerr := sess.goNodesTopPVLines(fen, scanBudget, topK, 300*time.Second)
		if aerr != nil || len(lines) < 2 {
			skipped++
			continue
		}
		rows = append(rows, sharpRow{
			fen:  fen,
			gap1: absInt(lines[0].score - lines[1].score),
			gapK: absInt(lines[0].score - lines[len(lines)-1].score),
		})
		if (i+1)%50 == 0 {
			fmt.Printf("  ...已扫 %d/%d\n", i+1, len(fens))
		}
	}

	if len(rows) == 0 {
		fmt.Println("没有有效局面")
		return
	}
	sort.SliceStable(rows, func(a, b int) bool { return rows[a].gap1 > rows[b].gap1 })

	// 锋利度分布
	buckets := []struct {
		lo, hi int
		label  string
	}{
		{0, 20, "0~20 分（谁选都一样）"},
		{21, 50, "21~50 分"},
		{51, 100, "51~100 分"},
		{101, 200, "101~200 分"},
		{201, 1 << 30, ">200 分（决定极锋利）"},
	}
	counts := make([]int, len(buckets))
	for _, r := range rows {
		for i, b := range buckets {
			if r.gap1 >= b.lo && r.gap1 <= b.hi {
				counts[i]++
				break
			}
		}
	}
	fmt.Printf("\n有效 %d（跳过 %d）｜首选与次选分差的分布：\n", len(rows), skipped)
	for i, b := range buckets {
		fmt.Printf("  %-26s %3d  %5.1f%%\n", b.label, counts[i], 100*float64(counts[i])/float64(len(rows)))
	}

	// 选出分差带内的局面（仍按锋利度降序，便于用 -mgl 截取）
	var sel []sharpRow
	for _, r := range rows {
		if r.gap1 >= gapMin && r.gap1 <= gapMax {
			sel = append(sel, r)
		}
	}
	if len(sel) == 0 {
		fmt.Printf("\n分差带 %d~%d 内没有局面\n", gapMin, gapMax)
		return
	}
	var sb strings.Builder
	for _, r := range sel {
		sb.WriteString(r.fen)
		sb.WriteString("\n")
	}
	if err := os.WriteFile(outPath, []byte(sb.String()), 0o644); err != nil {
		fmt.Printf("写出 %s 失败：%v\n", outPath, err)
		return
	}
	fmt.Printf("\n分差带 %d~%d 内有 %d 个局面（占全部 %d 的 %.0f%%），已按锋利度降序写到 %s\n",
		gapMin, gapMax, len(sel), len(rows), 100*float64(len(sel))/float64(len(rows)), outPath)
	fmt.Printf("  这 %d 个的分差范围：%d ~ %d 分\n", len(sel), sel[len(sel)-1].gap1, sel[0].gap1)
	fmt.Printf("  建议用 `-mode agree -corpus %s -mgl %d` 跑它。\n", outPath, minInt(len(sel), 150))
}
