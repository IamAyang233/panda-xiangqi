package nnue

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
	RebuildPSQ    int64 // rebuildPSQ 调用次数
	RebuildPiece  int64 // rebuildPSQ 累加的棋子特征总数
	ApplyEntries  int64 // applyThreats 实际消费的条目总数
	ApplyPieces   int64 // applyPSQ 实际消费的棋子条目总数
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
