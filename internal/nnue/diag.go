package nnue

import "fmt"

// 威胁增量链路的规模统计。
//
// 起因：把 slidingAttackBoth 改成空实现后，固定节点预算的搜索从 4.6s 掉到
// 1.64s。但它自身的开销远不足以解释这个量级 —— 空实现同时掐断了整条下游链：
// pendingThreats 不再增长，applyThreats 的 1024 通道 add/sub 与 ThreatIndex
// 的 4.1MB 随机访存一并消失。
//
// 所以「某段代码占多少时间」不能只看它本身，要看它牵动的整条链。
// 这些计数器把链条摊开：产生了多少条目、apply 多少次、窗口多长、
// 有没有频繁触发全量重建。
type DiagStats struct {
	SlidingCalls  int64 // slidingAttackBoth 调用次数
	RayAttackCall int64 // computeRay 段里 attacksBB 调用次数

	// computeRay 候选循环的规模 —— 用来判断「换成 pass 表」值不值得。
	// 2026-09-17 起候选循环已改用预计算表 rayPassBB/leaperPassBB：
	// 每次候选从「2 次含阻挡搜索的攻击计算」降到「1 次查表 + 2 个与」，
	// 固定节点基准快约 6.9%（中局 20 局面）。这几个计数器现在用来监控
	// 候选规模有没有变化 —— 它随在场子数增长，是这条链路的主要成本驱动。
	RayCalls   int64 // 进入 computeRay 段的次数
	CandRook   int64 // 车候选迭代数
	CandCannon int64 // 炮候选迭代数（最贵，2 次 slidingAttackDir）
	CandLeaper int64 // 马/象候选迭代数（2 次全量计算）
	ThreatOut  int64 // 发出的威胁条目数
	ThreatIn   int64 // 指向 s 的威胁条目数

	ApplyCalls    int64 // Apply 调用次数（每个评估点一次）
	ApplyWindow   int64 // 每次 Apply 时 pendingThreats 长度之和
	MaxWindow     int   // pendingThreats 峰值长度
	RebuildThreat int64 // rebuildThreats 调用次数（每个视角一次）
	RebuildFeat   int64 // rebuildThreats 枚举到的威胁特征总数
	RebuildPSQ    int64 // 未走增量路径的视角次数（整表重建 + 缓存差集）
	RebuildPiece  int64 // rebuildPSQ 累加的棋子特征总数
	ApplyEntries  int64 // applyThreats 实际消费的条目总数
	ApplyPieces   int64 // applyPSQ 实际消费的棋子条目总数

	// 全量重建的触发原因分解。
	//
	// 重建（rebuildPSQ + rebuildThreats）实测占全机 12.7%，是特征链路里
	// 除 applyThreats 之外最大的一块。但它是不是「可省的」取决于触发原因：
	// 首帧确实是设计代价，脏窗口超限与 pendingLimit 有关；
	// ⚠️ 而桶/镜像变化**不是**设计代价 —— PSQ 侧已由 (桶, 镜像) 缓存
	// 化解（见 psqcache.go），看 PSQCacheHit/Miss 判断它有没有起作用。
	// 没有这张分解表就没法判断该往哪使劲。
	RebStale    int64 // 局面被标记为脏（stale）
	RebInvalid  int64 // 累加器尚未建立（首帧）
	RebBucket   int64 // 特征桶变化（王换了桶）
	RebMirror   int64 // 镜像变化（跨中线）
	RebPieceWin int64 // PSQ 侧棋子脏窗口超限
	RebThrWin   int64 // 威胁侧脏窗口超限

	// PSQ 的 (桶, 镜像) 缓存命中情况（见 psqcache.go）。
	// 命中率低说明搜索里桶变化太散，缓存没起作用。
	PSQCacheHit  int64 // 命中缓存、只补差集
	PSQCacheMiss int64 // 未命中，整表重建

	// 配对与零头的分布：PairRows 是合成一趟的行对，SingleRows 是只能单行走的行。
	// 两者之比决定「还要不要为某个方向补一个内核循环体」。
	SingleRows int64

	// 零头按方向细分。能成对的零头只有两种形状：
	//   - 两个「加」→ 内核的 add+add 模式（威胁侧已实现，见 thrPairSameAdd）
	//   - 两个「减」→ 需要内核再加一个 `acc -= w1 + w2` 的循环体（尚未实现）
	// 这两个计数是「还值不值得补循环体」的唯一依据：单行的加若凑够成对的量，
	// 就该补；减若一直是奇数个，补了也没用（永远只剩一个）。
	SingleAddRows int64
	SingleSubRows int64

	// updateThreats 的实际枚举次数与被 suppressDirty 跳过的次数。
	//
	// Unmake 在「回滚段用不上」时会置 suppressDirty，让整轮枚举直接返回 ——
	// 那是已经吃到的免费路径。这两个数的比例决定「让 Unmake 复用 Make 的条目」
	// 这条改造还有多少空间：跳过比例越高，剩下的可省部分越小。
	UtEnumerated int64
	UtSuppressed int64

	// UtUnmakeKept 是 Unmake 里 keepRollback 为真（即必须重新枚举威胁、
	// 不能走 suppressDirty 免费路径）的次数。
	// 如果它接近 UtEnumerated 的一半，说明 Unmake 的枚举正是整条链的主要成本 ——
	// 那么「把 Make 的条目翻转复用」就有接近一半 updateThreats 的空间。
	UtUnmakeKept int64

	// UtReplayed 是 Unmake 里用「翻转 Make 的条目」替代枚举的次数。
	// 它应当接近 UtUnmakeKept 中「无吃子」的那部分；明显偏低说明本层没留到副本
	// （比如脏信息超限被丢弃），退回了枚举。
	UtReplayed int64

	// 两行合一内核（addRows2I16AVX2）真正处理的配对数。
	//
	// 存在的意义是给正确性测试一个「没测到而通过」的挡板：融合与单行两条路径
	// 产出的累加器逐位相同，所以如果融合压根没被走到，对拍测试会毫无理由地通过。
	// 这条计数把「有没有走到」变成可断言的事实。
	FusePairs int64
}

// rebPSQ 分类记录 PSQ 侧走全量重建的原因（只在 diagOn 时调用）。
func rebPSQ(stale bool, a *Accumulator, c, bucket int, mirror bool) {
	switch {
	case stale:
		diagStats.RebStale++
	case !a.valid[c]:
		diagStats.RebInvalid++
	case a.psqBucket[c] != int8(bucket):
		diagStats.RebBucket++
	case a.mirror[c] != mirror:
		diagStats.RebMirror++
	default:
		diagStats.RebPieceWin++
	}
}

// rebThreat 分类记录威胁侧走全量重建的原因（威胁侧没有桶条件）。
func rebThreat(stale bool, a *Accumulator, c int, mirror bool) {
	switch {
	case stale:
		diagStats.RebStale++
	case !a.valid[c]:
		diagStats.RebInvalid++
	case a.mirror[c] != mirror:
		diagStats.RebMirror++
	default:
		diagStats.RebThrWin++
	}
}

// RebuildReasonReport 返回重建原因的简短分布（供诊断测试打印）。
func (s DiagStats) RebuildReasonReport() string {
	tot := s.RebStale + s.RebInvalid + s.RebBucket + s.RebMirror + s.RebPieceWin + s.RebThrWin
	if tot == 0 {
		return "（无重建）"
	}
	pct := func(v int64) float64 { return 100 * float64(v) / float64(tot) }
	return fmt.Sprintf("stale %.1f%%｜首帧 %.1f%%｜桶变 %.1f%%｜镜像变 %.1f%%｜PSQ窗口 %.1f%%｜威胁窗口 %.1f%%（共 %d 次）",
		pct(s.RebStale), pct(s.RebInvalid), pct(s.RebBucket), pct(s.RebMirror),
		pct(s.RebPieceWin), pct(s.RebThrWin), tot)
}

// RebuildShare 返回威胁累加器走全量重建的比例（0~1）。
// 分母是「Apply 次数 × 2」—— 每个 Apply 对两个视角各做一次选择。
func (s DiagStats) RebuildShare() float64 {
	views := 2 * s.ApplyCalls
	if views == 0 {
		return 0
	}
	return float64(s.RebuildThreat) / float64(views)
}

// ApplyViewShare 返回走增量路径的视角比例。
func (s DiagStats) ApplyViewShare() float64 { return 1 - s.RebuildShare() }

// AvgRebuildFeat 返回每次全量重建枚举到的威胁特征数。
func (s DiagStats) AvgRebuildFeat() float64 {
	if s.RebuildThreat == 0 {
		return 0
	}
	return float64(s.RebuildFeat) / float64(s.RebuildThreat)
}

// diagOn 关闭时各调用点只多一次布尔判断，不影响微基准。
var diagOn = false

var diagStats DiagStats

// EnableDiag 开关统计。仅供测试与诊断工具使用。
func EnableDiag(on bool) { diagOn = on }

// ResetDiag 清零统计。
func ResetDiag() { diagStats = DiagStats{} }

// DiagSnapshot 返回当前累计值。
func DiagSnapshot() DiagStats { return diagStats }

// PendingLimit 返回走增量路径的脏条目上限（超过则全量重建）。
func PendingLimit() int { return pendingLimit }
