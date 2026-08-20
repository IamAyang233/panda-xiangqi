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
	name   string
	path   string
	mu     sync.Mutex
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	lines  chan string
	dead   chan struct{}
	lastID int
}

// NewUCIEngine 启动子进程并完成 uci / isready 握手。
func NewUCIEngine(path string) (*UCIEngine, error) {
	e := &UCIEngine{name: filepathBase(path), path: path, lines: make(chan string, 256)}
	if err := e.start(); err != nil {
		return nil, err
	}
	return e, nil
}

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
	_ = os.Chmod(e.path, 0o755)

	cmd := exec.Command(e.path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动引擎失败 %s: %w", e.path, err)
	}
	e.cmd, e.stdin = cmd, stdin
	e.dead = make(chan struct{})

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
	if err := e.expect("uciok", 5*time.Second); err != nil {
		e.kill()
		return err
	}

	// 指向随包内置的 NNUE 权重文件，避免引擎因找不到 pikafish.nnue 而退出。
	// 该文件与引擎二进制同目录（engines/pikafish.nnue）。
	if nnue := filepath2(e.path) + "/pikafish.nnue"; fileExists(nnue) {
		e.send("setoption name EvalFile value " + nnue)
	}
	e.send("isready")
	if err := e.expect("readyok", 5*time.Second); err != nil {
		e.kill()
		return err
	}
	return nil
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
