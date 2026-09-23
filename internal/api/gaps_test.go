package api

// 2.0.5 白盒补测（2026-09-23）：这些行为都已有实现，但此前零覆盖。
//
// 选这些点是因为它们各防一类真实故障：
//   - /api/status 的三个自定义残局字段是「降级为内存态」对外**唯一**的如实通道；
//   - noStaleCache 是单二进制内嵌前端「升级后新旧资源混用」的唯一保障；
//   - side/goal 归一化决定非法输入落到哪个安全默认值；
//   - Custom==nil（自定义残局未启用）与删除边界决定接口在残缺配置下怎么答；
//   - 落盘失败后库里不能留下半条数据。

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/IamAyang233/panda-xiangqi/internal/engine"
	"github.com/IamAyang233/panda-xiangqi/internal/puzzle"
	"github.com/IamAyang233/panda-xiangqi/internal/session"
)

// apiServerWith 按需拼一个 Server（比 testServer/customServer 更能覆盖字段组合：
// Custom 为 nil、CustomDir 为空、带静态资源、带网关前缀）。
func apiServerWith(t *testing.T, custom *puzzle.Store, dir string, static fstest.MapFS, prefix string) *httptest.Server {
	t.Helper()
	pz, err := puzzle.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{
		Sessions:      session.NewManager(),
		Engines:       engine.NewManager(""),
		Puzzles:       pz,
		Custom:        custom,
		CustomDir:     dir,
		GatewayPrefix: prefix,
	}
	if static != nil {
		srv.Static = static
	}
	return httptest.NewServer(srv.Handler())
}

func getStatus(t *testing.T, ts *httptest.Server, path string) map[string]any {
	t.Helper()
	resp, err := ts.Client().Get(ts.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s 状态码 = %d", path, resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestStatusCustomFields 三字段在三种配置下都要如实（这是降级唯一的对外通道）。
func TestStatusCustomFields(t *testing.T) {
	// ① 有目录、有库、目录可写
	ts, st, dir := customServer(t)
	defer ts.Close()
	if err := st.Add(&puzzle.Puzzle{ID: "custom-a", Name: "x", FEN: goodFEN,
		Difficulty: "自定义", PlayerSide: "red", Goal: "win"}); err != nil {
		t.Fatal(err)
	}
	got := getStatus(t, ts, "/api/status")
	if n, _ := got["customPuzzles"].(float64); n != 1 {
		t.Errorf("customPuzzles = %v，期望 1", got["customPuzzles"])
	}
	if got["customDir"] != dir {
		t.Errorf("customDir = %v，期望 %s", got["customDir"], dir)
	}
	if got["customWritable"] != true {
		t.Errorf("customWritable = %v，期望 true", got["customWritable"])
	}

	// ② 内存态（目录为空）：条数照报，但目录为空串、可写为 false —— 不能假装能落盘
	ts2 := apiServerWith(t, puzzle.NewEmpty(), "", nil, "")
	defer ts2.Close()
	got2 := getStatus(t, ts2, "/api/status")
	if got2["customDir"] != "" || got2["customWritable"] != false {
		t.Errorf("内存态应报 customDir=\"\" / customWritable=false，实际 %v / %v",
			got2["customDir"], got2["customWritable"])
	}

	// ③ 库为 nil 但目录配了：仍要报出目录与可写性，条数为 0
	writable := t.TempDir()
	ts3 := apiServerWith(t, nil, writable, nil, "")
	defer ts3.Close()
	got3 := getStatus(t, ts3, "/api/status")
	if n, _ := got3["customPuzzles"].(float64); n != 0 {
		t.Errorf("库为 nil 时 customPuzzles = %v，期望 0", got3["customPuzzles"])
	}
	if got3["customDir"] != writable || got3["customWritable"] != true {
		t.Errorf("库为 nil 时应仍上报目录与可写性，实际 %v / %v",
			got3["customDir"], got3["customWritable"])
	}
}

// TestNoStaleCacheHeaders 所有响应都要带 no-cache；并且**不能把缓存打掉**（条件请求仍 304）。
//
// 只断言头是弱证据：真正要保证的是「每次都要重新验证」，所以第三段用 If-Modified-Since
// 证明未变更时仍走 304 —— 否则这个头就成了每次全量重下。
func TestNoStaleCacheHeaders(t *testing.T) {
	static := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<!doctype html><p>hi</p>"),
			ModTime: time.Unix(1700000000, 0)},
	}
	ts := apiServerWith(t, puzzle.NewEmpty(), "", static, "")
	defer ts.Close()

	for _, p := range []string{"/index.html", "/api/status", "/api/puzzles", "/api/update"} {
		resp, err := ts.Client().Get(ts.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if got := resp.Header.Get("Cache-Control"); got != "no-cache" {
			t.Errorf("%s 的 Cache-Control = %q，期望 no-cache", p, got)
		}
	}

	// 条件请求：未变更 → 304，且依然带 no-cache
	resp, err := ts.Client().Get(ts.URL + "/index.html")
	if err != nil {
		t.Fatal(err)
	}
	lastMod := resp.Header.Get("Last-Modified")
	resp.Body.Close()
	if lastMod == "" {
		t.Fatal("静态资源应带 Last-Modified，否则客户端无从重新验证")
	}
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/index.html", nil)
	req.Header.Set("If-Modified-Since", lastMod)
	resp2, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotModified {
		t.Errorf("条件请求状态码 = %d，期望 304（no-cache 不应退化成每次都全量重下）", resp2.StatusCode)
	}
	if got := resp2.Header.Get("Cache-Control"); got != "no-cache" {
		t.Errorf("304 响应也需带 Cache-Control，实际 %q", got)
	}

	// 网关前缀路径同样要带（生产走的就是这条）
	ts2 := apiServerWith(t, puzzle.NewEmpty(), "", nil, "/app/panda-xiangqi")
	defer ts2.Close()
	resp3, err := ts2.Client().Get(ts2.URL + "/app/panda-xiangqi/api/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("网关前缀下 /api/status 状态码 = %d", resp3.StatusCode)
	}
	if got := resp3.Header.Get("Cache-Control"); got != "no-cache" {
		t.Errorf("网关前缀路径的 Cache-Control = %q，期望 no-cache", got)
	}
}

// TestSaveNormalizesSideAndGoal 只有精确 "black"/"draw" 被采纳，其余一律回落到安全默认。
func TestSaveNormalizesSideAndGoal(t *testing.T) {
	cases := []struct {
		side, goal         string
		wantSide, wantGoal string
	}{
		{"red", "win", "red", "win"},
		{"black", "draw", "black", "draw"},
		{"BLACK", "DRAW", "red", "win"}, // 大小写不认（刻意严格，避免歧义值悄悄进库）
		{"", "", "red", "win"},
		{"b", "w", "red", "win"},
		{"black ", " draw", "red", "win"}, // 不做 trim：带空格即视为非法
	}
	for _, c := range cases {
		ts, _, _ := customServer(t)
		body := `{"name":"归一化","fen":"` + goodFEN + `","side":"` + c.side + `","goal":"` + c.goal + `"}`
		code, out := postJSON(t, ts, "/api/puzzles", body)
		ts.Close()
		if code != http.StatusOK {
			t.Fatalf("side=%q goal=%q 保存失败：%d %v", c.side, c.goal, code, out)
		}
		if out["playerSide"] != c.wantSide || out["goal"] != c.wantGoal {
			t.Errorf("side=%q goal=%q → 实际 %v/%v，期望 %v/%v",
				c.side, c.goal, out["playerSide"], out["goal"], c.wantSide, c.wantGoal)
		}
	}
}

// TestPuzzleRoutesWhenCustomDisabled 未启用自定义残局（Custom==nil）时的接口语义。
func TestPuzzleRoutesWhenCustomDisabled(t *testing.T) {
	ts := apiServerWith(t, nil, "", nil, "")
	defer ts.Close()

	// 保存与删除：明确 503（不是 500，也不是静默成功）
	code, out := postJSON(t, ts, "/api/puzzles",
		`{"name":"n","fen":"`+goodFEN+`","side":"red","goal":"win"}`)
	if code != http.StatusServiceUnavailable || !strings.Contains(out["error"].(string), "未启用") {
		t.Errorf("未启用时保存 = %d %v，期望 503 且说明未启用", code, out)
	}
	code, out = postJSON(t, ts, "/api/puzzles/custom-abc/delete", "")
	if code != http.StatusServiceUnavailable {
		t.Errorf("未启用时删除 = %d %v，期望 503", code, out)
	}
	// 列表与详情不受影响：内置题库照常可用
	if code, _ := getRaw(t, ts, "/api/puzzles"); code != http.StatusOK {
		t.Errorf("未启用时列表 = %d，期望 200", code)
	}
	if code, _ := getRaw(t, ts, "/api/puzzles/custom-abc"); code != http.StatusNotFound {
		t.Errorf("未启用时查自定义 id = %d，期望 404（而不是 503）", code)
	}
}

// TestDeleteEdgeCases 删除的方法不匹配与磁盘删除失败。
func TestDeleteEdgeCases(t *testing.T) {
	// ① 非 POST → 405
	ts, _, _ := customServer(t)
	defer ts.Close()
	if code, _ := getRaw(t, ts, "/api/puzzles/custom-x/delete"); code != http.StatusMethodNotAllowed {
		t.Errorf("GET 删除 = %d，期望 405", code)
	}

	// ② 磁盘删除失败 → 500。
	// 构造：把目标文件名占成一个**非空目录** —— os.Remove 对非空目录必然失败，
	// 且不是「文件不存在」，因此不能按幂等放过。（用「目录」而不是「路径无效」是因为
	// Windows 上 ERROR_PATH_NOT_FOUND 会被 Go 归为 IsNotExist，那样就测不到这条分支。）
	dir := t.TempDir()
	blocker := filepath.Join(dir, "custom-blocked.json")
	if err := os.MkdirAll(filepath.Join(blocker, "inner"), 0o755); err != nil {
		t.Fatal(err)
	}
	ts2 := apiServerWith(t, puzzle.NewEmpty(), dir, nil, "")
	defer ts2.Close()
	code, out := postJSON(t, ts2, "/api/puzzles/custom-blocked/delete", "")
	if code != http.StatusInternalServerError {
		t.Errorf("磁盘删除失败应 500，实际 %d %v", code, out)
	}
}

// TestSaveFailureLeavesNoTrace 落盘失败时：500、库不变、目录不留半成品。
//
// 说明：handlePuzzleSave 里还有一条「落盘成功但 Add 失败 → 回滚删文件」的分支，
// 那条只在 id 撞车时才会走到（时间戳到秒 + 4 位随机后缀），**测试构造不出来**，
// 因此这里覆盖的是可构造的那一半；回滚分支只能靠代码审查。
func TestSaveFailureLeavesNoTrace(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "不存在的目录")
	st := puzzle.NewEmpty()
	ts := apiServerWith(t, st, missing, nil, "")
	defer ts.Close()

	code, out := postJSON(t, ts, "/api/puzzles",
		`{"name":"n","fen":"`+goodFEN+`","side":"red","goal":"win"}`)
	if code != http.StatusInternalServerError {
		t.Fatalf("目录不可用时保存应 500，实际 %d %v", code, out)
	}
	if !strings.Contains(out["error"].(string), "保存失败") {
		t.Errorf("错误文案应说明保存失败，实际 %v", out["error"])
	}
	if n := st.Count(); n != 0 {
		t.Errorf("保存失败后库里不该有条目，实际 %d 条", n)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Errorf("不应顺手把目录建出来（写权限问题会被藏起来）：err=%v", err)
	}
}

// ---------- 小工具 ----------

func getRaw(t *testing.T, ts *httptest.Server, path string) (int, string) {
	t.Helper()
	resp, err := ts.Client().Get(ts.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	return resp.StatusCode, string(b)
}
