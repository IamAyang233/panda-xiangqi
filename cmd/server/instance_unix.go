//go:build !windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func lockPath() string {
	return filepath.Join(os.TempDir(), "panda-xiangqi.lock")
}

func releaseSingleInstance() {
	_ = os.Remove(lockPath())
}

// acquireSingleInstance 已被占用返回 false，调用方只开浏览器并退出。
// 用 O_EXCL 原子创建锁文件；持有 PID 已死的残留锁自动抢占。
func acquireSingleInstance() bool {
	lock := lockPath()
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err == nil {
		fmt.Fprintln(f, os.Getpid())
		f.Close()
		return true
	}
	if b, rErr := os.ReadFile(lock); rErr == nil {
		var pid int
		if n, _ := fmt.Sscanf(string(b), "%d", &pid); n == 1 && pid > 0 {
			if pr, pErr := os.FindProcess(pid); pErr == nil {
				if rErr = pr.Signal(syscall.Signal(0)); rErr == nil {
					return false // 持有进程仍存活
				}
			}
		}
	}
	_ = os.Remove(lock)
	f, err = os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err == nil {
		fmt.Fprintln(f, os.Getpid())
		f.Close()
		return true
	}
	// ⚠️ 这里**故意 fail-open**（返回 true = 当作拿到锁继续启动）：走到这里说明
	// 「删锁或重建锁都失败了」，比如 /tmp 只读、并发竞争。单实例只是体验优化
	// （防重复开端口/标签页），而「因为锁的问题让用户启动不了」是更糟的失败模式。
	// Windows 侧 CreateMutex 失败时也是同一取舍（见 instance_windows.go）。
	return true
}
