package record

// 棋谱存储的边界行为。重点盯三件事：
//   1) 保存是**幂等覆盖**（用户连点/重试都会到同一个 id）；
//   2) 单条坏数据只跳过它自己（棋谱目录用户可碰）；
//   3) 列表视图不带 moves（一页 50 条都带着法，响应会白胖几十倍）。

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// recFEN：红车 e1、黑卒 e2、双方将帅各在 e0/d9 —— 红车一步可吃 e2 的卒。
const recFEN = "3k5/9/9/9/9/9/9/4p4/4R4/4K4 w"

func mkRec(id, created string) *Record {
	return &Record{
		ID: id, Name: "测试局", Mode: "engine", Level: 4, Model: "deepseek-chat",
		HumanSide: "red", StartFEN: recFEN,
		Moves:  []Move{{UCI: "e1e2", CN: "车一进一", Red: true, Captured: "p"}},
		Result: "red_win", Reason: "resign", Created: created,
		Analysis: []KeyMove{{Index: 0, Tag: TagCapture, Delta: 0}},
		Reviews:  map[string]string{"0": "这一手吃子很实惠。"},
	}
}

// TestStoreUpsertByIdempotent 同一 id 反复 Add 只留一条 —— 这是保存按钮连点的保证。
func TestStoreUpsertByIdempotent(t *testing.T) {
	st := NewEmpty()
	if err := st.Add(mkRec("g1", "2026-09-24 10:00:00")); err != nil {
		t.Fatal(err)
	}
	// 再存一次（模拟用户连点两下）：不该报错，也不该变成两条
	again := mkRec("g1", "2026-09-24 10:00:00")
	again.Name = "改过名字"
	if err := st.Add(again); err != nil {
		t.Fatalf("重复保存应覆盖而不是报错: %v", err)
	}
	if n := st.Count(); n != 1 {
		t.Fatalf("同一 id 只应留一条，实际 %d 条", n)
	}
	if r, _ := st.Get("g1"); r.Name != "改过名字" {
		t.Fatalf("重复保存应以后一次为准，实际 name=%q", r.Name)
	}
}

// TestStoreListOrderAndSummary 列表按时间倒序，且摘要不含 moves。
func TestStoreListOrderAndSummary(t *testing.T) {
	st := NewEmpty()
	for _, c := range []struct{ id, at string }{
		{"a", "2026-09-24 09:00:00"}, {"b", "2026-09-24 11:00:00"}, {"c", "2026-09-23 23:00:00"},
	} {
		if err := st.Add(mkRec(c.id, c.at)); err != nil {
			t.Fatal(err)
		}
	}
	list := st.List()
	if len(list) != 3 {
		t.Fatalf("应有 3 条，实际 %d", len(list))
	}
	if list[0].ID != "b" || list[1].ID != "a" || list[2].ID != "c" {
		t.Fatalf("应按时间倒序，实际 %s %s %s", list[0].ID, list[1].ID, list[2].ID)
	}
	if list[0].MoveCount != 1 || !list[0].Analyzed || list[0].Reviewed != 1 {
		t.Fatalf("摘要字段不对: %+v", list[0])
	}
	// Summary 的类型里就不该有 moves/reviews —— 用 JSON 落地再确认一次
	b, err := json.Marshal(list[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`"moves"`, `"reviews"`, `"startFen"`} {
		if strings.Contains(string(b), bad) {
			t.Fatalf("列表摘要不该带 %s：%s", bad, string(b))
		}
	}
}

// TestValidateRejects 落盘前的自检：id / 起始局面 / 结果 / 每手 uci 格式。
func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Record)
		want string
	}{
		{"缺 id", func(r *Record) { r.ID = "" }, "缺少 id"},
		{"起始局面坏", func(r *Record) { r.StartFEN = "9/9/9" }, "起始局面"},
		{"缺结果", func(r *Record) { r.Result = "" }, "缺少结果"},
		{"uci 不合法", func(r *Record) { r.Moves[0].UCI = "zz99" }, "uci"},
	}
	for _, c := range cases {
		r := mkRec("x", "2026-09-24 10:00:00")
		c.mut(r)
		err := NewEmpty().Add(r)
		if err == nil {
			t.Fatalf("%s：应当被拒绝", c.name)
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s：错误信息应含 %q，实际 %v", c.name, c.want, err)
		}
	}
}

// TestLoadDirSkipsBadEntries 一条坏文件不该让整个棋谱库打不开。
func TestLoadDirSkipsBadEntries(t *testing.T) {
	defer func(prev io.Writer) { log.SetOutput(prev) }(log.Writer())
	log.SetOutput(io.Discard)

	dir := t.TempDir()
	good, _ := json.Marshal(mkRec("good", "2026-09-24 10:00:00"))
	writeFile(t, dir, "good.json", string(good))
	writeFile(t, dir, "truncated.json", `{"id":"bad","moves":[`)                 // JSON 坏掉
	writeFile(t, dir, "no-id.json", `{"startFen":"`+recFEN+`","result":"draw"}`) // 缺 id
	writeFile(t, dir, "bad-fen.json", `{"id":"z","startFen":"乱写","result":"draw"}`)
	writeFile(t, dir, "notes.txt", "不是 json")

	st, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("坏条目不该让整库加载失败: %v", err)
	}
	if n := st.Count(); n != 1 {
		t.Fatalf("应只载入 1 条，实际 %d", n)
	}
	if _, ok := st.Get("good"); !ok {
		t.Fatal("好的那条应当可用")
	}
}

// TestSaveRemoveDirBehaviour 落盘参数与幂等删。
func TestSaveRemoveDirBehaviour(t *testing.T) {
	dir := t.TempDir()
	if err := SaveToDir(dir, mkRec("g9", "2026-09-24 10:00:00")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "g9.json")); err != nil {
		t.Fatalf("文件应存在: %v", err)
	}
	// 目录为空 / 缺 id 都要报错（否则会静默写到奇怪的地方）
	if err := SaveToDir("", mkRec("g9", "")); err == nil {
		t.Error("空目录应报错")
	}
	if err := SaveToDir(dir, &Record{}); err == nil {
		t.Error("缺 id 应报错")
	}
	if err := RemoveFromDir(dir, "g9"); err != nil {
		t.Fatal(err)
	}
	if err := RemoveFromDir(dir, "g9"); err != nil {
		t.Errorf("重复删除应幂等: %v", err)
	}
	if err := RemoveFromDir(dir, ""); err == nil {
		t.Error("空 id 应报错")
	}
	if !DirWritable(dir) {
		t.Error("临时目录应可写")
	}
	if DirWritable(filepath.Join(dir, "不存在")) {
		t.Error("不存在的目录不该判为可写")
	}
}

func TestAutoName(t *testing.T) {
	at := timeAt(t)
	cases := []struct{ mode, model, want string }{
		{"engine", "", "人机 · 第 4 档 · 09-24 10:30"},
		{"llm", "deepseek-chat", "大模型 · deepseek-chat · 09-24 10:30"},
		{"llm", "", "大模型 · 大模型 · 09-24 10:30"},
		{"local_2p", "", "双人 · 09-24 10:30"},
	}
	for _, c := range cases {
		if got := AutoName(c.mode, 4, c.model, at); got != c.want {
			t.Errorf("AutoName(%s,%q) = %q，期望 %q", c.mode, c.model, got, c.want)
		}
	}
}

// timeAt 固定一个本地时间，供命名断言用（AutoName 用本地时区格式化）。
func timeAt(t *testing.T) time.Time {
	t.Helper()
	return time.Date(2026, 9, 24, 10, 30, 0, 0, time.Local)
}

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
