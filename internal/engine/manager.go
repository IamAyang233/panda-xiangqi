package engine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/search"
)

// Manager 引擎管理器。
//
// 调度优先级：**内嵌 Go 引擎**（internal/search，不需要外部文件、不需要执行位）
// → 皮卡鱼 UCI 子进程（内嵌引擎不可用时的强引擎兜底）
// → 自研简单引擎（最后兜底）。
//
// 内嵌引擎是默认主力：它彻底消除了「应用以非 root 运行、却要给引擎文件补
// 可执行位」这个结构性依赖。
//
// ⚠️ 这个优先级**与难度档位无关**：BestMove 不按档位挑引擎。低档位的「人味失误」
// 由内嵌引擎的档位参数实现（search.Level 的 MaxDepth 与 TopN/Slack 随机池），
// 而不是靠换成一个更弱的引擎。本文件与 uci.go 的注释曾写「低档走 SimpleEngine」，
// 与实现不符，2026-09-21 已订正。
type Manager struct {
	mu     sync.RWMutex
	native *NativeEngine
	uci    *UCIEngine
	simple *SimpleEngine
	path   string   // 皮卡鱼路径；空 = 未配置
	diag   []string // 探测诊断：每个候选的尝试结果（失败原因），供日志输出
}

// NewManager 只配置皮卡鱼路径（内嵌引擎的权重走自动探测）。
func NewManager(enginePath string) *Manager {
	return NewManagerWithNNUE(enginePath, "")
}

// NewManagerWithNNUE 同时配置皮卡鱼路径与内嵌引擎的 NNUE 权重路径。
//
// 两个路径都可以留空：皮卡鱼会自动探测（参数 → PATH → 可执行文件同目录/engines），
// 权重也会自动探测（参数 → 可执行文件同目录/models → engines → 当前目录）。
func NewManagerWithNNUE(enginePath, nnuePath string) *Manager {
	m := &Manager{
		simple: NewSimpleEngine(),
		native: NewNativeEngine(nnuePath, 0),
		path:   enginePath,
	}

	// 内嵌引擎：只需要一个只读的权重文件。
	if p := firstExisting(candidateWeightPaths(nnuePath)); p != "" {
		m.native.weightsPath = p
		m.diag = append(m.diag, "内嵌 Go 引擎: 权重 "+p)
	} else {
		// 清掉无效配置：留着会让 Available() 误报「可用」。
		m.native.weightsPath = ""
		m.diag = append(m.diag, "内嵌 Go 引擎: 未找到 NNUE 权重（pikafish.nnue.flat，"+
			"期望位于可执行文件同目录或 engines/ 子目录）")
	}

	// 皮卡鱼兜底：每个候选的失败原因都记进诊断，"启动不了"类问题凭启动日志即可定位。
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
		m.diag = append(m.diag, "未找到外置 UCI 引擎（可选、非必需，仅作兜底；如需使用，"+
			"放在可执行文件同目录或 engines/ 子目录）")
	}
	return m
}

// firstExisting 返回候选路径里第一个存在的文件；都不存在返回空串。
func firstExisting(paths []string) string {
	seen := map[string]bool{}
	for _, p := range paths {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		if fileExists(p) {
			return p
		}
	}
	return ""
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
	out = append(out, m.native.Diagnostics()...)
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
	if m.HasNative() {
		return m.native.Name()
	}
	if m.uci != nil {
		return m.uci.Name()
	}
	return m.simple.Name()
}

// HasNative 报告内嵌 Go 引擎是否可用（权重文件存在）。
//
// 权重是延迟加载的，所以这里只判断「文件在不在」——若文件损坏，
// 首次搜索会失败并自动降级到皮卡鱼或自研引擎。
func (m *Manager) HasNative() bool {
	return m.native != nil && m.native.Available()
}

// HasUCI 是否有皮卡鱼可用（内嵌引擎不可用时的强引擎兜底）。
func (m *Manager) HasUCI() bool { return m.uci != nil }

// HasStrong 是否有任一强引擎可用（内嵌 Go 引擎或皮卡鱼）。
func (m *Manager) HasStrong() bool { return m.HasNative() || m.HasUCI() }

// maxDiag 限制运行时追加的诊断条数：长会话里搜索失败可能反复出现，
// 不设上限会让诊断列表无限增长（它会被 /api/status 输出）。
const maxDiag = 64

// noteDiag 追加一条诊断（去重且不超上限）。
func (m *Manager) noteDiag(s string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range m.diag {
		if d == s {
			return
		}
	}
	if len(m.diag) < maxDiag {
		m.diag = append(m.diag, s)
	}
}

// BestMove 按档位调度引擎。
//
// 档位语义（1~16 由 search.Level 定义）在两个强引擎间通用：内嵌引擎直接接受
// 档位参数；皮卡鱼则把档位映射成 Skill Level 与思考时间。
func (m *Manager) BestMove(ctx context.Context, pos *game.Position, level int) (game.Move, error) {
	if err := ctxErr(ctx); err != nil {
		return game.Move{}, err
	}

	if m.HasNative() {
		mv, err := m.native.BestMove(ctx, pos, level)
		if err == nil {
			return mv, nil
		}
		if ctxErr(ctx) != nil {
			// 是取消而不是引擎故障，不该降级接着算。
			return game.Move{}, err
		}
		m.noteDiag("内嵌 Go 引擎搜索失败，降级: " + err.Error())
	}

	m.mu.RLock()
	uci := m.uci
	m.mu.RUnlock()
	if uci != nil {
		mv, err := uci.BestMove(ctx, pos, level)
		if err == nil {
			return mv, nil
		}
		if ctxErr(ctx) != nil {
			return game.Move{}, err
		}
		m.noteDiag("外置 UCI 引擎搜索失败，降级到自研引擎: " + err.Error())
	}
	if err := ctxErr(ctx); err != nil {
		return game.Move{}, err
	}
	return m.simple.BestMove(ctx, pos, level)
}

// BestMoveObserve 是 BestMove 带「迭代观察器」的版本。
//
// ⚠️ **观察器只有内嵌引擎支持**：皮卡鱼走 UCI 协议（它的逐层信息要解析 info 行），
// 自研简易引擎没有迭代加深。走这两条降级路径时 obs **不会被调用** ——
// 调用方只能把回调当增强信息，不能依赖「一定会收到」。
func (m *Manager) BestMoveObserve(ctx context.Context, pos *game.Position, level int, obs func(search.Result)) (game.Move, error) {
	if err := ctxErr(ctx); err != nil {
		return game.Move{}, err
	}
	if m.HasNative() && obs != nil {
		mv, err := m.native.BestMoveObserve(ctx, pos, level, obs)
		if err == nil {
			return mv, nil
		}
		if ctxErr(ctx) != nil {
			return game.Move{}, err
		}
		m.noteDiag("内嵌 Go 引擎搜索失败，降级: " + err.Error())
	}
	return m.BestMove(ctx, pos, level)
}

// Hint 提示：中档位计算（T5.4）。
func (m *Manager) Hint(ctx context.Context, pos *game.Position) (game.Move, error) {
	mv, err := m.BestMove(ctx, pos, 10)
	if err != nil {
		return game.Move{}, fmt.Errorf("提示计算失败: %w", err)
	}
	return mv, nil
}

// RankedMoves 返回引擎评估排序的前 n 个候选着法（LLM 引擎候选模式）。
//
// 内嵌引擎的根节点本来就会给每个着法打分，直接取排序结果即可；
// 它不可用时退回到自研引擎的简化排序。
func (m *Manager) RankedMoves(ctx context.Context, pos *game.Position, level, n int) []game.Move {
	if m.HasNative() {
		if out := m.native.RankedMoves(ctx, pos, level, n); len(out) > 0 {
			return out
		}
	}
	return m.simple.RankedMoves(ctx, pos, level, n)
}

// Close 释放引擎资源。
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.uci != nil {
		m.uci.Close()
		m.uci = nil
	}
	if m.native != nil {
		m.native.Close()
	}
}
