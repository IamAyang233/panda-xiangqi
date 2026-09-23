package main

// mustCustom / 启动期自定义残局目录的落点与降级（2026-09-23 补）。
//
// 这条链原来是空白：config.Load 的 TRIM_PKGVAR 归一有单测，但从「配置解析出的目录」
// 到「真的把目录建出来、把内容读回来」这一步没人验。而这正是线上最容易出错的地方
// ——飞牛上目录落在数据卷（/vol{n}/@appdata/...），路径写错时表现是「保存成功但
// 重启就没了」，用户看得见、日志里却未必醒目。

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/IamAyang233/panda-xiangqi/internal/config"
	"github.com/IamAyang233/panda-xiangqi/internal/puzzle"
)

// 合法的自摆残局：单车对单将，红先。
const testCustomFEN = "3k5/9/9/9/9/9/9/9/R8/4K4 w"

// TestMustCustomFnOSDataDir 飞牛上目录应落在 TRIM_PKGVAR 下，且真的建得出来、读得回来。
func TestMustCustomFnOSDataDir(t *testing.T) {
	pkgvar := t.TempDir()
	t.Setenv("TRIM_PKGVAR", pkgvar)
	// 显式配置必须为空，否则会盖掉框架默认值；这个变量在 clearEnv 里被清过，
	// 这里再清一次是因为本测试只跑 main 包、不受 config 包测试的清理影响。
	t.Setenv("QIJING_CUSTOM", "")

	cfg := config.Load(filepath.Join(t.TempDir(), "不存在.yaml"))
	want := filepath.Join(pkgvar, config.DefaultCustomDir)

	if cfg.CustomDir != want {
		t.Fatalf("配置解析出的目录 = %q，期望 %q", cfg.CustomDir, want)
	}

	st, dir := mustCustom(cfg.CustomDir)
	if dir != want {
		t.Fatalf("mustCustom 返回目录 = %q，期望 %q", dir, want)
	}
	if st == nil {
		t.Fatal("mustCustom 返回了 nil store")
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("目录应被建出来：err=%v", err)
	}
	if !puzzle.DirWritable(dir) {
		t.Fatal("刚建出来的目录应可写")
	}

	// 存一条、再用同一个目录重新载入 —— 这一步才真正代表「重启后还在」。
	p := &puzzle.Puzzle{
		ID: "custom-t-1", Name: "单车站", Source: "自摆",
		Difficulty: "自定义", PlayerSide: "red", Goal: "win", FEN: testCustomFEN,
	}
	if err := st.Add(p); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := puzzle.SaveToDir(dir, p); err != nil {
		t.Fatalf("SaveToDir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "custom-t-1.json")); err != nil {
		t.Fatalf("落盘文件应存在：%v", err)
	}

	re, dir2 := mustCustom(cfg.CustomDir)
	if dir2 != want {
		t.Fatalf("二次载入目录 = %q，期望 %q", dir2, want)
	}
	if got := re.List(""); len(got) != 1 {
		t.Fatalf("重新载入应读到 1 条，实际 %d 条", len(got))
	}
}

// TestMustCustomDegradesToMemory 目录建不出来时降级为内存态（dir 返回空串），不中断启动。
// 自摆残局是附加功能，不该因为它让整个服务起不来；但降级的事实必须如实向外报出。
func TestMustCustomDegradesToMemory(t *testing.T) {
	// 拿一个普通文件当父目录，垫在它下面建目录必然失败（ENOTDIR / 路径无效）。
	parent := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(parent, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(parent, "custom-puzzles")

	st, dir := mustCustom(bad)
	if dir != "" {
		t.Fatalf("建不出目录时应返回空串表示内存态，实际 %q", dir)
	}
	if st == nil {
		t.Fatal("降级仍应返回一个可用（内存）的 store，否则保存会 panic")
	}
	if _, err := os.Stat(bad); err == nil {
		t.Fatal("预期该目录不存在")
	}

	// 内存态下 Add 仍应成功（进程内可玩），只是不落盘。
	p := &puzzle.Puzzle{
		ID: "custom-t-2", Name: "内存局", Difficulty: "自定义",
		PlayerSide: "red", Goal: "win", FEN: testCustomFEN,
	}
	if err := st.Add(p); err != nil {
		t.Fatalf("内存态 Add 应成功：%v", err)
	}
	if got := st.List(""); len(got) != 1 {
		t.Fatalf("内存态应有 1 条，实际 %d 条", len(got))
	}
}
