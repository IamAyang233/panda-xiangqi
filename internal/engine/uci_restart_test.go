package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
)

// 本文件是 UCI 引擎「重启路径」的回归哨兵（2026-09-21 审查第六轮发现）。
//
// 缺陷：读泵 goroutine 闭包**每轮重新读 `e.lines` / `e.dead` 字段**，而 restart()
// 会换掉这两个 channel。于是旧进程被杀、旧泵随之 EOF 时，它执行的是
// `close(e.lines)` —— 关掉的是**新**通道（旧通道已无人引用）。实测崩在
// `panic: close of closed channel`：新泵稍后也会 EOF 并再 close 一次。
// 本进程没有 recover 兜底 ⇒ 整个服务进程直接崩掉。
//
// 构造：现场编译一个极小的假引擎（UCI 握手正常、收到 `go` 后**活着但不予回应**），
// 逼出 BestMove → 超时失败 → restart() → 再次搜索 的路径。
//
// ⚠️「收到 go 就退出」的假引擎**测不出这个缺陷**（第一版就是那样写的，白跑）：
// 那种假引擎会在 restart 之前就 EOF，旧泵先 close 掉旧通道，而那时 e.lines 还没被
// 换掉，于是行为恰好正确。必须让假引擎在搜索期间保持存活，缺陷才可达。
//
// ⚠️ 判别力实测过：把 uci.go 里的 `lines := e.lines` 去掉、改回 `close(e.lines)`，
// 本测试以 `panic: close of closed channel` 崩掉（进程直接死，不是断言失败）。

const fakeUCISrc = `package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"
)

func main() {
	in := bufio.NewScanner(os.Stdin)
	out := bufio.NewWriter(os.Stdout)
	say := func(s string) { fmt.Fprintln(out, s); out.Flush() }
	for in.Scan() {
		switch line := strings.TrimSpace(in.Text()); {
		case line == "uci":
			say("id name FakeUCI")
			say("uciok")
		case line == "isready":
			say("readyok")
		case line == "quit":
			return
		case strings.HasPrefix(line, "go"):
			// ⚠️ 关键：**不给出 bestmove，也不退出**，而是继续活着读输入。
			//
			// 这样调用方会一路走到超时（movetime*3/2+2s）才返回错误、进而 restart()。
			// 此时旧进程**仍然活着** ⇒ restart 里的 kill() 才真正触发「旧泵正卡在
			// stdout 读取上、而 e.lines 已被换成新通道」这个窗口 —— 这正是缺陷
			// 会 panic 的时序。若这里改成「收到 go 就退出」（第一版就是这么写的），
			// 旧泵早在 restart 之前就已经 EOF 并 close 掉旧通道，缺陷根本走不到。
			time.Sleep(30 * time.Second)
		}
	}
}
`

// buildFakeUCI 现场编译假引擎，返回可执行文件路径。
func buildFakeUCI(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte(fakeUCISrc), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "fakeuci")
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", out, src)
	cmd.Env = os.Environ()
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("无法编译假引擎（%v）：%s", err, b)
	}
	return out
}

// TestUCIRestartDoesNotPanic 多轮逼出 restart 路径，验证进程不会崩。
func TestUCIRestartDoesNotPanic(t *testing.T) {
	if testing.Short() {
		t.Skip("-short 下跳过（要编译并起子进程做多轮重启）")
	}
	e, err := NewUCIEngine(buildFakeUCI(t))
	if err != nil {
		t.Skipf("假引擎握手失败，跳过：%v", err)
	}
	defer e.Close()

	p, err := game.ParseFEN("rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// 每轮都会：bestOnce 超时失败 → restart() 杀旧进程并换新通道 → 再 bestOnce 超时。
	// 修复前，旧泵在「e.lines 已换成新通道之后」才因进程被杀而 EOF，
	// 于是 close 掉新通道，新泵首次输出即 panic。
	//
	// 用短 movetime 压掉等待成本（超时 = movetime*3/2 + 2s，固定开销 2s 为主）。
	for i := 0; i < 2; i++ {
		if _, err := e.BestMoveTimed(ctx, p, 50*time.Millisecond, 1); err == nil {
			t.Fatalf("第 %d 轮：假引擎不可能给出着法，却返回了成功", i)
		}
	}
	// 走到这里 = 多轮「失败→重启」没有打崩进程。
}
