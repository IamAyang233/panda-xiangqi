package game

import "testing"

// TestLastCaptured_NoCapture：非吃子着法，LastCaptured 必须返回 Empty。
// 回归：Undo 之前误读 s.pos.Board[mv.To]（走完后是走子方自己），
// 会把走子棋当成被吃子，导致前端在落点凭空复原一颗棋子。
func TestLastCaptured_NoCapture(t *testing.T) {
	p, err := ParseFEN(InitialFEN)
	if err != nil {
		t.Fatalf("parse initial fen: %v", err)
	}
	mv, ok := MoveFromUCI("a3a4") // 红兵平推，无吃子
	if !ok {
		t.Fatal("bad uci a3a4")
	}
	cap := p.Make(mv)
	if cap != Empty {
		t.Fatalf("a3a4 should not capture, got %q", cap)
	}
	if got := p.LastCaptured(); got != Empty {
		t.Fatalf("LastCaptured after non-capture = %q, want Empty", got)
	}
}

// TestLastCaptured_Capture：吃子着法，LastCaptured 必须返回被吃子的 FEN 字符。
func TestLastCaptured_Capture(t *testing.T) {
	// 红车 h1、黑车 h5，红先。h1h5 吃黑车。
	p, err := ParseFEN("4k4/9/9/9/7r1/9/9/9/7R1/4K4 w - - 0 1")
	if err != nil {
		t.Fatalf("parse fen: %v", err)
	}
	mv, ok := MoveFromUCI("h1h5")
	if !ok {
		t.Fatal("bad uci h1h5")
	}
	cap := p.Make(mv)
	// Make/LastCaptured 返回的是引擎内部编码字节（黑车=0x0D），
	// 需经 PieceToFen 转成标准 FEN 字符再比较（前端收到的正是此字符）。
	if PieceToFen(cap) != 'r' {
		t.Fatalf("h1h5 should capture black rook, PieceToFen=%q", PieceToFen(cap))
	}
	if got := p.LastCaptured(); PieceToFen(got) != 'r' {
		t.Fatalf("LastCaptured after capture = %q, want 'r'", PieceToFen(got))
	}
}
