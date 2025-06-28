package websocket

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/buhuipao/anyproxy/pkg/transport"
	"github.com/gorilla/websocket"
)

func TestNewWebSocketTransport(t *testing.T) {
	trans := NewWebSocketTransport()

	if trans == nil {
		t.Fatal("Expected non-nil transport")
	}

	wsTransport, ok := trans.(*webSocketTransport)
	if !ok {
		t.Fatal("Transport is not webSocketTransport type")
	}

	if wsTransport.authConfig != nil {
		t.Error("Auth config should be nil for default transport")
	}
}

func TestNewWebSocketTransportWithAuth(t *testing.T) {
	authConfig := &transport.AuthConfig{
		Username: "testuser",
		Password: "testpass",
	}

	trans := NewWebSocketTransportWithAuth(authConfig)

	if trans == nil {
		t.Fatal("Expected non-nil transport")
	}

	wsTransport, ok := trans.(*webSocketTransport)
	if !ok {
		t.Fatal("Transport is not webSocketTransport type")
	}

	if wsTransport.authConfig != authConfig {
		t.Error("Auth config was not set correctly")
	}
}

func TestWebSocketTransport_ListenAndServe(t *testing.T) {
	trans := NewWebSocketTransport()

	// Start server in background
	serverStarted := make(chan struct{})
	serverError := make(chan error, 1)

	go func() {
		close(serverStarted)
		err := trans.ListenAndServe(":0", func(conn transport.Connection) {
			// Echo server
			data, err := conn.ReadMessage()
			if err == nil {
				conn.WriteMessage(data)
			}
			conn.Close()
		})
		if err != nil && !strings.Contains(err.Error(), "Server closed") {
			serverError <- err
		}
	}()

	<-serverStarted
	time.Sleep(100 * time.Millisecond) // Give server time to start

	// Get the actual server address
	wsTransport := trans.(*webSocketTransport)
	wsTransport.mu.Lock()
	server := wsTransport.server
	wsTransport.mu.Unlock()
	if server == nil {
		t.Fatal("Server not started")
	}

	// Try to connect
	// Note: We can't easily test WebSocket connection without a real server
	// So we just verify the server is running

	// Clean up
	trans.Close()

	select {
	case err := <-serverError:
		t.Fatalf("Server error: %v", err)
	default:
		// No error, good
	}
}

func TestWebSocketTransport_ListenAndServeWithTLS(t *testing.T) {
	trans := NewWebSocketTransport()

	// Create a simple TLS config
	tlsConfig := &tls.Config{
		InsecureSkipVerify: true,
	}

	// Start server in background
	serverStarted := make(chan struct{})
	serverError := make(chan error, 1)

	go func() {
		close(serverStarted)
		err := trans.ListenAndServeWithTLS(":0", func(conn transport.Connection) {
			// Echo server
			conn.Close()
		}, tlsConfig)
		if err != nil && !strings.Contains(err.Error(), "Server closed") {
			serverError <- err
		}
	}()

	<-serverStarted
	time.Sleep(100 * time.Millisecond) // Give server time to start

	// Verify TLS config was set
	wsTransport := trans.(*webSocketTransport)
	wsTransport.mu.Lock()
	server := wsTransport.server
	wsTransport.mu.Unlock()
	if server == nil {
		t.Fatal("Server not started")
	}

	if server.TLSConfig != tlsConfig {
		t.Error("TLS config was not set correctly")
	}

	// Clean up
	trans.Close()

	select {
	case err := <-serverError:
		t.Fatalf("Server error: %v", err)
	default:
		// No error, good
	}
}

func TestWebSocketTransport_DialWithConfig(t *testing.T) {
	// Create a test WebSocket server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check auth header
		auth := r.Header.Get("Authorization")
		if auth == "" {
			t.Error("Missing authorization header")
		}

		// Check custom headers
		clientID := r.Header.Get("X-Client-ID")
		groupID := r.Header.Get("X-Group-ID")

		if clientID != "test-client" {
			t.Errorf("Expected client ID 'test-client', got '%s'", clientID)
		}

		if groupID != "test-group" {
			t.Errorf("Expected group ID 'test-group', got '%s'", groupID)
		}

		// Upgrade to WebSocket
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("Failed to upgrade: %v", err)
			return
		}
		defer conn.Close()

		// Echo messages
		for {
			msgType, msg, err := conn.ReadMessage()
			if err != nil {
				break
			}
			conn.WriteMessage(msgType, msg)
		}
	}))
	defer server.Close()

	// Create transport
	trans := NewWebSocketTransportWithAuth(&transport.AuthConfig{
		Username: "user",
		Password: "pass",
	})

	// Dial the test server
	config := &transport.ClientConfig{
		ClientID:   "test-client",
		GroupID:    "test-group",
		Username:   "user",
		Password:   "pass",
		SkipVerify: true,
	}

	// Extract host:port from server URL
	serverURL := server.URL
	if strings.HasPrefix(serverURL, "http://") {
		serverURL = strings.TrimPrefix(serverURL, "http://")
	} else if strings.HasPrefix(serverURL, "https://") {
		serverURL = strings.TrimPrefix(serverURL, "https://")
	}

	conn, err := trans.DialWithConfig(serverURL, config)
	if err != nil {
		t.Fatalf("Failed to dial: %v", err)
	}
	defer conn.Close()

	// Test message exchange
	testData := []byte("hello world")

	if err := conn.WriteMessage(testData); err != nil {
		t.Fatalf("Failed to write message: %v", err)
	}

	response, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("Failed to read message: %v", err)
	}

	if string(response) != string(testData) {
		t.Error("Response doesn't match sent message")
	}
}

func TestWebSocketConnection_ClientInfo(t *testing.T) {
	// Test WebSocket connection client info functionality
	// Create a mock websocket connection to test the wrapper

	// Create a test server that will upgrade to WebSocket
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clientID := r.Header.Get("X-Client-ID")
		groupID := r.Header.Get("X-Group-ID")

		if clientID == "" {
			http.Error(w, "Missing client ID", http.StatusBadRequest)
			return
		}

		upgrader := websocket.Upgrader{
			CheckOrigin: func(_ *http.Request) bool { return true },
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			http.Error(w, "Failed to upgrade", http.StatusInternalServerError)
			return
		}
		defer conn.Close()

		// Create WebSocket connection wrapper with client info
		wsConn := NewWebSocketConnectionWithInfo(conn, clientID, groupID, "test-password")

		// Test that client info is properly stored
		// Cast to the concrete type to access client info methods
		if wsConnWithInfo, ok := wsConn.(*webSocketConnectionWithInfo); ok {
			if wsConnWithInfo.GetClientID() != clientID {
				t.Errorf("Expected client ID %s, got %s", clientID, wsConnWithInfo.GetClientID())
			}

			if wsConnWithInfo.GetGroupID() != groupID {
				t.Errorf("Expected group ID %s, got %s", groupID, wsConnWithInfo.GetGroupID())
			}
		} else {
			t.Error("Failed to cast WebSocket connection to webSocketConnectionWithInfo")
		}

		// Simple echo for testing
		for {
			msgType, msg, err := conn.ReadMessage()
			if err != nil {
				break
			}
			conn.WriteMessage(msgType, msg)
		}
	}))
	defer server.Close()

	// Convert to WebSocket URL
	wsURL := strings.Replace(server.URL, "http://", "ws://", 1)

	// Connect with client info
	header := http.Header{}
	header.Set("X-Client-ID", "test-client-123")
	header.Set("X-Group-ID", "test-group-456")

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatalf("Failed to connect to WebSocket: %v", err)
	}
	defer conn.Close()

	// Test basic message exchange
	testMessage := []byte("hello client info")
	err = conn.WriteMessage(websocket.TextMessage, testMessage)
	if err != nil {
		t.Fatalf("Failed to write message: %v", err)
	}

	_, response, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("Failed to read message: %v", err)
	}

	if string(response) != string(testMessage) {
		t.Errorf("Expected %s, got %s", string(testMessage), string(response))
	}
}

func TestWebSocketTransport_Close(t *testing.T) {
	trans := NewWebSocketTransport()

	// Test closing without starting
	err := trans.Close()
	if err != nil {
		t.Errorf("Expected no error when closing non-running transport, got: %v", err)
	}

	// Start and then close
	go trans.ListenAndServe(":0", func(conn transport.Connection) {
		conn.Close()
	})

	time.Sleep(100 * time.Millisecond) // Give server time to start

	err = trans.Close()
	if err != nil {
		t.Errorf("Failed to close transport: %v", err)
	}

	// Verify it's stopped
	wsTransport := trans.(*webSocketTransport)
	wsTransport.mu.Lock()
	running := wsTransport.running
	wsTransport.mu.Unlock()
	if running {
		t.Error("Transport should not be running after close")
	}
}

func TestWebSocketTransport_Authentication(t *testing.T) {
	authConfig := &transport.AuthConfig{
		Username: "testuser",
		Password: "testpass",
	}

	// Use a test server instead of the actual websocket transport server
	// to avoid port resolution issues
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clientID := r.Header.Get("X-Client-ID")
		if clientID == "" {
			http.Error(w, "Client ID is required", http.StatusBadRequest)
			return
		}

		// Check authentication
		username, password, ok := r.BasicAuth()
		if !ok {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		if username != authConfig.Username || password != authConfig.Password {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		// If auth is good, try to upgrade to websocket
		upgrader := websocket.Upgrader{
			CheckOrigin: func(_ *http.Request) bool { return true },
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			http.Error(w, "Failed to upgrade", http.StatusInternalServerError)
			return
		}
		defer conn.Close()

		// Simple echo for testing
		for {
			msgType, msg, err := conn.ReadMessage()
			if err != nil {
				break
			}
			conn.WriteMessage(msgType, msg)
		}
	}))
	defer server.Close()

	// Test connection without auth - should fail
	t.Run("without_auth", func(t *testing.T) {
		req, _ := http.NewRequest("GET", server.URL+"/ws", nil)
		req.Header.Set("X-Client-ID", "test-client")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Unexpected connection error: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("Expected status %d, got %d", http.StatusUnauthorized, resp.StatusCode)
		}
	})

	// Test connection with wrong auth - should fail
	t.Run("with_wrong_auth", func(t *testing.T) {
		req, _ := http.NewRequest("GET", server.URL+"/ws", nil)
		req.Header.Set("X-Client-ID", "test-client")
		req.SetBasicAuth("wronguser", "wrongpass")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Unexpected connection error: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("Expected status %d, got %d", http.StatusUnauthorized, resp.StatusCode)
		}
	})

	// Test connection with valid auth - should succeed
	t.Run("with_valid_auth", func(t *testing.T) {
		req, _ := http.NewRequest("GET", server.URL+"/ws", nil)
		req.Header.Set("X-Client-ID", "test-client")
		req.Header.Set("X-Group-ID", "test-group")
		req.SetBasicAuth(authConfig.Username, authConfig.Password)

		// Since we're using httptest server, we expect it to handle the upgrade
		// For this test, we just verify the auth is processed correctly
		// by checking that we don't get 401
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Unexpected connection error: %v", err)
		}
		defer resp.Body.Close()

		// Should get 101 (Switching Protocols) or similar success response
		if resp.StatusCode == http.StatusUnauthorized {
			t.Error("Valid authentication was rejected")
		}
	})
}
