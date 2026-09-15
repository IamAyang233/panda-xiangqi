// Package search 实现 alpha-beta 搜索。
//
// 依赖规则层（internal/game）与评估层（internal/nnue）。
// 结构：迭代加深 → alpha-beta（PVS）→ 静态搜索（quiescence），
// 外层可选 Lazy SMP 多线程（parallel.go）与时间预算（timemgr.go）。
//
// 评估用增量累加器（nnue.SyncTo），实测比全量重建快约 4 倍；
// 档位到搜索参数的映射见 level.go。
package search

import (
	"sync/atomic"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/nnue"
)

// 分值常量。单位与 C++ 的 Value 一致。
const (
	// Infinity 大于任何真实评估值，用于 alpha-beta 的初始窗口。
	Infinity = 1 << 20
	// MateScore 表示将杀基准分，实际分值加上 ply 以偏好更快的杀。
	MateScore = 1 << 19
	// MaxPly 是搜索的最大层数，防止非法局面导致栈溢出。
	MaxPly = 128

	// maxQuiescePly 限制静态搜索的最大延伸层数，避免无限将军链。
	maxQuiescePly = 24

	// historyMax 是历史启发的上限，必须显著小于吃子排序的基准分。
	historyMax = 1 << 18
)

// pieceValue 是着法排序用的子力价值，索引为 game 的棋子类型编码。
var pieceValue = [8]int{0, 10000, 200, 200, 400, 900, 450, 100}

// deltaMargin 是静态搜索里 delta pruning 的缓冲值。
//
// 判据是：即便白吃到目标格里最值钱的子，「静态评估 + 该子价值 + 缓冲」
// 仍追不上 alpha，才判定这一支毫无希望并剪掉。
// 留出缓冲是为了吸收评估误差，免得误砍那些先弃后取会盈利的吃子链。
const deltaMargin = 200

// Searcher 持有权重与可复用的缓冲，非并发安全 ——
// 多线程时每个线程一个实例，共享同一个 TranspositionTable 与 stop 标志
// （见 parallel.go）。
type Searcher struct {
	w   *nnue.Weights
	acc nnue.Accumulator

	// pos 是评估侧的增量局面，与 game 局面用同一步走法同步驱动：
	// 这样特征枚举与累加器更新都不需要每次扫全盘。
	pos nnue.Position
	tt  *TranspositionTable

	killers [MaxPly][2]game.Move
	history [90][90]int
	nonPawn [2]int // 双方非兵子力数，evaluate 时顺带统计

	scoreBuf [MaxPly][]int

	nodes     int64
	ttHits    int64
	nullMoves int64
	noNull    bool

	// probeCnt 是真正可用作分母的置换表统计：probe 只发生在 depth>0 的节点
	// （它在 depth<=0 转静态搜索的分支之后），所以命中率必须是
	// ttHits/probeCnt。用总节点数当分母会把从不查表的静态搜索节点混进来，
	// 把命中率稀释到原本的几分之一，极易误判成「置换表失效」。
	probeCnt  int64
	abCallCnt int64
	storeCnt  int64

	// stop 是中止标志。多线程时所有线程共享同一个实例，任一线程（或计时器）
	// 置位后全体尽快退出。alphaBeta 每节点读一次，所以用原子变量。
	stop *atomic.Bool
}

// Result 是一次搜索的结果。
type Result struct {
	Best  game.Move
	Score int
	Depth int
	Nodes int64
	// Roots 是根节点各着法及其分值，按分值降序。
	// 低档位用它在前几个着法里随机挑，以制造「人味失误」。
	Roots []RootMove
}

// RootMove 是根节点一个着法及其搜索分值。
type RootMove struct {
	Move  game.Move
	Score int
}

// New 构造搜索器，带默认大小的置换表。
func New(w *nnue.Weights) *Searcher {
	return &Searcher{
		w:    w,
		tt:   NewTranspositionTable(DefaultTTSizeMB),
		stop: &atomic.Bool{},
	}
}

// NewWithTT 构造指定置换表大小（兆字节）的搜索器；sizeMB <= 0 时用默认值。
func NewWithTT(w *nnue.Weights, sizeMB int) *Searcher {
	s := New(w)
	if sizeMB > 0 {
		s.tt = NewTranspositionTable(sizeMB)
	}
	return s
}

// SetTTSizeMB 重建置换表并清空启发式表。
func (s *Searcher) SetTTSizeMB(sizeMB int) {
	s.tt = NewTranspositionTable(sizeMB)
	s.Clear()
}

// Clear 清空置换表与启发式表（每次新搜索开始前调用，保证结果可复现）。
func (s *Searcher) Clear() {
	if s.tt != nil {
		s.tt.Clear()
	}
	s.clearHeuristics()
}

// clearHeuristics 清空线程私有的启发式表与统计，不碰共享的置换表。
// 多线程时由池统一清一次表，各线程各自调这个。
func (s *Searcher) clearHeuristics() {
	s.killers = [MaxPly][2]game.Move{}
	s.history = [90][90]int{}
	s.resetStats()
}

// resetStats 只重置计数，保留 killers/history。
//
// 连续对弈时每步都该保留启发式经验（它们按着法/局面索引，跨步复用有效），
// 但统计量要按次归零才能看清当前这一步的效率。
func (s *Searcher) resetStats() {
	s.ttHits = 0
	s.nullMoves = 0
	s.probeCnt = 0
	s.abCallCnt = 0
	s.storeCnt = 0
}

// Stop 请求中止当前搜索（超时或上层取消）。多线程下共享同一标志。
func (s *Searcher) Stop() {
	if s.stop != nil {
		s.stop.Store(true)
	}
}

// TTHits 返回本次搜索的置换表命中次数。
func (s *Searcher) TTHits() int64 { return s.ttHits }

// NullMoves 返回本次搜索实际尝试的空着次数。
func (s *Searcher) NullMoves() int64 { return s.nullMoves }

// DisableTT 关闭置换表，供对照实验使用（nil 表下探测恒为未命中）。
func (s *Searcher) DisableTT() { s.tt = nil }

// DisableNullMove 关闭空着剪枝，供对照实验使用。
func (s *Searcher) DisableNullMove() { s.noNull = true }

// ttProbe / ttStore 在置换表被禁用时退化为空操作。
func (s *Searcher) ttProbe(key uint64) (ttEntry, bool) {
	if s.tt == nil {
		return ttEntry{}, false
	}
	return s.tt.probe(key)
}

func (s *Searcher) ttStore(key uint64, move game.Move, score int32, depth int, flag uint8) {
	if s.tt == nil {
		return
	}
	s.tt.store(key, move, score, depth, flag)
}

// prepare 把评估侧局面与累加器对齐到搜索起点。
//
// 每次新搜索都必须调用：pos 要与传入局面一致，acc 要失效（它可能还缓存着
// 上一局或上一次搜索的局面，残留的 meta 会让增量路径拿错基准做差集）。
func (s *Searcher) prepare(p *game.Position) {
	s.pos.ResetFromGame(&p.Board, p.Turn>>3)
	s.acc.Invalidate()
}

// Search 从 depth 1 开始迭代加深到 maxDepth，返回最后完成的一层结果。
//
// 只采用**完整跑完**的那一层：被中止的层结果作废（半途而废的 alpha-beta
// 给出的分值不可信）。
func (s *Searcher) Search(p *game.Position, maxDepth int) Result {
	s.stop.Store(false)
	s.nodes = 0
	s.Clear()
	return s.searchLoop(p, TimeLimit{}, maxDepth, false)
}

// SearchDepth 只搜固定深度，不做迭代加深（用于逐层对拍）。
func (s *Searcher) SearchDepth(p *game.Position, depth int) Result {
	s.stop.Store(false)
	s.nodes = 0
	s.Clear()
	s.prepare(p)
	roots, ok := s.rootSearch(p, depth)
	if !ok || len(roots) == 0 {
		return Result{Nodes: s.nodes}
	}
	return Result{
		Best:  roots[0].Move,
		Score: roots[0].Score,
		Depth: depth,
		Nodes: s.nodes,
		Roots: roots,
	}
}

// Nodes 返回已搜索的节点数。
func (s *Searcher) Nodes() int64 { return s.nodes }

// TTLen 返回置换表容量（项数）；表被禁用时返回 0。
func (s *Searcher) TTLen() int {
	if s.tt == nil {
		return 0
	}
	return s.tt.Len()
}

// rootSearch 搜索根节点，返回按分值降序的根着法列表与「是否有合法着法」。
//
// 根节点不剪枝（每个着法都要有分值），因为低档位要按分值在前几个着法里
// 随机挑 —— 这需要知道除最优之外的着法有多好。分值是 fail-soft 语义，
// 未超过 alpha 的着法返回的是其子树实际搜到的最大值（真实值的上界），
// 用于排序足够。
func (s *Searcher) rootSearch(p *game.Position, depth int) ([]RootMove, bool) {
	moves := p.LegalMoves(p.Turn)
	if len(moves) == 0 {
		return nil, false
	}
	var ttMove game.Move
	if e, ok := s.ttProbe(p.Key); ok {
		ttMove = decodeMove(e.move)
	}
	s.orderMoves(p, moves, 0, ttMove)

	roots := make([]RootMove, 0, len(moves))
	best := game.Move{}
	bestScore := -Infinity
	alpha := -Infinity

	for i, m := range moves {
		if s.stopped() {
			return roots, true // 中止：本层作废，由调用方丢弃
		}
		victim := p.PieceAt90(int(m.To))
		p.Make(m)
		s.pos.Make(int(m.From), int(m.To))

		var score int
		if i == 0 {
			score = -s.alphaBeta(p, depth-1, -Infinity, -alpha, 1, true, true)
		} else {
			// 根节点同样走 PVS + LMR：先窄窗口试探，必要时重搜。
			red := s.reduction(depth, i, victim, false)
			score = -s.alphaBeta(p, depth-1-red, -alpha-1, -alpha, 1, false, true)
			if score > alpha && red > 0 {
				score = -s.alphaBeta(p, depth-1, -alpha-1, -alpha, 1, false, true)
			}
			if score > alpha {
				score = -s.alphaBeta(p, depth-1, -Infinity, -alpha, 1, true, true)
			}
		}
		p.Unmake()
		s.pos.Unmake()

		roots = append(roots, RootMove{Move: m, Score: score})
		if score > bestScore {
			bestScore, best = score, m
		}
		if score > alpha {
			alpha = score
		}
	}
	if s.stopped() {
		return roots, true
	}

	sortRootsDesc(roots)
	s.ttStore(p.Key, best, scoreToTT(bestScore, 0), depth, ttExact)
	return roots, true
}

// sortRootsDesc 按分值降序排列根着法；同分保持原顺序（原顺序来自着法排序，
// 同分时它更可信）。
func sortRootsDesc(roots []RootMove) {
	// 插入排序：根着法数通常不超过 60，且基本已按强弱排列。
	for i := 1; i < len(roots); i++ {
		v := roots[i]
		j := i - 1
		for j >= 0 && roots[j].Score < v.Score {
			roots[j+1] = roots[j]
			j--
		}
		roots[j+1] = v
	}
}

// stopped 返回是否已被要求中止（超时或上层取消）。
func (s *Searcher) stopped() bool {
	return s.stop != nil && s.stop.Load()
}

// alphaBeta 是 negamax 形式的 alpha-beta，返回**走子方视角**的分值。
//
// isPV 标记主变例节点：只有非 PV 节点才允许直接用置换表的分值剪枝，
// 因为 PV 节点的分值受窗口影响，直接返回会截断主变例。
// canNull 标记允许空着剪枝（连续两次空着会退化成无意义的搜索，须禁止）。
func (s *Searcher) alphaBeta(p *game.Position, depth, alpha, beta, ply int, isPV, canNull bool) int {
	// 每节点读一次中止标志：原子读比取时钟便宜得多，所以可以查得很密。
	if s.stopped() {
		return 0
	}
	s.abCallCnt++
	s.nodes++
	if ply >= MaxPly-1 {
		return s.evaluate(p)
	}

	if ply > 0 {
		// RepetitionCount 含当前局面，首次出现返回 1，所以 >1 才是真的重复。
		if p.RepetitionCount() > 1 {
			return 0
		}
		if p.Halfmove >= 120 { // 60 回合自然限着
			return 0
		}
	}

	// 将杀距离剪枝：已经找到更短的杀时不必再搜。
	if alpha < -MateScore+ply {
		alpha = -MateScore + ply
	}
	if beta > MateScore-ply-1 {
		beta = MateScore - ply - 1
	}
	if alpha >= beta {
		return alpha
	}

	if depth <= 0 {
		return s.quiesce(p, alpha, beta, ply)
	}

	inCheck := p.InCheck(p.Turn)

	var ttMove game.Move
	s.probeCnt++
	if e, ok := s.ttProbe(p.Key); ok {
		s.ttHits++
		ttMove = decodeMove(e.move)
		if !isPV && int(e.depth) >= depth {
			sc := scoreFromTT(e.score, ply)
			switch e.flag {
			case ttExact:
				return sc
			case ttLower:
				if sc >= beta {
					return sc
				}
			case ttUpper:
				if sc <= alpha {
					return sc
				}
			}
		}
	}

	// 空着剪枝：让对手连走两步仍不能改善，说明这个分支已经足够好。
	// 被将军时空着不合法；子力稀薄时禁用，避免残局的 zugzwang 误判。
	//
	// 条件顺序有讲究：s.nonPawn 由 evaluate 顺带统计，所以必须先调 evaluate
	// 再读它（&& 从左到右短路求值保证了这一点），否则读到的是上一个节点的旧值。
	if !s.noNull && canNull && !inCheck && depth >= 3 && s.evaluate(p) >= beta && s.nonPawn[p.Turn>>3] >= 2 {
		red := 2 + depth/6
		s.nullMoves++
		p.MakeNull()
		s.pos.MakeNull()
		score := -s.alphaBeta(p, depth-1-red, -beta, -beta+1, ply+1, false, false)
		p.UnmakeNull()
		s.pos.UnmakeNull()
		if score >= beta && score < MateScore-MaxPly {
			return score
		}
	}

	moves := p.LegalMoves(p.Turn)
	if len(moves) == 0 {
		// 中国象棋里无着法可走即为负，被将死与困毙同判。
		return -MateScore + ply
	}

	s.orderMoves(p, moves, ply, ttMove)

	best := -Infinity
	bestMove := game.Move{}
	origAlpha := alpha

	for i, m := range moves {
		if s.stopped() {
			return best
		}
		victim := p.PieceAt90(int(m.To))
		p.Make(m)
		s.pos.Make(int(m.From), int(m.To))

		var score int
		if i == 0 {
			score = -s.alphaBeta(p, depth-1, -beta, -alpha, ply+1, isPV, true)
		} else {
			red := s.reduction(depth, i, victim, inCheck)
			score = -s.alphaBeta(p, depth-1-red, -alpha-1, -alpha, ply+1, false, true)
			if score > alpha && red > 0 {
				score = -s.alphaBeta(p, depth-1, -alpha-1, -alpha, ply+1, false, true)
			}
			if score > alpha && score < beta {
				score = -s.alphaBeta(p, depth-1, -beta, -alpha, ply+1, isPV, true)
			}
		}
		p.Unmake()
		s.pos.Unmake()

		if score > best {
			best, bestMove = score, m
		}
		if score > alpha {
			alpha = score
		}
		if alpha >= beta {
			if victim == game.Empty {
				s.updateQuietStats(m, ply, depth)
			}
			break
		}
	}

	if s.stopped() {
		return best // 中止时不要写表：半途的分值会污染后续搜索
	}
	flag := ttExact
	if best <= origAlpha {
		flag = ttUpper
	} else if best >= beta {
		flag = ttLower
	}
	s.storeCnt++
	s.ttStore(p.Key, bestMove, scoreToTT(best, ply), depth, flag)
	return best
}

// reduction 计算后期着法削减量（LMR）。返回 0 表示不削减。
//
// 只削减“安静着法”（非吃子）：吃子着法价值高、容错低，削减容易漏掉战术。
// 被将军时不削减，否则会漏掉唯一的解将着法。
func (s *Searcher) reduction(depth, index int, victim byte, inCheck bool) int {
	if depth < 3 || index < 4 || victim != game.Empty || inCheck {
		return 0
	}
	red := 1 + index/8
	if red > depth-2 {
		red = depth - 2
	}
	if red < 0 {
		red = 0
	}
	return red
}

// quiesce 是静态搜索：只展开吃子，消除“评估在吃子中途截断”的地平线效应。
//
// 被将军时改用全宽搜索 —— 此时只搜吃子会漏掉解将着法，把被杀误判为安全。
func (s *Searcher) quiesce(p *game.Position, alpha, beta, ply int) int {
	if s.stopped() {
		return 0
	}
	s.nodes++
	if ply >= MaxPly-1 || ply >= maxQuiescePly {
		return s.evaluate(p)
	}
	if p.RepetitionCount() > 1 {
		return 0
	}

	inCheck := p.InCheck(p.Turn)

	best := -Infinity
	if !inCheck {
		stand := s.evaluate(p)
		if stand >= beta {
			return stand
		}
		if stand > alpha {
			alpha = stand
		}
		best = stand
	}

	moves := p.LegalMoves(p.Turn)
	if len(moves) == 0 {
		return -MateScore + ply
	}

	if !inCheck {
		captures := moves[:0]
		maxVictim := 0
		for _, m := range moves {
			if v := p.PieceAt90(int(m.To)); v != game.Empty {
				captures = append(captures, m)
				if val := pieceValue[game.TypeOf(v)]; val > maxVictim {
					maxVictim = val
				}
			}
		}
		moves = captures
		if len(moves) == 0 {
			return best
		}
		// delta pruning：即使吃掉这一格里最值钱的子也追不上 alpha，
		// 这一支就不可能优于已有选择，直接返回。这是静态搜索最便宜
		// 也最有效的剪枝之一 —— 缺了它，大量毫无希望的吃子链会被展开。
		if best+maxVictim+deltaMargin < alpha {
			return best
		}
	}

	s.orderMoves(p, moves, ply, game.Move{})

	for _, m := range moves {
		p.Make(m)
		s.pos.Make(int(m.From), int(m.To))
		score := -s.quiesce(p, -beta, -alpha, ply+1)
		p.Unmake()
		s.pos.Unmake()

		if score > best {
			best = score
		}
		if score > alpha {
			alpha = score
		}
		if alpha >= beta {
			break
		}
	}
	return best
}

// evaluate 返回走子方视角的 NNUE 评估值。
//
// 用增量同步（SyncTo）而非全量重建：累加器自己记住上次的特征集合，
// 走一步通常只改变几个特征，实测比全量 Refresh 快约 4 倍。
//
// 顺带统计双方非兵子力数（null-move 的 zugzwang 保护要用），
// 反正这里已经遍历全盘，单独再扫一遍不值。
func (s *Searcher) evaluate(_ *game.Position) int {
	// 子力计数由增量局面直接给出（O(1)），不必再扫全盘。
	s.nonPawn[0] = s.pos.NonPawnCount(0)
	s.nonPawn[1] = s.pos.NonPawnCount(1)
	s.w.Apply(&s.pos, &s.acc)
	return int(s.w.EvalValueAt(&s.pos, &s.acc))
}

// orderMoves 给着法打分并降序排列（TT 着法 > 吃子 MVV-LVA > 杀手 > 历史启发）。
func (s *Searcher) orderMoves(p *game.Position, moves []game.Move, ply int, ttMove game.Move) {
	if ply >= MaxPly {
		return
	}
	if s.scoreBuf[ply] == nil {
		s.scoreBuf[ply] = make([]int, 0, 128)
	}
	scores := s.scoreBuf[ply][:0]

	for _, m := range moves {
		scores = append(scores, s.moveScore(p, m, ply, ttMove))
	}
	s.scoreBuf[ply] = scores

	// 插入排序：着法数不大，且基本有序时接近线性。
	for i := 1; i < len(moves); i++ {
		mv, sv := moves[i], scores[i]
		j := i - 1
		for j >= 0 && scores[j] < sv {
			moves[j+1], scores[j+1] = moves[j], scores[j]
			j--
		}
		moves[j+1], scores[j+1] = mv, sv
	}
}

// moveScore 是单步着法的排序分。各档之间留出足够间隔，避免互相穿插。
func (s *Searcher) moveScore(p *game.Position, m game.Move, ply int, ttMove game.Move) int {
	if m == ttMove && ttMove != (game.Move{}) {
		return 1 << 24
	}
	victim := p.PieceAt90(int(m.To))
	if victim != game.Empty {
		// MVV-LVA：吃大子优先，用小子吃更优先（减少被反吃损失）。
		return (1 << 20) + pieceValue[game.TypeOf(victim)]*16 - pieceValue[game.TypeOf(p.PieceAt90(int(m.From)))]
	}
	if m == s.killers[ply][0] {
		return (1 << 19) + 1
	}
	if m == s.killers[ply][1] {
		return 1 << 19
	}
	return s.history[m.From][m.To]
}

// updateQuietStats 在安静着法引发 beta 截断时更新杀手与历史启发。
func (s *Searcher) updateQuietStats(m game.Move, ply, depth int) {
	if m != s.killers[ply][0] {
		s.killers[ply][1] = s.killers[ply][0]
		s.killers[ply][0] = m
	}
	bonus := depth * depth
	if v := s.history[m.From][m.To] + bonus; v > historyMax {
		s.history[m.From][m.To] = historyMax
	} else {
		s.history[m.From][m.To] = v
	}
}

// ageHistory 把历史分值减半。迭代加深时调用，让上一层的经验逐渐淡出。
func (s *Searcher) ageHistory() {
	for i := range s.history {
		for j := range s.history[i] {
			s.history[i][j] /= 2
		}
	}
}
