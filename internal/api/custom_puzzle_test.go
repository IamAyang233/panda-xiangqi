package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/IamAyang233/panda-xiangqi/internal/engine"
	"github.com/IamAyang233/panda-xiangqi/internal/puzzle"
	"github.com/IamAyang233/panda-xiangqi/internal/session"
)

// 合法自摆局面：红车 4/0（=e0 附近）+ 双将，红先。用作「应该保存成功」的样板。
const goodFEN = "3k5/9/9/9/9/9/9/9/4R4/4K4 w"

// customServer 起一个带自定义残局库（临时目录）的测试服务。
func customServer(t *testing.T) (*httptest.Server, *puzzle.Store, string) {
	t.Helper()
	pz, err := puzzle.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	srv := &Server{
		Sessions:  session.NewManager(),
		Engines:   engine.NewManager(""),
		Puzzles:   pz,
		Custom:    puzzle.NewEmpty(),
		CustomDir: dir,
	}
	return httptest.NewServer(srv.Handler()), srv.Custom, dir
}

func postJSON(t *testing.T, ts *httptest.Server, url string, body string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Post(ts.URL+url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out)
	return resp.StatusCode, out
}

func post(t *testing.T, ts *httptest.Server, url string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Post(ts.URL+url, "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out)
	return resp.StatusCode, out
}

// TestCustomSaveAcceptsLegal 合法局面应被保存，并出现在「自定义」筛选里。
func TestCustomSaveAcceptsLegal(t *testing.T) {
	ts, store, dir := customServer(t)
	defer ts.Close()

	code, body := postJSON(t, ts, "/api/puzzles", `{"name":"一步杀","fen":"`+goodFEN+`","side":"red","goal":"win"}`)
	if code != http.StatusOK {
		t.Fatalf("合法局面应保存成功，实际 %d: %v", code, body)
	}
	id, _ := body["id"].(string)
	if !strings.HasPrefix(id, "custom-") {
		t.Fatalf("自定义残局 id 应带 custom- 前缀，实际 %q", id)
	}
	if store.Count() != 1 {
		t.Fatalf("内存库应有 1 条，实际 %d", store.Count())
	}
	// 必须真的落盘（全服共享的判据）
	if _, err := os.Stat(filepath.Join(dir, id+".json")); err != nil {
		t.Fatalf("残局文件未落盘: %v", err)
	}
	// 列表里要能按「自定义」筛到
	var list []map[string]any
	resp, err := http.Get(ts.URL + "/api/puzzles?difficulty=" + "自定义")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_ = json.NewDecoder(resp.Body).Decode(&list)
	if len(list) != 1 || list[0]["id"] != id {
		t.Fatalf("自定义筛选页应只含这条，实际 %+v", list)
	}
	if got := list[0]["parMoves"]; got != float64(0) {
		t.Fatalf("自摆残局 parMoves 应为 0（不评星），实际 %v", got)
	}
}

// TestCustomSaveRejectsIllegal 四条硬校验：服务端是唯一防线，前端提示可绕过。
func TestCustomSaveRejectsIllegal(t *testing.T) {
	// 每个用例都实测过：它确实非法，且**踩的是哪一条**是明确的（不是碰巧被别的规则挡下）。
	cases := []struct {
		name string
		fen  string
	}{
		// ① FEN 语法
		{"FEN 语法非法", "这不是一个 FEN"},
		{"缺红帅（ParseFEN 即拒）", "3k5/9/9/9/9/9/9/9/4R4/9 w"},
		// ② 双方各一枚将帅（ParseFEN 只要求"有"，不要求"各一枚"，故需单独校验）。
		// 此局面刻意让黑走且红不被将军：它**只**违反这一条，不会被 ③④ 抢先挡下，
		// 否则单独回退 checkKings 时测试仍然通过，等于没测到。
		{"红方两枚帅", "3k5/9/9/9/9/9/9/9/9/3K1K3 b"},
		// ③ 局面合法：将帅照面
		{"将帅照面（同列无遮挡）", "4k4/9/9/9/9/9/9/9/9/4K4 w"},
		// ④ 先走方无着法：局面本身合法（非行棋方未被将军），但红方零合法着法
		{"先走方无棋可走", "3k5/9/9/9/9/9/9/4r4/3r1r3/4K4 w"},
	}
	for _, c := range cases {
		ts, store, dir := customServer(t)
		code, body := postJSON(t, ts, "/api/puzzles", `{"name":"`+c.name+`","fen":"`+c.fen+`","side":"red","goal":"win"}`)
		if code != http.StatusBadRequest {
			t.Errorf("%s：应返回 400，实际 %d: %v", c.name, code, body)
		}
		if _, ok := body["error"]; !ok {
			t.Errorf("%s：错误响应应带 error 字段，实际 %v", c.name, body)
		}
		if store.Count() != 0 {
			t.Errorf("%s：被拒的局面不应入库，实际 %d 条", c.name, store.Count())
		}
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			if strings.HasSuffix(e.Name(), ".json") {
				t.Errorf("%s：被拒的局面不应落盘，却有 %s", c.name, e.Name())
			}
		}
		ts.Close()
	}
}

// TestCustomDelete 删除：自定义可删、内置不可删、不存在 404、文件已丢仍幂等摘索引。
func TestCustomDelete(t *testing.T) {
	ts, store, dir := customServer(t)
	defer ts.Close()

	_, body := postJSON(t, ts, "/api/puzzles", `{"name":"待删除","fen":"`+goodFEN+`","side":"red","goal":"win"}`)
	id, _ := body["id"].(string)

	// ① 内置残局不可删：取一条真实内置 id
	pz, err := puzzle.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	all := pz.All()
	if len(all) == 0 {
		t.Fatal("内置题库为空，无法验证")
	}
	builtin := all[0].ID
	if code, got := post(t, ts, "/api/puzzles/"+builtin+"/delete"); code != http.StatusForbidden {
		t.Fatalf("内置残局应 403 拒绝删除，实际 %d: %v", code, got)
	}
	if _, ok := pz.Get(builtin); !ok {
		t.Fatal("被拒绝后内置残局必须还在")
	}

	// ② 不存在的自定义 id → 404
	if code, got := post(t, ts, "/api/puzzles/custom-nope/delete"); code != http.StatusNotFound {
		t.Fatalf("不存在的 id 应 404，实际 %d: %v", code, got)
	}

	// ③ 正常删除
	if code, got := post(t, ts, "/api/puzzles/"+id+"/delete"); code != http.StatusOK {
		t.Fatalf("删除自定义残局应成功，实际 %d: %v", code, got)
	}
	if _, err := os.Stat(filepath.Join(dir, id+".json")); !os.IsNotExist(err) {
		t.Fatalf("删除后磁盘文件应消失，实际 err=%v", err)
	}
	if store.Count() != 0 {
		t.Fatalf("删除后内存库应为空，实际 %d", store.Count())
	}
}

// TestCustomDeleteIdempotent 磁盘文件被手工删掉后，删除接口仍要摘掉内存索引，
// 否则列表里会留下一个「点开必 404」的幽灵条目。
func TestCustomDeleteIdempotent(t *testing.T) {
	ts, store, dir := customServer(t)
	defer ts.Close()

	_, body := postJSON(t, ts, "/api/puzzles", `{"name":"幽灵","fen":"`+goodFEN+`","side":"red","goal":"win"}`)
	id, _ := body["id"].(string)
	if err := os.Remove(filepath.Join(dir, id+".json")); err != nil {
		t.Fatal(err)
	}
	if code, got := post(t, ts, "/api/puzzles/"+id+"/delete"); code != http.StatusOK {
		t.Fatalf("文件已丢时删除仍应成功（幂等），实际 %d: %v", code, got)
	}
	if store.Count() != 0 {
		t.Fatalf("幽灵条目应被摘除，实际还剩 %d 条", store.Count())
	}
}

// TestCustomMemoryMode 内存态（目录不可写）下：保存仍可用，但删除必须如实报错，
// 不能假装成功——否则用户以为删了，重启后它又回来。
func TestCustomMemoryMode(t *testing.T) {
	pz, err := puzzle.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{
		Sessions: session.NewManager(),
		Engines:  engine.NewManager(""),
		Puzzles:  pz,
		Custom:   puzzle.NewEmpty(),
		// CustomDir 留空 = 内存态
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	code, body := postJSON(t, ts, "/api/puzzles", `{"name":"内存态","fen":"`+goodFEN+`","side":"red","goal":"win"}`)
	if code != http.StatusOK {
		t.Fatalf("内存态下保存应仍可用，实际 %d: %v", code, body)
	}
	id, _ := body["id"].(string)
	if code, got := post(t, ts, "/api/puzzles/"+id+"/delete"); code != http.StatusBadRequest {
		t.Fatalf("内存态下删除应明确报错，实际 %d: %v", code, got)
	}
}

// TestCustomDetailFromBothStores 详情接口要能查到自定义残局（按 id 挑战的前提）。
func TestCustomDetailFromBothStores(t *testing.T) {
	ts, _, _ := customServer(t)
	defer ts.Close()

	_, body := postJSON(t, ts, "/api/puzzles", `{"name":"可挑战","fen":"`+goodFEN+`","side":"red","goal":"win"}`)
	id, _ := body["id"].(string)

	resp, err := http.Get(ts.URL + "/api/puzzles/" + id)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("自定义残局详情应可查，实际 %d", resp.StatusCode)
	}
	var pub puzzle.Public
	if err := json.NewDecoder(resp.Body).Decode(&pub); err != nil {
		t.Fatal(err)
	}
	if pub.ID != id {
		t.Fatalf("详情 id 不符: %q", pub.ID)
	}
	// 答案字段绝不能外泄：重新取一次原始 JSON 核对字段名（上面的 Public 结构体
	// 反序列化会把未知字段丢掉，靠它看不出「服务端多发了 fen」）。
	resp2, err := http.Get(ts.URL + "/api/puzzles/" + id)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	var raw map[string]any
	if err := json.NewDecoder(resp2.Body).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["fen"]; ok {
		t.Fatal("详情不应包含 fen（与题库 Public 视图一致）")
	}
}

// TestCreateGameFromCustomPuzzle 自摆残局保存后必须能按 id 起局。
//
// 这是「保存并挑战」的关键一步：handleCreateGame 的残局分支若只查内置题库，
// 保存会成功但建局 404 —— 用户看到的是「已保存，开始挑战」之后卡在摆局屏。
// 端到端黑盒实测抓到过这个缺陷，这里用单测钉住。
func TestCreateGameFromCustomPuzzle(t *testing.T) {
	ts, _, _ := customServer(t)
	defer ts.Close()

	_, body := postJSON(t, ts, "/api/puzzles", `{"name":"可挑战","fen":"`+goodFEN+`","side":"red","goal":"win"}`)
	id, _ := body["id"].(string)
	if id == "" {
		t.Fatal("保存未返回 id")
	}

	code, created := postJSON(t, ts, "/api/games",
		`{"mode":"puzzle","puzzleId":"`+id+`","level":8,"side":"red"}`)
	if code != http.StatusOK {
		t.Fatalf("按自定义残局 id 起局应成功，实际 %d: %v", code, created)
	}
	if created["youSide"] != "red" {
		t.Fatalf("youSide 应为 red，实际 %v", created["youSide"])
	}
	if gid, _ := created["gameId"].(string); gid == "" {
		t.Fatal("未返回 gameId")
	}

	// 黑先的自摆残局同样要能起局（playerSide 由残局定义决定）
	_, b2 := postJSON(t, ts, "/api/puzzles", `{"name":"黑先局","fen":"3k5/9/9/9/9/9/9/9/R8/4K4 b","side":"black","goal":"draw"}`)
	id2, _ := b2["id"].(string)
	code2, created2 := postJSON(t, ts, "/api/games", `{"mode":"puzzle","puzzleId":"`+id2+`","level":4}`)
	if code2 != http.StatusOK {
		t.Fatalf("黑先自摆残局起局应成功，实际 %d: %v", code2, created2)
	}
	if created2["youSide"] != "black" {
		t.Fatalf("黑先局 youSide 应为 black，实际 %v", created2["youSide"])
	}
	// 不存在的 id 仍要 404
	if code, _ := postJSON(t, ts, "/api/games", `{"mode":"puzzle","puzzleId":"custom-none"}`); code != http.StatusNotFound {
		t.Fatalf("不存在的 id 应 404，实际 %d", code)
	}
}

// TestCustomSaveNameTruncatesByRune 超长名字必须按「字符」截断。
//
// 原先写的是 len(name) > 40 → name[:40]，而 Go 的 len 是字节数：中文一个字 3 字节，
// 40 字节只有 13 个汉字，任何超过 13 字的中文名字都会被切成半个字，存下来是无效
// UTF-8（前端显示为半字或替换符）；前端输入框限的却是 40 字符，两边口径也不一致。
// 这个缺陷是对抗性黑盒测试（故意喂长中文名与标签）跑出来的。
func TestCustomSaveNameTruncatesByRune(t *testing.T) {
	ts, _, _ := customServer(t)
	defer ts.Close()

	cn := strings.Repeat("长", 60)
	code, body := postJSON(t, ts, "/api/puzzles", `{"name":"`+cn+`","fen":"`+goodFEN+`","side":"red","goal":"win"}`)
	if code != http.StatusOK {
		t.Fatalf("超长名字应可保存，实际 %d: %v", code, body)
	}
	name, _ := body["name"].(string)
	if got := len([]rune(name)); got != 40 {
		t.Errorf("中文名应截到 40 个字符，实际 %d 字符 / %d 字节", got, len(name))
	}
	if !utf8.ValidString(name) {
		t.Error("截断后必须是合法 UTF-8（按字节截断会切裂汉字）")
	}
	if strings.ContainsRune(name, '\uFFFD') {
		t.Error("截断后不应含替换符 U+FFFD")
	}

	// 短于上限的名字必须一字不改（含标签与特殊字符；前端负责转义显示）
	evil := `<img src=x onerror=alert(1)>「引」&<b>`
	if len([]rune(evil)) > 40 {
		t.Fatalf("用例自身有误：名字 %d 字符已超上限", len([]rune(evil)))
	}
	code2, body2 := postJSON(t, ts, "/api/puzzles", `{"name":`+strconv.Quote(evil)+`,"fen":"`+goodFEN+`","side":"red","goal":"win"}`)
	if code2 != http.StatusOK {
		t.Fatalf("含特殊字符的名字应可保存，实际 %d", code2)
	}
	if got, _ := body2["name"].(string); got != evil {
		t.Errorf("短名字应原样保存\n  got=%q\n want=%q", got, evil)
	}
	code3, body3 := postJSON(t, ts, "/api/puzzles", `{"name":"   ","fen":"`+goodFEN+`","side":"red","goal":"win"}`)
	if code3 != http.StatusOK || body3["name"] == "" {
		t.Errorf("空名字应兜底为非空，实际 %d %v", code3, body3["name"])
	}
}

// TestPuzzleStoreAddRejects 库层面的校验：非法局面与重复 id 都必须在入内存前被挡住。
func TestPuzzleStoreAddRejects(t *testing.T) {
	s := puzzle.NewEmpty()
	p := &puzzle.Puzzle{ID: "custom-x", Name: "x", FEN: goodFEN, Difficulty: "自定义"}
	if err := s.Add(p); err != nil {
		t.Fatalf("合法局面应可加入: %v", err)
	}
	if err := s.Add(p); err == nil {
		t.Fatal("重复 id 应报错")
	}
	if err := s.Add(&puzzle.Puzzle{ID: "custom-y", FEN: "这不是FEN"}); err == nil {
		t.Fatal("非法 FEN 应报错")
	}
	if err := s.Add(&puzzle.Puzzle{FEN: goodFEN}); err == nil {
		t.Fatal("缺 id 应报错")
	}
	if s.Count() != 1 {
		t.Fatalf("只有一条应入库，实际 %d", s.Count())
	}
}

// TestPuzzleSaveAtomic 原子写：只可能留下 .tmp，不会留下半截 .json。
func TestPuzzleSaveAtomic(t *testing.T) {
	dir := t.TempDir()
	p := &puzzle.Puzzle{ID: "custom-atomic", Name: "原子", FEN: goodFEN, Difficulty: "自定义"}
	if err := puzzle.SaveToDir(dir, p); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("原子写后不应残留临时文件: %s", e.Name())
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "custom-atomic.json"))
	if err != nil {
		t.Fatal(err)
	}
	var back puzzle.Puzzle
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("落盘的 JSON 必须能被解析回来: %v", err)
	}
	if back.FEN != goodFEN || back.ID != "custom-atomic" {
		t.Fatalf("回读内容不符: %+v", back)
	}
	// 目录不存在时必须报错，而不是静默 MkdirAll
	if err := puzzle.SaveToDir(filepath.Join(dir, "nope"), p); err == nil {
		t.Fatal("目录不存在时应报错")
	}
}

// TestPuzzleDirWritable 可写性探测（用于 /api/status 如实降级）。
func TestPuzzleDirWritable(t *testing.T) {
	if puzzle.DirWritable("") {
		t.Fatal("空目录应判为不可写")
	}
	if puzzle.DirWritable(filepath.Join(t.TempDir(), "nope")) {
		t.Fatal("不存在的目录应判为不可写")
	}
	if !puzzle.DirWritable(t.TempDir()) {
		t.Fatal("临时目录应判为可写")
	}
}
