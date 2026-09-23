package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"math/rand"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/IamAyang233/panda-xiangqi/internal/engine"
	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/llm"
	"github.com/IamAyang233/panda-xiangqi/internal/puzzle"
	"github.com/IamAyang233/panda-xiangqi/internal/session"
)

// Server HTTP 服务：REST + WS + 静态资源。
type Server struct {
	Sessions      *session.Manager
	Engines       *engine.Manager
	Puzzles       *puzzle.Store
	Custom        *puzzle.Store // 自定义残局（自摆局面，全服共享、可增删）
	CustomDir     string        // 自定义残局落盘目录；空表示内存态（不持久化）
	Static        fs.FS         // 前端资源（web/dist 或 web/）
	UpdateAPI     string        // PanDa 推送更新服务入口
	FeedbackToken string        // 反馈共享 Token
	GatewayPrefix string        // 飞牛 fnOS 统一网关注册前缀（如 /app/panda-xiangqi）；为空则本地开发直连
}

// Handler 组装路由。
//
// 飞牛 fnOS 统一网关不会剥离前缀，请求以 /app/<appname>/... 的形式到达本服务，
// 因此所有路由在网关前缀下与根路径下各注册一份，本地开发与网关部署均可工作。
func (s *Server) Handler() http.Handler {
	inner := http.NewServeMux()
	inner.HandleFunc("/api/games", s.handleCreateGame)
	inner.HandleFunc("/api/games/", s.handleGameAction)
	inner.HandleFunc("/api/puzzles", s.handlePuzzleList)
	inner.HandleFunc("/api/puzzles/", s.handlePuzzle)
	inner.HandleFunc("/api/llm/validate", s.handleLLMValidate)
	inner.HandleFunc("/api/update", s.handleUpdate)
	inner.HandleFunc("/api/feedback", s.handleFeedback)
	inner.HandleFunc("/api/status", s.handleStatus)
	inner.HandleFunc("/api/ws", s.handleWS)
	if s.Static != nil {
		inner.Handle("/", http.FileServerFS(s.Static))
	}

	prefix := strings.TrimRight(s.GatewayPrefix, "/")
	if prefix == "" {
		return logRequest(inner)
	}

	root := http.NewServeMux()
	// 网关前缀下的请求：剥离前缀后交给 inner 处理。
	root.Handle(prefix+"/", http.StripPrefix(prefix, inner))
	// 精确前缀（无尾斜杠）重定向到带斜杠版本，保证相对资源正确解析。
	root.HandleFunc(prefix, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, prefix+"/", http.StatusFound)
	})
	// 根路径仍可用，便于本地直接访问（同端口）。
	root.Handle("/", inner)
	return logRequest(root)
}

// gatewayUser 返回网关转发的可信用户身份（X-Trim-* Header）。
// 本地开发时这些 Header 不存在，返回空值，调用方应以匿名处理。
func gatewayUser(r *http.Request) (uid, username string, isAdmin bool) {
	uid = r.Header.Get("X-Trim-Userid")
	username = r.Header.Get("X-Trim-Username")
	isAdmin = r.Header.Get("X-Trim-Isadmin") == "true"
	return
}

// handleStatus GET /api/status —— 轻量运行状态：版本、引擎、残局数、网关用户等。
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "需要 GET")
		return
	}
	uid, username, isAdmin := gatewayUser(r)
	customCount, customDir, customWritable := 0, "", false
	if s.Custom != nil {
		customCount = s.Custom.Count()
	}
	if s.CustomDir != "" {
		customDir = s.CustomDir
		// 如实暴露可写性：目录不可写时保存会降级为内存态，若不让外面看见，
		// 用户只会以为存好了，重启后才发现没了。
		customWritable = puzzle.DirWritable(s.CustomDir)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"app":            AppName,
		"version":        AppVersion,
		"engine":         s.Engines.EngineName(),
		"uciAvailable":   s.Engines.HasUCI(),
		"engineDiag":     s.Engines.Diagnostics(),
		"puzzles":        s.Puzzles.Count(),
		"customPuzzles":  customCount,
		"customDir":      customDir,
		"customWritable": customWritable,
		"sessions":       s.Sessions.Count(),
		"gatewayUser":    username,
		"gatewayUid":     uid,
		"gatewayIsAdmin": isAdmin,
		"gatewayMode":    s.GatewayPrefix != "",
		"goVersion":      runtime.Version(),
		"platform":       runtime.GOOS + "/" + runtime.GOARCH,
		"timestamp":      time.Now().Format("2006-01-02 15:04:05"),
	})
}

func logRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			return
		}
		logWS("%s %s %v", r.Method, r.URL.Path, time.Since(start))
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// ---------------------------------------------------------------- 创建对局

type createGameReq struct {
	Mode     string     `json:"mode"`
	Side     string     `json:"side"` // red | black
	Level    int        `json:"level"`
	PuzzleID string     `json:"puzzleId"`
	LLM      llm.Config `json:"llm"`
}

func (s *Server) handleCreateGame(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "需要 POST")
		return
	}
	var req createGameReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体解析失败")
		return
	}
	humanSide := game.Red
	if req.Side == "black" {
		humanSide = game.Black
	}

	var pz *puzzle.Puzzle
	mode := req.Mode
	switch mode {
	case session.ModeEngine, session.ModeLLM, session.ModeLocal:
	case session.ModePuzzle:
		// 题库与自定义库都要查：自摆残局保存后就是按 id 从自定义库起局的。
		// 漏掉自定义库会让「保存并挑战」建局直接 404（保存成功却进不去）。
		var ok bool
		pz, ok = s.lookupPuzzle(req.PuzzleID)
		if !ok {
			writeErr(w, http.StatusNotFound, "残局不存在")
			return
		}
		// 残局玩家执子方由关卡定义（playerSide；默认红先）。
		humanSide = game.Red
		if pz.PlayerSide == "black" {
			humanSide = game.Black
		}
	default:
		writeErr(w, http.StatusBadRequest, "未知模式 "+req.Mode)
		return
	}

	sess := session.NewSession(mode, humanSide, req.Level, req.LLM, pz, s.Engines)
	s.Sessions.Put(sess)
	sess.StartIfAIToMove()
	writeJSON(w, http.StatusOK, map[string]any{
		"gameId": sess.ID,
		"youSide": func() string {
			if humanSide == game.Red {
				return "red"
			}
			return "black"
		}(),
	})
}

// ---------------------------------------------------------------- 对局操作 REST

func (s *Server) handleGameAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "需要 POST")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/games/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 {
		writeErr(w, http.StatusNotFound, "路径错误")
		return
	}
	sess, ok := s.Sessions.Get(parts[0])
	if !ok {
		writeErr(w, http.StatusNotFound, "对局不存在")
		return
	}
	var err error
	switch parts[1] {
	case "undo":
		err = sess.Undo()
	case "hint":
		err = sess.Hint()
	case "resign":
		err = sess.Resign()
	case "restart":
		err = sess.Restart()
	default:
		writeErr(w, http.StatusNotFound, "未知操作")
		return
	}
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---------------------------------------------------------------- 残局

// handlePuzzleList 列出残局：内置/外置题库 + 自定义残局合并返回。
// 两者刻意不合并成一个 Store：外置题库会整体替换内嵌题库，而自定义残局是增量的、
// 且需要可写，混在一起会让「配了 puzzles 目录」变成能不能自摆的前提条件。
func (s *Server) handlePuzzleList(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		s.handlePuzzleSave(w, r)
		return
	}
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "需要 GET")
		return
	}
	difficulty := r.URL.Query().Get("difficulty")
	out := s.Puzzles.List(difficulty)
	if s.Custom != nil {
		out = append(out, s.Custom.List(difficulty)...)
	}
	writeJSON(w, http.StatusOK, out)
}

// handlePuzzle 处理 /api/puzzles/{id} 与 /api/puzzles/{id}/delete。
// 删除用 POST 而非 DELETE：项目里所有变更动作（undo/hint/resign/restart）都是 POST，
// 且生产走 fnOS 统一网关反代 —— 不在「网关是否放行 DELETE」上赌运气。
func (s *Server) handlePuzzle(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/puzzles/")
	if strings.HasSuffix(id, "/delete") {
		s.handlePuzzleDelete(w, r, strings.TrimSuffix(id, "/delete"))
		return
	}
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "需要 GET")
		return
	}
	p, ok := s.lookupPuzzle(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "残局不存在")
		return
	}
	writeJSON(w, http.StatusOK, puzzle.Public{
		ID: p.ID, Name: p.Name, Source: p.Source, Difficulty: p.Difficulty,
		PlayerSide: p.PlayerSide, Goal: p.Goal, ParMoves: p.ParMoves, Tags: p.Tags,
	})
}

// lookupPuzzle 在两个库里查找（自定义 id 统一用 custom- 前缀，无歧义）。
func (s *Server) lookupPuzzle(id string) (*puzzle.Puzzle, bool) {
	if p, ok := s.Puzzles.Get(id); ok {
		return p, true
	}
	if s.Custom != nil {
		return s.Custom.Get(id)
	}
	return nil, false
}

// ---------------------------------------------------------------- 自定义残局

const (
	// customPrefix 自定义残局 id 前缀；同时是「允许删除」的判据（防止误删内置 3576 关）。
	customPrefix = "custom-"
	// customDifficulty 自定义残局的难度标签，前端据此加一个「自定义」筛选页。
	customDifficulty = "自定义"
)

// handlePuzzleSave POST /api/puzzles —— 保存一个自摆局面。
//
// 服务端是唯一防线：前端的校验条只是提示，请求可以绕过去。四条硬校验任一不过即 400，
// 宁可拒也得保证进库的局面能被安全地跑起来（非法局面会让「吃将」成为合法着法，
// 使 kingSq 悬空、整套合法性判定失真）。
func (s *Server) handlePuzzleSave(w http.ResponseWriter, r *http.Request) {
	if s.Custom == nil {
		writeErr(w, http.StatusServiceUnavailable, "自定义残局功能未启用")
		return
	}
	var req struct {
		Name string `json:"name"`
		FEN  string `json:"fen"`
		Side string `json:"side"`
		Goal string `json:"goal"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体解析失败")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "自摆残局"
	}
	// 按「字符」截断而不是字节：中文一个字 3 字节，按字节截会把一个字切成两半，
	// 存进去就是无效 UTF-8（前端显示成半个字或替换符）。前端输入框限 40 字符，这里对齐。
	if r := []rune(name); len(r) > 40 {
		name = string(r[:40])
	}
	side := req.Side
	if side != "black" {
		side = "red"
	}
	goal := req.Goal
	if goal != "draw" {
		goal = "win"
	}

	// ① FEN 可解析（语法：10 行 / 每行 9 格 / 双将齐备 / 轮走方合法）
	pos, err := game.ParseFEN(req.FEN)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "局面无法解析: "+err.Error())
		return
	}
	// ② 双方各恰好一个将/帅
	if err := checkKings(pos); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// ③ 局面合法（九宫、士象位置、将帅不照面、非行棋方不被将军）
	if err := pos.LegalPosition(); err != nil {
		writeErr(w, http.StatusBadRequest, "局面非法: "+err.Error())
		return
	}
	// ④ 先走方至少有一个合法着法（否则一开局就判结束，棋还没下就完了）
	if len(pos.LegalMoves(pos.Turn)) == 0 {
		writeErr(w, http.StatusBadRequest, "先走方无棋可走（被困毙或将死），无法作为残局")
		return
	}

	now := time.Now()
	p := &puzzle.Puzzle{
		ID:         fmt.Sprintf("%s%s-%s", customPrefix, now.Format("20060102-150405"), randSuffix(4)),
		Name:       name,
		Source:     "自摆",
		Difficulty: customDifficulty,
		PlayerSide: side,
		Goal:       goal,
		// TrimSpace + 标准化轮走方：编辑器据 side 决定谁先走，落盘的就是唯一权威局面。
		FEN:      strings.TrimSpace(req.FEN),
		ParMoves: 0, // 自摆残局没有记录步数的正解，因此不参与评星
	}
	if s.CustomDir != "" {
		if err := puzzle.SaveToDir(s.CustomDir, p); err != nil {
			writeErr(w, http.StatusInternalServerError, "保存失败: "+err.Error())
			return
		}
	}
	if err := s.Custom.Add(p); err != nil {
		// 落盘成功却没能入内存（id 冲突）时不应留下磁盘垃圾。
		if s.CustomDir != "" {
			_ = puzzle.RemoveFromDir(s.CustomDir, p.ID)
		}
		writeErr(w, http.StatusInternalServerError, "保存失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, puzzle.Public{
		ID: p.ID, Name: p.Name, Source: p.Source, Difficulty: p.Difficulty,
		PlayerSide: p.PlayerSide, Goal: p.Goal, ParMoves: p.ParMoves,
	})
}

// handlePuzzleDelete POST /api/puzzles/{id}/delete —— 删除一个自定义残局。
func (s *Server) handlePuzzleDelete(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "需要 POST")
		return
	}
	if s.Custom == nil {
		writeErr(w, http.StatusServiceUnavailable, "自定义残局功能未启用")
		return
	}
	if !strings.HasPrefix(id, customPrefix) {
		writeErr(w, http.StatusForbidden, "内置残局不可删除")
		return
	}
	if s.CustomDir == "" {
		// 内存态：磁盘上本来就没有记录，无从删起。如实告知而不是假装成功，
		// 否则用户以为删了，重启后它又回来了。
		writeErr(w, http.StatusBadRequest, "自定义残局当前为内存态（目录不可写），重启后会重新出现")
		return
	}
	if err := puzzle.RemoveFromDir(s.CustomDir, id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// 文件已不存在（手工删过）时 RemoveFromDir 不算错误，但内存里的索引仍可能被查到，
	// 若此刻返回 404 就会留下一个「点开必错」的幽灵条目。这里一律尝试摘索引。
	if !s.Custom.Remove(id) {
		writeErr(w, http.StatusNotFound, "残局不存在")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// checkKings 双方必须各恰好一枚将/帅。
// 没有王的一方走子后无从判将军；而多枚王会让「被将军」的语义发散。
func checkKings(pos *game.Position) error {
	var red, black int
	for sq := 0; sq < 90; sq++ {
		pc := pos.PieceAt(sq)
		if pc == game.Empty || game.TypeOf(pc) != game.King {
			continue
		}
		if game.ColorOf(pc) == game.Red {
			red++
		} else {
			black++
		}
	}
	if red != 1 || black != 1 {
		return fmt.Errorf("双方必须各有一枚将/帅（当前红 %d 黑 %d）", red, black)
	}
	return nil
}

func randSuffix(n int) string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = chars[rand.Intn(len(chars))]
	}
	return string(b)
}

// ---------------------------------------------------------------- LLM 连通性测试

func (s *Server) handleLLMValidate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "需要 POST")
		return
	}
	var cfg llm.Config
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&cfg); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体解析失败")
		return
	}
	if err := cfg.Validate(); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	client := llm.NewClient(cfg)
	// ⚠️ 别写死 30s：推理模型连一次 ping 也可能超过 30s，会给出「测试失败但对弈可用」
	// 的假阴性。与 llm 包的默认（llm.DefaultTimeoutMs）保持一致。
	timeout := time.Duration(cfg.TimeoutMs) * time.Millisecond
	if cfg.TimeoutMs <= 0 {
		timeout = time.Duration(llm.DefaultTimeoutMs) * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	latency, err := client.Ping(ctx)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message": err.Error(), "latencyMs": latency})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "连接成功", "latencyMs": latency})
}

// ---------------------------------------------------------------- WebSocket

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	gameID := r.URL.Query().Get("gameId")
	sess, ok := s.Sessions.Get(gameID)
	if !ok {
		writeErr(w, http.StatusNotFound, "对局不存在")
		return
	}
	conn, err := upgradeWS(w, r)
	if err != nil {
		return
	}
	sess.Join(conn)
	defer func() {
		sess.Leave(conn)
		conn.Close()
	}()

	for {
		// 读超时兼作心跳监测：正常客户端每 25s 一次 ping，读到任何消息都会刷新期限。
		// 超时返回错误 → defer 里 Leave+Close，死连接的 goroutine 得以回收。
		_ = conn.conn.SetReadDeadline(time.Now().Add(wsReadTimeout))
		data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var msg struct {
			Type string `json:"type"`
			From string `json:"from"`
			To   string `json:"to"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			conn.SendJSON(map[string]any{"type": "error", "code": "bad_message", "message": "消息格式错误"})
			continue
		}
		switch msg.Type {
		case "move":
			if err := sess.ApplyPlayerMove(msg.From, msg.To); err != nil {
				code := "illegal_move"
				if strings.Contains(err.Error(), "轮到") {
					code = "not_your_turn"
				} else if strings.Contains(err.Error(), "思考中") {
					code = "thinking"
				}
				conn.SendJSON(map[string]any{"type": "error", "code": code, "message": err.Error()})
			}
		case "legal":
			targets, err := sess.LegalTargets(msg.From)
			if err != nil {
				conn.SendJSON(map[string]any{"type": "error", "code": "legal", "message": err.Error()})
			} else {
				conn.SendJSON(map[string]any{"type": "legal_moves", "from": msg.From, "targets": targets})
			}
		case "undo":
			if err := sess.Undo(); err != nil {
				conn.SendJSON(map[string]any{"type": "error", "code": "undo", "message": err.Error()})
			}
		case "hint":
			if err := sess.Hint(); err != nil {
				conn.SendJSON(map[string]any{"type": "error", "code": "hint", "message": err.Error()})
			}
		case "resign":
			if err := sess.Resign(); err != nil {
				conn.SendJSON(map[string]any{"type": "error", "code": "resign", "message": err.Error()})
			}
		case "restart":
			if err := sess.Restart(); err != nil {
				conn.SendJSON(map[string]any{"type": "error", "code": "restart", "message": err.Error()})
			}
		case "ping":
			conn.SendJSON(map[string]any{"type": "pong"})
		default:
			conn.SendJSON(map[string]any{"type": "error", "code": "unknown_type", "message": "未知消息类型 " + msg.Type})
		}
	}
}
