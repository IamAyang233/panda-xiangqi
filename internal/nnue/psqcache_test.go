package nnue

import (
	"math/rand"
	"testing"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
)

// TestPSQCacheMatchesRebuild PSQ 缓存路径必须与全量重建逐位相同。
//
// 缓存把「桶/镜像变化 → 31.6 行整表重建」换成「拷回缓存 + 补棋盘差集」。
// 加法是环绕加、与顺序无关，所以两者**应当**逐位相同；但「应当」不是保证 ——
// 缓存项与 (棋盘, 桶, 镜像) 三者错配任何一处，评估值都会静默偏移，
// 进而让搜索选错着法而**没有任何报错**。所以必须逐局面逐元素对拍。
func TestPSQCacheMatchesRebuild(t *testing.T) {
	if psqCacheOff {
		t.Skip("QIJING_PSQCACHE=off，缓存已关闭")
	}
	w, err := Load(flatPath)
	if err != nil {
		t.Skip("未找到展开后的权重，跳过")
	}

	cases := []struct {
		name  string
		fen   string
		seed  int64
		steps int
	}{
		{"初始局面", game.InitialFEN, 0x5A17, 500},
		{"中局（子力互缠）", "2bak1b2/4a4/4b4/p1p1p3p/6p2/2P6/P3P1P1P/1C2C4/9/RNBAKABNR w - - 0 1", 0x9E37, 500},
		{"残局（少子，桶随子力走）", "3ak4/4a4/4b4/9/9/9/4P4/9/4R4/4K4 w - - 0 1", 0x2545, 500},
	}

	ResetDiag()
	EnableDiag(true)
	defer EnableDiag(false)
	startHit, startMiss := DiagSnapshot().PSQCacheHit, DiagSnapshot().PSQCacheMiss

	total := 0
	for _, c := range cases {
		gp, perr := game.ParseFEN(c.fen)
		if perr != nil {
			t.Fatalf("%s: %v", c.name, perr)
		}
		rng := rand.New(rand.NewSource(c.seed))
		var inc Accumulator
		pos := &Position{}
		pos.ResetFromGame(&gp.Board, gp.Turn>>3)

		done := 0
		for i := 0; i < c.steps; i++ {
			moves := gp.LegalMoves(gp.Turn)
			if len(moves) == 0 {
				break
			}
			m := moves[rng.Intn(len(moves))]
			gp.Make(m)
			pos.Make(int(m.From), int(m.To))

			w.Apply(pos, &inc)
			var full Accumulator
			w.RefreshFromPosition(pos, &full)
			if !accumEqual(&inc, &full) {
				t.Fatalf("%s：第 %d 步缓存路径与全量重建不一致\nFEN: %s\n第 %d 步的 FEN 需从初始 FEN 重放",
					c.name, i+1, c.fen, i+1)
			}
			done++
			total++
		}
		t.Logf("%-22s 连续 %d 步逐位一致 ✓", c.name, done)
	}

	// 反向验证：缓存必须真的被走到（否则这条守卫什么都没测到）。
	st := DiagSnapshot()
	hits, misses := st.PSQCacheHit-startHit, st.PSQCacheMiss-startMiss
	if hits == 0 {
		t.Fatal("整个游走里缓存一次都没命中，这条守卫是空的")
	}
	t.Logf("合计 %d 个局面逐位一致 ✓ ｜ 缓存命中 %d 次、未命中 %d 次（命中率 %.1f%%）",
		total, hits, misses, 100*float64(hits)/float64(hits+misses))
}

// TestPSQCacheBucketFlip 专测「桶来回翻转」这一最危险的情形。
//
// 搜索里最常见的模式是「在同一个结点上试若干走法，每个走完就回退」——
// 走将时桶会翻过去再翻回来，正是缓存要处理的路径。这里显式构造：
// 走将 → 评估对拍 → 回退 → 评估对拍，反复若干轮。
func TestPSQCacheBucketFlip(t *testing.T) {
	if psqCacheOff {
		t.Skip("QIJING_PSQCACHE=off，缓存已关闭")
	}
	w, err := Load(flatPath)
	if err != nil {
		t.Skip("未找到展开后的权重，跳过")
	}
	gp, perr := game.ParseFEN("2bak1b2/4a4/4b4/p1p1p3p/6p2/2P6/P3P1P1P/1C2C4/9/RNBAKABNR w - - 0 1")
	if perr != nil {
		t.Fatal(perr)
	}
	var inc Accumulator
	pos := &Position{}
	pos.ResetFromGame(&gp.Board, gp.Turn>>3)

	check := func(what string) {
		w.Apply(pos, &inc)
		var full Accumulator
		w.RefreshFromPosition(pos, &full)
		if !accumEqual(&inc, &full) {
			t.Fatalf("%s：缓存路径与全量重建不一致", what)
		}
	}

	check("起点")
	sawFlip := 0
	prevBucket, _ := pos.FeatureBucket(0)
	for round := 0; round < 60; round++ {
		moves := gp.LegalMoves(gp.Turn)
		if len(moves) == 0 {
			break
		}
		// 优先挑「动将」的着法：只有它会改桶。
		ks := pos.kingSq[gp.Turn>>3]
		pick := -1
		for i, m := range moves {
			if int(m.From) == ks {
				pick = i
				break
			}
		}
		if pick < 0 {
			t.Skipf("该局面在第 %d 轮已无动将着法，翻转测不到（不是失败）", round)
		}
		m := moves[pick]
		gp.Make(m)
		pos.Make(int(m.From), int(m.To))
		check("走将之后")
		if b, _ := pos.FeatureBucket(0); b != prevBucket {
			sawFlip++
			prevBucket = b
		}

		gp.Unmake()
		pos.Unmake()
		check("回退之后")
		if b, _ := pos.FeatureBucket(0); b != prevBucket {
			sawFlip++
			prevBucket = b
		}
	}
	if sawFlip == 0 {
		t.Fatal("一次桶变化都没发生，这条守卫是空的")
	}
	t.Logf("桶翻转 %d 次、来回各一轮，全部与全量重建逐位一致 ✓", sawFlip)
}

// TestPSQCacheSlotCoverage 槽位数必须覆盖 FeatureBucket 的全部取值，尺寸也不能失控。
//
// 这条不变式踩过一次：槽位数原先是**包级变量初始化**里扫 kingBuckets 表算出来的，
// 而那张表是在 init() 里填的（init 晚于包级变量初始化）⇒ 表还是全零、算出 1 个桶，
// 第一次换桶就撞进长度 8 的切片越界 panic。改成惰性计算后必须钉住它。
func TestPSQCacheSlotCoverage(t *testing.T) {
	slots := psqBucketSlotCount()
	maxBucket := -1
	for ksq := range kingBuckets {
		for oksq := range kingBuckets[ksq] {
			for mm := 0; mm < 2; mm++ {
				b := int(kingBuckets[ksq][oksq][mm].bucket)*4 + 3 // attack bucket 最高 3
				if b > maxBucket {
					maxBucket = b
				}
			}
		}
	}
	if maxBucket >= slots {
		t.Fatalf("槽位不足：最大桶 %d 需要 %d 个槽，实际只有 %d", maxBucket, maxBucket+1, slots)
	}
	perPerspective := float64(slots*2) * float64(sizeofPsqCacheEntry())
	t.Logf("槽位 %d（覆盖桶 0..%d）｜ 单项 %d 字节 ⇒ 两视角合计 %.1f KB",
		slots*2, maxBucket, sizeofPsqCacheEntry(), perPerspective/1024)
	if perPerspective > 512*1024 {
		t.Fatalf("缓存尺寸失控：两视角 %.1f KB（上限 512 KB）", perPerspective/1024)
	}
}
