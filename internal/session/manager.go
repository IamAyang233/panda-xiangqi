package session

import (
	"log"
	"sync"
	"time"
)

// Manager 会话存储与生命周期管理。
type Manager struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

// NewManager 创建管理器并启动过期清理。
func NewManager() *Manager {
	m := &Manager{sessions: make(map[string]*Session)}
	go m.gcLoop()
	return m
}

func (m *Manager) gcLoop() {
	ticker := time.NewTicker(10 * time.Minute)
	for range ticker.C {
		m.mu.Lock()
		for id, s := range m.sessions {
			// 无人连接且闲置超过 2 小时的会话回收
			if time.Since(s.Created()) > 2*time.Hour && s.idle() {
				s.Close()
				delete(m.sessions, id)
				log.Printf("session: 回收过期会话 %s", id)
			}
		}
		m.mu.Unlock()
	}
}

// idle 报告会话当前是否无连接。
func (s *Session) idle() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns) == 0
}

// Put 存入会话。
func (m *Manager) Put(s *Session) {
	m.mu.Lock()
	m.sessions[s.ID] = s
	m.mu.Unlock()
}

// Get 按 ID 取会话。
func (m *Manager) Get(id string) (*Session, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[id]
	return s, ok
}

// Count 当前会话数。
func (m *Manager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.sessions)
}
