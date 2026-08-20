// Command matesolve: exhaustive forced-mate prover for panda-xiangqi puzzles.
//
// It proves that, from a given position, the side to move (the "attacker",
// Red by default, or Black with -side black) can force a genuine 将死
// (checkmate) within a bounded number of its own moves, against EVERY legal
// defense by the opponent. By default it requires genuine checkmate so that its
// verdict matches the authoritative self-play puzzle-check gate (which only
// accepts 将死). Pass -allow-stalemate to also accept 困毙 (stalemate).
//
// Usage:
//   go run ./cmd/matesolve -in internal/puzzle/data/killers.json -depth 3
//   go run ./cmd/matesolve -fen "..." -depth 3                # red to move (default)
//   go run ./cmd/matesolve -fen "..." -side black -depth 3    # black to move
//
// When -in is used, each puzzle's "playerSide" field selects the attacker
// (default red). The -side flag only applies to the single -fen form.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
)

// Puzzle mirrors the JSON schema we store puzzles in. Only FEN/id/name/playerSide
// are used by the prover, but we decode the whole record so the file round-trips.
type Puzzle struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Source     string   `json:"source"`
	Difficulty string   `json:"difficulty"`
	PlayerSide string   `json:"playerSide"`
	Goal       string   `json:"goal"`
	FEN        string   `json:"fen"`
	ParMoves   int      `json:"parMoves"`
	Solution   []string `json:"solution"`
	Tags       []string `json:"tags"`
	Verified   bool     `json:"verified"`
}

// parseSide maps "red"/"black" (default red) to a game color.
func parseSide(s string) int {
	if s == "black" {
		return game.Black
	}
	return game.Red
}

// sideName is the human-readable label for a color.
func sideName(c int) string {
	if c == game.Black {
		return "black"
	}
	return "red"
}

// winResult is the Result* constant for a win by color c.
func winResult(c int) string {
	if c == game.Black {
		return game.ResultBlackWin
	}
	return game.ResultRedWin
}

func main() {
	in := flag.String("in", "", "path to a puzzle JSON array file")
	fen := flag.String("fen", "", "a single FEN to prove (alternative to -in)")
	depth := flag.Int("depth", 3, "max attacker moves allowed to force mate")
	side := flag.String("side", "red", "attacker side for the -fen form: red | black")
	allowStalemate := flag.Bool("allow-stalemate", false,
		"also accept 困毙 (stalemate) forced wins, not just 将死 (checkmate)")
	flag.Parse()

	if *fen != "" {
		runOne(Puzzle{Name: "(fen)", FEN: *fen}, *depth, parseSide(*side), *allowStalemate)
		return
	}
	if *in == "" {
		fmt.Fprintln(os.Stderr, "specify either -in <file> or -fen <fen>")
		os.Exit(2)
	}
	data, err := os.ReadFile(*in)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read:", err)
		os.Exit(1)
	}
	var puzzles []Puzzle
	if err := json.Unmarshal(data, &puzzles); err != nil {
		fmt.Fprintln(os.Stderr, "json:", err)
		os.Exit(1)
	}
	allPass := true
	for _, p := range puzzles {
		// Per-puzzle attacker is driven by playerSide; fall back to red.
		if !runOne(p, *depth, parseSide(p.PlayerSide), *allowStalemate) {
			allPass = false
		}
	}
	if !allPass {
		os.Exit(1)
	}
}

func runOne(p Puzzle, depth, attacker int, allowStalemate bool) bool {
	pos, err := game.ParseFEN(p.FEN)
	if err != nil {
		fmt.Printf("[%s] %s  PARSE ERROR: %v\n", p.ID, p.Name, err)
		return false
	}
	defender := game.Opponent(attacker)
	if pos.Turn != attacker {
		fmt.Printf("[%s] %s  WARN: not %s to move (turn=%d)\n", p.ID, p.Name, sideName(attacker), pos.Turn)
	}
	// Root legality: with the attacker to move, the defender must NOT already
	// be in check (that would be an illegal "facing check" root position).
	rootIllegal := pos.InCheck(defender)

	k, pv, reason := forceWin(pos, depth, attacker, defender, allowStalemate)
	label := p.ID
	if label == "" {
		label = p.Name
	}
	name := p.Name
	if name == "" {
		name = p.FEN
	}
	fmt.Printf("[%s] %s\n", label, name)
	fmt.Printf("  FEN: %s\n", p.FEN)
	if rootIllegal {
		fmt.Printf("  WARN: root illegal — %s is in check with %s to move\n",
			sideName(defender), sideName(attacker))
	}
	if k < 0 {
		fmt.Printf("  FAIL: no forced mate within %d %s move(s)\n", depth, sideName(attacker))
		return false
	}
	var uci []string
	for _, m := range pv {
		uci = append(uci, m.String())
	}
	fmt.Printf("  PASS: forced mate in %d %s move(s)  (%s)\n", k, sideName(attacker), reasonLabel(reason))
	fmt.Printf("  PV:   %s\n", strings.Join(uci, " "))
	return true
}

// forceWin: the attacker is to move. Returns the shortest number of ATTACKER
// moves (>=1) to force a win, a principal variation, and the terminal win
// reason, or (-1, nil, "") if no forced win exists within `limit` attacker moves.
//
// The terminal win must be a genuine 将死 (checkmate) when allowStalemate is
// false. This keeps the prover's verdict consistent with the authoritative
// puzzle-check gate, which rejects 困毙 (stalemate) wins.
func forceWin(pos *game.Position, limit, attacker, defender int, allowStalemate bool) (int, []game.Move, string) {
	if limit <= 0 {
		return -1, nil, ""
	}
	best := -1
	var bestPV []game.Move
	bestReason := ""
	for _, m := range pos.LegalMoves(attacker) {
		pos.Make(m)
		st := pos.CheckStatus()
		if st.Result == winResult(attacker) && mateReasonOK(st, attacker, allowStalemate) {
			pos.Unmake()
			if best == -1 || 1 < best {
				best = 1
				bestPV = []game.Move{m}
				bestReason = st.Reason
			}
			continue
		}
		// Defender to move now; every reply must lead to a forced win.
		k, bpv, r := defendWin(pos, limit-1, attacker, defender, m, allowStalemate)
		pos.Unmake()
		if k >= 0 {
			total := 1 + k
			if best == -1 || total < best {
				best = total
				bestPV = append([]game.Move{m}, bpv...)
				bestReason = r
			}
		}
	}
	return best, bestPV, bestReason
}

// defendWin: the defender is to move. Returns the worst-case additional attacker
// moves needed to force the win across ALL defender replies (or -1 if any reply
// escapes), the principal variation, and the worst-case terminal win reason.
// `atkMove` is the attacker move that produced this position (used only for clarity).
func defendWin(pos *game.Position, limit, attacker, defender int, _ game.Move, allowStalemate bool) (int, []game.Move, string) {
	moves := pos.LegalMoves(defender)
	if len(moves) == 0 {
		// Attacker just moved and the defender has no legal reply. The position
		// is a win for the attacker, but we must confirm it is the kind of win
		// the prover accepts: genuine 将死 (checkmate) by default, 困毙
		// (stalemate) only when allowStalemate is set. Otherwise it is a false
		// PASS (a stalemate win rejected by the authoritative puzzle-check).
		st := pos.CheckStatus()
		if !mateReasonOK(st, attacker, allowStalemate) {
			return -1, nil, ""
		}
		return 0, nil, st.Reason
	}
	worst := -1
	var worstPV []game.Move
	worstReason := ""
	for _, b := range moves {
		pos.Make(b)
		st := pos.CheckStatus()
		if st.IsDraw || st.Result == winResult(defender) {
			pos.Unmake()
			return -1, nil, "" // this defense escapes (draw or defender win)
		}
		k, pv, r := forceWin(pos, limit, attacker, defender, allowStalemate)
		pos.Unmake()
		if k < 0 {
			return -1, nil, ""
		}
		if worst == -1 || k > worst {
			worst = k
			worstPV = append([]game.Move{b}, pv...)
			worstReason = r
		}
	}
	return worst, worstPV, worstReason
}

// mateReasonOK reports whether a win status for the given attacker counts as a
// forced mate for the prover. By default (allowStalemate=false) we require
// genuine 将死 (checkmate), matching the authoritative puzzle-check gate. When
// allowStalemate is true we also accept 困毙 (stalemate), which is a legal win
// under Xiangqi rules but is REJECTED by puzzle-check.
func mateReasonOK(st game.Status, attacker int, allowStalemate bool) bool {
	if st.Result != winResult(attacker) {
		return false
	}
	if st.Reason == game.ReasonCheckmate {
		return true
	}
	return allowStalemate && st.Reason == game.ReasonStalemate
}

// reasonLabel maps an internal win reason to a human-readable label.
func reasonLabel(r string) string {
	switch r {
	case game.ReasonCheckmate:
		return "将死 checkmate"
	case game.ReasonStalemate:
		return "困毙 stalemate"
	default:
		return r
	}
}
