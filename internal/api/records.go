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
	// reviewTimeout 讲解外层超时的**下限**：实际值由用户配置的超时推导
	// （见 handleRecordReview），因为要包住 llm 内部的「3 次尝试 + 退避」。
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

// beginWork 登记「这个 key 上已有重活在跑」。
//
// 返回 (wait, done)：
//   - wait 非 nil → 别人正在跑同一个 key，调用方应等它结束再读结果（等它写进缓存即可）；
//   - done 非 nil → 本次由调用方执行，**完成后必须调用 done**（摘登记 + 唤醒等待者）。
//
// 为什么需要：分析要跑整局引擎浅搜（一局几秒），讲解要调大模型（几十秒）。两个客户端
// 同时点开同一条记录／同一手时，不登记就会白跑两遍 —— 分析还会与进行中的对局抢引擎。
func (s *Server) beginWork(key string) (wait <-chan struct{}, done func()) {
	s.inflightMu.Lock()
	defer s.inflightMu.Unlock()
	if s.inflight == nil {
		s.inflight = map[string]chan struct{}{}
	}
	if ch, ok := s.inflight[key]; ok {
		return ch, nil
	}
	ch := make(chan struct{})
	s.inflight[key] = ch
	return nil, func() {
		s.inflightMu.Lock()
		delete(s.inflight, key)
		s.inflightMu.Unlock()
		close(ch)
	}
}

// waitWork 等同一 key 上的重活结束。返回 false 表示等待被取消（客户端断开）。
func waitWork(r *http.Request, ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	case <-r.Context().Done():
		return false
	}
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
	// 钳死页号：page*50 溢出成负数会让 from/to 变负、钳制不触发，all[from:to] 直接 panic。
	// 一个 ?page=1000000000000000000 就能打出 500 + 一整段栈，属于白送的噪声。
	const maxPage = 1 << 20
	if page > maxPage {
		page = maxPage
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

// validRecordID 棋谱 id 只可能是「对局 id」（16 位十六进制），因此按白名单校验。
//
// 这是**安全边界**：id 取自 URL 路径、随后参与 filepath.Join(dir, id+".json")。
// Go 的 ServeMux 是按 EscapedPath 做路径清理的，`%2e%2e%2f`（编码的点斜杠）不是 `..`、
// 不会被清理，解码后 id 里就带着 `../` —— 足以让 Join 逃出 records 目录、删掉目录外的
// 任意 .json（删除是「先删文件后查索引」，纯盲删）。对照：自定义残局的删除有
// `custom-` 前缀校验，恰好使 `..` 前面必为一段非空路径而被 filepath.Clean 吃掉。
func validRecordID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// handleRecord GET /api/records/{id} 及其子动作（/delete、/analyze、/review）。
func (s *Server) handleRecord(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimPrefix(r.URL.Path, "/api/records/")
	action, id := "", raw
	for _, a := range []string{"/delete", "/analyze", "/review"} {
		if strings.HasSuffix(raw, a) {
			action, id = a, strings.TrimSuffix(raw, a)
			break
		}
	}
	if !validRecordID(id) {
		writeErr(w, http.StatusBadRequest, "棋谱 id 非法")
		return
	}
	switch action {
	case "/delete":
		s.handleRecordDelete(w, r, id)
	case "/analyze":
		s.handleRecordAnalyze(w, r, id)
	case "/review":
		s.handleRecordReview(w, r, id)
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
	// 先查存在性再删文件：反过来会「文件已经删了、才回 404」，语义颠倒。
	if _, ok := s.Records.Get(id); !ok {
		writeErr(w, http.StatusNotFound, "棋谱不存在")
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
	s.Records.Remove(id)
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
	if rec.Analyzed {
		writeJSON(w, http.StatusOK, map[string]any{"analysis": rec.Analysis, "cached": true})
		return
	}
	if s.Engines == nil {
		writeErr(w, http.StatusServiceUnavailable, "引擎不可用，无法分析")
		return
	}
	// 单飞：已有同一局的分析在跑就等它（结果写进缓存后直接取用）
	if wait, done := s.beginWork("analyze:" + id); wait != nil {
		if !waitWork(r, wait) {
			writeErr(w, http.StatusServiceUnavailable, "等待分析结果超时，请稍后重试")
			return
		}
		if rec2, ok := s.Records.Get(id); ok && rec2.Analyzed {
			writeJSON(w, http.StatusOK, map[string]any{"analysis": rec2.Analysis, "cached": true})
			return
		}
		writeErr(w, http.StatusServiceUnavailable, "分析未完成，请重试")
		return
	} else {
		defer done()
	}
	// 拿到单飞令牌后重新读一次：登记窗口内可能已经有人算完了
	if rec2, ok := s.Records.Get(id); ok && rec2.Analyzed {
		writeJSON(w, http.StatusOK, map[string]any{"analysis": rec2.Analysis, "cached": true})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), analyzeTimeout)
	defer cancel()
	km, err := review.Analyze(ctx, rec, s.Engines)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "分析失败: "+err.Error())
		return
	}
	// 写回：走 Store.Update 在锁内**合并**（只改 Analysis）——不能拿计算前的快照整体覆盖，
	// 那会把并发的讲解结果一起抹掉。
	updated, ok2 := s.Records.Update(id, func(r *record.Record) {
		r.Analysis = km
		r.Analyzed = true // 空结果也要记「分析过」，否则每次打开都重算
	})
	if !ok2 {
		writeErr(w, http.StatusNotFound, "棋谱不存在")
		return
	}
	if s.RecordsDir != "" {
		if err := record.SaveToDir(s.RecordsDir, updated); err != nil {
			// 缓存写不进磁盘不影响本次结果：照常返回，只是下次还要再算一遍。
			log.Printf("棋谱分析缓存落盘失败 %s: %v", updated.ID, err)
		}
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
	if req.Index < 0 || req.Index >= len(rec.Moves) {
		// 参数错误就是参数错误：返回 400，别把客户端问题伪装成上游模型故障（502）
		writeErr(w, http.StatusBadRequest, "手数超出范围")
		return
	}
	key := strconv.Itoa(req.Index)
	if text, ok := rec.Reviews[key]; ok && strings.TrimSpace(text) != "" {
		writeJSON(w, http.StatusOK, map[string]any{"text": text, "cached": true})
		return
	}
	// 单飞：同一手已有讲解在跑就等它（讲解要几十秒、还花 token，白跑两遍最亏）
	if wait, done := s.beginWork("review:" + id + ":" + key); wait != nil {
		if !waitWork(r, wait) {
			writeErr(w, http.StatusServiceUnavailable, "等待讲解结果超时，请稍后重试")
			return
		}
		if rec2, ok := s.Records.Get(id); ok {
			if text, ok := rec2.Reviews[key]; ok && strings.TrimSpace(text) != "" {
				writeJSON(w, http.StatusOK, map[string]any{"text": text, "cached": true})
				return
			}
		}
		writeErr(w, http.StatusServiceUnavailable, "讲解未完成，请重试")
		return
	} else {
		defer done()
	}
	// 该手若有关键手标记，把它带给模型当线索（引擎的最佳手与分差）
	var km *record.KeyMove
	for i := range rec.Analysis {
		if rec.Analysis[i].Index == req.Index {
			km = &rec.Analysis[i]
			break
		}
	}
	// 外层要包住 llm 内部最坏情况：3 次尝试 + 退避。原先写死 6 分钟，
	// 而用户把「超时」调大后，一次尝试就可能吃掉外层一半，第二次（截断加倍）会被掐死。
	outer := reviewTimeout
	if d := 3*time.Duration(req.LLM.TimeoutMs)*time.Millisecond + 30*time.Second; d > outer {
		outer = d
	}
	ctx, cancel := context.WithTimeout(r.Context(), outer)
	defer cancel()
	text, err := review.Explain(ctx, req.LLM, rec, req.Index, km)
	if err != nil {
		// 502：失败发生在上游模型侧。错误文案里已带可操作提示（调大输出上限等）。
		writeErr(w, http.StatusBadGateway, "讲解失败: "+err.Error())
		return
	}
	// 写回缓存：同样走 Store.Update 在锁内只合并这一条讲解（理由见 analyze）
	updated, ok2 := s.Records.Update(id, func(r *record.Record) {
		if r.Reviews == nil {
			r.Reviews = map[string]string{}
		}
		r.Reviews[key] = text
	})
	if !ok2 {
		writeErr(w, http.StatusNotFound, "棋谱不存在")
		return
	}
	if s.RecordsDir != "" {
		if err := record.SaveToDir(s.RecordsDir, updated); err != nil {
			log.Printf("棋谱讲解缓存落盘失败 %s: %v", updated.ID, err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"text": text})
}
