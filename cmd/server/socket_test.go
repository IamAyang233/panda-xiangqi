package main

// cleanStaleSocket 的守卫（2026-09-21 审查第六轮）。
//
// 这是「破坏性操作 + 前置条件」的一类：删除不可逆，而路径来自配置文件。
// 原来的写法是 `if stat 成功 { os.Remove(path) }` —— SocketPath 写错成普通文件
// 时会把那个文件删掉。

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCleanStaleSocketRefusesRegularFile 普通文件必须被拒绝且**原样保留**。
func TestCleanStaleSocketRefusesRegularFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "not-a-socket")
	const content = "重要数据"
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := cleanStaleSocket(p); err == nil {
		t.Error("普通文件应返回错误（否则会被静默删除）")
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("普通文件被删掉了！%v", err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != content {
		t.Errorf("文件内容被改动: %q", b)
	}
}

// TestCleanStaleSocketIgnoresMissing 路径不存在时应无操作（正常情况）。
func TestCleanStaleSocketIgnoresMissing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "not-there.sock")
	if err := cleanStaleSocket(p); err != nil {
		t.Errorf("路径不存在时应无操作，实际 %v", err)
	}
}
