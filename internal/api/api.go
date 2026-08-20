package api

import (
	"context"
	"encoding/json"
	"io"
	"io/fs"
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
	Static        fs.FS  // 前端资源（web/dist 或 web/）
	UpdateAPI     string // PanDa 推送更新服务入口
	FeedbackToken string // 反馈共享 Token
	GatewayPrefix string // 飞牛 fnOS 统一网关注册前缀（如 /app/panda-xiangqi）；为空则本地开发直连
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
	inner.HandleFunc("/api/puzzles/", s.handlePuzzleDetail)
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
	writeJSON(w, http.StatusOK, map[string]any{
		"app":            AppName,
		"version":        AppVersion,
		"engine":         s.Engines.EngineName(),
		"uciAvailable":   s.Engines.HasUCI(),
		"puzzles":        s.Puzzles.Count(),
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
	Mode     string    `json:"mode"`
	Side     string    `json:"side"` // red | black
	Level    int       `json:"level"`
	PuzzleID string    `json:"puzzleId"`
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
		var ok bool
		pz, ok = s.Puzzles.Get(req.PuzzleID)
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

func (s *Server) handlePuzzleList(w http.ResponseWriter, r *http.Request) {
	difficulty := r.URL.Query().Get("difficulty")
	writeJSON(w, http.StatusOK, s.Puzzles.List(difficulty))
}

func (s *Server) handlePuzzleDetail(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/puzzles/")
	p, ok := s.Puzzles.Get(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "残局不存在")
		return
	}
	writeJSON(w, http.StatusOK, puzzle.Public{
		ID: p.ID, Name: p.Name, Source: p.Source, Difficulty: p.Difficulty,
		PlayerSide: p.PlayerSide, Goal: p.Goal, ParMoves: p.ParMoves, Tags: p.Tags,
	})
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
	timeout := time.Duration(cfg.TimeoutMs) * time.Millisecond
	if cfg.TimeoutMs <= 0 {
		timeout = 30 * time.Second
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
