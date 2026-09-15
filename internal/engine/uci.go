package engine

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

// executable 报告文件是否带可执行位。
//
// Windows 没有权限位这一说，只看是不是目录；Unix 上检查 mode 的 0111 位。
func executable(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if info.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	return info.Mode()&0o111 != 0
}

func (e *UCIEngine) start() error {
	// 只在确实缺可执行位时才尝试补位：本应用以专用用户（非 root）运行，
	// 进程内 chmod 属于 root 的文件会 EPERM —— 这正是过去「皮卡鱼可用: false」
	// 的结构性根源。引擎改 Go 内嵌后已不再随包分发，走到这里的通常是本机
	// 自带的外置引擎（属主是自己，chmod 能成功）。
	//
	// 补位失败也不提前返回：交给 os/exec 给出准确的错误信息，
	// 免得把一个本可运行的引擎判成不可用。
	if !executable(e.path) {
		_ = os.Chmod(e.path, 0o755)
	}

	// 工作目录设为引擎所在目录：皮卡鱼默认在 cwd 找 pikafish.nnue（EvalFile 相对路径），
	// fnOS 启动时 cwd=应用根而引擎在 app/server/engines/，不设 Dir 会导致引擎按相对路径
	// 找不到权重（部分版本缺权重直接退出，uciok 等不到 → 启动失败）。
	//
	// 必须先转成绝对路径：设了 cmd.Dir 之后 exec 会把相对路径解释为「相对于 cmd.Dir」，
	// 于是 "dist/pikafish.exe" 会跑去 dist/ 下再找一次 dist/pikafish.exe 而失败。
	exePath := e.path
	if abs, err := filepath.Abs(exePath); err == nil {
		exePath = abs
	}
	cmd := exec.Command(exePath)
	cmd.Dir = filepath2(exePath)
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

	// 权重固定用**相对路径**：工作目录已设为引擎所在目录（见 start），引擎按
	// 相对名就能找到。
	//
	// 不要传绝对路径 —— 引擎对含非 ASCII 字符的路径（中文用户名、中文项目目录
	// 都很常见）处理不了，会加载失败后直接退出，症状表现为「引擎输出流关闭」，
	// 从外部完全看不出是路径编码问题。
	if fileExists(filepath2(e.path) + "/pikafish.nnue") {
		e.send("setoption name EvalFile value pikafish.nnue")
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
	skill, movetime := uciLevelParams(level)
	e.mu.Lock()
	defer e.mu.Unlock()

	mv, err := e.bestOnce(ctx, pos, skill, movetime)
	if err != nil {
		_ = e.restart()
		mv, err = e.bestOnce(ctx, pos, skill, movetime)
	}
	return mv, err
}

// BestMoveTimed 用指定的思考时间与 Skill 等级求着法。
//
// 供跨引擎**等时公平对拍**使用：档位映射表两套引擎并不通用（皮卡鱼调的是
// Skill Level，内嵌引擎调的是深度与随机池），要比较棋力只能把思考时间固定成同一个量。
// skill 传 20 表示不削弱。
func (e *UCIEngine) BestMoveTimed(ctx context.Context, pos *game.Position, movetime time.Duration, skill int) (game.Move, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	mv, err := e.bestOnce(ctx, pos, skill, movetime)
	if err != nil {
		_ = e.restart()
		mv, err = e.bestOnce(ctx, pos, skill, movetime)
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

func (e *UCIEngine) bestOnce(ctx context.Context, pos *game.Position, skill int, movetime time.Duration) (game.Move, error) {
	e.send(fmt.Sprintf("setoption name Skill Level value %d", skill))
	e.send("isready")
	if err := e.expect("readyok", 5*time.Second); err != nil {
		return game.Move{}, err
	}

	e.send(positionCommand(pos))
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
	e.send(positionCommand(pos))
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

// positionCommand 构造发给引擎的 position 命令。
//
// **只发 FEN，绝不附加 moves 尾巴**：FEN 已经完整描述了当前局面（含走子方与
// 着数），再附上历史着法等于把整局重放一遍。那些着法在 FEN 局面上多半本身
// 就不合法（实测：FEN 已写 b（黑走），却还附带红方的 b2b5）。皮卡鱼遇到
// 不合法的 moves 会静默忽略、仍按 FEN 作答，所以这个 bug 不会稳定复现成
// 错误着法，但会偶发地让引擎答出非法着法（对局工具里表现为 "illegal:" 中断）。
func positionCommand(pos *game.Position) string {
	return "position fen " + pos.FEN()
}
