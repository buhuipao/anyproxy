// Package credential provides simple credential management for AnyProxy gateway
package credential

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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
	// DB stores credentials in a database
	DB Type = "db"
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
	Type     Type      `yaml:"type"`
	FilePath string    `yaml:"file_path"` // Only used for file type
	DB       *DBConfig `yaml:"db"`        // Only used for db type
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
	case DB:
		if config.DB == nil {
			return nil, fmt.Errorf("database configuration is required for DB store type")
		}
		store, err = NewDBStore(config.DB)
		if err != nil {
			return nil, fmt.Errorf("failed to create db store: %v", err)
		}
		logger.Info("Created database-based credential store", "driver", config.DB.Driver)
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
