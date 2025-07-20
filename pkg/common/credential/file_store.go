package credential

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

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
