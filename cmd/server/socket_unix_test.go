//go:build !windows

package main

// 真 socket 的清理用例：Windows 上造不出 Unix Socket，故单独放这里。

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

// TestCleanStaleSocketRemovesRealSocket 残留 socket（上个实例异常退出的产物）
// 必须被清掉，否则 bind 失败、应用起不来。
func TestCleanStaleSocketRemovesRealSocket(t *testing.T) {
	p := filepath.Join(t.TempDir(), "panda.sock")
	ln, err := net.Listen("unix", p)
	if err != nil {
		t.Skipf("本环境不支持 Unix Socket：%v", err)
	}
	ln.Close() // 关掉监听但不删文件，模拟「残留 socket」

	if _, err := os.Stat(p); err != nil {
		t.Fatalf("socket 文件应仍存在（作为残留）: %v", err)
	}
	if err := cleanStaleSocket(p); err != nil {
		t.Fatalf("残留 socket 应被清理，实际 %v", err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("残留 socket 未被删除，err=%v", err)
	}
}
