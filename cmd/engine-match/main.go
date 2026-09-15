// cmd/engine-match：内嵌 Go 引擎（PandaEngine）与皮卡鱼的等时对拍工具。
//
// 两种模式：
//
//	moves —— 若干典型局面，两边各搜同样时间，比 bestmove 一致率（约一分钟出结果）
//	games —— 完整对局，交替先后手，统计胜负并估算 Elo 差（取决于局数与每步时限）
//
// 等时口径：两边固定同样的 movetime，皮卡鱼 Skill=20（不削弱）。两套引擎的档位
// 参数表并不通用（皮卡鱼调 Skill Level，内嵌引擎调深度与随机池），只有思考时间
// 是可比的量。
//
// 关于置换表：双方都不在局间清空（皮卡鱼进程常驻、内嵌引擎池也常驻），
// 这与用户连续对局的实际情况一致；也因此结果不受「谁记得更多」影响。
//
// 用法示例：
//
//	go run ./cmd/engine-match -mode moves -movetime 1000
//	go run ./cmd/engine-match -mode games -movetime 1000 -games 6 -maxply 50
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/IamAyang233/panda-xiangqi/internal/engine"
	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/puzzle"
)

// timedEngine 是等时对拍的统一接口。
type timedEngine interface {
	BestMoveTimed(ctx context.Context, pos *game.Position, movetime time.Duration) (game.Move, error)
	Name() string
}

// nativeAdapter 把 NativeEngine 适配到 timedEngine。
type nativeAdapter struct{ e *engine.NativeEngine }

func (a nativeAdapter) BestMoveTimed(ctx context.Context, pos *game.Position, mt time.Duration) (game.Move, error) {
	return a.e.BestMoveTimed(ctx, pos, mt)
}
func (a nativeAdapter) Name() string { return a.e.Name() }

// uciAdapter 把 UCIEngine 适配到 timedEngine，并固定 Skill 等级。
type uciAdapter struct {
	e     *engine.UCIEngine
	skill int
}

func (a uciAdapter) BestMoveTimed(ctx context.Context, pos *game.Position, mt time.Duration) (game.Move, error) {
	return a.e.BestMoveTimed(ctx, pos, mt, a.skill)
}
func (a uciAdapter) Name() string { return a.e.Name() }

// matchFENs 是着法一致率模式用的局面集：覆盖开局、中局与几类常见残局。
var matchFENs = []struct {
	name string
	fen  string
}{
	{"初始局面", game.InitialFEN},
	{"开局（中炮对屏风马型）", "r1ba1a3/4kn3/2n1b4/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"},
	{"中局（子力互缠）", "2bak1b2/4a4/4b4/p1p1p3p/6p2/2P6/P3P1P1P/1C2C4/9/RNBAKABNR w - - 0 1"},
	{"中局（车炮对车马）", "3ak1b2/4a4/4b4/p1p1p3p/9/2P6/P3P1P1P/1C2C1R2/9/2BAKABN1 w - - 0 1"},
	{"残局（车兵对士象全）", "3ak4/4a4/4b4/9/9/9/4P4/9/4R4/4K4 w - - 0 1"},
	{"残局（双车对车士）", "4k4/4a4/9/9/9/9/9/9/2R1R4/3K5 w - - 0 1"},
	{"残局（车马对车）", "3k5/9/9/9/4r4/9/9/9/3NR4/3K5 w - - 0 1"},
	{"残局（炮兵对士象）", "3ak4/4a4/4b4/9/9/9/9/4N4/3C5/4K4 w - - 0 1"},
}

func main() {
	mode := flag.String("mode", "moves", "moves = 着法一致率；games = 完整对局；puzzle = 残局战术诊断；depth = 同题同时间的搜索深度对比")
	movetime := flag.Int("movetime", 1000, "每步思考时间（毫秒）")
	games := flag.Int("games", 6, "对局数（mode=games）")
	maxPly := flag.Int("maxply", 50, "单局步数上限（mode=games）")
	uciPath := flag.String("uci", "dist/pikafish.exe", "皮卡鱼可执行文件路径")
	uciSkill := flag.Int("skill", 20, "皮卡鱼 Skill 等级（20 = 不削弱）")
	flatPath := flag.String("flat", "engines/pikafish.nnue.flat", "内嵌引擎的 NNUE 权重路径")
	threads := flag.Int("threads", 0, "内嵌引擎线程数（0 = 自动探测）")
	puzzleDir := flag.String("data", "internal/puzzle/data", "残局题库目录（mode=puzzle）")
	puzzleLimit := flag.Int("limit", 24, "残局题数（mode=puzzle）")
	verbose := flag.Bool("v", false, "逐着打印")
	maxDepthFlag := flag.Int("maxdepth", 7, "逐层诊断的最大深度（mode=profile）")
	fenFlag := flag.String("fen", "", "只诊断这一个 FEN（mode=profile，留空则用内置局面集）")
	depthsFlag := flag.String("depths", "10,12", "固定深度列表，逗号分隔（mode=self；调 LMR/剪枝时对比节点总数）")
	flag.Parse()

	mt := time.Duration(*movetime) * time.Millisecond

	native := engine.NewNativeEngine(*flatPath, *threads)
	defer native.Close()
	if err := native.Warmup(); err != nil {
		fmt.Println("内嵌引擎初始化失败：", err)
		os.Exit(1)
	}
	na := nativeAdapter{native}

	uci, err := engine.NewUCIEngine(*uciPath)
	if err != nil {
		fmt.Printf("皮卡鱼启动失败（%s）：%v\n", *uciPath, err)
		fmt.Println("提示：本机可用的皮卡鱼在 dist/ 或 dist-engines/ 下，用 -uci 指定。")
		os.Exit(1)
	}
	defer uci.Close()
	ua := uciAdapter{uci, *uciSkill}

	fmt.Printf("=== 等时对拍 ===\n")
	fmt.Printf("内嵌引擎: %s（%d 线程）\n", na.Name(), native.Threads())
	fmt.Printf("对手    : %s（Skill %d）\n", ua.Name(), *uciSkill)
	fmt.Printf("每步时限: %v\n\n", mt)

	switch *mode {
	case "moves":
		runMoves(na, ua, mt, *verbose)
	case "games":
		runGames(na, ua, mt, *games, *maxPly, *verbose)
	case "puzzle":
		runPuzzles(na, ua, mt, *puzzleDir, *puzzleLimit, *verbose)
	case "depth":
		runDepthProbe(native, *uciPath, mt, *uciSkill)
	case "profile":
		set := matchFENs
		if *fenFlag != "" {
			set = []struct {
				name string
				fen  string
			}{{"自定义局面", *fenFlag}}
		}
		uci.Close() // profile 自行建立 UCI 会话，先释放长连接
		runSearchProfile(*flatPath, *uciPath, *maxDepthFlag, *uciSkill, set)
	case "nodes":
		uci.Close()
		nodesBudget(*flatPath, *uciPath, *uciSkill, []int64{5000, 20000, 100000, 500000}, matchFENs)
	case "self":
		runSelfProbe(*flatPath, parseInts(*depthsFlag, []int{10, 12}), matchFENs)
	default:
		fmt.Println("未知 mode：", *mode)
		os.Exit(2)
	}
}

// loadPuzzles 从题库目录载入带正解的胜局题（题库按来源分成多个 canju_*.json）。
func loadPuzzles(dir string, limit int) []puzzle.Puzzle {
	entries, err := os.ReadDir(dir)
	if err != nil {
		fmt.Printf("题库目录读取失败：%v\n", err)
		return nil
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)

	var out []puzzle.Puzzle
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			continue
		}
		var list []puzzle.Puzzle
		if err := json.Unmarshal(data, &list); err != nil {
			fmt.Printf("题库 %s 解析失败：%v\n", f, err)
			continue
		}
		for _, p := range list {
			if p.Goal == "win" && len(p.Solution) > 0 {
				out = append(out, p)
				if limit > 0 && len(out) >= limit {
					return out
				}
			}
		}
	}
	return out
}

// runPuzzles 残局战术诊断：比较引擎首着与题库正解。
//
// 正解可能有等价多解，所以命中率不是绝对指标；但命中率过低（比如低于三成）
// 基本可以断定搜索存在缺陷，而不是「棋风不同」—— 这类题目标的是唯一杀法。
func runPuzzles(na, ua timedEngine, mt time.Duration, dir string, limit int, verbose bool) {
	ps := loadPuzzles(dir, limit)
	if len(ps) == 0 {
		fmt.Println("没找到可用题目（需要 Verified 且带 Solution 的胜局题）")
		return
	}
	fmt.Printf("载入 %d 道残局题，每步 %v\n\n", len(ps), mt)

	for _, eng := range []timedEngine{na, ua} {
		hit, total, skipped := 0, 0, 0
		start := time.Now()
		for _, pz := range ps {
			pos, err := game.ParseFEN(pz.FEN)
			if err != nil {
				skipped++
				continue
			}
			wantRed := pz.PlayerSide != "black"
			if (pos.Turn == game.Red) != wantRed {
				skipped++ // 轮次与出题方不符，跳过
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), mt*5+10*time.Second)
			mv, err := eng.BestMoveTimed(ctx, pos, mt)
			cancel()
			if err != nil {
				skipped++
				continue
			}
			total++
			if mv.String() == pz.Solution[0] {
				hit++
				if verbose {
					fmt.Printf("  ✓ %s %s（%s）\n", pz.ID, mv, pz.Name)
				}
			} else if verbose {
				fmt.Printf("  ✗ %s 走 %s，正解 %s（%s）\n", pz.ID, mv, pz.Solution[0], pz.Name)
			}
		}
		el := time.Since(start)
		acc := 0.0
		if total > 0 {
			acc = float64(hit) / float64(total) * 100
		}
		fmt.Printf("%-14s 正解命中 %d/%d = %.0f%%（跳过 %d，用时 %v）\n",
			eng.Name(), hit, total, acc, skipped, el.Round(time.Second))
	}
	fmt.Println("\n正解可能有多解，命中率不是绝对棋力指标；但明显偏低说明战术搜索有缺陷。")
}

// runMoves 比对同样时限下两边选出的着法。
//
// 一致率高说明两者对这局的判断接近；一致率低也不必然是坏消息 ——
// 若两者分值接近（互有胜负的等价着法），选不同是正常的。所以这里同时打出
// 双方各自的搜索耗时，便于判断是否有一方"想得太少"。
func runMoves(na, ua timedEngine, mt time.Duration, verbose bool) {
	same, total := 0, 0
	for _, c := range matchFENs {
		p, err := game.ParseFEN(c.fen)
		if err != nil {
			fmt.Printf("跳过（FEN 非法）%s: %v\n", c.name, err)
			continue
		}
		var mvNative, mvUCI game.Move
		var tN, tU time.Duration

		start := time.Now()
		mvNative, err = na.BestMoveTimed(context.Background(), p, mt)
		tN = time.Since(start)
		if err != nil {
			fmt.Printf("%-24s 内嵌引擎出错: %v\n", c.name, err)
			continue
		}

		start = time.Now()
		mvUCI, err = ua.BestMoveTimed(context.Background(), p, mt)
		tU = time.Since(start)
		if err != nil {
			fmt.Printf("%-24s 皮卡鱼出错: %v\n", c.name, err)
			continue
		}

		total++
		mark := "不同"
		if mvNative == mvUCI {
			same++
			mark = "一致"
		}
		fmt.Printf("%-24s 内嵌 %-6s(%5v)  皮卡鱼 %-6s(%5v)  %s\n",
			c.name, mvNative, tN.Round(time.Millisecond), mvUCI, tU.Round(time.Millisecond), mark)
		if verbose {
			fmt.Printf("      FEN: %s\n", c.fen)
		}
	}
	if total > 0 {
		fmt.Printf("\n着法一致率: %d/%d = %.0f%%\n", same, total, float64(same)/float64(total)*100)
		fmt.Println("（低一致率不必然代表弱：等价着法之间选择不同是正常的，要看对局胜负才有结论）")
	}
}

// runGames 完整对局，交替先后手。
func runGames(na, ua timedEngine, mt time.Duration, games, maxPly int, verbose bool) {
	var win, loss, draw, errs int
	for g := 0; g < games; g++ {
		nativeIsRed := g%2 == 0 // 交替先后手，避免先手优势污染结论
		side := "红"
		if !nativeIsRed {
			side = "黑"
		}
		fmt.Printf("第 %d/%d 局（内嵌引擎执%s）… ", g+1, games, side)

		res, plies, detail := playGame(na, ua, mt, maxPly, nativeIsRed, verbose)
		// 顺序有讲究：非终局结果（步数上限/异常）必须先判，
		// 否则会被后面的胜负分支误吞（例如 maxply 曾被判成「内嵌引擎胜」）。
		switch {
		case res == "maxply":
			draw++
			fmt.Printf("达到步数上限判和（%d 步）\n", plies)
		case strings.HasPrefix(res, "error:") || strings.HasPrefix(res, "illegal:"):
			errs++
			fmt.Printf("异常：%s\n", res)
		case res == game.ResultDraw:
			draw++
			fmt.Printf("和棋（%d 步，%s）\n", plies, detail)
		case (res == game.ResultRedWin) == nativeIsRed:
			win++
			fmt.Printf("内嵌引擎胜（%d 步，%s）\n", plies, detail)
		default:
			loss++
			fmt.Printf("皮卡鱼胜（%d 步，%s）\n", plies, detail)
		}
	}

	fmt.Printf("\n=== 结果 ===\n")
	fmt.Printf("内嵌引擎 %d 胜 / %d 负 / %d 和", win, loss, draw)
	if errs > 0 {
		fmt.Printf("，%d 局异常", errs)
	}
	fmt.Println()

	n := win + loss + draw
	if n == 0 {
		return
	}
	score := (float64(win) + float64(draw)/2) / float64(n)
	fmt.Printf("得分率 %.1f%%\n", score*100)

	const maxElo = 800.0
	switch {
	case score >= 1:
		fmt.Printf("Elo 差 > +%.0f（内嵌引擎更强；样本太小无意义，需更多局）\n", maxElo)
	case score <= 0:
		fmt.Printf("Elo 差 < -%.0f（皮卡鱼更强）\n", maxElo)
	default:
		elo := -400 * math.Log10(1/score-1)
		fmt.Printf("Elo 差 ≈ %+.0f（正 = 内嵌引擎更强）\n", elo)
	}

	// 小样本的置信区间很宽，必须讲清楚，否则容易过度解读。
	se := math.Sqrt(score * (1 - score) / float64(n)) // 得分率的标准误
	fmt.Printf("样本仅 %d 局，得分率标准误约 ±%.1f 个百分点", n, se*100)
	if n < 40 {
		fmt.Printf(" —— 这个样本量只能看出「量级差异」，要得出稳定结论建议 100 局以上")
	}
	fmt.Println()
	fmt.Println("说明：双方都在局间保留置换表（引擎进程常驻），与实际连续对局一致。")
}

// playGame 走完一局，返回（结果, 步数, 结束原因）。
func playGame(na, ua timedEngine, mt time.Duration, maxPly int, nativeIsRed bool, verbose bool) (string, int, string) {
	p := game.NewPosition()
	for ply := 0; ply < maxPly; ply++ {
		var eng timedEngine
		if (p.Turn == game.Red) == nativeIsRed {
			eng = na
		} else {
			eng = ua
		}

		// 给引擎留出充裕余量（它自己按 movetime 控制），避免 context 先超时。
		ctx, cancel := context.WithTimeout(context.Background(), mt*5+10*time.Second)
		mv, err := eng.BestMoveTimed(ctx, p, mt)
		cancel()
		if err != nil {
			return "error:" + err.Error(), ply, "引擎出错"
		}
		if !p.IsLegal(mv) {
			// 报出是哪一方、在什么局面上：非法着法往往对应引擎内部的
			// 状态残留，带上 FEN 才能复现。
			who := "皮卡鱼"
			if eng == na {
				who = "内嵌引擎"
			}
			return "illegal:" + mv.String() + "（" + who + "，局面 " + p.FEN() + "）", ply, "引擎给出非法着法"
		}
		p.Make(mv)

		if verbose {
			fmt.Println()
			fmt.Printf("    %2d. %s %s\n", ply/2+1, p.MoveToChinese(mv), mv)
		}

		if st := p.CheckStatus(); st.Result != game.ResultNone {
			return st.Result, ply + 1, reasonText(st.Reason)
		}
	}
	return "maxply", maxPly, "步数上限"
}

// reasonText 把终局原因转成中文。
func reasonText(reason string) string {
	switch reason {
	case game.ReasonCheckmate:
		return "将死"
	case game.ReasonStalemate:
		return "困毙"
	case game.ReasonRepetition:
		return "三次重复"
	case game.ReasonLongCheck:
		return "长将"
	case game.ReasonSixtyMoves:
		return "60 回合限着"
	case game.ReasonInsufficient:
		return "子力不足"
	default:
		return reason
	}
}
