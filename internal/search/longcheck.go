package search

import (
	"os"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
)

// longCheckEnabled 决定搜索的重复判定是否考虑「长将方判负」。
//
// 默认开启；`QIJING_LC=off` 关闭 —— 给同一份二进制内的交替 A/B 提供两态
// （本机有快慢相，跨进程比较不可靠，见 paired_ab_test.go）。
//
// 背景：规则上长将方判负（`game.CheckStatus` ⇒ `ReasonLongCheck`），降级引擎
// `engine.SimpleEngine` 也实现了它，**但搜索此前没有** —— 它在 ply>0 时把任何
// 重复一律当和棋（返回 0）。于是搜索对这类局面的估值与对局裁决不一致：
//
//	① 引擎主动走进「自己长将被判负」的线，却以为那是和棋；
//	② 错失「对方长将被判负」（对引擎是胜）的局面。
//
// 阈值取 **>= 3 次重复**，与 `status.go` 的规则判定一致，而不是沿用搜索惯用的
// 「二次重复即判和」：二次重复仍走 0 分，原有启发式不变 —— 这样这次改动只在
// 规则真正生效的那一档上生效，对搜索树的影响面最小。
var longCheckEnabled = os.Getenv("QIJING_LC") != "off"

// SetLongCheck 切换长将感知并返回原值，供测试与基准使用。
func SetLongCheck(on bool) bool {
	old := longCheckEnabled
	longCheckEnabled = on
	return old
}

// longCheckTriggers 统计「长将判负分支真的被走到」的次数。
//
// 两件事的依据：①守卫里断言 > 0，防「新分支一次都没被走到而对拍照样通过」；
// ②判断这次改动会不会动到搜索树 —— 在标准语料上它是 0 就说明树逐位不变。
var longCheckTriggers int64

// LongCheckTriggers 返回长将分支的触发次数。
func LongCheckTriggers() int64 { return longCheckTriggers }

// ResetLongCheckTriggers 清零触发计数器。
func ResetLongCheckTriggers() { longCheckTriggers = 0 }

// repetitionScore 返回「当前局面已构成重复」时该给的分值，从**当前走子方**视角。
//
//	普通重复        → 0（和棋）
//	三次重复且长将  → 长将方判负 ⇒ 长将方 −Mate、对手 +Mate
//
// 带 ply 是为了偏好更快的判定，与搜索里其它将杀分的约定一致。
func repetitionScore(p *game.Position, ply int) int {
	if longCheckEnabled && p.RepetitionCount() >= 3 {
		if winner, ok := p.LongCheckWinner(); ok {
			longCheckTriggers++
			if (winner == game.ResultRedWin) == (p.Turn == game.Red) {
				return MateScore - ply // 当前走子方是长将方的对手 ⇒ 胜
			}
			return -MateScore + ply // 当前走子方就是长将方 ⇒ 负
		}
	}
	return 0
}
