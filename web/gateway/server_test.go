package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/buhuipao/anyproxy/pkg/common/monitoring"
	"github.com/buhuipao/anyproxy/pkg/common/ratelimit"
)

func TestNewSessionManager(t *testing.T) {
	timeout := 1 * time.Hour
	sm := NewSessionManager(timeout)

	if sm == nil {
		t.Fatal("NewSessionManager should return non-nil session manager")
	}

	if sm.timeout != timeout {
		t.Errorf("Expected timeout %v, got %v", timeout, sm.timeout)
	}

	if sm.sessions == nil {
		t.Error("Sessions map should be initialized")
	}
}

func TestSessionManager_CreateSession(t *testing.T) {
	sm := NewSessionManager(1 * time.Hour)
	username := "testuser"

	session := sm.CreateSession(username)

	if session == nil {
		t.Fatal("CreateSession should return non-nil session")
	}

	if session.Username != username {
		t.Errorf("Expected username %s, got %s", username, session.Username)
	}

	if session.ID == "" {
		t.Error("Session ID should not be empty")
	}

	if session.CreatedAt.IsZero() {
		t.Error("Created time should be set")
	}

	if session.LastSeen.IsZero() {
		t.Error("Last seen time should be set")
	}

	if session.ExpiresAt.IsZero() {
		t.Error("Expires time should be set")
	}

	// Session should be stored in manager
	storedSession := sm.GetSession(session.ID)
	if storedSession == nil {
		t.Error("Session should be stored in manager")
	}

	if storedSession.ID != session.ID {
		t.Error("Stored session should have same ID")
	}
}

func TestSessionManager_GetSession(t *testing.T) {
	sm := NewSessionManager(1 * time.Hour)

	// Non-existent session should return nil
	session := sm.GetSession("nonexistent")
	if session != nil {
		t.Error("Non-existent session should return nil")
	}

	// Create and retrieve valid session
	created := sm.CreateSession("testuser")
	retrieved := sm.GetSession(created.ID)

	if retrieved == nil {
		t.Fatal("Valid session should be retrieved")
	}

	if retrieved.ID != created.ID {
		t.Error("Retrieved session should have same ID")
	}
}

func TestSessionManager_GetExpiredSession(t *testing.T) {
	sm := NewSessionManager(1 * time.Millisecond) // Very short timeout

	session := sm.CreateSession("testuser")

	// Wait for session to expire
	time.Sleep(10 * time.Millisecond)

	// Expired session should return nil
	retrieved := sm.GetSession(session.ID)
	if retrieved != nil {
		t.Error("Expired session should return nil")
	}
}

func TestSessionManager_UpdateSession(t *testing.T) {
	sm := NewSessionManager(1 * time.Hour)

	session := sm.CreateSession("testuser")
	originalLastSeen := session.LastSeen
	originalExpiresAt := session.ExpiresAt

	// Wait a bit to ensure time difference
	time.Sleep(10 * time.Millisecond)

	sm.UpdateSession(session.ID)

	updatedSession := sm.GetSession(session.ID)
	if updatedSession == nil {
		t.Fatal("Session should still exist after update")
	}

	if !updatedSession.LastSeen.After(originalLastSeen) {
		t.Error("Last seen time should be updated")
	}

	if !updatedSession.ExpiresAt.After(originalExpiresAt) {
		t.Error("Expires time should be updated")
	}
}

func TestSessionManager_DeleteSession(t *testing.T) {
	sm := NewSessionManager(1 * time.Hour)

	session := sm.CreateSession("testuser")

	// Session should exist
	retrieved := sm.GetSession(session.ID)
	if retrieved == nil {
		t.Fatal("Session should exist before deletion")
	}

	sm.DeleteSession(session.ID)

	// Session should no longer exist
	retrieved = sm.GetSession(session.ID)
	if retrieved != nil {
		t.Error("Session should not exist after deletion")
	}
}

func TestNewGatewayWebServer(t *testing.T) {
	addr := ":8080"
	staticDir := "./static"
	rateLimiter := ratelimit.NewRateLimiter(nil)

	server := NewGatewayWebServer(addr, staticDir, rateLimiter)

	if server == nil {
		t.Fatal("NewGatewayWebServer should return non-nil server")
	}

	if server.addr != addr {
		t.Errorf("Expected addr %s, got %s", addr, server.addr)
	}

	if server.staticDir != staticDir {
		t.Errorf("Expected static dir %s, got %s", staticDir, server.staticDir)
	}

	if server.rateLimiter != rateLimiter {
		t.Error("Rate limiter should be set")
	}

	if server.sessionManager == nil {
		t.Error("Session manager should be initialized")
	}
}

func TestWebServer_SetAuth(t *testing.T) {
	server := NewGatewayWebServer(":8080", "", nil)

	// Initially auth should be disabled
	if server.authEnabled {
		t.Error("Auth should be disabled initially")
	}

	server.SetAuth(true, "admin", "password")

	if !server.authEnabled {
		t.Error("Auth should be enabled after SetAuth")
	}

	if server.authUsername != "admin" {
		t.Errorf("Expected username 'admin', got '%s'", server.authUsername)
	}

	if server.authPassword != "password" {
		t.Errorf("Expected password 'password', got '%s'", server.authPassword)
	}
}

func TestWebServer_GetStaticDir(t *testing.T) {
	tests := []struct {
		name      string
		staticDir string
		expected  string
	}{
		{
			name:      "custom static dir",
			staticDir: "./custom",
			expected:  "./custom",
		},
		{
			name:      "empty static dir",
			staticDir: "",
			expected:  "web/gateway/static/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := NewGatewayWebServer(":8080", tt.staticDir, nil)
			result := server.getStaticDir()

			if result != tt.expected {
				t.Errorf("Expected %s, got %s", tt.expected, result)
			}
		})
	}
}

func TestWebServer_IsPublicPath(t *testing.T) {
	server := NewGatewayWebServer(":8080", "", nil)

	tests := []struct {
		path     string
		expected bool
	}{
		{"/login.html", true},
		{"/js/i18n.js", true},
		{"/api/auth/login", true},
		{"/api/auth/logout", true},
		{"/api/auth/check", true},
		{"/style.css", true},
		{"/app.js", true},
		{"/favicon.ico", true},
		{"/logo.png", true},
		{"/icon.svg", true},
		{"/dashboard.html", false},
		{"/api/metrics/global", false},
		{"/", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			result := server.isPublicPath(tt.path)
			if result != tt.expected {
				t.Errorf("isPublicPath(%s) = %v, want %v", tt.path, result, tt.expected)
			}
		})
	}
}

func TestWebServer_HandleLogin(t *testing.T) {
	server := NewGatewayWebServer(":8080", "", nil)
	server.SetAuth(true, "admin", "password")

	tests := []struct {
		name           string
		method         string
		body           string
		expectedStatus int
		expectCookie   bool
	}{
		{
			name:           "valid login",
			method:         "POST",
			body:           `{"username":"admin","password":"password"}`,
			expectedStatus: http.StatusOK,
			expectCookie:   true,
		},
		{
			name:           "invalid credentials",
			method:         "POST",
			body:           `{"username":"admin","password":"wrong"}`,
			expectedStatus: http.StatusUnauthorized,
			expectCookie:   false,
		},
		{
			name:           "invalid json",
			method:         "POST",
			body:           `invalid json`,
			expectedStatus: http.StatusBadRequest,
			expectCookie:   false,
		},
		{
			name:           "wrong method",
			method:         "GET",
			body:           "",
			expectedStatus: http.StatusMethodNotAllowed,
			expectCookie:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "/api/auth/login", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")

			rr := httptest.NewRecorder()
			server.handleLogin(rr, req)

			if rr.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d", tt.expectedStatus, rr.Code)
			}

			cookies := rr.Result().Cookies()
			hasCookie := false
			for _, cookie := range cookies {
				if cookie.Name == "gateway_session_id" && cookie.Value != "" {
					hasCookie = true
					break
				}
			}

			if hasCookie != tt.expectCookie {
				t.Errorf("Expected cookie %v, got %v", tt.expectCookie, hasCookie)
			}

			if tt.expectedStatus == http.StatusOK {
				var response LoginResponse
				if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
					t.Errorf("Failed to decode response: %v", err)
				}

				if response.Status != "success" {
					t.Errorf("Expected status 'success', got '%s'", response.Status)
				}

				if response.Username != "admin" {
					t.Errorf("Expected username 'admin', got '%s'", response.Username)
				}
			}
		})
	}
}

func TestWebServer_HandleLogout(t *testing.T) {
	server := NewGatewayWebServer(":8080", "", nil)
	server.SetAuth(true, "admin", "password")

	tests := []struct {
		name           string
		method         string
		expectedStatus int
	}{
		{
			name:           "valid logout",
			method:         "POST",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "wrong method",
			method:         "GET",
			expectedStatus: http.StatusMethodNotAllowed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "/api/auth/logout", nil)
			rr := httptest.NewRecorder()

			server.handleLogout(rr, req)

			if rr.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d", tt.expectedStatus, rr.Code)
			}

			if tt.expectedStatus == http.StatusOK {
				// Check that session cookie is cleared
				cookies := rr.Result().Cookies()
				cleared := false
				for _, cookie := range cookies {
					if cookie.Name == "gateway_session_id" && cookie.Value == "" {
						cleared = true
						break
					}
				}

				if !cleared {
					t.Error("Session cookie should be cleared")
				}

				var response LogoutResponse
				if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
					t.Errorf("Failed to decode response: %v", err)
				}

				if response.Status != "success" {
					t.Errorf("Expected status 'success', got '%s'", response.Status)
				}
			}
		})
	}
}

func TestWebServer_HandleAuthCheck(t *testing.T) {
	server := NewGatewayWebServer(":8080", "", nil)
	server.SetAuth(true, "admin", "password")

	// Test without session cookie
	req := httptest.NewRequest("GET", "/api/auth/check", nil)
	rr := httptest.NewRecorder()

	server.handleAuthCheck(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rr.Code)
	}

	var response AuthCheckResponse
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Errorf("Failed to decode response: %v", err)
	}

	if response.Authenticated {
		t.Error("Should not be authenticated without session")
	}

	// Test with valid session
	session := server.sessionManager.CreateSession("admin")

	req = httptest.NewRequest("GET", "/api/auth/check", nil)
	req.AddCookie(&http.Cookie{
		Name:  "gateway_session_id",
		Value: session.ID,
	})
	rr = httptest.NewRecorder()

	server.handleAuthCheck(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rr.Code)
	}

	response = AuthCheckResponse{}
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Errorf("Failed to decode response: %v", err)
	}

	if !response.Authenticated {
		t.Error("Should be authenticated with valid session")
	}

	if response.Username != "admin" {
		t.Errorf("Expected username 'admin', got '%s'", response.Username)
	}
}

func TestWebServer_HandleGlobalMetrics(t *testing.T) {
	server := NewGatewayWebServer(":8080", "", nil)

	// Reset monitoring to clean state
	monitoring.UpdateClientMetrics("test", "group1", 1000, 2000, false)

	req := httptest.NewRequest("GET", "/api/metrics/global", nil)
	rr := httptest.NewRecorder()

	server.handleGlobalMetrics(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rr.Code)
	}

	if rr.Header().Get("Content-Type") != "application/json" {
		t.Error("Content-Type should be application/json")
	}

	var response GlobalMetricsResponse
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Errorf("Failed to decode response: %v", err)
	}

	// Basic validation that response contains expected fields
	if response.Uptime == "" {
		t.Error("Uptime should not be empty")
	}

	if response.SuccessRate < 0 || response.SuccessRate > 100 {
		t.Errorf("Success rate should be between 0-100, got %f", response.SuccessRate)
	}
}

func TestWebServer_CorsMiddleware(t *testing.T) {
	server := NewGatewayWebServer(":8080", "", nil)

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

func TestWebServer_AuthMiddleware(t *testing.T) {
	server := NewGatewayWebServer(":8080", "", nil)
	server.SetAuth(true, "admin", "password")

	protectedHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("protected"))
	})

	middleware := server.authMiddleware(protectedHandler)

	// Test access to public path
	req := httptest.NewRequest("GET", "/login.html", nil)
	rr := httptest.NewRecorder()

	middleware.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Public path should be accessible, got status %d", rr.Code)
	}

	// Test access to protected path without session
	req = httptest.NewRequest("GET", "/dashboard.html", nil)
	rr = httptest.NewRecorder()

	middleware.ServeHTTP(rr, req)

	if rr.Code != http.StatusFound {
		t.Errorf("Protected path without session should redirect, got status %d", rr.Code)
	}

	// Test API access without session
	req = httptest.NewRequest("GET", "/api/metrics/global", nil)
	rr = httptest.NewRecorder()

	middleware.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("Protected API without session should return 401, got status %d", rr.Code)
	}

	// Test access with valid session
	session := server.sessionManager.CreateSession("admin")
	req = httptest.NewRequest("GET", "/dashboard.html", nil)
	req.AddCookie(&http.Cookie{
		Name:  "gateway_session_id",
		Value: session.ID,
	})
	rr = httptest.NewRecorder()

	middleware.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Protected path with valid session should be accessible, got status %d", rr.Code)
	}

	if rr.Body.String() != "protected" {
		t.Error("Should reach protected handler")
	}
}

func TestWebServer_RequireAuth(t *testing.T) {
	server := NewGatewayWebServer(":8080", "", nil)

	// Test API request
	req := httptest.NewRequest("GET", "/api/test", nil)
	rr := httptest.NewRecorder()

	server.requireAuth(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("API request should return 401, got %d", rr.Code)
	}

	// Test non-API request
	req = httptest.NewRequest("GET", "/dashboard.html", nil)
	rr = httptest.NewRecorder()

	server.requireAuth(rr, req)

	if rr.Code != http.StatusFound {
		t.Errorf("Non-API request should redirect, got %d", rr.Code)
	}

	location := rr.Header().Get("Location")
	if location != "/login.html" {
		t.Errorf("Should redirect to login page, got %s", location)
	}
}

func TestSessionManager_GenerateSessionID(t *testing.T) {
	sm := NewSessionManager(1 * time.Hour)

	id1 := sm.generateSessionID()
	id2 := sm.generateSessionID()

	if id1 == "" {
		t.Error("Session ID should not be empty")
	}

	if id2 == "" {
		t.Error("Session ID should not be empty")
	}

	if id1 == id2 {
		t.Error("Session IDs should be unique")
	}

	// Session ID should be hex encoded (64 characters for 32 bytes)
	if len(id1) != 64 {
		t.Errorf("Expected session ID length 64, got %d", len(id1))
	}
}

func TestWebServer_RespondJSON(t *testing.T) {
	server := NewGatewayWebServer(":8080", "", nil)

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

func TestWebServer_GetProtectedHandler(t *testing.T) {
	// Test with auth disabled
	server := NewGatewayWebServer(":8080", "", nil)
	server.SetAuth(false, "", "")

	handler := server.getProtectedHandler()

	// Should return the handler directly without auth wrapper
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("test"))
	})

	req := httptest.NewRequest("GET", "/test", nil)
	rr := httptest.NewRecorder()

	wrappedHandler := handler(testHandler)
	wrappedHandler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", rr.Code)
	}

	if rr.Body.String() != "test" {
		t.Error("Should reach test handler without auth")
	}

	// Test with auth enabled
	server.SetAuth(true, "admin", "password")

	handler = server.getProtectedHandler()
	rr = httptest.NewRecorder()

	wrappedHandler = handler(testHandler)
	wrappedHandler.ServeHTTP(rr, req)

	// Should redirect because no auth
	if rr.Code == http.StatusOK {
		t.Error("Should require auth when auth is enabled")
	}
}
