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
