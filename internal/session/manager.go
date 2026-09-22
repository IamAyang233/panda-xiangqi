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

// maxSessions 是在场会话数上限。
//
// 没有上限的话，`POST /api/games` 每调一次就多一个常驻内存的 Session（含局面、
// 着法列表、连接表），创建速度快于 GC 回收速度时可以被无限堆高；而下面的清理
// 要求「创建满 2 小时**且**零连接」，一个「连上但不玩」的客户端靠心跳保活就永远
// 不会被回收。上限取 200：远超任何真实使用量，同时给内存一个硬边界。
const maxSessions = 200

// idleGrace 是「无连接多久算闲置」。清理同时要求创建满 gcAge。
const idleGrace = 30 * time.Minute

// gcAge 是「会话存活多久后才考虑回收」。设得比 idleGrace 长，
// 避免刚创建、用户还在选模式的会话被误杀。
const gcAge = 2 * time.Hour

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
			// 存活够久且长时间无连接 ⇒ 回收。
			// 「无连接」用 idleGrace 而不是「此刻零连接」：TCP 半开/客户端崩溃时
			// 连接可能长期不被注销，只看瞬时状态会让这类会话永久占位。
			if time.Since(s.Created()) > gcAge && s.idleFor(idleGrace) {
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

// idleFor 报告会话是否已连续 d 时长没有任何连接。
//
// 判据是「最后一次有连接的时刻」而不是「此刻连接数为 0」：客户端断网/进程被杀时
// 连接不会立刻注销，看瞬时值会让这类会话一直看起来「有人在用」。
func (s *Session) idleFor(d time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.conns) > 0 {
		return false
	}
	return time.Since(s.lastActive) > d
}

// Put 存入会话。超出上限时淘汰「最旧且空闲」的会话，避免无界增长。
func (m *Manager) Put(s *Session) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.sessions) >= maxSessions {
		m.evictLocked()
	}
	m.sessions[s.ID] = s
}

// evictLocked 淘汰一个最旧且当前无连接的会话。调用方持 m.mu。
//
// 只淘汰空闲会话：正在被使用的会话即使数量到了上限也不动 —— 用户的对局被
// 后台清掉是远比内存紧张更糟的体验。若全都忙碌，则本次不淘汰（宁可短暂超限）。
func (m *Manager) evictLocked() {
	var oldest *Session
	var oldestID string
	for id, s := range m.sessions {
		if !s.idle() {
			continue
		}
		if oldest == nil || s.Created().Before(oldest.Created()) {
			oldest, oldestID = s, id
		}
	}
	if oldest == nil {
		log.Printf("session: 会话数达上限 %d 且全部在用，暂不淘汰", maxSessions)
		return
	}
	oldest.Close()
	delete(m.sessions, oldestID)
	log.Printf("session: 会话数达上限，淘汰最旧空闲会话 %s", oldestID)
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
