// Package puzzle 残局库：加载、检索（不含答案对外）。
package puzzle

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"sync"
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

func (s *Store) add(p *Puzzle) {
	s.puzzles = append(s.puzzles, p)
	s.byID[p.ID] = p
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
			Goal: p.Goal, ParMoves: p.ParMoves, Tags: p.Tags,
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
