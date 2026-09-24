package review

// 关键手分析的两条底线：真亏的一手要标出来、正常的一手不许误伤。
//
// 这组测试**需要真的 NNUE 权重**：没有权重时引擎只给静态评估（Strong=false），
// 分差类标签一律不打（这是刻意的保守策略），测不到 delta 这条路径。缺权重时
// 与 internal/engine 各测试同一约定——skip。
//
// 为什么要专门测「不误伤」：第一版用「走子前搜一次 + 走子后搜一次」再相减，
// 两次搜索的地平线效应不同源，实测把「马8进7」这种正常出子也算成亏 129 分。
// 改成同一次搜索的根节点分值后误差才收敛 —— 这条回归必须钉住。

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/IamAyang233/panda-xiangqi/internal/engine"
	"github.com/IamAyang233/panda-xiangqi/internal/record"
)

// hangFEN：黑车 a1 无人保护，红车 e1 沿横线一步可白吃它。
// 红帅 e0、黑将 d9 不同线（不照面）；红方未被将军（黑车在 1 行，帅在 0 行）。
//
// 不能把黑车放在 e 线上：那样红车 e1 就是「挡住对方车将军」的唯一屏障，
// 让它离开 e 线属于自曝其帅、根本不合法 —— 第一版这么写，测试报「该手不合法」。
const hangFEN = "3k5/9/9/9/9/9/9/9/r3R4/4K4 w"

func newEngine(t *testing.T) *engine.Manager {
	t.Helper()
	w := "../../engines/pikafish.nnue.flat"
	if _, err := os.Stat(w); err != nil {
		t.Skip("未找到 NNUE 权重（engines/pikafish.nnue.flat），跳过分差类断言")
	}
	m := engine.NewManagerWithNNUE("", w)
	t.Cleanup(m.Close)
	return m
}

func makeRec(startFEN string, ucis ...string) *record.Record {
	rec := &record.Record{
		ID: "t", Name: "测试", Mode: "engine", HumanSide: "red",
		StartFEN: startFEN, Result: "resign", Reason: "resign",
		Created: time.Now().Format("2006-01-02 15:04:05"),
	}
	for _, u := range ucis {
		rec.Moves = append(rec.Moves, record.Move{UCI: u, CN: u, Red: len(rec.Moves)%2 == 0})
	}
	return rec
}

func analyze(t *testing.T, rec *record.Record) []record.KeyMove {
	t.Helper()
	m := newEngine(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	km, err := Analyze(ctx, rec, m)
	if err != nil {
		t.Fatalf("分析失败: %v", err)
	}
	return km
}

func tagOf(km []record.KeyMove, index int) string {
	for _, k := range km {
		if k.Index == index {
			return k.Tag
		}
	}
	return ""
}

// ⚠️ 这里**没有**「真失误必须被标出来」的合成测试，原因如实记下：
// 分差类标签要求引擎给出的是**子力型**分值，而我试过的两个构造都不可用 ——
//   1) 车对车残局（黑车被白吃）是杀棋主导的：所有着法同为杀棋分值（实测 524285），
//      分差恒为 0，测不出任何东西；
//   2) 开局局面里任何一步的落差都远小于阈值（没有子可丢）。
// 于是改用**实测分布**来佐证阈值合理（2026-09-24 一次性诊断，6 手后的真实中局、
// 每手 50ms 浅搜、深度 10）：35 个合法着法的根节点分从 +14 到 −921（最差那手等于
// 白丢一个车），其中 7 手越过「疑问手」阈值、分差 < 200 的正常出子不会被误伤。
// 要让这条变成自动化测试，得先构造「有子可丢且无速杀」的局面，或给 Analyze 注入
// 打分函数（当前签名收的是 *engine.Manager，注入不了）。

// TestAnalyzeDoesNotFlagBestMove 真吃子（也是最佳手）不该被标成失误/疑问手。
func TestAnalyzeDoesNotFlagBestMove(t *testing.T) {
	rec := makeRec(hangFEN, "e1a1") // 沿 1 行吃掉 a1 的黑车
	rec.Moves[0].Captured = "r"
	km := analyze(t, rec)
	switch got := tagOf(km, 0); got {
	case record.TagBlunder, record.TagMistake:
		t.Fatalf("白吃车是最佳手，不该标成 %q（关键手：%+v）", got, km)
	}
}

// TestAnalyzeNormalOpeningNotFlagged 开局正常出子不该进关键手列表 —— 这是第一版
// 「同源分差」修好之前误报最多的一类。
func TestAnalyzeNormalOpeningNotFlagged(t *testing.T) {
	const initFEN = "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w"
	km := analyze(t, makeRec(initFEN, "b2e2", "h9g7")) // 炮二平五、马8进7（都是正常开局）
	for _, k := range km {
		if k.Tag == record.TagBlunder || k.Tag == record.TagMistake {
			t.Fatalf("正常开局不该被标成 %s（第 %d 手，delta=%d）", k.Tag, k.Index+1, k.Delta)
		}
	}
}

// TestAnalyzeSkipsTerminalMove 终局那一手标 mate，不再拿分差去标（终局分值是极值）。
func TestAnalyzeSkipsTerminalMove(t *testing.T) {
	// 红车 e1 直接吃 e2 的车后…这里构造一个一步将死的局面：
	// 黑将 d9、红车 d1（d 线直通）、红帅 e0 —— 车 d1→d9 将死（黑将无处可走）
	const mateFEN = "3k5/9/9/9/9/9/9/9/3R5/4K4 w"
	rec := makeRec(mateFEN, "d1d9")
	rec.Result, rec.Reason = "red_win", "checkmate"
	km := analyze(t, rec)
	if got := tagOf(km, 0); got != record.TagMate {
		t.Fatalf("终局一手应标 mate，实际 %q（关键手：%+v）", got, km)
	}
}
