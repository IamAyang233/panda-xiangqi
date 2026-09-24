// Package filestore 一目录一条 JSON 的落盘原语：原子写、幂等删、可写探测。
//
// 抽出来的原因：残局（internal/puzzle）与棋谱（internal/record）用的是同一套
// 「一目录一文件、文件名 = id + .json」的落盘约定。原先把这套写在 puzzle 里，
// 再抄一份到 record 就会有两份会各自漂移的实现 —— 而这里恰好有几个容易写错的点
// （临时文件清理、目录必须已存在、文件不存在不算错），值得只写一次。
package filestore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// safeID 只放行「普通文件名」：非空、不是 . / ..、不含路径分隔符与 ..
//
// 这是**安全边界**而不是洁癖：id 最终会进 filepath.Join，而 id 常常来自 URL 路径。
// Go 的 ServeMux 按 EscapedPath 做路径清理，`%2e%2e%2f` 不是 `..`、不会被清理，
// 解码后 id 里就带着 `../` —— 足以让 Join 逃出数据目录。调用方（api 层）另有更严的
// 白名单，这里再挡一次，避免将来别处传入用户可控的 id。
func safeID(id string) bool {
	if id == "" || id == "." || id == ".." {
		return false
	}
	if strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return false
	}
	return true
}

// SaveJSON 把 v 原子写入 dir 下的 <id>.json。
//
// 采用「临时文件 + rename」：断电或进程被杀时只可能留下临时文件，不会留下半截 JSON
// 让下次启动的加载器读到脏数据。rename 失败时清理临时文件（扩展名不匹配故不会被加载，
// 但仍应清掉，不为排查留垃圾）。
//
// 目录必须已存在：由启动流程创建，这里不静默 MkdirAll，避免把写权限问题藏起来。
func SaveJSON(dir, id string, v any) error {
	if dir == "" {
		return fmt.Errorf("目录为空")
	}
	if !safeID(id) {
		return fmt.Errorf("id 非法")
	}
	if st, err := os.Stat(dir); err != nil {
		return fmt.Errorf("目录不可用: %w", err)
	} else if !st.IsDir() {
		return fmt.Errorf("目录不是一个文件夹: %s", dir)
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化失败: %w", err)
	}
	data = append(data, '\n')
	tmp := filepath.Join(dir, id+".json.tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("写入失败: %w", err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, id+".json")); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("落盘失败: %w", err)
	}
	return nil
}

// RemoveJSON 删除 dir 下的 <id>.json。
// 文件已不存在（例如被手工删过）时**不算错误**：调用方仍可据此摘除内存索引。
func RemoveJSON(dir, id string) error {
	if dir == "" {
		return fmt.Errorf("目录为空")
	}
	if !safeID(id) {
		return fmt.Errorf("id 非法")
	}
	if err := os.Remove(filepath.Join(dir, id+".json")); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("删除失败: %w", err)
	}
	return nil
}

// DirWritable 目录是否可写（用于降级时如实向外报告，而不是静默变成"保存了其实没保存"）。
func DirWritable(dir string) bool {
	if dir == "" {
		return false
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return false
	}
	f, err := os.CreateTemp(dir, ".write-check-")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	_ = os.Remove(name)
	return true
}
