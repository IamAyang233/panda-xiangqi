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
	path   string   // 皮卡鱼路径；空 = 未配置
	diag   []string // 探测诊断：每个候选引擎的尝试结果（失败原因），供日志输出
}

// NewManager 探测皮卡鱼：优先 enginePath 参数，其次 PATH 中的 pikafish 与
// 可执行文件同目录 engines/ 下。每个候选的失败原因记录进 Diagnostics，
// "启动不了"类问题凭启动日志即可定位（文件缺失/权限/架构不符/杀软拦截/权重缺失）。
func NewManager(enginePath string) *Manager {
	m := &Manager{simple: NewSimpleEngine(), path: enginePath}
	seen := map[string]bool{}
	for _, cand := range candidatePaths(enginePath) {
		if cand == "" || seen[cand] {
			continue
		}
		seen[cand] = true
		if !fileExists(cand) {
			// 不存在的候选（LookPath 命中的除外）静默跳过，避免日志噪音
			continue
		}
		u, err := NewUCIEngine(cand)
		if err != nil {
			m.diag = append(m.diag, cand+" → "+err.Error())
			continue
		}
		m.uci = u
		m.path = cand
		m.diag = append(m.diag, cand+" → 启动成功")
		break
	}
	if m.uci == nil && len(m.diag) == 0 {
		m.diag = append(m.diag, "未找到皮卡鱼可执行文件（期望位于可执行文件同目录或 engines/ 子目录）")
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
		// 顺序即优先级：标准名 → 低指令集兜底名（pikafish-sse41 等，供老 CPU 设备；
		// avx2 专用版在这些设备上会因 Illegal instruction 直接崩溃）。逐个尝试直到握手成功。
		for _, name := range []string{
			"pikafish", "Pikafish", "pikafish.exe", "Pikafish.exe",
			"pikafish-sse41", "pikafish-sse41.exe",
			"pikafish-noavx", "pikafish-noavx.exe",
			"pikafish-legacy", "pikafish-legacy.exe",
		} {
			list = append(list, dir+"/"+name)
			// 兼容随包内置：可执行文件同目录下的 engines/ 子目录（README 文档约定）
			list = append(list, dir+"/engines/"+name)
		}
	}
	return list
}

// Diagnostics 返回引擎探测/运行诊断（每行一条），供启动日志与 /api/status 输出。
func (m *Manager) Diagnostics() []string {
	out := append([]string{}, m.diag...)
	m.mu.RLock()
	u := m.uci
	m.mu.RUnlock()
	if u != nil {
		out = append(out, u.Diagnostics()...)
	}
	return out
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
