// Package client provides web interface for the client server
package client

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/buhuipao/anyproxy/pkg/common/monitoring"
	"github.com/buhuipao/anyproxy/pkg/common/ratelimit"
	"github.com/buhuipao/anyproxy/pkg/logger"
)

// API Response Structures (all exclude GroupID for security)

// LocalMetricsData represents local metrics data in status response
type LocalMetricsData struct {
	ActiveConnections int64 `json:"active_connections"`
	TotalConnections  int64 `json:"total_connections"`
	BytesSent         int64 `json:"bytes_sent"`
	BytesReceived     int64 `json:"bytes_received"`
	ErrorCount        int64 `json:"error_count"`
}

// StatusResponse represents client status API response
type StatusResponse struct {
	ClientID      string           `json:"client_id"`
	Status        string           `json:"status"`
	Uptime        string           `json:"uptime"`
	LocalMetrics  LocalMetricsData `json:"local_metrics"`
	ClientMetrics *MetricsResponse `json:"client_metrics,omitempty"`
}

// MetricsResponse represents client metrics response for API (excludes GroupID)
type MetricsResponse struct {
	ClientID          string    `json:"client_id"`
	ActiveConnections int64     `json:"active_connections"`
	TotalConnections  int64     `json:"total_connections"`
	BytesSent         int64     `json:"bytes_sent"`
	BytesReceived     int64     `json:"bytes_received"`
	ErrorCount        int64     `json:"error_count"`
	LastSeen          time.Time `json:"last_seen"`
	IsOnline          bool      `json:"is_online"`
}

// toClientMetricsResponse converts ClientMetrics to MetricsResponse (excluding GroupID)
func toClientMetricsResponse(metrics *monitoring.ClientMetrics) *MetricsResponse {
	return &MetricsResponse{
		ClientID:          metrics.ClientID,
		ActiveConnections: metrics.ActiveConnections,
		TotalConnections:  metrics.TotalConnections,
		BytesSent:         metrics.BytesSent,
		BytesReceived:     metrics.BytesReceived,
		ErrorCount:        metrics.ErrorCount,
		LastSeen:          metrics.LastSeen,
		IsOnline:          metrics.IsOnline,
	}
}

// WebServer represents the Client web server
type WebServer struct {
	rateLimiter *ratelimit.RateLimiter
	clientID    string
	clientIDs   []string     // Track multiple client IDs
	mu          sync.RWMutex // Protect clientIDs slice
	addr        string
	staticDir   string
	server      *http.Server
	startTime   time.Time
}

// NewClientWebServer creates a new Client web server
func NewClientWebServer(addr, staticDir, clientID string, rateLimiter *ratelimit.RateLimiter) *WebServer {
	return &WebServer{
		addr:        addr,
		staticDir:   staticDir,
		clientID:    clientID,
		rateLimiter: rateLimiter,
		startTime:   time.Now(),
	}
}

// SetActualClientID adds a client ID to the tracked list
func (cws *WebServer) SetActualClientID(clientID string) {
	cws.mu.Lock()
	defer cws.mu.Unlock()

	// Check if client ID already exists
	for _, id := range cws.clientIDs {
		if id == clientID {
			return
		}
	}

	// Add new client ID
	cws.clientIDs = append(cws.clientIDs, clientID)
}

// getClientIDs returns a copy of all tracked client IDs
func (cws *WebServer) getClientIDs() []string {
	cws.mu.RLock()
	defer cws.mu.RUnlock()

	// Always include the original client ID
	result := []string{cws.clientID}

	// Add any additional client IDs
	for _, id := range cws.clientIDs {
		// Don't duplicate the original client ID
		if id != cws.clientID {
			result = append(result, id)
		}
	}

	return result
}

// Start starts the web server
func (cws *WebServer) Start() error {
	mux := http.NewServeMux()

	// Static files
	if cws.staticDir != "" {
		mux.Handle("/", http.FileServer(http.Dir(cws.staticDir)))
	} else {
		mux.Handle("/", http.FileServer(http.Dir("web/client/static/")))
	}

	// API routes
	mux.HandleFunc("/api/status", cws.handleStatus)
	mux.HandleFunc("/api/metrics/connections", cws.handleConnectionMetrics)

	// Core APIs only - removed unnecessary config, rate limiting, health and diagnostics APIs

	cws.server = &http.Server{
		Addr:              cws.addr,
		Handler:           cws.corsMiddleware(mux),
		ReadHeaderTimeout: 30 * time.Second,
	}

	logger.Info("Starting Client Web server", "addr", cws.addr, "client_id", cws.clientID)
	return cws.server.ListenAndServe()
}

// Stop stops the web server gracefully
func (cws *WebServer) Stop() error {
	if cws.server != nil {
		return cws.server.Close()
	}
	return nil
}

// corsMiddleware adds CORS support
func (cws *WebServer) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// handleStatus handles status requests
func (cws *WebServer) handleStatus(w http.ResponseWriter, _ *http.Request) {
	// 🔧 Get all tracked client IDs
	clientIDs := cws.getClientIDs()
	primaryClientID := clientIDs[0] // Use first client ID for response

	// 🔧 SIMPLIFIED FIX: Base replica count on tracked client IDs, not metrics
	totalTrackedClients := len(clientIDs)

	// Format client ID based on tracked client count
	clientIDDisplay := primaryClientID
	if totalTrackedClients > 1 {
		clientIDDisplay = fmt.Sprintf("%s (+%d replicas)", primaryClientID, totalTrackedClients-1)
	}

	// 🔧 Get global metrics (for LocalMetrics - represents entire client process)
	globalMetrics := monitoring.GetMetrics()

	// 🔧 Calculate active connections using same method as Gateway (real-time connection status)
	connectionStats := monitoring.GetAllConnectionMetrics()
	realTimeActiveConnections := int64(0)

	// Only count connections that belong to this client
	for _, conn := range connectionStats {
		if conn.Status == "active" {
			// Check if this connection belongs to any of our client IDs
			for _, clientID := range clientIDs {
				if conn.ClientID == clientID || strings.HasPrefix(conn.ClientID, clientID) {
					realTimeActiveConnections++
					break
				}
			}
		}
	}

	// 🔧 Get client metrics for aggregation (ClientMetrics - represents tracked client replicas)
	var aggregatedClientMetrics *MetricsResponse
	allClientMetrics := monitoring.GetAllClientMetrics()

	// Aggregate metrics from ALL client replicas (not just tracked ones)
	totalActiveConnections := int64(0)
	totalConnections := int64(0)
	totalBytesSent := int64(0)
	totalBytesReceived := int64(0)
	totalErrorCount := int64(0)

	latestSeen := time.Time{}
	hasOnlineClient := false

	// Fix: Find all client metrics that match our client pattern (client-r*)
	baseClientID := cws.clientID // e.g., "client"
	matchedCount := 0

	// 🔧 FIX: Also consider additional client IDs tracked via SetActualClientID
	trackedClientIDs := cws.getClientIDs()

	for clientID, clientMetrics := range allClientMetrics {
		// Match clients that start with our base client ID (e.g., "client-r0-", "client-r1-", etc.)
		// OR match any client IDs we're explicitly tracking
		isReplicaPattern := strings.HasPrefix(clientID, baseClientID+"-r")
		isTrackedClient := false
		for _, trackedID := range trackedClientIDs {
			if clientID == trackedID {
				isTrackedClient = true
				break
			}
		}

		if isReplicaPattern || isTrackedClient {
			matchedCount++

			// Accumulate metrics from this client replica
			totalActiveConnections += clientMetrics.ActiveConnections
			totalConnections += clientMetrics.TotalConnections
			totalBytesSent += clientMetrics.BytesSent
			totalBytesReceived += clientMetrics.BytesReceived
			totalErrorCount += clientMetrics.ErrorCount

			// Track latest activity and online status
			if clientMetrics.LastSeen.After(latestSeen) {
				latestSeen = clientMetrics.LastSeen
			}
			if clientMetrics.IsOnline {
				hasOnlineClient = true
			}

			// Use first available client metrics as template
			if aggregatedClientMetrics == nil {
				aggregatedClientMetrics = toClientMetricsResponse(clientMetrics)
			}
		}
	}

	// Update aggregated client metrics with correct totals
	if aggregatedClientMetrics != nil {
		aggregatedClientMetrics.ClientID = clientIDDisplay
		aggregatedClientMetrics.ActiveConnections = totalActiveConnections
		aggregatedClientMetrics.TotalConnections = totalConnections
		aggregatedClientMetrics.BytesSent = totalBytesSent
		aggregatedClientMetrics.BytesReceived = totalBytesReceived
		aggregatedClientMetrics.ErrorCount = totalErrorCount
		aggregatedClientMetrics.LastSeen = latestSeen
		aggregatedClientMetrics.IsOnline = hasOnlineClient
	}

	// 🔧 FIXED: Use consistent data source for both local_metrics and client_metrics
	// The local_metrics should represent the current client process data
	// The client_metrics should represent aggregated replica data

	localMetrics := LocalMetricsData{
		ActiveConnections: realTimeActiveConnections,
		TotalConnections:  globalMetrics.TotalConnections,
		BytesSent:         globalMetrics.BytesSent,
		BytesReceived:     globalMetrics.BytesReceived,
		ErrorCount:        globalMetrics.ErrorCount,
	}

	// 🔧 CRITICAL FIX: For single-process client, local_metrics should match client_metrics
	// If we have aggregated client metrics and they seem reasonable, use them for local_metrics too
	if aggregatedClientMetrics != nil && matchedCount > 0 {
		// Use aggregated data for accurate representation when multiple replicas exist
		localMetrics.TotalConnections = aggregatedClientMetrics.TotalConnections
		localMetrics.ErrorCount = aggregatedClientMetrics.ErrorCount

		// 🔧 IMPORTANT: Only use aggregated bytes if they're significantly larger than global
		// This handles the case where global metrics reset but client metrics persist
		if aggregatedClientMetrics.BytesSent > globalMetrics.BytesSent {
			localMetrics.BytesSent = aggregatedClientMetrics.BytesSent
		}
		if aggregatedClientMetrics.BytesReceived > globalMetrics.BytesReceived {
			localMetrics.BytesReceived = aggregatedClientMetrics.BytesReceived
		}

		logger.Debug("Using aggregated client metrics for local display",
			"client_id", clientIDDisplay,
			"matched_replicas", matchedCount,
			"global_sent", globalMetrics.BytesSent,
			"global_received", globalMetrics.BytesReceived,
			"aggregated_sent", aggregatedClientMetrics.BytesSent,
			"aggregated_received", aggregatedClientMetrics.BytesReceived)
	}

	response := StatusResponse{
		ClientID:      clientIDDisplay,
		Status:        "running",
		Uptime:        globalMetrics.Uptime().String(),
		LocalMetrics:  localMetrics,
		ClientMetrics: aggregatedClientMetrics,
	}

	cws.respondJSON(w, response)
}

// handleConnectionMetrics handles connection metrics requests
func (cws *WebServer) handleConnectionMetrics(w http.ResponseWriter, r *http.Request) {
	connID := r.URL.Query().Get("conn_id")

	if connID != "" {
		// Get specific connection
		allConnections := monitoring.GetAllConnectionMetrics()
		if conn, exists := allConnections[connID]; exists {
			// Create enhanced response with computed duration
			response := map[string]interface{}{
				"connection_id":  conn.ConnectionID,
				"client_id":      conn.ClientID,
				"target_host":    conn.TargetHost,
				"start_time":     conn.StartTime,
				"bytes_sent":     conn.BytesSent,
				"bytes_received": conn.BytesReceived,
				"status":         conn.Status,
				"duration":       time.Since(conn.StartTime).Nanoseconds(),
			}
			cws.respondJSON(w, response)
		} else {
			http.Error(w, "Connection not found", http.StatusNotFound)
		}
	} else {
		// Get all client-related connections
		allConnections := monitoring.GetAllConnectionMetrics()
		clientConnections := make(map[string]interface{})

		// Get all tracked client IDs
		clientIDs := cws.getClientIDs()

		// Match connections that belong to any of the clients
		for id, conn := range allConnections {
			for _, clientID := range clientIDs {
				if conn.ClientID == clientID || strings.HasPrefix(conn.ClientID, clientID) {
					// Create enhanced response with computed duration
					clientConnections[id] = map[string]interface{}{
						"connection_id":  conn.ConnectionID,
						"client_id":      conn.ClientID,
						"target_host":    conn.TargetHost,
						"start_time":     conn.StartTime,
						"bytes_sent":     conn.BytesSent,
						"bytes_received": conn.BytesReceived,
						"status":         conn.Status,
						"duration":       time.Since(conn.StartTime).Nanoseconds(),
					}
					break
				}
			}
		}

		cws.respondJSON(w, clientConnections)
	}
}

// Removed unnecessary config, rate limiting, health and diagnostics handlers to minimize code

// respondJSON returns JSON response
func (cws *WebServer) respondJSON(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(data); err != nil {
		logger.Error("Failed to encode JSON response", "err", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}
