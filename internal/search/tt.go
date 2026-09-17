package search

import "github.com/IamAyang233/panda-xiangqi/internal/game"

// 置换表项的标志位，描述存下来的分值与真实值的关系。
const (
	ttNone  uint8 = iota
	ttExact       // 精确值：搜索窗口未被截断
	ttLower       // 下界：发生了 beta 截断，真实值 >= 存值
	ttUpper       // 上界：未超过 alpha，真实值 <= 存值
)

// flag 字段的低 2 位是上面这四种「界限」，第 2 位单独放 PV 标记。
//
// 这么塞是为了**不改变 ttEntry 的 16 字节布局**（一个缓存行正好 4 项，
// 4 字节的 padding 换一个 bool 不划算）。代价是所有读 flag 的地方都要
// 用 bound() 掩掉 PV 位 —— 直接拿 e.flag 去 switch 会全部落空。
const (
	ttFlagMask uint8 = 0x03
	ttPVBit    uint8 = 0x04
)

// bound 返回不含附加位的「界限」。
func (e ttEntry) bound() uint8 { return e.flag & ttFlagMask }

// isPV 返回该表项是否来自 PV 结点。奇异延伸要用它判断「这棵子树是不是
// 在主变例上」，从而决定触发深度门槛与验证余量。
func (e ttEntry) isPV() bool { return e.flag&ttPVBit != 0 }

// ttEntry 固定 16 字节（8+4+2+1+1），一个缓存行放 4 项。
//
// key 存完整 Zobrist 键而非只存高位：索引已经由低位决定，
// 存全键可以用一次比较确认命中，不必再做额外校验。
type ttEntry struct {
	key   uint64
	score int32
	move  uint16 // From | To<<8；0 表示无着法（a0→a0 非法，可作哨兵）
	depth uint8  // 保留层数，0~255 足够（MaxPly = 128）
	flag  uint8
}

// TranspositionTable 是按 2 的幂取模的直接映射表，无锁。
type TranspositionTable struct {
	entries []ttEntry
	mask    uint64
}

// DefaultTTSizeMB 是默认表大小。上限按内存定：16 MiB 可放约 100 万项。
const DefaultTTSizeMB = 16

// NewTranspositionTable 分配约 sizeMB 兆字节的表，容量向下取到 2 的幂。
func NewTranspositionTable(sizeMB int) *TranspositionTable {
	if sizeMB < 1 {
		sizeMB = 1
	}
	n := (sizeMB << 20) / 16
	p := 1
	for p*2 <= n {
		p *= 2
	}
	return &TranspositionTable{entries: make([]ttEntry, p), mask: uint64(p - 1)}
}

// Clear 清空整表（换局或长思考开始前调用）。
func (t *TranspositionTable) Clear() {
	for i := range t.entries {
		t.entries[i] = ttEntry{}
	}
}

// Len 返回可容纳的项数。
func (t *TranspositionTable) Len() int { return len(t.entries) }

// probe 查询命中项。
func (t *TranspositionTable) probe(key uint64) (ttEntry, bool) {
	e := t.entries[key&t.mask]
	if e.bound() != ttNone && e.key == key {
		return e, true
	}
	return ttEntry{}, false
}

// store 写入一项。
//
// 覆盖策略：同键时只在「新深度不更浅」或「新值是精确值」时覆盖；
// 异键时只要槽位里的旧项不更深就覆盖。这样深搜索结果不会被浅搜索冲掉 ——
// 迭代加深下浅层搜索频繁且几乎必然命中同键，放任覆盖会让 TT 失去作用。
// ttAlwaysReplace 是诊断开关：为真时 store 无条件覆盖目标槽位，
// 用来对比「深度优先替换」是否把深旧项锁死在槽里、导致新项进不来。
// 生产构建恒为 false。
var ttAlwaysReplace = false

func (t *TranspositionTable) store(key uint64, move game.Move, score int32, depth int, flag uint8, pv bool) {
	idx := key & t.mask
	stored := flag
	if pv {
		stored |= ttPVBit
	}
	if ttAlwaysReplace {
		t.entries[idx] = ttEntry{
			key:   key,
			score: score,
			move:  encodeMove(move),
			depth: uint8(depth),
			flag:  stored,
		}
		return
	}
	old := t.entries[idx]
	if old.key == key && old.depth > uint8(depth) && flag != ttExact {
		return
	}
	if old.key != key && old.bound() != ttNone && old.depth > uint8(depth) {
		return
	}
	t.entries[idx] = ttEntry{
		key:   key,
		score: score,
		move:  encodeMove(move),
		depth: uint8(depth),
		flag:  stored,
	}
}

func encodeMove(m game.Move) uint16 { return uint16(m.From) | uint16(m.To)<<8 }

func decodeMove(v uint16) game.Move {
	return game.Move{From: uint8(v & 0xff), To: uint8(v >> 8)}
}

// scoreToTT / scoreFromTT 把将杀分值换算成与 ply 无关的形式再入表。
//
// 存 "MateScore - ply" 会让同一局面的分值随搜索路径漂移，命中后给出错误结果。
// 加入 ply 抵消：入表时 +ply，取出时 -ply，还原到当前节点的距离语义。
func scoreToTT(score, ply int) int32 {
	if score > MateScore-MaxPly {
		return int32(score + ply)
	}
	if score < -MateScore+MaxPly {
		return int32(score - ply)
	}
	return int32(score)
}

func scoreFromTT(score int32, ply int) int {
	v := int(score)
	if v > MateScore-MaxPly {
		return v - ply
	}
	if v < -MateScore+MaxPly {
		return v + ply
	}
	return v
}
