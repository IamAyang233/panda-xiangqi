// Package search 实现 alpha-beta 搜索。
//
// 依赖规则层（internal/game）与评估层（internal/nnue）。
// 结构：迭代加深 → alpha-beta（PVS）→ 静态搜索（quiescence），
// 外层可选 Lazy SMP 多线程（parallel.go）与时间预算（timemgr.go）。
//
// 评估用增量累加器（nnue.SyncTo），实测比全量重建快约 4 倍；
// 档位到搜索参数的映射见 level.go。
package search

import (
	"math"
	"os"
	"strconv"
	"sync/atomic"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
	"github.com/IamAyang233/panda-xiangqi/internal/nnue"
)

// seeEnabled 由环境变量 QIJING_SEE=on 启用静态搜索里的坏吃子剪枝。
//
// **默认关闭。** 正确性已经锁定：与朴素递归 SEE 对拍 1779 个吃子着法零分歧
// （见 internal/game/see_ref_test.go），搜索里也表现为「减少节点且结果不变」。
// 但没有可观测的棋力收益 —— 400 道残局、固定深度 9、逐题配对（TestSeeTacticsAB）：
//
//	SEE 关  267/400 = 66.8%    合计节点 1,735,649
//	SEE 开  264/400 = 66.0%    合计节点 1,702,721（98.1%）
//
// 只在 7/400 的题上改变决策（仅 SEE 对 2、仅非 SEE 对 5，样本太少不显著），
// 换来 1.9% 的节点节省。所以默认不开；它留下的价值是**可用的 SEE 基础设施**
// ——静默着法剪枝与 ProbCut 都要用它。
//
// 它同时是运维开关：怀疑搜索有异常时可以关掉它看现象是否消失。
// 关掉只会少剪（变慢），不会剪掉本该保留的吃子。
var seeEnabled = os.Getenv("QIJING_SEE") == "on"

// seeQuietEnabled 由环境变量 QIJING_SEEQ=on 启用**静的着法**（非吃子）的 SEE 剪枝。
//
// 与上面那个「静态搜索里的坏吃子剪枝」是两回事：那个剪的是 quiesce 里的吃子，
// 这个剪的是主搜索里的安静着法。阈值照抄皮卡鱼 `see_ge(move, -35*lmrDepth²)`。
//
// 默认关闭，等实测确认有收益再开 —— 上一轮的教训是「SEE 基础设施可用」不等于
// 「某个具体用法有收益」，必须逐个量。
var seeQuietEnabled = os.Getenv("QIJING_SEEQ") == "on"

// probCutEnabled 由环境变量 QIJING_PC=on 启用 ProbCut。
//
// 它是 SEE 的第三个用法 —— 用 `see_ge(move, probCutBeta - staticEval)` 筛掉
// 白丢子的吃子。前两个用法（静态搜索里的坏吃子剪枝、静的着法剪枝）实测都没有
// 可靠收益，但那是「剪掉整个着法」；ProbCut 是「先花一次浅搜验证，再决定是否
// 按这个下界返回」，SEE 在这里省掉那些注定验证不通过的浅搜 —— **实测只有这个
// 用法让 SEE 有了正向贡献**（去掉筛选后从 0.992× 变 1.039×）。
//
// 默认关闭 —— ProbCut 本身在本引擎上是净亏损，理由见 alphaBeta 里的注释。
var probCutEnabled = os.Getenv("QIJING_PC") == "on"

// seEnabled 由环境变量 QIJING_SE=on 启用奇异延伸（singular extension）。
//
// 语义：如果除 ttMove 之外的所有着法都明显更差（拿掉 ttMove 后同一局面搜不出
// ttValue 那个水平），说明 ttMove 是「唯一好手」，它的对手很难应对 —— 于是把它
// 多搜深一层（皮卡鱼最新版最多 3 层）。反过来，如果拿掉 ttMove 之后仍然 fail
// high，说明有多个着法都够好，这个结点必然被截断，可以直接剪掉整棵子树
// （多切剪枝 multi-cut）。
//
// 这是本项目此前**完全没有**的一类机制：既有剪枝都是「减少搜索」，延伸是
// 「主动多花」；而防守型剪枝会漏杀，延伸不会（它只加深）。皮卡鱼的 extension
// 只来自 singular + 负延伸，没有将军延伸。
//
// ⚠️ **实测净亏，默认关闭。** 移植是完整的（触发条件、验证搜索、多切剪枝、
// double/triple 余量、−3 负延伸、cutNode 传递、ttPv 位都按皮卡源码对上），
// 守卫也证明排除管线真的生效（见 singular_test.go，还原 bug 必失败）。
// 全部数据为确定性测量（单线程 + 每题独立置换表）：
//
//	固定 10 万节点·150 道残局题   命中 105 → 92（≤1 层延伸）/ 89（≤3 层）
//	                              均深 25.74 → 23.99 / 19.70
//	固定深度 9·同一批题           命中  98 → 89，节点 684,677 → 9,753,818（14.2×）
//	固定 depth 10·20 个安静局面   节点 258,469 → 510,217(1.97×) / 1,589,734(6.15×)
//	                                        / 3,340,797(12.9×)，按 QIJING_SE_MAXEXT 1/2/3
//	固定 6 万节点·20 个安静局面   均深 14.70 → 12.35 / 10.60 / 9.70
//
// **「等深度命中率」那行是判死刑的一条**：同样的名义深度下命中反而少 9 题，
// 说明延伸不只是「多花了预算」，而是**主动把选择带偏了** —— 它把 ttMove 的
// 子树搜得比同层其它着法深得多，一旦 ttMove 是错的那一步，这个偏置就变成伤害。
//
// 机制确实在触发（15,734 次触发 / 12,168 次延伸，占节点约 0.6~1%），
// 验证搜索返回值也正常（只有 1.2% 落在杀分区间），所以这不是接线错误。
// 规模上不去、且代价是 2~14 倍节点，故默认关闭。
var seEnabled = os.Getenv("QIJING_SE") == "on"

// seMaxExt 限制单次奇异延伸的最大层数（皮卡鱼不设上限，可到 3 层）。
//
// 留作实验旋钮而不写死：实测延伸幅度是**代价的主因**，1/2/3 层的代价分别是
// 1.97× / 6.15× / 12.9× 节点。将来若要重新评估，先用它把幅度压到 1 再谈。
var seMaxExt = envIntOr("QIJING_SE_MAXEXT", 3)

func envIntOr(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// probCutSmallEnabled 由环境变量 QIJING_PC_SMALL=on 启用「廉价的 ProbCut 变体」
// （皮卡鱼 Step 11）：置换表里已有「下界、足够深、高出 beta 470」的证据时，
// 直接按该下界返回 —— 不额外搜索，所以是纯粹的免费捷径。
// 实测同样净亏损，默认关闭（理由见 alphaBeta 里的注释）。
var probCutSmallEnabled = os.Getenv("QIJING_PC_SMALL") == "on"

// lmrBound 由环境变量 QIJING_LMRBOUND=on 启用**有界重搜**（皮卡鱼 Step 16 的模型）。
//
// 背景：我们的削减搜索一旦「比 alpha 略好」就**直接跳回全深重搜**，于是削减必须
// 保守 —— 代码里那条实测记录（线性 1.79 / 对数 1.85 / max 2.44）说的就是这个
// 代价：削得越狠，越多着法在浅层通过、触发越多全深重搜，多出来的节点超过省下的。
//
// 皮卡鱼的模型不同：削减搜索之后**只在新深度上下浮动一层**
//
//	doDeeperSearch    = d < newDepth && value > bestValue + 60
//	doShallowerSearch = value < bestValue + 9
//	newDepth += doDeeperSearch - doShallowerSearch
//	if newDepth > d { 重搜 }        // 从不跳回全深
//
// 所以它敢把后期着法削 4~5 层。实测（2026-09-18，同局面固定深度迭代加深、
// 「走子次数」口径）：固定名义深度 d12 我们要 39.3 万次走子、皮卡鱼只要 2.0 万
// （19.6×），每层树规模倍增率我们 2.32 vs 它 1.75 —— 差异的根源就在这里。
//
// 默认关闭：本开关只改「削减之后怎么重搜」，削减量本身仍用既有公式，
// 这样能单独量出重搜策略的贡献（单变量）。
//
// ⚠️ **实测（2026-09-18）：单变量版本方向不一致，不能直接采纳。**
//
//	固定 6 万节点·20 个安静局面：平均深度 14.70 → **14.90**（略好）
//	中局（子力互亏）固定深度 d12：结点 170k → 414k（**+143%**）、走子 +180%、耗时 +164%
//	初始局面固定深度 d12：结点 120k → 78k（−35%）、走子 −23%、耗时 −25%（明显更好）
//	search 包全套测试：基线 109s → **跑 18m37s 仍未结束**（那些固定节点/深度的场景树规模爆炸）
//
// 方向取决于局面 ⇒ 说明它不是「免费的效率」，而是换了一种树形。
// 原因大概率在于：我们的 `red` 是整数层、且后期着法可削到 depth−2，
// 一旦在深度 1 上失败高，`nd = newDepth±1` 相对 `d` 仍是**接近全深**的一跳，
// 于是既没省下重搜、又让「削减搜索本身」变得不可信 —— 皮卡鱼能把两者配对，
// 靠的是它整套 1/1024 缩放的 r 公式 + Step 17 的追加削减 + 修正历史项一起作用。
// ⇒ 真要移植，必须整块做（公式 + 重搜深度 + Step 17 + 需要的辅助结构），
//
//	并把「等走子量」的质量判据先建起来。
var lmrBound = os.Getenv("QIJING_LMRBOUND") == "on"

// lmr2 启用**整块移植**皮卡鱼 Step 16/17 的削减结构：
// 1/1024 缩放量 + 与它同形的 r 公式 + 有界重搜 + 追加削减项。
//
// ⚠️ 为什么必须整块换而不是单件搬：2026-09-18 只搬「有界重搜」这一件（`lmrBound`），
// 实测**方向不一致** —— 初始局面 d12 走子 −23%、中局（子力互缠）d12 +180%、
// search 套件 109s → >18min 未跑完。根因是我们的 `red` 是**整数层**、后期着法最多
// 削到 depth−2，削减量本身没有随「深度 × 序号」平滑加大；换成有界重搜之后，
// 浅层试探的通过率暴涨而有界重搜收不回来。皮卡能把两者配对，靠的是它的 r 是
// `reductions[d]*reductions[mn]` 这种「两个对数相乘」的平滑量。
//
// ⚠️ **有意省略的两项**（照抄系数只会得到随机偏置）：
//   - `r -= statScore * 946 / 8192`：皮卡的 statScore 是「主历史 + 两层延续历史」的
//     合成分；本项目没有延续历史，唯一的主历史表上限是 `historyMax = 1<<18`，
//     量纲差几十倍。
//   - `r -= abs(correctionValue) / 30558`：本项目没有修正历史。
//   - `(ss+1)->cutoffCnt`：本项目不统计「下一层已发生的截断次数」。
//
// ⚠️⚠️ **实测（2026-09-18）：净亏，默认关闭。** 8 个局面、固定深度：
//
//	d6  +44.2% ｜ d8 +48.1% ｜ d10 +18.8% ｜ d12 +27.9%（合计节点）
//	d12 逐局面：初始 −0.1% ｜ 开局 +36.4% ｜ 中局(子力互缠) +67.0%
//	            残局(车兵) −48.2% ｜ 残局(炮兵) +4.6%
//
// 两条被量出来的机制：
//
//  1. ⚠️ **必须保留「只削安静着法」的门**（下面的 `if lmr2 && red > 0`）。皮卡鱼 Step 16
//     对吃子/将军一视同仁，但它 Step 13 先有「吃子 futility + 吃子/将军 SEE 剪枝」
//     把坏吃子清掉。本项目那两处只作用于安静着法 ⇒ 去掉门会让坏吃子被削到 1 层，
//     频繁 fail high 触发重搜：同一个中局局面 d12 节点 **35,308 → 806,245（22.8×）**。
//  2. ⚠️ 即便带门，**r 的形状本身在我们这儿也不划算**：`reductions[d]*reductions[mn]`
//     是两个对数相乘，在 `moveCount` 小（2~10）时比我们的 `1+index/8` 削得**更深**，
//     而在高位反而更浅；实测削减搜索的平均 `newDepth` 只有 1.23、平均只少搜 0.6 层
//     —— 也就是说这套公式的收益主要落在「已经很浅」的地方，而重搜链的成本照付。
//
// ⇒ **结论：LMR 块不能单独移植。** 它至少还依赖 Step 13 的吃子剪枝（下一条待做），
//
//	以及本项目有意省略的历史/修正历史项。要再试，先把吃子剪枝补上再整块量。
var lmr2 = os.Getenv("QIJING_LMR2") == "on"

// capPrune 启用皮卡鱼 Step 13 里**吃子/将军**那一半剪枝（吃子 futility +
// 吃子的 SEE 剪枝）。默认关闭，因为要单独量它的收益。
//
// 为什么值得单独立项：我们原来的前向剪枝（futility / LMP / SEE）**只作用于安静
// 着法**，而皮卡鱼 Step 13 对吃子和将军同样剪 —— 这正是它能把 Step 16（LMR）
// 对吃子一视同仁地削减的前提（详见 `lmr2` 的注释：去掉「只削安静着法」的门会
// 让坏吃子被削到 1 层、节点 +22.8×）。也就是说：**这两处剪枝是 LMR 整块移植
// 的前置条件**，而它本身也是我们剪枝链里缺的一块。
//
// ⚠️⚠️ **实测（2026-09-18）：净亏，默认关闭；而且两半各自都亏。**
//
//	8 个局面固定深度合计（基线 → 只开 futility → 连吃子 SEE 一起开）：
//	  d6  −7.7% → −7.7%
//	  d8  +6.4% → +5.4%
//	  d10 +4.0% → +0.5%
//	  d12 **+22.4%** → +28.0%
//
// 逐局面看，两个版本各自只炸**一个**局面、而且不是同一个：
//
//	吃子 futility → 中局（子力互缠）170,483 → **263,483（+55%）**，其余逐位不变
//	吃子 SEE     → 初始局面      120,833 → **170,435（+41%）**，其余逐位不变
//
// ⇒ 两个解释（都是「这套剪枝需要配套」的同一件事）：
//
//	① 我们的搜索对「静态评估 + 余量」的依赖比皮卡鱼弱：被剪掉的吃子里有不少
//	   其实是**能给出 beta 截断的那一步**，剪掉它之后后面的着法反而全都要搜。
//	② SEE「每个吃子都算一遍」的成本在本项目早已实测大于收益（见 `seeQuietEnabled`
//	   处的记录），而这里比静的那一处更贵（吃子在排序里靠前，先被问到）。
//
// ⚠️ 另：`TestForwardPruningFidelity` 在这个改动上**先抓到一个越界 panic** ——
// `PieceAt90` 返回的是带颜色位的编码（0..15），必须过 `game.TypeOf` 才能当
// `capPieceValue` 的下标。**剪枝类改动第一件事就是跑保真度测试。**
var capPrune = os.Getenv("QIJING_CAPP") == "on"

// lmrTable[i] = int(17.4 * ln(i))，与皮卡鱼初始化循环里的
// `reductions[i] = int(1740 / 100.0 * std::log(i))`（src/search.cpp:683）逐位同形。
var lmrTable = func() [MaxPly]int {
	var t [MaxPly]int
	for i := 1; i < len(t); i++ {
		t[i] = int(17.4 * math.Log(float64(i)))
	}
	return t
}()

// lmrRated 返回削减量，**单位是 1/1024 层**（皮卡鱼 src/search.cpp::reduction）。
//
//	reductionScale = reductions[d] * reductions[mn]
//	r = reductionScale - delta*1138/rootDelta + !improving*reductionScale*166/512 + 1934
//
// delta 是本节点的窗口宽度、rootDelta 是根窗口宽度：全窗 PV 节点几乎不额外削减，
// 零窗非 PV 节点（delta=1）拿到完整的削减量。
func lmrRated(improving bool, depth, moveCount, delta, rootDelta int) int {
	d, mn := depth, moveCount
	if d < 1 {
		d = 1
	}
	if d >= MaxPly {
		d = MaxPly - 1
	}
	if mn < 1 {
		mn = 1
	}
	if mn >= MaxPly {
		mn = MaxPly - 1
	}
	if rootDelta < 1 {
		rootDelta = 1
	}
	scale := lmrTable[d] * lmrTable[mn]
	r := scale - delta*1138/rootDelta + 1934
	if !improving {
		r += scale * 166 / 512
	}
	return r
}

// 分值常量。单位与 C++ 的 Value 一致。
const (
	// Infinity 大于任何真实评估值，用于 alpha-beta 的初始窗口。
	Infinity = 1 << 20
	// MateScore 表示将杀基准分，实际分值加上 ply 以偏好更快的杀。
	MateScore = 1 << 19
	// MaxPly 是搜索的最大层数，防止非法局面导致栈溢出。
	MaxPly = 128

	// maxQuiescePly 限制静态搜索的最大延伸层数，避免无限将军链。
	maxQuiescePly = 24

	// historyMax 是历史启发的上限，必须显著小于吃子排序的基准分。
	historyMax = 1 << 18
)

// pieceValue 是着法排序用的子力价值，索引为 game 的棋子类型编码。
var pieceValue = [8]int{0, 10000, 200, 200, 400, 900, 450, 100}

// capPieceValue 是**评估尺度**的子力价值（索引同为 game 的棋子类型编码）。
//
// ⚠️ 它与 `pieceValue`（排序尺度）是两套数：车在那里是 900、这里是 1305。
// 剪枝余量用的是评估值（`staticEval`），所以必须用这一套 —— 两边的评估输出
// 已经对拍过（同一局面我们 4094 vs 皮卡鱼内部值 4094.6），量纲是一致的。
//
// 取自皮卡鱼 src/types.h：Rook 1305、Advisor 219、Cannon 773、Pawn 144、
// Knight 720、Bishop 187；King 在它的表里是 0。
var capPieceValue = [8]int{0, 0, 219, 187, 720, 1305, 773, 144}

// deltaMargin 是静态搜索里 delta pruning 的缓冲值。
//
// 判据是：即便白吃到目标格里最值钱的子，「静态评估 + 该子价值 + 缓冲」
// 仍追不上 alpha，才判定这一支毫无希望并剪掉。
// 留出缓冲是为了吸收评估误差，免得误砍那些先弃后取会盈利的吃子链。
const deltaMargin = 200

// futilityDepth 返回允许做静态空着剪枝的最大深度，对应皮卡鱼的 futility_depth()。
//
// |eval| + |beta| 越大 —— 也就是越接近将杀分值 —— 返回值越小，剪枝越保守。
// 查表值与阈值都照搬皮卡鱼（其注释明确写着「这个深度条件对发现将杀至关重要，
// 不应自行调参」）。这里不是可调参数，是保护杀棋查找的一环：
// 用固定深度上限（早期版本是 depth<=6）会把深处的将杀直接剪没，
// 实测出现过同一着法 4194 与 524283 的分歧。
func futilityDepth(eval, beta int) int {
	// 末项取一个足够大的值，保证 prob 再大也能终止。
	lut := [...]int{1657, 2555, 3294, 4122, 5314, 8194, 1 << 30}
	prob := absInt(eval) + absInt(beta)
	d := 0
	for d < len(lut)-1 && lut[d] < prob {
		d++
	}
	return 15 - d
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// clampInt 把 v 限制在 [lo, hi]。皮卡鱼的削减公式里有 `std::clamp(alpha-eval,-64,96)`。
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// diagMoveOrder 为真时额外统计「TT 着法是否出现在当前局面的合法着法列表中」。
// 该检查是 O(着法数) 的线性扫描，只在诊断时开启，生产路径保持零额外开销。
var diagMoveOrder = false

// Searcher 持有权重与可复用的缓冲，非并发安全 ——
// 多线程时每个线程一个实例，共享同一个 TranspositionTable 与 stop 标志
// （见 parallel.go）。
type Searcher struct {
	w   *nnue.Weights
	acc nnue.Accumulator

	// pos 是评估侧的增量局面，与 game 局面用同一步走法同步驱动：
	// 这样特征枚举与累加器更新都不需要每次扫全盘。
	pos nnue.Position
	tt  *TranspositionTable

	killers [MaxPly][2]game.Move
	history [90][90]int
	nonPawn [2]int // 双方非兵子力数，evaluate 时顺带统计

	// staticEvalHist 按 ply 保存静态评估，用来判断 improving
	// ——「当前评估是否优于两步前」。
	//
	// 深度优先搜索保证：处理 ply 这个节点时，ply-2 槽位仍属于它的祖父节点
	// （祖父的整棵子树还没搜完），所以读 ply-2 拿到的正是两步前那个节点的值。
	staticEvalHist [MaxPly]int

	scoreBuf [MaxPly][]int

	nodes int64
	// makes 是**走子次数**，也就是「走一步算一个」的口径。
	//
	// ⚠️ 这不是 nodes 的另一种写法，两者比值随深度变化（本项目实测 d12 时
	// nodes:makes ≈ 1:2.3，d1 时反而 >1）。**它存在的唯一理由是跨引擎比对**：
	// 皮卡鱼的 `++nodes` 记在 `do_move` 里（src/search.cpp:628），即走子次数；
	// 我们记的是结点进入次数。拿这两个数直接相比，等于把我们的工作量报少了
	// 一倍多 —— 这正是 2026-09-18 之前所有「等节点」跨引擎结论失真的原因。
	makes  int64
	ttHits int64
	// rootDelta 是根节点的搜索窗口宽度（皮卡鱼：`rootDelta = beta - alpha`）。
	// onIter 若非 nil，会在**每次完整迭代完成后**被调用一次。
	//
	// 用途是「思考信息流」：UI 想实时显示深度/分值/节点数，就必须拿到每一次
	// 迭代加深的结果，而不是等搜索结束。默认 nil ⇒ 生产路径零开销（只是一次
	// 接口判空）。
	//
	// ⚠️ 只在**主线程**（p.searchers[0]）上生效：辅助线程跑的是同一深度的另一棵
	// 树，它们的结果不代表本线程选定的着法，混进来会让 UI 显示跳变。
	onIter func(Result)
	// 只被 LMR 的缩放公式用来归一化「当前节点的窗口相对根窗口有多宽」——
	// 全窗 PV 节点的 delta 大、零窗非 PV 节点的 delta 就是 1。
	rootDelta int
	nullMoves int64
	noNull    bool
	noForward bool // 关闭前向剪枝（reverse futility / futility / LMP），供对照实验
	noSEE     bool // 关闭静态搜索里的坏吃子剪枝，供对照实验
	noAsp     bool // 关闭期望窗口（每层都用全窗），供对照实验
	noProbCut bool // 关闭 ProbCut 与「廉价 ProbCut 变体」，供对照实验
	noPCSee   bool // 让 ProbCut 考虑全部吃子而不做 SEE 筛选，供对照实验

	// 排序质量统计。走法排序的作用是让「最好的着法尽早被搜到」，
	// 因为 alpha-beta 的第一个着法能定下 alpha，越早出现高分着法，
	// 后续着法越容易被剪掉。所以衡量它的直接指标是：
	// beta 截断发生在第几个着法上 —— 序号越大说明排序越差。
	cutoffs      int64 // 发生 beta 截断的总次数
	cutoffFirst  int64 // 其中在第一个着法上就截断的次数
	cutoffIdxSum int64 // 截断序号之和，除 cutoffs 得平均序号
	ttMoveAvail  int64 // probe 给出非零着法的次数
	ttMoveSorted int64 // 其中真的走到排序的（未被空着剪枝等提前 return 打断）
	ttMoveFirst  int64 // 其中排序后确实排在首位的次数
	ttMoveIlleg  int64 // 其中该着法压根不在当前局面的合法着法列表里（仅诊断开关开启时统计）

	// probeCnt 是置换表命中率唯一正确的分母：probe 只发生在 depth>0 的节点
	// （它在 depth<=0 转静态搜索的分支之后），而静态搜索节点占了 nodes 的一半，
	// 用总节点数当分母会把命中率稀释成 1/8，极易误判成「置换表失效」。
	probeCnt  int64
	abCallCnt int64
	storeCnt  int64

	// 奇异延伸的诊断计数。延伸类改动的效果分散在整棵树上，光看节点数与
	// 深度说不出「机制到底有没有生效」，这几个计数是唯一的直接证据。
	seTriggers int64 // 触发条件全部成立、进入验证搜索的次数
	seExtended int64 // 验证通过（ttMove 确为奇异着法）并延伸的次数
	seMultiCut int64 // 验证后直接按下界返回（多切剪枝）的次数
	seNegExt   int64 // 走负延伸（削减 3 层）的次数

	// excludedMove 是「奇异延伸」验证搜索要排除的着法。
	//
	// 验证搜索的语义是「如果拿掉 ttMove，这个局面还有多好」，所以必须真的把它
	// 从着法列表里跳过（`m == excludedMove` 时 continue），否则会原样搜出同一个
	// 分值、永远判不出「奇异」。同一个理由下，排除模式里**既不能读置换表截断、
	// 也不能写表** —— 前者会拿原先那个表项自证，后者会把「拿掉 ttMove 的分值」
	// 覆盖到该局面的表项上，污染后续搜索。
	//
	// 用 Searcher 字段而不是函数参数：验证搜索搜的是**同一个局面**（ply 不变），
	// 只在这一次调用期间有效，递归进入子结点前必须清空。alphaBeta 入口读走即清，
	// 所以不会漏给子树。
	excludedMove game.Move

	// moveHist[ply] 记录在 ply 这一层实际搜过的着法，供「反复挪子」判定
	// （isShuffling）读 ply-2 / ply-4 的着法。深度优先保证这两个槽位属于
	// 当前路径上的祖先，不会读到别的分支。
	moveHist [MaxPly]game.Move

	// stop 是中止标志。多线程时所有线程共享同一个实例，任一线程（或计时器）
	// 置位后全体尽快退出。alphaBeta 每节点读一次，所以用原子变量。
	stop *atomic.Bool
}

// Result 是一次搜索的结果。
type Result struct {
	Best  game.Move
	Score int
	Depth int
	// Nodes 是结点进入次数（本引擎的内部口径）；Makes 是走子次数。
	//
	// ⚠️ 跨引擎比较工作量只能用 Makes：皮卡鱼的 `++nodes` 记在 `do_move` 里
	// （src/search.cpp:628），即走子次数。两者比值随深度变化（中局 d12 实测
	// Nodes:Makes ≈ 1:2.3、d1 反而 >1），拿 Nodes 去比会让我们的工作量
	// 少报一倍多 —— 这正是 2026-09-18 之前所有「等节点」跨引擎结论失真的原因。
	Nodes int64
	Makes int64
	// Roots 是根节点各着法及其分值，按分值降序。
	// 低档位用它在前几个着法里随机挑，以制造「人味失误」。
	Roots []RootMove
}

// RootMove 是根节点一个着法及其搜索分值。
type RootMove struct {
	Move  game.Move
	Score int
}

// New 构造搜索器，带默认大小的置换表。
func New(w *nnue.Weights) *Searcher {
	return &Searcher{
		w:    w,
		tt:   NewTranspositionTable(DefaultTTSizeMB),
		stop: &atomic.Bool{},
	}
}

// NewWithTT 构造指定置换表大小（兆字节）的搜索器；sizeMB <= 0 时用默认值。
func NewWithTT(w *nnue.Weights, sizeMB int) *Searcher {
	s := New(w)
	if sizeMB > 0 {
		s.tt = NewTranspositionTable(sizeMB)
	}
	return s
}

// SetTTSizeMB 重建置换表并清空启发式表。
func (s *Searcher) SetTTSizeMB(sizeMB int) {
	s.tt = NewTranspositionTable(sizeMB)
	s.Clear()
}

// Clear 清空置换表与启发式表（每次新搜索开始前调用，保证结果可复现）。
func (s *Searcher) Clear() {
	if s.tt != nil {
		s.tt.Clear()
	}
	s.clearHeuristics()
}

// clearHeuristics 清空线程私有的启发式表与统计，不碰共享的置换表。
// 多线程时由池统一清一次表，各线程各自调这个。
func (s *Searcher) clearHeuristics() {
	s.killers = [MaxPly][2]game.Move{}
	s.history = [90][90]int{}
	s.resetStats()
}

// resetStats 只重置计数，保留 killers/history。
//
// 连续对弈时每步都该保留启发式经验（它们按着法/局面索引，跨步复用有效），
// 但统计量要按次归零才能看清当前这一步的效率。
func (s *Searcher) resetStats() {
	if diagQ {
		qdAbNodes, qdQNodes, qdInCheck, qdDeltaCut = 0, 0, 0, 0
	}
	s.ttHits = 0
	s.makes = 0
	s.nullMoves = 0
	s.probeCnt = 0
	s.abCallCnt = 0
	s.storeCnt = 0
	s.cutoffs = 0
	s.cutoffFirst = 0
	s.cutoffIdxSum = 0
	s.ttMoveAvail = 0
	s.ttMoveSorted = 0
	s.ttMoveFirst = 0
	s.ttMoveIlleg = 0
	s.seTriggers = 0
	s.seExtended = 0
	s.seMultiCut = 0
	s.seNegExt = 0
}

// Stop 请求中止当前搜索（超时或上层取消）。多线程下共享同一标志。
func (s *Searcher) Stop() {
	if s.stop != nil {
		s.stop.Store(true)
	}
}

// TTHits 返回本次搜索的置换表命中次数。
func (s *Searcher) TTHits() int64 { return s.ttHits }

// NullMoves 返回本次搜索实际尝试的空着次数。
func (s *Searcher) NullMoves() int64 { return s.nullMoves }

// DisableTT 关闭置换表，供对照实验使用（nil 表下探测恒为未命中）。
func (s *Searcher) DisableTT() { s.tt = nil }

// DisableNullMove 关闭空着剪枝，供对照实验使用。
func (s *Searcher) DisableNullMove() { s.noNull = true }

// DisableForwardPruning 关闭前向剪枝（reverse futility / futility / LMP）。
//
// 与 TT、空着剪枝不同，这几个是**有损启发式**：它们按静态评估推断
// 「这个分支不可能更好」并直接跳过，因此会改变搜索结果换取速度。
// 对照实验需要这个开关来量出它们各自的收益。
func (s *Searcher) DisableForwardPruning() { s.noForward = true }

// DisableSEE 关闭静态搜索里的坏吃子剪枝，供对照实验使用。
func (s *Searcher) DisableSEE() { s.noSEE = true }

// DisableProbCut 关闭 ProbCut（含「廉价 ProbCut 变体」），供对照实验使用。
func (s *Searcher) DisableProbCut() { s.noProbCut = true }

// DisableProbCutSeeFilter 让 ProbCut 考虑全部吃子而不做 SEE 筛选，供对照实验使用。
//
// 实测这个筛选是 ProbCut 能否有收益的关键：去掉后安静局面节点从 0.992× 变成
// 1.039×。守卫 TestProbCutSeeFilterIsUsed 靠它证明「筛选确实被读取」。
func (s *Searcher) DisableProbCutSeeFilter() { s.noPCSee = true }

// DisableAspiration 关闭期望窗口，每层都用全窗 —— 即引入期望窗口之前的行为。
//
// 对照实验里要它是因为：期望窗口会通过置换表扰动搜索轨迹，使个别局面在同一
// 深度上的将杀发现晚一层。想让参照搜索给出「信息最完整」的结果时，必须把它关掉。
func (s *Searcher) DisableAspiration() { s.noAsp = true }

// ttProbe / ttStore 在置换表被禁用时退化为空操作。
func (s *Searcher) ttProbe(key uint64) (ttEntry, bool) {
	if s.tt == nil {
		return ttEntry{}, false
	}
	return s.tt.probe(key)
}

func (s *Searcher) ttStore(key uint64, move game.Move, score int32, depth int, flag uint8, pv bool) {
	if s.tt == nil {
		return
	}
	s.tt.store(key, move, score, depth, flag, pv)
}

// prepare 把评估侧局面与累加器对齐到搜索起点。
//
// 每次新搜索都必须调用：pos 要与传入局面一致，acc 要失效（它可能还缓存着
// 上一局或上一次搜索的局面，残留的 meta 会让增量路径拿错基准做差集）。
func (s *Searcher) prepare(p *game.Position) {
	s.pos.ResetFromGame(&p.Board, p.Turn>>3)
	s.acc.Invalidate()
}

// Search 从 depth 1 开始迭代加深到 maxDepth，返回最后完成的一层结果。
//
// 只采用**完整跑完**的那一层：被中止的层结果作废（半途而废的 alpha-beta
// 给出的分值不可信）。
func (s *Searcher) Search(p *game.Position, maxDepth int) Result {
	s.stop.Store(false)
	s.nodes = 0
	s.Clear()
	return s.searchLoop(p, TimeLimit{}, maxDepth, false)
}

// SearchDepth 只搜固定深度，不做迭代加深（用于逐层对拍）。
func (s *Searcher) SearchDepth(p *game.Position, depth int) Result {
	s.stop.Store(false)
	s.nodes = 0
	s.Clear()
	s.prepare(p)
	roots, ok := s.rootSearch(p, depth, -Infinity, Infinity)
	if !ok || len(roots) == 0 {
		return Result{Nodes: s.nodes, Makes: s.makes}
	}
	return Result{
		Best:  roots[0].Move,
		Score: roots[0].Score,
		Depth: depth,
		Nodes: s.nodes,
		Makes: s.makes,
		Roots: roots,
	}
}

// Nodes 返回已搜索的节点数（**结点进入次数**，本引擎的内部口径）。

// SetIterObserver 设置「每次完整迭代完成后」的回调；传 nil 取消。
//
// 回调里给的 Result 是**该次迭代结束时的快照**（Depth/Score/Best/Nodes/Makes
// 都是当时的累计值）。它在搜索线程上被调用，所以回调体必须自己保证并发安全，
// 且不能反过来调用本 Searcher（会破坏搜索状态）。
func (s *Searcher) SetIterObserver(f func(Result)) { s.onIter = f }
func (s *Searcher) Nodes() int64                   { return s.nodes }

// Makes 返回走子次数，即「走一步算一个」的口径 —— **与皮卡鱼的 nodes 同口径**。
//
// 跨引擎比较搜索工作量只能用这个，不能用 Nodes()：两者的比值随深度变化
// （d12 时 nodes:makes ≈ 1:2.3），直接比会把我们的工作量报少一倍多。
func (s *Searcher) Makes() int64 { return s.makes }

// TTLen 返回置换表容量（项数）；表被禁用时返回 0。
func (s *Searcher) TTLen() int {
	if s.tt == nil {
		return 0
	}
	return s.tt.Len()
}

// rootSearch 搜索根节点，返回按分值降序的根着法列表与「是否有合法着法」。
//
// 根节点不剪枝（每个着法都要有分值），因为低档位要按分值在前几个着法里
// 随机挑 —— 这需要知道除最优之外的着法有多好。分值是 fail-soft 语义，
// 未超过 alpha 的着法返回的是其子树实际搜到的最大值（真实值的上界），
// 用于排序足够。
func (s *Searcher) rootSearch(p *game.Position, depth, alpha, beta int) ([]RootMove, bool) {
	// 记录根窗口宽度供 LMR 的缩放公式使用（皮卡鱼：`rootDelta = beta - alpha`）。
	// 全窗（±Infinity）不是有意义的宽度，退回期望窗口的默认半宽两倍。
	if w := beta - alpha; w > 0 && w < 1<<16 {
		s.rootDelta = w
	} else {
		s.rootDelta = 2 * aspirationDelta
	}
	moves := p.LegalMoves(p.Turn)
	if len(moves) == 0 {
		return nil, false
	}
	var ttMove game.Move
	if e, ok := s.ttProbe(p.Key); ok {
		ttMove = decodeMove(e.move)
	}
	s.orderMoves(p, moves, 0, ttMove)

	roots := make([]RootMove, 0, len(moves))
	best := game.Move{}
	bestScore := -Infinity
	entryAlpha := alpha

	for i, m := range moves {
		if s.stopped() {
			return roots, true // 中止：本层作废，由调用方丢弃
		}
		victim := p.PieceAt90(int(m.To))
		p.Make(m)
		s.makes++
		s.pos.Make(int(m.From), int(m.To))

		var score int
		if i == 0 {
			score = -s.alphaBeta(p, depth-1, -beta, -alpha, 1, true, true, false)
		} else {
			// 根节点同样走 PVS + LMR：先窄窗口试探，必要时重搜。
			// 只有落在窗口内部的着法才值得全窗重搜 —— 已经达到 beta 的着法
			// 意味着本层 fail high，调用方会放宽窗口整层重搜，这里不必再花代价。
			// 根结点恒为 PV，故其非 PV 子结点的 cutNode = !false = true
			// （皮卡：`-search<NonPV>(..., !cutNode)`，根的 cutNode 为 false）。
			red := s.reduction(depth, i, victim, false)
			score = -s.alphaBeta(p, depth-1-red, -alpha-1, -alpha, 1, false, true, true)
			if score > alpha && red > 0 {
				score = -s.alphaBeta(p, depth-1, -alpha-1, -alpha, 1, false, true, true)
			}
			if score > alpha && score < beta {
				score = -s.alphaBeta(p, depth-1, -beta, -alpha, 1, true, true, false)
			}
		}
		p.Unmake()
		s.pos.Unmake()

		roots = append(roots, RootMove{Move: m, Score: score})
		if score > bestScore {
			bestScore, best = score, m
		}
		if score > alpha {
			alpha = score
		}
		// fail high：本层作废，调用方会放宽窗口把整层重搜一次。继续往下搜只会
		// 在退化的窗口里空转（alpha 已 ≥ beta，后续子节点拿到的是空窗口），
		// 等于白费节点。全窗时 beta = +∞，这条分支不可能触发，所以它只影响
		// 期望窗口路径。
		if score >= beta {
			break
		}
	}
	if s.stopped() {
		return roots, true
	}

	sortRootsDesc(roots)
	// 写表的标志必须跟着**入口窗口**走：入口是 ±∞（全窗）时才是精确值；
	// 期望窗口下没有超过入口 alpha 说明是上界、达到入口 beta 说明是下界。
	// 一律写 ttExact 会把截断值当成精确值喂给后续搜索。
	flag := ttExact
	if bestScore <= entryAlpha {
		flag = ttUpper
	} else if bestScore >= beta {
		flag = ttLower
	}
	s.ttStore(p.Key, best, scoreToTT(bestScore, 0), depth, flag, true)
	return roots, true
}

// sortRootsDesc 按分值降序排列根着法；同分保持原顺序（原顺序来自着法排序，
// 同分时它更可信）。
func sortRootsDesc(roots []RootMove) {
	// 插入排序：根着法数通常不超过 60，且基本已按强弱排列。
	for i := 1; i < len(roots); i++ {
		v := roots[i]
		j := i - 1
		for j >= 0 && roots[j].Score < v.Score {
			roots[j+1] = roots[j]
			j--
		}
		roots[j+1] = v
	}
}

// stopped 返回是否已被要求中止（超时或上层取消）。
func (s *Searcher) stopped() bool {
	return s.stop != nil && s.stop.Load()
}

// alphaBeta 是 negamax 形式的 alpha-beta，返回**走子方视角**的分值。
//
// isPV 标记主变例节点：只有非 PV 节点才允许直接用置换表的分值剪枝，
// 因为 PV 节点的分值受窗口影响，直接返回会截断主变例。
// canNull 标记允许空着剪枝（连续两次空着会退化成无意义的搜索，须禁止）。
// diagTier 开启「截断着法来自哪一档排序」的统计（`QIJING_TIER=on`）。
//
// 用途：判断该补哪一项排序技术，而不是凭「皮卡鱼有、我们没有」就动手。
//
// **实测结论（2026-09-17，23 个局面、15037 次截断）：排序不是本引擎的瓶颈，
// 投历史启发是白费。** 分布如下（后者为平均截断序号）：
//
//	ttMove        23.8%（0.00）   吃子 MVV-LVA  46.2%（0.29）
//	killer[0]     16.4%（0.97）   killer[1]      3.9%（1.45）
//	history>0      7.1%（2.69）   history=0      2.5%（11.83）
//
// 即 **70% 的截断发生在搜索序号 ≤0.3 的位置**，已经接近最优；历史两档合计
// 只有 9.6%。而且历史表**没有饱和**（非零格仅 0.2%、顶到上限 0 格）——
// 这一点与「历史启发只加不减会导致饱和」的直觉相反，是我先错误假设、后实测
// 推翻的。即使把历史修到完美，最多也只值约 2% 节点（<0.03 层），低于噪声。
//
// 保留它作为将来的机制指标：任何排序改动都应该先让这张表动起来。
var diagTier = os.Getenv("QIJING_TIER") == "on"

// diagQ 由 QIJING_QDIAG=on 启用静态搜索结构诊断（TestQuiesceStructure 打印）。
//
// 用来回答「战术弱项是不是出在静态搜索」这类问题 —— 靠读数，不靠对照源码猜。
// 2026-09-17 实测（150 道残局题 × 10 万节点）：
//
//	qsearch 占全部节点 **21.5%**、其中被将结点仅 **0.5%**
//	被 delta 剪枝整批返回的占 qsearch 结点 **93.4%**
//
// ⇒ 静态搜索已经很精简，且「被将展开全部着法」这条最贵的路径几乎没有量级。
//
//	靠改 qsearch 提升战术没有空间（SEE 坏吃子剪枝也早测过无收益）。
//
// ⚠️ 它**不是** EBF 的成因：qsearch 只占 21.5%，即使全部砍掉也换不到 0.3 层。
var diagQ = os.Getenv("QIJING_QDIAG") == "on"

var (
	qdAbNodes  int64 // alphaBeta 节点数
	qdQNodes   int64 // qsearch 节点数
	qdInCheck  int64 // qsearch 中处于被将的节点
	qdDeltaCut int64 // 被 delta 剪枝整批返回的次数
)

type tierStat struct{ cnt, idxSum int64 }

var tierDiag [6]tierStat
var tierNames = [6]string{"ttMove", "吃子(MVV-LVA)", "killer[0]", "killer[1]", "history>0", "history=0"}

// moveTier 判定着法落在哪一档（顺序必须与 moveScore 的优先级一致）。
func (s *Searcher) moveTier(m game.Move, ply int, ttMove game.Move, victim uint8) int {
	if m == ttMove && ttMove != (game.Move{}) {
		return 0
	}
	if victim != game.Empty {
		return 1
	}
	if m == s.killers[ply][0] {
		return 2
	}
	if m == s.killers[ply][1] {
		return 3
	}
	if s.history[m.From][m.To] > 0 {
		return 4
	}
	return 5
}

// boolToInt 把布尔转成 0/1，用于照抄皮卡鱼那些「按条件加减余量」的公式。
// Go 的 bool 不能直接参与算术，写成 if 会让公式与源码对不上号。
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// isShuffling 判断这步棋是不是「反复挪子」（来回走同一条线）。
//
// 奇异延伸的前提是「ttMove 是唯一好手」，但在子力稀薄的残局里，来回挪子也会
// 让置换表反复给出同一个「低价值但唯一」的着法，于是每一步都被判成奇异、被
// 一路延伸，树会莫名膨胀 —— 本引擎的和棋局正是这种形态。
//
// 判据照抄皮卡鱼：现在这步的起点等于两步前的落点，且两步前那步的起点等于
// 四步前的落点（即一个完整的来回）。三个前置条件也必须保留：吃子着法不算
// 挪子、自然限着回合数还小的时候不算（棋局还在实质进展）、开局阶段不算。
//
// **与皮卡的差异**：它还要求 `pliesFromNull >= 6`（刚空着过就不算挪子），
// 本引擎没有这个计数，故省略。省略的后果是**多抑制一些延伸**（树更小），
// 属于偏保守的方向。
func (s *Searcher) isShuffling(p *game.Position, m game.Move, ply int) bool {
	if p.PieceAt90(int(m.To)) != game.Empty || p.Halfmove < 10 || ply < 20 {
		return false
	}
	a, b := s.moveHist[ply-2], s.moveHist[ply-4]
	return m.From == a.To && a.From == b.To
}

// cutNode 标记「预期会发生 beta 截断」的结点（皮卡鱼的 cutNode）。
//
// 它在皮卡鱼里参与多处启发式，本引擎目前**只用于奇异延伸**（负延伸的触发、
// 以及传给验证搜索）。传递规则照抄皮卡鱼：非 PV 子结点取反，PV 子结点恒为
// false —— 于是这个标记沿非 PV 路径逐层交替，标识出「这里该是截断点」。
func (s *Searcher) alphaBeta(p *game.Position, depth, alpha, beta, ply int, isPV, canNull, cutNode bool) int {
	// 每节点读一次中止标志：原子读比取时钟便宜得多，所以可以查得很密。
	if s.stopped() {
		return 0
	}
	s.abCallCnt++
	s.nodes++
	if diagQ {
		qdAbNodes++
	}
	if ply >= MaxPly-1 {
		return s.evaluate(p)
	}

	// 排除着法（奇异延伸的验证搜索）：读走即清，只对本次调用有效。
	// 子结点读到的必须是零值，否则「拿掉 ttMove」的语义会漏进整棵子树。
	excludedMove := s.excludedMove
	hasExcluded := excludedMove.From != 0 || excludedMove.To != 0
	s.excludedMove = game.Move{}
	// 本层尚未搜索任何着法。空着剪枝不会经过着法循环，所以这里先清一次，
	// 保证 isShuffling 读到的 ply-2/ply-4 槽位不会把空着当成真实着法。
	s.moveHist[ply] = game.Move{}

	if ply > 0 {
		// RepetitionCount 含当前局面，首次出现返回 1，所以 >1 才是真的重复。
		if p.RepetitionCount() > 1 {
			return 0
		}
		if p.Halfmove >= 120 { // 60 回合自然限着
			return 0
		}
	}

	// 将杀距离剪枝：已经找到更短的杀时不必再搜。
	if alpha < -MateScore+ply {
		alpha = -MateScore + ply
	}
	if beta > MateScore-ply-1 {
		beta = MateScore - ply - 1
	}
	if alpha >= beta {
		return alpha
	}

	if depth <= 0 {
		return s.quiesce(p, alpha, beta, ply)
	}

	inCheck := p.InCheck(p.Turn)

	var ttMove game.Move
	ttHit := false
	ttPVEntry := false
	ttScore := -Infinity
	ttDepth := 0
	ttFlag := uint8(ttNone)
	s.probeCnt++
	if e, ok := s.ttProbe(p.Key); ok {
		s.ttHits++
		ttHit = true
		ttScore = scoreFromTT(e.score, ply)
		ttDepth = int(e.depth)
		ttFlag = e.bound()
		ttPVEntry = e.isPV()
		ttMove = decodeMove(e.move)
		if ttMove.From != 0 || ttMove.To != 0 {
			s.ttMoveAvail++
		}
		// hasExcluded 时绝不能按表截断：这个表项正是「存在 ttMove 局面」的结果，
		// 拿它下结论等于自己证明自己，验证搜索永远判不出「奇异」。
		if !isPV && !hasExcluded && int(e.depth) >= depth {
			sc := scoreFromTT(e.score, ply)
			switch e.bound() {
			case ttExact:
				return sc
			case ttLower:
				if sc >= beta {
					return sc
				}
			case ttUpper:
				if sc <= alpha {
					return sc
				}
			}
		}
	}

	// ttPvSeen 对应皮卡的 `ss->ttPv`（「这棵子树整体位于主变例上」）。
	//
	// 皮卡的完整定义是 `PvNode || (ttHit && ttData.is_pv())`，并且沿栈向下传播
	// （`ss->ttPv = ss->ttPv || (ss-1)->ttPv`）。本引擎**没有做向下传播**：
	// 只取「本结点是 PV」或「本局面的表项来自 PV」，于是 PV 线以下的那些结点
	// 会漏掉 ttPv。**漏掉的方向是更激进，不是更保守**：ttPv=0 让深度门槛从 6
	// 降到 5、余量从 116 降到 44，都是**更容易触发延伸**的一档。所以如果实测
	// 出现树变大，先怀疑这里，别先怀疑机制本身。
	ttPvSeen := isPV || ttPVEntry

	// 静态评估只算一次，供下面几种前向剪枝共用（evaluate 约 6µs，不能重复调用）。
	//
	// 副作用说明：evaluate 会顺带更新 s.nonPawn，而空着剪枝要读它，
	// 所以必须在这里先完成这次调用，之后再读 nonPawn 才是当前节点的值。
	staticEval := -Infinity
	if !inCheck {
		staticEval = s.evaluate(p)
	}
	s.staticEvalHist[ply] = staticEval

	// improving：本节点评估优于两步前，说明局势正朝有利方向走。
	// 此时静态评估更可信，各种剪枝可以更积极（皮卡鱼用它动态放松余量）。
	// 被将军时 staticEval 为 -Infinity，比较结果为 false，落在保守一侧。
	improving := ply >= 2 && staticEval > s.staticEvalHist[ply-2]

	// Razoring：静态评估比 alpha 低了整整 709*depth²，这个节点几乎不可能
	// 找到好着法，直接交给静态搜索给一个上界，省掉整棵子树。
	// 二次因子让它在深层迅速失效 —— 深层误剪的代价大得多。
	if !isPV && !inCheck && depth <= 6 && staticEval < alpha-709*depth*depth {
		return s.quiesce(p, alpha, beta, ply)
	}

	// Reverse futility pruning（静态空着剪枝）：静态评估已经高出 beta 一大截，
	// 即便对手连走两步也追不回来，直接返回。比空着剪枝便宜得多 —— 它不用真的搜一遍。
	//
	// 允许的最大深度由 futilityDepth 动态给出（而不是固定值）：评估越接近
	// 将杀分值，允许剪的深度越小，从而保住杀棋查找。
	if !s.noForward && !isPV && !inCheck && beta < MateScore-MaxPly &&
		depth < futilityDepth(staticEval, beta) {
		margin := reverseFutilityMargin(depth, improving,
			ply >= 1 && staticEval > -s.staticEvalHist[ply-1])
		if staticEval-margin >= beta {
			return staticEval
		}
	}

	// 空着剪枝：让对手连走两步仍不能改善，说明这个分支已经足够好。
	// 被将军时空着不合法；子力稀薄时禁用，避免残局的 zugzwang 误判。
	// hasExcluded 时不做空着剪枝（皮卡鱼同样排除）：验证搜索要回答的是
	// 「拿掉 ttMove 后这个局面有多好」，空着给出的下界会把结论带偏。
	if !s.noNull && !hasExcluded && canNull && !inCheck && depth >= 3 && staticEval >= beta && s.nonPawn[p.Turn>>3] >= 2 {
		// 削减量与深度线性相关，并随「评估超出 beta 的幅度」继续加大：
		// 静态评估越是碾压 beta，空着搜索越没有必要搜得那么深。
		red := 8 + depth/3
		if d := (staticEval - beta) / 256; d > 0 {
			red += d
		}
		s.nullMoves++
		p.MakeNull()
		s.pos.MakeNull()
		score := -s.alphaBeta(p, depth-1-red, -beta, -beta+1, ply+1, false, false, !cutNode)
		p.UnmakeNull()
		s.pos.UnmakeNull()
		if score >= beta && score < MateScore-MaxPly {
			return score
		}
	}

	// 内部迭代削减（IIR）：置换表里没有这个局面的着法，说明它还没被搜过，
	// 着法排序只能靠 MVV-LVA 与历史启发，质量差。先按削减一层的深度搜一遍
	// 把它写进置换表，后续（迭代加深的下一轮、或别的路径到达同一局面）
	// 就能拿到着法来排序，从而剪掉更多分支。
	//
	// 我们的置换表命中率只有 7~8%（剪枝与置换表在降低有效分支因子上相互替代，
	// 见 tt_diag_test），所以「没有置换表着法」是常态而不是例外。
	//
	// 实测（固定 10 万节点、150 道残局题）：命中 102/150 → 105/150，
	// 均深 25.23 → 25.74 层。固定深度下残局题节点减少 27% —— 削减的是
	// depth >= 6 的节点，它们的子树很大；安静局面则几乎不变（−0.3%）。
	// 固定 6 万节点下安静局面的平均深度 14.55 → 14.70。
	//
	// 与皮卡鱼的差异：它额外要求 cutNode（非 PV 节点）。这里两个变体都测过：
	// 命中相同（105/150），不限节点类型的均深略好（25.74 vs 25.57），故不加限制。
	if depth >= 6 && ttMove.From == 0 && ttMove.To == 0 {
		depth--
	}

	moves := p.LegalMoves(p.Turn)
	if len(moves) == 0 {
		// 中国象棋里无着法可走即为负，被将死与困毙同判。
		return -MateScore + ply
	}

	s.orderMoves(p, moves, ply, ttMove)

	// TT 着法拿到最高档，只要它出现在着法列表里就必然排首位。
	// 于是「未排首位」只有两种解释：统计被提前 return 打断（看 ttMoveSorted
	// 与 ttMoveAvail 的差额），或者该着法根本不在这个局面的着法列表里
	// （ttMoveIlleg，意味着置换表存了与当前局面不符的着法）。
	if len(moves) > 0 && (ttMove.From != 0 || ttMove.To != 0) {
		s.ttMoveSorted++
		if moves[0] == ttMove {
			s.ttMoveFirst++
		} else if diagMoveOrder {
			missing := true
			for _, m := range moves {
				if m == ttMove {
					missing = false
					break
				}
			}
			if missing {
				s.ttMoveIlleg++
			}
		}
	}

	best := -Infinity
	bestMove := game.Move{}
	origAlpha := alpha

	// ProbCut：某个吃子着法一经浅搜就远超 beta，说明当前节点几乎必然 fail high
	// （对手不会容忍走到这里），可以直接按这个下界返回。
	//
	// 与其它前向剪枝的根本区别：它**真的搜一遍**（先静态搜索验证，再按削减的
	// 深度搜），所以代价高得多 —— 只在 depth >= 3、且有「看起来白赚」的吃子时才做。
	//
	// 候选着法的筛选用 SEE（`see_ge(move, probCutBeta - staticEval)`）——这一步
	// 照抄皮卡鱼 MovePicker 的 PROBCUT 阶段。**这个筛选是 ProbCut 能否站得住的
	// 关键**：实测去掉它（考虑全部吃子）后安静局面从 0.992× 变成 1.039×，由
	// 「小赚」变「净亏」。它也是本项目里 SEE 唯一测出正向贡献的用法（另外两个
	// 用法见 seeEnabled / seeQuietEnabled 的注释，都无收益）。
	//
	// 阈值里的 improving 项方向是**抬高一档更难触发**：251 - 66*improving
	// （improving 时余量 185），同时削减的深度也更多（-5 而非 -3）。
	// 这条与反 futility 的 improving 项方向相反，别凭直觉统一。
	//
	// ⚠️ 实测在本引擎上是净亏损，默认关闭（QIJING_PC=on 启用）。
	// 表里所有数字都是确定性测量（单线程 + 每题独立置换表）：
	//
	//	安静局面 depth 10（55 个）      节点 0.992×
	//	固定 6 万节点（20 个安静局面）   均深 14.70 → 14.90
	//	固定 10 万节点·400 题           命中 336 → 329、均深 50.17 → 49.95
	//	残局 400 题 depth 9            节点 1.110×（命中 263 → 264）
	//	保真度（20 个安静局面 depth 6，对照「关前向剪枝 + 关期望窗口」）
	//	                              一致率 13/20、平均分值差 56、最大 237
	//	                              —— 开关两态**完全相同**
	//
	// 保真度无差异说明它**不是剪过头**，问题是每节点并不更有效。成因已量清：
	// 残局题上 42573 个节点进入判断、只有 6988 个吃子通过 SEE（16.4%）、其中
	// 4351 个验证成功（62.3%）—— 失败的 2637 次验证（qsearch + 削减 alphaBeta）
	// 是纯开销，而成功截断省下的着法循环本来就不贵。ProbCut 成立的前提是
	// 「节点内着法循环贵、浅搜便宜」，而本引擎经期望窗口 + IIR + 反 futility
	// 之后恰好相反：着法循环已经很便宜，验证搜索反而不便宜。
	probCutBeta := beta + 251
	if improving {
		probCutBeta = beta + 185
	}
	// 置换表已经给出「低于阈值」的证据时不必再试（皮卡鱼的
	// `!(is_valid(ttData.value) && ttData.value < probCutBeta)`）。
	if !s.noProbCut && probCutEnabled && depth >= 3 &&
		beta < MateScore-MaxPly && beta > -MateScore+MaxPly &&
		!(ttHit && ttScore < probCutBeta) {
		probCutDepth := depth - 3
		if improving {
			probCutDepth = depth - 5
		}
		for _, m := range moves {
			// 只考虑吃子（皮卡鱼的 PROBCUT 阶段也只生成吃子）。
			//
			// 这里**不能**用「遇到第一个安静着法就 break」来筛 —— 排序分里
			// TT 着法（1<<24）高于所有吃子，TT 着法恰是安静着法时它会排在首位，
			// 那样一进循环就 break，ProbCut 等于没实现。用 continue 逐个判。
			if p.PieceAt90(int(m.To)) == game.Empty {
				continue
			}
			if s.stopped() {
				return best
			}
			// SEE 必须在落子之前算（落子后 from 已空、to 上站着自己的子，
			// SeeGE 的第二层捷径恒成立 —— 这个坑在静的着法剪枝上踩过一次）。
			if !s.noPCSee && !p.SeeGE(int(m.From), int(m.To), probCutBeta-staticEval) {
				continue
			}
			p.Make(m)
			s.makes++
			s.pos.Make(int(m.From), int(m.To))
			// 先做一次零窗静态搜索：吃子本身就被静态搜索覆盖，不划算的在这里就出局了。
			v := -s.quiesce(p, -probCutBeta, -probCutBeta+1, ply+1)
			if v >= probCutBeta && probCutDepth > 0 {
				v = -s.alphaBeta(p, probCutDepth, -probCutBeta, -probCutBeta+1, ply+1, false, true, !cutNode)
			}
			p.Unmake()
			s.pos.Unmake()
			if v >= probCutBeta {
				// 存进去让后续节点也能直接用这个下界。深度夹紧到 0：probCutDepth
				// 在 depth 3~4 且 improving 时会是负数，若直接传给 uint8 的深度
				// 字段会回绕成 255 —— 那会变成「极深的下界」，引发错误的截断。
				stored := probCutDepth + 1
				if stored < 0 {
					stored = 0
				}
				s.ttStore(p.Key, m, scoreToTT(v, ply), stored, ttLower, false)
				if v < MateScore-MaxPly {
					// 把「在更高的 beta 上验证出的下界」换算回本节点的窗口。
					return v - (probCutBeta - beta)
				}
				// 将杀分不做换算也不返回：那是「找到了杀」而不是「评估远超 beta」，
				// 换算会得到一个无意义的分数。留给正常着法循环去确认。
			}
		}
	}

	// 「廉价 ProbCut 变体」（皮卡鱼 Step 11）：置换表里已经有「下界、深度只差
	// 不到 4 层、分值高出 beta 470」的证据时，直接按这个下界返回 —— 不额外搜索，
	// 所以它比上面那个 ProbCut 便宜得多，代价是精度更低（用一个别处搜出来的
	// 下界近似本节点的分值）。
	//
	// 与上面的 ProbCut 分开开关，因为两者代价量级不同，必须各自量。
	// 将杀分与将杀窗口都要排除：那是「找到杀」而不是「评估远超 beta」，
	// 返回 beta+470 会掩盖真实的杀棋距离。
	//
	// ⚠️ 实测同样净亏损，默认关闭（QIJING_PC_SMALL=on 启用）：
	//
	//	安静局面 depth 10（55 个）      节点 1.006×（触发 8680 次）
	//	固定 10 万节点·400 题           命中 336 → 327、均深 50.17 → 50.66
	//
	// 后一行是关键：**深度升了 0.49 层而命中反降 9 题** —— 深了却更不准，
	// 这正是「有害剪枝」的特征（用一个别处搜出的下界近似本节点分值，
	// 省了节点但把分值带偏了）。
	if !s.noProbCut && probCutSmallEnabled && ttHit && ttFlag == ttLower &&
		ttDepth >= depth-4 && beta < MateScore-MaxPly && beta > -MateScore+MaxPly &&
		ttScore >= beta+470 && ttScore < MateScore-MaxPly && ttScore > -MateScore+MaxPly {
		return beta + 470
	}

	for i, m := range moves {
		if s.stopped() {
			return best
		}
		// 验证搜索要把被排除的着法真正拿掉，否则原样搜回同一个分值。
		if hasExcluded && m == excludedMove {
			continue
		}
		victim := p.PieceAt90(int(m.To))
		quiet := victim == game.Empty
		skip := false
		// 记录本层正在搜的着法（isShuffling 要读 ply-2 / ply-4）。
		// 放在 continue 之后：被跳过的着法等于没搜过。
		s.moveHist[ply] = m

		// 削减量在这里先算好：下面的 futility 与 SEE 都要用 lmrDepth
		// （削减后的有效深度），真正的搜索也用它，不必算两遍。
		red := s.reduction(depth, i, victim, inCheck)
		r1024 := red * 1024
		// ⚠️ 只在基础削减本就生效时才用缩放公式（即「安静着法 && 非将军 && depth>=3 &&
		// index>=4」）。皮卡鱼对吃子/将军也套 Step 16，但它 Step 13 先有「吃子 futility +
		// 吃子/将军 SEE 剪枝」把坏吃子拿掉；我们那两处只作用于安静着法 ⇒ 去掉这个门会让
		// 坏吃子被削到 1 层、频繁 fail high 触发重搜（实测中局 d12 节点 +378%）。
		if lmr2 && red > 0 {
			// ⚠️ delta 必须夹到 rootDelta 以内。皮卡鱼的根窗口恒是期望窗口，所以
			// `delta = beta - alpha` 天然 ≤ rootDelta；而我们的固定深度搜索用全窗
			// 根节点（`±Infinity`），根的子结点会拿到 `delta = 2×Infinity` ——
			// 代进公式会让 r 剧烈为负、`d` 被抬到 `newDepth+2`，等于**全树白送两层
			// 延伸**（实测中局局面节点 +378%）。夹一下即回到皮卡的语义。
			delta := beta - alpha
			if delta > s.rootDelta {
				delta = s.rootDelta
			}
			r1024 = lmrRated(improving, depth, i+1, delta, s.rootDelta)
			// 追加削减项（只搬本项目有对应信号的；详见 lmr2 的注释）。
			if ttPvSeen {
				r1024 += 931
			}
			if cutNode {
				r1024 += 3251
				if !ttHit {
					r1024 += 1048
				}
			}
			ttCapture := ttHit && p.PieceAt90(int(ttMove.To)) != game.Empty
			if ttCapture {
				r1024 += 1571
			}
			if ttHit && m == ttMove {
				r1024 -= 2730
			}
			if victim == game.Empty && absInt(alpha) < MateScore-MaxPly {
				r1024 += 3 * clampInt(alpha-staticEval, -64, 96)
			}
			if !isPV && !cutNode { // 皮卡的 allNode
				r1024 += r1024 * 256 / (256*depth + 256)
			}
			red = r1024 / 1024
		}
		lmrDepth := depth - 1 - red
		if lmr2 {
			// 皮卡这一步用的是 /1005（不是 /1024），照抄。
			lmrDepth = depth - 1 - r1024/1005
		}
		if lmrDepth < 0 {
			lmrDepth = 0
		}

		// 前向剪枝，只作用于安静着法：吃子会大幅改变评估，不能按静态值判断。
		// best > -Infinity 保证至少搜完一个着法，否则整层可能被剪空。
		//
		// 将杀窗口内一律不剪 —— 那时 alpha/beta 本身已是杀分，静态评估失去
		// 参考意义。
		if !s.noForward && !isPV && !inCheck && quiet && best > -Infinity &&
			beta < MateScore-MaxPly && alpha > -MateScore+MaxPly {
			// Futility pruning：静态评估加上随有效深度放宽的余量仍够不到 alpha，
			// 这个安静着法不可能成为最佳着法。
			//
			// 余量与深度上限都照抄皮卡鱼（`129*lmrDepth + 112*(eval>alpha) + 319`，
			// 有效深度 < 10）。此前用的是自定的 `120 + 60*depth` 且只作用于
			// depth ≤ 5 —— 余量约为它的一半、深度覆盖也小得多，等于把 depth 6
			// 以上整段让了出去。
			if lmrDepth < 10 {
				fv := staticEval + 129*lmrDepth + 319
				if staticEval > alpha {
					// 静态评估已经高于 alpha 时放宽余量：这种节点的着法更可能
					// 被接受，剪掉的收益小于风险。
					fv += 112
				}
				if fv <= alpha {
					// fail-soft：被剪的这一层也交回一个有信息量的上界，
					// 而不是让调用方以为这里毫无价值。
					if false && best < fv && fv < MateScore-MaxPly && best > -MateScore+MaxPly {
						best = fv
					}
					skip = true
				}
			}
			// Late Move Pruning：着法已按强弱排序，越靠后越不可能好。
			//
			// 阈值 (3+depth²)/(2-improving)：局势在改善时静态评估更可信，
			// 阈值减半、剪得更狠；否则保守。阈值随 depth² 增长，
			// 深层自然几乎不触发，所以不需要额外的深度上限。
			if !skip {
				limit := 3 + depth*depth
				if !improving {
					limit /= 2
				}
				if i >= limit {
					skip = true
				}
			}
		}

		// 静的着法的 SEE 剪枝：这步交换下来会白丢子，而静态评估看不出这点
		// （futility/LMP 是按位置与静态值判断的）。放在 futility/LMP 之后，
		// 免得给本该被免费剪掉的着法也付一遍 SEE 的成本。
		//
		// **必须在落子之前算。** SeeGE 读的是 from/to 两格上当前的子：落子后
		// from 已空、to 上站着自己的子，于是它的「第二层捷径」（我方动用的子
		// 不比 swap 大就直接成立）恒成立，剪枝一次都不会触发 —— 实测把调用
		// 写在 Make 之后时，8.7 万次调用剪掉 0 次，节点数逐位不变。
		//
		// 阈值照抄皮卡鱼：`see_ge(move, -35 * lmrDepth²)`，lmrDepth 是**削减后**
		// 的有效深度（夹紧到 ≥0）—— 有效深度越大越不该剪，所以允许 SEE 越亏。
		// 注意因此它只在 lmrDepth 小（浅层）时才咬得住：阈值到了 −1715，任何子
		// 都在预算内，剪不动。皮卡鱼也是这个性质。
		if !skip && seeQuietEnabled && !isPV && !inCheck && quiet {
			skip = !p.SeeGE(int(m.From), int(m.To), -35*lmrDepth*lmrDepth)
		}

		// 吃子的 SEE 剪枝（皮卡鱼 Step 13）：这步交换净亏太多就直接跳过。
		// 阈值照抄皮卡鱼 `-256*depth`（它还有一项吃子历史 `captHist*34/1024`，
		// 本项目没有吃子历史，省略 ⇒ 略保守）。
		//
		// ⚠️ 与静的那一处同理，**必须在落子之前算**：落子后 from 已空、to 上
		// 是自己的子，SeeGE 的「第二层捷径」恒成立，一次都剪不到（这个坑踩过，
		// 见上面静的着法那处的注释）。
		//
		// 额外挂 `seeQuietEnabled`：SEE 在本项目实测**成本大于收益**（见该开关处
		// 的记录），而这里是「每个吃子都调一次」，比静的那一处更贵。单独打开
		// `QIJING_CAPP` 时只吃 futility（便宜），要吃子 SEE 得再开 `QIJING_SEEQ`。
		if !skip && capPrune && seeQuietEnabled && !isPV && !inCheck && !quiet {
			skip = !p.SeeGE(int(m.From), int(m.To), -256*depth)
		}

		// 奇异延伸（皮卡鱼 Step 14）。**必须在落子之前**：验证搜索要的是
		// 「拿掉这个着法之后」的局面，落子后再调用就得先撤回去。
		//
		// 这段会让树变大（正向）或变小（多切/负延伸），两个方向都要有，
		// 否则要么爆炸要么白花。实测数据见 seEnabled 的注释。
		ext := 0
		if seEnabled && !skip && !hasExcluded && ply > 0 &&
			(m.From != 0 || m.To != 0) && m == ttMove && ttHit &&
			(ttFlag == ttLower || ttFlag == ttExact) && ttDepth >= depth-3 &&
			ttScore > -MateScore+MaxPly && ttScore < MateScore-MaxPly &&
			depth >= 5+boolToInt(ttPvSeen) && !s.isShuffling(p, m, ply) {
			// 余量随深度线性增长；ttPv 时放宽（皮的 `44 + 72*(ttPv && !PvNode)`）。
			singularBeta := ttScore - (44+72*boolToInt(ttPvSeen && !isPV))*depth/69
			singularDepth := (depth - 1) / 2
			s.seTriggers++
			s.excludedMove = m
			v := s.alphaBeta(p, singularDepth, singularBeta-1, singularBeta, ply, false, true, cutNode)
			s.excludedMove = game.Move{}
			notDecisive := v > -MateScore+MaxPly && v < MateScore-MaxPly
			ttCapture := p.PieceAt90(int(m.To)) != game.Empty
			// 两个附加位都来自皮卡鱼，本引擎缺对应机制而省略：
			// corrValAdj（我们没有 correction history）与
			// ttMoveHistory（我们只按着法统计历史，不按局面+着法）。
			doubleMargin := -4 + 234*boolToInt(isPV) - 172*boolToInt(!ttCapture) - 43*boolToInt(ply > 0)
			tripleMargin := 106 + 299*boolToInt(isPV) - 263*boolToInt(!ttCapture) +
				93*boolToInt(ttPvSeen) - 60*boolToInt(ply > 0)
			switch {
			case v < singularBeta:
				// 拿掉 ttMove 后掉到 singularBeta 以下 —— 它是唯一好手，值得加深。
				// 掉得越多越确定，最多 3 层（皮卡的 double/triple margin）。
				ext = 1
				if v < singularBeta-doubleMargin {
					ext++
				}
				if v < singularBeta-tripleMargin {
					ext++
				}
				if ext > seMaxExt {
					ext = seMaxExt
				}
				s.seExtended++
			case v >= beta && notDecisive:
				// 拿掉 ttMove 仍然 fail high ⇒ 不止一个着法够好，这个结点必被截断，
				// 整棵子树都可以剪掉（多切剪枝）。将杀分除外：那是「找到杀」，
				// 直接把杀分当成「评估远超 beta」返回会掩盖真实的杀棋距离。
				s.seMultiCut++
				return v
			case ttScore >= beta || cutNode:
				// 既判不出奇异、又不能多切 —— 说明 ttMove 未必最好，
				// 削减它、把预算让给别的着法（负延伸）。
				//
				// 皮卡鱼的条件是 `ttData.value >= beta || cutNode`，cutNode 是
				// 真实参数；本引擎的 cutNode 只在此处消费，传递规则与皮卡一致。
				ext = -3
				s.seNegExt++
			}
		}

		p.Make(m)
		s.makes++
		s.pos.Make(int(m.From), int(m.To))

		// 将军豁免对 SEE 与前向剪枝都生效：令对手被将的安静着法常含杀机，
		// 按「会白丢子」或静态评估剪掉都会漏杀（这条教训在 futility 上
		// 已经踩过一次 —— 曾把 depth 6 的将杀剪没）。这也是唯一需要落子
		// 才能回答的问题，所以放在 Make 之后。
		givesCheck := p.InCheck(p.Turn)
		// 吃子的 futility 剪枝（皮卡鱼 Step 13 的另一半）：静态评估加上余量与
		// 被吃子的价值仍够不到 alpha，这个吃子不可能成为最佳着法。
		//
		// 余量照抄皮卡鱼 `322 + 336*lmrDepth`（它还有一项吃子历史，我们省略）。
		//
		// ⚠️ 必须在落子之后：皮卡的条件是 `!givesCheck`，而「这步是不是将军」
		// 只有落子后才算得准（本项目没有 `gives_check(move)` 的预计算表）。
		// 代价是被剪掉的吃子也要付一次 Make/Unmake，但省下的是整棵子树。
		if !skip && capPrune && !s.noForward && !isPV && !quiet && best > -Infinity &&
			beta < MateScore-MaxPly && alpha > -MateScore+MaxPly &&
			!givesCheck && lmrDepth < 19 &&
			// ⚠️ `victim` 是**带颜色位**的棋子编码（0..15），必须过 `TypeOf`
			// 才能当下标用（本项目其它处都这么写；直接下标会在吃掉黑子时越界）。
			staticEval+322+336*lmrDepth+capPieceValue[game.TypeOf(victim)] <= alpha {
			skip = true
		}
		if skip && givesCheck {
			skip = false
		}
		if skip {
			p.Unmake()
			s.pos.Unmake()
			continue
		}

		// 本步的实际搜索深度：基础一层减去削减、再加上延伸（皮卡的
		// `newDepth = depth - 1; newDepth += extension`）。
		newDepth := depth - 1 + ext

		var score int
		if i == 0 {
			// PV 子结点恒为 cutNode=false；非 PV 子结点取反（皮卡的传递规则）。
			childCut := !isPV && !cutNode
			score = -s.alphaBeta(p, newDepth, -beta, -alpha, ply+1, isPV, true, childCut)
		} else if lmrBound || lmr2 {
			// 有界重搜：削减搜索之后**不跳回全深**，只在 newDepth 上下浮动一层。
			d := newDepth - red
			childCut := !cutNode
			if lmr2 {
				// 皮卡：`max(1, min(newDepth - r/1024, newDepth + 2)) + PvNode`
				d = newDepth - r1024/1024
				if d < 1 {
					d = 1
				}
				if d > newDepth+2 {
					d = newDepth + 2
				}
				if isPV {
					d++
				}
				// 皮卡的削减搜索恒以 cutNode=true 进入（`search<NonPV>(..., d, true)`）。
				childCut = true
			} else {
				if d < 1 {
					d = 1
				}
				if d > newDepth+2 {
					d = newDepth + 2
				}
			}
			score = -s.alphaBeta(p, d, -alpha-1, -alpha, ply+1, false, true, childCut)
			if score > alpha && (red > 0 || r1024 > 0) {
				// 60 / 9 两个阈值照抄皮卡鱼：明显更好才加深一层，不够好就减一层。
				nd := newDepth
				if d < newDepth && score > best+60 {
					nd++
				}
				if score < best+9 {
					nd--
				}
				if nd > d {
					score = -s.alphaBeta(p, nd, -alpha-1, -alpha, ply+1, false, true, !cutNode)
				}
			}
			if score > alpha && score < beta {
				score = -s.alphaBeta(p, newDepth, -beta, -alpha, ply+1, isPV, true, false)
			}
		} else {
			score = -s.alphaBeta(p, newDepth-red, -alpha-1, -alpha, ply+1, false, true, !cutNode)
			if score > alpha && red > 0 {
				score = -s.alphaBeta(p, newDepth, -alpha-1, -alpha, ply+1, false, true, !cutNode)
			}
			if score > alpha && score < beta {
				score = -s.alphaBeta(p, newDepth, -beta, -alpha, ply+1, isPV, true, false)
			}
		}
		p.Unmake()
		s.pos.Unmake()

		if score > best {
			best, bestMove = score, m
		}
		if score > alpha {
			alpha = score
		}
		if alpha >= beta {
			if diagTier {
				// 必须在 updateQuietStats 之前分类：否则杀手与历史已被这一步改过，
				// 统计到的是「改后」的档位。
				ti := s.moveTier(m, ply, ttMove, victim)
				tierDiag[ti].cnt++
				tierDiag[ti].idxSum += int64(i)
			}
			if victim == game.Empty {
				s.updateQuietStats(m, ply, depth)
			}
			s.cutoffs++
			s.cutoffIdxSum += int64(i)
			if i == 0 {
				s.cutoffFirst++
			}
			break
		}
	}

	if s.stopped() {
		return best // 中止时不要写表：半途的分值会污染后续搜索
	}
	flag := ttExact
	if best <= origAlpha {
		flag = ttUpper
	} else if best >= beta {
		flag = ttLower
	}
	s.storeCnt++
	// 排除模式下不写表：这里是「拿掉 ttMove 之后」的分值，写进去会把该局面
	// 的真实表项覆盖掉（皮卡同样以 `!excludedMove` 保护）。
	if !hasExcluded {
		s.ttStore(p.Key, bestMove, scoreToTT(best, ply), depth, flag, isPV)
	}
	return best
}

// reduction 计算后期着法削减量（LMR）。返回 0 表示不削减。
//
// 只削减“安静着法”（非吃子）：吃子着法价值高、容错低，削减容易漏掉战术。
// 被将军时不削减，否则会漏掉唯一的解将着法。
//
// **线性式 `1 + index/8` 是实测选出来的，不是随手写的。** 曾怀疑它比经典
// 对数式 `0.5 + ln(depth)·ln(index)/1.95` 保守、树因此偏大，实测三种公式的
// 有效分支因子（3 个中局，depth 8→10）：
//
//	线性（本实现） 1.79      对数式 1.85      max(线性, 对数) 2.44
//
// 削减**更多**反而树更大 —— 因为下面调用处是「浅层试探通过（score > alpha）
// 就用全深重搜」，削过头会让更多着法在浅层通过、触发更多全深重搜，多出来的
// 节点超过削减省下的。要再动这个公式，先量这三项，别只凭对数式更“标准”。
func (s *Searcher) reduction(depth, index int, victim byte, inCheck bool) int {
	if depth < 3 || index < 4 || victim != game.Empty || inCheck {
		return 0
	}
	red := 1 + index/8
	if red > depth-2 {
		red = depth - 2
	}
	if red < 0 {
		red = 0
	}
	return red
}

// quiesce 是静态搜索：只展开吃子，消除“评估在吃子中途截断”的地平线效应。
//
// 被将军时改用全宽搜索 —— 此时只搜吃子会漏掉解将着法，把被杀误判为安全。
func (s *Searcher) quiesce(p *game.Position, alpha, beta, ply int) int {
	if s.stopped() {
		return 0
	}
	s.nodes++
	if diagQ {
		qdQNodes++
	}
	if ply >= MaxPly-1 || ply >= maxQuiescePly {
		return s.evaluate(p)
	}
	if p.RepetitionCount() > 1 {
		return 0
	}

	inCheck := p.InCheck(p.Turn)
	if diagQ && inCheck {
		qdInCheck++
	}

	best := -Infinity
	if !inCheck {
		stand := s.evaluate(p)
		if stand >= beta {
			return stand
		}
		if stand > alpha {
			alpha = stand
		}
		best = stand
	}

	moves := p.LegalMoves(p.Turn)
	if len(moves) == 0 {
		return -MateScore + ply
	}

	if !inCheck {
		captures := moves[:0]
		maxVictim := 0
		for _, m := range moves {
			v := p.PieceAt90(int(m.To))
			if v == game.Empty {
				continue
			}
			// 坏吃子剪枝：静态交换评估为亏的吃子直接丢弃 —— 它只会白送子，
			// 展开它等于把搜索预算花在确定的损失上。
			//
			// 判据用 SeeGE(...,0) 而不是精确的 SEE 值：只需知道亏不亏。
			// 注意 maxVictim 仍统计全部吃子（含被剪掉的那些），delta pruning
			// 的意图是「这一格最值钱能有多少」，与留不留它无关。
			if !seeEnabled || s.noSEE || p.SeeGE(int(m.From), int(m.To), 0) {
				captures = append(captures, m)
			}
			if val := pieceValue[game.TypeOf(v)]; val > maxVictim {
				maxVictim = val
			}
		}
		moves = captures
		if len(moves) == 0 {
			return best
		}
		// delta pruning：即使吃掉这一格里最值钱的子也追不上 alpha，
		// 这一支就不可能优于已有选择，直接返回。这是静态搜索最便宜
		// 也最有效的剪枝之一 —— 缺了它，大量毫无希望的吃子链会被展开。
		if best+maxVictim+deltaMargin < alpha {
			if diagQ {
				qdDeltaCut++
			}
			return best
		}
	}

	s.orderMoves(p, moves, ply, game.Move{})

	for _, m := range moves {
		p.Make(m)
		s.makes++
		s.pos.Make(int(m.From), int(m.To))
		score := -s.quiesce(p, -beta, -alpha, ply+1)
		p.Unmake()
		s.pos.Unmake()

		if score > best {
			best = score
		}
		if score > alpha {
			alpha = score
		}
		if alpha >= beta {
			break
		}
	}
	return best
}

// evaluate 返回走子方视角的 NNUE 评估值。
//
// 用增量同步（SyncTo）而非全量重建：累加器自己记住上次的特征集合，
// 走一步通常只改变几个特征，实测比全量 Refresh 快约 4 倍。
//
// 顺带统计双方非兵子力数（null-move 的 zugzwang 保护要用），
// 反正这里已经遍历全盘，单独再扫一遍不值。
func (s *Searcher) evaluate(_ *game.Position) int {
	// 子力计数由增量局面直接给出（O(1)），不必再扫全盘。
	s.nonPawn[0] = s.pos.NonPawnCount(0)
	s.nonPawn[1] = s.pos.NonPawnCount(1)
	s.w.Apply(&s.pos, &s.acc)
	return int(s.w.EvalValueAt(&s.pos, &s.acc))
}

// orderMoves 给着法打分并降序排列（TT 着法 > 吃子 MVV-LVA > 杀手 > 历史启发）。
func (s *Searcher) orderMoves(p *game.Position, moves []game.Move, ply int, ttMove game.Move) {
	if ply >= MaxPly {
		return
	}
	if s.scoreBuf[ply] == nil {
		s.scoreBuf[ply] = make([]int, 0, 128)
	}
	scores := s.scoreBuf[ply][:0]

	for _, m := range moves {
		scores = append(scores, s.moveScore(p, m, ply, ttMove))
	}
	s.scoreBuf[ply] = scores

	// 插入排序：着法数不大，且基本有序时接近线性。
	for i := 1; i < len(moves); i++ {
		mv, sv := moves[i], scores[i]
		j := i - 1
		for j >= 0 && scores[j] < sv {
			moves[j+1], scores[j+1] = moves[j], scores[j]
			j--
		}
		moves[j+1], scores[j+1] = mv, sv
	}
}

// moveScore 是单步着法的排序分。各档之间留出足够间隔，避免互相穿插。
func (s *Searcher) moveScore(p *game.Position, m game.Move, ply int, ttMove game.Move) int {
	if m == ttMove && ttMove != (game.Move{}) {
		return 1 << 24
	}
	victim := p.PieceAt90(int(m.To))
	if victim != game.Empty {
		// MVV-LVA：吃大子优先，用小子吃更优先（减少被反吃损失）。
		return (1 << 20) + pieceValue[game.TypeOf(victim)]*16 - pieceValue[game.TypeOf(p.PieceAt90(int(m.From)))]
	}
	if m == s.killers[ply][0] {
		return (1 << 19) + 1
	}
	if m == s.killers[ply][1] {
		return 1 << 19
	}
	return s.history[m.From][m.To]
}

// updateQuietStats 在安静着法引发 beta 截断时更新杀手与历史启发。
func (s *Searcher) updateQuietStats(m game.Move, ply, depth int) {
	if m != s.killers[ply][0] {
		s.killers[ply][1] = s.killers[ply][0]
		s.killers[ply][0] = m
	}
	bonus := depth * depth
	if v := s.history[m.From][m.To] + bonus; v > historyMax {
		s.history[m.From][m.To] = historyMax
	} else {
		s.history[m.From][m.To] = v
	}
}

// ageHistory 把历史分值减半。迭代加深时调用，让上一层的经验逐渐淡出。
func (s *Searcher) ageHistory() {
	for i := range s.history {
		for j := range s.history[i] {
			s.history[i][j] /= 2
		}
	}
}

// reverseFutilityMargin 是反 futility 剪枝允许的评估余量（照皮卡鱼 Step 7）。
//
// 余量随深度增长得比线性慢：futilityMult 从 depth 1 的 44 长到 129 后封顶
// （depth ≥ 23）。再按 improving / opponentWorsening 各减一档 —— 局势正在
// 改善、或对手刚走差时静态评估更可信，敢多剪；两项同时成立最多减掉
// 2.785×mult，浅层足以让余量变成负数。
//
// 原来用的是固定 120*depth（更早版本自定），实测那是一条**曲率不对**的线：
// 反 futility 的触发点里平均深度只有 1.5，而 120 在 depth 1 就要求评估高出
// beta 120，浅层几乎剪不动；到 depth 20 又比皮卡鱼宽松。换成这里之后安静局面
// 节点减少 49%、固定 6 万节点的平均深度 12.30 → 14.55，且与「关掉前向剪枝」
// 的参照相比平均分值差从 71 降到 56 —— 是剪得更准，不是更狠。
func reverseFutilityMargin(depth int, improving, oppWorse bool) int {
	mult := 40 + depth*4
	if mult > 129 {
		mult = 129
	}
	margin := mult * depth
	adj := 0
	if improving {
		adj += 2512
	}
	if oppWorse {
		adj += 340
	}
	return margin - adj*mult/1024
}
