package api

// 棋谱（对局记录）相关的 HTTP 处理器。
//
// 与自定义残局那套的关键差别：**没有「新建」端点**。棋谱只能由对局产生 ——
// 用户在结算弹窗点「保存此局」时，服务端从内存里的会话导出记录。这样：
//   - 前端不上传任何对局内容，因此不需要四条硬校验/UTF-8/名字截断那一整块；
//   - 也不可能伪造别人的棋谱（没有可写入口）；
//   - 记录 id 就用对局 id，重复保存天然幂等（覆盖同一文件）。

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/IamAyang233/panda-xiangqi/internal/llm"
	"github.com/IamAyang233/panda-xiangqi/internal/record"
	"github.com/IamAyang233/panda-xiangqi/internal/review"
	"github.com/IamAyang233/panda-xiangqi/internal/session"
)

const (
	// recordPageSize 列表每页条数：一页 50 条摘要，翻页由前端按需拉。
	recordPageSize = 50
	// analyzeTimeout 关键手分析的外层上限：一局 40 手 × 前后两次 40ms 约 3 秒，
	// 30 秒是给长局留的余量（分析上限 240 手）。
	analyzeTimeout = 30 * time.Second
	// reviewTimeout 讲解的外层上限：要包住 llm 自己的超时与「截断加倍重试」，
	// 所以比默认的 180s 宽（用户可能把超时调到很大）。
	reviewTimeout = 6 * time.Minute
)

// handleGameSave POST /api/games/{id}/save —— 把已终局的这一局存成棋谱。
//
// 由用户在结算弹窗上触发。服务端只在收到请求时导出，因此「只存下完的局」
// 是天然成立的：未终局（result 为空）直接拒。
func (s *Server) handleGameSave(w http.ResponseWriter, sess *session.Session) {
	if s.Records == nil {
		writeErr(w, http.StatusServiceUnavailable, "棋谱功能未启用")
		return
	}
	rec, ok := sess.SnapshotRecord()
	if !ok {
		// 未终局，或这是残局对局（残局不进棋谱）。不是操作失败，是这一局还不能存。
		writeErr(w, http.StatusConflict, "这一局还不能保存（未终局或为残局挑战）")
		return
	}
	_, existed := s.Records.Get(rec.ID)
	if s.RecordsDir != "" {
		if err := record.SaveToDir(s.RecordsDir, &rec); err != nil {
			writeErr(w, http.StatusInternalServerError, "保存失败: "+err.Error())
			return
		}
	}
	if err := s.Records.Add(&rec); err != nil {
		// 这里**不做**磁盘回滚：棋谱的保存语义是「同 id 覆盖」，
		// 回滚会把用户之前存过的那一份一起删掉（与自定义残局的新建语义不同）。
		// Add 只在记录自检不过时失败，属于程序性错误，如实报出来即可。
		writeErr(w, http.StatusInternalServerError, "保存失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": rec.ID, "existed": existed})
}

// handleRecordList GET /api/records —— 列表（摘要，不含着法）。
func (s *Server) handleRecordList(w http.ResponseWriter, r *http.Request) {
	if s.Records == nil {
		writeErr(w, http.StatusServiceUnavailable, "棋谱功能未启用")
		return
	}
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "需要 GET")
		return
	}
	all := s.Records.List()
	page := 0
	if v, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && v > 0 {
		page = v
	}
	from, to := page*recordPageSize, (page+1)*recordPageSize
	if from > len(all) {
		from = len(all)
	}
	if to > len(all) {
		to = len(all)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"total":    len(all),
		"page":     page,
		"pageSize": recordPageSize,
		"items":    all[from:to],
	})
}

// handleRecord GET /api/records/{id} 及其子动作（/delete、/analyze、/review）。
func (s *Server) handleRecord(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/records/")
	switch {
	case strings.HasSuffix(id, "/delete"):
		s.handleRecordDelete(w, r, strings.TrimSuffix(id, "/delete"))
	case strings.HasSuffix(id, "/analyze"):
		s.handleRecordAnalyze(w, r, strings.TrimSuffix(id, "/analyze"))
	case strings.HasSuffix(id, "/review"):
		s.handleRecordReview(w, r, strings.TrimSuffix(id, "/review"))
	default:
		if r.Method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, "需要 GET")
			return
		}
		s.handleRecordDetail(w, r, id)
	}
}

func (s *Server) handleRecordDetail(w http.ResponseWriter, r *http.Request, id string) {
	if s.Records == nil {
		writeErr(w, http.StatusServiceUnavailable, "棋谱功能未启用")
		return
	}
	rec, ok := s.Records.Get(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "棋谱不存在")
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

// handleRecordDelete POST /api/records/{id}/delete —— 删除一条棋谱。
// 与残局不同：棋谱没有「不能再删」的条目，任何一条都可删（列表卡片上是二次确认）。
func (s *Server) handleRecordDelete(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "需要 POST")
		return
	}
	if s.Records == nil {
		writeErr(w, http.StatusServiceUnavailable, "棋谱功能未启用")
		return
	}
	if s.RecordsDir == "" {
		// 内存态：磁盘上本来就没有记录，无从删起。如实告知而不是假装成功。
		writeErr(w, http.StatusBadRequest, "棋谱当前为内存态（目录不可写），重启后会重新出现")
		return
	}
	if err := record.RemoveFromDir(s.RecordsDir, id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !s.Records.Remove(id) {
		writeErr(w, http.StatusNotFound, "棋谱不存在")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleRecordAnalyze POST /api/records/{id}/analyze —— 算关键手。
//
// 首次调用计算并落盘缓存（一局几秒，前端先渲染回放、再异步补关键手列表）；
// 已有缓存直接返回，不重复占用引擎。
func (s *Server) handleRecordAnalyze(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "需要 POST")
		return
	}
	if s.Records == nil {
		writeErr(w, http.StatusServiceUnavailable, "棋谱功能未启用")
		return
	}
	rec, ok := s.Records.Get(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "棋谱不存在")
		return
	}
	if len(rec.Analysis) > 0 {
		writeJSON(w, http.StatusOK, map[string]any{"analysis": rec.Analysis, "cached": true})
		return
	}
	if s.Engines == nil {
		writeErr(w, http.StatusServiceUnavailable, "引擎不可用，无法分析")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), analyzeTimeout)
	defer cancel()
	km, err := review.Analyze(ctx, rec, s.Engines)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "分析失败: "+err.Error())
		return
	}
	// 写回：内存里**换成新对象**（不能原地改 Get 拿到的指针，那会和并发读者抢数据）
	updated := *rec
	updated.Analysis = km
	if s.RecordsDir != "" {
		if err := record.SaveToDir(s.RecordsDir, &updated); err != nil {
			// 缓存写不进磁盘不影响本次结果：照常返回，只是下次还要再算一遍。
			log.Printf("棋谱分析缓存落盘失败 %s: %v", updated.ID, err)
		}
	}
	if err := s.Records.Add(&updated); err != nil {
		log.Printf("棋谱分析缓存入内存失败 %s: %v", updated.ID, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"analysis": km})
}

// handleRecordReview POST /api/records/{id}/review —— 「问 AI」讲解某一手。
//
// 按需触发（一次调用 45~90 秒），讲完写回棋谱缓存；下次同一手直接返回缓存。
func (s *Server) handleRecordReview(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "需要 POST")
		return
	}
	if s.Records == nil {
		writeErr(w, http.StatusServiceUnavailable, "棋谱功能未启用")
		return
	}
	var req struct {
		Index int        `json:"index"`
		LLM   llm.Config `json:"llm"`
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "请求体读取失败")
		return
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体解析失败")
		return
	}
	rec, ok := s.Records.Get(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "棋谱不存在")
		return
	}
	key := strconv.Itoa(req.Index)
	if text, ok := rec.Reviews[key]; ok && strings.TrimSpace(text) != "" {
		writeJSON(w, http.StatusOK, map[string]any{"text": text, "cached": true})
		return
	}
	// 该手若有关键手标记，把它带给模型当线索（引擎的最佳手与分差）
	var km *record.KeyMove
	for i := range rec.Analysis {
		if rec.Analysis[i].Index == req.Index {
			km = &rec.Analysis[i]
			break
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), reviewTimeout)
	defer cancel()
	text, err := review.Explain(ctx, req.LLM, rec, req.Index, km)
	if err != nil {
		// 502：失败发生在上游模型侧。错误文案里已带可操作提示（调大输出上限等）。
		writeErr(w, http.StatusBadGateway, "讲解失败: "+err.Error())
		return
	}
	// 写回缓存（复制一份，避免原地改内部记录）
	updated := *rec
	updated.Reviews = make(map[string]string, len(rec.Reviews)+1)
	for k, v := range rec.Reviews {
		updated.Reviews[k] = v
	}
	updated.Reviews[key] = text
	if s.RecordsDir != "" {
		if err := record.SaveToDir(s.RecordsDir, &updated); err != nil {
			log.Printf("棋谱讲解缓存落盘失败 %s: %v", updated.ID, err)
		}
	}
	if err := s.Records.Add(&updated); err != nil {
		log.Printf("棋谱讲解缓存入内存失败 %s: %v", updated.ID, err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"text": text})
}
