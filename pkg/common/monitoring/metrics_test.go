package monitoring

import (
	"testing"
)

// TestDataConsistencyFix tests that the metrics fix ensures data consistency
func TestDataConsistencyFix(t *testing.T) {
	// Reset global manager state
	globalManager.mu.Lock()
	globalManager.connections = make(map[string]*ConnectionMetrics)
	globalManager.clients = make(map[string]*ClientMetrics)
	globalManager.global.ActiveConnections = 0
	globalManager.global.TotalConnections = 0
	globalManager.global.BytesSent = 0
	globalManager.global.BytesReceived = 0
	globalManager.global.ErrorCount = 0
	globalManager.mu.Unlock()

	connID := "test-conn"
	clientID := "test-client"
	targetHost := "example.com:443"

	// Create connection and transfer some data
	CreateConnection(connID, clientID, targetHost)
	UpdateConnectionBytes(connID, clientID, 1000, 500)

	// Check initial state
	globalMetrics := GetMetrics()
	if globalMetrics.BytesSent != 1000 {
		t.Errorf("Expected global BytesSent=1000, got %d", globalMetrics.BytesSent)
	}

	// 🔥 CRITICAL: Connection gets cleaned up but data transfer continues
	CloseConnection(connID)

	// 🔥 THE FIX: UpdateConnectionBytes should still update global metrics
	UpdateConnectionBytes(connID, clientID, 2000, 1500)

	// Verify global metrics are updated despite connection being closed
	globalMetrics = GetMetrics()
	if globalMetrics.BytesSent != 3000 { // 1000 + 2000
		t.Errorf("FIX FAILED: Expected global BytesSent=3000, got %d", globalMetrics.BytesSent)
	}
	if globalMetrics.BytesReceived != 2000 { // 500 + 1500
		t.Errorf("FIX FAILED: Expected global BytesReceived=2000, got %d", globalMetrics.BytesReceived)
	}

	// Verify no ghost connections were created
	globalCount, actualCount, isConsistent := ValidateConnectionCounts()
	if !isConsistent || globalCount != 0 || actualCount != 0 {
		t.Errorf("FIX FAILED: Created ghost connections: global=%d, actual=%d", globalCount, actualCount)
	}

	t.Log("✅ Data consistency fix verified successfully")
}
