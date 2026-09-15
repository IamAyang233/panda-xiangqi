package nnue

// 本文件是增量累加器「脏信息完整性」的回归测试。
//
// 增量更新的正确性建立在一条不变式上：走子过程中累积的脏信息（pending）
// 必须完整且精确地描述「从累加器缓存的那个局面到当前局面」的全部特征变化。
// 算错不会报错，只会让评估值静默偏移 —— 所以这里逐着、逐层对拍。
//
// 判据：把 pending 转成「特征索引 → 增减次数」，与「两次全量枚举的集合差」
// 逐项比较，必须完全一致（不多不少、方向正确）。

import (
	"fmt"
	"testing"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
)

// diagCheckGap 控制 TestApplyMatchesRefresh 里「每走几步评估一次」。
var diagCheckGap = 1

// threatSet 是「威胁特征索引 → 出现次数」的多重集。
type threatSet map[int]int

// snapshotThreats 全量枚举某局面的威胁特征集合。
func snapshotThreats(pos *Position) ([colorNB]threatSet, [colorNB]bool) {
	var out [colorNB]threatSet
	var mir [colorNB]bool
	for c := 0; c < colorNB; c++ {
		out[c] = threatSet{}
		_, mir[c] = pos.FeatureBucket(c)
		pos.forEachThreat(c, mir[c], func(idx int) { out[c][idx]++ })
	}
	return out, mir
}

// pendingThreatDelta 把累积的脏信息转成多重集（按当前镜像换算索引）。
func pendingThreatDelta(pos *Position, c int, mirror bool) threatSet {
	out := threatSet{}
	for _, d := range pos.pendingThreats {
		idx := ThreatIndex(c, int(d.attacker), d.from, d.to, int(d.attacked), mirror)
		if idx >= ThreatInputs {
			continue
		}
		if d.add {
			out[idx]++
		} else {
			out[idx]--
		}
	}
	for k, v := range out {
		if v == 0 {
			delete(out, k)
		}
	}
	return out
}

func setDiff(old, new threatSet) threatSet {
	out := threatSet{}
	for k, v := range new {
		out[k] += v
	}
	for k, v := range old {
		out[k] -= v
	}
	for k, v := range out {
		if v == 0 {
			delete(out, k)
		}
	}
	return out
}

func sameSet(a, b threatSet) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// ---- 索引解码：仅用于失败时打印可读信息 ----

var pieceNames = [16]string{"-", "R车", "A仕", "C炮", "P兵", "N马", "B象", "K帅",
	"-", "r车", "a士", "c炮", "p卒", "n馬", "b象", "k将"}

func pieceName(pc int) string {
	if pc < 0 || pc >= 16 {
		return "?"
	}
	return pieceNames[pc]
}

func sqStr(s int) string {
	return fmt.Sprintf("%c%d", rune('a'+fileOf(s)), rankOf(s))
}

var threatDecode []struct{ attacker, from, to, attacked int }

func buildThreatDecode() {
	if threatDecode != nil {
		return
	}
	threatDecode = make([]struct{ attacker, from, to, attacked int }, ThreatInputs)
	for _, a := range allPieceList {
		for from := 0; from < squareNB; from++ {
			for to := 0; to < squareNB; to++ {
				for _, att := range allPieceList {
					if idx := int(threatOffsets[a][from][to][att]); idx < ThreatInputs {
						threatDecode[idx] = struct{ attacker, from, to, attacked int }{a, from, to, att}
					}
				}
			}
		}
	}
}

// decodeThreat 把特征索引还原成 (攻击者, from, to, 被攻击者) 的可读形式。
func decodeThreat(idx int) string {
	buildThreatDecode()
	if idx < 0 || idx >= len(threatDecode) {
		return "越界"
	}
	d := threatDecode[idx]
	return fmt.Sprintf("%s@%s → %s@%s", pieceName(d.attacker), sqStr(d.from), pieceName(d.attacked), sqStr(d.to))
}

// diagReportDiff 打出「真实差集」与「pending 差集」的差异明细。
func diagReportDiff(t *testing.T, delta, real threatSet, limit int) {
	t.Helper()
	shown := 0
	for k, v := range real {
		if delta[k] != v && shown < limit {
			t.Logf("    真实 %+d  %s（pending=%d）", v, decodeThreat(k), delta[k])
			shown++
		}
	}
	for k, v := range delta {
		if _, ok := real[k]; !ok && shown < limit {
			t.Logf("    pending %+d  %s（多余）", v, decodeThreat(k))
			shown++
		}
	}
}

// testFENs 是覆盖各类边界的局面：初始、只剩将帅（桶切换）、子力交错（吃子与炮架）、
// 以及实际测试里出现过的中局。
var testFENs = []string{
	game.InitialFEN,
	"rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR b - - 0 1",
	"3k5/9/9/9/9/9/9/9/9/4K4 w - - 0 1",
	"2bak4/9/4c4/9/9/9/9/4C4/9/3AK4 w - - 0 1",
	"3k5/2P1P4/4b4/9/9/9/9/4p1p2/2p1p4/3K1C3 w - - 0 1",
	"r1ba1a3/4kn3/2n1b4/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1",
	"rnbakab2/1C7/2c3n2/p1prp1C1p/9/2P1P4/P5P1P/9/c3A4/R1B1KABNR w - - 3 10",
	"r1bakab2/1C7/2c3n2/p1prp1C1p/9/2P1P4/P5P1P/9/c3A4/R1B1KABNR b - - 3 10",
}

// TestThreatDeltaMatchesFullEnum 逐着验证：走一步之后，pending 的净效果
// 必须与「两次全量枚举的集合差」完全一致。
func TestThreatDeltaMatchesFullEnum(t *testing.T) {
	bad, checked, skipped := 0, 0, 0
	for _, fen := range testFENs {
		root, err := game.ParseFEN(fen)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range root.LegalMoves(root.Turn) {
			p, _ := game.ParseFEN(fen)
			var pos Position
			pos.ResetFromGame(&p.Board, p.Turn>>3)

			oldSet, oldMirror := snapshotThreats(&pos)
			p.Make(m)
			pos.Make(int(m.From), int(m.To))
			newSet, newMirror := snapshotThreats(&pos)

			if oldMirror != newMirror {
				skipped++
				continue
			}
			checked++
			for c := 0; c < colorNB; c++ {
				delta := pendingThreatDelta(&pos, c, newMirror[c])
				real := setDiff(oldSet[c], newSet[c])
				if !sameSet(delta, real) {
					bad++
					if bad <= 3 {
						t.Errorf("FEN=%s 着法=%s c=%d：pending %d 项 / 真实 %d 项",
							fen, m, c, len(delta), len(real))
						diagReportDiff(t, delta, real, 6)
					}
				}
			}
		}
	}
	if skipped > 0 {
		t.Logf("累计 %d 个着法（跳过 %d 个镜像翻转，索引不可比）", checked, skipped)
	}
}

// TestThreatDeltaAcrossSteps 多步累积：不回退连续走多步后，
// pending 的净效果仍须等于两次全量枚举的集合差。
func TestThreatDeltaAcrossSteps(t *testing.T) {
	bad := 0
	for _, fen := range testFENs {
		for steps := 1; steps <= 6; steps++ {
			p, err := game.ParseFEN(fen)
			if err != nil {
				t.Fatal(err)
			}
			var pos Position
			pos.ResetFromGame(&p.Board, p.Turn>>3)

			oldSet, oldMirror := snapshotThreats(&pos)
			mirrorChanged := false
			desc := ""
			for i := 0; i < steps; i++ {
				moves := p.LegalMoves(p.Turn)
				if len(moves) == 0 {
					break
				}
				// 优先吃子着法，覆盖 swapPiece 路径。
				m := moves[0]
				for _, x := range moves {
					if p.PieceAt90(int(x.To)) != game.Empty {
						m = x
						break
					}
				}
				desc += m.String() + "+"
				p.Make(m)
				pos.Make(int(m.From), int(m.To))
				_, mir := snapshotThreats(&pos)
				for c := 0; c < colorNB; c++ {
					if mir[c] != oldMirror[c] {
						mirrorChanged = true
					}
				}
			}
			newSet, newMirror := snapshotThreats(&pos)
			if mirrorChanged {
				continue
			}
			for c := 0; c < colorNB; c++ {
				delta := pendingThreatDelta(&pos, c, newMirror[c])
				real := setDiff(oldSet[c], newSet[c])
				if sameSet(delta, real) {
					continue
				}
				bad++
				if bad <= 3 {
					t.Errorf("FEN=%s 走 %d 步(%s) c=%d：pending %d 项 / 真实 %d 项",
						fen, steps, desc, c, len(delta), len(real))
					diagReportDiff(t, delta, real, 6)
				}
			}
		}
	}
}

// TestUndoLeavesNoResidue 走一步再回退，累积脏信息的净效果必须为零 ——
// 局面回到原处，累加器不该有任何变化。
func TestUndoLeavesNoResidue(t *testing.T) {
	bad := 0
	for _, fen := range testFENs {
		root, err := game.ParseFEN(fen)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range root.LegalMoves(root.Turn) {
			p, _ := game.ParseFEN(fen)
			var pos Position
			pos.ResetFromGame(&p.Board, p.Turn>>3)

			p.Make(m)
			pos.Make(int(m.From), int(m.To))
			p.Unmake()
			pos.Unmake()

			for c := 0; c < colorNB; c++ {
				_, mirror := pos.FeatureBucket(c)
				delta := pendingThreatDelta(&pos, c, mirror)
				if len(delta) == 0 {
					continue
				}
				bad++
				if bad <= 3 {
					t.Errorf("FEN=%s 走 %s 再回退：残留 %d 项（version=%d base=%d stale=%v）",
						fen, m, len(delta), pos.version, pos.pendingBase, pos.stale)
					diagReportDiff(t, delta, threatSet{}, 8)
				}
			}
		}
	}
}
