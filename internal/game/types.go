// Package game 实现中国象棋棋规引擎（纯函数、无 IO）。
// 坐标系：file ∈ [0,8] 自红方左侧起（红方视角），rank ∈ [0,9] 自红方底线起。
// 内部采用 16×16 mailbox + 哨兵边界：滑子（车/炮）越界时碰到哨兵自然停止。
package game

import "math/bits"

// 棋子颜色（字节高 4 位）。
const (
	Red   = 0
	Black = 8
)

// 棋子类型（字节低 4 位）。
const (
	King     = 1 // 帅/将
	Advisor  = 2 // 仕/士
	Elephant = 3 // 相/象
	Horse    = 4 // 马
	Rook     = 5 // 车
	Cannon   = 6 // 炮
	Pawn     = 7 // 兵/卒
)

// 特殊格子值。
const (
	Empty byte = 0x00
	Edge  byte = 0xFF // 16×16 边界哨兵
)

// Piece 由颜色与类型构造棋子字节编码。
func Piece(color, typ int) byte { return byte(color | typ) }

// ColorOf 返回棋子颜色（Red/Black）。调用前须确认非 Empty/Edge。
func ColorOf(p byte) int { return int(p & 0x08) }

// TypeOf 返回棋子类型（King..Pawn）。
func TypeOf(p byte) int { return int(p & 0x07) }

// Opponent 返回对方颜色。
func Opponent(color int) int { return color ^ 8 }

// 90 格一维坐标（rank*9+file）与 16×16 mailbox 下标互转表。
var (
	mailbox256 [90]int  // sq90 -> sq256
	mailbox90  [256]int // sq256 -> sq90，越界为 -1
)

// SQ256 由 file/rank 计算 256 下标。
func SQ256(f, r int) int { return (r+3)*16 + (f + 3) }

// FileOf256 / RankOf256 由 256 下标反算 file/rank（仅对合法格有效）。
func FileOf256(sq int) int { return sq%16 - 3 }
func RankOf256(sq int) int { return sq/16 - 3 }

// 马步预生成表：目标偏移 (df,dr) 与对应蹩腿偏移 (lf,lr)（见计划书 A1）。
var knightTbl = [8]struct{ df, dr, lf, lr int }{
	{+1, +2, 0, +1}, {+2, +1, +1, 0}, {+2, -1, +1, 0}, {+1, -2, 0, -1},
	{-1, -2, 0, -1}, {-2, -1, -1, 0}, {-2, +1, -1, 0}, {-1, +2, 0, +1},
}

// horseAttack[sq90] 列出所有能攻击 sq 的 (马位, 蹩腿点)（256 下标），用于反向探测。
var horseAttack [90][]struct{ origin, leg int }

func init() {
	for r := 0; r < 10; r++ {
		for f := 0; f < 9; f++ {
			sq90 := r*9 + f
			mailbox256[sq90] = SQ256(f, r)
		}
	}
	for i := range mailbox90 {
		mailbox90[i] = -1
	}
	for sq90, sq256 := range mailbox256 {
		mailbox90[sq256] = sq90
	}
	// 反向马攻表：马在 o、以 leg 为蹩腿点时可攻击 t。
	for _, sq256 := range mailbox256 {
		f, r := FileOf256(sq256), RankOf256(sq256)
		for _, m := range knightTbl {
			tf, tr := f+m.df, r+m.dr
			if tf < 0 || tf > 8 || tr < 0 || tr > 9 {
				continue
			}
			t := SQ256(tf, tr)
			leg := SQ256(f+m.lf, r+m.lr)
			horseAttack[mailbox90[t]] = append(horseAttack[mailbox90[t]],
				struct{ origin, leg int }{sq256, leg})
		}
	}
}

// inPalace 判断 (f,r) 是否位于 color 方九宫。
func inPalace(color, f, r int) bool {
	if f < 3 || f > 5 {
		return false
	}
	if color == Red {
		return r >= 0 && r <= 2
	}
	return r >= 7 && r <= 9
}

// ownSide 判断 (f,r) 是否位于 color 方本侧（未过河）。
func ownSide(color, r int) bool {
	if color == Red {
		return r <= 4
	}
	return r >= 5
}

// splitmix64 确定性伪随机，用于生成可复现的 Zobrist 数。
func splitmix64(x *uint64) uint64 {
	*x += 0x9E3779B97F4A7C15
	z := *x
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

// Zobrist 数：pieceKeys[piece][sq256] 与轮走方 Key。
var (
	pieceKeys [16][256]uint64
	sideKey   uint64
)

func init() {
	var seed uint64 = 0x20260816 // 固定种子，保证哈希可复现
	for p := 0; p < 16; p++ {
		for sq := 0; sq < 256; sq++ {
			pieceKeys[p][sq] = splitmix64(&seed)
		}
	}
	sideKey = splitmix64(&seed)
}

// MoveDesc 着法附加信息（供 UI / 记谱使用）。
type MoveDesc struct {
	Check     bool // 走完后将军对方
	Capture   bool
	Cn        string // 中文记谱
	From, To  string // UCI 格式坐标
}

// popcount 保留给评估函数使用。
func popcount(x uint64) int { return bits.OnesCount64(x) }
