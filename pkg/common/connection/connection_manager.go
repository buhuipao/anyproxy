// Package connection provides connection management utilities for the anyproxy system.
// It includes connection wrappers, managers, and related networking abstractions.
package connection

import (
	"net"
	"sync"

	"github.com/buhuipao/anyproxy/pkg/logger"
)

// Manager manages connections (renamed from ConnectionManager to avoid stuttering)
type Manager struct {
	mu          sync.RWMutex
	conns       map[string]net.Conn
	msgChans    map[string]chan map[string]interface{}
	clientID string // For logging
}

// NewManager creates a new connection manager
func NewManager(clientID string) *Manager {
	return &Manager{
		conns:       make(map[string]net.Conn),
		msgChans:    make(map[string]chan map[string]interface{}),
		clientID: clientID,
	}
}

// AddConnection adds a connection to the manager
func (cm *Manager) AddConnection(connID string, conn net.Conn) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	cm.conns[connID] = conn
	connectionCount := len(cm.conns)
	logger.Debug("Connection added to manager", "client_id", cm.clientID, "conn_id", connID, "total_connections", connectionCount)
}

// GetConnection retrieves a connection by ID
func (cm *Manager) GetConnection(connID string) (net.Conn, bool) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	conn, exists := cm.conns[connID]
	return conn, exists
}

// RemoveConnection removes a connection from the manager
func (cm *Manager) RemoveConnection(connID string) (net.Conn, bool) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	conn, exists := cm.conns[connID]
	if exists {
		delete(cm.conns, connID)
		logger.Debug("Connection removed from manager", "client_id", cm.clientID, "conn_id", connID, "remaining_connections", len(cm.conns))
	}
	return conn, exists
}

// GetConnectionCount gets the connection count
func (cm *Manager) GetConnectionCount() int {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return len(cm.conns)
}

// GetAllConnections returns all connections
func (cm *Manager) GetAllConnections() map[string]net.Conn {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	// Create copy to avoid concurrency issues
	connsCopy := make(map[string]net.Conn, len(cm.conns))
	for k, v := range cm.conns {
		connsCopy[k] = v
	}
	return connsCopy
}

// CloseAllConnections closes all connections
func (cm *Manager) CloseAllConnections() {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	connectionCount := len(cm.conns)
	if connectionCount == 0 {
		logger.Debug("No connections to close", "client_id", cm.clientID)
		return
	}

	logger.Debug("Closing all connections", "client_id", cm.clientID, "connection_count", connectionCount)

	closedCount := 0
	for connID, conn := range cm.conns {
		if err := conn.Close(); err != nil {
			logger.Debug("Error closing connection (expected during shutdown)", "client_id", cm.clientID, "conn_id", connID, "err", err)
		} else {
			closedCount++
		}
		delete(cm.conns, connID)
	}

	logger.Debug("All connections closed", "client_id", cm.clientID, "connections_closed", closedCount)
}

// CreateMessageChannel creates a message channel
func (cm *Manager) CreateMessageChannel(connID string, bufferSize int) chan map[string]interface{} {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// Check if the channel already exists
	if msgChan, exists := cm.msgChans[connID]; exists {
		logger.Debug("Message channel already exists", "client_id", cm.clientID, "conn_id", connID)
		return msgChan
	}

	msgChan := make(chan map[string]interface{}, bufferSize)
	cm.msgChans[connID] = msgChan

	logger.Debug("Created message channel", "client_id", cm.clientID, "conn_id", connID, "buffer_size", bufferSize)
	return msgChan
}

// GetMessageChannel gets a message channel
func (cm *Manager) GetMessageChannel(connID string) (chan map[string]interface{}, bool) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	msgChan, exists := cm.msgChans[connID]
	return msgChan, exists
}

// RemoveMessageChannel removes and closes a message channel
func (cm *Manager) RemoveMessageChannel(connID string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if msgChan, exists := cm.msgChans[connID]; exists {
		delete(cm.msgChans, connID)
		close(msgChan)
		logger.Debug("Message channel removed and closed", "client_id", cm.clientID, "conn_id", connID)
	}
}

// CloseAllMessageChannels closes all message channels
func (cm *Manager) CloseAllMessageChannels() {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	channelCount := len(cm.msgChans)
	if channelCount == 0 {
		return
	}

	for connID, msgChan := range cm.msgChans {
		close(msgChan)
		delete(cm.msgChans, connID)
	}

	logger.Debug("All message channels closed", "client_id", cm.clientID, "channel_count", channelCount)
}

// CleanupConnection cleans up connection and related resources
func (cm *Manager) CleanupConnection(connID string) {
	// Remove and close connection
	if conn, exists := cm.RemoveConnection(connID); exists && conn != nil {
		if err := conn.Close(); err != nil {
			logger.Debug("Error closing connection (expected during cleanup)", "client_id", cm.clientID, "conn_id", connID, "err", err)
		}
	}

	// Remove message channel
	cm.RemoveMessageChannel(connID)

	logger.Debug("Connection cleaned up", "client_id", cm.clientID, "conn_id", connID)
}

// GetMessageChannelCount gets the message channel count
func (cm *Manager) GetMessageChannelCount() int {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return len(cm.msgChans)
}

