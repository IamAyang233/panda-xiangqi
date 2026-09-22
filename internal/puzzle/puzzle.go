// Package puzzle 残局库：加载、检索（不含答案对外）。
package puzzle

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/IamAyang233/panda-xiangqi/internal/game"
)

// Puzzle 残局数据（计划书 §4.5）。
type Puzzle struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Source     string   `json:"source,omitempty"`
	Difficulty string   `json:"difficulty"` // 入门/初级/中级/高级/大师
	PlayerSide string   `json:"playerSide"`
	Goal       string   `json:"goal"` // win | draw
	FEN        string   `json:"fen"`
	ParMoves   int      `json:"parMoves"`
	Solution   []string `json:"solution,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	Verified   bool     `json:"verified,omitempty"`
}

// Public 对外视图（不含答案）。
type Public struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Source     string   `json:"source,omitempty"`
	Difficulty string   `json:"difficulty"`
	PlayerSide string   `json:"playerSide"` // red | black（玩家执子方，用于黑先关）
	Goal       string   `json:"goal"`
	ParMoves   int      `json:"parMoves"`
	Tags       []string `json:"tags,omitempty"`
}

// Store 残局库。
type Store struct {
	mu      sync.RWMutex
	puzzles []*Puzzle
	byID    map[string]*Puzzle
}

// NewStore 从目录（.json 文件集合）或内嵌 FS 加载残局。
func NewStore(fsys fs.FS) (*Store, error) {
	s := &Store{byID: map[string]*Puzzle{}}
	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !(len(path) > 5 && path[len(path)-5:] == ".json") {
			return nil
		}
		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			return err
		}
		var list []*Puzzle
		if err := json.Unmarshal(data, &list); err != nil { // 数组格式
			var one Puzzle
			if err2 := json.Unmarshal(data, &one); err2 != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			list = []*Puzzle{&one}
		}
		for _, p := range list {
			if p.ID == "" {
				return fmt.Errorf("%s: 残局缺少 id", path)
			}
			// 语义校验：FEN 语法合法还不够，局面本身必须能安全进入对局路径。
			// 「非行棋方被将军」的摆局会让走子方可以吃将，而吃将会让 kingSq 悬空、
			// 整套合法性判定失真（详见 game.LegalPosition 的说明）。内置题库里
			// 实测有这种条目（是数据缺陷，不是解析问题），外置目录更可能撞上。
			//
			// 策略：**跳过并告警**而不是整体加载失败 —— 一关数据坏掉不该让
			// 整个残局库打不开。
			if pos, err := game.ParseFEN(p.FEN); err != nil {
				log.Printf("puzzle: %s 跳过（FEN 无法解析: %v）", p.ID, err)
				continue
			} else if err := pos.LegalPosition(); err != nil {
				log.Printf("puzzle: %s 跳过（局面非法: %v）", p.ID, err)
				continue
			}
			s.add(p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(s.puzzles, func(i, j int) bool { return s.puzzles[i].ID < s.puzzles[j].ID })
	return s, nil
}

// LoadDir 从磁盘目录加载（优先于内嵌，便于用户自定义残局）。
func LoadDir(dir string) (*Store, error) {
	if dir == "" {
		return nil, fmt.Errorf("目录为空")
	}
	if _, err := os.Stat(dir); err != nil {
		return nil, err
	}
	return NewStore(os.DirFS(dir))
}

// NewEmpty 构造一个空库（自定义残局目录不存在 / 不可写时降级为内存态使用）。
func NewEmpty() *Store {
	return &Store{byID: map[string]*Puzzle{}}
}

func (s *Store) add(p *Puzzle) {
	s.puzzles = append(s.puzzles, p)
	s.byID[p.ID] = p
}

// Add 内存追加一条残局，并按与加载时相同的口径校验局面（FEN 可解析 + 局面合法）。
// ID 重复直接报错：自定义残局的 id 是文件名，冲突会覆盖已有条目。
func (s *Store) Add(p *Puzzle) error {
	if p == nil {
		return fmt.Errorf("残局为空")
	}
	if p.ID == "" {
		return fmt.Errorf("残局缺少 id")
	}
	if pos, err := game.ParseFEN(p.FEN); err != nil {
		return fmt.Errorf("FEN 无法解析: %w", err)
	} else if err := pos.LegalPosition(); err != nil {
		return fmt.Errorf("局面非法: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[p.ID]; ok {
		return fmt.Errorf("残局 %s 已存在", p.ID)
	}
	s.add(p)
	return nil
}

// Remove 摘除一条残局。不存在返回 false，调用方据此区分「删过了」与「本来就没有」。
func (s *Store) Remove(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[id]; !ok {
		return false
	}
	delete(s.byID, id)
	for i, p := range s.puzzles {
		if p.ID == id {
			s.puzzles = append(s.puzzles[:i], s.puzzles[i+1:]...)
			break
		}
	}
	return true
}

// List 按难度列出（difficulty 为空返回全部）。
func (s *Store) List(difficulty string) []Public {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []Public{}
	for _, p := range s.puzzles {
		if difficulty != "" && p.Difficulty != difficulty {
			continue
		}
		out = append(out, Public{
			ID: p.ID, Name: p.Name, Source: p.Source, Difficulty: p.Difficulty,
			PlayerSide: p.PlayerSide, Goal: p.Goal, ParMoves: p.ParMoves, Tags: p.Tags,
		})
	}
	return out
}

// Get 取完整残局（含答案，仅内部使用）。
func (s *Store) Get(id string) (*Puzzle, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.byID[id]
	return p, ok
}

// Count 残局总数。
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.puzzles)
}

// All 返回全部残局（工具/校验用）。
func (s *Store) All() []*Puzzle {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]*Puzzle(nil), s.puzzles...)
}

// ---------------------------------------------------------------- 落盘

// SaveToDir 把一条残局写入目录（文件名 <id>.json），采用「临时文件 + rename」的原子写：
// 断电或进程被杀时只可能留下临时文件，不会留下半截 JSON 让下次启动的加载器读到脏数据。
func SaveToDir(dir string, p *Puzzle) error {
	if dir == "" {
		return fmt.Errorf("目录为空")
	}
	if p == nil || p.ID == "" {
		return fmt.Errorf("残局缺少 id")
	}
	// 目录必须已存在：由启动流程创建，这里不静默 MkdirAll，避免把写权限问题藏起来。
	if st, err := os.Stat(dir); err != nil {
		return fmt.Errorf("目录不可用: %w", err)
	} else if !st.IsDir() {
		return fmt.Errorf("目录不是一个文件夹: %s", dir)
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化失败: %w", err)
	}
	data = append(data, '\n')
	tmp := filepath.Join(dir, p.ID+".json.tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("写入失败: %w", err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, p.ID+".json")); err != nil {
		// rename 失败时清理临时文件，避免下次加载把它当成残局（扩展名不匹配故不会被加载，
		// 但仍应清掉，不为排查留垃圾）。
		_ = os.Remove(tmp)
		return fmt.Errorf("落盘失败: %w", err)
	}
	return nil
}

// RemoveFromDir 删除目录里某条残局的磁盘文件。
// 文件已不存在（例如被手工删过）时**不算错误**：调用方仍可据此摘除内存索引，
// 否则「磁盘丢了但内存还在」会留下一个点开就 404 的幽灵条目。
func RemoveFromDir(dir string, id string) error {
	if dir == "" {
		return fmt.Errorf("目录为空")
	}
	if id == "" {
		return fmt.Errorf("残局 id 为空")
	}
	path := filepath.Join(dir, id+".json")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("删除失败: %w", err)
	}
	return nil
}

// DirWritable 目录是否可写（用于降级时如实向外报告，而不是静默变成"保存了其实没保存"）。
func DirWritable(dir string) bool {
	if dir == "" {
		return false
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return false
	}
	f, err := os.CreateTemp(dir, ".write-check-")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	_ = os.Remove(name)
	return true
}
