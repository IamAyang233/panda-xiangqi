//go:build windows

package main

import (
	"log"
	"syscall"
	"unsafe"
)

// 用内核命名 Mutex 做单实例互斥：进程消亡由 OS 自动释放，不会残留文件或句柄。
var (
	procCreateMutex    *syscall.LazyProc
	singleInstanceMutex uintptr
)

func init() {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	procCreateMutex = kernel32.NewProc("CreateMutexW")
}

func releaseSingleInstance() {
	if singleInstanceMutex != 0 {
		syscall.CloseHandle(syscall.Handle(singleInstanceMutex))
		singleInstanceMutex = 0
	}
}

// acquireSingleInstance 已被占用返回 false，调用方只开浏览器并退出。
func acquireSingleInstance() bool {
	name, _ := syscall.UTF16PtrFromString("Global\\panda-xiangqi-instance")
	h, _, err := procCreateMutex.Call(0, 0, uintptr(unsafe.Pointer(name)))
	if h == 0 {
		log.Printf("创建单实例锁失败: %v", err)
		return true // 保守：锁失败仍继续，避免用户启动不了
	}
	singleInstanceMutex = h
	if errno, ok := err.(syscall.Errno); ok && errno == syscall.ERROR_ALREADY_EXISTS {
		return false
	}
	return true
}