package credential

import (
	"fmt"
	"sync"
)

// MemoryStore implements in-memory credential storage
type MemoryStore struct {
	passwords map[string]string // groupID -> password (raw, not hashed)
	mu        sync.RWMutex
}

// NewMemoryStore creates a new memory-based credential store
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		passwords: make(map[string]string),
	}
}

// Set stores or updates password (raw, not hashed)
func (ms *MemoryStore) Set(groupID string, password string) error {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	ms.passwords[groupID] = password
	return nil
}

// SetIfAbsent stores the password unless the group already has one, and returns the stored password
func (ms *MemoryStore) SetIfAbsent(groupID string, password string) (string, error) {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	if stored, exists := ms.passwords[groupID]; exists {
		return stored, nil
	}
	ms.passwords[groupID] = password
	return password, nil
}

// Get retrieves password
func (ms *MemoryStore) Get(groupID string) (string, error) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()

	password, exists := ms.passwords[groupID]
	if !exists {
		return "", fmt.Errorf("credentials not found for group: %s", groupID)
	}
	return password, nil
}

// Delete removes password
func (ms *MemoryStore) Delete(groupID string) error {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	delete(ms.passwords, groupID)
	return nil
}

// ValidatePassword checks password validity (direct comparison, no hash)
func (ms *MemoryStore) ValidatePassword(groupID string, password string) bool {
	ms.mu.RLock()
	defer ms.mu.RUnlock()

	storedPassword, exists := ms.passwords[groupID]
	if !exists {
		return false
	}

	return storedPassword == password
}

// GetAll returns all stored credentials
func (ms *MemoryStore) GetAll() map[string]string {
	ms.mu.RLock()
	defer ms.mu.RUnlock()

	result := make(map[string]string, len(ms.passwords))
	for k, v := range ms.passwords {
		result[k] = v
	}
	return result
}
