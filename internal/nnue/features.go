package nnue

// 本文件重建 Pikafish 输入特征集 HalfKAv2_hm（PSQ 特征）的编译期表。
// 对应 Pikafish-2026-01-02 的 src/nnue/features/half_ka_v2_hm.{h,cpp} 与 src/bitboard.{h,cpp}。
//
// 皮的坐标与棋子编码和本项目 internal/game 不同：坐标完全一致（sq = rank*9+file，
// rank 0 为红方底线），但棋子类型编码顺序不同（皮 ROOK=1..KING=7，项目 King=1..Pawn=7），
// 颜色也不同（皮 WHITE=0/BLACK=1，项目 Red=0/Black=8）。本文件一律使用皮的编码，
// 与 internal/game 交换数据时经 pieceTypeFromGame 转换。

// 棋盘与颜色（types.h）。
const (
	fileNB   = 9
	rankNB   = 10
	squareNB = 90

	colorWhite = 0
	colorBlack = 1
	colorNB    = 2
)

// 棋子类型（types.h 的 PieceType，注意与 internal/game 的顺序不同）。
const (
	ptRook    = 1
	ptAdvisor = 2
	ptCannon  = 3
	ptPawn    = 4
	ptKnight  = 5
	ptBishop  = 6
	ptKing    = 7

	// KNIGHT_TO / PAWN_TO 是「反向查询」用的额外槽位。
	ptKnightTo = 8
	ptPawnTo   = 9
	ptTypeNB   = 10

	pieceNB = 16 // 2 色 × 8（含空）
)

// 方向（types.h 的 Direction）。
const (
	dirNorth = 9
	dirSouth = -dirNorth
	dirEast  = 1
	dirWest  = -1

	dirNorthEast = dirNorth + dirEast
	dirSouthEast = dirSouth + dirEast
	dirSouthWest = dirSouth + dirWest
	dirNorthWest = dirNorth + dirWest
)

// makePiece 对应 make_piece(c, pt) = (c << 3) | pt。
func makePiece(c, pt int) int { return c<<3 | pt }

func pieceColor(pc int) int { return pc >> 3 }
func pieceType(pc int) int  { return pc & 7 }

// flipPiece 对应 C++ 的 operator~(Piece)，只翻颜色位。
func flipPiece(pc int) int { return pc ^ 8 }

func sqOf(f, r int) int { return r*fileNB + f }
func fileOf(s int) int  { return s % fileNB }
func rankOf(s int) int  { return s / fileNB }
func okSquare(s int) bool {
	return s >= 0 && s < squareNB
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// chebyshev 对应 bitboard.h 的 distance<Square>：王的步数。
func chebyshev(a, b int) int {
	df, dr := absInt(fileOf(a)-fileOf(b)), absInt(rankOf(a)-rankOf(b))
	if df > dr {
		return df
	}
	return dr
}

// flipFileSq / flipRankSq 对应 types.h 的 flip_file / flip_rank。
func flipFileSq(s int) int { return sqOf(8-fileOf(s), rankOf(s)) }
func flipRankSq(s int) int { return sqOf(fileOf(s), 9-rankOf(s)) }

// bitboard 是 90 格位棋盘，与 internal/game.Bitboard 同构。
type bitboard [2]uint64

func (b *bitboard) set(s int)   { b[s>>6] |= 1 << uint(s&63) }
func (b *bitboard) clear(s int) { b[s>>6] &^= 1 << uint(s&63) }
func (b bitboard) test(s int) bool {
	return b[s>>6]&(1<<uint(s&63)) != 0
}
func (b bitboard) and(o bitboard) bitboard    { return bitboard{b[0] & o[0], b[1] & o[1]} }
func (b bitboard) or(o bitboard) bitboard     { return bitboard{b[0] | o[0], b[1] | o[1]} }
func (b bitboard) andNot(o bitboard) bitboard { return bitboard{b[0] &^ o[0], b[1] &^ o[1]} }
func (b bitboard) xor(o bitboard) bitboard    { return bitboard{b[0] ^ o[0], b[1] ^ o[1]} }
func (b bitboard) isEmpty() bool              { return (b[0] | b[1]) == 0 }

// lsb 返回最低置位的格子，不移除；空盘返回 -1。
func (b bitboard) lsb() int {
	if b[0] != 0 {
		return trailingZeros64(b[0])
	}
	if b[1] != 0 {
		return 64 + trailingZeros64(b[1])
	}
	return -1
}
func (b bitboard) count() int { return bitsOnesCount64(b[0]) + bitsOnesCount64(b[1]) }

// msb 返回最高置位的格子，不移除；空盘返回 -1。
func (b bitboard) msb() int {
	if b[1] != 0 {
		return 64 + 63 - bitsLeadingZeros64(b[1])
	}
	if b[0] != 0 {
		return 63 - bitsLeadingZeros64(b[0])
	}
	return -1
}

// popLSB 弹出最低置位并返回其格子；空盘返回 -1。
func (b *bitboard) popLSB() int {
	for w := 0; w < 2; w++ {
		if b[w] != 0 {
			s := w*64 + trailingZeros64(b[w])
			b[w] &^= 1 << uint(s&63)
			return s
		}
	}
	return -1
}

// bbOf 由格子集合构造位棋盘。
func bbOf(squares ...int) bitboard {
	var b bitboard
	for _, s := range squares {
		b.set(s)
	}
	return b
}

// ---- 掩码（bitboard.h）----

var (
	rankBBs [rankNB]bitboard
	fileBBs [fileNB]bitboard

	// palaceBB 是双方九宫（rank 0-2 与 7-9 的 d/e/f 列）。
	palaceBB bitboard

	// halfBB[0] 为红方半场（rank 0-4），halfBB[1] 为黑方半场（rank 5-9）。
	halfBB [2]bitboard

	// pawnFileBB 是兵初始所在列（a/c/e/g/i）。
	pawnFileBB bitboard

	// pawnBB 是兵的有效格：己方半场 + 未过河时只在本列。
	pawnBB [2]bitboard

	// validBB 是每个棋子的有效格掩码，索引为皮的棋子编码。
	validBB [pieceNB]bitboard

	// rayBB[sq][di] 是从 sq 沿 rayDirs[di] 到棋盘边缘的全部格子（不含 sq）。
	// 用于把滑子攻击从「逐格步进」改成「位运算取段」。
	rayBB [squareNB][4]bitboard

	// beyondInc / beyondDec 把「找第一个阻挡」的两步合并成一次查表：
	// 下标由 incIndex / decIndex 从占用集直接编码出来（编码里含「射线全空」
	// 这一档，表项为空集），于是热路径上一个数据相关的分支都不剩。
	//
	//	beyondInc[di][k] = rayBB[k][di]    （k < 90；90~128 为空集）
	//	beyondDec[di][k] = rayBB[k-1][di]  （k ≥ 1；0 为空集）
	//
	// 两张表分开是因为两个方向的编码差 1：递减方向要用 0 表示「射线全空」，
	// 而 0 是合法的格号 0。表本身各 4×129×16B ≈ 8.2KB，只占 L1 的一小块。
	beyondInc [4][129]bitboard
	beyondDec [4][129]bitboard
)

// rayDirs 的顺序与 slidingAttack 的循环一致；北/东方向格号递增，
// 南/西方向格号递减，取「第一个阻挡」时据此选 lsb 还是 msb。
var rayDirs = [4]int{dirNorth, dirSouth, dirEast, dirWest}

func buildRayTable() {
	for sq := 0; sq < squareNB; sq++ {
		for di, d := range rayDirs {
			var b bitboard
			for t := sq + d; okSquare(t) && chebyshev(t-d, t) == 1; t += d {
				b.set(t)
			}
			rayBB[sq][di] = b
		}
	}
	for di := 0; di < 4; di++ {
		for k := 0; k < 129; k++ {
			if k < squareNB {
				beyondInc[di][k] = rayBB[k][di]
			}
			if k >= 1 && k-1 < squareNB {
				beyondDec[di][k] = rayBB[k-1][di]
			}
		}
	}
}

// allPieceList 对应 HalfKAv2_hm::AllPieces 的顺序。
var allPieceList = [14]int{
	makePiece(colorWhite, ptRook), makePiece(colorWhite, ptAdvisor), makePiece(colorWhite, ptCannon),
	makePiece(colorWhite, ptPawn), makePiece(colorWhite, ptKnight), makePiece(colorWhite, ptBishop),
	makePiece(colorWhite, ptKing),
	makePiece(colorBlack, ptRook), makePiece(colorBlack, ptAdvisor), makePiece(colorBlack, ptCannon),
	makePiece(colorBlack, ptPawn), makePiece(colorBlack, ptKnight), makePiece(colorBlack, ptBishop),
	makePiece(colorBlack, ptKing),
}

func buildMasks() {
	for r := 0; r < rankNB; r++ {
		for f := 0; f < fileNB; f++ {
			rankBBs[r].set(sqOf(f, r))
			fileBBs[f].set(sqOf(f, r))
		}
	}
	for _, r := range []int{0, 1, 2, 7, 8, 9} {
		for _, f := range []int{3, 4, 5} {
			palaceBB.set(sqOf(f, r))
		}
	}
	for r := 0; r <= 4; r++ {
		halfBB[colorWhite] = halfBB[colorWhite].or(rankBBs[r])
	}
	for r := 5; r <= 9; r++ {
		halfBB[colorBlack] = halfBB[colorBlack].or(rankBBs[r])
	}
	for _, f := range []int{0, 2, 4, 6, 8} {
		pawnFileBB = pawnFileBB.or(fileBBs[f])
	}
	// 红兵过河后可在任意列，未过河（rank 3-4）只在本列。
	pawnBB[colorWhite] = halfBB[colorBlack].or(rankBBs[3].or(rankBBs[4]).and(pawnFileBB))
	pawnBB[colorBlack] = halfBB[colorWhite].or(rankBBs[6].or(rankBBs[5]).and(pawnFileBB))

	// 见 half_ka_v2_hm.h 的 ValidBB 字面量。
	all := halfBB[colorWhite].or(halfBB[colorBlack])
	validBB[makePiece(colorWhite, ptRook)] = all
	validBB[makePiece(colorWhite, ptAdvisor)] = rankBBs[0].or(rankBBs[2]).and(fileBBs[3].or(fileBBs[5])).
		or(rankBBs[1].and(fileBBs[4]))
	validBB[makePiece(colorWhite, ptCannon)] = all
	validBB[makePiece(colorWhite, ptPawn)] = pawnBB[colorWhite]
	validBB[makePiece(colorWhite, ptKnight)] = all
	validBB[makePiece(colorWhite, ptBishop)] = rankBBs[0].or(rankBBs[4]).and(fileBBs[2].or(fileBBs[6])).
		or(rankBBs[2].and(fileBBs[0].or(fileBBs[4]).or(fileBBs[8])))
	// 王只保留非镜像侧（f 列被镜像到 e/d），所以排除 file 5。
	validBB[makePiece(colorWhite, ptKing)] = halfBB[colorWhite].and(palaceBB).andNot(fileBBs[5])

	validBB[makePiece(colorBlack, ptRook)] = all
	validBB[makePiece(colorBlack, ptAdvisor)] = rankBBs[7].or(rankBBs[9]).and(fileBBs[3].or(fileBBs[5])).
		or(rankBBs[8].and(fileBBs[4]))
	validBB[makePiece(colorBlack, ptCannon)] = all
	validBB[makePiece(colorBlack, ptPawn)] = pawnBB[colorBlack]
	validBB[makePiece(colorBlack, ptKnight)] = all
	validBB[makePiece(colorBlack, ptBishop)] = rankBBs[5].or(rankBBs[9]).and(fileBBs[2].or(fileBBs[6])).
		or(rankBBs[7].and(fileBBs[0].or(fileBBs[4]).or(fileBBs[8])))
	// 黑将保留完整九宫：黑方视角会整体镜像，不需要在这里预先排除。
	validBB[makePiece(colorBlack, ptKing)] = halfBB[colorBlack].and(palaceBB)
}

// ---- 特征偏移表（half_ka_v2_hm.cpp 的 PSQOffsets）----

var (
	// psqOffsets[pc][sq] 是该棋子在该格的特征偏移；仅 validBB 内的格子有效。
	psqOffsets [pieceNB][squareNB]int
	// psqCount 是全部有效 (棋子, 格子) 组合数，必须等于 PS_NB。
	psqCount int
)

func buildPSQOffsets() {
	off := 0
	for _, pc := range allPieceList {
		for s := 0; s < squareNB; s++ {
			if validBB[pc].test(s) {
				psqOffsets[pc][s] = off
				off++
			}
		}
	}
	psqCount = off
}

// ---- king bucket 与镜像（half_ka_v2_hm.h）----

// kingBucketRaw 是源码里的 KingBuckets 字面量，低 3 位为 bucket，bit3 为镜像标志。
var kingBucketRaw = [squareNB]uint8{
	0, 0, 0, 0, 1, 8 | 0, 0, 0, 0,
	0, 0, 0, 2, 3, 8 | 2, 0, 0, 0,
	0, 0, 0, 4, 5, 8 | 4, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 4, 5, 8 | 4, 0, 0, 0,
	0, 0, 0, 2, 3, 8 | 2, 0, 0, 0,
	0, 0, 0, 0, 1, 8 | 0, 0, 0, 0,
}

type kingSlot struct {
	bucket uint8
	mirror bool
}

// kingBuckets[ksq][oksq][midMirror] 给出己方 king bucket 与是否镜像。
var kingBuckets [squareNB][squareNB][2]kingSlot

// midMirrorEncoding[pc][sq] 是用于判定中线镜像的 64 位编码。
var midMirrorEncoding [pieceNB][squareNB]uint64

// balanceEncoding 对应源码的 BalanceEncoding。
const balanceEncoding uint64 = 0xa4a92a74e989d3a7

// indexMap[mirror][rotate][sq]：mirror 翻列，rotate（黑方视角）翻行。
var indexMap [2][2][squareNB]int

func buildKingBuckets() {
	for ksq := 0; ksq < squareNB; ksq++ {
		for oksq := 0; oksq < squareNB; oksq++ {
			for midm := 0; midm <= 1; midm++ {
				kr := kingBucketRaw[ksq]
				or := kingBucketRaw[oksq]
				kb, okb := int(kr&7), int(or&7)
				mirror := (kr>>3) != 0 ||
					((kb&1) != 0 && ((or>>3) != 0 || (okb&1 != 0 && midm == 1)))
				kingBuckets[ksq][oksq][midm] = kingSlot{bucket: uint8(kb), mirror: mirror}
			}
		}
	}
}

// buildMidMirrorEncoding 重建 MidMirrorEncoding 表。
// shifts 按棋子类型索引；未使用的槽为 0。
var midMirrorShifts = [8][2]uint{}

func buildMidMirrorEncoding() {
	midMirrorShifts = [8][2]uint{{0, 0}, {44, 0}, {60, 36}, {47, 7}, {53, 21}, {50, 14}, {57, 29}, {0, 0}}

	for _, c := range []int{colorWhite, colorBlack} {
		for pt := ptRook; pt <= ptKing; pt++ {
			for r := 0; r < rankNB; r++ {
				for f := 0; f < fileNB; f++ {
					var enc uint64
					switch {
					case f != 4 && pt != ptKing:
						rr := r
						if c == colorBlack {
							rr = 9 - r
						}
						ff := f
						if f >= 4 {
							ff = 8 - f
						}
						s1, s2 := midMirrorShifts[pt][0], midMirrorShifts[pt][1]
						enc = (1 << s1) | (uint64(3-ff)*10+uint64(rr))<<s2
						if f >= 4 {
							enc = uint64(-int64(enc))
						}
					case f != 4 && pt == ptKing:
						enc = 1 << 63
					}
					midMirrorEncoding[makePiece(c, pt)][sqOf(f, r)] = enc
				}
			}
		}
	}
}

func buildIndexMap() {
	for m := 0; m < 2; m++ {
		for r := 0; r < 2; r++ {
			for s := 0; s < squareNB; s++ {
				t := s
				if m == 1 {
					t = flipFileSq(t)
				}
				if r == 1 {
					t = flipRankSq(t)
				}
				indexMap[m][r][s] = t
			}
		}
	}
}

func init() {
	buildMasks()
	// rayBB 必须最先建：buildPseudoAttacks / buildThreatOffsets 在初始化期
	// 就会调用 slidingAttack，那时表还是空的会静默算出空攻击集。
	buildRayTable()
	buildPSQOffsets()
	buildKingBuckets()
	buildMidMirrorEncoding()
	buildIndexMap()
	buildPseudoAttacks()
	buildThreatOffsets()
	buildAttackPassTables()
}
