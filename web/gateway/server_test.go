package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/buhuipao/anyproxy/pkg/common/credential"
	"github.com/buhuipao/anyproxy/pkg/common/monitoring"
)

func TestNewGatewayWebServer(t *testing.T) {
	addr := ":8080"
	staticDir := "./static"

	server := NewGatewayWebServer(addr, staticDir)

	if server == nil {
		t.Fatal("NewGatewayWebServer should return non-nil server")
	}

	if server.addr != addr {
		t.Errorf("Expected addr %s, got %s", addr, server.addr)
	}

	if server.staticDir != staticDir {
		t.Errorf("Expected static dir %s, got %s", staticDir, server.staticDir)
	}

	if server.sessionManager == nil {
		t.Error("Session manager should be initialized")
	}
}

func TestWebServer_SetAuth(t *testing.T) {
	server := NewGatewayWebServer(":8080", "")

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
			server := NewGatewayWebServer(":8080", tt.staticDir)
			result := server.getStaticDir()

			if result != tt.expected {
				t.Errorf("Expected %s, got %s", tt.expected, result)
			}
		})
	}
}

func TestWebServer_IsPublicPath(t *testing.T) {
	server := NewGatewayWebServer(":8080", "")

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
	server := NewGatewayWebServer(":8080", "")
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
	server := NewGatewayWebServer(":8080", "")
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
	server := NewGatewayWebServer(":8080", "")
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
	server := NewGatewayWebServer(":8080", "")

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
	server := NewGatewayWebServer(":8080", "")

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
	server := NewGatewayWebServer(":8080", "")
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
	server := NewGatewayWebServer(":8080", "")

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

func TestWebServer_RespondJSON(t *testing.T) {
	server := NewGatewayWebServer(":8080", "")

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
	server := NewGatewayWebServer(":8080", "")
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

func TestWebServer_HandleCredentialRegisterAuth(t *testing.T) {
	tests := []struct {
		name        string
		authEnabled bool
		username    string
		password    string
		reqUser     string
		reqPass     string
		sendAuth    bool
		wantStatus  int
	}{
		{"no credentials configured, no header", false, "", "", "", "", false, http.StatusUnauthorized},
		{"no credentials configured, empty basic auth", false, "", "", "", "", true, http.StatusUnauthorized},
		{"console auth off, wrong password", false, "admin", "secret", "admin", "wrong", true, http.StatusUnauthorized},
		{"console auth off, correct credentials", false, "admin", "secret", "admin", "secret", true, http.StatusOK},
		{"console auth on, no header", true, "admin", "secret", "", "", false, http.StatusUnauthorized},
		{"console auth on, correct credentials", true, "admin", "secret", "admin", "secret", true, http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cm, err := credential.NewManager(&credential.Config{Type: credential.Memory})
			if err != nil {
				t.Fatalf("NewManager() error = %v", err)
			}
			if err := cm.RegisterClientTransport(&credential.ClientCredential{ClientID: "victim", ClientKey: "old-key"}); err != nil {
				t.Fatalf("RegisterClientTransport() error = %v", err)
			}

			server := NewGatewayWebServer(":8080", "")
			server.SetAuth(tt.authEnabled, tt.username, tt.password)
			server.SetCredentialManager(cm)

			body := `{"client_id":"victim","client_key":"new-key"}`
			req := httptest.NewRequest(http.MethodPost, "/api/credentials/register", strings.NewReader(body))
			if tt.sendAuth {
				req.SetBasicAuth(tt.reqUser, tt.reqPass)
			}
			w := httptest.NewRecorder()
			server.handleCredentialRegister(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
			}
			replaced := cm.ValidateClient("victim", "new-key")
			if replaced != (tt.wantStatus == http.StatusOK) {
				t.Errorf("key replaced = %v, want %v", replaced, tt.wantStatus == http.StatusOK)
			}
		})
	}
}
