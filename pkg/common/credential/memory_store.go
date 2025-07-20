package credential

import (
	"fmt"
	"sync"
)

// MemoryStore implements in-memory credential storage
type MemoryStore struct {
	passwords map[string]string // groupID -> passwordHash
	mu        sync.RWMutex
}

// NewMemoryStore creates a new memory-based credential store
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		passwords: make(map[string]string),
	}
}

// Set stores or updates password hash
func (ms *MemoryStore) Set(groupID string, passwordHash string) error {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	ms.passwords[groupID] = passwordHash
	return nil
}

// Get retrieves password hash
func (ms *MemoryStore) Get(groupID string) (string, error) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()

	hash, exists := ms.passwords[groupID]
	if !exists {
		return "", fmt.Errorf("credentials not found for group: %s", groupID)
	}
	return hash, nil
}

// Delete removes password
func (ms *MemoryStore) Delete(groupID string) error {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	delete(ms.passwords, groupID)
	return nil
}

// ValidatePassword checks password validity
func (ms *MemoryStore) ValidatePassword(groupID string, password string) bool {
	ms.mu.RLock()
	defer ms.mu.RUnlock()

	hash, exists := ms.passwords[groupID]
	if !exists {
		return false
	}

	return hash == hashPassword(password)
}
