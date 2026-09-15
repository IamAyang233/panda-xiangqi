package search

import (
	"os"
	"strings"
	"testing"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
)

// loadQuietFENs 读 testdata/quiet_fens.txt —— 由「自对弈 + 安静性过滤」产生的
// 对局局面语料（生成方式见 memory；需要扩充时重跑生成器）。
//
// 为什么需要单独的语料：评测依赖「相邻层分值相关」的机制（期望窗口、LMR 调参、
// 剪枝余量）时，战术题库会给出完全误导的结论 —— 那里的分值常在某层突然跳到
// 将杀分（实测 fail high 的平均幅度达 6000~20000 centipawn 量级），于是任何
// 有效的窗口都必然反复失败。必须用真实对局局面才有判别力。
func loadQuietFENs(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile("testdata/quiet_fens.txt")
	if err != nil {
		t.Skipf("缺少安静局面语料（%v）", err)
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		t.Fatal("语料文件为空")
	}
	return out
}

// TestGenerateQuietCorpus 重新生成 testdata/quiet_fens.txt。
//
// 做法：从若干开局自对弈，每隔一步取一个局面，用独立的一次深度 8 搜索判断它
// 是否安静（分值在 ±800 内、未见将杀），合格就写入。默认跳过（要跑约 1 分钟），
// 需要时：
//
//	QIJING_GEN_CORPUS=1 go test ./internal/search/ -run TestGenerateQuietCorpus -v
//
// 用文件而不是 Go 字面量：语料会随调参需要不断扩充，写成源码字面量会让 diff
// 里全是 FEN。
func TestGenerateQuietCorpus(t *testing.T) {
	if testing.Short() {
		t.Skip("生成器，默认跳过")
	}
	if os.Getenv("QIJING_GEN_CORPUS") == "" {
		t.Skip("默认跳过：设 QIJING_GEN_CORPUS=1 才运行")
	}
	w := loadWeights(t)
	openings := []string{
		game.InitialFEN,
		"rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1",
		"1nbakabn1/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C2C4/9/RNBAKABNR w - - 0 1",
		"2bakab2/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/2N1C4/9/R1BAKABNR w - - 0 1",
		"1nbakab2/4a4/2n1c4/p1p1p1p1p/9/9/P1P1P1P1P/1C2C1N2/9/R1BAKAB1R w - - 0 1",
		"rnbakab1r/9/1c5c1/p1p1p1p1p/9/2P6/P3P1P1P/1C2C1N2/9/RNBAKAB1R w - - 0 1",
		"3ak4/4a4/4b4/p1p1p3p/9/2P6/P3P1P1P/1C2C4/9/3AKAB2 w - - 0 1",
		"r1bakab1r/9/1cn4c1/p1p1p1p1p/9/9/P1P1P1P1P/1CN2C3/9/R1BAKAB1R w - - 0 1",
	}

	const (
		moveDepth    = 6
		quietDepth   = 8
		quietBand    = 800
		pliesPerGame = 60
	)

	var kept []string
	seen := map[string]bool{}
	for gi, op := range openings {
		p, err := game.ParseFEN(op)
		if err != nil {
			t.Fatalf("第 %d 个开局解析失败：%v", gi+1, err)
		}
		s := New(w)
		for i := 0; i < pliesPerGame; i++ {
			if len(p.LegalMoves(p.Turn)) == 0 {
				break
			}
			res := s.Search(p, moveDepth)
			if i%2 == 1 && i >= 12 {
				rs := New(w).Search(p.Clone(), quietDepth)
				mate := rs.Score > MateScore-MaxPly || rs.Score < -(MateScore-MaxPly)
				if !mate && rs.Score < quietBand && rs.Score > -quietBand {
					// 归一化到固定回合，免得同一局面因步数不同被当成两个。
					fen := strings.Join(strings.Fields(p.FEN())[:4], " ") + " 0 1"
					if !seen[fen] {
						seen[fen] = true
						kept = append(kept, fen)
					}
				}
			}
			p.Make(res.Best)
		}
	}
	if len(kept) < 40 {
		t.Fatalf("只收集到 %d 个局面，样本太少；放宽过滤或增加开局数", len(kept))
	}
	if err := os.MkdirAll("testdata", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("testdata/quiet_fens.txt",
		[]byte(strings.Join(kept, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("已写入 testdata/quiet_fens.txt，共 %d 个局面", len(kept))
}
