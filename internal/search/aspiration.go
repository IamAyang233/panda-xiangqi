package search

import (
	"os"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
)

// 期望窗口（aspiration window）：迭代加深时不再每层都用全窗 (-∞,+∞)，而是
// 假设上一层的分值附近就是本层的答案，用 [prev-δ, prev+δ] 这样的小窗口去搜。
//
// 收益来自 alpha-beta 的性质：窗口越窄，被剪掉的枝越多。代价是判断错了要重搜 ——
// 分值真落在窗口外时（fail low / fail high），必须放宽窗口重来一次。
// 只要「猜中」的收益大于「猜错重来」的代价，净收益就是正的；深度越大、前后层
// 分值相关性越高，猜中率越高。
//
// 皮卡鱼与 Stockfish 都有这一层（根节点的迭代加深外面套一个重试循环）。
// 本实现此前完全没有 —— 每层都是全窗。
//
// 实测（本机，55 个真实对局局面、固定深度 → 节点总数，完全可复现）：
//
//	δ        100     200     300     400
//	节点比   0.878   0.837   0.839   0.860
//
// 取 300：它与 200 的节点收益基本持平，但在战术题上更保守（200 那档命中掉 9 题，
// 300 与全窗打平）。δ 越大结果越回归全窗，δ 大到覆盖所有可达分值时逐位相同 ——
// 这条性质由 TestAspirationFaithfulWhenWide 锁定。
//
// 写成变量而非常量是为了让测试能改它：δ 需要被设到极大来验证「宽窗口下接线无损」，
// 也需要被设到极小来验证重试逻辑真的在跑。同包内可见，不对外暴露。
var (
	// aspirationDelta 是首次窗口的半宽（centipawn）。
	//
	// 本引擎需要 300 这么大的半宽，是**自身问题的症状**：实测相邻层的分值摆动
	// 中位数约 100、最大值 432（含符号交替的偶奇摆动），而 Stockfish 的
	// delta = 10 + prev²/16384 在 prev=300 时只有 15 —— 它的摆动小得多，所以能
	// 用窄得多的窗口。等哪天把摆动压下来，这个值应该跟着调小。
	aspirationDelta = 300

	// aspirationMinDepth 之下不收窄窗口。浅层分值差得远、摆动又大
	// （实测最大的几次摆动都出现在 depth 4 附近），窄窗口几乎必然失败，
	// 重搜的成本直接超过收益。
	aspirationMinDepth = 4
)

// aspirationOff 由环境变量 QIJING_ASP=off 置位，用于运维排查。
//
// 期望窗口会扰动搜索轨迹（重搜写入的置换表项会参与后续搜索），实测能让
// 个别局面在同一深度上的将杀发现**晚一层**（见 TestForwardPruningFidelity
// 的说明：那处将杀本来就依赖置换表在迭代加深中带出来，换个搜索轨迹就丢了）。
// 怀疑真机上出现「该看到的杀没看到」时，关掉它看现象是否消失，是最快的一刀。
var aspirationOff = os.Getenv("QIJING_ASP") == "off"

// aspirationMaxRetry 是放宽窗口重搜的次数上限。达到上限就退回全窗搜一次，
// 保证返回的分值一定可信（宁可多搜一次，也不返回一个被窗口截断的分值）。
const aspirationMaxRetry = 4

// searchRootAspiration 带期望窗口地在根节点搜索 depth 层，必要时放宽重搜。
//
// prevScore 是上一层（depth-1）得到的分值；hasPrev 为假（第一层）或分值已进
// 将杀区间时不收窄窗口 —— 将杀分按 ply 跳变（每层差 2），拿它做几百的窗口
// 必然反复失败，白白重搜。
func (s *Searcher) searchRootAspiration(p *game.Position, depth, prevScore int, hasPrev bool) ([]RootMove, bool) {
	if aspirationOff || s.noAsp || !hasPrev || depth < aspirationMinDepth ||
		prevScore > MateScore-MaxPly || prevScore < -(MateScore-MaxPly) {
		return s.rootSearch(p, depth, -Infinity, Infinity)
	}

	delta := aspirationDelta
	alpha, beta := prevScore-delta, prevScore+delta

	for attempt := 0; attempt < aspirationMaxRetry; attempt++ {
		roots, ok := s.rootSearch(p, depth, alpha, beta)
		if !ok || len(roots) == 0 || s.stopped() {
			// 无着法可走（将死/困毙）或已被中止：直接把结果交回调用方，
			// 它会在下一层前检查 s.stopped()（中止时不能把未完成的层当结果）。
			return roots, ok
		}
		score := roots[0].Score

		switch {
		case alpha > -Infinity && score <= alpha:
			// fail low：真实值在窗口下方。上界收到窗口中点（真实值必在其下），
			// 下界放到返回值之下。
			beta = (alpha + beta) / 2
			if v := score - delta; v > -Infinity {
				alpha = v
			} else {
				alpha = -Infinity
			}
		case beta < Infinity && score >= beta:
			// fail high：真实值在窗口上方，上界放到返回值之上。
			if v := score + delta; v < Infinity {
				beta = v
			} else {
				beta = Infinity
			}
		default:
			return roots, true // 落在窗口内，本层完成
		}

		if alpha >= beta { // 窗口退化，直接退回全窗
			alpha, beta = -Infinity, Infinity
		}
		// 放宽幅度递增：连续失败说明这个局面的分值摆动远大于 δ，
		// 缓慢加宽只会把重试次数用光。
		delta += delta/4 + 5
	}

	// 反复失败：退回全窗搜一次，保证返回的分值可信。
	return s.rootSearch(p, depth, -Infinity, Infinity)
}
