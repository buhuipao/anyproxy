// Package credential provides simple credential management for AnyProxy gateway
package credential

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/buhuipao/anyproxy/pkg/logger"
)

// Type represents the credential storage type
type Type string

const (
	// Memory stores credentials in memory only
	Memory Type = "memory"
	// File stores credentials in a file
	File Type = "file"
)

// Store interface defines credential storage operations
type Store interface {
	// Set stores or updates password for a group
	Set(groupID string, passwordHash string) error
	// Get retrieves password hash for a group
	Get(groupID string) (string, error)
	// Delete removes password for a group
	Delete(groupID string) error
	// ValidatePassword checks if the provided password matches the stored one
	ValidatePassword(groupID string, password string) bool
}

// Manager manages credential operations
type Manager struct {
	store Store
	mu    sync.RWMutex
}

// Config represents credential manager configuration
type Config struct {
	Type     Type   `yaml:"type"`
	FilePath string `yaml:"file_path"` // Only used for file type
}

// NewManager creates a new credential manager
func NewManager(config *Config) (*Manager, error) {
	if config == nil {
		config = &Config{Type: Memory}
	}

	var store Store
	var err error

	switch config.Type {
	case Memory:
		store = NewMemoryStore()
		logger.Info("Created memory-based credential store")
	case File:
		if config.FilePath == "" {
			config.FilePath = "credentials.json"
		}
		store, err = NewFileStore(config.FilePath)
		if err != nil {
			return nil, fmt.Errorf("failed to create file store: %v", err)
		}
		logger.Info("Created file-based credential store", "file", config.FilePath)
	default:
		return nil, fmt.Errorf("unsupported credential store type: %s", config.Type)
	}

	return &Manager{
		store: store,
	}, nil
}

// RegisterGroup registers or updates password for a group
func (m *Manager) RegisterGroup(groupID, password string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if groupID == "" || password == "" {
		return fmt.Errorf("group ID and password cannot be empty")
	}

	// Hash the password
	hash := hashPassword(password)

	// Store the credentials
	if err := m.store.Set(groupID, hash); err != nil {
		return fmt.Errorf("failed to store credentials: %v", err)
	}

	logger.Info("Registered credentials for group", "group_id", groupID)
	return nil
}

// ValidateGroup validates password for a group
func (m *Manager) ValidateGroup(groupID, password string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if groupID == "" || password == "" {
		return false
	}

	return m.store.ValidatePassword(groupID, password)
}

// RemoveGroup removes password for a group
func (m *Manager) RemoveGroup(groupID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.store.Delete(groupID); err != nil {
		return fmt.Errorf("failed to remove group credentials: %v", err)
	}

	logger.Info("Removed credentials for group", "group_id", groupID)
	return nil
}

// hashPassword creates a SHA256 hash of the password
func hashPassword(password string) string {
	hash := sha256.Sum256([]byte(password))
	return hex.EncodeToString(hash[:])
}

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

// FileStore implements file-based credential storage
type FileStore struct {
	filePath string
	mu       sync.RWMutex
}

// NewFileStore creates a new file-based credential store
func NewFileStore(filePath string) (*FileStore, error) {
	// Ensure directory exists
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create directory: %v", err)
	}

	fs := &FileStore{
		filePath: filePath,
	}

	// Create file if it doesn't exist
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		if err := fs.save(make(map[string]string)); err != nil {
			return nil, fmt.Errorf("failed to create credential file: %v", err)
		}
	}

	return fs, nil
}

// load reads credentials from file
func (fs *FileStore) load() (map[string]string, error) {
	data, err := os.ReadFile(fs.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]string), nil
		}
		return nil, err
	}

	var passwords map[string]string
	if err := json.Unmarshal(data, &passwords); err != nil {
		return nil, err
	}

	if passwords == nil {
		passwords = make(map[string]string)
	}

	return passwords, nil
}

// save writes credentials to file
func (fs *FileStore) save(passwords map[string]string) error {
	data, err := json.MarshalIndent(passwords, "", "  ")
	if err != nil {
		return err
	}

	// Write to temporary file first
	tmpFile := fs.filePath + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0600); err != nil {
		return err
	}

	// Rename to actual file (atomic operation)
	return os.Rename(tmpFile, fs.filePath)
}

// Set stores or updates password hash
func (fs *FileStore) Set(groupID string, passwordHash string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	passwords, err := fs.load()
	if err != nil {
		return err
	}

	passwords[groupID] = passwordHash
	return fs.save(passwords)
}

// Get retrieves password hash
func (fs *FileStore) Get(groupID string) (string, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	passwords, err := fs.load()
	if err != nil {
		return "", err
	}

	hash, exists := passwords[groupID]
	if !exists {
		return "", fmt.Errorf("credentials not found for group: %s", groupID)
	}
	return hash, nil
}

// Delete removes password
func (fs *FileStore) Delete(groupID string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	passwords, err := fs.load()
	if err != nil {
		return err
	}

	delete(passwords, groupID)
	return fs.save(passwords)
}

// ValidatePassword checks password validity
func (fs *FileStore) ValidatePassword(groupID string, password string) bool {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	passwords, err := fs.load()
	if err != nil {
		return false
	}

	hash, exists := passwords[groupID]
	if !exists {
		return false
	}

	return hash == hashPassword(password)
}
