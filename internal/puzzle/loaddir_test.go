package puzzle

// 加载与落盘目录的边界行为（2026-09-23 白盒补测）。
//
// 这一组测试盯的是「一条坏数据会不会把整个残局库带下水」。自定义残局目录是
// 用户可直接触碰的目录（手改、同步、断电留半截文件都会撞上），而启动流程
// mustCustom 一旦 LoadDir 报错就整体降级为内存态 —— 那意味着用户所有自摆残局
// 在界面上一起消失，只剩日志里一行。所以「单条坏数据只跳过自己」是必须钉住的
// 契约，而不是可以商量的策略。

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 合法自摆局面：单车对单将，红先。
const loadTestFEN = "3k5/9/9/9/9/9/9/9/4R4/4K4 w"

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestLoadDirSkipsBadEntries 目录里混进坏条目时：坏的跳过、好的照常载入。
//
// 四种坏法都要单独覆盖，因为它们走的是不同分支：整块 JSON 坏掉、缺 id、
// FEN 语法错、局面非法（将帅照面）。前两种原先直接 return 错误，
// 于是整个目录算加载失败。
func TestLoadDirSkipsBadEntries(t *testing.T) {
	// 静音加载期的告警日志，避免测试输出被逐条 bad entry 刷屏。
	defer func(prev io.Writer) { log.SetOutput(prev) }(log.Writer())
	log.SetOutput(io.Discard)

	dir := t.TempDir()
	writeFile(t, dir, "good.json", `{"id":"custom-good","name":"好的","difficulty":"自定义","playerSide":"red","goal":"win","fen":"`+loadTestFEN+`"}`)
	writeFile(t, dir, "truncated.json", `{"id":"custom-bad","name":"半截`)                      // JSON 整体不可解析
	writeFile(t, dir, "no-id.json", `{"name":"没有 id","fen":"`+loadTestFEN+`"}`)               // 可解析但缺 id
	writeFile(t, dir, "bad-fen.json", `{"id":"custom-fen","fen":"3k5/9/9/9 乱写"}`)             // FEN 语法错
	writeFile(t, dir, "flying.json", `{"id":"custom-fly","fen":"4k4/9/9/9/9/9/9/9/9/4K4 w"}`) // 将帅照面，局面非法
	writeFile(t, dir, "notes.txt", `不是 json，扩展名也不匹配，应被忽略`)
	writeFile(t, dir, "half.json.tmp", `{"id":"custom-tmp"}`) // 原子写残留，扩展名不匹配

	st, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("一条坏数据不该让整个残局库加载失败：%v", err)
	}
	if got := st.Count(); got != 1 {
		var ids []string
		for _, p := range st.All() {
			ids = append(ids, p.ID)
		}
		t.Fatalf("应只载入 1 条（good），实际 %d 条：%v", got, ids)
	}
	if _, ok := st.Get("custom-good"); !ok {
		t.Fatal("好的那条应当可用")
	}
	for _, bad := range []string{"custom-bad", "custom-fly", "custom-fen"} {
		if _, ok := st.Get(bad); ok {
			t.Errorf("坏条目 %s 不该进库", bad)
		}
	}
}

// TestLoadDirParamAndDirErrors 参数与目录级错误仍要如实报错（不该被上面的宽容吞掉）。
func TestLoadDirParamAndDirErrors(t *testing.T) {
	if _, err := LoadDir(""); err == nil {
		t.Error("空目录应报错")
	}
	if _, err := LoadDir(filepath.Join(t.TempDir(), "并不存在")); err == nil {
		t.Error("不存在的目录应报错")
	}
	st, err := LoadDir(t.TempDir())
	if err != nil {
		t.Fatalf("空目录应能加载（0 条）：%v", err)
	}
	if st.Count() != 0 {
		t.Errorf("空目录应为 0 条，实际 %d", st.Count())
	}
}

// TestSaveToDirCleansTmpOnRenameFailure 原子写失败时不能留下 .tmp 垃圾。
//
// 构造方式：先把目标文件名占成一个**目录**，rename 就必然失败（Windows 与 Linux
// 都如此）。这正是「不为排查留垃圾」那条注释要守的情形，此前无测试。
func TestSaveToDirCleansTmpOnRenameFailure(t *testing.T) {
	dir := t.TempDir()
	p := &Puzzle{ID: "custom-rename", Name: "x", FEN: loadTestFEN, Difficulty: "自定义"}
	// 占位：与目标同名的目录
	if err := os.Mkdir(filepath.Join(dir, p.ID+".json"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := SaveToDir(dir, p); err == nil {
		t.Fatal("目标被目录占位时 rename 应失败")
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".json.tmp") {
			t.Fatalf("rename 失败后残留了临时文件：%s", e.Name())
		}
	}
}

// TestRemoveFromDirIdempotentAndParamErrors 删除：参数错要报错，文件不在不算错。
func TestRemoveFromDirIdempotentAndParamErrors(t *testing.T) {
	if err := RemoveFromDir("", "x"); err == nil {
		t.Error("空目录应报错")
	}
	dir := t.TempDir()
	if err := RemoveFromDir(dir, ""); err == nil {
		t.Error("空 id 应报错")
	}
	// 文件本来就不在：幂等，不报错
	if err := RemoveFromDir(dir, "custom-nope"); err != nil {
		t.Errorf("文件不存在不应报错（幂等）：%v", err)
	}
	// 真的删一次
	p := &Puzzle{ID: "custom-del", Name: "x", FEN: loadTestFEN, Difficulty: "自定义"}
	if err := SaveToDir(dir, p); err != nil {
		t.Fatal(err)
	}
	if err := RemoveFromDir(dir, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, p.ID+".json")); !os.IsNotExist(err) {
		t.Error("文件应已被删除")
	}
}

// TestEmbeddedCountPinned 内嵌题库条数钉死。
//
// 为什么需要：NewStore 现在对坏条目是「跳过并告警」而不是整体失败（见上），
// 这条策略换来的是「一条坏数据不至于让整库消失」，代价是坏文件会**静默少一条**。
// 这个计数断言就是那个代价的对冲：内嵌题库少一条就红，逼人去日志里找原因。
// 注意 3577 份数据里有 1 份是已知的非法局面（非行棋方被将军，2.0.3 起计入数据缺陷），
// 会被跳过，所以载入条数比文件里的条数少 1。新增残局时请同步改这个期望值。
const wantEmbeddedCount = 3576

func TestEmbeddedCountPinned(t *testing.T) {
	st, err := Embedded()
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Count(); got != wantEmbeddedCount {
		t.Fatalf("内嵌题库条数 = %d，期望 %d（若有数据坏掉会被跳过、条数变少，请查加载告警）",
			got, wantEmbeddedCount)
	}
}
