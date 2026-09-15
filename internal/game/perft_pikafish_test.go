package game

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// 与 C++ 皮卡鱼逐着对拍（M3 验收）：皮卡鱼是完全独立的实现，
// 其 `go perft` 既给总节点数也给逐着节点数，是走法生成最有力的交叉验证。
// 找不到可执行文件时跳过（不阻塞无引擎环境的测试）。

func findPikafish() string {
	cands := []string{
		"../../dist/pikafish.exe",
		"../../dist-engines/pikafish-avx2.exe",
		"../../engines/pikafish",
		"../../dist/pikafish",
	}
	for _, c := range cands {
		abs, err := filepath.Abs(c)
		if err != nil {
			continue
		}
		if fi, err := os.Stat(abs); err == nil && !fi.IsDir() {
			return abs
		}
	}
	return ""
}

// pikafishPerft 跑一次 `go perft`，返回总节点数与逐着节点数。
func pikafishPerft(t *testing.T, exe, fen string, depth int) (uint64, map[string]uint64) {
	t.Helper()
	cmd := exec.Command(exe)
	cmd.Dir = filepath.Dir(exe)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(stdin, "position fen %s\ngo perft %d\nquit\n", fen, depth)
	_ = stdin.Close()

	div := map[string]uint64{}
	var total uint64
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if rest, ok := strings.CutPrefix(line, "Nodes searched:"); ok {
			total, _ = strconv.ParseUint(strings.TrimSpace(rest), 10, 64)
			continue
		}
		if i := strings.Index(line, ": "); i == 4 {
			mv := line[:i]
			if n, err := strconv.ParseUint(strings.TrimSpace(line[i+2:]), 10, 64); err == nil {
				div[mv] = n
			}
		}
	}
	_ = cmd.Wait()
	if total == 0 {
		t.Fatalf("皮卡鱼未返回 perft 结果（depth=%d）", depth)
	}
	return total, div
}

// hasKingCapture 当前轮走方是否可直接吃对方将帅（极端局面才会出现）。
func hasKingCapture(p *Position) bool {
	for _, m := range p.LegalMoves(p.Turn) {
		if TypeOf(p.PieceAt(int(m.To))) == King {
			return true
		}
	}
	return false
}

func TestPerftAgainstPikafish(t *testing.T) {
	exe := findPikafish()
	if exe == "" {
		t.Skip("未找到皮卡鱼可执行文件，跳过对拍")
	}
	t.Logf("对拍引擎: %s", exe)

	fens := []string{
		InitialFEN,
		"3k5/9/9/9/9/9/9/9/9/4K4 w - - 0 1",
		"r1ba1a3/4kn3/2n1b4/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1",
		"4k4/9/9/9/4P4/9/9/9/9/3KC4 w - - 0 1",
		"3k5/9/5Pn2/9/9/9/9/9/9/4K4 b - - 0 1",
		"4k4/4a4/4b4/9/9/9/9/4B4/4A4/4K4 w - - 0 1",
	}
	for _, fen := range fens {
		p, err := ParseFEN(fen)
		if err != nil {
			t.Fatalf("解析失败 %s: %v", fen, err)
		}
		// 存在"直接吃将"着法的局面：皮卡鱼在吃将线上的 perft 计数与其
		// 对无将局面的独立 perft 结果自相矛盾（父局面走 e0e9 得 3，而
		// 直接给无将局面跑 perft 得 0），属其边界行为；此时跳过吃将线
		// 与该局面总量的比较，其余着法仍严格对拍。
		kingCap := hasKingCapture(p)
		for depth := 1; depth <= 4; depth++ {
			want, wantDiv := pikafishPerft(t, exe, fen, depth)
			if !kingCap {
				if got := Perft(p, depth); got != want {
					t.Errorf("perft 不一致\nFEN: %s\ndepth=%d\n皮卡鱼=%d\n本实现=%d", fen, depth, want, got)
				}
			}
			for mv, cnt := range PerftDiv(p, depth) {
				if TypeOf(p.PieceAt(int(mv.To))) == King {
					continue // 吃将线：见上方说明
				}
				if wantDiv[mv.String()] != cnt {
					t.Errorf("逐着不一致\nFEN: %s\ndepth=%d\n着法=%s\n皮卡鱼=%d\n本实现=%d",
						fen, depth, mv, wantDiv[mv.String()], cnt)
				}
			}
		}
	}
}
