package engine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
)

// Manager 引擎管理器：优先使用皮卡鱼（高棋力），低档位与缺失时用自研引擎兜底。
type Manager struct {
	mu     sync.RWMutex
	uci    *UCIEngine
	simple *SimpleEngine
	path   string // 皮卡鱼路径；空 = 未配置
}

// NewManager 探测皮卡鱼：优先 enginePath 参数，其次 PATH 中的 pikafish 与
// 可执行文件同目录 engines/ 下。
func NewManager(enginePath string) *Manager {
	m := &Manager{simple: NewSimpleEngine(), path: enginePath}
	for _, cand := range candidatePaths(enginePath) {
		if cand == "" {
			continue
		}
		if u, err := NewUCIEngine(cand); err == nil {
			m.uci = u
			m.path = cand
			break
		}
	}
	return m
}

func candidatePaths(cfgPath string) []string {
	list := []string{}
	if cfgPath != "" {
		list = append(list, cfgPath)
	}
	if p, err := exec.LookPath("pikafish"); err == nil {
		list = append(list, p)
	}
	if p, err := exec.LookPath("Pikafish"); err == nil {
		list = append(list, p)
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath2(exe)
		for _, name := range []string{"pikafish", "Pikafish", "pikafish.exe", "Pikafish.exe"} {
			list = append(list, dir+"/"+name)
			// 兼容随包内置：可执行文件同目录下的 engines/ 子目录（README 文档约定）
			list = append(list, dir+"/engines/"+name)
		}
	}
	return list
}

func filepath2(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			return p[:i]
		}
	}
	return "."
}

// EngineName 返回当前主力引擎名。
func (m *Manager) EngineName() string {
	if m.uci != nil {
		return m.uci.Name()
	}
	return m.simple.Name()
}

// HasUCI 是否有皮卡鱼可用。
func (m *Manager) HasUCI() bool { return m.uci != nil }

// BestMove 按档位调度引擎：1~4 档恒用自研（含随机性），5 档以上优先皮卡鱼。
func (m *Manager) BestMove(ctx context.Context, pos *game.Position, level int) (game.Move, error) {
	if level >= 1 && level <= 4 {
		return m.simple.BestMove(ctx, pos, level)
	}
	m.mu.RLock()
	uci := m.uci
	m.mu.RUnlock()
	if uci != nil {
		if mv, err := uci.BestMove(ctx, pos, level); err == nil {
			return mv, nil
		}
		// 皮卡鱼故障 → 降档到自研引擎高深度
		return m.simple.BestMove(ctx, pos, 12)
	}
	return m.simple.BestMove(ctx, pos, level)
}

// Hint 提示：中档位计算（T5.4）。
func (m *Manager) Hint(ctx context.Context, pos *game.Position) (game.Move, error) {
	mv, err := m.BestMove(ctx, pos, 10)
	if err != nil {
		return game.Move{}, fmt.Errorf("提示计算失败: %w", err)
	}
	return mv, nil
}

// RankedMoves 自研引擎排序的前 n 候选着法（LLM 引擎候选模式）。
func (m *Manager) RankedMoves(ctx context.Context, pos *game.Position, level, n int) []game.Move {
	return m.simple.RankedMoves(ctx, pos, level, n)
}

// Close 释放引擎资源。
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.uci != nil {
		m.uci.Close()
	}
}
