package game

import (
	"math/rand"
	"testing"
)

// TestSeeGEBasicExchanges 用能手算清的交换验证 SEE。
//
// FEN 行序：rows[0] 是 rank 9（黑方底线），rows[9] 是 rank 0。
// 坐标 sq90 = rank*9 + file。
func TestSeeGEBasicExchanges(t *testing.T) {
	cases := []struct {
		name     string
		fen      string
		from, to int
		want     bool
		why      string
	}{
		{
			name: "车吃无保护的兵",
			fen:  "4k4/9/9/9/4p4/4R4/9/9/9/4K4 w - - 0 1",
			from: 40, to: 49, want: true,
			why: "兵值 100 且无人保护，车吃完不会被吃回",
		},
		{
			name: "车吃被车保护的兵",
			fen:  "4k4/9/9/9/4p4/4R4/9/9/4r4/4K4 w - - 0 1",
			from: 40, to: 49, want: false,
			why: "吃兵得 100，但黑车沿同列吃回（净亏 800）",
		},
		{
			name: "车换车且无人吃回",
			fen:  "4k4/9/9/9/4r4/4R4/9/9/9/4K4 w - - 0 1",
			from: 40, to: 49, want: true,
			why: "等值交换，不亏",
		},
		{
			name: "车换车但被兵吃回",
			fen:  "4k4/9/9/4p4/4r4/4R4/9/9/9/4K4 w - - 0 1",
			from: 40, to: 49, want: true,
			why: "车换车后黑兵吃回，净 0，仍不算亏",
		},
		{
			name: "炮翻山吃兵后被兵吃回",
			fen:  "4k4/9/4C4/4p4/4p4/9/9/9/9/4K4 w - - 0 1",
			from: 67, to: 49, want: false,
			why: "炮值 450 只换到兵 100，被兵吃回后亏 350",
		},
	}

	for _, c := range cases {
		p, err := ParseFEN(c.fen)
		if err != nil {
			t.Errorf("%s：解析 FEN 失败: %v", c.name, err)
			continue
		}
		if p.PieceAt90(c.to) == Empty {
			t.Errorf("%s：目标格 %d 是空的，用例写错了", c.name, c.to)
			continue
		}
		if got := p.SeeGE(c.from, c.to, 0); got != c.want {
			t.Errorf("%s：SeeGE(%d,%d,0) = %v，应为 %v —— %s",
				c.name, c.from, c.to, got, c.want, c.why)
		}
	}
}

// TestSeeGEThreshold 门槛语义：threshold 抬高后，原本成立的吃子会变得不划算。
func TestSeeGEThreshold(t *testing.T) {
	// 车吃无保护的兵，净得 100。
	p, err := ParseFEN("4k4/9/9/9/4p4/4R4/9/9/9/4K4 w - - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	if !p.SeeGE(40, 49, 0) {
		t.Error("门槛 0 时应判为不亏")
	}
	if !p.SeeGE(40, 49, 100) {
		t.Error("门槛 100 时净得恰好 100，应判为不亏")
	}
	if p.SeeGE(40, 49, 101) {
		t.Error("门槛 101 时净得 100 不够，应判为亏")
	}
}

// TestSeeGEQuietMove 覆盖「安静着法」（不吃子的走子）——此前的用例全是吃子。
//
// 为什么必须单独覆盖：静的着法的 SEE 剪枝是 SEE 的主要用法之一，而它的语义与
// 吃子不同 —— 第一层「被吃子价值」为 0，全靠后续的交换链判断。而且它的
// 调用时机有个极易踩的坑：SeeGE 读的是 from/to 两格**当前**的子，必须在落子
// 之前调用；落子后 from 已空、to 上站着自己的子，「第二层捷径」会恒成立，
// 于是剪枝一次都不会触发（实测 8.7 万次调用剪掉 0 次，节点数逐位不变）。
//
// 布局：红车在 (file1,rank0)、黑兵在 (file1,rank3)。车走到 (file1,rank2) 会被
// 兵吃掉且无法吃回 —— 净亏一个车（900）。
func TestSeeGEQuietMove(t *testing.T) {
	p, err := ParseFEN("4k4/9/9/9/9/9/1p7/9/9/1R1K5 w - - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	const rookFrom, hangTo, safeTo = 1, 19, 0

	if p.SeeGE(rookFrom, hangTo, 0) {
		t.Error("车走到被兵看住的格：净亏一个车，门槛 0 时应判为亏")
	}
	if !p.SeeGE(rookFrom, hangTo, -1000) {
		t.Error("门槛 -1000 时亏损 900 在预算内，应判为不亏")
	}
	if !p.SeeGE(rookFrom, safeTo, 0) {
		t.Error("车走到无子看住的空格：无人能吃，SEE = 0，应判为不亏")
	}
}

// TestSeeGEDefensive 边界：越界坐标一律返回 false，不应 panic。
func TestSeeGEDefensive(t *testing.T) {
	p, err := ParseFEN(InitialFEN)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range [][2]int{{-1, 40}, {40, -1}, {90, 40}, {40, 90}} {
		if p.SeeGE(c[0], c[1], 0) {
			t.Errorf("越界坐标 (%d,%d) 应返回 false", c[0], c[1])
		}
	}
}

// TestAttackersToBasic 攻击者查询：同列的滑子要能找到，够不着的兵不能误报。
func TestAttackersToBasic(t *testing.T) {
	// 红车在 (4,4)=40；黑车在 (4,5)=49 与它同列相邻；黑兵在 (1,1)=10 在另一列。
	p, err := ParseFEN("4k4/9/9/9/4r4/4R4/9/9/1p7/4K4 w - - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	const target = 40
	if p.PieceAt90(target) == Empty {
		t.Fatal("用例写错了：目标格 40 应是红车")
	}
	got := p.bb.attackersTo(target, p.bb.occ)
	if !got.Test(49) {
		t.Errorf("黑车 49 与 40 同列相邻，应算作攻击者；实际集合 %v", got)
	}
	if got.Test(10) {
		t.Errorf("黑兵 10 在另一列，不该出现在攻击者里")
	}
}

// TestAttackersToElephantMatchesMovegen 用走法生成交叉验证 attackersTo 的象分支。
//
// attackersTo 是按「能攻击 sq 的子」**反向**查预计算表的，而几何表是按
// 「起点 → 落点」**正向**构建的 —— 这个方向差正是 bug 的温床：buildElephant
// 只判落点不过河，于是被摆到对岸的象也会在 elephantSteps 里带出己方半场的
// 落点，反向查就得到「红象攻击黑方半场」的假攻击者（实测 8/1741 处与朴素
// SEE 分歧）。
//
// 这里拿 GenMoves 当独立裁判：某方的象能走到 sq，才允许被判为该格攻击者。
// 注意跨版本对拍（TestGenMovesMatchesReference）发现不了这个 bug —— 走法生成
// 只从象的真实位置正向查表，那条路径一直是对的，只有反向消费才出错。
func TestAttackersToElephantMatchesMovegen(t *testing.T) {
	rng := rand.New(rand.NewSource(0x51DE))
	positions := []*Position{NewPosition()}
	for i := 0; i < 400; i++ {
		positions = append(positions, scatter(rng, 1+rng.Intn(32)))
	}
	for i := 0; i < 300; i++ {
		positions = append(positions, randomWalk(rng, 1+rng.Intn(60)))
	}

	for _, p := range positions {
		for _, side := range []int{Red, Black} {
			// want[t] = 能走到 t 的己方象。GenMoves 不允许落在己方子占据的格上，
			// 所以这类 t 不参与比较（attackersTo 只回答「谁能攻击该格」，
			// 不区分该格上站的是谁）。
			var want [bbSquares]Bitboard
			for _, m := range p.GenMoves(side) {
				if TypeOf(p.Board[m.From]) == Elephant {
					want[m.To].Set(int(m.From))
				}
			}
			for sq := 0; sq < bbSquares; sq++ {
				if p.bb.byColor[side>>3].Test(sq) {
					continue
				}
				got := p.bb.attackersTo(sq, p.bb.occ).
					And(p.bb.byType[Elephant]).
					And(p.bb.byColor[side>>3])
				if got != want[sq] {
					t.Fatalf("局面 %s\n  side=%d sq=%d 象攻击者不符：attackersTo=%v，走法生成=%v",
						p.FEN(), side, sq, got, want[sq])
				}
			}
		}
	}
}
