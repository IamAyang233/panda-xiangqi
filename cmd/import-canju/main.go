// import-canju 把 canju12 的内置题库（FEN txt）转换为 panda-xiangqi 的残局 JSON。
//
// 设计要点（适配现有系统、避免棋子放错）：
//   - 棋子的位置零变换：canju12 的 FEN 原样保留；仅 2# 前缀置 playerSide=black（引擎先走），
//     不做任何 180° 旋转，从根本上杜绝"棋子放错位置"。
//   - 合法性校验：game.ParseFEN 基础校验 + PositionValidator（象不过河、将士在九宫、将帅不对脸）。
//   - 难度分类：映射到现有 5 级（入门/初级/中级/高级/大师），按来源谱目粗分。
//   - 正解生成（-solve）：用皮卡鱼（Windows 本地引擎）求主变 PV，推导 goal/正解/parMoves，保留星级模型。
//
// 用法：
//   go run ./cmd/import-canju -intxt D:/.../canju12_tmp/app/src/main/assets -out internal/puzzle/data_canju
//   go run ./cmd/import-canju -intxt ... -out ... -solve -engine dist-engines/pikafish-avx2.exe -workers 10
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/IamAyang233/panda-xiangqi/internal/engine"
	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/puzzle"
)

// ---- 题库解析 ----

type record struct {
	fen     string
	flipped bool
}

var parenRe = regexp.MustCompile(`\(\d+\)`)
var bom = "\ufeff"

// loadTikuFile 解析一个 canju12 题库 txt：支持单行 FEN、2# 前缀、跨两行的棋盘包裹、
// 注释（# / //）与行尾 (N) 标记；并剥离 UTF-8 BOM。
func loadTikuFile(path string) ([]record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var recs []record
	var buf string
	var bufFlipped bool

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		line = strings.TrimPrefix(line, bom)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		flipped := false
		if strings.HasPrefix(line, "2#") {
			flipped = true
			line = line[2:]
		}
		line = parenRe.ReplaceAllString(line, "")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.Contains(line, " w ") || strings.Contains(line, " b ") {
			full := buf + line
			buf = ""
			recs = append(recs, record{fen: full, flipped: bufFlipped || flipped})
			bufFlipped = false
		} else {
			if buf != "" && !strings.HasSuffix(buf, "/") {
				buf += "/"
			}
			buf += line
			bufFlipped = bufFlipped || flipped
		}
	}
	return recs, sc.Err()
}

// ---- 合法性校验（防止棋子放错） ----

var redElephant = map[[2]int]bool{
	{2, 0}: true, {6, 0}: true, {0, 2}: true, {4, 2}: true, {8, 2}: true, {2, 4}: true, {6, 4}: true,
}
var redAdvisor = map[[2]int]bool{
	{3, 0}: true, {5, 0}: true, {4, 1}: true, {3, 2}: true, {5, 2}: true,
}

func elephantPoint(file, rank, col int) bool {
	if col == game.Red {
		return redElephant[[2]int{file, rank}]
	}
	return redElephant[[2]int{file, 9 - rank}]
}

func advisorPoint(file, rank, col int) bool {
	if col == game.Red {
		return redAdvisor[[2]int{file, rank}]
	}
	return redAdvisor[[2]int{file, 9 - rank}]
}

// validatePlacement 检查静态摆位合法性（不检查轮走方是否被将军，因为"轮走方被将"在残局题里可能合法）。
func validatePlacement(pos *game.Position) error {
	for sq90 := 0; sq90 < 90; sq90++ {
		pc := pos.PieceAt90(sq90)
		if pc == game.Empty {
			continue
		}
		typ := game.TypeOf(pc)
		col := game.ColorOf(pc)
		file := sq90 % 9
		rank := sq90 / 9
		switch typ {
		case game.King:
			if file < 3 || file > 5 {
				return fmt.Errorf("将/帅不在九宫列(file=%d)", file)
			}
			if col == game.Red && rank > 2 {
				return fmt.Errorf("红帅出宫(rank=%d)", rank)
			}
			if col == game.Black && rank < 7 {
				return fmt.Errorf("黑将出宫(rank=%d)", rank)
			}
		case game.Advisor:
			if !advisorPoint(file, rank, col) {
				return fmt.Errorf("士不在九宫点(file=%d,rank=%d)", file, rank)
			}
		case game.Elephant:
			if col == game.Red && rank > 4 {
				return fmt.Errorf("红相过河(rank=%d)", rank)
			}
			if col == game.Black && rank < 5 {
				return fmt.Errorf("黑象过河(rank=%d)", rank)
			}
			if !elephantPoint(file, rank, col) {
				return fmt.Errorf("相/象不在象位(file=%d,rank=%d)", file, rank)
			}
		}
	}
	if flyingGeneral(pos) {
		return fmt.Errorf("将帅对脸(飞将)")
	}
	return nil
}

func flyingGeneral(pos *game.Position) bool {
	rk, bk := -1, -1
	for sq90 := 0; sq90 < 90; sq90++ {
		pc := pos.PieceAt90(sq90)
		if pc == game.Empty {
			continue
		}
		if game.TypeOf(pc) == game.King {
			if game.ColorOf(pc) == game.Red {
				rk = sq90
			} else {
				bk = sq90
			}
		}
	}
	if rk < 0 || bk < 0 {
		return false
	}
	rf, rrank := rk%9, rk/9
	bf, brank := bk%9, bk/9
	if rf != bf {
		return false
	}
	lo, hi := rrank, brank
	if lo > hi {
		lo, hi = hi, lo
	}
	for r := lo + 1; r < hi; r++ {
		if pos.PieceAt90(r*9+rf) != game.Empty {
			return false
		}
	}
	return true
}

// ---- 难度分类 ----

func classifyDifficulty(source string) string {
	s := source
	switch {
	case strings.Contains(s, "初级") || strings.Contains(s, "第1阶段") || strings.Contains(s, "（初）") || strings.Contains(s, "（一）"):
		return "入门"
	case strings.Contains(s, "中级") || strings.Contains(s, "第2阶段") || strings.Contains(s, "（中）") || strings.Contains(s, "（二）"):
		return "初级"
	case strings.Contains(s, "高级") || strings.Contains(s, "第3阶段") || strings.Contains(s, "（高）"):
		return "中级"
	case strings.Contains(s, "连将杀一至四"):
		return "中级"
	case strings.Contains(s, "连将杀五至七"):
		return "大师" // 5~7 步连将杀，最难 → 大师档
	case strings.Contains(s, "象棋杀着大全"):
		return "高级"
	case strings.Contains(s, "陈松顺"):
		return "高级"
	case strings.Contains(s, "屠景明"):
		return "中级"
	case strings.Contains(s, "竹子涨棋"):
		return "初级"
	}
	return "中级"
}

// ---- 构建 Puzzle（仅解析 + 校验 + 分类，不含正解） ----

func buildPuzzle(id, name string, rec record, source string) (*puzzle.Puzzle, error) {
	pos, err := game.ParseFEN(rec.fen)
	if err != nil {
		return nil, fmt.Errorf("FEN 非法: %w", err)
	}
	if err := validatePlacement(pos); err != nil {
		return nil, fmt.Errorf("摆位非法: %w", err)
	}
	p := &puzzle.Puzzle{
		ID:         id,
		Name:       name,
		Source:     source,
		Difficulty: classifyDifficulty(source),
		PlayerSide: "red",
		Goal:       "win",
		FEN:        rec.fen,
		Tags:       []string{"canju12", source},
		Verified:   false,
	}
	if pos.Turn == game.Black {
		p.PlayerSide = "black"
	}
	if rec.flipped {
		p.PlayerSide = "black"
		p.Goal = "draw" // 执黑防守题：先按守和，第二阶段由引擎修正（若红方必胜则跳过）
		p.Tags = append(p.Tags, "防守")
	}
	return p, nil
}

// ---- 第二阶段：皮卡鱼求主变 PV，推导正解/par/goal ----

type job struct {
	idx int
	p   *puzzle.Puzzle
}

type result struct {
	idx    int
	p      *puzzle.Puzzle
	sol    []string
	goal   string
	par    int
	ok     bool
	reason string
}

// loaded 是"已解析+校验"的题（第二阶段求解前/后共用）。
type loaded struct {
	p       *puzzle.Puzzle
	source  string
	invalid bool
	reason  string
}

// solveWithPikafish 对一个局面求最佳路线。返回整条 PV（双方交替）、目标与 human 步数。
func solveWithPikafish(eng *engine.UCIEngine, p *puzzle.Puzzle, movetimeMs, maxPly int) (sol []string, goal string, par int, ok bool, reason string) {
	pos, err := game.ParseFEN(p.FEN)
	if err != nil {
		return nil, "", 0, false, "FEN非法"
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(movetimeMs*4+8000)*time.Millisecond)
	defer cancel()
	pv, cp, mate, err := eng.BestLine(ctx, pos, movetimeMs)
	if err != nil {
		return nil, "", 0, false, "引擎错误: " + err.Error()
	}
	if len(pv) == 0 {
		return nil, "", 0, false, "未获得PV"
	}
	sol = make([]string, 0, len(pv))
	for i, m := range pv {
		if i >= maxPly {
			break
		}
		sol = append(sol, m.String())
	}
	if len(sol) == 0 {
		return nil, "", 0, false, "PV为空"
	}
	human := game.Red
	if p.PlayerSide == "black" {
		human = game.Black
	}
	startTurn := pos.Turn
	par = 0
	for i := range sol {
		isHuman := (startTurn == human && i%2 == 0) || (startTurn != human && i%2 == 1)
		if isHuman {
			par++
		}
	}
	// 优先看将杀符号（弃子杀的静态分可能为负，必须以 mate 为准）：
	//   mate>0  行棋方必胜；mate<0 行棋方必败；mate==0 才看 cp。
	sideToMoveWins := mate > 0 || (mate == 0 && cp > 200)
	sideToMoveLoses := mate < 0 || (mate == 0 && cp < -200)
	if p.PlayerSide == "black" {
		// 执黑防守：红先行。若红能赢（含将杀）则黑无法守和 → 跳过该题
		if sideToMoveWins {
			return nil, "", 0, false, "执黑无法守和(红方必胜)"
		}
		goal = "draw"
	} else {
		if sideToMoveLoses {
			return nil, "", 0, false, "红方劣势(非胜局)"
		}
		if sideToMoveWins {
			goal = "win"
		} else {
			goal = "draw"
		}
	}
	return sol, goal, par, true, ""
}

func worker(jobs <-chan job, results chan<- result, engPath string, movetimeMs, maxPly int) {
	eng, err := engine.NewUCIEngine(engPath)
	if err != nil {
		for j := range jobs {
			results <- result{idx: j.idx, p: j.p, ok: false, reason: "引擎启动失败: " + err.Error()}
		}
		return
	}
	defer eng.Close()
	for j := range jobs {
		sol, goal, par, ok, reason := solveWithPikafish(eng, j.p, movetimeMs, maxPly)
		results <- result{j.idx, j.p, sol, goal, par, ok, reason}
	}
}

// ---- main ----

func main() {
	inDir := flag.String("intxt", "canju12_tmp/app/src/main/assets", "canju12 题库目录（含 NeizhiTiku/YincangTiku）")
	outDir := flag.String("out", "internal/puzzle/data_canju", "输出目录（不覆盖现有 data）")
	solve := flag.Bool("solve", false, "第二阶段：用皮卡鱼求主变 PV 填充正解/par/goal")
	engPath := flag.String("engine", "dist-engines/pikafish-avx2.exe", "皮卡鱼 Windows 可执行文件")
	workers := flag.Int("workers", 10, "并行引擎数")
	movetime := flag.Int("movetime", 1200, "每题搜索时长(ms)")
	maxPly := flag.Int("maxply", 40, "正解最多保留半回合数")
	match := flag.String("match", "", "只处理来源名包含该子串的题库（空=全部）")
	exclude := flag.String("exclude", "", "跳过来源名包含这些子串的题库（逗号分隔，如 夏老大）")
	limit := flag.Int("limit", 0, "最多处理 N 题（0=全部，用于小批量验证）")
	flag.Parse()

	subDirs := []string{"NeizhiTiku", "YincangTiku"}
	var files []string
	for _, sd := range subDirs {
		entries, err := os.ReadDir(filepath.Join(*inDir, sd))
		if err != nil {
			fmt.Fprintf(os.Stderr, "跳过 %s: %v\n", sd, err)
			continue
		}
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".txt") {
				files = append(files, filepath.Join(*inDir, sd, e.Name()))
			}
		}
	}
	sort.Strings(files)

	os.MkdirAll(*outDir, 0o755)

	// ---- 解析 + 校验 + 分类（所有来源） ----
	var all []loaded
	diffCount := map[string]int{}
	var invalidSamples []string

	for _, fp := range files {
		base := filepath.Base(fp)
		source := strings.TrimSuffix(base, ".txt")
		if *match != "" && !strings.Contains(source, *match) {
			continue
		}
		if *exclude != "" {
			skip := false
			for _, ex := range strings.Split(*exclude, ",") {
				if ex != "" && strings.Contains(source, ex) {
					skip = true
					break
				}
			}
			if skip {
				continue
			}
		}
		recs, err := loadTikuFile(fp)
		if err != nil {
			fmt.Fprintf(os.Stderr, "读取 %s 失败: %v\n", base, err)
			continue
		}
		for i, rec := range recs {
			if *limit > 0 && len(all) >= *limit {
				break
			}
			id := fmt.Sprintf("cj-%s-%04d", sanitize(source), i+1)
			name := fmt.Sprintf("%s #%04d", source, i+1)
			p, err := buildPuzzle(id, name, rec, source)
			if err != nil {
				if len(invalidSamples) < 50 {
					invalidSamples = append(invalidSamples, fmt.Sprintf("[%s] %s -> %v", source, rec.fen, err))
				}
				all = append(all, loaded{nil, source, true, err.Error()})
				continue
			}
			all = append(all, loaded{p, source, false, ""})
			diffCount[p.Difficulty]++
		}
	}

	if !*solve {
		// 仅输出 FEN/分类（无正解）
		writeBySource(*outDir, all, false)
		printSummary(diffCount, invalidSamples, len(all))
		return
	}

	// ---- 第二阶段：皮卡鱼并行求解 ----
	if _, err := os.Stat(*engPath); err != nil {
		fmt.Fprintf(os.Stderr, "找不到引擎 %s：%v\n", *engPath, err)
		os.Exit(1)
	}
	n := *workers
	if n < 1 {
		n = 1
	}
	jobs := make(chan job, n*2)
	results := make(chan result, n*2)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			worker(jobs, results, *engPath, *movetime, *maxPly)
		}()
	}
	go func() {
		idx := 0
		for _, l := range all {
			if l.invalid {
				continue
			}
			jobs <- job{idx, l.p}
			idx++
		}
		close(jobs)
	}()

	// 收集结果，更新 puzzle
	valid, invalid := 0, 0
	goalCount := map[string]int{}
	var failSamples []string
	total := 0
	for _, l := range all {
		if l.invalid {
			invalid++
			continue
		}
		total++
	}
	for i := 0; i < total; i++ {
		r := <-results
		if !r.ok {
			invalid++
			if len(failSamples) < 50 {
				failSamples = append(failSamples, fmt.Sprintf("[%s] %s -> %s", r.p.Source, r.p.FEN, r.reason))
			}
			continue
		}
		r.p.Solution = r.sol
		r.p.Goal = r.goal
		r.p.ParMoves = r.par
		r.p.Verified = true
		goalCount[r.goal]++
		valid++
		if (valid+invalid)%200 == 0 {
			fmt.Printf("  进度 %d/%d (有效 %d)\n", valid+invalid, total, valid)
		}
	}
	close(results)
	wg.Wait()

	writeBySource(*outDir, all, true)
	printSummary(diffCount, nil, len(all))
	fmt.Printf("求解有效: %d    求解失败/跳过: %d\n", valid, invalid)
	fmt.Printf("目标分布: ")
	for _, g := range []string{"win", "draw"} {
		if n := goalCount[g]; n > 0 {
			fmt.Printf("%s=%d  ", g, n)
		}
	}
	fmt.Println()
	if len(failSamples) > 0 {
		fmt.Printf("\n---- 求解失败样本（前 %d）----\n", len(failSamples))
		for _, s := range failSamples {
			fmt.Println(s)
		}
	}
}

func writeBySource(outDir string, all []loaded, doSolve bool) {
	bySource := map[string][]*puzzle.Puzzle{}
	for _, l := range all {
		if l.invalid || l.p == nil {
			continue
		}
		// 求解模式下，丢弃：无解 / human 步数为 0 / 回放正解后未达成宣称目标（非强制胜局或伪和）
		if doSolve && (len(l.p.Solution) == 0 || l.p.ParMoves <= 0 || !solutionReachesGoal(l.p)) {
			continue
		}
		bySource[l.source] = append(bySource[l.source], l.p)
	}
	for source, ps := range bySource {
		outPath := filepath.Join(outDir, "canju_"+sanitize(source)+".json")
		blob, err := json.MarshalIndent(ps, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "序列化 %s 失败: %v\n", source, err)
			continue
		}
		if err := os.WriteFile(outPath, append(blob, '\n'), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "写 %s 失败: %v\n", outPath, err)
			continue
		}
		fmt.Printf("%-44s 有效 %3d\n", source, len(ps))
	}
}

// solutionReachesGoal 回放整条正解，确认终局符合 goal（与 internal/puzzle 的 CI 全量复验收敛一致）。
// 只保留"强制杀/真和"的题，剔除 PV 未延伸到将死、或伪和（fortress 等无法判和）的题。
func solutionReachesGoal(p *puzzle.Puzzle) bool {
	pos, err := game.ParseFEN(p.FEN)
	if err != nil {
		return false
	}
	for _, uci := range p.Solution {
		m, ok := game.MoveFromUCI(uci)
		if !ok || !pos.IsLegal(m) {
			return false
		}
		pos.Make(m)
	}
	st := pos.CheckStatus()
	winner := game.Red
	if p.PlayerSide == "black" {
		winner = game.Black
	}
	switch p.Goal {
	case "draw":
		return st.IsDraw || st.Result == game.ResultDraw
	case "win":
		want := game.ResultRedWin
		if winner == game.Black {
			want = game.ResultBlackWin
		}
		return st.Result == want && (st.Reason == game.ReasonCheckmate || st.Reason == game.ReasonStalemate)
	default:
		return false
	}
}

func printSummary(diffCount map[string]int, invalidSamples []string, total int) {
	fmt.Printf("\n==== 汇总 ====\n")
	fmt.Printf("总题数(含非法): %d\n", total)
	fmt.Printf("难度分布: ")
	for _, d := range []string{"入门", "初级", "中级", "高级", "大师"} {
		if n := diffCount[d]; n > 0 {
			fmt.Printf("%s=%d  ", d, n)
		}
	}
	fmt.Println()
	if len(invalidSamples) > 0 {
		fmt.Printf("\n---- 非法/未解出样本（前 %d）----\n", len(invalidSamples))
		for _, s := range invalidSamples {
			fmt.Println(s)
		}
	}
}

func sanitize(s string) string {
	r := strings.NewReplacer("（", "(", "）", ")", " ", "_", "/", "_", "\\", "_", ":", "_")
	return r.Replace(s)
}
