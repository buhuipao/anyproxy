// Package session provides shared session management for web servers
package session

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/buhuipao/anyproxy/pkg/logger"
)

// Session represents a user session
type Session struct {
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	CreatedAt time.Time `json:"created_at"`
	LastSeen  time.Time `json:"last_seen"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Manager manages user sessions
type Manager struct {
	mu       sync.RWMutex
	sessions map[string]*Session
	timeout  time.Duration
}

// NewManager creates a new session manager
func NewManager(timeout time.Duration) *Manager {
	sm := &Manager{
		sessions: make(map[string]*Session),
		timeout:  timeout,
	}

	// Start cleanup goroutine
	go sm.cleanupExpiredSessions()

	return sm
}

// CreateSession creates a new session for the user
func (sm *Manager) CreateSession(username string) *Session {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sessionID := sm.generateSessionID()
	now := time.Now()

	s := &Session{
		ID:        sessionID,
		Username:  username,
		CreatedAt: now,
		LastSeen:  now,
		ExpiresAt: now.Add(sm.timeout),
	}

	sm.sessions[sessionID] = s
	return s
}

// GetSession retrieves a session by ID
func (sm *Manager) GetSession(sessionID string) *Session {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	s, exists := sm.sessions[sessionID]
	if !exists || s.ExpiresAt.Before(time.Now()) {
		return nil
	}

	return s
}

// UpdateSession updates the last seen time for a session
func (sm *Manager) UpdateSession(sessionID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if s, exists := sm.sessions[sessionID]; exists {
		s.LastSeen = time.Now()
		s.ExpiresAt = time.Now().Add(sm.timeout)
	}
}

// DeleteSession deletes a session
func (sm *Manager) DeleteSession(sessionID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	delete(sm.sessions, sessionID)
}

// cleanupExpiredSessions removes expired sessions
func (sm *Manager) cleanupExpiredSessions() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		sm.mu.Lock()
		now := time.Now()

		for sessionID, s := range sm.sessions {
			if s.ExpiresAt.Before(now) {
				delete(sm.sessions, sessionID)
			}
		}
		sm.mu.Unlock()
	}
}

// generateSessionID generates a random session ID
func (sm *Manager) generateSessionID() string {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		logger.Error("Failed to generate session ID", "err", err)
		return ""
	}
	return hex.EncodeToString(bytes)
}
