// Command matesolve: exhaustive forced-mate prover for panda-xiangqi puzzles.
//
// It proves that, from a given red-to-move position, Red can force a genuine
// 将死 (checkmate) within a bounded number of RED moves, against EVERY legal
// Black defense. By default it requires genuine checkmate so that its verdict
// matches the authoritative self-play puzzle-check gate (which only accepts
// 将死). Pass -allow-stalemate to also accept 困毙 (stalemate), which is a legal
// Red win under Xiangqi rules but is REJECTED by puzzle-check.
//
// Unlike the self-play puzzle-check, this does NOT rely on an engine choosing
// the toughest defense — it enumerates all defender replies, so a PASS here is a
// genuine proof of a forced win.
//
// Usage:
//   go run ./cmd/matesolve -in internal/puzzle/data/killers.json -depth 3
//   go run ./cmd/matesolve -fen "..." -depth 3
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
)

// Puzzle mirrors the JSON schema we store puzzles in. Only FEN/id/name are used
// by the prover, but we decode the whole record so the file round-trips.
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

func main() {
	in := flag.String("in", "", "path to a puzzle JSON array file")
	fen := flag.String("fen", "", "a single FEN to prove (alternative to -in)")
	depth := flag.Int("depth", 3, "max red moves allowed to force mate")
	allowStalemate := flag.Bool("allow-stalemate", false,
		"also accept 困毙 (stalemate) forced wins, not just 将死 (checkmate)")
	flag.Parse()

	if *fen != "" {
		runOne(Puzzle{Name: "(fen)", FEN: *fen}, *depth, *allowStalemate)
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
		if !runOne(p, *depth, *allowStalemate) {
			allPass = false
		}
	}
	if !allPass {
		os.Exit(1)
	}
}

func runOne(p Puzzle, depth int, allowStalemate bool) bool {
	pos, err := game.ParseFEN(p.FEN)
	if err != nil {
		fmt.Printf("[%s] %s  PARSE ERROR: %v\n", p.ID, p.Name, err)
		return false
	}
	if pos.Turn != game.Red {
		fmt.Printf("[%s] %s  WARN: not red to move (turn=%d)\n", p.ID, p.Name, pos.Turn)
	}
	// Root legality: with Red to move, Black must not already be in check.
	rootIllegal := pos.InCheck(game.Black)

	k, pv, reason := redForce(pos, depth, allowStalemate)
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
		fmt.Println("  WARN: root illegal — Black is in check with Red to move")
	}
	if k < 0 {
		fmt.Printf("  FAIL: no forced mate within %d red move(s)\n", depth)
		return false
	}
	var uci []string
	for _, m := range pv {
		uci = append(uci, m.String())
	}
	fmt.Printf("  PASS: forced mate in %d red move(s)  (%s)\n", k, reasonLabel(reason))
	fmt.Printf("  PV:   %s\n", strings.Join(uci, " "))
	return true
}

// redForce: Red is to move. Returns the shortest number of RED moves (>=1) to
// force a win, a principal variation, and the terminal win reason, or
// (-1, nil, "") if no forced win exists within `limit` red moves.
//
// The terminal win must be a genuine 将死 (checkmate) when allowStalemate is
// false. This keeps the prover's verdict consistent with the authoritative
// puzzle-check gate, which rejects 困毙 (stalemate) wins.
func redForce(pos *game.Position, limit int, allowStalemate bool) (int, []game.Move, string) {
	if limit <= 0 {
		return -1, nil, ""
	}
	best := -1
	var bestPV []game.Move
	bestReason := ""
	for _, m := range pos.LegalMoves(game.Red) {
		pos.Make(m)
		st := pos.CheckStatus()
		if st.Result == game.ResultRedWin && mateReasonOK(st, allowStalemate) {
			pos.Unmake()
			if best == -1 || 1 < best {
				best = 1
				bestPV = []game.Move{m}
				bestReason = st.Reason
			}
			continue
		}
		// Black to move now; every reply must lead to a forced win.
		k, bpv, r := blackDefend(pos, limit-1, m, allowStalemate)
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

// blackDefend: Black is to move. Returns the worst-case additional red moves
// needed to force the win across ALL black replies (or -1 if any reply escapes),
// the principal variation, and the worst-case terminal win reason.
// `redMove` is the red move that produced this position (used only for clarity).
func blackDefend(pos *game.Position, limit int, _ game.Move, allowStalemate bool) (int, []game.Move, string) {
	moves := pos.LegalMoves(game.Black)
	if len(moves) == 0 {
		// Red just moved and Black has no legal reply. The position is a win
		// for Red, but we must confirm it is the kind of win the prover
		// accepts: genuine 将死 (checkmate) by default, 困毙 (stalemate) only
		// when allowStalemate is set. This branch previously returned success
		// unconditionally, which let forced stalemate wins (rejected by the
		// authoritative puzzle-check) slip through as a false PASS.
		st := pos.CheckStatus()
		if !mateReasonOK(st, allowStalemate) {
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
		if st.IsDraw || st.Result == game.ResultBlackWin {
			pos.Unmake()
			return -1, nil, "" // this defense escapes (draw or black win)
		}
		k, pv, r := redForce(pos, limit, allowStalemate)
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

// mateReasonOK reports whether a Red-win status counts as a forced mate for the
// prover. By default (allowStalemate=false) we require genuine 将死 (checkmate),
// matching the authoritative puzzle-check gate. When allowStalemate is true we
// also accept 困毙 (stalemate), which is a legal Red win under Xiangqi rules but
// is REJECTED by puzzle-check.
func mateReasonOK(st game.Status, allowStalemate bool) bool {
	if st.Result != game.ResultRedWin {
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
