package nnue

// 本文件实现 HalfKAv2_hm 与 FullThreats 的特征索引，
// 对应 Pikafish-2026-01-02 的 half_ka_v2_hm.cpp 与 full_threats.cpp 的 make_index 系列。
//
// 为了不与 internal/game 耦合，这里只通过 Board 数组交换局面信息。

// Board 是本包需要的局面视图：90 格，每格放皮的棋子编码（0 表示空）。
// 坐标与 internal/game 完全一致（sq = rank*9 + file，rank 0 为红方底线）。
type Board [squareNB]int

// gameTypeToPikafish 把 internal/game 的棋子类型映射到皮的编码。
// 两套编码的类型顺序不同：项目 King=1,Advisor=2,Elephant=3,Horse=4,Rook=5,Cannon=6,Pawn=7，
// 皮 ROOK=1,ADVISOR=2,CANNON=3,PAWN=4,KNIGHT=5,BISHOP=6,KING=7。
var gameTypeToPikafish = [8]int{
	0, ptKing, ptAdvisor, ptBishop, ptKnight, ptRook, ptCannon, ptPawn,
}

// PieceFromGame 把 internal/game 的棋子字节转成皮的编码；空或哨兵返回 0。
func PieceFromGame(p byte) int {
	if p == 0x00 || p == 0xFF {
		return 0
	}
	c := colorWhite
	if p&0x08 != 0 {
		c = colorBlack
	}
	return makePiece(c, gameTypeToPikafish[p&0x07])
}

// BoardFromGame 由 internal/game 的 90 格棋盘构造视图。
func BoardFromGame(squares [squareNB]byte) Board {
	var b Board
	for s := 0; s < squareNB; s++ {
		b[s] = PieceFromGame(squares[s])
	}
	return b
}

// occupied 返回所有有子的格子。
func (b Board) occupied() bitboard {
	var occ bitboard
	for s := 0; s < squareNB; s++ {
		if b[s] != 0 {
			occ.set(s)
		}
	}
	return occ
}

// kingSquare 返回某方的帅/将所在格；不存在返回 -1。
func (b Board) kingSquare(c int) int {
	target := makePiece(c, ptKing)
	for s := 0; s < squareNB; s++ {
		if b[s] == target {
			return s
		}
	}
	return -1
}

// countPiece 统计某方某类棋子的数量。
func (b Board) countPiece(c, pt int) int {
	target := makePiece(c, pt)
	n := 0
	for s := 0; s < squareNB; s++ {
		if b[s] == target {
			n++
		}
	}
	return n
}

// midEncoding 对应 Position::midEncoding：把某方所有棋子的中线平衡编码相加。
// C++ 侧是随走子增量维护的，这里直接全量和，结果相同（uint64 回绕加法）。
func (b Board) midEncoding(c int) uint64 {
	var enc uint64
	for s := 0; s < squareNB; s++ {
		if pc := b[s]; pc != 0 && pieceColor(pc) == c {
			enc += midMirrorEncoding[pc][s]
		}
	}
	return enc
}

// requiresMidMirror 对应 HalfKAv2_hm::requires_mid_mirror。
func (b Board) requiresMidMirror(c int) bool {
	me, opp := b.midEncoding(c), b.midEncoding(c^1)
	if me&(1<<63) == 0 || opp&(1<<63) == 0 {
		return false
	}
	return me < balanceEncoding || (me == balanceEncoding && opp < balanceEncoding)
}

// attackBucket 对应 HalfKAv2_hm::make_attack_bucket。
// 桶值只看「是否有车」与「是否有马或炮」，不看具体数量。
func (b Board) attackBucket(c int) int {
	r := b.countPiece(c, ptRook)
	n := b.countPiece(c, ptKnight)
	cn := b.countPiece(c, ptCannon)
	v := 0
	if r > 0 {
		v += 2
	}
	if n+cn > 0 {
		v++
	}
	return v
}

// FeatureBucket 对应 HalfKAv2_hm::make_feature_bucket，返回
// (特征桶号, 是否水平镜像)。桶号为 king_bucket*4 + attack_bucket。
//
// 这里刻意用一次全盘遍历同时收集将位、中线编码与子力计数：按对应的 C++ 函数
// 逐个调用（kingSquare×2 + midEncoding×2 + countPiece×3）要扫 7 遍，
// 而特征枚举每次评估都要跑，实测是主要开销之一。
func (b Board) FeatureBucket(perspective int) (bucket int, mirror bool) {
	var me, opp uint64
	ksq, oksq := -1, -1
	var rooks, knightCannon [colorNB]int

	for s := 0; s < squareNB; s++ {
		pc := b[s]
		if pc == 0 {
			continue
		}
		c := pieceColor(pc)
		switch pieceType(pc) {
		case ptKing:
			if c == perspective {
				ksq = s
			} else {
				oksq = s
			}
		case ptRook:
			rooks[c]++
		case ptKnight, ptCannon:
			knightCannon[c]++
		}
		if c == perspective {
			me += midMirrorEncoding[pc][s]
		} else {
			opp += midMirrorEncoding[pc][s]
		}
	}
	if ksq < 0 || oksq < 0 {
		return 0, false
	}

	need := me&(1<<63) != 0 && opp&(1<<63) != 0 &&
		(me < balanceEncoding || (me == balanceEncoding && opp < balanceEncoding))
	slot := kingBuckets[ksq][oksq][boolInt(need)]

	ab := 0
	if rooks[perspective] > 0 {
		ab += 2
	}
	if knightCannon[perspective] > 0 {
		ab++
	}
	return int(slot.bucket)*4 + ab, slot.mirror
}

// LayerStackBucket 对应 HalfKAv2_hm::make_layer_stack_bucket，取值 0..15。
// 由双方的车数与马炮总数决定；us 为走子方。
func (b Board) LayerStackBucket(us int) int {
	usRook, oppRook := b.countPiece(us, ptRook), b.countPiece(us^1, ptRook)
	usNC := b.countPiece(us, ptKnight) + b.countPiece(us, ptCannon)
	oppNC := b.countPiece(us^1, ptKnight) + b.countPiece(us^1, ptCannon)
	return layerStackBucketValue(usRook, oppRook, usNC, oppNC)
}

// layerStackBucketValue 重建 LayerStackBuckets 表。
func layerStackBucketValue(usRook, oppRook, usNC, oppNC int) int {
	switch {
	case usRook == oppRook:
		v := usRook * 4
		if usNC+oppNC >= 4 {
			v += 2
		}
		if usNC == oppNC {
			v++
		}
		return v
	case usRook == 2 && oppRook == 1:
		return 12
	case usRook == 1 && oppRook == 2:
		return 13
	case usRook > 0 && oppRook == 0:
		return 14
	default:
		return 15
	}
}

// PSQIndex 对应 HalfKAv2_hm::make_index。
func PSQIndex(perspective, s, pc, bucket int, mirror bool) int {
	s = indexMap[boolInt(mirror)][boolInt(perspective == colorBlack)][s]
	if perspective == colorBlack {
		pc = flipPiece(pc)
	}
	return psqOffsets[pc][s] + psqCount*bucket
}

// ThreatIndex 对应 FullThreats::make_index。
// 无效组合返回 ThreatInputs（即 Dimensions），调用方应据此过滤。
func ThreatIndex(perspective, attacker, from, to, attacked int, mirror bool) int {
	mi, rot := boolInt(mirror), boolInt(perspective == colorBlack)
	from = indexMap[mi][rot][from]
	to = indexMap[mi][rot][to]
	if perspective == colorBlack {
		attacker, attacked = flipPiece(attacker), flipPiece(attacked)
	}
	return int(threatOffsets[attacker][from][to][attacked])
}

// attacksBB 对应 bitboard.h 的 attacks_bb(pt, s, occupied)。
func attacksBB(pt, s int, occupied bitboard) bitboard {
	switch pt {
	case ptRook:
		return slidingAttack(ptRook, s, occupied)
	case ptCannon:
		return slidingAttack(ptCannon, s, occupied)
	case ptBishop:
		return lameLeaperAttack(ptBishop, s, occupied)
	case ptKnight:
		return lameLeaperAttack(ptKnight, s, occupied)
	default:
		return pseudoAttacks[pt][s]
	}
}

// ActivePSQ 返回某视角下所有激活的 PSQ 特征索引。
// PSQ 特征集是「每个棋子一个特征」，所以数量等于场上棋子数。
func (b Board) ActivePSQ(perspective int) []int {
	bucket, mirror := b.FeatureBucket(perspective)
	out := make([]int, 0, 32)
	for s := 0; s < squareNB; s++ {
		if pc := b[s]; pc != 0 {
			out = append(out, PSQIndex(perspective, s, pc, bucket, mirror))
		}
	}
	return out
}

// ActiveThreats 返回某视角下所有激活的威胁特征索引。
// 对应 FullThreats::append_active_indices。
func (b Board) ActiveThreats(perspective int) []int {
	_, mirror := b.FeatureBucket(perspective)
	occupied := b.occupied()
	out := make([]int, 0, 64)

	for bb := occupied; !bb.isEmpty(); {
		from := bb.popLSB()
		attacker := b[from]
		pt, c := pieceType(attacker), pieceColor(attacker)

		var attacks bitboard
		if pt == ptPawn {
			attacks = pseudoAttacks[pawnSlot(c)][from]
		} else {
			attacks = attacksBB(pt, from, occupied)
		}
		attacks = attacks.and(occupied)

		for !attacks.isEmpty() {
			to := attacks.popLSB()
			idx := ThreatIndex(perspective, attacker, from, to, b[to], mirror)
			if idx < ThreatInputs {
				out = append(out, idx)
			}
		}
	}
	return out
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
