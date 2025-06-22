package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/buhuipao/anyproxy/pkg/common/monitoring"
	"github.com/buhuipao/anyproxy/pkg/common/ratelimit"
)

func TestNewClientWebServer(t *testing.T) {
	addr := ":8081"
	staticDir := "./static"
	clientID := "test-client"
	rateLimiter := ratelimit.NewRateLimiter(nil)

	server := NewClientWebServer(addr, staticDir, clientID, rateLimiter)

	if server == nil {
		t.Fatal("NewClientWebServer should return non-nil server")
	}

	if server.addr != addr {
		t.Errorf("Expected addr %s, got %s", addr, server.addr)
	}

	if server.staticDir != staticDir {
		t.Errorf("Expected static dir %s, got %s", staticDir, server.staticDir)
	}

	if server.clientID != clientID {
		t.Errorf("Expected client ID %s, got %s", clientID, server.clientID)
	}

	if server.rateLimiter != rateLimiter {
		t.Error("Rate limiter should be set")
	}

	if server.startTime.IsZero() {
		t.Error("Start time should be set")
	}
}

func TestWebServer_SetActualClientID(t *testing.T) {
	server := NewClientWebServer(":8081", "", "original-client", nil)

	// Initially should only have the original client ID
	clientIDs := server.getClientIDs()
	if len(clientIDs) != 1 || clientIDs[0] != "original-client" {
		t.Errorf("Expected initial client IDs ['original-client'], got %v", clientIDs)
	}

	actualID := "runtime-client-id"
	server.SetActualClientID(actualID)

	// Should now have both client IDs
	clientIDs = server.getClientIDs()
	if len(clientIDs) != 2 {
		t.Errorf("Expected 2 client IDs after adding one, got %d", len(clientIDs))
	}

	// Check that the new client ID is added
	found := false
	for _, id := range clientIDs {
		if id == actualID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Expected client ID %s to be added, got %v", actualID, clientIDs)
	}

	// Adding the same client ID again should not duplicate
	server.SetActualClientID(actualID)
	clientIDs = server.getClientIDs()
	if len(clientIDs) != 2 {
		t.Errorf("Expected 2 client IDs after adding duplicate, got %d", len(clientIDs))
	}
}

func TestWebServer_HandleStatus(t *testing.T) {
	server := NewClientWebServer(":8081", "", "test-client", nil)

	// Add some test metrics
	monitoring.UpdateClientMetrics("test-client", "group1", 1000, 2000, false)

	req := httptest.NewRequest("GET", "/api/status", nil)
	rr := httptest.NewRecorder()

	server.handleStatus(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rr.Code)
	}

	if rr.Header().Get("Content-Type") != "application/json" {
		t.Error("Content-Type should be application/json")
	}

	var response StatusResponse
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Errorf("Failed to decode response: %v", err)
	}

	if response.ClientID != "test-client" {
		t.Errorf("Expected client ID 'test-client', got '%s'", response.ClientID)
	}

	if response.Status != "running" {
		t.Errorf("Expected status 'running', got '%s'", response.Status)
	}

	if response.Uptime == "" {
		t.Error("Uptime should not be empty")
	}

	if response.LocalMetrics.TotalConnections < 0 {
		t.Error("Local metrics should have valid values")
	}
}

func TestWebServer_HandleStatusWithActualClientID(t *testing.T) {
	server := NewClientWebServer(":8081", "", "test-client", nil)
	actualID := "runtime-client-123"
	server.SetActualClientID(actualID)

	// Add metrics for the actual client ID
	monitoring.UpdateClientMetrics(actualID, "group1", 1000, 2000, false)

	req := httptest.NewRequest("GET", "/api/status", nil)
	rr := httptest.NewRecorder()

	server.handleStatus(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rr.Code)
	}

	var response StatusResponse
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Errorf("Failed to decode response: %v", err)
	}

	// Should show aggregated client ID in response (includes replica count)
	expectedPrefix := "test-client (+1 replicas)"
	if response.ClientID != expectedPrefix {
		t.Errorf("Expected client ID '%s', got '%s'", expectedPrefix, response.ClientID)
	}
}

func TestWebServer_HandleConnectionMetrics(t *testing.T) {
	server := NewClientWebServer(":8081", "", "test-client", nil)

	// Add some test connection data
	monitoring.UpdateConnectionMetrics("conn1", "test-client", "example.com:80", 1000, 2000, "active")
	monitoring.UpdateConnectionMetrics("conn2", "test-client", "google.com:443", 500, 1000, "active") // Keep it active so it won't be removed

	// Test getting all connections
	req := httptest.NewRequest("GET", "/api/metrics/connections", nil)
	rr := httptest.NewRecorder()

	server.handleConnectionMetrics(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rr.Code)
	}

	if rr.Header().Get("Content-Type") != "application/json" {
		t.Error("Content-Type should be application/json")
	}

	var connections map[string]*monitoring.ConnectionMetrics
	if err := json.NewDecoder(rr.Body).Decode(&connections); err != nil {
		t.Errorf("Failed to decode response: %v", err)
	}

	// Should contain connections for this client
	foundConn1 := false
	foundConn2 := false
	for connID, conn := range connections {
		if connID == "conn1" && conn.ClientID == "test-client" {
			foundConn1 = true
		}
		if connID == "conn2" && conn.ClientID == "test-client" {
			foundConn2 = true
		}
	}

	if !foundConn1 {
		t.Error("Should find conn1 for test-client")
	}

	if !foundConn2 {
		t.Error("Should find conn2 for test-client")
	}
}

func TestWebServer_HandleConnectionMetricsWithSpecificID(t *testing.T) {
	server := NewClientWebServer(":8081", "", "test-client", nil)

	// Add test connection
	monitoring.UpdateConnectionMetrics("specific-conn", "test-client", "example.com:80", 1000, 2000, "active")

	// Test getting specific connection
	req := httptest.NewRequest("GET", "/api/metrics/connections?conn_id=specific-conn", nil)
	rr := httptest.NewRecorder()

	server.handleConnectionMetrics(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rr.Code)
	}

	var conn monitoring.ConnectionMetrics
	if err := json.NewDecoder(rr.Body).Decode(&conn); err != nil {
		t.Errorf("Failed to decode response: %v", err)
	}

	if conn.ClientID != "test-client" {
		t.Errorf("Expected client ID 'test-client', got '%s'", conn.ClientID)
	}

	if conn.TargetHost != "example.com:80" {
		t.Errorf("Expected target host 'example.com:80', got '%s'", conn.TargetHost)
	}
}

func TestWebServer_HandleConnectionMetricsNotFound(t *testing.T) {
	server := NewClientWebServer(":8081", "", "test-client", nil)

	// Test getting non-existent connection
	req := httptest.NewRequest("GET", "/api/metrics/connections?conn_id=nonexistent", nil)
	rr := httptest.NewRecorder()

	server.handleConnectionMetrics(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("Expected status 404, got %d", rr.Code)
	}
}

func TestWebServer_RespondJSON(t *testing.T) {
	server := NewClientWebServer(":8081", "", "test-client", nil)

	data := map[string]interface{}{
		"test": "value",
		"num":  42,
	}

	rr := httptest.NewRecorder()
	server.respondJSON(rr, data)

	if rr.Header().Get("Content-Type") != "application/json" {
		t.Error("Content-Type should be application/json")
	}

	var result map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&result); err != nil {
		t.Errorf("Failed to decode JSON response: %v", err)
	}

	if result["test"] != "value" {
		t.Errorf("Expected test value 'value', got %v", result["test"])
	}

	if result["num"] != float64(42) { // JSON numbers are float64
		t.Errorf("Expected num value 42, got %v", result["num"])
	}
}

func TestWebServer_CorsMiddleware(t *testing.T) {
	server := NewClientWebServer(":8081", "", "test-client", nil)

	// Test with regular request
	handler := server.corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/test", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("CORS origin header should be set to *")
	}

	if rr.Header().Get("Access-Control-Allow-Methods") == "" {
		t.Error("CORS methods header should be set")
	}

	if rr.Header().Get("Access-Control-Allow-Headers") == "" {
		t.Error("CORS headers header should be set")
	}

	// Test with OPTIONS request
	req = httptest.NewRequest("OPTIONS", "/test", nil)
	rr = httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("OPTIONS request should return 200, got %d", rr.Code)
	}
}

func TestToClientMetricsResponse(t *testing.T) {
	clientMetrics := &monitoring.ClientMetrics{
		ClientID:          "test-client",
		ActiveConnections: 5,
		TotalConnections:  10,
		BytesSent:         1000,
		BytesReceived:     2000,
		ErrorCount:        2,
		LastSeen:          time.Now(),
		IsOnline:          true,
	}

	response := toClientMetricsResponse(clientMetrics)

	if response == nil {
		t.Fatal("Response should not be nil")
	}

	if response.ClientID != clientMetrics.ClientID {
		t.Errorf("Expected client ID '%s', got '%s'", clientMetrics.ClientID, response.ClientID)
	}

	if response.ActiveConnections != clientMetrics.ActiveConnections {
		t.Errorf("Expected active connections %d, got %d", clientMetrics.ActiveConnections, response.ActiveConnections)
	}

	if response.TotalConnections != clientMetrics.TotalConnections {
		t.Errorf("Expected total connections %d, got %d", clientMetrics.TotalConnections, response.TotalConnections)
	}

	if response.BytesSent != clientMetrics.BytesSent {
		t.Errorf("Expected bytes sent %d, got %d", clientMetrics.BytesSent, response.BytesSent)
	}

	if response.BytesReceived != clientMetrics.BytesReceived {
		t.Errorf("Expected bytes received %d, got %d", clientMetrics.BytesReceived, response.BytesReceived)
	}

	if response.ErrorCount != clientMetrics.ErrorCount {
		t.Errorf("Expected error count %d, got %d", clientMetrics.ErrorCount, response.ErrorCount)
	}

	if !response.LastSeen.Equal(clientMetrics.LastSeen) {
		t.Errorf("Expected last seen %v, got %v", clientMetrics.LastSeen, response.LastSeen)
	}

	if response.IsOnline != clientMetrics.IsOnline {
		t.Errorf("Expected online %v, got %v", clientMetrics.IsOnline, response.IsOnline)
	}
}
