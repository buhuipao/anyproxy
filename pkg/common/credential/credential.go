// Package credential provides simple credential management for AnyProxy gateway
package credential

import (
	"crypto/subtle"
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
	// Set stores or updates password for a group (stores raw password, no hash)
	Set(groupID string, password string) error
	// SetIfAbsent stores the password only if the group has none, and returns the stored password
	SetIfAbsent(groupID string, password string) (string, error)
	// Get retrieves password for a group
	Get(groupID string) (string, error)
	// Delete removes password for a group
	Delete(groupID string) error
	// ValidatePassword checks if the provided password matches the stored one
	ValidatePassword(groupID string, password string) bool
	// GetAll returns all stored credentials (groupID -> password)
	GetAll() map[string]string
}

// BandwidthInfo stores bandwidth limit info for a group
type BandwidthInfo struct {
	GroupID       string `json:"group_id"`
	BandwidthMbps int    `json:"bandwidth_mbps"` // 0=unlimited, >0=limit in Mbps; nothing sets it above 0 today
}

// ClientCredential stores client authentication info for dynamic auth
// This is used when Platform pre-registers client credentials before client connects
type ClientCredential struct {
	ClientID      string `json:"client_id"`
	ClientKey     string `json:"client_key"` // Transport layer auth (maps to auth_password)
	GroupID       string `json:"group_id"`
	GroupPassword string `json:"group_password"` // Application layer auth
	BandwidthMbps int    `json:"bandwidth_mbps"`
}

// Manager manages credential operations
type Manager struct {
	store             Store
	bandwidth         map[string]*BandwidthInfo    // bandwidth limits per group
	clientCredentials map[string]*ClientCredential // client_id -> credential for dynamic auth
	observers         []CredentialObserver         // registered observers for credential updates
	mu                sync.RWMutex
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
		store:             store,
		bandwidth:         make(map[string]*BandwidthInfo),
		clientCredentials: make(map[string]*ClientCredential),
		observers:         make([]CredentialObserver, 0),
	}, nil
}

// RegisterGroup registers the password for a group.
// If the group already has a password, the given password must match it.
func (m *Manager) RegisterGroup(groupID, password string) error {
	return m.RegisterGroupWithBandwidth(groupID, password, 0)
}

// RegisterGroupWithBandwidth registers credentials with bandwidth limit.
// If the group already has a password, the given password must match it;
// the stored password is never replaced.
func (m *Manager) RegisterGroupWithBandwidth(groupID, password string, bandwidthMbps int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if groupID == "" || password == "" {
		return fmt.Errorf("group ID and password cannot be empty")
	}

	// Store the credentials (raw password, no hash) unless the group already has a password.
	// Insert-only, so gateways that share a database cannot both register different passwords.
	stored, err := m.store.SetIfAbsent(groupID, password)
	if err != nil {
		// The error reaches the remote client, so keep store details in the gateway log only
		logger.Error("Failed to store group credentials", "group_id", groupID, "err", err)
		return fmt.Errorf("authentication failed: credential store unavailable")
	}
	if subtle.ConstantTimeCompare([]byte(stored), []byte(password)) != 1 {
		return fmt.Errorf("authentication failed: group_password does not match the registered password for group %s", groupID)
	}

	// Store bandwidth info
	m.bandwidth[groupID] = &BandwidthInfo{
		GroupID:       groupID,
		BandwidthMbps: bandwidthMbps,
	}

	// Notify all observers of the credential update
	m.notifyObservers(groupID, password)

	logger.Info("Registered credentials for group", "group_id", groupID, "bandwidth_mbps", bandwidthMbps)
	return nil
}

// GetBandwidthLimit returns the bandwidth limit for a group in bytes per second.
// The gateway applies it, but it is 0 (no limit) in practice: RegisterGroup
// stores 0, RegisterClient has no callers, and bandwidth_mbps from the
// register API stays on the client credential, which this does not read.
func (m *Manager) GetBandwidthLimit(groupID string) int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()

	info, exists := m.bandwidth[groupID]
	if !exists || info.BandwidthMbps == 0 {
		return 0 // unlimited
	}
	// Convert Mbps to bytes per second (Mbps * 1024 * 1024 / 8)
	return int64(info.BandwidthMbps) * 131072
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

// CheckGroupPassword checks the group password of a connecting tunnel client.
// The password is required for every store type; it is registered for a new
// group or must match the stored one.
func (m *Manager) CheckGroupPassword(groupID, password string) error {
	if password == "" {
		return fmt.Errorf("authentication failed: group_password is required for group %s", groupID)
	}
	return m.RegisterGroup(groupID, password)
}

// IsMemoryStore reports whether credentials are kept in memory only.
// Memory credentials come from connected clients; file and db credentials persist,
// whether they were pre-configured or registered by a client.
func (m *Manager) IsMemoryStore() bool {
	_, ok := m.store.(*MemoryStore)
	return ok
}

// RemoveGroup removes password for a group and notifies observers
func (m *Manager) RemoveGroup(groupID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.store.Delete(groupID); err != nil {
		return fmt.Errorf("failed to remove group credentials: %v", err)
	}

	// Remove bandwidth info
	delete(m.bandwidth, groupID)

	// Notify observers of removal (for TUIC to remove users)
	m.notifyObserversRemove(groupID)

	logger.Info("Removed credentials for group", "group_id", groupID)
	return nil
}

// --- Client Credential Management for Dynamic Auth ---

// RegisterClientTransport registers only transport layer auth (client_id + client_key)
// This is called by Platform via /api/credentials/register
// Group credentials (group_id + group_password) are registered when tunnel client connects
func (m *Manager) RegisterClientTransport(cred *ClientCredential) error {
	if cred == nil {
		return fmt.Errorf("credential cannot be nil")
	}
	if cred.ClientID == "" || cred.ClientKey == "" {
		return fmt.Errorf("client_id and client_key cannot be empty")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Store only transport layer credential (no group info yet)
	m.clientCredentials[cred.ClientID] = &ClientCredential{
		ClientID:      cred.ClientID,
		ClientKey:     cred.ClientKey,
		BandwidthMbps: cred.BandwidthMbps,
		// GroupID and GroupPassword are empty - will be set by tunnel client
	}

	logger.Info("Registered client transport credentials",
		"client_id", cred.ClientID,
		"bandwidth_mbps", cred.BandwidthMbps)
	return nil
}

// RegisterClient registers full client credentials (used when tunnel client connects)
// This updates the group info for a client that was pre-registered by Platform
func (m *Manager) RegisterClient(cred *ClientCredential) error {
	if cred == nil {
		return fmt.Errorf("credential cannot be nil")
	}
	if cred.ClientID == "" || cred.ClientKey == "" {
		return fmt.Errorf("client_id and client_key cannot be empty")
	}
	if cred.GroupID == "" || cred.GroupPassword == "" {
		return fmt.Errorf("group_id and group_password cannot be empty")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Store client credential
	m.clientCredentials[cred.ClientID] = cred

	// Also register group credentials for application layer auth (raw password, no hash)
	if err := m.store.Set(cred.GroupID, cred.GroupPassword); err != nil {
		return fmt.Errorf("failed to store group credentials: %v", err)
	}

	// Store bandwidth info
	m.bandwidth[cred.GroupID] = &BandwidthInfo{
		GroupID:       cred.GroupID,
		BandwidthMbps: cred.BandwidthMbps,
	}

	// Notify all observers of the credential update
	m.notifyObservers(cred.GroupID, cred.GroupPassword)

	logger.Info("Registered client credentials",
		"client_id", cred.ClientID,
		"group_id", cred.GroupID,
		"bandwidth_mbps", cred.BandwidthMbps)
	return nil
}

// ValidateClient validates client_id and client_key for transport layer auth
// Returns true if credentials match a pre-registered client
func (m *Manager) ValidateClient(clientID, clientKey string) bool {
	if clientID == "" || clientKey == "" {
		return false
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	cred, exists := m.clientCredentials[clientID]
	if !exists {
		return false
	}

	return cred.ClientKey == clientKey
}

// GetClientCredential returns the full credential info for a client
// Returns nil if client is not registered
func (m *Manager) GetClientCredential(clientID string) *ClientCredential {
	m.mu.RLock()
	defer m.mu.RUnlock()

	cred, exists := m.clientCredentials[clientID]
	if !exists {
		return nil
	}

	// Return a copy to prevent external modification
	return &ClientCredential{
		ClientID:      cred.ClientID,
		ClientKey:     cred.ClientKey,
		GroupID:       cred.GroupID,
		GroupPassword: cred.GroupPassword,
		BandwidthMbps: cred.BandwidthMbps,
	}
}

// RemoveClient removes a client's credentials
func (m *Manager) RemoveClient(clientID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.clientCredentials[clientID]; exists {
		delete(m.clientCredentials, clientID)
		logger.Info("Removed client credentials", "client_id", clientID)
	}
}

// --- Credential Observer Pattern for Dynamic Sync ---

// CredentialObserver defines callback interface for credential updates
// This allows protocols like TUIC to receive credential updates dynamically
type CredentialObserver interface { //nolint:revive // keep the exported name; renaming would change the package API
	// OnCredentialUpdate is called when a group credential is registered or updated
	// NOTE: password is the raw password (not hashed), as some protocols (TUIC) need raw passwords
	OnCredentialUpdate(groupID, password string)

	// OnCredentialRemove is called when a group credential is removed
	// This allows TUIC to remove users from sing-quic when client disconnects
	OnCredentialRemove(groupID string)
}

// RegisterObserver registers an observer to receive credential update notifications
func (m *Manager) RegisterObserver(observer CredentialObserver) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.observers == nil {
		m.observers = make([]CredentialObserver, 0)
	}
	m.observers = append(m.observers, observer)
	logger.Debug("Registered credential observer", "total_observers", len(m.observers))
}

// notifyObservers notifies all registered observers of credential updates
// Should be called with lock held
func (m *Manager) notifyObservers(groupID, password string) {
	for _, observer := range m.observers {
		// Run in goroutine to avoid blocking
		go observer.OnCredentialUpdate(groupID, password)
	}
}

// notifyObserversRemove notifies all registered observers of credential removal
// Should be called with lock held
func (m *Manager) notifyObserversRemove(groupID string) {
	for _, observer := range m.observers {
		// Run in goroutine to avoid blocking
		go observer.OnCredentialRemove(groupID)
	}
}

// GetAllGroups returns all registered group credentials (for initial sync)
// Returns map of groupID -> password
func (m *Manager) GetAllGroups() map[string]string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.store.GetAll()
}
