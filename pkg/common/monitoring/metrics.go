// Package monitoring provides simplified performance metrics
package monitoring

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/buhuipao/anyproxy/pkg/logger"
)

// Metrics represents essential system metrics (backward compatibility)
type Metrics struct {
	ActiveConnections int64     `json:"active_connections"`
	TotalConnections  int64     `json:"total_connections"`
	BytesSent         int64     `json:"bytes_sent"`
	BytesReceived     int64     `json:"bytes_received"`
	ErrorCount        int64     `json:"error_count"`
	StartTime         time.Time `json:"start_time"`
}

// Uptime returns system uptime
func (m *Metrics) Uptime() time.Duration {
	return time.Since(m.StartTime)
}

// SuccessRate calculates success rate
func (m *Metrics) SuccessRate() float64 {
	total := atomic.LoadInt64(&m.TotalConnections)
	if total == 0 {
		return 100.0
	}
	errors := atomic.LoadInt64(&m.ErrorCount)
	return float64(total-errors) / float64(total) * 100
}

// ClientMetrics represents per-client statistics (simplified)
type ClientMetrics struct {
	ClientID          string    `json:"client_id"`
	GroupID           string    `json:"group_id"`
	ActiveConnections int64     `json:"active_connections"`
	TotalConnections  int64     `json:"total_connections"`
	BytesSent         int64     `json:"bytes_sent"`
	BytesReceived     int64     `json:"bytes_received"`
	ErrorCount        int64     `json:"error_count"`
	LastSeen          time.Time `json:"last_seen"`
	IsOnline          bool      `json:"is_online"`
}

// ConnectionMetrics represents connection information (simplified)
type ConnectionMetrics struct {
	ConnectionID  string    `json:"connection_id"`
	ClientID      string    `json:"client_id"`
	TargetHost    string    `json:"target_host"`
	StartTime     time.Time `json:"start_time"`
	BytesSent     int64     `json:"bytes_sent"`
	BytesReceived int64     `json:"bytes_received"`
	Status        string    `json:"status"`
}

// MetricsManager manages all metrics with minimal complexity
type MetricsManager struct {
	mu          sync.RWMutex
	global      *Metrics
	clients     map[string]*ClientMetrics
	connections map[string]*ConnectionMetrics
}

// Global instance
var globalManager = &MetricsManager{
	global: &Metrics{
		StartTime: time.Now(),
	},
	clients:     make(map[string]*ClientMetrics),
	connections: make(map[string]*ConnectionMetrics),
}

// CreateConnection creates a new connection record and increments counters
func (m *MetricsManager) CreateConnection(connID, clientID, targetHost string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if connection already exists
	if _, exists := m.connections[connID]; exists {
		logger.Warn("Attempted to create duplicate connection", "conn_id", connID, "client_id", clientID)
		return
	}

	logger.Debug("Creating new connection in metrics", "conn_id", connID, "client_id", clientID, "target_host", targetHost)

	// Create connection record
	conn := &ConnectionMetrics{
		ConnectionID: connID,
		ClientID:     clientID,
		TargetHost:   targetHost,
		StartTime:    time.Now(),
		Status:       "active",
	}
	m.connections[connID] = conn

	// Increment active connections
	atomic.AddInt64(&m.global.ActiveConnections, 1)
	atomic.AddInt64(&m.global.TotalConnections, 1)

	// Increment client's total connections
	m.incrementClientConnections(clientID)
}

// UpdateConnectionBytes updates byte counters for existing connection
func (m *MetricsManager) UpdateConnectionBytes(connID, clientID string, bytesSent, bytesReceived int64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Always update global and client metrics, even if specific connection doesn't exist
	// This ensures metrics consistency across distributed processes
	if bytesSent > 0 {
		atomic.AddInt64(&m.global.BytesSent, bytesSent)
		logger.Debug("Global bytes sent updated", "conn_id", connID, "client_id", clientID, "bytes", bytesSent, "new_total", atomic.LoadInt64(&m.global.BytesSent))
	}
	if bytesReceived > 0 {
		atomic.AddInt64(&m.global.BytesReceived, bytesReceived)
		logger.Debug("Global bytes received updated", "conn_id", connID, "client_id", clientID, "bytes", bytesReceived, "new_total", atomic.LoadInt64(&m.global.BytesReceived))
	}

	// Update client stats
	if bytesSent > 0 || bytesReceived > 0 {
		m.updateClientStats(clientID, "", bytesSent, bytesReceived, false)
	}

	// Update connection-specific bytes if connection exists
	conn, exists := m.connections[connID]
	if exists {
		if bytesSent > 0 {
			atomic.AddInt64(&conn.BytesSent, bytesSent)
		}
		if bytesReceived > 0 {
			atomic.AddInt64(&conn.BytesReceived, bytesReceived)
		}
		logger.Debug("Updated connection metrics", "conn_id", connID, "client_id", clientID, "bytes_sent", bytesSent, "bytes_received", bytesReceived)
	} else {
		// Log when updating metrics for non-existent connection (this is expected in distributed setup)
		logger.Debug("Updated global metrics for cleaned-up connection", "conn_id", connID, "client_id", clientID, "bytes_sent", bytesSent, "bytes_received", bytesReceived)
	}
}

// CloseConnection removes connection and updates counters
func (m *MetricsManager) CloseConnection(connID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if conn, exists := m.connections[connID]; exists {
		logger.Debug("Closing connection in metrics", "conn_id", connID, "client_id", conn.ClientID, "target_host", conn.TargetHost)
		delete(m.connections, connID)
		atomic.AddInt64(&m.global.ActiveConnections, -1)
	} else {
		logger.Debug("Attempted to close non-existent connection", "conn_id", connID)
	}
}

// updateClientStats updates client statistics (internal, must hold lock)
func (m *MetricsManager) updateClientStats(clientID, groupID string, bytesSent, bytesReceived int64, isError bool) {
	client, exists := m.clients[clientID]
	if !exists {
		client = &ClientMetrics{
			ClientID: clientID,
			GroupID:  groupID,
			IsOnline: true,
		}
		m.clients[clientID] = client
	}

	client.LastSeen = time.Now()
	client.IsOnline = true

	if bytesSent > 0 {
		atomic.AddInt64(&client.BytesSent, bytesSent)
	}
	if bytesReceived > 0 {
		atomic.AddInt64(&client.BytesReceived, bytesReceived)
	}
	if isError {
		atomic.AddInt64(&client.ErrorCount, 1)
		atomic.AddInt64(&m.global.ErrorCount, 1)
	}
}

// incrementClientConnections increments total connections for a specific client
func (m *MetricsManager) incrementClientConnections(clientID string) {
	client, exists := m.clients[clientID]
	if !exists {
		client = &ClientMetrics{
			ClientID: clientID,
			IsOnline: true,
		}
		m.clients[clientID] = client
	}

	atomic.AddInt64(&client.TotalConnections, 1)
	client.LastSeen = time.Now()
	client.IsOnline = true
}

// UpdateClientMetrics updates client metrics
func (m *MetricsManager) UpdateClientMetrics(clientID, groupID string, bytesSent, bytesReceived int64, isError bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updateClientStats(clientID, groupID, bytesSent, bytesReceived, isError)
}

// GetClientStats returns client statistics
func (m *MetricsManager) GetClientStats(clientID string) *ClientMetrics {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.clients[clientID]
}

// GetAllClientStats returns all client statistics
func (m *MetricsManager) GetAllClientStats() map[string]*ClientMetrics {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string]*ClientMetrics)

	for k, v := range m.clients {
		// Update active connections count from actual connections
		activeCount := int64(0)
		for _, conn := range m.connections {
			if conn.ClientID == k {
				activeCount++
			}
		}

		// Create copy with updated active connections
		clientCopy := *v
		clientCopy.ActiveConnections = activeCount

		// Check if client should be marked as offline based on inactivity
		// 🔧 IMPROVED: Mark as offline if no activity for more than 2 minutes (was 5 minutes)
		if clientCopy.IsOnline && time.Since(clientCopy.LastSeen) > 2*time.Minute {
			clientCopy.IsOnline = false
		}

		// 🔧 CRITICAL FIX: If client is offline, it should have 0 active connections
		if !clientCopy.IsOnline {
			clientCopy.ActiveConnections = 0
			// Also clean up any stale connections for this offline client
			m.cleanupOfflineClientConnections(k)
		}

		result[k] = &clientCopy
	}
	return result
}

// cleanupOfflineClientConnections removes stale connections for offline clients
func (m *MetricsManager) cleanupOfflineClientConnections(clientID string) {
	connectionsToRemove := make([]string, 0)

	// Find all connections belonging to this offline client
	for connID, conn := range m.connections {
		if conn.ClientID == clientID {
			connectionsToRemove = append(connectionsToRemove, connID)
		}
	}

	// Remove stale connections and update global active count
	for _, connID := range connectionsToRemove {
		logger.Warn("Cleaning up stale connection from offline client",
			"client_id", clientID, "conn_id", connID)
		delete(m.connections, connID)
		atomic.AddInt64(&m.global.ActiveConnections, -1)
	}

	if len(connectionsToRemove) > 0 {
		logger.Info("Cleaned up stale connections",
			"client_id", clientID, "connections_removed", len(connectionsToRemove))
	}
}

// GetActiveConnections returns active connection information
func (m *MetricsManager) GetActiveConnections() map[string]*ConnectionMetrics {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string]*ConnectionMetrics)
	for k, v := range m.connections {
		result[k] = v
	}
	return result
}

// MarkClientOffline marks a client as offline
func (m *MetricsManager) MarkClientOffline(clientID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if client, exists := m.clients[clientID]; exists {
		client.IsOnline = false
		client.LastSeen = time.Now()
		logger.Debug("Marked client offline", "client_id", clientID)
	}
}

// Cleanup removes old offline clients (call periodically)
func (m *MetricsManager) Cleanup(maxOfflineTime time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	cutoff := time.Now().Add(-maxOfflineTime)
	removedCount := 0
	markedOfflineCount := 0

	for clientID, client := range m.clients {
		// 🔧 IMPROVED: Mark clients as offline if they've been inactive for more than 2 minutes (was 5 minutes)
		if client.IsOnline && time.Since(client.LastSeen) > 2*time.Minute {
			client.IsOnline = false
			client.LastSeen = time.Now()
			markedOfflineCount++
		}

		// Remove offline clients that have been offline for the specified duration
		if !client.IsOnline && client.LastSeen.Before(cutoff) {
			delete(m.clients, clientID)
			removedCount++
		}
	}

	if markedOfflineCount > 0 {
		logger.Debug("Marked inactive clients as offline", "marked_offline_count", markedOfflineCount)
	}
	if removedCount > 0 {
		logger.Debug("Cleaned up offline clients", "removed_count", removedCount)
	}
}

// UpdateClientMetrics updates client metrics (legacy compatibility)
func UpdateClientMetrics(clientID, groupID string, bytesSent, bytesReceived int64, isError bool) {
	globalManager.UpdateClientMetrics(clientID, groupID, bytesSent, bytesReceived, isError)
}

// UpdateConnectionMetrics updates connection metrics (legacy compatibility)
func UpdateConnectionMetrics(connID, clientID, targetHost string, bytesSent, bytesReceived int64, status string) {
	if status == "closed" {
		globalManager.CloseConnection(connID)
	} else {
		// For backward compatibility: create connection if it doesn't exist, then update bytes
		globalManager.mu.RLock()
		_, exists := globalManager.connections[connID]
		globalManager.mu.RUnlock()

		if !exists {
			globalManager.CreateConnection(connID, clientID, targetHost)
		}

		if bytesSent > 0 || bytesReceived > 0 {
			globalManager.UpdateConnectionBytes(connID, clientID, bytesSent, bytesReceived)
		}
	}
}

// UpdateConnectionBytesWithStatus updates connection byte counters with status (legacy compatibility)
func UpdateConnectionBytesWithStatus(connID, clientID string, bytesSent, bytesReceived int64, status string) {
	if status == "closed" {
		globalManager.CloseConnection(connID)
	} else {
		globalManager.UpdateConnectionBytes(connID, clientID, bytesSent, bytesReceived)
	}
}

// GetMetrics returns global metrics
func GetMetrics() *Metrics {
	return globalManager.global
}

// GetAllClientMetrics returns all client metrics
func GetAllClientMetrics() map[string]*ClientMetrics {
	return globalManager.GetAllClientStats()
}

// GetAllConnectionMetrics returns all connection metrics
func GetAllConnectionMetrics() map[string]*ConnectionMetrics {
	return globalManager.GetActiveConnections()
}

// GetClientMetrics returns metrics for a specific client
func GetClientMetrics(clientID string) *ClientMetrics {
	return globalManager.GetClientStats(clientID)
}

// MarkClientOffline marks a client as offline
func MarkClientOffline(clientID string) {
	globalManager.MarkClientOffline(clientID)
}

// Simple cleanup timer with proper synchronization
var (
	cleanupMu      sync.Mutex
	cleanupTicker  *time.Ticker
	cleanupCancel  context.CancelFunc
	cleanupWg      sync.WaitGroup
	cleanupRunning bool
)

// StartCleanupProcess starts the cleanup process for old metrics
func StartCleanupProcess() {
	cleanupMu.Lock()
	defer cleanupMu.Unlock()

	if cleanupRunning {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	cleanupTicker = time.NewTicker(10 * time.Second) // 🔧 More frequent cleanup (every 10 seconds instead of 1 minute)
	cleanupCancel = cancel
	cleanupRunning = true

	cleanupWg.Add(1)
	go func() {
		defer func() {
			cleanupWg.Done()
			if r := recover(); r != nil {
				// Prevent panic if ticker is nil
				logger.Error("Clean up panic", "error", r, "stack", string(debug.Stack()))
			}
		}()

		// Use local variables to avoid race conditions
		ticker := cleanupTicker

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// 🔧 More aggressive cleanup: offline timeout reduced to 2 minutes, cleanup after 3 minutes
				globalManager.Cleanup(3 * time.Minute) // Remove clients offline for more than 3 minutes (was 5 minutes)

				// 🔧 Also validate and fix connection count inconsistencies periodically
				globalCount, actualCount, isConsistent := globalManager.ValidateConnectionCounts()
				if !isConsistent {
					logger.Warn("Detected connection count inconsistency during cleanup",
						"global_count", globalCount,
						"actual_count", actualCount,
						"difference", globalCount-actualCount)

					// Fix inconsistency automatically
					oldCount, newCount := globalManager.FixConnectionCountInconsistency()
					if oldCount != newCount {
						logger.Info("Auto-fixed connection count inconsistency",
							"old_count", oldCount,
							"new_count", newCount,
							"corrected", oldCount-newCount)
					}
				}
			}
		}
	}()
}

// StopCleanupProcess stops the cleanup process
func StopCleanupProcess() {
	cleanupMu.Lock()

	if !cleanupRunning {
		cleanupMu.Unlock()
		return
	}

	// Cancel the context and stop the ticker
	if cleanupCancel != nil {
		cleanupCancel()
	}
	if cleanupTicker != nil {
		cleanupTicker.Stop()
		cleanupTicker = nil
	}
	cleanupCancel = nil
	cleanupRunning = false

	cleanupMu.Unlock()

	// Wait for cleanup goroutine to finish (outside of lock)
	cleanupWg.Wait()
}

// Legacy compatibility functions (for tests only)

// IncrementActiveConnections increments active connection count (legacy compatibility - tests only)
func IncrementActiveConnections() {
	atomic.AddInt64(&globalManager.global.ActiveConnections, 1)
	atomic.AddInt64(&globalManager.global.TotalConnections, 1)
}

// DecrementActiveConnections decrements active connection count (legacy compatibility - tests only)
func DecrementActiveConnections() {
	atomic.AddInt64(&globalManager.global.ActiveConnections, -1)
}

// AddBytesSent adds bytes sent to global counter (legacy compatibility - tests only)
func AddBytesSent(bytes int64) {
	atomic.AddInt64(&globalManager.global.BytesSent, bytes)
}

// AddBytesReceived adds bytes received to global counter (legacy compatibility - tests only)
func AddBytesReceived(bytes int64) {
	atomic.AddInt64(&globalManager.global.BytesReceived, bytes)
}

// IncrementErrors increments error count (used in production code)
func IncrementErrors() {
	atomic.AddInt64(&globalManager.global.ErrorCount, 1)
}

// humanizeBytes converts bytes to human-readable format
func humanizeBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// Test-only validation functions

// ValidateConnectionCounts validates that the global active connection count matches actual connections (tests only)
func (m *MetricsManager) ValidateConnectionCounts() (globalCount, actualCount int64, isConsistent bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	globalCount = atomic.LoadInt64(&m.global.ActiveConnections)
	actualCount = int64(len(m.connections))
	isConsistent = globalCount == actualCount

	if !isConsistent {
		logger.Warn("Connection count inconsistency detected",
			"global_count", globalCount,
			"actual_count", actualCount,
			"difference", globalCount-actualCount)
	}

	return globalCount, actualCount, isConsistent
}

// FixConnectionCountInconsistency fixes connection count inconsistency by resetting global count to actual count (tests only)
func (m *MetricsManager) FixConnectionCountInconsistency() (oldCount, newCount int64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	oldCount = atomic.LoadInt64(&m.global.ActiveConnections)
	newCount = int64(len(m.connections))

	if oldCount != newCount {
		logger.Warn("Fixing connection count inconsistency",
			"old_global_count", oldCount,
			"new_global_count", newCount,
			"difference", oldCount-newCount)

		// Reset global count to actual count
		atomic.StoreInt64(&m.global.ActiveConnections, newCount)

		logger.Info("Connection count inconsistency fixed",
			"corrected_count", newCount,
			"connections_corrected", oldCount-newCount)
	}

	return oldCount, newCount
}

// ValidateConnectionCounts validates connection count consistency (tests only)
func ValidateConnectionCounts() (globalCount, actualCount int64, isConsistent bool) {
	return globalManager.ValidateConnectionCounts()
}

// FixConnectionCountInconsistency fixes connection count inconsistency (tests only)
func FixConnectionCountInconsistency() (oldCount, newCount int64) {
	return globalManager.FixConnectionCountInconsistency()
}

// CreateConnection creates a new connection record (public API)
func CreateConnection(connID, clientID, targetHost string) {
	globalManager.CreateConnection(connID, clientID, targetHost)
}

// UpdateConnectionBytes updates connection byte counters (public API)
func UpdateConnectionBytes(connID, clientID string, bytesSent, bytesReceived int64) {
	globalManager.UpdateConnectionBytes(connID, clientID, bytesSent, bytesReceived)
}

// CloseConnection closes a connection (public API)
func CloseConnection(connID string) {
	globalManager.CloseConnection(connID)
}
