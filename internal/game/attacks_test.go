package game

import (
	"math/rand"
	"testing"
)

// 本文件给 rookAttacks / cannonAttacks 补上直接的正确性守卫与基准。
//
// 这两个函数此前只有间接覆盖（走子生成 → perft、SEE → see_ref_test），
// 而它们恰好是「用位运算取段」这类改写的高危区：改错了不报错，只表现为
// 走子集少几个格、或 SEE 交换链算歪。本轮试着把「找第一个阻挡」改成无分支
// 查表（实测更慢、已回退）时，正是靠下面的对拍确认了语义无误。
//
// isAttackedBB 不在这里加参考实现：bitboard_test.go 的
// TestAttackMatchesReference 已经拿 mailbox 版当裁判跑了 10 万次随机摆放
// 与 2000 局随机对局，比抄一份旧代码更强。

// ---- 参考实现：显然正确的分支版，充当 rookAttacks / cannonAttacks 的规格 ----

func firstBlockerRefAt(occ Bitboard, sq, dir int) int {
	m := rayMask[sq][dir].And(occ)
	if m.IsEmpty() {
		return -1
	}
	if dir == dirUp || dir == dirRight {
		return m.lsb()
	}
	return m.msb()
}

func rookAttacksRef(sq int, occ, own Bitboard) Bitboard {
	var out Bitboard
	for d := 0; d < 4; d++ {
		fb := firstBlockerRefAt(occ, sq, d)
		if fb < 0 {
			out = out.Or(rayMask[sq][d])
			continue
		}
		seg := rayMask[sq][d].AndNot(rayMask[fb][d])
		seg.Clear(fb)
		out = out.Or(seg)
		if !own.Test(fb) {
			out.Set(fb)
		}
	}
	return out
}

func cannonAttacksRef(sq int, occ, own Bitboard) (quiet, capture Bitboard) {
	for d := 0; d < 4; d++ {
		fb := firstBlockerRefAt(occ, sq, d)
		if fb < 0 {
			quiet = quiet.Or(rayMask[sq][d])
			continue
		}
		seg := rayMask[sq][d].AndNot(rayMask[fb][d])
		seg.Clear(fb)
		quiet = quiet.Or(seg)
		if fb2 := firstBlockerRefAt(occ, fb, d); fb2 >= 0 && !own.Test(fb2) {
			capture.Set(fb2)
		}
	}
	return quiet, capture
}

func eqBB(a, b Bitboard) bool { return a[0] == b[0] && a[1] == b[1] }

func randBB(rng *rand.Rand, n int) Bitboard {
	var b Bitboard
	for i := 0; i < n; i++ {
		b.Set(rng.Intn(bbSquares))
	}
	return b
}

// TestRookCannonAttacksMatchRef 与分支版参考实现逐位对拍。
//
// 覆盖三类输入：
//   - 空盘，以及整条射线填满（车/炮要正好停在最后一个子之前）；
//   - 随机占用集 × 随机 own —— own 与 occ **故意不一致**（own 只是 occ 的任意子集），
//     因为 see.go 里就有 `rookAttacks(sq, occ, Bitboard{})` 这种「所有子都当敌方」
//     的调用，而 movegen 传的是真正的己方子集；
//   - 真实可达局面（随机对局），occ 取全场占用、own 取某一方子力。
func TestRookCannonAttacksMatchRef(t *testing.T) {
	rng := rand.New(rand.NewSource(0x20260916))

	check := func(occ, own Bitboard, tag string) {
		t.Helper()
		for sq := 0; sq < bbSquares; sq++ {
			if got, want := rookAttacks(sq, occ, own), rookAttacksRef(sq, occ, own); !eqBB(got, want) {
				t.Fatalf("%s：rookAttacks 格 %s 不一致\n占用=%v 己方=%v",
					tag, SquareName(uint8(sq)), occ, own)
			}
			gq, gc := cannonAttacks(sq, occ, own)
			wq, wc := cannonAttacksRef(sq, occ, own)
			if !eqBB(gq, wq) || !eqBB(gc, wc) {
				t.Fatalf("%s：cannonAttacks 格 %s 不一致\n占用=%v 己方=%v",
					tag, SquareName(uint8(sq)), occ, own)
			}
		}
	}

	check(Bitboard{}, Bitboard{}, "空盘")
	check(Bitboard{}, randBB(rng, 20), "空占用但有己方子（畸形输入）")
	check(rayMask[bbSquare(4, 0)][dirUp], Bitboard{}, "整条纵线填满")
	check(rayMask[bbSquare(0, 4)][dirRight], Bitboard{}, "整条横线填满")

	for i := 0; i < 20000; i++ {
		occ := randBB(rng, 1+rng.Intn(40))
		own := occ.And(randBB(rng, 1+rng.Intn(20)))
		check(occ, own, "随机摆放")
	}
	for i := 0; i < 300; i++ {
		p := randomWalk(rng, 1+rng.Intn(60))
		check(p.bb.occ, p.bb.byColor[0], "红方视角")
		check(p.bb.occ, p.bb.byColor[1], "黑方视角")
	}
}

// ---- 基准 ----
//
// 入参每轮都换（Varying）：真实搜索里 (sq, occ) 变化极剧烈，分支预测器学不到规律，
// 固定入参的基准会低估分支成本（见 ENGINE-PITFALLS「Fixed 与 Varying 要分开看」）。
// 这三个基准是「再有人想重写这块」时的现成量尺 —— 端到端固定节点搜索的跨轮漂移有
// 5%，判不出 5% 量级的差异；路径级微基准的噪声只有 ±0.5%。

type benchCase struct {
	sq       int
	occ, own Bitboard
}

func benchCases(rng *rand.Rand, n int) []benchCase {
	cs := make([]benchCase, 0, n)
	for i := 0; i < n; i++ {
		occ := randBB(rng, 1+rng.Intn(40))
		cs = append(cs, benchCase{rng.Intn(bbSquares), occ, occ.And(randBB(rng, 1+rng.Intn(20)))})
	}
	return cs
}

// benchSink 是包级 sink：常量入参 + 结果未用 + 无副作用的调用会被编译器整体删掉。
var benchSink Bitboard

func BenchmarkRookAttacks(b *testing.B) {
	cs := benchCases(rand.New(rand.NewSource(7)), 512)
	var sink Bitboard
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c := cs[i%len(cs)]
		sink = sink.Xor(rookAttacks(c.sq, c.occ, c.own))
	}
	benchSink = sink
}

func BenchmarkCannonAttacks(b *testing.B) {
	cs := benchCases(rand.New(rand.NewSource(7)), 512)
	var sink Bitboard
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c := cs[i%len(cs)]
		q, cap := cannonAttacks(c.sq, c.occ, c.own)
		sink = sink.Xor(q).Xor(cap)
	}
	benchSink = sink
}

func BenchmarkIsAttackedBB(b *testing.B) {
	p := randomWalk(rand.New(rand.NewSource(11)), 40)
	b.ResetTimer()
	n := 0
	for i := 0; i < b.N; i++ {
		if p.bb.isAttackedBB((i*7)%bbSquares, i&1) {
			n++
		}
	}
	if n == 0 {
		b.Log("全程未命中（纯否分支，与真实搜索的混合情形不同）")
	}
}
