// Package protocols provides UDP session management with LRU eviction and timeout cleanup.
package protocols

import (
	"container/list"
	"net"
	"sync"
	"time"

	"github.com/buhuipao/anyproxy/pkg/common/utils"
	"github.com/buhuipao/anyproxy/pkg/logger"
)

const (
	// DefaultMaxUDPSessions is the default maximum number of UDP sessions
	DefaultMaxUDPSessions = 10000
	// DefaultUDPSessionTimeout is the default session timeout
	DefaultUDPSessionTimeout = 5 * time.Minute
	// DefaultCleanupInterval is the interval between cleanup runs
	DefaultCleanupInterval = 30 * time.Second
)

// UDPSessionManager manages UDP sessions with LRU eviction and timeout cleanup
type UDPSessionManager struct {
	mu sync.RWMutex

	// Primary index: sessionKey -> *list.Element (for O(1) access and LRU)
	sessions map[string]*list.Element

	// Secondary index: TCP address -> sessionKey (for quick lookup by TCP reference)
	tcpIndex map[string]string

	// LRU list: front = most recently used, back = least recently used
	lruList *list.List

	// Configuration
	maxSessions    int
	sessionTimeout time.Duration

	// Cleanup control
	stopCleanup chan struct{}
	cleanupWg   sync.WaitGroup
	stopOnce    sync.Once // 🛡️ Ensure stopCleanup is only closed once
}

// sessionEntry is stored in the LRU list
type sessionEntry struct {
	key     string
	session *UDPSession
}

// NewUDPSessionManager creates a new UDP session manager
func NewUDPSessionManager(maxSessions int, sessionTimeout time.Duration) *UDPSessionManager {
	if maxSessions <= 0 {
		maxSessions = DefaultMaxUDPSessions
	}
	if sessionTimeout <= 0 {
		sessionTimeout = DefaultUDPSessionTimeout
	}

	mgr := &UDPSessionManager{
		sessions:       make(map[string]*list.Element),
		tcpIndex:       make(map[string]string),
		lruList:        list.New(),
		maxSessions:    maxSessions,
		sessionTimeout: sessionTimeout,
		stopCleanup:    make(chan struct{}),
	}

	// Start background cleanup goroutine
	mgr.cleanupWg.Add(1)
	go mgr.cleanupLoop()

	logger.Info("UDP session manager started", "max_sessions", maxSessions, "timeout", sessionTimeout)
	return mgr
}

// Store stores a UDP session with the given key
func (m *UDPSessionManager) Store(key string, session *UDPSession) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if session already exists
	if elem, exists := m.sessions[key]; exists {
		// Update existing session and move to front
		entry := elem.Value.(*sessionEntry)
		entry.session = session
		m.lruList.MoveToFront(elem)
		logger.Debug("UDP session updated", "key", key, "conn_id", session.ConnID)
		return
	}

	// Evict LRU sessions if at capacity
	for m.lruList.Len() >= m.maxSessions {
		m.evictOldestLocked()
	}

	// Add new session
	entry := &sessionEntry{
		key:     key,
		session: session,
	}
	elem := m.lruList.PushFront(entry)
	m.sessions[key] = elem

	logger.Debug("UDP session stored", "key", key, "conn_id", session.ConnID, "total_sessions", m.lruList.Len())
}

// StoreTCPRef stores a session with a TCP reference key for later lookup
func (m *UDPSessionManager) StoreTCPRef(tcpKey, sessionKey string, session *UDPSession) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if session already exists
	if elem, exists := m.sessions[sessionKey]; exists {
		entry := elem.Value.(*sessionEntry)
		entry.session = session
		m.lruList.MoveToFront(elem)
		return
	}

	// Evict if at capacity
	for m.lruList.Len() >= m.maxSessions {
		m.evictOldestLocked()
	}

	// Store session
	entry := &sessionEntry{
		key:     sessionKey,
		session: session,
	}
	elem := m.lruList.PushFront(entry)
	m.sessions[sessionKey] = elem

	// Store TCP reference
	m.tcpIndex[tcpKey] = sessionKey

	logger.Debug("UDP session stored with TCP ref", "tcp_key", tcpKey, "session_key", sessionKey, "conn_id", session.ConnID)
}

// Load loads a UDP session by key and updates LRU
func (m *UDPSessionManager) Load(key string) (*UDPSession, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	elem, exists := m.sessions[key]
	if !exists {
		return nil, false
	}

	// Move to front (most recently used)
	m.lruList.MoveToFront(elem)
	entry := elem.Value.(*sessionEntry)

	return entry.session, true
}

// LoadOrAdoptTCPSession tries to load a session by key, or adopts a TCP-referenced session
func (m *UDPSessionManager) LoadOrAdoptTCPSession(key string, srcAddr *net.UDPAddr) (*UDPSession, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// First, try direct lookup
	if elem, exists := m.sessions[key]; exists {
		m.lruList.MoveToFront(elem)
		entry := elem.Value.(*sessionEntry)
		return entry.session, true
	}

	// Try to find and adopt a TCP-referenced session
	for tcpKey, sessionKey := range m.tcpIndex {
		elem, exists := m.sessions[sessionKey]
		if !exists {
			// Clean up stale index entry
			delete(m.tcpIndex, tcpKey)
			continue
		}

		entry := elem.Value.(*sessionEntry)
		session := entry.session

		// RFC 1928 section 7: a session only accepts datagrams from the IP
		// of its TCP control connection.
		if session.ClientAddr == nil || !session.ClientAddr.IP.Equal(srcAddr.IP) {
			continue
		}

		// Adopt this session with the new UDP address
		session.ClientAddr = srcAddr

		// Remove old key, add new key
		delete(m.sessions, sessionKey)
		delete(m.tcpIndex, tcpKey)

		// Update entry key and re-add
		entry.key = key
		m.sessions[key] = elem
		m.lruList.MoveToFront(elem)

		logger.Debug("Adopted TCP-referenced UDP session", "old_key", sessionKey, "new_key", key, "conn_id", session.ConnID)
		return session, true
	}

	return nil, false
}

// Delete deletes a session by key
func (m *UDPSessionManager) Delete(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.deleteLocked(key)
}

// deleteLocked deletes a session (caller must hold lock)
func (m *UDPSessionManager) deleteLocked(key string) {
	elem, exists := m.sessions[key]
	if !exists {
		return
	}

	entry := elem.Value.(*sessionEntry)
	m.lruList.Remove(elem)
	delete(m.sessions, key)

	// Clean up TCP index if present
	for tcpKey, sessionKey := range m.tcpIndex {
		if sessionKey == key {
			delete(m.tcpIndex, tcpKey)
			break
		}
	}

	logger.Debug("UDP session deleted", "key", key, "conn_id", entry.session.ConnID)
}

// evictOldestLocked evicts the oldest session (caller must hold lock)
func (m *UDPSessionManager) evictOldestLocked() {
	back := m.lruList.Back()
	if back == nil {
		return
	}

	entry := back.Value.(*sessionEntry)
	logger.Info("Evicting UDP session (LRU)", "key", entry.key, "conn_id", entry.session.ConnID)

	// Close tunnel connections before eviction
	entry.session.tunnelConns.Range(func(_, value any) bool {
		if info, ok := value.(*tunnelConnInfo); ok {
			// 🛡️ Use closeOnce to prevent double-close panic
			info.closeOnce.Do(func() {
				if info.stopChan != nil {
					close(info.stopChan)
				}
			})
			if info.conn != nil {
				_ = info.conn.Close()
			}
		}
		return true
	})

	m.lruList.Remove(back)
	delete(m.sessions, entry.key)

	// Clean up TCP index
	for tcpKey, sessionKey := range m.tcpIndex {
		if sessionKey == entry.key {
			delete(m.tcpIndex, tcpKey)
			break
		}
	}
}

// Range iterates over all sessions (for cleanup operations)
func (m *UDPSessionManager) Range(fn func(key string, session *UDPSession) bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for elem := m.lruList.Front(); elem != nil; elem = elem.Next() {
		entry := elem.Value.(*sessionEntry)
		if !fn(entry.key, entry.session) {
			break
		}
	}
}

// UpdateLastActive updates the last active time for a session
func (m *UDPSessionManager) UpdateLastActive(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	elem, exists := m.sessions[key]
	if !exists {
		return
	}

	entry := elem.Value.(*sessionEntry)
	entry.session.LastActive = time.Now()
	m.lruList.MoveToFront(elem)
}

// Len returns the number of sessions
func (m *UDPSessionManager) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lruList.Len()
}

// cleanupLoop runs the periodic cleanup of expired sessions
func (m *UDPSessionManager) cleanupLoop() {
	defer m.cleanupWg.Done()

	ticker := time.NewTicker(DefaultCleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopCleanup:
			logger.Debug("UDP session cleanup loop stopped")
			return
		case <-ticker.C:
			m.cleanupExpiredSessions()
		}
	}
}

// cleanupExpiredSessions removes sessions that have exceeded the timeout
func (m *UDPSessionManager) cleanupExpiredSessions() {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	var expiredKeys []string

	// Find expired sessions from the back (oldest first)
	for elem := m.lruList.Back(); elem != nil; elem = elem.Prev() {
		entry := elem.Value.(*sessionEntry)
		if now.Sub(entry.session.LastActive) > m.sessionTimeout {
			expiredKeys = append(expiredKeys, entry.key)
		} else {
			// Sessions are ordered by activity, so we can stop early
			break
		}
	}

	// Clean up expired sessions
	for _, key := range expiredKeys {
		elem, exists := m.sessions[key]
		if !exists {
			continue
		}

		entry := elem.Value.(*sessionEntry)
		logger.Info("Cleaning up expired UDP session", "key", key, "conn_id", entry.session.ConnID,
			"last_active", entry.session.LastActive, "timeout", m.sessionTimeout)

		// Close tunnel connections
		entry.session.tunnelConns.Range(func(_, v any) bool {
			if info, ok := v.(*tunnelConnInfo); ok {
				// 🛡️ Use closeOnce to prevent double-close panic
				info.closeOnce.Do(func() {
					if info.stopChan != nil {
						close(info.stopChan)
					}
				})
				if info.conn != nil {
					_ = info.conn.Close()
				}
			}
			return true
		})

		m.lruList.Remove(elem)
		delete(m.sessions, key)

		// Clean up TCP index
		for tcpKey, sessionKey := range m.tcpIndex {
			if sessionKey == key {
				delete(m.tcpIndex, tcpKey)
				break
			}
		}
	}

	if len(expiredKeys) > 0 {
		logger.Info("UDP session cleanup completed", "expired_count", len(expiredKeys), "remaining_sessions", m.lruList.Len())
	}
}

// Stop stops the session manager and cleans up all sessions
func (m *UDPSessionManager) Stop() {
	// 🛡️ Use sync.Once to prevent double-close panic
	m.stopOnce.Do(func() {
		close(m.stopCleanup)
	})
	m.cleanupWg.Wait()

	m.mu.Lock()
	defer m.mu.Unlock()

	// Clean up all sessions
	for elem := m.lruList.Front(); elem != nil; elem = elem.Next() {
		entry := elem.Value.(*sessionEntry)
		entry.session.tunnelConns.Range(func(_, v any) bool {
			if info, ok := v.(*tunnelConnInfo); ok {
				// 🛡️ Use closeOnce to prevent double-close panic
				info.closeOnce.Do(func() {
					if info.stopChan != nil {
						close(info.stopChan)
					}
				})
				if info.conn != nil {
					_ = info.conn.Close()
				}
			}
			return true
		})
	}

	m.sessions = make(map[string]*list.Element)
	m.tcpIndex = make(map[string]string)
	m.lruList = list.New()

	logger.Info("UDP session manager stopped")
}

// GetStats returns statistics about the session manager
func (m *UDPSessionManager) GetStats() map[string]interface{} {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var oldestAge, newestAge time.Duration
	now := time.Now()

	if m.lruList.Len() > 0 {
		front := m.lruList.Front().Value.(*sessionEntry)
		back := m.lruList.Back().Value.(*sessionEntry)
		newestAge = now.Sub(front.session.LastActive)
		oldestAge = now.Sub(back.session.LastActive)
	}

	return map[string]interface{}{
		"total_sessions":     m.lruList.Len(),
		"max_sessions":       m.maxSessions,
		"tcp_refs":           len(m.tcpIndex),
		"oldest_session_age": oldestAge.String(),
		"newest_session_age": newestAge.String(),
		"session_timeout":    m.sessionTimeout.String(),
	}
}

// Ensure UDPSessionManager is created per proxy instance (not global)
var _ = utils.GenerateConnID // Import check
