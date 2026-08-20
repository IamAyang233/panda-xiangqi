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

	llmCfg   llm.Config
	llmPlayer *llm.Player // 会话级复用（HTTP keep-alive，避免每步重建连接）
	pz       *puzzle.Puzzle
	pzStep   int // solution 已消费的下标
	pzFail   bool
	hintUsed bool

	conns   map[Conn]struct{}
	engines *engine.Manager
	created time.Time
}

// NewSession 创建会话。pz 非空时为残局模式。
func NewSession(mode string, humanSide int, level int, llmCfg llm.Config, pz *puzzle.Puzzle, engines *engine.Manager) *Session {
	s := &Session{
		ID:        newID(),
		Mode:      mode,
		Level:     clampLevel(level),
		HumanSide: humanSide,
		llmCfg:    llmCfg,
		pz:        pz,
		conns:     make(map[Conn]struct{}),
		engines:   engines,
		created:   time.Now(),
	}
	if pz != nil {
		pos, err := game.ParseFEN(pz.FEN)
		if err != nil {
			log.Printf("session: 残局 FEN 非法 %s: %v", pz.ID, err)
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
	msg := s.buildStateLocked()
	s.mu.Unlock()
	c.SendJSON(msg)
}

// Leave 注销连接。
func (s *Session) Leave(c Conn) {
	s.mu.Lock()
	delete(s.conns, c)
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
			"step": s.playerMoveCountLocked(),
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
			msgs = s.applyMoveLocked(m)
			msgs = append(msgs, map[string]any{"type": "puzzle_event", "event": "deviate",
				"message": "偏离正解，可悔棋修正或重开本关"})
			needAI = true
		} else {
			s.pzStep++ // 消费玩家正解着法
			msgs = s.applyMoveLocked(m)
		}
	} else {
		msgs = s.applyMoveLocked(m)
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
func (s *Session) applyMoveLocked(m game.Move) []any {
	cn := s.pos.MoveToChinese(m)
	red := s.pos.Turn == game.Red
	s.pos.Make(m)
	s.moves = append(s.moves, MoveRecord{UCI: m.String(), CN: cn, Red: red})

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
			// 胜负关：玩家（执子方）将死或困毙对方即算通过。
			playerWon := (result == game.ResultRedWin && s.HumanSide == game.Red) ||
				(result == game.ResultBlackWin && s.HumanSide == game.Black)
			cleared = playerWon && (reason == game.ReasonCheckmate || reason == game.ReasonStalemate)
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
	default:
		mv, err = s.engines.BestMove(ctx, s.snapshot(), s.puzzleLevel())
	}
	if elapsed := time.Since(start); elapsed < 600*time.Millisecond {
		time.Sleep(600*time.Millisecond - elapsed) // 思考动画保底时长
	}
	cancel()

	s.mu.Lock()
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
	if s.Mode == ModePuzzle && s.pz != nil && !s.pzFail {
		s.pzStep++ // 消费守方正解应着
	}
	if fallback {
		msgs = append(msgs, map[string]any{"type": "llm_fallback", "by": "local_engine"})
	}
	msgs = append(msgs, s.applyMoveLocked(mv)...)
	if comment != "" {
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
			// Unmake 前读被吃子身份（该步落点原棋子；若为吃子则存在）
			// mv.To 为 256 mailbox 下标，与 Position.Board 直接索引一致。
			if capPiece := s.pos.Board[mv.To]; capPiece != game.Empty {
				um.Captured = string(capPiece)
			}
		} else {
			um = undoMove{From: last.UCI[:4], To: last.UCI[:4], CN: last.CN}
		}
		undone = append(undone, um)
		s.pos.Unmake()
		s.moves = s.moves[:len(s.moves)-1]
	}
	if s.Mode == ModePuzzle {
		s.pzFail = false
		if s.pzStep >= n {
			s.pzStep -= n
		} else {
			s.pzStep = 0
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
	s.mu.Unlock()
	s.flush(msgs)
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
	s.hintUsed = true
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
	s.pos = pos
	s.moves = nil
	s.result = ""
	s.reason = ""
	s.pzStep = 0
	s.pzFail = false
	s.hintUsed = false
	msgs := []any{
		map[string]any{"type": "restart"},
		s.buildStateLocked(),
	}
	s.mu.Unlock()
	s.flush(msgs)
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
