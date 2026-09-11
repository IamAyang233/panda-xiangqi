package engine

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
)

// UCIEngine 皮卡鱼（Pikafish）适配器（A10）：子进程 + UCI 协议，单读 goroutine 分发行输出。
// 档位映射（计划 4.2）：
//
//	1~4  → Skill 1,    movetime 300ms（基本不用：Manager 在低档直接走 SimpleEngine）
//	5~8  → Skill 1~8,  movetime 400~850ms
//	9~13 → Skill 8~12, movetime 1~1.8s
//	14~16 → Skill 20,  movetime 1~3s
type UCIEngine struct {
	name     string
	path     string
	mu       sync.Mutex
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	lines    chan string
	errLines chan string
	dead     chan struct{}
	lastID   int
	diag     []string // 启动/运行期诊断（stderr 摘录）
}

// NewUCIEngine 启动子进程并完成 uci / isready 握手。
func NewUCIEngine(path string) (*UCIEngine, error) {
	e := &UCIEngine{
		name:     filepathBase(path),
		path:     path,
		lines:    make(chan string, 256),
		errLines: make(chan string, 32),
	}
	if err := e.start(); err != nil {
		return nil, err
	}
	return e, nil
}

// Diagnostics 返回引擎启动/运行期诊断信息（stderr 摘录等），供上层日志输出。
func (e *UCIEngine) Diagnostics() []string { return e.diag }

func filepathBase(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}

// fileExists 判断文件是否存在（用于探测随包内置的 NNUE 权重）。
func fileExists(p string) bool {
	if _, err := os.Stat(p); err != nil {
		return false
	}
	return true
}

func (e *UCIEngine) start() error {
	// fnpack 等打包器不会保留可执行位（打包为 0o666），必须在拉起子进程前补回 +x，
	// 否则 os/exec 在 Start 时会因权限不足直接失败（握手逻辑根本走不到）。
	if err := os.Chmod(e.path, 0o755); err != nil {
		return fmt.Errorf("设置引擎可执行权限失败 %s: %w", e.path, err)
	}

	// 工作目录设为引擎所在目录：皮卡鱼默认在 cwd 找 pikafish.nnue（EvalFile 相对路径），
	// fnOS 启动时 cwd=应用根而引擎在 app/server/engines/，不设 Dir 会导致引擎按相对路径
	// 找不到权重（部分版本缺权重直接退出，uciok 等不到 → 启动失败）。
	cmd := exec.Command(e.path)
	cmd.Dir = filepath2(e.path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		// 细分常见失败原因，便于用户自助排查（Windows 杀软拦截/架构不符/路径问题）
		return fmt.Errorf("启动引擎失败 %s: %w（若反复出现：Windows 请检查杀毒软件是否拦截未签名 exe；"+
			"Linux 请确认架构匹配、挂载点未含 noexec）", e.path, err)
	}
	e.cmd, e.stdin = cmd, stdin
	e.dead = make(chan struct{})

	// 引擎 stderr 单独收进诊断通道（皮卡鱼把加载失败原因写在 stderr）
	if stderr != nil {
		go func() {
			sc := bufio.NewScanner(stderr)
			for sc.Scan() {
				select {
				case e.errLines <- sc.Text():
				case <-e.dead:
					return
				}
			}
		}()
	}

	// 单读 goroutine：把引擎全部输出泵入 lines 通道
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 1<<16), 1<<20)
		for sc.Scan() {
			select {
			case e.lines <- sc.Text():
			case <-e.dead:
				return
			}
		}
		close(e.lines)
	}()

	e.send("uci")
	// uciok：纯协议握手不加载权重，10s 覆盖最慢设备的进程冷启动
	if err := e.expect("uciok", 10*time.Second); err != nil {
		e.kill()
		return fmt.Errorf("%w（引擎 stderr: %s）", err, e.lastStderr())
	}

	// 指向随包内置的 NNUE 权重文件，避免引擎因找不到 pikafish.nnue 而退出。
	// 该文件与引擎二进制同目录（engines/pikafish.nnue）。
	if nnue := filepath2(e.path) + "/pikafish.nnue"; fileExists(nnue) {
		e.send("setoption name EvalFile value " + nnue)
	} else {
		// cwd 已是引擎目录，皮卡鱼会按相对路径 pikafish.nnue 自动找到；都没有则告警
		e.reportStderr()
	}
	e.send("isready")
	// readyok：此刻引擎加载 53MB NNUE 权重，慢盘（机械/NFS/加密卷）可能明显超过 5s，
	// 原 5s 超时会误杀健康引擎 —— 放宽到 60s。
	if err := e.expect("readyok", 60*time.Second); err != nil {
		e.kill()
		return fmt.Errorf("%w（引擎 stderr: %s）", err, e.lastStderr())
	}
	return nil
}

// reportStderr 把引擎 stderr 里的最近内容记入诊断（缺权重/指令集不兼容等线索）。
func (e *UCIEngine) reportStderr() {
	if s := e.lastStderr(); s != "" {
		e.diag = append(e.diag, "引擎输出: "+s)
	}
}

// lastStderr 返回 stderr 缓冲的最近一行（多行拼接，限长）。
func (e *UCIEngine) lastStderr() string {
	select {
	case s := <-e.errLines:
		e.diag = append(e.diag, s)
		if len(e.diag) > 5 {
			e.diag = e.diag[len(e.diag)-5:]
		}
		return s
	default:
		if len(e.diag) > 0 {
			return e.diag[len(e.diag)-1]
		}
		return ""
	}
}

func (e *UCIEngine) send(line string) {
	_, _ = io.WriteString(e.stdin, line+"\n")
}

func (e *UCIEngine) kill() {
	if e.cmd != nil && e.cmd.Process != nil {
		_ = e.cmd.Process.Kill()
	}
	if e.dead != nil {
		select {
		case <-e.dead:
		default:
			close(e.dead)
		}
	}
}

// expect 逐行消费引擎输出直到出现 token 前缀；非匹配行（info 等）直接丢弃。
func (e *UCIEngine) expect(token string, timeout time.Duration) error {
	for {
		select {
		case line, ok := <-e.lines:
			if !ok {
				return fmt.Errorf("引擎输出流已关闭")
			}
			if strings.HasPrefix(strings.TrimSpace(line), token) {
				return nil
			}
		case <-time.After(timeout):
			return fmt.Errorf("等待 %s 超时", token)
		}
	}
}

func (e *UCIEngine) Name() string { return e.name }

// BestMove 通过 UCI 协议求着法；崩溃/超时自动重启并重试一次。
func (e *UCIEngine) BestMove(ctx context.Context, pos *game.Position, level int) (game.Move, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	mv, err := e.bestOnce(ctx, pos, level)
	if err != nil {
		_ = e.restart()
		mv, err = e.bestOnce(ctx, pos, level)
	}
	return mv, err
}

// Close 终止子进程。
func (e *UCIEngine) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.send("quit")
	time.Sleep(50 * time.Millisecond)
	e.kill()
}

func (e *UCIEngine) restart() error {
	e.kill()
	e.lines = make(chan string, 256)
	time.Sleep(100 * time.Millisecond)
	return e.start()
}

func (e *UCIEngine) bestOnce(ctx context.Context, pos *game.Position, level int) (game.Move, error) {
	skill, movetime := uciLevelParams(level)
	e.send(fmt.Sprintf("setoption name Skill Level value %d", skill))
	e.send("isready")
	if err := e.expect("readyok", 5*time.Second); err != nil {
		return game.Move{}, err
	}

	if moves := uciMoves(pos); moves != "" {
		e.send("position fen " + pos.FEN() + " moves " + moves)
	} else {
		e.send("position fen " + pos.FEN())
	}
	e.send(fmt.Sprintf("go movetime %d", movetime.Milliseconds()))

	type res struct {
		m   game.Move
		err error
	}
	var r res
	done := make(chan res, 1)
	go func() {
		for {
			select {
			case line, ok := <-e.lines:
				if !ok {
					done <- res{game.Move{}, fmt.Errorf("引擎输出流关闭")}
					return
				}
				fields := strings.Fields(strings.TrimSpace(line))
				if len(fields) >= 2 && fields[0] == "bestmove" {
					if m, ok := game.MoveFromUCI(fields[1]); ok {
						done <- res{m, nil}
					} else {
						done <- res{game.Move{}, fmt.Errorf("bestmove 解析失败: %s", line)}
					}
					return
				}
			case <-e.dead:
				done <- res{game.Move{}, fmt.Errorf("引擎已终止")}
				return
			}
		}
	}()
	timeout := movetime*3/2 + 2*time.Second
	select {
	case r = <-done:
	case <-time.After(timeout):
		e.send("stop")
		// stop 后引擎仍会输出 bestmove，等待其到达以免污染下一次请求
		_ = e.expect("bestmove", 2*time.Second)
		return game.Move{}, fmt.Errorf("等待 bestmove 超时")
	case <-ctx.Done():
		e.send("stop")
		_ = e.expect("bestmove", 2*time.Second)
		return game.Move{}, ctx.Err()
	}
	return r.m, r.err
}

// BestLine 通过 UCI 协议求最佳路线（PV）。以最高棋力（Skill Level 20）搜索
// movetimeMs 毫秒，返回从当前局面出发的主变着法序列、分数（cp）与将杀步数
// （mate，正 = 行棋方胜）。用于残局导入时生成"完整正解线"。
func (e *UCIEngine) BestLine(ctx context.Context, pos *game.Position, movetimeMs int) (pv []game.Move, scoreCp int, mateIn int, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.send("setoption name Skill Level value 20")
	e.send("isready")
	if err = e.expect("readyok", 5*time.Second); err != nil {
		return
	}
	if moves := uciMoves(pos); moves != "" {
		e.send("position fen " + pos.FEN() + " moves " + moves)
	} else {
		e.send("position fen " + pos.FEN())
	}
	e.send(fmt.Sprintf("go movetime %d", movetimeMs))

	type res struct {
		pv      []game.Move
		scoreCp int
		mateIn  int
		err     error
	}
	done := make(chan res, 1)
	go func() {
		var lastPV []game.Move
		var lastCp, lastMate int
		seen := false
		for {
			select {
			case line, ok := <-e.lines:
				if !ok {
					if seen {
						done <- res{lastPV, lastCp, lastMate, nil}
					} else {
						done <- res{nil, 0, 0, fmt.Errorf("引擎输出流关闭")}
					}
					return
				}
				fields := strings.Fields(strings.TrimSpace(line))
				if len(fields) >= 2 && fields[0] == "bestmove" {
					if seen {
						done <- res{lastPV, lastCp, lastMate, nil}
					} else {
						done <- res{nil, 0, 0, fmt.Errorf("未收到 PV")}
					}
					return
				}
				if len(fields) >= 2 && fields[0] == "info" {
					for i := 0; i+2 < len(fields); i++ {
						if fields[i] == "score" {
							switch fields[i+1] {
							case "cp":
								lastCp, _ = strconv.Atoi(fields[i+2])
							case "mate":
								lastMate, _ = strconv.Atoi(fields[i+2])
							}
							break
						}
					}
					for i := 0; i < len(fields); i++ {
						if fields[i] == "pv" {
							seq := fields[i+1:]
							line2 := make([]game.Move, 0, len(seq))
							for _, s := range seq {
								if m, ok := game.MoveFromUCI(s); ok {
									line2 = append(line2, m)
								} else {
									break
								}
							}
							if len(line2) > 0 {
								lastPV = line2
								seen = true
							}
							break
						}
					}
				}
			case <-e.dead:
				done <- res{nil, 0, 0, fmt.Errorf("引擎已终止")}
				return
			}
		}
	}()
	timeout := time.Duration(movetimeMs)*3/2 + 3*time.Second
	select {
	case r := <-done:
		return r.pv, r.scoreCp, r.mateIn, r.err
	case <-ctx.Done():
		e.send("stop")
		_ = e.expect("bestmove", 2*time.Second)
		return nil, 0, 0, ctx.Err()
	case <-time.After(timeout):
		e.send("stop")
		_ = e.expect("bestmove", 2*time.Second)
		return nil, 0, 0, fmt.Errorf("等待 bestmove 超时")
	}
}

func uciLevelParams(level int) (skill int, movetime time.Duration) {
	switch {
	case level <= 4:
		return 1, 300 * time.Millisecond
	case level <= 8:
		return level - 4, time.Duration(400+(level-5)*150) * time.Millisecond
	case level <= 13:
		return level - 1, time.Duration(1000+(level-9)*200) * time.Millisecond
	default:
		return 20, time.Duration(1000+(level-14)*1000) * time.Millisecond
	}
}

// uciMoves 从局面历史栈还原 UCI 着法序列。
func uciMoves(pos *game.Position) string {
	parts := make([]string, 0, pos.MoveCount())
	for _, m := range pos.HistoryMoves() {
		parts = append(parts, m.String())
	}
	return strings.Join(parts, " ")
}
