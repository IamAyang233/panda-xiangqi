package game

import (
	"fmt"
	"strings"
)

// FEN 解析与生成（A2）。字母大小写区分颜色：大写红、小写黑。
// 相/象同时接受 B 与 E（两种常见写法），输出用 B；马接受 N 与 H。

var fenToPiece = map[byte]byte{
	'K': Piece(Red, King), 'A': Piece(Red, Advisor), 'B': Piece(Red, Elephant),
	'E': Piece(Red, Elephant), 'N': Piece(Red, Horse), 'H': Piece(Red, Horse),
	'R': Piece(Red, Rook), 'C': Piece(Red, Cannon), 'P': Piece(Red, Pawn),
	'k': Piece(Black, King), 'a': Piece(Black, Advisor), 'b': Piece(Black, Elephant),
	'e': Piece(Black, Elephant), 'n': Piece(Black, Horse), 'h': Piece(Black, Horse),
	'r': Piece(Black, Rook), 'c': Piece(Black, Cannon), 'p': Piece(Black, Pawn),
}

var pieceToFen = [16]byte{}

func init() {
	for c, t := range fenToPiece {
		if (t == Piece(Red, Elephant) && c == 'E') ||
			(t == Piece(Red, Horse) && c == 'H') ||
			(t == Piece(Black, Elephant) && c == 'e') ||
			(t == Piece(Black, Horse) && c == 'h') {
			continue
		}
		pieceToFen[t] = c
	}
}

// ParseFEN 解析标准 Xiangqi FEN。
func ParseFEN(fen string) (*Position, error) {
	p := &Position{}
	for i := range p.Board { // 16×16 边界哨兵：滑子越界自然停止
		p.Board[i] = Edge
	}
	for _, sq := range mailbox256 { // 有效格先清空
		p.Board[sq] = Empty
	}
	fields := strings.Fields(fen)
	if len(fields) < 2 {
		return nil, fmt.Errorf("FEN 至少需要 2 个字段: %q", fen)
	}
	rows := strings.Split(fields[0], "/")
	if len(rows) != 10 {
		return nil, fmt.Errorf("FEN 棋盘须为 10 行, 得到 %d", len(rows))
	}
	for i, row := range rows { // rows[0] 是 rank 9（黑方底线）
		r := 9 - i
		f := 0
		for _, ch := range []byte(row) {
			if ch >= '1' && ch <= '9' {
				f += int(ch - '0')
				continue
			}
			pc, ok := fenToPiece[ch]
			if !ok {
				return nil, fmt.Errorf("FEN 非法字符 %q", string(rune(ch)))
			}
			if f > 8 {
				return nil, fmt.Errorf("FEN 第 %d 行超长", r)
			}
			sq := SQ256(f, r)
			p.Board[sq] = pc
			if TypeOf(pc) == King {
				p.kingSq[ColorOf(pc)>>3] = sq
			}
			f++
		}
		if f != 9 {
			return nil, fmt.Errorf("FEN 第 %d 行长度为 %d, 应为 9", r, f)
		}
	}
	switch fields[1] {
	case "w":
		p.Turn = Red
	case "b":
		p.Turn = Black
	default:
		return nil, fmt.Errorf("FEN 轮走方须为 w/b, 得到 %q", fields[1])
	}
	if p.Board[p.kingSq[0]] != Piece(Red, King) {
		return nil, fmt.Errorf("FEN 缺少红帅")
	}
	if p.Board[p.kingSq[1]] != Piece(Black, King) {
		return nil, fmt.Errorf("FEN 缺少黑将")
	}
	if len(fields) >= 5 { // board turn - - halfmove fullmove（后两项可省略）
		fmt.Sscanf(fields[4], "%d", &p.Halfmove)
	}
	if len(fields) >= 6 {
		fmt.Sscanf(fields[5], "%d", &p.Fullmove)
	}
	p.recomputeKey()
	return p, nil
}

func (p *Position) recomputeKey() {
	var key uint64
	for sq90 := 0; sq90 < 90; sq90++ {
		sq := mailbox256[sq90]
		if pc := p.Board[sq]; pc != Empty {
			key ^= pieceKeys[pc][sq]
		}
	}
	if p.Turn == Black {
		key ^= sideKey
	}
	p.Key = key
}

// FEN 生成标准 FEN 串。
func (p *Position) FEN() string {
	var b strings.Builder
	for r := 9; r >= 0; r-- {
		if r < 9 {
			b.WriteByte('/')
		}
		emptyRun := 0
		for f := 0; f < 9; f++ {
			pc := p.Board[SQ256(f, r)]
			if pc == Empty {
				emptyRun++
				continue
			}
			if emptyRun > 0 {
				b.WriteByte(byte('0' + emptyRun))
				emptyRun = 0
			}
			b.WriteByte(pieceToFen[pc])
		}
		if emptyRun > 0 {
			b.WriteByte(byte('0' + emptyRun))
		}
	}
	turn := "w"
	if p.Turn == Black {
		turn = "b"
	}
	return fmt.Sprintf("%s %s - - %d %d", b.String(), turn, p.Halfmove, p.Fullmove)
}
