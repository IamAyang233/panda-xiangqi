package nnue

import "testing"

// TestFeatureTableSizes 是特征表构建正确性的核心判据。
//
// 两张偏移表都是按源码顺序逐项累加分配的，最后一个偏移 +1 就是表的总维度。
// 掩码定义、棋子枚举顺序、攻击生成、过滤条件里任何一处出错，这两个数字都会对不上：
//   - PSQOffsets 的有效 (棋子, 格子) 组合数必须等于 HalfKAv2_hm::PS_NB = 689
//   - ThreatOffsets 的分配总数必须等于 FullThreats::Dimensions = 45649
//
// 这两个值与权重文件里的架构常量独立对应，所以是端到端的强校验。
func TestFeatureTableSizes(t *testing.T) {
	if psqCount != 689 {
		t.Errorf("PSQOffsets 有效组合数 = %d，应为 PS_NB = 689", psqCount)
	}
	if threatCount != ThreatInputs {
		t.Errorf("ThreatOffsets 分配总数 = %d，应为 FullThreats::Dimensions = %d",
			threatCount, ThreatInputs)
	}
}

// TestValidBB 逐个棋子核对有效格数。总数 689 等于这些数字之和。
func TestValidBB(t *testing.T) {
	want := map[int]int{
		makePiece(colorWhite, ptRook):    90,
		makePiece(colorWhite, ptAdvisor): 5,
		makePiece(colorWhite, ptCannon):  90,
		makePiece(colorWhite, ptPawn):    55,
		makePiece(colorWhite, ptKnight):  90,
		makePiece(colorWhite, ptBishop):  7,
		makePiece(colorWhite, ptKing):    6,
		makePiece(colorBlack, ptRook):    90,
		makePiece(colorBlack, ptAdvisor): 5,
		makePiece(colorBlack, ptCannon):  90,
		makePiece(colorBlack, ptPawn):    55,
		makePiece(colorBlack, ptKnight):  90,
		makePiece(colorBlack, ptBishop):  7,
		makePiece(colorBlack, ptKing):    9,
	}
	sum := 0
	for pc, n := range want {
		if got := validBB[pc].count(); got != n {
			t.Errorf("validBB[%d] 有效格 = %d，应为 %d", pc, got, n)
		}
		sum += n
	}
	if sum != 689 {
		t.Errorf("有效格总数 = %d，应为 689", sum)
	}
}

// TestMasks 校验几个关键掩码的几何含义。
func TestMasks(t *testing.T) {
	if got := palaceBB.count(); got != 18 {
		t.Errorf("九宫格数 = %d，应为 18", got)
	}
	if got := halfBB[colorWhite].count(); got != 45 {
		t.Errorf("红方半场格数 = %d，应为 45", got)
	}
	if got := pawnFileBB.count(); got != 50 {
		t.Errorf("兵列格数 = %d，应为 50", got)
	}
	// 九宫必须落在 d/e/f 列。
	for s := 0; s < squareNB; s++ {
		if palaceBB.test(s) && (fileOf(s) < 3 || fileOf(s) > 5) {
			t.Fatalf("格子 %d (file=%d) 不属于九宫", s, fileOf(s))
		}
	}
}

// TestAttackGeneration 抽查几个攻击集，确认边界处理没有跨列。
func TestAttackGeneration(t *testing.T) {
	// 车在角落 A0 沿四条射线：A 列向上 9 格 + 底行 8 格。
	got := pseudoAttacks[ptRook][sqOf(0, 0)].count()
	if got != 17 {
		t.Errorf("角落车的空盘攻击数 = %d，应为 17", got)
	}
	// 马在棋盘中央应有 8 个落点。
	if got := pseudoAttacks[ptKnight][sqOf(4, 4)].count(); got != 8 {
		t.Errorf("中央马的空盘攻击数 = %d，应为 8", got)
	}
	// 马在角落只有 2 个落点。
	if got := pseudoAttacks[ptKnight][sqOf(0, 0)].count(); got != 2 {
		t.Errorf("角落马的空盘攻击数 = %d，应为 2", got)
	}
	// 红兵未过河只能前进一格。
	if got := pseudoAttacks[paPawnWhite][sqOf(4, 3)].count(); got != 1 {
		t.Errorf("未过河红兵走法数 = %d，应为 1", got)
	}
	// 红兵过河后可直走或横走，最多 3 个。
	if got := pseudoAttacks[paPawnWhite][sqOf(4, 5)].count(); got != 3 {
		t.Errorf("过河红兵走法数 = %d，应为 3", got)
	}
	// 中线的红兵过河后只有直走 + 左右横走（不越界）。
	if got := pseudoAttacks[paPawnWhite][sqOf(0, 5)].count(); got != 2 {
		t.Errorf("过河边线红兵走法数 = %d，应为 2", got)
	}
}

// TestIndexMap 校验镜像映射：翻列翻行都是对合，且列方向不混行。
func TestIndexMap(t *testing.T) {
	for s := 0; s < squareNB; s++ {
		if got := indexMap[1][0][s]; got != flipFileSq(s) {
			t.Fatalf("翻列映射错误 sq=%d", s)
		}
		if got := indexMap[0][1][s]; got != flipRankSq(s) {
			t.Fatalf("翻行映射错误 sq=%d", s)
		}
		// 对合性
		if indexMap[1][0][indexMap[1][0][s]] != s {
			t.Fatalf("翻列不是对合 sq=%d", s)
		}
	}
	if indexMap[1][0][sqOf(0, 0)] != sqOf(8, 0) {
		t.Error("A0 翻列应为 I0")
	}
	if indexMap[0][1][sqOf(0, 0)] != sqOf(0, 9) {
		t.Error("A0 翻行应为 A9")
	}
}

// TestKingBuckets 校验 king bucket 落在 0..5，且中线列标记镜像。
func TestKingBuckets(t *testing.T) {
	for ksq := 0; ksq < squareNB; ksq++ {
		for oksq := 0; oksq < squareNB; oksq++ {
			for midm := 0; midm <= 1; midm++ {
				slot := kingBuckets[ksq][oksq][midm]
				if slot.bucket > 5 {
					t.Fatalf("king bucket 越界: %d", slot.bucket)
				}
			}
		}
	}
	// 红帅在 e0（file 4，中线）且黑将在 e9 时不应镜像。
	if kingBuckets[sqOf(4, 0)][sqOf(4, 9)][0].mirror {
		t.Error("双方都在中线列时不应镜像")
	}
	// 红帅在 f0（file 5）时必定镜像。
	if !kingBuckets[sqOf(5, 0)][sqOf(4, 9)][0].mirror {
		t.Error("帅在 f 列时必须镜像")
	}
}
