package api

// 棋谱接口的边界行为。重点：
//   - 保存只在**终局**成立（「只存下完的局」这条口径的落点）；
//   - 幂等（连点两次不产生第二条）；
//   - 落盘文件里**不能**出现 apiKey（Key 只该活在浏览器与内存里）；
//   - 删除要同时清磁盘与内存索引；内存态（目录不可写）如实拒绝。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IamAyang233/panda-xiangqi/internal/engine"
	"github.com/IamAyang233/panda-xiangqi/internal/puzzle"
	"github.com/IamAyang233/panda-xiangqi/internal/record"
	"github.com/IamAyang233/panda-xiangqi/internal/session"
)

// recordServer 起一个带棋谱库（临时目录）的测试服务，并把 Server 一并返回：
// 走子要从会话对象直接推（REST 没有「走子」这个动作，走子走 WS）。
func recordServer(t *testing.T) (*httptest.Server, *Server, string) {
	t.Helper()
	pz, err := puzzle.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	srv := &Server{
		Sessions:   session.NewManager(),
		Engines:    engine.NewManager(""),
		Puzzles:    pz,
		Custom:     puzzle.NewEmpty(),
		CustomDir:  dir,
		Records:    record.NewEmpty(),
		RecordsDir: dir,
	}
	return httptest.NewServer(srv.Handler()), srv, dir
}

// newLocalGame 建一局双人对局（不依赖引擎），返回 gameId。
func newLocalGame(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	code, out := postJSON(t, ts, "/api/games", `{"mode":"local_2p","side":"red","level":4}`)
	if code != http.StatusOK {
		t.Fatalf("建局失败: %d %v", code, out)
	}
	id, _ := out["gameId"].(string)
	if id == "" {
		t.Fatal("建局未返回 gameId")
	}
	return id
}

func TestRecordSaveFlow(t *testing.T) {
	ts, srv, dir := recordServer(t)
	defer ts.Close()
	id := newLocalGame(t, ts)

	// ① 未终局：409（这一局还不能存）
	if code, out := postJSON(t, ts, "/api/games/"+id+"/save", ""); code != http.StatusConflict {
		t.Fatalf("未终局保存应 409，实际 %d %v", code, out)
	}
	// 走一手，让记录里有着法
	sess, ok := srv.Sessions.Get(id)
	if !ok {
		t.Fatal("会话不存在")
	}
	if err := sess.ApplyPlayerMove("e3", "e4"); err != nil {
		t.Fatalf("走子失败: %v", err)
	}
	// ② 认输 = 终局
	if code, out := postJSON(t, ts, "/api/games/"+id+"/resign", ""); code != http.StatusOK {
		t.Fatalf("认输失败: %d %v", code, out)
	}
	// ③ 保存成功，id 就是对局 id
	code, out := postJSON(t, ts, "/api/games/"+id+"/save", "")
	if code != http.StatusOK || out["id"] != id {
		t.Fatalf("保存失败: %d %v", code, out)
	}
	if out["existed"] != false {
		t.Fatalf("首次保存不该报 existed=true: %v", out)
	}
	// ④ 幂等：再点一次仍是同一条
	code, out = postJSON(t, ts, "/api/games/"+id+"/save", "")
	if code != http.StatusOK || out["existed"] != true {
		t.Fatalf("重复保存应返回 existed=true: %d %v", code, out)
	}
	if n := srv.Records.Count(); n != 1 {
		t.Fatalf("同一局只应留一条，实际 %d", n)
	}
	// ⑤ 列表：摘要 + total
	got := getStatus(t, ts, "/api/records")
	if got["total"] != float64(1) {
		t.Fatalf("列表 total 应为 1：%v", got)
	}
	items, _ := got["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("应返回 1 条摘要：%v", got["items"])
	}
	first, _ := items[0].(map[string]any)
	if first["id"] != id || first["moveCount"] != float64(1) {
		t.Fatalf("摘要内容不对: %v", first)
	}
	if _, hasMoves := first["moves"]; hasMoves {
		t.Fatal("列表摘要不该带着法")
	}
	// ⑥ 详情带着法，且能重放（startFen 非空）
	detail := getStatus(t, ts, "/api/records/"+id)
	moves, _ := detail["moves"].([]any)
	if len(moves) != 1 || detail["startFen"] == "" {
		t.Fatalf("详情应含 1 手与起始局面: %v", detail)
	}
	// ⑦ 删除：磁盘 + 内存都要清
	if code, out := postJSON(t, ts, "/api/records/"+id+"/delete", ""); code != http.StatusOK {
		t.Fatalf("删除失败: %d %v", code, out)
	}
	if srv.Records.Count() != 0 {
		t.Fatal("删除后内存索引应清空")
	}
	if _, err := os.Stat(filepath.Join(dir, id+".json")); !os.IsNotExist(err) {
		t.Fatalf("删除后磁盘文件应消失: %v", err)
	}
	if code, _ := postJSON(t, ts, "/api/records/"+id+"/delete", ""); code != http.StatusNotFound {
		t.Fatalf("再删应 404，实际 %d", code)
	}
}

func TestRecordSaveUnknownGame(t *testing.T) {
	ts, _, _ := recordServer(t)
	defer ts.Close()
	if code, _ := postJSON(t, ts, "/api/games/nope/save", ""); code != http.StatusNotFound {
		t.Fatalf("未知对局应 404，实际 %d", code)
	}
}

func TestRecordDisabledWhenNotWired(t *testing.T) {
	ts, _ := testServer(t, "", "") // 没有 Records
	defer ts.Close()
	for _, p := range []string{"/api/records", "/api/records/x"} {
		if code, _ := getRaw(t, ts, p); code != http.StatusServiceUnavailable {
			t.Fatalf("%s 未启用时应 503，实际 %d", p, code)
		}
	}
	// 保存：要用**真实存在**的对局（未知对局会先撞 404「对局不存在」，
	// 那是更准确的答复；这里要验的是「功能没接上」这条）
	id := newLocalGame(t, ts)
	if code, _ := postJSON(t, ts, "/api/games/"+id+"/resign", ""); code != http.StatusOK {
		t.Fatal("认输失败")
	}
	if code, out := postJSON(t, ts, "/api/games/"+id+"/save", ""); code != http.StatusServiceUnavailable {
		t.Fatalf("未启用时保存应 503，实际 %d %v", code, out)
	}
}

func TestRecordDeleteMemoryMode(t *testing.T) {
	// Records 有值但 RecordsDir 为空 = 内存态：删不掉（磁盘上本来就没有），
	// 要如实说而不是假装成功，否则用户以为删了、重启后它又回来了。
	pz, err := puzzle.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	mem := &Server{Sessions: session.NewManager(), Engines: engine.NewManager(""),
		Puzzles: pz, Records: record.NewEmpty()}
	ts := httptest.NewServer(mem.Handler())
	defer ts.Close()
	rec := &record.Record{ID: "r1", Name: "x", Mode: "engine", HumanSide: "red",
		StartFEN: "3k5/9/9/9/9/9/9/9/4R4/4K4 w", Result: "draw", Reason: "repetition",
		Created: "2026-09-24 10:00:00"}
	if err := mem.Records.Add(rec); err != nil {
		t.Fatal(err)
	}
	if code, out := postJSON(t, ts, "/api/records/r1/delete", ""); code != http.StatusBadRequest {
		t.Fatalf("内存态删除应 400（如实告知），实际 %d %v", code, out)
	}
	// 但列表与详情仍然可用
	if got := getStatus(t, ts, "/api/records"); got["total"] != float64(1) {
		t.Fatalf("内存态列表应仍可见: %v", got)
	}
	if code, _ := getRaw(t, ts, "/api/records/r1"); code != http.StatusOK {
		t.Fatalf("内存态详情应 200，实际 %d", code)
	}
}

func TestRecordAnalyzeRulesTags(t *testing.T) {
	ts, srv, _ := recordServer(t)
	defer ts.Close()
	// 红车 e1 吃 e2 的黑卒：规则层就该标成「吃子」（这台机器的测试引擎没有权重，
	// 评分走静态兜底 ⇒ Strong=false ⇒ 不打失误/疑问手标签，正好只剩规则层可验）
	rec := &record.Record{ID: "cap1", Name: "吃子局", Mode: "engine", HumanSide: "red",
		StartFEN: "3k5/9/9/9/9/9/9/4p4/4R4/4K4 w",
		Moves:    []record.Move{{UCI: "e1e2", CN: "车一进一", Red: true, Captured: "p"}},
		Result:   "resign", Reason: "resign", Created: "2026-09-24 10:00:00"}
	if err := srv.Records.Add(rec); err != nil {
		t.Fatal(err)
	}
	code, out := postJSON(t, ts, "/api/records/cap1/analyze", "")
	if code != http.StatusOK {
		t.Fatalf("分析失败: %d %v", code, out)
	}
	list, _ := out["analysis"].([]any)
	if len(list) != 1 {
		t.Fatalf("应标出 1 个关键手，实际 %v", out["analysis"])
	}
	km, _ := list[0].(map[string]any)
	if km["tag"] != record.TagCapture || km["index"] != float64(0) {
		t.Fatalf("关键手标签不对: %v", km)
	}
	// 第二次应命中缓存（不再占引擎）
	code, out2 := postJSON(t, ts, "/api/records/cap1/analyze", "")
	if code != http.StatusOK || out2["cached"] != true {
		t.Fatalf("二次分析应命中缓存: %d %v", code, out2)
	}
}

func TestRecordAnalyzeRejectsCorruptMoves(t *testing.T) {
	ts, srv, _ := recordServer(t)
	defer ts.Close()
	// 局面合法但着法在该局面走不出来 —— 分析应如实报错，而不是给似是而非的结论
	rec := &record.Record{ID: "bad1", Name: "坏着法", Mode: "engine", HumanSide: "red",
		StartFEN: "3k5/9/9/9/9/9/9/4p4/4R4/4K4 w",
		Moves:    []record.Move{{UCI: "a0a1", CN: "车九进一", Red: true}},
		Result:   "resign", Reason: "resign", Created: "2026-09-24 10:00:00"}
	if err := srv.Records.Add(rec); err != nil {
		t.Fatal(err)
	}
	if code, out := postJSON(t, ts, "/api/records/bad1/analyze", ""); code != http.StatusInternalServerError {
		t.Fatalf("坏着法应 500（含具体原因），实际 %d %v", code, out)
	}
}

func TestRecordReviewWithoutLLM(t *testing.T) {
	ts, srv, _ := recordServer(t)
	defer ts.Close()
	rec := &record.Record{ID: "rv1", Name: "局", Mode: "engine", HumanSide: "red",
		StartFEN: "3k5/9/9/9/9/9/9/4p4/4R4/4K4 w",
		Moves:    []record.Move{{UCI: "e1e2", CN: "车一进一", Red: true, Captured: "p"}},
		Result:   "red_win", Reason: "resign", Created: "2026-09-24 10:00:00"}
	if err := srv.Records.Add(rec); err != nil {
		t.Fatal(err)
	}
	// 没配大模型：应明确报「未配置地址」，且是 502（上游不可用）
	code, out := postJSON(t, ts, "/api/records/rv1/review", `{"index":0,"llm":{}}`)
	if code != http.StatusBadGateway {
		t.Fatalf("未配置大模型应 502，实际 %d %v", code, out)
	}
	if msg, _ := out["error"].(string); !strings.Contains(msg, "未配置") {
		t.Fatalf("错误文案应说明未配置: %v", out["error"])
	}
}

// TestRecordFileKeepsModelNotKey 落盘文件里只允许有模型名，不允许有 API Key。
// 这不是洁癖：Key 只该活在浏览器 localStorage 与请求内存里，写进棋谱目录就等于
// 明文存盘（棋谱目录还是全服共享、可被任何访问者读的）。
func TestRecordFileKeepsModelNotKey(t *testing.T) {
	ts, _, dir := recordServer(t)
	defer ts.Close()
	const secret = "sk-SECRET-DO-NOT-PERSIST-12345"
	// 大模型模式但人执红：AI（黑）在红方走子前不会动，因此不会真去调大模型
	code, out := postJSON(t, ts, "/api/games",
		`{"mode":"llm","side":"red","level":4,"llm":{"baseURL":"http://127.0.0.1:1/v1","apiKey":"`+secret+`","model":"mock-model"}}`)
	if code != http.StatusOK {
		t.Fatalf("建局失败: %d %v", code, out)
	}
	id, _ := out["gameId"].(string)
	if code, _ := postJSON(t, ts, "/api/games/"+id+"/resign", ""); code != http.StatusOK {
		t.Fatal("认输失败")
	}
	if code, out := postJSON(t, ts, "/api/games/"+id+"/save", ""); code != http.StatusOK {
		t.Fatalf("保存失败: %d %v", code, out)
	}
	raw, err := os.ReadFile(filepath.Join(dir, id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatalf("棋谱文件里出现了 API Key:\n%s", raw)
	}
	if !strings.Contains(string(raw), "mock-model") {
		t.Fatalf("模型名应当留在棋谱里（便于回看这是跟谁下的）:\n%s", raw)
	}
	var parsed record.Record
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("落盘文件应能解析: %v", err)
	}
	if parsed.Mode != "llm" || parsed.Result == "" {
		t.Fatalf("落盘内容不对: %+v", parsed)
	}
}
