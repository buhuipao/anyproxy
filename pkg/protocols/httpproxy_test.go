package protocols

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/buhuipao/anyproxy/pkg/config"
)

// Mock dial function for testing
func mockDialFunc(ctx context.Context, network, addr string) (net.Conn, error) {
	// Create a pipe to simulate network connection
	client, server := net.Pipe()

	// Instead of immediately closing, simulate a proper HTTP response
	go func() {
		defer server.Close()
		// Read the request and send a response
		buf := make([]byte, 4096)
		server.Read(buf) // Read the HTTP request

		// Send a basic HTTP response with correct Content-Length
		responseBody := "Test response"
		response := fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n%s", len(responseBody), responseBody)
		server.Write([]byte(response))
	}()

	return client, nil
}

// Mock dial function that always fails
func failingDialFunc(ctx context.Context, network, addr string) (net.Conn, error) {
	return nil, fmt.Errorf("dial failed")
}

// Mock group validator
func mockGroupValidator(groupID, password string) bool {
	// Simple validation for testing
	return groupID == "testgroup" && password == "testpass"
}

func TestNewHTTPProxyWithAuth(t *testing.T) {
	config := &config.HTTPConfig{
		ListenAddr: ":8080",
	}

	proxy, err := NewHTTPProxyWithAuth(config, mockDialFunc, mockGroupValidator)
	if err != nil {
		t.Fatalf("Failed to create HTTP proxy: %v", err)
	}

	httpProxy, ok := proxy.(*HTTPProxy)
	if !ok {
		t.Fatal("Proxy is not HTTPProxy type")
	}

	if httpProxy.config != config {
		t.Error("Config was not set correctly")
	}

	if httpProxy.dialFunc == nil {
		t.Error("Dial function was not set")
	}

	if httpProxy.groupValidator == nil {
		t.Error("Group validator was not set")
	}
}

func TestHTTPProxy_StartStop(t *testing.T) {
	config := &config.HTTPConfig{
		ListenAddr: "127.0.0.1:0", // Use port 0 for automatic assignment
	}

	proxy, err := NewHTTPProxyWithAuth(config, mockDialFunc, nil)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}

	// Start proxy
	err = proxy.Start()
	if err != nil {
		t.Fatalf("Failed to start proxy: %v", err)
	}

	// Give server time to start
	time.Sleep(100 * time.Millisecond)

	// Stop proxy
	err = proxy.Stop()
	if err != nil {
		t.Errorf("Failed to stop proxy: %v", err)
	}
}

func TestHTTPProxy_GetListenAddr(t *testing.T) {
	config := &config.HTTPConfig{
		ListenAddr: ":8080",
	}

	proxy, _ := NewHTTPProxyWithAuth(config, mockDialFunc, nil)

	httpProxy := proxy.(*HTTPProxy)
	if httpProxy.GetListenAddr() != ":8080" {
		t.Errorf("Expected listen addr :8080, got %s", httpProxy.GetListenAddr())
	}
}

func TestHTTPProxy_Authentication(t *testing.T) {
	config := &config.HTTPConfig{
		ListenAddr: "127.0.0.1:0",
	}

	proxy, _ := NewHTTPProxyWithAuth(config, mockDialFunc, mockGroupValidator)
	httpProxy := proxy.(*HTTPProxy)

	tests := []struct {
		name         string
		authHeader   string
		expectedUser string
		expectedAuth bool
		groupID      string
		password     string
	}{
		{
			name:         "no auth header",
			authHeader:   "",
			expectedUser: "",
			expectedAuth: false,
		},
		{
			name:         "valid group auth",
			authHeader:   "Basic " + base64.StdEncoding.EncodeToString([]byte("testgroup:testpass")),
			expectedUser: "testgroup",
			expectedAuth: true,
			groupID:      "testgroup",
			password:     "testpass",
		},
		{
			name:         "invalid group",
			authHeader:   "Basic " + base64.StdEncoding.EncodeToString([]byte("wronggroup:testpass")),
			expectedUser: "wronggroup",
			expectedAuth: true, // Basic auth parsing succeeds
			groupID:      "wronggroup",
			password:     "testpass",
		},
		{
			name:         "invalid password",
			authHeader:   "Basic " + base64.StdEncoding.EncodeToString([]byte("testgroup:wrongpass")),
			expectedUser: "testgroup",
			expectedAuth: true, // Basic auth parsing succeeds
			groupID:      "testgroup",
			password:     "wrongpass",
		},
		{
			name:         "invalid format",
			authHeader:   "Basic invalid",
			expectedUser: "",
			expectedAuth: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "http://example.com", nil)
			if tt.authHeader != "" {
				req.Header.Set("Proxy-Authorization", tt.authHeader)
			}

			user, password, auth := httpProxy.authenticateAndExtractUser(req)

			if user != tt.expectedUser {
				t.Errorf("Expected user %s, got %s", tt.expectedUser, user)
			}

			if auth != tt.expectedAuth {
				t.Errorf("Expected auth %v, got %v", tt.expectedAuth, auth)
			}

			// Test group validation if auth parsing succeeded
			if auth && tt.groupID != "" {
				groupValid := mockGroupValidator(user, password)
				if tt.groupID == "testgroup" && tt.password == "testpass" {
					if !groupValid {
						t.Errorf("Expected group validation to succeed for valid credentials")
					}
				} else {
					if groupValid {
						t.Errorf("Expected group validation to fail for invalid credentials")
					}
				}
			}
		})
	}
}

func TestHTTPProxy_HandleHTTP_NoAuth(t *testing.T) {
	config := &config.HTTPConfig{
		ListenAddr: "127.0.0.1:0",
	}

	proxy, _ := NewHTTPProxyWithAuth(config, mockDialFunc, nil)
	httpProxy := proxy.(*HTTPProxy)

	// Test normal HTTP request
	req := httptest.NewRequest("GET", "http://example.com", nil)
	req.Host = "example.com"
	req.URL.Host = req.Host

	w := httptest.NewRecorder()
	httpProxy.handleHTTP(w, req)

	resp := w.Result()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	if string(body) != "Test response" {
		t.Errorf("Expected body 'Test response', got %s", string(body))
	}
}

func TestHTTPProxy_HandleHTTP_WithAuth(t *testing.T) {
	config := &config.HTTPConfig{
		ListenAddr: "127.0.0.1:0",
	}

	proxy, _ := NewHTTPProxyWithAuth(config, mockDialFunc, mockGroupValidator)
	httpProxy := proxy.(*HTTPProxy)

	// Test request without auth - should fail
	t.Run("without auth", func(t *testing.T) {
		req := httptest.NewRequest("GET", "http://example.com", nil)
		w := httptest.NewRecorder()

		httpProxy.handleHTTP(w, req)

		resp := w.Result()
		if resp.StatusCode != http.StatusProxyAuthRequired {
			t.Errorf("Expected status 407, got %d", resp.StatusCode)
		}

		if resp.Header.Get("Proxy-Authenticate") != "Basic realm=\"Proxy\"" {
			t.Error("Missing or incorrect Proxy-Authenticate header")
		}
	})

	// Test request with valid auth
	t.Run("with valid auth", func(t *testing.T) {
		req := httptest.NewRequest("GET", "http://example.com", nil)
		req.Host = "example.com"
		req.URL.Host = req.Host
		req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("testgroup:testpass")))

		w := httptest.NewRecorder()
		httpProxy.handleHTTP(w, req)

		resp := w.Result()
		body, _ := io.ReadAll(resp.Body)

		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected status 200, got %d", resp.StatusCode)
		}

		if string(body) != "Test response" {
			t.Errorf("Expected body 'Test response', got %s", string(body))
		}
	})
}

func TestHTTPProxy_HandleConnect(t *testing.T) {
	// Create a test HTTPS server
	httpsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("HTTPS response"))
	}))
	defer httpsServer.Close()

	config := &config.HTTPConfig{
		ListenAddr: "127.0.0.1:0",
	}

	// Use a custom dial function that returns a mock connection
	testDialFunc := func(ctx context.Context, network, addr string) (net.Conn, error) {
		// Create a pipe to simulate the connection
		client, server := net.Pipe()

		// Close the server side immediately to avoid hanging
		go func() {
			time.Sleep(10 * time.Millisecond)
			server.Close()
		}()

		return client, nil
	}

	proxy, _ := NewHTTPProxyWithAuth(config, testDialFunc, nil)
	httpProxy := proxy.(*HTTPProxy)

	// Create a mock hijackable response writer
	mockConn := &mockHijackConn{
		readData:  []byte{},
		writeData: &strings.Builder{},
	}

	w := &mockHijacker{
		ResponseWriter: httptest.NewRecorder(),
		conn:           mockConn,
	}

	// Extract host and port from httpsServer.URL
	serverURL := httpsServer.URL
	host := strings.TrimPrefix(serverURL, "https://")

	req := httptest.NewRequest("CONNECT", host, nil)
	req.Host = host

	// Run handleConnect in a goroutine with timeout
	done := make(chan struct{})
	go func() {
		httpProxy.handleConnect(w, req, "127.0.0.1")
		close(done)
	}()

	// Wait for completion or timeout
	select {
	case <-done:
		// Good, completed
	case <-time.After(100 * time.Millisecond):
		// That's ok, the transfer goroutines are still running
	}

	// Check that 200 Connection Established was sent
	response := mockConn.writeData.String()
	if !strings.Contains(response, "HTTP/1.1 200 Connection Established") {
		t.Errorf("Expected 200 Connection Established response, got: %s", response)
	}
}

func TestHTTPProxy_HandleConnect_FailedDial(t *testing.T) {
	config := &config.HTTPConfig{
		ListenAddr: "127.0.0.1:0",
	}

	proxy, _ := NewHTTPProxyWithAuth(config, failingDialFunc, nil)
	httpProxy := proxy.(*HTTPProxy)

	// Create a mock hijackable response writer
	mockConn := &mockHijackConn{
		readData:  []byte{},
		writeData: &strings.Builder{},
	}

	w := &mockHijacker{
		ResponseWriter: httptest.NewRecorder(),
		conn:           mockConn,
	}

	req := httptest.NewRequest("CONNECT", "example.com:443", nil)
	req.Host = "example.com:443"

	httpProxy.handleConnect(w, req, "127.0.0.1")

	// Check that 502 Bad Gateway was sent
	response := mockConn.writeData.String()
	if !strings.Contains(response, "HTTP/1.1 502 Bad Gateway") {
		t.Errorf("Expected 502 Bad Gateway response, got: %s", response)
	}
}

func TestHTTPProxy_Transfer(t *testing.T) {
	// Create two connected pipes
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	config := &config.HTTPConfig{}
	proxy, _ := NewHTTPProxyWithAuth(config, mockDialFunc, nil)
	httpProxy := proxy.(*HTTPProxy)

	testData := []byte("test data transfer")

	// Start transfer in background
	done := make(chan struct{})
	go func() {
		httpProxy.transfer(server, client, "test", "test-conn-id")
		close(done)
	}()

	// Write data to client
	go func() {
		client.Write(testData)
		client.Close()
	}()

	// Read from server
	buf := make([]byte, len(testData))
	n, _ := server.Read(buf)

	if string(buf[:n]) != string(testData) {
		t.Errorf("Expected %s, got %s", testData, buf[:n])
	}

	// Wait for transfer to complete
	<-done
}

func TestGetClientIP(t *testing.T) {
	tests := []struct {
		name       string
		headers    map[string]string
		remoteAddr string
		expected   string
	}{
		{
			name:       "X-Forwarded-For",
			headers:    map[string]string{"X-Forwarded-For": "192.168.1.1, 10.0.0.1"},
			remoteAddr: "127.0.0.1:1234",
			expected:   "192.168.1.1",
		},
		{
			name:       "X-Real-IP",
			headers:    map[string]string{"X-Real-IP": "192.168.1.2"},
			remoteAddr: "127.0.0.1:1234",
			expected:   "192.168.1.2",
		},
		{
			name:       "RemoteAddr with port",
			headers:    map[string]string{},
			remoteAddr: "192.168.1.3:5678",
			expected:   "192.168.1.3",
		},
		{
			name:       "RemoteAddr without port",
			headers:    map[string]string{},
			remoteAddr: "192.168.1.4",
			expected:   "192.168.1.4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "http://example.com", nil)
			req.RemoteAddr = tt.remoteAddr

			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}

			ip := getClientIP(req)
			if ip != tt.expected {
				t.Errorf("Expected IP %s, got %s", tt.expected, ip)
			}
		})
	}
}

// Mock hijacker for testing CONNECT
type mockHijacker struct {
	http.ResponseWriter
	conn   net.Conn
	reader *strings.Reader
}

func (m *mockHijacker) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return m.conn, nil, nil
}

// Mock connection for hijacking
type mockHijackConn struct {
	readData  []byte
	writeData *strings.Builder
	closed    bool
}

func (m *mockHijackConn) Read(b []byte) (n int, err error) {
	if len(m.readData) == 0 {
		return 0, io.EOF
	}
	n = copy(b, m.readData)
	m.readData = m.readData[n:]
	return n, nil
}

func (m *mockHijackConn) Write(b []byte) (n int, err error) {
	return m.writeData.Write(b)
}

func (m *mockHijackConn) Close() error {
	m.closed = true
	return nil
}

func (m *mockHijackConn) LocalAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8080}
}

func (m *mockHijackConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1234}
}

func (m *mockHijackConn) SetDeadline(t time.Time) error {
	return nil
}

func (m *mockHijackConn) SetReadDeadline(t time.Time) error {
	return nil
}

func (m *mockHijackConn) SetWriteDeadline(t time.Time) error {
	return nil
}
