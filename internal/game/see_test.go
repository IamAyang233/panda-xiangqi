package game

import "testing"

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
