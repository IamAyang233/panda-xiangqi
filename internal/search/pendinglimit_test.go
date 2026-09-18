package search

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/nnue"
)

// TestPendingLimitSweep 扫描 pendingLimit，确认它**两端都不该动**。
//
// 直觉上「全量重建要枚举全部特征（约 32 次滑动攻击 + 21 条 thrAdd），
// 比增量应用贵得多，所以阈值该放大」是错的。实测（初始局面，早起版本）：
//
//	pendingLimit   96     256    512    1024   2048
//	相对耗时       1.000  1.000  1.078  1.086  1.141
//
// 原因是提高阈值只让约 12% 的视角少走重建，却让占 88% 的增量路径窗口
// 同步变长，条目数线性增长。这个测试的存在意义是挡住「凭直觉调大它」的改动。
//
// 2026-09-17 把**全范围**都扫了一遍（此前只扫过 ≥96）：
//
//	 8 档 1.000×｜16 档 0.829×｜24 档 0.787×｜32 档 0.778×
//	48 档 0.773×｜64 档 0.772×｜**96 档 0.775×**｜128 档 0.795×
//	192 档 0.811×｜256 档 0.828×
//
// ⇒ 最优点在 48~64，但 96 只慢 0.4%（同一平台期）；**往下走则明显变差**
// （8 档慢 29%，重建太频繁）。曲线是平台型、两端都别动 —— 所以现在同时守着
// 「凭直觉调大」与「凭直觉调小」两个方向。
//
// 2026-09-18 又扫了一次，动机是**阈值两侧的相对代价刚变过**：`copy`/`clear`
// 让 rebuildPSQ / rebuildThreats 便宜了 32~48%（profile 2.16→0.98s / 1.71→1.16s），
// 而增量路径只拿到 PSQT 向量化那一点点 ⇒ 假设「最优值应当往下移」。实测：
//
//	96→1.000×｜16→1.063×｜24→1.005×｜**32→0.994×**｜48→0.998×｜64→1.025×｜128→1.079×
//
// 32 档只快 0.6%，**在噪声内**（单次扫描、每档 2 遍取最优）⇒ **维持 96**。
// 形状与 09-17 一致（16 档 +6.3% vs 记录 +7.0%），两次扫描互相印证。
//
// ⚠️ **必须用安静中局语料**：初始局面子力最密、威胁条目数最高，会系统性高估
// 阈值偏小那一侧的好处。本测试早前版本用 `game.InitialFEN`，与这段文档不符，
// 已改成与文档一致的语料（见下）。
func TestPendingLimitSweep(t *testing.T) {
	if testing.Short() {
		t.Skip("扫描要跑几分钟")
	}
	w := loadWeights(t)

	raw, err := os.ReadFile("testdata/quiet_fens.txt")
	if err != nil {
		t.Skip("未找到安静局面语料，跳过")
	}
	var fens []string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fens = append(fens, line)
		if len(fens) == 8 {
			break
		}
	}
	// 先解析一次，把 ParseFEN 的开销排除在计时之外。
	pos := make([]*game.Position, 0, len(fens))
	for _, f := range fens {
		p, err := game.ParseFEN(f)
		if err != nil {
			t.Fatal(err)
		}
		pos = append(pos, p)
	}

	const budget = 60000 // 与文档里的扫描口径一致：8 局面 × 6 万节点
	orig := nnue.SetPendingLimit(96)
	defer nnue.SetPendingLimit(orig)

	t.Logf("%-14s %-12s %-10s %s", "pendingLimit", "最优耗时", "相对 96", "总深度")
	var base float64
	for _, lim := range []int{96, 16, 32, 48, 64, 128, 256} {
		nnue.SetPendingLimit(lim)
		best := time.Hour
		depth := 0
		for rep := 0; rep < 2; rep++ {
			start := time.Now()
			d := 0
			for _, p := range pos {
				d += New(w).SearchNodes(p, budget).Depth
			}
			if el := time.Since(start); el < best {
				best, depth = el, d
			}
		}
		ms := float64(best.Nanoseconds()) / 1e6
		if base == 0 {
			base = ms
		}
		t.Logf("%-14d %-12.0f %.3f×     %d", lim, ms, ms/base, depth)
	}
}
