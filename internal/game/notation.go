package game

import (
	"strconv"
	"strings"
)

// 中文记谱生成（A12）。pos 为走子前的局面。
//
// 规则要点：
//   - 纵线号：红方用汉字一~九、从红方右侧数起（file 8 → 一）；黑方用数字 1~9、从红方左侧数起；
//   - 同纵线存在同类同色棋子 ≥2 时加"前/中/后"前缀并省略纵线号（兵最多 3 子同线用前中后，
//     >3 子属极端排局，按位置以 前/二/三/后 命名）；
//   - 直线子（车炮帅兵）：横走"平+目标纵线"，直走"进/退+格数"（红方 rank 增大为进）；
//   - 斜走子（马相仕）："进/退+目标纵线"。

var cnNum = [...]string{"一", "二", "三", "四", "五", "六", "七", "八", "九"}

var pieceName = [2][8]string{
	{"", "帅", "仕", "相", "马", "车", "炮", "兵"}, // 红
	{"", "将", "士", "象", "马", "车", "炮", "卒"}, // 黑
}

func fileStr(color, f int) string {
	if color == Red {
		return cnNum[9-f-1] // 红方从右侧数起：file 0 → 九
	}
	return strconv.Itoa(f + 1) // 黑方从红方左侧数起：file 0 → 1
}

func numStr(color, n int) string {
	if color == Red {
		return cnNum[n-1]
	}
	return strconv.Itoa(n)
}

// MoveToChinese 生成着法 m 的标准中文记谱。
func (p *Position) MoveToChinese(m Move) string {
	pc := p.Board[m.From]
	color := ColorOf(pc)
	typ := TypeOf(pc)
	f, r := FileOf256(int(m.From)), RankOf256(int(m.From))
	tf, tr := FileOf256(int(m.To)), RankOf256(int(m.To))

	name := pieceName[color>>3][typ]

	// 同线同类子前缀
	prefix := ""
	var same []int // 同纵线同类同色子的 rank 列表
	for rr := 0; rr < 10; rr++ {
		if p.Board[SQ256(f, rr)] == pc {
			same = append(same, rr)
		}
	}
	if len(same) >= 2 {
		// 排序：靠近对方底线者为"前"。same 按 rank 升序收集，
		// 红方"前"= rank 最大（需倒序），黑方"前"= rank 最小（天然有序）。
		if color == Red {
			for i, j := 0, len(same)-1; i < j; i, j = i+1, j-1 {
				same[i], same[j] = same[j], same[i]
			}
		}
		idx := 0
		for i, rr := range same {
			if rr == r {
				idx = i
				break
			}
		}
		switch len(same) {
		case 2:
			if idx == 0 {
				prefix = "前"
			} else {
				prefix = "后"
			}
		case 3:
			prefix = []string{"前", "中", "后"}[idx]
		default: // 4~5 子同线：前/二/三/后（近似，极端排局）
			names := make([]string, len(same))
			names[0], names[len(same)-1] = "前", "后"
			for i := 1; i < len(same)-1; i++ {
				names[i] = numStr(color, i) // 中间子按序号
			}
			prefix = names[idx]
		}
	}

	var action string
	forward := tr > r // 红方 rank 增大为进
	if color == Black {
		forward = tr < r
	}
	dir := "退"
	if forward {
		dir = "进"
	}
	if typ == Rook || typ == Cannon || typ == King || typ == Pawn {
		if tf == f { // 直走
			action = dir + numStr(color, abs(tr-r))
		} else if tr == r { // 平移
			action = "平" + fileStr(color, tf)
		} else { // 兵横走必然同行；此处兜底
			action = "平" + fileStr(color, tf)
		}
	} else { // 马相仕：斜走，进/退 + 目标纵线
		action = dir + fileStr(color, tf)
	}

	if prefix != "" {
		return prefix + name + action
	}
	return name + fileStr(color, f) + action
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// LegalMovesChinese 返回全部合法着法的 (着法, 中文记谱) 列表，供 LLM 生成-匹配解析（A13）使用。
func (p *Position) LegalMovesChinese() []struct {
	Move Move
	Cn   string
} {
	moves := p.LegalMoves(p.Turn)
	out := make([]struct {
		Move Move
		Cn   string
	}, 0, len(moves))
	for _, m := range moves {
		out = append(out, struct {
			Move Move
			Cn   string
		}{m, p.MoveToChinese(m)})
	}
	return out
}

// NormalizeCN 中文着法串归一化：去空白、全角→半角、繁→简（常用字）。
func NormalizeCN(s string) string {
	repl := strings.NewReplacer(
		" ", "", "\t", "", "\n", "", "　", "",
		"０", "0", "１", "1", "２", "2", "３", "3", "４", "4",
		"５", "5", "６", "6", "７", "7", "８", "8", "９", "9",
		"將", "将", "士", "仕", "象", "相", "車", "车", "砲", "炮",
		"俥", "车", "傌", "马", "馬", "马", "卒", "卒",
	)
	return repl.Replace(s)
}
