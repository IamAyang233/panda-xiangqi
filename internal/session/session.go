// Package session 对局会话状态机：模式调度 · 着法应用 · 悔棋 · 提示 · 残局进度。
package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/IamAyang233/panda-xiangqi/internal/engine"
	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/llm"
	"github.com/IamAyang233/panda-xiangqi/internal/puzzle"
)

// Mode 对局模式。
const (
	ModeEngine = "engine"
	ModeLLM    = "llm"
	ModeLocal  = "local_2p"
	ModePuzzle = "puzzle"
)

// MoveRecord 着法记录（供 UI 着法列表与断线恢复）。
type MoveRecord struct {
	UCI string `json:"uci"`
	CN  string `json:"cn"`
	Red bool   `json:"red"` // 红方所走
}

// Conn 会话广播通道（由 api 层注入 WS 连接）。
type Conn interface {
	SendJSON(v any)
	Close()
}

// Session 一局对局。
//
// 并发模型：所有状态修改在 s.mu 下进行；持锁阶段只构造消息列表（*Locked 函数返回
// []any），解锁后由 flush 统一发送——避免重入死锁，也避免慢客户端阻塞棋局状态机。
type Session struct {
	ID        string
	Mode      string
	Level     int
	HumanSide int // 人机/残局中人类的执子方（game.Red / game.Black）

	mu          sync.Mutex
	pos         *game.Position
	moves       []MoveRecord
	result      string // game.Result*（空 = 进行中）
	reason      string
	thinking    bool
	cancelThink context.CancelFunc

	// gen 是局面世代，Restart 时递增。异步应着（aiReply）在开始与结束各读一次，
	// 不一致就丢弃结果 —— 否则「刚走完一手立刻重开」会把旧局面的着法落到新局面上。
	// ⚠️ 用世代号而不是复用 thinking 标志：thinking 只能表达「有人在算」，
	// 无法区分「谁在算」，旧 goroutine 收尾时就会把新那次的标志清掉。
	gen int

	llmCfg    llm.Config
	llmPlayer *llm.Player // 会话级复用（HTTP keep-alive，避免每步重建连接）
	pz        *puzzle.Puzzle
	pzStep    int // solution 已消费的下标
	// pzConsumed 与 moves 逐手对应：第 i 手是否消费了正解。
	// 悔棋时游标按「实际消费的正解手数」重算，不能按「撤了几手」硬减 ——
	// 偏离正解后的手不消费正解，硬减会把游标推到局面之前，玩家之后**走对**
	// 反而被判偏离正解（静默误判）。用逐手记账而非「停止下标」是因为双方在
	// 正解耗尽时的行为并不对称（玩家侧仍计消费、守方侧不计），单一 stop 下标
	// 无法表达。
	pzConsumed []bool
	pzFail     bool
	hintUsed   bool

	conns   map[Conn]struct{}
	engines *engine.Manager
	created time.Time
	// lastActive 是「最后一次有连接」的时刻，供 Manager 判断闲置。
	// 只看 len(conns)==0 不够：客户端断网/被杀时连接不会立刻注销，
	// 这类会话会一直看起来「有人在用」而永不回收。
	lastActive time.Time
}

// NewSession 创建会话。pz 非空时为残局模式。
func NewSession(mode string, humanSide int, level int, llmCfg llm.Config, pz *puzzle.Puzzle, engines *engine.Manager) *Session {
	s := &Session{
		ID:         newID(),
		Mode:       mode,
		Level:      clampLevel(level),
		HumanSide:  humanSide,
		llmCfg:     llmCfg,
		pz:         pz,
		conns:      make(map[Conn]struct{}),
		engines:    engines,
		created:    time.Now(),
		lastActive: time.Now(),
	}
	if pz != nil {
		pos, err := game.ParseFEN(pz.FEN)
		if err != nil {
			log.Printf("session: 残局 FEN 非法 %s: %v", pz.ID, err)
			pos = game.NewPosition()
		} else if err := pos.LegalPosition(); err != nil {
			// 题库加载（puzzle.NewStore）与 Restart 都已过滤过，这里是最后一道闸：
			// 非法局面会让「吃将」成为合法着法，进而使 kingSq 悬空、整套合法性
			// 判定失真。宁可退回初始局面，也不能让这种棋局跑起来。
			log.Printf("session: 残局局面非法 %s: %v（退回初始局面）", pz.ID, err)
			pos = game.NewPosition()
		}
		s.pos = pos
	} else {
		s.pos = game.NewPosition()
	}
	return s
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func clampLevel(l int) int {
	if l < 1 {
		return 4
	}
	if l > 16 {
		return 16
	}
	return l
}

// Join 注册广播连接并回送完整状态。
func (s *Session) Join(c Conn) {
	s.mu.Lock()
	s.conns[c] = struct{}{}
	s.lastActive = time.Now()
	msg := s.buildStateLocked()
	s.mu.Unlock()
	c.SendJSON(msg)
}

// Leave 注销连接。
func (s *Session) Leave(c Conn) {
	s.mu.Lock()
	delete(s.conns, c)
	// 记下「最后一次有连接的时刻」：Manager 用它判断闲置，而不是看瞬时连接数
	// （客户端断网时连接可能长期不注销）。
	if len(s.conns) == 0 {
		s.lastActive = time.Now()
	}
	s.mu.Unlock()
}

// connSnapshot 持锁取连接快照。
func (s *Session) connSnapshot() []Conn {
	out := make([]Conn, 0, len(s.conns))
	for c := range s.conns {
		out = append(out, c)
	}
	return out
}

// flush 解锁后统一发送。
func (s *Session) flush(msgs []any) {
	if len(msgs) == 0 {
		return
	}
	s.mu.Lock()
	conns := s.connSnapshot()
	s.mu.Unlock()
	for _, c := range conns {
		for _, m := range msgs {
			c.SendJSON(m)
		}
	}
}

// broadcast 单条消息即时广播（调用方不得持有 s.mu）。
func (s *Session) broadcast(v any) { s.flush([]any{v}) }

// BroadcastState 向所有连接推送完整状态（悔棋/重开后同步用）。
func (s *Session) BroadcastState() {
	s.mu.Lock()
	msg := s.buildStateLocked()
	s.mu.Unlock()
	s.broadcast(msg)
}

// ---------------------------------------------------------------- 状态消息

func (s *Session) buildStateLocked() map[string]any {
	msg := map[string]any{
		"type":     "state",
		"fen":      s.pos.FEN(),
		"turn":     sideName(s.pos.Turn),
		"status":   statusName(s.result),
		"mode":     s.Mode,
		"moves":    s.moves,
		"check":    s.result == "" && s.pos.InCheck(s.pos.Turn),
		"result":   s.result,
		"reason":   s.reason,
		"level":    s.Level,
		"thinking": s.thinking,
	}
	if last, ok := s.pos.LastMove(); ok {
		msg["lastMove"] = map[string]any{
			"from": game.SquareName(last.From),
			"to":   game.SquareName(last.To),
		}
	}
	if s.Mode == ModePuzzle && s.pz != nil {
		msg["puzzle"] = map[string]any{
			"id": s.pz.ID, "goal": s.pz.Goal, "playerSide": s.pz.PlayerSide,
			"step":   s.playerMoveCountLocked(),
			"failed": s.pzFail, "hintUsed": s.hintUsed, "parMoves": s.pz.ParMoves,
		}
	}
	return msg
}

// playerMoveCountLocked 玩家（HumanSide）已落子步数，无论正解与否都计数。
// 残局目标栏"已走 N 步"应反映玩家实际着数，而非正解消费进度。
func (s *Session) playerMoveCountLocked() int {
	n := 0
	for _, mv := range s.moves {
		if mv.Red == (s.HumanSide == game.Red) {
			n++
		}
	}
	return n
}

func sideName(c int) string {
	if c == game.Red {
		return "red"
	}
	return "black"
}

func statusName(result string) string {
	if result == "" {
		return "playing"
	}
	return "over"
}

type sessionError struct{ msg string }

func (e *sessionError) Error() string { return e.msg }

func errf(format string, a ...any) error {
	return &sessionError{msg: fmt.Sprintf(format, a...)}
}

// IsBusy 报告会话是否处于 AI 思考中。
func (s *Session) IsBusy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.thinking
}

// LegalTargets 返回 from 格棋子的全部合法落点（规则单一事实来源：前端可落点提示由此查询）。
func (s *Session) LegalTargets(from string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sq, ok := game.SquareFromName(from)
	if !ok {
		return nil, errf("坐标格式错误")
	}
	out := []string{}
	for _, m := range s.pos.LegalMoves(s.pos.Turn) {
		if m.From == sq {
			out = append(out, game.SquareName(m.To))
		}
	}
	return out, nil
}

// ---------------------------------------------------------------- 玩家着法

// ApplyPlayerMove 应用玩家着法（WS move）。
func (s *Session) ApplyPlayerMove(from, to string) error {
	s.mu.Lock()
	if s.result != "" {
		s.mu.Unlock()
		return errf("对局已结束")
	}
	if s.thinking {
		s.mu.Unlock()
		return errf("对方思考中，请稍候")
	}
	if s.Mode != ModeLocal && s.pos.Turn != s.HumanSide {
		s.mu.Unlock()
		return errf("现在轮到对方行棋")
	}

	m, ok := game.MoveFromUCI(from + to)
	if !ok {
		s.mu.Unlock()
		return errf("坐标格式错误")
	}
	if !s.pos.IsLegal(m) {
		s.mu.Unlock()
		return errf("该着法不合法")
	}

	// 残局：校验是否偏离正解（偏离后局面继续，由引擎守方代走，无法获得星级）
	var msgs []any
	needAI := false
	if s.Mode == ModePuzzle && s.pz != nil && !s.pzFail {
		if want := s.solutionMoveAt(s.pzStep); want != "" && want != m.String() {
			s.pzFail = true
			msgs = s.applyMoveLocked(m, false)
			msgs = append(msgs, map[string]any{"type": "puzzle_event", "event": "deviate",
				"message": "偏离正解，可悔棋修正或重开本关"})
			needAI = true
		} else {
			s.pzStep++ // 消费玩家正解着法
			msgs = s.applyMoveLocked(m, true)
		}
	} else {
		msgs = s.applyMoveLocked(m, false)
	}
	if s.result == "" && s.Mode != ModeLocal && s.pos.Turn != s.HumanSide {
		needAI = true
	}
	s.mu.Unlock()

	s.flush(msgs)
	if needAI {
		go s.aiReply()
	}
	return nil
}

func (s *Session) solutionMoveAt(i int) string {
	if s.pz == nil || i < 0 || i >= len(s.pz.Solution) {
		return ""
	}
	return s.pz.Solution[i]
}

// applyMoveLocked 走子并构造广播消息（调用方持锁）。返回待发送消息。
//
// consumedSolution 报告本手是否命中并消费了残局正解（非残局恒为 false）：
// 它会被记进 pzConsumed，供悔棋时按「实际消费的正解手数」精确回退游标。
func (s *Session) applyMoveLocked(m game.Move, consumedSolution bool) []any {
	cn := s.pos.MoveToChinese(m)
	red := s.pos.Turn == game.Red
	s.pos.Make(m)
	s.moves = append(s.moves, MoveRecord{UCI: m.String(), CN: cn, Red: red})
	s.pzConsumed = append(s.pzConsumed, consumedSolution)

	st := s.pos.CheckStatus()
	inCheck := st.Result == "" && s.pos.InCheck(s.pos.Turn)

	base := map[string]any{
		"from": game.SquareName(m.From), "to": game.SquareName(m.To),
		"cn": cn, "check": inCheck,
	}
	byHuman := red == (s.HumanSide == game.Red)
	switch s.Mode {
	case ModeEngine, ModePuzzle:
		base["type"], base["byHuman"] = "engine_move", byHuman
	case ModeLLM:
		base["type"], base["byHuman"] = "llm_move", byHuman
	default:
		base["type"] = "move"
	}
	if s.Mode == ModePuzzle && s.pz != nil {
		// 走子消息直接携带玩家已走步数（无论正解与否），前端目标栏无需等 state 全量同步即可刷新。
		base["step"] = s.playerMoveCountLocked()
	}
	msgs := []any{base}
	if inCheck {
		msgs = append(msgs, map[string]any{"type": "check", "side": sideName(s.pos.Turn)})
	}
	if st.Result != "" {
		msgs = append(msgs, s.finishLocked(st.Result, st.Reason)...)
	}
	return msgs
}

// finishLocked 结束对局（调用方持锁）。返回 game_over 消息。
func (s *Session) finishLocked(result, reason string) []any {
	s.result = result
	s.reason = reason
	msg := map[string]any{"type": "game_over", "result": result, "reason": reason}
	if s.Mode == ModePuzzle && s.pz != nil {
		cleared := false
		if s.pz.Goal == "draw" {
			// 和棋关：终局为和棋即算通过（含三次重复 / 60 回合 / 子力不足）。
			cleared = result == game.ResultDraw
		} else {
			// 胜负关：玩家（执子方）将死、困毙对方，或对方长将被判负，即算通过。
			// 长将判负（ReasonLongCheck）也是玩家按棋规取胜，漏掉它会让「靠长将
			// 取胜」的玩家拿到 0 星并被显示为「挑战失败」。
			playerWon := (result == game.ResultRedWin && s.HumanSide == game.Red) ||
				(result == game.ResultBlackWin && s.HumanSide == game.Black)
			cleared = playerWon && (reason == game.ReasonCheckmate ||
				reason == game.ReasonStalemate ||
				reason == game.ReasonLongCheck)
		}
		if cleared {
			playerMoves := 0
			for _, mv := range s.moves {
				if mv.Red == (s.HumanSide == game.Red) {
					playerMoves++
				}
			}
			switch {
			// 偏离正解或用过提示：封顶 1 星；否则按步数给 2/3 星。
			case s.pzFail || s.hintUsed:
				msg["stars"] = 1
			case playerMoves <= s.pz.ParMoves:
				msg["stars"] = 3
			default:
				msg["stars"] = 2
			}
		}
	}
	return []any{msg}
}

// ---------------------------------------------------------------- AI 应着

// aiReply AI（引擎/LLM/残局守方）应着，goroutine 中运行。
func (s *Session) aiReply() {
	s.mu.Lock()
	if s.result != "" {
		s.mu.Unlock()
		return
	}
	// gen 是「局面世代」：Restart 会递增它。本 goroutine 算完回来时若已变，
	// 说明期间重开过 —— 这次应着算的是**旧局面**，必须整体作废。
	gen := s.gen
	s.thinking = true
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	s.cancelThink = cancel
	side := s.pos.Turn
	// 残局守方优先走记录的正解应着
	solUCI := ""
	if s.Mode == ModePuzzle && s.pz != nil && !s.pzFail {
		solUCI = s.solutionMoveAt(s.pzStep)
	}
	s.mu.Unlock()
	s.broadcast(map[string]any{"type": "engine_thinking", "side": sideName(side)})

	start := time.Now()
	var (
		mv       game.Move
		comment  string
		fallback bool
		err      error
	)
	switch {
	case solUCI != "":
		if m, ok := game.MoveFromUCI(solUCI); ok {
			mv = m
		} else {
			err = fmt.Errorf("正解着法非法: %s", solUCI)
		}
	case s.Mode == ModeLLM:
		if s.llmPlayer == nil {
			s.llmPlayer = llm.NewPlayer(s.llmCfg)
		}
		player := s.llmPlayer
		// 引擎候选模式：先由本地引擎排序前 8 候选（低深度，快速），
		// 模型只需在强着中挑选并解说——更快、棋力更高。
		// （此阶段未持锁；最终落子前仍会重校验局面。）
		var candidates []game.Move
		if s.llmCfg.EngineAssist {
			candidates = s.engines.RankedMoves(ctx, s.snapshot(), 5, 8)
		}
		var res llm.Result
		res, err = player.BestMove(ctx, s.snapshot(), candidates)
		mv, comment, fallback = res.Move, res.Comment, res.Fallback
	case s.Mode == ModeEngine || s.Mode == ModePuzzle:
		mv, err = s.engines.BestMove(ctx, s.snapshot(), s.puzzleLevel())
	default:
		mv, err = s.engines.BestMove(ctx, s.snapshot(), s.puzzleLevel())
	}
	if elapsed := time.Since(start); elapsed < 600*time.Millisecond {
		time.Sleep(600*time.Millisecond - elapsed) // 思考动画保底时长
	}
	cancel()

	s.mu.Lock()
	if s.gen != gen {
		// 期间发生过重开（见 Restart）：这次应着算的是旧局面，丢弃。
		//
		// ⚠️ 这里**不能**碰 thinking / cancelThink：它们要么已被 Restart 重置，
		// 要么属于「重开之后由 StartIfAIToMove 新发起的那一次应着」——
		// 碰了就会把新那次认领的标志清掉，让玩家在 AI 还在算的时候就能走子。
		s.mu.Unlock()
		return
	}
	s.thinking = false
	s.cancelThink = nil
	if s.result != "" || s.pos.Turn == s.HumanSide {
		s.mu.Unlock()
		return
	}
	var msgs []any
	if !s.pos.IsLegal(mv) {
		log.Printf("session %s: AI 出着异常（%v），改走首个合法着法", s.ID, err)
		legal := s.pos.LegalMoves(s.pos.Turn)
		if len(legal) == 0 {
			st := s.pos.CheckStatus()
			msgs = s.finishLocked(st.Result, st.Reason)
			s.mu.Unlock()
			s.flush(msgs)
			return
		}
		mv = legal[0]
	}
	consumedSol := false
	if s.Mode == ModePuzzle && s.pz != nil && !s.pzFail {
		switch {
		case solUCI == "":
			// 已偏离正解：守方由引擎自由应着，游标不推进。
		case mv.String() == solUCI:
			s.pzStep++ // 消费守方正解应着
			consumedSol = true
		default:
			// ⚠️ 关卡正解**不可用**：记录着法解析不出、或在当前局面非法，
			// 上面 `IsLegal` 兜底后实际走的是替代着法。
			//
			// 此时绝不能推进游标 —— 否则游标与实际局面错位，玩家之后**走对**
			// 也会被判「偏离正解」（静默误判，且没人知道是数据问题）。
			// 数据来源：内置残局由 cmd/puzzle-check 校验过，但 **LoadDir 允许
			// 用户自带目录**，那条路径没有任何校验。
			s.pzFail = true
			log.Printf("session %s: 残局 %s 第 %d 手正解不可用（%q），改由引擎应着",
				s.ID, s.pz.ID, s.pzStep, solUCI)
			msgs = append(msgs, map[string]any{"type": "puzzle_event", "event": "solution_broken",
				"message": "本关正解数据有误，已转为自由对弈（本局不计星级）"})
		}
	}
	if fallback {
		msgs = append(msgs, map[string]any{"type": "llm_fallback", "by": "local_engine"})
	}
	msgs = append(msgs, s.applyMoveLocked(mv, consumedSol)...)
	// LLM 模式：**即使模型没给棋评也要下发一次**（comment 为空串）。
	// 前端是「替换」气泡内容的，不下发就会一直留着上一手的解说，
	// 看起来像在描述当前这一手；空串让前端把气泡复位。
	if s.Mode == ModeLLM {
		msgs = append(msgs, map[string]any{"type": "llm_comment", "comment": comment})
	}
	s.mu.Unlock()
	s.flush(msgs)
}

// snapshot 持锁克隆局面。
func (s *Session) snapshot() *game.Position {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pos.Clone()
}

// MoveCount 已走着数（测试/监控用）。
func (s *Session) MoveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.moves)
}

// SnapshotFEN 当前局面 FEN（测试/监控用）。
func (s *Session) SnapshotFEN() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pos.FEN()
}

// puzzleLevel 残局守方难度：按残局级别映射引擎档位。
func (s *Session) puzzleLevel() int {
	if s.pz == nil {
		return s.Level
	}
	switch s.pz.Difficulty {
	case "入门":
		return 2
	case "初级":
		return 3
	case "中级":
		return 5
	case "高级":
		return 7
	default:
		return 9
	}
}

// ---------------------------------------------------------------- 悔棋/提示/认输/重开

// Undo 悔棋：人机/残局回退一整个回合（轮到人类时），双人回退一步。
func (s *Session) Undo() error {
	s.mu.Lock()
	if s.thinking {
		s.mu.Unlock()
		return errf("对方思考中，无法悔棋")
	}
	if s.result != "" && s.Mode != ModePuzzle {
		s.mu.Unlock()
		return errf("对局已结束")
	}
	if len(s.moves) == 0 {
		s.mu.Unlock()
		return errf("没有可悔的着法")
	}

	n := 1
	if s.Mode != ModeLocal && s.pos.Turn == s.HumanSide && len(s.moves) >= 2 {
		n = 2
	}
	// 收集被撤着法（含 from/to/吃子），供前端播反向动画。
	type undoMove struct {
		From     string `json:"from"`
		To       string `json:"to"`
		CN       string `json:"cn"`
		Captured string `json:"captured,omitempty"` // 被吃子 FEN 字符（空=无吃子）
	}
	var undone []undoMove
	for i := 0; i < n && len(s.moves) > 0; i++ {
		last := s.moves[len(s.moves)-1]
		mv, ok := game.MoveFromUCI(last.UCI)
		var um undoMove
		if ok {
			um = undoMove{
				From: game.SquareName(mv.From), To: game.SquareName(mv.To),
				CN: last.CN,
			}
			// 真正的被吃子：从位置历史栈栈顶读取（game.Make 走子时压入的
			// histEntry.captured），而非 s.pos.Board[mv.To]——后者在该步走完后
			// 站的是“走子方自己”，会误把走子棋当作被吃子，导致前端在落点凭空
			// 复原一颗棋子（如红兵闪现后随 setFEN 重建而消失）。
			// 引擎内部用编码字节存棋子，需经 PieceToFen 转为标准 FEN 字符再下发，
			// 否则前端按 FEN 字符解析会落空、默认成红兵。
			if capPiece := s.pos.LastCaptured(); capPiece != game.Empty {
				um.Captured = string(game.PieceToFen(capPiece))
			}
		} else {
			um = undoMove{From: last.UCI[:4], To: last.UCI[:4], CN: last.CN}
		}
		undone = append(undone, um)
		s.pos.Unmake()
		s.moves = s.moves[:len(s.moves)-1]
		if len(s.pzConsumed) > 0 {
			s.pzConsumed = s.pzConsumed[:len(s.pzConsumed)-1]
		}
	}
	if s.Mode == ModePuzzle {
		s.pzFail = false
		// ⚠️ 游标不能按「撤了几手」硬减：偏离正解后的手不消费正解，减多了会让
		// 游标落到局面之前，玩家之后**走对**也被判「偏离正解」。这里按剩余着法里
		// 实际消费掉的正解手数重算（pzStep 恒等于已消费正解手数）。
		s.pzStep = 0
		for _, consumed := range s.pzConsumed {
			if consumed {
				s.pzStep++
			}
		}
	}
	if s.result != "" { // 悔棋复活对局（残局重试场景）
		s.result = ""
		s.reason = ""
	}
	msgs := []any{
		map[string]any{"type": "undo_result", "ok": true, "moves": undone},
		s.buildStateLocked(),
	}
	// 悔棋后若又轮到 AI，必须重新拉应着：撤掉的可能正是 AI 的开局首手
	// （人执黑时 len(moves)==1 ⇒ n=1）。Undo 是唯一不重新触发应着的回退路径，
	// 少了这一步局面会永久停摆 —— 轮到 AI 却没有任何人在算。
	needAI := s.result == "" && s.Mode != ModeLocal && s.pos.Turn != s.HumanSide
	s.mu.Unlock()
	s.flush(msgs)
	if needAI {
		go s.aiReply()
	}
	return nil
}

// Hint 提示一步（残局优先给正解着法）。
func (s *Session) Hint() error {
	s.mu.Lock()
	if s.result != "" {
		s.mu.Unlock()
		return errf("对局已结束")
	}
	if s.Mode != ModeLocal && s.pos.Turn != s.HumanSide {
		s.mu.Unlock()
		return errf("只有轮到你时才能提示")
	}
	pos := s.pos.Clone()
	solUCI := ""
	if s.Mode == ModePuzzle && s.pz != nil {
		solUCI = s.solutionMoveAt(s.pzStep)
	}
	s.mu.Unlock()

	var mv game.Move
	var err error
	if solUCI != "" {
		if m, ok := game.MoveFromUCI(solUCI); ok {
			mv = m
		} else {
			err = fmt.Errorf("正解着法非法: %s", solUCI)
		}
	} else {
		mv, err = s.engines.Hint(context.Background(), pos)
	}
	if err != nil {
		return errf("提示失败: %v", err)
	}
	// hintUsed 只在提示**真的给出**之后才记账：放在引擎调用之前的话，
	// 引擎报错时玩家什么也没看到，却已被永久剥夺三星资格。
	s.mu.Lock()
	s.hintUsed = true
	s.mu.Unlock()
	s.broadcast(map[string]any{
		"type": "hint_result",
		"from": game.SquareName(mv.From), "to": game.SquareName(mv.To),
		"cn": pos.MoveToChinese(mv),
	})
	return nil
}

// Resign 认输（默认人类认输；双人模式为当前轮走方认输）。
func (s *Session) Resign() error {
	s.mu.Lock()
	if s.result != "" {
		s.mu.Unlock()
		return errf("对局已结束")
	}
	result := game.ResultBlackWin
	if s.HumanSide == game.Black {
		result = game.ResultRedWin
	}
	if s.Mode == ModeLocal {
		if s.pos.Turn == game.Red {
			result = game.ResultBlackWin
		} else {
			result = game.ResultRedWin
		}
	}
	msgs := s.finishLocked(result, game.ReasonResign)
	s.mu.Unlock()
	s.flush(msgs)
	return nil
}

// Restart 重开（残局：还原初始局面与进度）。
func (s *Session) Restart() error {
	s.mu.Lock()
	if s.Mode != ModePuzzle || s.pz == nil {
		s.mu.Unlock()
		return errf("仅残局模式支持重开")
	}
	pos, err := game.ParseFEN(s.pz.FEN)
	if err != nil {
		s.mu.Unlock()
		return errf("残局 FEN 非法")
	}
	if err := pos.LegalPosition(); err != nil {
		s.mu.Unlock()
		return errf("残局局面非法: %v", err)
	}
	s.pos = pos
	s.moves = nil
	s.result = ""
	s.reason = ""
	s.pzStep = 0
	s.pzConsumed = nil
	s.pzFail = false
	s.hintUsed = false

	// 作废正在进行的思考：它算的是重开**之前**的局面（在飞的 goroutine 会靠
	// gen 校验自行丢弃结果，也不会再改 thinking/cancelThink）。不取消的话，
	// 「刚走完一手立刻重开」会让那手旧着法落到新局面上（黑先关尤其明显）。
	if s.cancelThink != nil {
		s.cancelThink()
		s.cancelThink = nil
	}
	s.gen++
	s.thinking = false

	msgs := []any{
		map[string]any{"type": "restart"},
		s.buildStateLocked(),
	}
	s.mu.Unlock()
	s.flush(msgs)

	// 黑先关（playerSide=black 且 FEN 为红先）重开后仍归 AI 先走。
	// 原来没有这一步 ⇒ 重开后没人应着，玩家一动就被告知「现在轮到对方行棋」，
	// 局面直接卡死（3577 关里有 2 关是黑先）。
	s.StartIfAIToMove()
	return nil
}

// StartIfAIToMove 开局若轮到 AI（人类执黑）则先走。
func (s *Session) StartIfAIToMove() {
	s.mu.Lock()
	need := s.result == "" && s.Mode != ModeLocal && s.pos.Turn != s.HumanSide
	s.mu.Unlock()
	if need {
		go s.aiReply()
	}
}

// Created 返回创建时间（供管理器清理）。
func (s *Session) Created() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.created
}

// Close 终止思考并断开全部连接。
func (s *Session) Close() {
	s.mu.Lock()
	if s.cancelThink != nil {
		s.cancelThink()
	}
	conns := s.connSnapshot()
	s.mu.Unlock()
	for _, c := range conns {
		c.Close()
	}
}
