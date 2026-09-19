package search

import (
	"testing"
	"time"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/nnue"
)

// 配对 A/B 的模板：把 A/B 两个分支放进**同一个循环**里逐轮交替，
// 用两侧耗时之比当指标。
//
// 为什么不能靠跨进程的「跑一次 A、跑一次 B」：这台机器有快慢相
// （同一份代码两次运行能差 3.7×）。实测同一对改动，
//
//	慢相里跨进程 ABBA 八轮   ⇒ 比值中位 0.866（+15%）
//	快相里跨进程 ABBA 四轮   ⇒ 比值中位 0.989（+1%）
//	同一进程逐轮交替三次     ⇒ 0.9314 / 0.9371 / 0.9315（+7.2%）
//
// 前两个数都是漂移的产物，只有第三个站得住。跨进程再怎么换顺序，AB 两轮
// 之间仍然隔了一次进程启动与一次相位跳变；放进同一循环后，两侧共享同一段
// 时间里的同一台机器，比值才是干净的。
//
// 用法：把 SetUseFuseRows 换成你要对比的开关（要留成运行时可切，不能改代码）。
// 参考量级：6 轮 ≈ 40s，够出稳定比值。
func BenchmarkFuseRowsPairedAB(b *testing.B) {
	w, err := nnue.Load(flatPath)
	if err != nil {
		b.Skip("未找到展开后的权重，跳过")
	}
	fens := quietFENsForBench(b, 20)
	parsed := make([]*game.Position, len(fens))
	for i, f := range fens {
		p, err := game.ParseFEN(f)
		if err != nil {
			b.Fatal(err)
		}
		parsed[i] = p
	}
	round := func() { // 每次都用全新 Searcher，状态不跨轮共享
		for _, p := range parsed {
			New(w).SearchNodes(p, 60000)
		}
	}
	var tOff, tOn time.Duration
	for i := 0; i < b.N*6; i++ {
		nnue.SetUseFuseRows(false)
		t0 := time.Now()
		round()
		tOff += time.Since(t0)

		nnue.SetUseFuseRows(true)
		t0 = time.Now()
		round()
		tOn += time.Since(t0)
	}
	b.ReportMetric(float64(tOn)/float64(tOff), "on/off")
	b.ReportMetric(float64(tOff.Milliseconds()), "off_ms")
	b.ReportMetric(float64(tOn.Milliseconds()), "on_ms")
}

// 配对本身的边际贡献：融合在两侧都开着，只切配对开关。
func BenchmarkRowPairingPairedAB(b *testing.B) {
	w, err := nnue.Load(flatPath)
	if err != nil {
		b.Skip("未找到展开后的权重，跳过")
	}
	fens := quietFENsForBench(b, 20)
	parsed := make([]*game.Position, len(fens))
	for i, f := range fens {
		p, err := game.ParseFEN(f)
		if err != nil {
			b.Fatal(err)
		}
		parsed[i] = p
	}
	round := func() {
		for _, p := range parsed {
			New(w).SearchNodes(p, 60000)
		}
	}
	nnue.SetUseFuseRows(true)
	var tOff, tOn time.Duration
	for i := 0; i < b.N*6; i++ {
		nnue.SetRowPairing(false)
		t0 := time.Now()
		round()
		tOff += time.Since(t0)

		nnue.SetRowPairing(true)
		t0 = time.Now()
		round()
		tOn += time.Since(t0)
	}
	b.ReportMetric(float64(tOn)/float64(tOff), "pair/off")
}

// GivesCheck 快路径 vs 全量参考实现：融合在两侧都开着，只切 GivesCheck 的实现。
func BenchmarkGivesCheckPairedAB(b *testing.B) {
	w, err := nnue.Load(flatPath)
	if err != nil {
		b.Skip("未找到展开后的权重，跳过")
	}
	fens := quietFENsForBench(b, 20)
	parsed := make([]*game.Position, len(fens))
	for i, f := range fens {
		p, err := game.ParseFEN(f)
		if err != nil {
			b.Fatal(err)
		}
		parsed[i] = p
	}
	round := func() {
		for _, p := range parsed {
			New(w).SearchNodes(p, 60000)
		}
	}
	var tOff, tOn time.Duration
	for i := 0; i < b.N*6; i++ {
		game.SetBruteGivesCheck(true) // 参考实现（慢）
		t0 := time.Now()
		round()
		tOff += time.Since(t0)

		game.SetBruteGivesCheck(false) // 快路径
		t0 = time.Now()
		round()
		tOn += time.Since(t0)
	}
	b.ReportMetric(float64(tOn)/float64(tOff), "fast/brute")
}

// fc_0 的 8 输出版 vs 4 输出版。
func BenchmarkFC0PairedAB(b *testing.B) {
	w, err := nnue.Load(flatPath)
	if err != nil {
		b.Skip("未找到展开后的权重，跳过")
	}
	fens := quietFENsForBench(b, 20)
	parsed := make([]*game.Position, len(fens))
	for i, f := range fens {
		p, err := game.ParseFEN(f)
		if err != nil {
			b.Fatal(err)
		}
		parsed[i] = p
	}
	round := func() {
		for _, p := range parsed {
			New(w).SearchNodes(p, 60000)
		}
	}
	var tOff, tOn time.Duration
	for i := 0; i < b.N*6; i++ {
		nnue.SetFC0Block8(false)
		t0 := time.Now()
		round()
		tOff += time.Since(t0)

		nnue.SetFC0Block8(true)
		t0 = time.Now()
		round()
		tOn += time.Since(t0)
	}
	b.ReportMetric(float64(tOn)/float64(tOff), "b8/b4")
}
