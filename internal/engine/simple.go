// Package engine 提供统一的"思考者"抽象：本地强引擎（皮卡鱼 UCI）与自研兜底引擎。
package engine

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"time"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
)

// Engine 统一引擎接口（计划书 4.2）。
type Engine interface {
	// BestMove 在时限内为当前轮走方返回一步棋。pos 会被 make/unmake 还原，不被破坏。
	BestMove(ctx context.Context, pos *game.Position, level int) (game.Move, error)
	Name() string
}

// ---------------------------------------------------------------- 评估函数

// 子力价值（计划书 A9）：帅 10000 / 车 900 / 炮 450 / 马 400 / 相 210 / 仕 200 / 兵 100。
var pieceValue = [8]int16{0, 10000, 200, 210, 400, 900, 450, 100}

// 位置表（红方视角，sq90 = rank*9+file；黑方取 89-sq90 镜像）。
var pst = [8][90]int16{
	game.King: {}, // 帅的位置价值并入安全近似，忽略
	game.Advisor: {
		0, 0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 3, 0, 3, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 0,
	},
	game.Elephant: {
		0, 0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 4, 0, 0, 0, 4, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 0,
	},
	game.Horse: {
		4, 8, 16, 12, 4, 12, 16, 8, 4,
		4, 10, 28, 16, 8, 16, 28, 10, 4,
		12, 14, 16, 20, 18, 20, 16, 14, 12,
		8, 24, 18, 24, 20, 24, 18, 24, 8,
		14, 16, 16, 20, 20, 20, 16, 16, 14,
		8, 24, 18, 24, 20, 24, 18, 24, 8,
		12, 14, 16, 20, 18, 20, 16, 14, 12,
		14, 16, 18, 20, 28, 20, 18, 16, 14,
		24, 24, 16, 32, 32, 32, 16, 24, 24,
		16, 20, 20, 32, 36, 32, 20, 20, 16,
	},
	game.Rook: {
		14, 14, 12, 18, 16, 18, 12, 14, 14,
		16, 20, 18, 24, 26, 24, 18, 20, 16,
		12, 12, 12, 18, 18, 18, 12, 12, 12,
		12, 18, 16, 22, 22, 22, 16, 18, 12,
		12, 14, 12, 18, 18, 18, 12, 14, 12,
		12, 16, 14, 20, 20, 20, 14, 16, 12,
		6, 10, 8, 14, 14, 14, 8, 10, 6,
		4, 8, 6, 14, 12, 14, 6, 8, 4,
		8, 4, 8, 16, 8, 16, 8, 4, 8,
		-2, 10, 6, 14, 12, 14, 6, 10, -2,
	},
	game.Cannon: {
		6, 4, 2, 4, 4, 4, 2, 4, 6,
		2, 2, 0, -4, -4, -4, 0, 2, 2,
		2, 2, 0, 4, 14, 4, 0, 2, 2,
		0, 0, -2, 2, 6, 2, -2, 0, 0,
		0, 0, -2, 4, 8, 4, -2, 0, 0,
		-2, 0, 0, 2, 6, 2, 0, 0, -2,
		0, 0, 0, 2, 4, 2, 0, 0, 0,
		0, 0, 2, 4, 12, 4, 2, 0, 0,
		0, 0, 6, 8, 14, 8, 6, 0, 0,
		0, -2, 4, 6, 10, 6, 4, -2, 0,
	},
	game.Pawn: {
		0, 0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 0,
		10, 12, 14, 16, 18, 16, 14, 12, 10,
		22, 24, 26, 28, 32, 28, 26, 24, 22,
		36, 38, 42, 46, 52, 46, 42, 38, 36,
		50, 54, 58, 62, 68, 62, 58, 54, 50,
		60, 64, 68, 74, 80, 74, 68, 64, 60,
		62, 66, 70, 76, 82, 76, 70, 66, 62,
		40, 44, 48, 52, 56, 52, 48, 44, 40,
	},
}

// Evaluate 静态评估：子力 + 位置表 + 无进攻子力罚（红方视角）。
func Evaluate(p *game.Position) int16 {
	var score int16
	var attackers [2]int
	for sq90 := 0; sq90 < 90; sq90++ {
		pc := p.PieceAt90(sq90)
		if pc == game.Empty {
			continue
		}
		typ := game.TypeOf(pc)
		v := pieceValue[typ]
		switch game.ColorOf(pc) {
		case game.Red:
			score += v + pst[typ][sq90]
		default:
			score -= v + pst[typ][89-sq90]
		}
		switch typ {
		case game.Rook, game.Cannon, game.Horse, game.Pawn:
			if game.ColorOf(pc) == game.Red {
				attackers[0]++
			} else {
				attackers[1]++
			}
		}
	}
	if attackers[0] == 0 {
		score -= 80
	}
	if attackers[1] == 0 {
		score += 80
	}
	return score
}

// ---------------------------------------------------------------- 搜索

const (
	mateScore = 30000
	mateBound = 29000
	infScore  = 31000
	ttExact   = 1
	ttLower   = 2
	ttUpper   = 3
	maxPly    = 127
)

type ttEntry struct {
	depth int8
	flag  uint8
	score int16
	move  game.Move
}

// SimpleEngine 自研兜底引擎：α-β 剪枝 + 迭代加深 + 置换表 + MVV-LVA/杀手/历史排序 + 静态搜索。
type SimpleEngine struct{}

// NewSimpleEngine 创建自研引擎实例。
func NewSimpleEngine() *SimpleEngine { return &SimpleEngine{} }

func (e *SimpleEngine) Name() string { return "SimpleEngine" }

// 档位参数：深度上限 / 思考预算 / 随机性（从前 3 优着随机取一的概率）。
type levelCfg struct {
	maxDepth int
	budget   time.Duration
	randProb float64
}

var simpleLevels = map[int]levelCfg{
	1: {1, 200 * time.Millisecond, 0.30},
	2: {2, 300 * time.Millisecond, 0.20},
	3: {3, 400 * time.Millisecond, 0.10},
	4: {3, 500 * time.Millisecond, 0},
	5: {4, 600 * time.Millisecond, 0},
	6: {4, 800 * time.Millisecond, 0},
	7: {5, 1000 * time.Millisecond, 0},
	8: {5, 1200 * time.Millisecond, 0},
	9: {6, 1500 * time.Millisecond, 0},
	10: {6, 1800 * time.Millisecond, 0},
	11: {7, 2000 * time.Millisecond, 0},
	12: {7, 2400 * time.Millisecond, 0},
	13: {8, 2600 * time.Millisecond, 0},
	14: {9, 3000 * time.Millisecond, 0},
	15: {10, 3200 * time.Millisecond, 0},
	16: {12, 3500 * time.Millisecond, 0},
}

func cfgOf(level int) levelCfg {
	if c, ok := simpleLevels[level]; ok {
		return c
	}
	return simpleLevels[8]
}

type searcher struct {
	pos      *game.Position
	tt       map[uint64]ttEntry
	start    time.Time
	deadline time.Time
	stopped  bool
	nodes    int
	killers  [maxPly]game.Move
	history  [65536]int32
}

func (s *searcher) checkTime() {
	s.nodes++
	if s.nodes%2048 == 0 && time.Now().After(s.deadline) {
		s.stopped = true
	}
}

// EvaluateRel 相对当前轮走方的评估。
func (s *searcher) EvaluateRel() int16 {
	sc := Evaluate(s.pos)
	if s.pos.Turn == game.Black {
		return -sc
	}
	return sc
}

func (s *searcher) scoreMove(m game.Move, ttMove game.Move, ply int) int32 {
	if m == ttMove {
		return 1 << 24
	}
	if victim := s.pos.Board[m.To]; victim != game.Empty {
		return 1<<20 + int32(pieceValue[game.TypeOf(victim)])*16 -
			int32(pieceValue[game.TypeOf(s.pos.Board[m.From])])/16 // MVV-LVA
	}
	if ply < maxPly && (s.killers[ply] == m || (ply >= 1 && s.killers[ply-1] == m)) {
		return 1 << 18
	}
	return s.history[int(m.From)<<8|int(m.To)]
}

func (s *searcher) sortMoves(moves []game.Move, ttMove game.Move, ply int) {
	scores := make([]int32, len(moves))
	for i, m := range moves {
		scores[i] = s.scoreMove(m, ttMove, ply)
	}
	sort.SliceStable(moves, func(i, j int) bool { return scores[i] > scores[j] })
}

// AlphaBeta 主搜索（A8）：伪合法生成 + make 后己方被将军过滤。
func (s *searcher) AlphaBeta(depth, ply int, alpha, beta int16) int16 {
	if ply >= maxPly-1 {
		return s.EvaluateRel()
	}
	s.checkTime()
	if s.stopped {
		return 0
	}
	origAlpha := alpha

	var ttMove game.Move
	if e, ok := s.tt[s.pos.Key]; ok && ply > 0 {
		ttMove = e.move
		if int(e.depth) >= depth {
			switch e.flag {
			case ttExact:
				return e.score
			case ttLower:
				if e.score > alpha {
					alpha = e.score
				}
			case ttUpper:
				if e.score < beta {
					beta = e.score
				}
			}
			if alpha >= beta {
				return e.score
			}
		}
	}

	if s.pos.InCheck(s.pos.Turn) {
		depth++ // 将军延伸
	}

	// 局面重复：长将判负（当前走方是长将方对手 → 胜），普通重复按和棋分，
	// 防止 AI 在劣势下用"长将逼和"逃避败局。
	if ply >= 2 && s.pos.RepetitionCount() >= 3 {
		if w, ok := s.pos.LongCheckWinner(); ok {
			winColor := game.Red
			if w == game.ResultBlackWin {
				winColor = game.Black
			}
			if winColor == s.pos.Turn {
				return mateScore - int16(ply)
			}
			return -mateScore + int16(ply)
		}
		return 0 // 普通重复 → 和棋分
	}

	if depth <= 0 {
		return s.Quiescence(alpha, beta)
	}

	moves := s.pos.GenMoves(s.pos.Turn)
	s.sortMoves(moves, ttMove, ply)

	legalCount := 0
	best := int16(-infScore)
	var bestMove game.Move
	for _, m := range moves {
		s.pos.Make(m)
		mover := game.Opponent(s.pos.Turn) // Make 后 Turn 已切换
		if s.pos.InCheck(mover) {          // 走完己方仍被将军 → 非法着法
			s.pos.Unmake()
			continue
		}
		legalCount++
		score := -s.AlphaBeta(depth-1, ply+1, -beta, -alpha)
		s.pos.Unmake()
		if s.stopped {
			return 0
		}
		if score > best {
			best, bestMove = score, m
		}
		if score > alpha {
			alpha = score
		}
		if alpha >= beta {
			if s.pos.Board[m.To] == game.Empty && ply < maxPly {
				s.killers[ply] = m
				s.history[int(m.From)<<8|int(m.To)] += int32(depth * depth)
			}
			break
		}
	}
	if legalCount == 0 {
		return -mateScore + int16(ply) // 将死/困毙同判负，越浅分越高
	}
	flag := ttExact
	if best <= origAlpha {
		flag = ttUpper
	} else if best >= beta {
		flag = ttLower
	}
	s.tt[s.pos.Key] = ttEntry{int8(depth), uint8(flag), best, bestMove}
	return best
}

// Quiescence 静态搜索（A9）：只延伸吃子，消除水平线效应。
func (s *searcher) Quiescence(alpha, beta int16) int16 {
	s.checkTime()
	if s.stopped {
		return 0
	}
	stand := s.EvaluateRel()
	if stand >= beta {
		return stand
	}
	if stand > alpha {
		alpha = stand
	}

	mover := s.pos.Turn
	var captures []game.Move
	for _, m := range s.pos.GenMoves(mover) {
		if s.pos.Board[m.To] != game.Empty {
			captures = append(captures, m)
		}
	}
	sort.SliceStable(captures, func(i, j int) bool { // MVV-LVA 降序
		vi := pieceValue[game.TypeOf(s.pos.Board[captures[i].To])]
		vj := pieceValue[game.TypeOf(s.pos.Board[captures[j].To])]
		return vi > vj
	})
	for _, m := range captures {
		s.pos.Make(m)
		if s.pos.InCheck(mover) {
			s.pos.Unmake()
			continue
		}
		score := -s.Quiescence(-beta, -alpha)
		s.pos.Unmake()
		if s.stopped {
			return 0
		}
		if score >= beta {
			return score
		}
		if score > alpha {
			alpha = score
		}
	}
	return alpha
}

// BestMove 迭代加深主循环（A7）。
func (e *SimpleEngine) BestMove(ctx context.Context, pos *game.Position, level int) (game.Move, error) {
	cfg := cfgOf(level)
	legal := pos.LegalMoves(pos.Turn)
	if len(legal) == 0 {
		return game.Move{}, fmt.Errorf("无合法着法")
	}
	if len(legal) == 1 {
		return legal[0], nil
	}

	now := time.Now()
	s := &searcher{
		pos:      pos.Clone(),
		tt:       make(map[uint64]ttEntry, 1<<16),
		start:    now,
		deadline: now.Add(cfg.budget),
	}
	rng := rand.New(rand.NewSource(now.UnixNano()))

	// 低档随机性（制造"人味失误"）：全窗口评估所有根着法，从前 3 优着中按概率随机取一
	if cfg.randProb > 0 {
		type scored struct {
			m game.Move
			s int16
		}
		list := make([]scored, 0, len(legal))
		for _, m := range legal {
			s.pos.Make(m)
			sc := -s.AlphaBeta(cfg.maxDepth-1, 1, -infScore, infScore)
			s.pos.Unmake()
			if s.stopped {
				sc = s.EvaluateRel()
			}
			list = append(list, scored{m, sc})
		}
		sort.SliceStable(list, func(i, j int) bool { return list[i].s > list[j].s })
		if rng.Float64() < cfg.randProb && len(list) >= 2 {
			top := list
			if len(top) > 3 {
				top = top[:3]
			}
			return top[rng.Intn(len(top))].m, nil
		}
		return list[0].m, nil
	}

	var best game.Move
	var bestScore int16
	for depth := 1; depth <= cfg.maxDepth; depth++ {
		score, mv := s.rootSearch(depth)
		if s.stopped && best != (game.Move{}) {
			break // 超时：沿用上一层完整结果
		}
		if mv != (game.Move{}) {
			best, bestScore = mv, score
		}
		if bestScore >= mateBound {
			break // 已见杀，无需再深
		}
		if time.Since(s.start) > cfg.budget/2 {
			break // 下一层大概率超时，止损
		}
	}
	if best == (game.Move{}) {
		best = legal[0]
	}
	return best, nil
}

// RankedMoves 返回按优劣排序的前 n 个根着法（全窗口逐着评估，供 LLM 引擎候选模式使用）。
// level 决定搜索深度与预算；结果首元素即该档位最佳着法。
func (e *SimpleEngine) RankedMoves(ctx context.Context, pos *game.Position, level, n int) []game.Move {
	legal := pos.LegalMoves(pos.Turn)
	if len(legal) == 0 {
		return nil
	}
	cfg := cfgOf(level)
	now := time.Now()
	s := &searcher{
		pos:      pos.Clone(),
		tt:       make(map[uint64]ttEntry, 1<<15),
		start:    now,
		deadline: now.Add(cfg.budget),
	}
	depth := cfg.maxDepth
	if depth > 6 {
		depth = 6
	}
	type scored struct {
		m game.Move
		s int16
	}
	list := make([]scored, 0, len(legal))
	for _, m := range legal {
		s.pos.Make(m)
		sc := -s.AlphaBeta(depth-1, 1, -infScore, infScore)
		s.pos.Unmake()
		if s.stopped {
			break
		}
		list = append(list, scored{m, sc})
	}
	if len(list) == 0 {
		return []game.Move{legal[0]}
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].s > list[j].s })
	if n > len(list) {
		n = len(list)
	}
	out := make([]game.Move, n)
	for i := 0; i < n; i++ {
		out[i] = list[i].m
	}
	return out
}

func (s *searcher) rootSearch(depth int) (int16, game.Move) {
	alpha, beta := int16(-infScore), int16(infScore)
	var ttMove game.Move
	if e, ok := s.tt[s.pos.Key]; ok {
		ttMove = e.move
	}
	moves := s.pos.LegalMoves(s.pos.Turn)
	s.sortMoves(moves, ttMove, 0)

	best := int16(-infScore)
	var bestMove game.Move
	for _, m := range moves {
		s.pos.Make(m)
		score := -s.AlphaBeta(depth-1, 1, -beta, -alpha)
		s.pos.Unmake()
		if s.stopped {
			return best, bestMove
		}
		if score > best {
			best, bestMove = score, m
		}
		if score > alpha {
			alpha = score
		}
	}
	return best, bestMove
}
