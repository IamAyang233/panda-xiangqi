package nnue

import (
	"math/rand"
	"testing"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
)

// 本文件是「Unmake 复用 Make 的威胁条目」（threat replay）的正确性关卡。
//
// 这条改造把 Unmake 的威胁枚举换成了「搬 Make 时留的副本 + 翻转 add」，依据是
// 两次走子的 updateThreats 调用**逐段配对**：同一段的 occupied 完全相同、
// 只有 put 相反，而条目内容只取决于 (occupied, s, pc)。论证细节见
// Position 里 replayBuf 那段注释。
//
// 危险点在于：这条论证一旦不成立（比如某个分支改了调用顺序、或某段被算进了
// 另一类走法），累加器会**静默偏移** —— 不 panic、不越界，搜索只会慢慢选错着法。
// 所以必须逐局面逐检查点把两条路径对上，并且断言回放真的被走到过。

// TestThreatReplayMatchesEnumeration 对拍 Unmake 的两条路径：
// 复用 Make 的副本（翻转）与重新枚举，必须产出逐位相同的累加器。
//
// ⚠️ 断言 UtReplayed > 0 与 TestFuseRowsMatchesSingle 里断言配对数同一个理由：
// 两条路径产出逐位相同时，对拍测试会「因为没测到而通过」，哪怕回放一次都没生效。
func TestThreatReplayMatchesEnumeration(t *testing.T) {
	w, err := Load(flatPath)
	if err != nil {
		t.Skip("未找到展开后的权重，跳过")
	}

	fens := []string{
		game.InitialFEN,
		"3k5/9/9/9/9/9/9/9/9/4K4 w - - 0 1",
		"rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR b - - 0 1",
		"3k5/2P1P4/4b4/9/9/9/9/4p1p2/2p1p4/3K1C3 w - - 0 1",
		"2bak4/9/4c4/9/9/9/9/4C4/9/3AK4 w - - 0 1",
	}

	EnableDiag(true)
	defer EnableDiag(false)

	// walk 用固定种子走一遍（前进→Apply→后退→Apply，正是触发回滚段的那种序列），
	// 返回每个检查点上的累加器指纹、回放次数、以及「段不足」次数。
	walk := func(replay bool) ([]uint64, int64, int64) {
		old := useThreatReplay
		useThreatReplay = replay
		defer func() { useThreatReplay = old }()

		ResetDiag()
		var dig []uint64

		for _, fen := range fens {
			p, err := game.ParseFEN(fen)
			if err != nil {
				t.Fatalf("解析 %s 失败: %v", fen, err)
			}
			rng := rand.New(rand.NewSource(int64(len(fen)) * 7919))

			var pos Position
			pos.ResetFromGame(&p.Board, p.Turn>>3)
			var acc Accumulator

			record := func() {
				var h uint64 = 0xcbf29ce484222325
				mix := func(v int16) { h = (h ^ uint64(uint16(v))) * 0x100000001b3 }
				for c := 0; c < colorNB; c++ {
					for i := 0; i < L1; i++ {
						mix(acc.PsqAcc[c][i])
						mix(acc.ThrAcc[c][i])
					}
					for k := 0; k < PSQTBuckets; k++ {
						mix(int16(acc.PsqPsqt[c][k]))
						mix(int16(acc.ThrPsqt[c][k]))
					}
				}
				dig = append(dig, h)
			}

			w.Apply(&pos, &acc)
			record()

			depth := 0
			for round := 0; round < 40; round++ {
				adv := 1 + rng.Intn(3)
				for i := 0; i < adv; i++ {
					moves := p.LegalMoves(p.Turn)
					if len(moves) == 0 {
						break
					}
					m := moves[rng.Intn(len(moves))]
					p.Make(m)
					pos.Make(int(m.From), int(m.To))
					depth++
				}
				if depth > 0 {
					w.Apply(&pos, &acc)
					record()
				}
				for back := rng.Intn(3); back > 0 && depth > 0; back-- {
					p.Unmake()
					pos.Unmake()
					depth--
					w.Apply(&pos, &acc)
					record()
				}
			}
		}
		s := DiagSnapshot()
		return dig, s.UtReplayed, s.UtReplayShort
	}

	digEnum, _, _ := walk(false)
	digReplay, replays, shorts := walk(true)

	if len(digEnum) != len(digReplay) {
		t.Fatalf("两遍的检查点数不同：枚举 %d，回放 %d", len(digEnum), len(digReplay))
	}
	for i := range digEnum {
		if digEnum[i] != digReplay[i] {
			t.Fatalf("第 %d 个检查点：回放与枚举的累加器不同（枚举 %#x，回放 %#x）—— "+
				"说明回放搬来的条目集合与枚举不一致", i, digEnum[i], digReplay[i])
		}
	}
	if replays == 0 {
		t.Fatal("回放路径一次都没被走到 —— 这个测试没有测到目标路径")
	}
	// ⚠️ 段不足时那条路径会**静默少算条目**（不报错、不越界，只是评估值偏移）。
	// 它必须恒为 0；一旦非 0，说明 Make/Unmake 的 updateThreats 调用次数不再逐段
	// 配对，回放的整个前提已经失效 —— 那种情况下上面「逐位相同」的结论也不可信。
	if shorts != 0 {
		t.Fatalf("有 %d 次回放因「段已用尽」被跳过 —— 逐段配对的前提被破坏，"+
			"回放会静默少算条目", shorts)
	}
	t.Logf("集成层：%d 个检查点，回放 %d 次、段不足 0 次，逐位一致 ✓", len(digEnum), replays)
}
