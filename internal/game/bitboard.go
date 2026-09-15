package game

import "math/bits"

// Bitboard 位棋盘：90 个有效格拆成 2 个 64 位字（sq 0..63 落在 [0]，64..89 落在 [1]）。
// 90 格装不进单个 uint64；用两字表示只需扫描 2 个字即可遍历全盘，
// 比沿 16×16 mailbox 展开成 4 字的方案少一半扫描量。
type Bitboard [2]uint64

// 坐标：sq = rank*9 + file，rank 自红方底线起、file 自红方左侧起（与 types.go 一致）。
const (
	bbSquares = 90
	bbWords   = 2
)

func bitOf(sq int) (int, uint64) { return sq >> 6, uint64(1) << uint(sq&63) }

// Set / Clear / Test 单个格子的置位、清位与测试。
func (b *Bitboard) Set(sq int)   { w, m := bitOf(sq); b[w] |= m }
func (b *Bitboard) Clear(sq int) { w, m := bitOf(sq); b[w] &^= m }
func (b Bitboard) Test(sq int) bool {
	w, m := bitOf(sq)
	return b[w]&m != 0
}

// bbSquare 由 file/rank 计算 90 格索引；越界返回 -1。
func bbSquare(f, r int) int {
	if f < 0 || f > 8 || r < 0 || r > 9 {
		return -1
	}
	return r*9 + f
}

// bbFile / bbRank 反算 file/rank。
func bbFile(sq int) int { return sq % 9 }
func bbRank(sq int) int { return sq / 9 }

// IsEmpty 是否无置位。
func (b Bitboard) IsEmpty() bool { return (b[0] | b[1]) == 0 }

// Count 置位个数。
func (b Bitboard) Count() int { return bits.OnesCount64(b[0]) + bits.OnesCount64(b[1]) }

// And / Or / Xor / AndNot 集合运算。
func (b Bitboard) And(o Bitboard) Bitboard    { return Bitboard{b[0] & o[0], b[1] & o[1]} }
func (b Bitboard) Or(o Bitboard) Bitboard     { return Bitboard{b[0] | o[0], b[1] | o[1]} }
func (b Bitboard) Xor(o Bitboard) Bitboard    { return Bitboard{b[0] ^ o[0], b[1] ^ o[1]} }
func (b Bitboard) AndNot(o Bitboard) Bitboard { return Bitboard{b[0] &^ o[0], b[1] &^ o[1]} }

// lsb 最低置位索引；空盘返回 -1。
func (b Bitboard) lsb() int {
	if b[0] != 0 {
		return bits.TrailingZeros64(b[0])
	}
	if b[1] != 0 {
		return 64 + bits.TrailingZeros64(b[1])
	}
	return -1
}

// msb 最高置位索引；空盘返回 -1。
func (b Bitboard) msb() int {
	if b[1] != 0 {
		return 64 + 63 - bits.LeadingZeros64(b[1])
	}
	if b[0] != 0 {
		return 63 - bits.LeadingZeros64(b[0])
	}
	return -1
}

// popLSB 弹出最低置位并返回其索引；空盘返回 -1。
func (b *Bitboard) popLSB() int {
	sq := b.lsb()
	if sq < 0 {
		return -1
	}
	b.Clear(sq)
	return sq
}

// bbAll 全 90 格掩码（用于边界裁剪）。
var bbAll Bitboard

func init() {
	for sq := 0; sq < bbSquares; sq++ {
		bbAll.Set(sq)
	}
}
