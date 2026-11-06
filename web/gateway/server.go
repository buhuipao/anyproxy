// Package gateway provides web interface for the gateway server
package gateway

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"time"

	"github.com/buhuipao/anyproxy/pkg/common/credential"
	"github.com/buhuipao/anyproxy/pkg/common/monitoring"
	"github.com/buhuipao/anyproxy/pkg/common/utils"
	"github.com/buhuipao/anyproxy/pkg/logger"
	"github.com/buhuipao/anyproxy/pkg/web/session"
)

// HTTP methods and status constants
const (
	methodPOST   = "POST"
	methodGET    = "GET"
	statusActive = "active"
)

// WebServer represents the Gateway web server
type WebServer struct {
	addr      string
	staticDir string
	server    *http.Server

	// Authentication
	authEnabled    bool
	authUsername   string
	authPassword   string
	sessionManager *session.Manager

	// Credential Manager for SaaS API
	credentialManager *credential.Manager
}

// NewGatewayWebServer creates a new Gateway web server
func NewGatewayWebServer(addr, staticDir string) *WebServer {
	return &WebServer{
		addr:           addr,
		staticDir:      staticDir,
		sessionManager: session.NewManager(24 * time.Hour), // 24 hour sessions
	}
}

// SetAuth configures authentication for the web server. enabled turns on the
// console login; username and password also guard the platform API.
func (gws *WebServer) SetAuth(enabled bool, username, password string) {
	gws.authEnabled = enabled
	gws.authUsername = username
	gws.authPassword = password
}

// SetCredentialManager sets the credential manager for SaaS APIs
func (gws *WebServer) SetCredentialManager(cm *credential.Manager) {
	gws.credentialManager = cm
}

// Start starts the web server
func (gws *WebServer) Start() error {
	mux := http.NewServeMux()

	// IMPORTANT: Register API routes BEFORE the catch-all "/" route
	// Go 1.22+ ServeMux matches more specific patterns first, but to ensure
	// compatibility and clarity, we register specific routes first.

	// SaaS Platform API - Credential Registration with Basic Auth
	// This endpoint is called by the platform to register credentials before client connects
	// Registered first to ensure it takes precedence over the "/" catch-all
	mux.HandleFunc("/api/credentials/register", gws.handleCredentialRegister)

	// TUIC UUID Lookup API - Get the TUIC UUID for a given group_id
	// This helps users configure their TUIC clients (Clash Meta, sing-box, etc.)
	mux.HandleFunc("/api/tuic/uuid", gws.handleTUICUUIDLookup)

	// Authentication APIs (always available if auth is enabled)
	if gws.authEnabled {
		mux.HandleFunc("/api/auth/login", gws.handleLogin)
		mux.HandleFunc("/api/auth/logout", gws.handleLogout)
		mux.HandleFunc("/api/auth/check", gws.handleAuthCheck)
	}

	// Protected API routes
	protectedHandler := gws.getProtectedHandler()

	mux.HandleFunc("/api/metrics/global", protectedHandler(gws.handleGlobalMetrics))
	mux.HandleFunc("/api/metrics/clients", protectedHandler(gws.handleClientMetrics))
	mux.HandleFunc("/api/metrics/connections", protectedHandler(gws.handleConnectionMetrics))

	// Static files (with auth protection if enabled)
	// This is the catch-all "/" route, registered LAST
	staticHandler := http.FileServer(http.Dir(gws.getStaticDir()))
	if gws.authEnabled {
		mux.Handle("/", gws.authMiddleware(staticHandler))
	} else {
		mux.Handle("/", staticHandler)
	}

	// Core APIs only - removed unnecessary rate limiting and stats APIs

	gws.server = &http.Server{
		Addr:              gws.addr,
		Handler:           gws.corsMiddleware(mux),
		ReadHeaderTimeout: 30 * time.Second,
	}

	logger.Info("Starting Gateway Web server", "addr", gws.addr, "auth_enabled", gws.authEnabled)
	return gws.server.ListenAndServe()
}

// getStaticDir returns the static directory path
func (gws *WebServer) getStaticDir() string {
	if gws.staticDir != "" {
		return gws.staticDir
	}
	return "web/gateway/static/"
}

// getProtectedHandler returns a handler wrapper based on auth configuration
func (gws *WebServer) getProtectedHandler() func(http.HandlerFunc) http.HandlerFunc {
	if gws.authEnabled {
		return func(handler http.HandlerFunc) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) {
				gws.authMiddleware(handler).ServeHTTP(w, r)
			}
		}
	}
	return func(handler http.HandlerFunc) http.HandlerFunc {
		return handler
	}
}

// authMiddleware checks authentication for protected routes
func (gws *WebServer) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Allow access to login page and auth APIs without authentication
		if gws.isPublicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		// Get session from cookie
		cookie, err := r.Cookie("gateway_session_id")
		if err != nil {
			gws.requireAuth(w, r)
			return
		}

		// Validate session
		session := gws.sessionManager.GetSession(cookie.Value)
		if session == nil {
			gws.requireAuth(w, r)
			return
		}

		// Update session activity
		gws.sessionManager.UpdateSession(session.ID)

		// Add user info to request context
		r.Header.Set("X-User", session.Username)
		next.ServeHTTP(w, r)
	})
}

// isPublicPath checks if a path should be accessible without authentication
func (gws *WebServer) isPublicPath(path string) bool {
	publicPaths := []string{
		"/login.html",
		"/js/i18n.js",
		"/api/auth/login",
		"/api/auth/logout",
		"/api/auth/check",
		"/api/credentials/register", // SaaS API uses Basic Auth, not session
	}

	for _, publicPath := range publicPaths {
		if path == publicPath {
			return true
		}
	}

	// Allow access to static assets (js, css, images, etc.) for login page
	if len(path) > 4 {
		ext := path[len(path)-4:]
		if ext == ".css" || ext == ".png" || ext == ".ico" || ext == ".svg" {
			return true
		}
	}
	if len(path) > 3 {
		ext := path[len(path)-3:]
		if ext == ".js" {
			return true
		}
	}

	return false
}

// requireAuth redirects to login or returns 401 for API calls
func (gws *WebServer) requireAuth(w http.ResponseWriter, r *http.Request) {
	// Check if this is an API call
	if len(r.URL.Path) >= 4 && r.URL.Path[:4] == "/api" {
		http.Error(w, "Authentication required", http.StatusUnauthorized)
		return
	}

	// Redirect to login page
	http.Redirect(w, r, "/login.html", http.StatusFound)
}

// handleLogin handles user login requests
func (gws *WebServer) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != methodPOST {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var loginReq struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}

	if err := json.NewDecoder(r.Body).Decode(&loginReq); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// Validate credentials in constant time; compare both, so the time does not show which one is wrong
	userOK := subtle.ConstantTimeCompare([]byte(loginReq.Username), []byte(gws.authUsername)) == 1
	passOK := subtle.ConstantTimeCompare([]byte(loginReq.Password), []byte(gws.authPassword)) == 1
	if !userOK || !passOK {
		logger.Warn("Failed login attempt", "username", loginReq.Username, "remote_addr", r.RemoteAddr)
		http.Error(w, "Invalid credentials", http.StatusUnauthorized)
		return
	}

	// Create session
	session := gws.sessionManager.CreateSession(loginReq.Username)

	// Set session cookie
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // the web UI serves plain HTTP, so the cookie cannot be Secure
		Name:     "gateway_session_id",
		Value:    session.ID,
		Path:     "/",
		HttpOnly: true,
		Secure:   false, // Set to true in production with HTTPS
		SameSite: http.SameSiteStrictMode,
		Expires:  session.ExpiresAt,
	})

	logger.Info("User logged in", "username", loginReq.Username, "remote_addr", r.RemoteAddr)

	response := LoginResponse{
		Status:    "success",
		Message:   "Login successful",
		Username:  session.Username,
		ExpiresAt: session.ExpiresAt,
	}
	gws.respondJSON(w, response)
}

// handleLogout handles user logout requests
func (gws *WebServer) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != methodPOST {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Get session from cookie
	cookie, err := r.Cookie("gateway_session_id")
	if err == nil {
		gws.sessionManager.DeleteSession(cookie.Value)
	}

	// Clear session cookie
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // the web UI serves plain HTTP, so the cookie cannot be Secure
		Name:     "gateway_session_id",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Expires:  time.Unix(0, 0),
	})

	response := LogoutResponse{Status: "success"}
	gws.respondJSON(w, response)
}

// handleAuthCheck checks authentication status
func (gws *WebServer) handleAuthCheck(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("gateway_session_id")
	if err != nil {
		response := AuthCheckResponse{Authenticated: false}
		gws.respondJSON(w, response)
		return
	}

	session := gws.sessionManager.GetSession(cookie.Value)
	if session == nil {
		response := AuthCheckResponse{Authenticated: false}
		gws.respondJSON(w, response)
		return
	}

	response := AuthCheckResponse{
		Authenticated: true,
		Username:      session.Username,
		ExpiresAt:     session.ExpiresAt,
	}
	gws.respondJSON(w, response)
}

// Stop stops the web server gracefully
func (gws *WebServer) Stop() error {
	if gws.server != nil {
		return gws.server.Close()
	}
	return nil
}

// corsMiddleware adds CORS support
func (gws *WebServer) corsMiddleware(next http.Handler) http.Handler {
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

// handleGlobalMetrics handles global metrics requests
func (gws *WebServer) handleGlobalMetrics(w http.ResponseWriter, _ *http.Request) {
	global := monitoring.GetMetrics()

	// Get real-time active connections count from actual connection data
	connectionStats := monitoring.GetAllConnectionMetrics()
	activeConnections := int64(0)
	for _, conn := range connectionStats {
		if conn.Status == statusActive {
			activeConnections++
		}
	}

	// Get all client metrics to filter active clients for accurate statistics
	allClientMetrics := monitoring.GetAllClientMetrics()
	onlineClientsCount := int64(0)
	for _, client := range allClientMetrics {
		if client.IsOnline {
			onlineClientsCount++
		}
	}

	response := GlobalMetricsResponse{
		ActiveConnections: activeConnections,
		TotalConnections:  global.TotalConnections,
		BytesSent:         global.BytesSent,
		BytesReceived:     global.BytesReceived,
		ErrorCount:        global.ErrorCount,
		SuccessRate:       global.SuccessRate(),
		Uptime:            global.Uptime().String(),
	}
	gws.respondJSON(w, response)
}

// API Response Structures (all exclude GroupID for security)

// LoginResponse represents login API response
type LoginResponse struct {
	Status    string    `json:"status"`
	Message   string    `json:"message"`
	Username  string    `json:"username"`
	ExpiresAt time.Time `json:"expires_at"`
}

// LogoutResponse represents logout API response
type LogoutResponse struct {
	Status string `json:"status"`
}

// AuthCheckResponse represents authentication check API response
type AuthCheckResponse struct {
	Authenticated bool      `json:"authenticated"`
	Username      string    `json:"username,omitempty"`
	ExpiresAt     time.Time `json:"expires_at,omitempty"`
}

// GlobalMetricsResponse represents global metrics API response
type GlobalMetricsResponse struct {
	ActiveConnections int64   `json:"active_connections"`
	TotalConnections  int64   `json:"total_connections"`
	BytesSent         int64   `json:"bytes_sent"`
	BytesReceived     int64   `json:"bytes_received"`
	ErrorCount        int64   `json:"error_count"`
	SuccessRate       float64 `json:"success_rate"`
	Uptime            string  `json:"uptime"`
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

// handleClientMetrics handles client metrics requests
func (gws *WebServer) handleClientMetrics(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case methodGET:
		clientID := r.URL.Query().Get("client_id")
		if clientID != "" {
			// Get specific client metrics
			clientMetrics := monitoring.GetClientMetrics(clientID)
			if clientMetrics == nil {
				http.Error(w, "Client not found", http.StatusNotFound)
				return
			}
			// Convert to response format without GroupID
			response := toClientMetricsResponse(clientMetrics)
			gws.respondJSON(w, response)
		} else {
			// Get all client metrics
			allMetrics := monitoring.GetAllClientMetrics()

			// Show empty result if no client data available
			if len(allMetrics) == 0 {
				logger.Info("No client metrics found")
				response := make(map[string]*MetricsResponse)
				gws.respondJSON(w, response)
				return
			}

			// Convert to response format without GroupID
			response := make(map[string]*MetricsResponse)
			for clientID, metrics := range allMetrics {
				response[clientID] = toClientMetricsResponse(metrics)
			}
			gws.respondJSON(w, response)
		}
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleConnectionMetrics handles connection metrics requests
func (gws *WebServer) handleConnectionMetrics(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case methodGET:
		connID := r.URL.Query().Get("conn_id")
		if connID != "" {
			// Get specific connection metrics
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
				gws.respondJSON(w, response)
			} else {
				http.Error(w, "Connection not found", http.StatusNotFound)
			}
		} else {
			// Get all connection metrics with computed duration
			allMetrics := monitoring.GetAllConnectionMetrics()
			response := make(map[string]interface{})

			for id, conn := range allMetrics {
				response[id] = map[string]interface{}{
					"connection_id":  conn.ConnectionID,
					"client_id":      conn.ClientID,
					"target_host":    conn.TargetHost,
					"start_time":     conn.StartTime,
					"bytes_sent":     conn.BytesSent,
					"bytes_received": conn.BytesReceived,
					"status":         conn.Status,
					"duration":       time.Since(conn.StartTime).Nanoseconds(),
				}
			}

			gws.respondJSON(w, response)
		}
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// Removed unnecessary rate limiting and stats handlers to minimize code

// countActiveDomains was removed (domain metrics not supported in simplified version)

// CredentialRegisterRequest represents the request body for credential registration
// Platform only registers transport layer auth (client_id + client_key)
// Proxy auth (group_id + group_password) is registered by tunnel client when connecting
type CredentialRegisterRequest struct {
	ClientID      string `json:"client_id"`       // Required: unique client identifier
	ClientKey     string `json:"client_key"`      // Required: transport layer auth key
	BandwidthMbps int    `json:"bandwidth_mbps"`  // Optional: stored, not enforced yet
}

// CredentialRegisterResponse represents the response for credential registration
type CredentialRegisterResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
}

// handleCredentialRegister handles POST /api/credentials/register
// Platform registers transport layer auth (client_id + client_key)
// Proxy auth (group_id + group_password) is registered by tunnel client when connecting
func (gws *WebServer) handleCredentialRegister(w http.ResponseWriter, r *http.Request) {
	logger.Debug("Received credential register request", "method", r.Method, "path", r.URL.Path, "remote_addr", r.RemoteAddr)

	// Only allow POST method
	if r.Method != methodPOST {
		logger.Warn("Invalid method for credential register", "method", r.Method, "remote_addr", r.RemoteAddr)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Validate Basic Auth using gateway's auth credentials
	if !gws.validateBasicAuth(r) {
		logger.Warn("Basic auth failed for credential register", "remote_addr", r.RemoteAddr, "has_auth_header", r.Header.Get("Authorization") != "")
		w.Header().Set("WWW-Authenticate", `Basic realm="AnyProxy Gateway"`)
		w.WriteHeader(http.StatusUnauthorized)
		gws.respondJSON(w, CredentialRegisterResponse{
			Success: false,
			Message: "Unauthorized",
		})
		return
	}

	// Parse request body
	var req CredentialRegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		logger.Error("Failed to decode credential register request", "err", err)
		gws.respondJSON(w, CredentialRegisterResponse{
			Success: false,
			Message: "Invalid request body",
		})
		return
	}

	// Validate required fields - only client_id and client_key are required
	if req.ClientID == "" || req.ClientKey == "" {
		gws.respondJSON(w, CredentialRegisterResponse{
			Success: false,
			Message: "client_id and client_key are required",
		})
		return
	}

	if gws.credentialManager == nil {
		logger.Error("Credential manager not configured")
		gws.respondJSON(w, CredentialRegisterResponse{
			Success: false,
			Message: "Feature not available",
		})
		return
	}

	// Register transport layer client credential only
	// Note: group_id/group_password will be registered when tunnel client connects
	cred := &credential.ClientCredential{
		ClientID:      req.ClientID,
		ClientKey:     req.ClientKey,
		BandwidthMbps: req.BandwidthMbps,
		// GroupID and GroupPassword are empty - will be set by tunnel client
	}

	if err := gws.credentialManager.RegisterClientTransport(cred); err != nil {
		logger.Error("Failed to register client transport credential", "client_id", req.ClientID, "err", err)
		gws.respondJSON(w, CredentialRegisterResponse{
			Success: false,
			Message: err.Error(),
		})
		return
	}

	logger.Info("Client transport credential registered via API", "client_id", req.ClientID, "bandwidth_mbps", req.BandwidthMbps)

	gws.respondJSON(w, CredentialRegisterResponse{
		Success: true,
		Message: "Transport credential registered successfully",
	})
}

// TUICUUIDRequest request for TUIC UUID lookup
type TUICUUIDRequest struct {
	GroupID string `json:"group_id"`
}

// TUICUUIDResponse response for TUIC UUID lookup
type TUICUUIDResponse struct {
	GroupID  string `json:"group_id"`
	UUID     string `json:"uuid"`
	Message  string `json:"message,omitempty"`
	Example  string `json:"example,omitempty"`
	Success  bool   `json:"success"`
}

// handleTUICUUIDLookup handles TUIC UUID lookup requests
// GET /api/tuic/uuid?group_id=xxx or POST /api/tuic/uuid with JSON body
func (gws *WebServer) handleTUICUUIDLookup(w http.ResponseWriter, r *http.Request) {
	var groupID string

	switch r.Method {
	case methodGET:
		// GET request: get group_id from query parameter
		groupID = r.URL.Query().Get("group_id")
	case methodPOST:
		// POST request: get group_id from JSON body
		var req TUICUUIDRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			gws.respondJSON(w, TUICUUIDResponse{
				Success: false,
				Message: "Invalid request body",
			})
			return
		}
		groupID = req.GroupID
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if groupID == "" {
		gws.respondJSON(w, TUICUUIDResponse{
			Success: false,
			Message: "group_id is required",
		})
		return
	}

	// Generate UUID using the shared utility function
	_, uuidStr := utils.GroupIDToTUICUUID(groupID)

	// Build example Clash Meta config snippet
	example := `proxies:
  - name: "anyproxy-tuic"
    type: tuic
    server: your-server
    port: 443
    uuid: "` + uuidStr + `"
    password: "your-group-password"
    alpn: [h3]`

	gws.respondJSON(w, TUICUUIDResponse{
		Success: true,
		GroupID: groupID,
		UUID:    uuidStr,
		Message: "Use this UUID in your TUIC client configuration",
		Example: example,
	})

	logger.Debug("TUIC UUID lookup", "group_id", groupID, "uuid", uuidStr)
}

// validateBasicAuth validates Basic Authentication using gateway's auth credentials.
// It fails when no credentials are configured, so the platform API is never open.
func (gws *WebServer) validateBasicAuth(r *http.Request) bool {
	if gws.authUsername == "" || gws.authPassword == "" {
		return false
	}

	username, password, ok := r.BasicAuth()
	if !ok {
		return false
	}

	userOK := subtle.ConstantTimeCompare([]byte(username), []byte(gws.authUsername)) == 1
	passOK := subtle.ConstantTimeCompare([]byte(password), []byte(gws.authPassword)) == 1
	return userOK && passOK
}

// respondJSON returns JSON response
func (gws *WebServer) respondJSON(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(data); err != nil {
		logger.Error("Failed to encode JSON response", "err", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// Removed stats reset handler as part of code minimization

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
