package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// 应用标识（PanDa「推送更新」后台录入时使用同一 app_name）。
const AppName = "panda-xiangqi"

// AppVersion 版本号优先级：编译时由 -ldflags -X 注入的 buildVersion ＞ 随包 manifest 的 version= 行。
// 单文件 exe / fnOS 部署若无 manifest 时，靠 buildVersion 兜底，避免「检查更新」误报 0.0.0。
var (
	AppVersion     = buildVersion // 初始 = 编译期注入值（未注入则为空，运行时再尝试 manifest）
	appVersionOnce sync.Once
)

// buildVersion 由 Makefile 的 -ldflags "-X ...buildVersion=$(VERSION)" 注入；不注入则为空。
var buildVersion string

// InitAppVersion 从 manifest 读取真实版本号覆盖 AppVersion；读不到则保留 buildVersion。
// 应在服务启动早期（构造 Server 前）调用一次；重复调用安全（仅首次生效）。
func InitAppVersion() {
	appVersionOnce.Do(func() {
		if v, ok := readManifestVersion(); ok && v != "" {
			AppVersion = v
			return
		}
		if AppVersion == "" {
			AppVersion = "0.0.0"
		}
	})
}

// readManifestVersion 在若干候选路径中寻找并解析 manifest 的 version= 行。
// manifest 为 INI 风格（key=value）。候选顺序：当前工作目录（fnOS 启动 cwd=应用根）、
// 本地开发仓库路径、以及相对可执行文件上层（<app>/app/server/<bin> → ../../manifest）。
func readManifestVersion() (string, bool) {
	candidates := []string{"manifest", filepath.Join("fnos", "panda-xiangqi", "manifest")}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "..", "..", "manifest"))
	}
	for _, p := range candidates {
		if v, ok := parseManifestVersion(p); ok {
			return v, true
		}
	}
	return "", false
}

// parseManifestVersion 读取一个 INI 风格文件，返回 version= 的值（去引号/前后空白）。
func parseManifestVersion(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(k), "version") {
			if ver := strings.TrimSpace(strings.Trim(v, `"'`)); ver != "" {
				return ver, true
			}
		}
	}
	return "", false
}

const updateHTTPTimeout = 8 * time.Second

// handleUpdate GET /api/update —— 检测更新：转发 PanDa /api/app-update/<app>，
// current 覆盖为应用自身版本（PanDa 文档 §6.2 约定）。
func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	resp := s.fetchUpdate()
	resp["selfVersion"] = AppVersion
	writeJSON(w, http.StatusOK, resp)
}

// fetchUpdate 拉取 PanDa 版本记录；失败时返回友好错误（HTTP 仍 200，前端可展示版本）。
func (s *Server) fetchUpdate() map[string]any {
	out := map[string]any{
		"ok":       false,
		"app":      AppName,
		"current":  AppVersion,
		"releases": []any{},
	}
	base := s.UpdateAPI
	if base == "" {
		out["message"] = "未配置更新服务地址"
		return out
	}
	client := &http.Client{Timeout: updateHTTPTimeout}
	url := trimRight(base) + "/api/app-update/" + AppName
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		out["message"] = err.Error()
		return out
	}
	resp, err := client.Do(req)
	if err != nil {
		out["message"] = "更新服务不可达：" + err.Error()
		return out
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		out["message"] = err.Error()
		return out
	}
	if resp.StatusCode != http.StatusOK {
		out["message"] = fmt.Sprintf("更新服务响应异常 HTTP %d", resp.StatusCode)
		return out
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		out["message"] = "更新服务响应解析失败"
		return out
	}
	if ok, _ := parsed["ok"].(bool); !ok {
		if msg, _ := parsed["message"].(string); msg != "" {
			out["message"] = msg
		} else {
			out["message"] = "更新服务返回 ok:false"
		}
		return out
	}
	parsed["current"] = AppVersion // 覆盖为应用自身版本，供前端比较
	return parsed
}

// handleFeedback POST /api/feedback —— Bug 反馈：收集诊断信息并转发 PanDa。
func (s *Server) handleFeedback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "需要 POST")
		return
	}
	var req struct {
		Category    string `json:"category"`
		Title       string `json:"title"`
		Description string `json:"description"`
		Contact     string `json:"contact"`
		IncludeLogs bool   `json:"includeLogs"`
		ClientInfo  string `json:"clientInfo"` // 前端采集（UA / 屏幕 / 语言）
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<18)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体解析失败")
		return
	}
	if req.Title == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message": "标题不能为空"})
		return
	}

	payload := map[string]any{
		"app":        AppName,
		"version":    AppVersion,
		"category":   req.Category,
		"title":      req.Title,
		"description": req.Description,
		"contact":    req.Contact,
	}
	if req.IncludeLogs {
		payload["logs"] = s.collectDiagnostics(req.ClientInfo)
	}

	body, _ := json.Marshal(payload)
	base := s.UpdateAPI
	if base == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message": "未配置反馈服务地址"})
		return
	}
	url := trimRight(base) + "/api/app-feedback/" + AppName // PanDa 通用反馈端点（按应用派生的 X-Feedback-Token 校验）
	httpReq, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if s.FeedbackToken != "" {
		httpReq.Header.Set("X-Feedback-Token", s.FeedbackToken)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message": "反馈服务不可达：" + err.Error()})
		return
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode == http.StatusUnauthorized {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message": "反馈 Token 校验失败（401）"})
		return
	}
	if resp.StatusCode != http.StatusOK {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message": fmt.Sprintf("反馈服务响应异常 HTTP %d", resp.StatusCode)})
		return
	}
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	if out == nil {
		out = map[string]any{"ok": true, "message": "已提交"}
	}
	out["ok"] = true
	writeJSON(w, http.StatusOK, out)
}

// collectDiagnostics 服务端诊断信息（PanDa 文档 §6.4 日志收集的轻量版）。
func (s *Server) collectDiagnostics(clientInfo string) string {
	log := fmt.Sprintf("=== 熊猫象棋诊断信息 ===\n版本: %s\n引擎: %s（皮卡鱼可用: %v）\n残局: %d 关\n在线会话: %d\n运行时: %s %s/%s\n时间: %s\n",
		AppVersion, s.Engines.EngineName(), s.Engines.HasUCI(),
		s.Puzzles.Count(), s.Sessions.Count(), runtime.Version(), runtime.GOOS, runtime.GOARCH,
		time.Now().Format("2006-01-02 15:04:05"))
	if clientInfo != "" {
		log += "--- 客户端 ---\n" + clientInfo + "\n"
	}
	return log
}

func trimRight(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}
