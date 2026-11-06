package session

import (
	"testing"
	"time"
)

func TestNewManager(t *testing.T) {
	timeout := 1 * time.Hour
	sm := NewManager(timeout)

	if sm == nil {
		t.Fatal("NewManager should return non-nil session manager")
	}

	if sm.timeout != timeout {
		t.Errorf("Expected timeout %v, got %v", timeout, sm.timeout)
	}

	if sm.sessions == nil {
		t.Error("Sessions map should be initialized")
	}
}

func TestManager_CreateSession(t *testing.T) {
	sm := NewManager(1 * time.Hour)
	username := "testuser"

	s := sm.CreateSession(username)

	if s == nil {
		t.Fatal("CreateSession should return non-nil session")
	}

	if s.Username != username {
		t.Errorf("Expected username %s, got %s", username, s.Username)
	}

	if s.ID == "" {
		t.Error("Session ID should not be empty")
	}

	if s.CreatedAt.IsZero() {
		t.Error("Created time should be set")
	}

	if s.LastSeen.IsZero() {
		t.Error("Last seen time should be set")
	}

	if s.ExpiresAt.IsZero() {
		t.Error("Expires time should be set")
	}

	// Session should be stored in manager
	storedSession := sm.GetSession(s.ID)
	if storedSession == nil {
		t.Error("Session should be stored in manager")
	}

	if storedSession.ID != s.ID {
		t.Error("Stored session should have same ID")
	}
}

func TestManager_GetSession(t *testing.T) {
	sm := NewManager(1 * time.Hour)

	// Non-existent session should return nil
	s := sm.GetSession("nonexistent")
	if s != nil {
		t.Error("Non-existent session should return nil")
	}

	// Create and retrieve valid session
	created := sm.CreateSession("testuser")
	retrieved := sm.GetSession(created.ID)

	if retrieved == nil {
		t.Fatal("Valid session should be retrieved")
	}

	if retrieved.ID != created.ID {
		t.Error("Retrieved session should have same ID")
	}
}

func TestManager_GetExpiredSession(t *testing.T) {
	sm := NewManager(1 * time.Millisecond) // Very short timeout

	s := sm.CreateSession("testuser")

	// Wait for session to expire
	time.Sleep(10 * time.Millisecond)

	// Expired session should return nil
	retrieved := sm.GetSession(s.ID)
	if retrieved != nil {
		t.Error("Expired session should return nil")
	}
}

func TestManager_UpdateSession(t *testing.T) {
	sm := NewManager(1 * time.Hour)

	s := sm.CreateSession("testuser")
	originalLastSeen := s.LastSeen
	originalExpiresAt := s.ExpiresAt

	// Wait a bit to ensure time difference
	time.Sleep(10 * time.Millisecond)

	sm.UpdateSession(s.ID)

	updatedSession := sm.GetSession(s.ID)
	if updatedSession == nil {
		t.Fatal("Session should still exist after update")
	}

	if !updatedSession.LastSeen.After(originalLastSeen) {
		t.Error("Last seen time should be updated")
	}

	if !updatedSession.ExpiresAt.After(originalExpiresAt) {
		t.Error("Expires time should be updated")
	}
}

func TestManager_DeleteSession(t *testing.T) {
	sm := NewManager(1 * time.Hour)

	s := sm.CreateSession("testuser")

	// Session should exist
	retrieved := sm.GetSession(s.ID)
	if retrieved == nil {
		t.Fatal("Session should exist before deletion")
	}

	sm.DeleteSession(s.ID)

	// Session should no longer exist
	retrieved = sm.GetSession(s.ID)
	if retrieved != nil {
		t.Error("Session should not exist after deletion")
	}
}

func TestManager_GenerateSessionID(t *testing.T) {
	sm := NewManager(1 * time.Hour)

	id1 := sm.generateSessionID()
	id2 := sm.generateSessionID()

	if id1 == "" {
		t.Error("Session ID should not be empty")
	}

	if id2 == "" {
		t.Error("Session ID should not be empty")
	}

	if id1 == id2 {
		t.Error("Session IDs should be unique")
	}

	// Session ID should be hex encoded (64 characters for 32 bytes)
	if len(id1) != 64 {
		t.Errorf("Expected session ID length 64, got %d", len(id1))
	}
}
