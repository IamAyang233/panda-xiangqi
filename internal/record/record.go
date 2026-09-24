// Package record 棋谱：一局一 JSON 的落盘、索引与列表。
//
// 与 internal/puzzle 的存储**刻意不复用同一个 Store**：残局的 Add 语义是
// 「重复 id 报错」，而棋谱的保存由用户在结束弹窗上点按钮触发，连点、超时重试、
// 断线重发都会重复到达同一局 —— 语义必须是「同 id 覆盖」，否则连点两次要么报错、
// 要么在列表里留下两份。id 直接用对局 id，覆盖即幂等。
//
// 落盘原语（原子写 / 幂等删 / 可写探测）与残局共用 internal/filestore。
package record

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/IamAyang233/panda-xiangqi/internal/filestore"
	"github.com/IamAyang233/panda-xiangqi/internal/game"
)

// Move 一手棋。与 session.MoveRecord 同构，但不依赖 session 包。
type Move struct {
	UCI string `json:"uci"`
	CN  string `json:"cn"`
	Red bool   `json:"red"` // 红方所走
	// Captured 被吃子的 FEN 字符（无吃子为空）。供着法列表标注与回放校验，
	// 重放本身不依赖它（from→to 覆盖落点即可）。
	Captured string `json:"captured,omitempty"`
}

// 关键手标签。
const (
	TagCapture = "capture" // 吃子（含吃回、兑子）
	TagCheck   = "check"   // 将军
	TagMate    = "mate"    // 终局那一手（将死/困毙）
	TagBlunder = "blunder" // 引擎判定的大漏着
	TagMistake = "mistake" // 引擎判定的疑问手
)

// KeyMove 复盘标出的关键手（落盘缓存，避免每次打开详情都重算一遍引擎）。
type KeyMove struct {
	Index int    `json:"index"` // 在 Moves 中的下标
	Tag   string `json:"tag"`
	// Delta 引擎视角的损失（仅 blunder/mistake 有值）：越大越亏。
	Delta int `json:"delta,omitempty"`
	// BestUCI 引擎建议的更好着法（仅 blunder/mistake 有值）。
	BestUCI string `json:"bestUci,omitempty"`
}

// Record 一局棋谱。
type Record struct {
	// ID 直接等于对局 id：同一局重复保存只会覆盖同一个文件。
	ID        string `json:"id"`
	Name      string `json:"name"`
	Mode      string `json:"mode"`            // engine | llm | local_2p
	Level     int    `json:"level,omitempty"` // 人机档位
	Model     string `json:"model,omitempty"` // 大模型名；**绝不写入 apiKey**
	HumanSide string `json:"humanSide"`       // red | black（双人模式为红）
	StartFEN  string `json:"startFen"`        // 重放必需
	Moves     []Move `json:"moves"`
	Result    string `json:"result"` // red_win | black_win | draw
	Reason    string `json:"reason"`
	// Created 用本地时间字符串：列表按它倒序即时间序，人工翻 JSON 也一眼能读。
	Created string `json:"created"`
	// Analysis / Reviews 是复盘缓存（第一次打开详情时算、问过 AI 后写回）。
	Analysis []KeyMove `json:"analysis,omitempty"`
	// Analyzed 是否已经跑过关键手分析。
	//
	// 必须显式记一个标记，不能拿「Analysis 非空」代替：对没有关键手的干净对局，
	// 分析结果本身就是空数组（且 omitempty 让它不落盘），于是「分析过但没找到」
	// 与「还没分析」在数据上分不开 —— 结果是每次打开详情都重跑一遍整局引擎浅搜。
	Analyzed bool              `json:"analyzed,omitempty"`
	Reviews  map[string]string `json:"reviews,omitempty"` // "手数下标" → 讲解文本
}

// Summary 列表视图：不含 moves/analysis/reviews。
// 列表只要目录信息 —— 一页 50 条若都带上着法，响应会白胖几十倍，而这些内容
// 只有点进详情才用得上（与残局列表隐藏答案同一思路）。
type Summary struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Mode      string `json:"mode"`
	Level     int    `json:"level,omitempty"`
	Model     string `json:"model,omitempty"`
	HumanSide string `json:"humanSide"`
	Result    string `json:"result"`
	Reason    string `json:"reason"`
	MoveCount int    `json:"moveCount"`
	Created   string `json:"created"`
	Analyzed  bool   `json:"analyzed"`      // 是否已算过关键手
	Reviewed  int    `json:"reviewedCount"` // 已有讲解的手数
}

func (r *Record) summary() Summary {
	return Summary{
		ID: r.ID, Name: r.Name, Mode: r.Mode, Level: r.Level, Model: r.Model,
		HumanSide: r.HumanSide, Result: r.Result, Reason: r.Reason,
		MoveCount: len(r.Moves), Created: r.Created,
		Analyzed: r.Analyzed, Reviewed: len(r.Reviews),
	}
}

// AutoName 自动命名一局棋谱（用户不用起名字；之后想改名再说）。
func AutoName(mode string, level int, model string, at time.Time) string {
	stamp := at.Format("01-02 15:04")
	switch mode {
	case "engine":
		return fmt.Sprintf("人机 · 第 %d 档 · %s", level, stamp)
	case "llm":
		if model == "" {
			model = "大模型"
		}
		return fmt.Sprintf("大模型 · %s · %s", model, stamp)
	case "local_2p":
		return "双人 · " + stamp
	default:
		return mode + " · " + stamp
	}
}

// validate 记录的基本自洽性：id、起始局面可解析、有结果、每手 uci 合法。
//
// 刻意**不做**整局重放校验：那是每条记录 O(手数) 的工作，放在启动加载路径上
// 会让库变大后启动变慢；格式层面挡掉明显坏数据即可（FEN 能不能重放由前端回放时
// 与复盘分析时自然暴露）。
func validate(r *Record) error {
	if r == nil || r.ID == "" {
		return fmt.Errorf("棋谱缺少 id")
	}
	if _, err := game.ParseFEN(r.StartFEN); err != nil {
		return fmt.Errorf("起始局面无法解析: %w", err)
	}
	if r.Result == "" {
		return fmt.Errorf("棋谱缺少结果")
	}
	// 结束原因必须存在：复盘里「终局那一手」的判定依赖它（不满足时终局手会带着极值分差
	// 被误标成失误），手改过的文件在这里就该被拦下。
	if r.Reason == "" {
		return fmt.Errorf("棋谱缺少结束原因")
	}
	for i, m := range r.Moves {
		if _, ok := game.MoveFromUCI(m.UCI); !ok {
			return fmt.Errorf("第 %d 手的 uci 不合法: %q", i+1, m.UCI)
		}
	}
	return nil
}

// ---------------------------------------------------------------- 存储

// Store 棋谱库（内存索引；磁盘由 SaveToDir/RemoveFromDir 维护）。
type Store struct {
	mu      sync.RWMutex
	records []*Record
	byID    map[string]*Record
}

// NewStore 从目录（.json 文件集合）或内嵌 FS 加载。
//
// 与残局库同样的策略：**单条坏数据只跳过它自己**。棋谱目录是用户可碰的目录
// （手改、同步、断电），一条坏文件不该让整个棋谱库打不开。
func NewStore(fsys fs.FS) (*Store, error) {
	s := &Store{byID: map[string]*Record{}}
	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !(len(path) > 5 && path[len(path)-5:] == ".json") {
			return nil
		}
		data, rerr := fs.ReadFile(fsys, path)
		if rerr != nil {
			log.Printf("record: %s 跳过（读取失败: %v）", path, rerr)
			return nil
		}
		var list []*Record
		if err := json.Unmarshal(data, &list); err != nil { // 数组格式（兼容手工合并的文件）
			var one Record
			if err2 := json.Unmarshal(data, &one); err2 != nil {
				log.Printf("record: %s 跳过（JSON 无法解析: %v）", path, err)
				return nil
			}
			list = []*Record{&one}
		}
		for _, r := range list {
			if err := validate(r); err != nil {
				log.Printf("record: %s 跳过（%v）", path, err)
				continue
			}
			s.add(r)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s, nil
}

// LoadDir 从磁盘目录加载。
func LoadDir(dir string) (*Store, error) {
	if dir == "" {
		return nil, fmt.Errorf("目录为空")
	}
	if _, err := os.Stat(dir); err != nil {
		return nil, err
	}
	return NewStore(os.DirFS(dir))
}

// NewEmpty 构造空库（目录不可用时降级为内存态使用）。
func NewEmpty() *Store { return &Store{byID: map[string]*Record{}} }

func (s *Store) add(r *Record) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.byID[r.ID]; ok {
		for i, x := range s.records {
			if x == old {
				s.records[i] = r
				break
			}
		}
	} else {
		s.records = append(s.records, r)
	}
	s.byID[r.ID] = r
}

// Add 写入或覆盖（upsert）。重复 id 覆盖而不是报错 —— 见包注释。
func (s *Store) Add(r *Record) error {
	if err := validate(r); err != nil {
		return err
	}
	s.add(r)
	return nil
}

// Update 在锁内取最新快照、按 mutate 修改后整体替换。
//
// 为什么必须有它：analyze 与 review 都是「Get 快照 → 长耗时计算（几秒~几十秒）→
// 复制后 Add 整体覆盖」。两条路径各持**计算开始前**的快照，谁后写谁赢，会把对方刚写进去
// 的结果整条抹掉（实测：用户点「问 AI 讲解」期间分析完成并落盘，讲解结束时用旧快照覆盖，
// Analysis 丢失、摘要 analyzed 又变回 false）。这里把「读最新 → 合并 → 替换」放进同一把锁，
// 让两条路径只合并各自的字段。
func (s *Store) Update(id string, mutate func(*Record)) (*Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.byID[id]
	if !ok {
		return nil, false
	}
	next := *old // 浅拷贝；Moves 只读共享
	// Reviews 是 map，浅拷贝会与旧对象共享，必须复制一份再交给 mutate
	if old.Reviews != nil {
		next.Reviews = make(map[string]string, len(old.Reviews)+1)
		for k, v := range old.Reviews {
			next.Reviews[k] = v
		}
	}
	mutate(&next)
	for i, x := range s.records {
		if x == old {
			s.records[i] = &next
			break
		}
	}
	s.byID[id] = &next
	return &next, true
}

// Remove 摘除索引；返回是否原本存在。
func (s *Store) Remove(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.byID[id]
	if !ok {
		return false
	}
	delete(s.byID, id)
	for i, x := range s.records {
		if x == r {
			s.records = append(s.records[:i], s.records[i+1:]...)
			break
		}
	}
	return true
}

// Get 取一条（返回内部指针；调用方只读）。
func (s *Store) Get(id string) (*Record, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.byID[id]
	return r, ok
}

// Count 条数。
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.records)
}

// List 列表视图，按时间倒序（同时间按 id 倒序，保证顺序稳定）。
func (s *Store) List() []Summary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Summary, 0, len(s.records))
	for _, r := range s.records {
		out = append(out, r.summary())
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Created != out[j].Created {
			return out[i].Created > out[j].Created
		}
		return out[i].ID > out[j].ID
	})
	return out
}

// ---------------------------------------------------------------- 磁盘

// SaveToDir 写入 dir 下的 <id>.json（原子写）。
func SaveToDir(dir string, r *Record) error {
	if r == nil || r.ID == "" {
		return fmt.Errorf("棋谱缺少 id")
	}
	return filestore.SaveJSON(dir, r.ID, r)
}

// RemoveFromDir 删除 dir 下的 <id>.json（文件已不存在不算错）。
func RemoveFromDir(dir, id string) error {
	if id == "" {
		return fmt.Errorf("棋谱 id 为空")
	}
	return filestore.RemoveJSON(dir, id)
}

// DirWritable 目录是否可写（供 /api/status 如实上报降级状态）。
func DirWritable(dir string) bool { return filestore.DirWritable(dir) }
