package gateway

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/buhuipao/anyproxy/pkg/common/credential"
	"github.com/buhuipao/anyproxy/pkg/common/utils"
	"github.com/buhuipao/anyproxy/pkg/config"
	"github.com/buhuipao/anyproxy/pkg/transport"

	// Import SQLite driver for testing
	_ "modernc.org/sqlite" // Pure Go SQLite driver (no CGO required)
)

// mockAddr implements net.Addr
type mockAddr struct {
	network string
	address string
}

func (a mockAddr) Network() string { return a.network }
func (a mockAddr) String() string  { return a.address }

// Mock transport implementation
type mockTransport struct {
	listenAddr   string
	handler      func(transport.Connection)
	closed       bool
	mu           sync.Mutex
	listenErr    error
	listenTLSErr error
	closeErr     error
}

func (m *mockTransport) ListenAndServe(addr string, handler func(transport.Connection)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.listenAddr = addr
	m.handler = handler
	return m.listenErr
}

func (m *mockTransport) ListenAndServeWithTLS(addr string, handler func(transport.Connection), tlsConfig *tls.Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.listenAddr = addr
	m.handler = handler
	return m.listenTLSErr
}

func (m *mockTransport) Dial(addr string) (transport.Connection, error) {
	return &mockConnection{}, nil
}

func (m *mockTransport) DialWithTLS(addr string, tlsConfig *tls.Config) (transport.Connection, error) {
	return &mockConnection{}, nil
}

func (m *mockTransport) DialWithConfig(addr string, config *transport.ClientConfig) (transport.Connection, error) {
	return &mockConnection{
		clientID: config.ClientID,
		groupID:  config.GroupID,
	}, nil
}

func (m *mockTransport) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return m.closeErr
}

func (m *mockTransport) Name() string {
	return "mock"
}

// Mock connection implementation
type mockConnection struct {
	clientID string
	groupID  string
	password string
	closed   bool
	mu       sync.Mutex
	readErr  error
	writeErr error
	readChan chan struct{}
}

func (m *mockConnection) Read(p []byte) (n int, err error) {
	if m.readChan != nil {
		<-m.readChan
	}
	return 0, m.readErr
}

func (m *mockConnection) Write(p []byte) (n int, err error) {
	return len(p), m.writeErr
}

func (m *mockConnection) WriteMessage(data []byte) error {
	return m.writeErr
}

func (m *mockConnection) ReadMessage() ([]byte, error) {
	if m.readChan != nil {
		<-m.readChan
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.readErr != nil {
		return nil, m.readErr
	}
	// Return a valid binary message (ping)
	return []byte{0xAB, 0xCD, 0x01, 0x00, 0x00, 0x00, 0x00}, nil
}

func (m *mockConnection) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

func (m *mockConnection) LocalAddr() net.Addr {
	return mockAddr{network: "tcp", address: "127.0.0.1:8080"}
}

func (m *mockConnection) RemoteAddr() net.Addr {
	return mockAddr{network: "tcp", address: "127.0.0.1:12345"}
}

func (m *mockConnection) GetClientID() string {
	return m.clientID
}

func (m *mockConnection) GetGroupID() string {
	return m.groupID
}

func (m *mockConnection) GetPassword() string {
	return m.password
}

func (m *mockConnection) SetDeadline(t time.Time) error {
	return nil
}

func (m *mockConnection) SetReadDeadline(t time.Time) error {
	return nil
}

func (m *mockConnection) SetWriteDeadline(t time.Time) error {
	return nil
}

// Mock proxy implementation
type mockProxy struct {
	started  bool
	stopped  bool
	startErr error
	stopErr  error
}

func (m *mockProxy) Start() error {
	m.started = true
	return m.startErr
}

func (m *mockProxy) Stop() error {
	m.stopped = true
	return m.stopErr
}

func TestNewGateway(t *testing.T) {
	tests := []struct {
		name          string
		config        *config.Config
		transportType string
		expectError   bool
		errorContains string
	}{
		{
			name: "successful creation with HTTP proxy",
			config: &config.Config{
				Gateway: config.GatewayConfig{
					ListenAddr: ":8080",
					Proxy: config.ProxyConfig{
						HTTP: config.HTTPConfig{
							ListenAddr: ":8081",
						},
					},
				},
			},
			transportType: "mock",
			expectError:   false,
		},
		{
			name: "successful creation with SOCKS5 proxy",
			config: &config.Config{
				Gateway: config.GatewayConfig{
					ListenAddr: ":8080",
					Proxy: config.ProxyConfig{
						SOCKS5: config.SOCKS5Config{
							ListenAddr: ":8082",
						},
					},
				},
			},
			transportType: "mock",
			expectError:   false,
		},
		{
			name: "successful creation with both proxies",
			config: &config.Config{
				Gateway: config.GatewayConfig{
					ListenAddr: ":8080",
					Proxy: config.ProxyConfig{
						HTTP: config.HTTPConfig{
							ListenAddr: ":8081",
						},
						SOCKS5: config.SOCKS5Config{
							ListenAddr: ":8082",
						},
					},
				},
			},
			transportType: "mock",
			expectError:   false,
		},
		{
			name: "error when no proxy configured",
			config: &config.Config{
				Gateway: config.GatewayConfig{
					ListenAddr: ":8080",
					Proxy:      config.ProxyConfig{},
				},
			},
			transportType: "mock",
			expectError:   true,
			errorContains: "no proxy configured",
		},
		{
			name: "error when transport creation fails",
			config: &config.Config{
				Gateway: config.GatewayConfig{
					ListenAddr: ":8080",
					Proxy: config.ProxyConfig{
						HTTP: config.HTTPConfig{
							ListenAddr: ":8081",
						},
					},
				},
			},
			transportType: "invalid",
			expectError:   true,
			errorContains: "failed to create transport",
		},
	}

	// Register mock transport for testing
	transport.RegisterTransportCreator("mock", func(authConfig *transport.AuthConfig) transport.Transport {
		return &mockTransport{}
	})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gw, err := NewGateway(tt.config, tt.transportType)

			if tt.expectError {
				if err == nil {
					t.Error("Expected error but got nil")
				}
				if tt.errorContains != "" && err != nil {
					if !containsString(err.Error(), tt.errorContains) {
						t.Errorf("Error message should contain %q, got %q", tt.errorContains, err.Error())
					}
				}
				if gw != nil {
					t.Error("Gateway should be nil on error")
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
				if gw == nil {
					t.Error("Gateway should not be nil")
				} else {
					if gw.transport == nil {
						t.Error("Transport should not be nil")
					}
					if gw.clients == nil {
						t.Error("Clients map should not be nil")
					}
					if gw.groups == nil {
						t.Error("Groups map should not be nil")
					}

					if gw.portForwardMgr == nil {
						t.Error("PortForwardMgr should not be nil")
					}
					if len(gw.proxies) == 0 {
						t.Error("Should have at least one proxy")
					}

					// Cleanup
					gw.cancel()
				}
			}
		})
	}
}

func TestGateway_ClientManagement(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	credentialMgr, _ := credential.NewManager(&credential.Config{Type: credential.Memory})

	gw := &Gateway{
		clients:        make(map[string]*ClientConn),
		groups:         make(map[string]*GroupInfo),
		portForwardMgr: NewPortForwardManager(),
		credentialMgr:  credentialMgr,
		config:         &config.GatewayConfig{}, // Add config to prevent nil pointer
		ctx:            ctx,
		cancel:         cancel,
	}

	// Test adding clients
	t.Run("add clients", func(t *testing.T) {
		mockConn1 := &mockConnection{
			clientID: "client1",
			groupID:  "group1",
		}

		client1 := &ClientConn{
			ID:             "client1",
			GroupID:        "group1",
			Conn:           mockConn1,
			Conns:          make(map[string]*Conn),
			msgChans:       make(map[string]chan map[string]interface{}),
			ctx:            ctx,
			cancel:         cancel,
			portForwardMgr: gw.portForwardMgr,
		}

		if err := gw.addClient(client1, "pass1"); err != nil {
			t.Fatalf("Failed to add client: %v", err)
		}

		if len(gw.clients) != 1 {
			t.Errorf("Expected 1 client, got %d", len(gw.clients))
		}
		if gw.clients["client1"] != client1 {
			t.Error("Client1 not found in clients map")
		}
		if !containsSlice(gw.groups["group1"].Clients, "client1") {
			t.Error("Client1 not found in group1 clients")
		}

		// Add another client to the same group
		mockConn2 := &mockConnection{
			clientID: "client2",
			groupID:  "group1",
		}

		client2 := &ClientConn{
			ID:             "client2",
			GroupID:        "group1",
			Conn:           mockConn2,
			Conns:          make(map[string]*Conn),
			msgChans:       make(map[string]chan map[string]interface{}),
			ctx:            ctx,
			cancel:         cancel,
			portForwardMgr: gw.portForwardMgr,
		}

		if err := gw.addClient(client2, "pass1"); err != nil {
			t.Fatalf("Failed to add client: %v", err)
		}

		if len(gw.clients) != 2 {
			t.Errorf("Expected 2 clients, got %d", len(gw.clients))
		}
		if len(gw.groups["group1"].Clients) != 2 {
			t.Errorf("Expected 2 clients in group1, got %d", len(gw.groups["group1"].Clients))
		}
	})

	// Test getting client by group with round-robin
	t.Run("get client by group with round-robin", func(t *testing.T) {
		// First call should return client1
		client, err := gw.getClientByGroup("group1")
		if err != nil {
			t.Errorf("Unexpected error: %v", err)
		}
		if client == nil || client.ID != "client1" {
			t.Errorf("Expected client1, got %v", client)
		}

		// Second call should return client2 (round-robin)
		client, err = gw.getClientByGroup("group1")
		if err != nil {
			t.Errorf("Unexpected error: %v", err)
		}
		if client == nil || client.ID != "client2" {
			t.Errorf("Expected client2, got %v", client)
		}

		// Third call should return client1 again
		client, err = gw.getClientByGroup("group1")
		if err != nil {
			t.Errorf("Unexpected error: %v", err)
		}
		if client == nil || client.ID != "client1" {
			t.Errorf("Expected client1, got %v", client)
		}

		// Test non-existent group
		_, err = gw.getClientByGroup("nonexistent")
		if err == nil {
			t.Error("Expected error for non-existent group")
		}
		if !containsString(err.Error(), "no clients available") {
			t.Errorf("Error should contain 'no clients available', got %v", err)
		}
	})

	// Test removing clients
	t.Run("remove clients", func(t *testing.T) {
		gw.removeClient(gw.clients["client1"])

		if len(gw.clients) != 1 {
			t.Errorf("Expected 1 client after removal, got %d", len(gw.clients))
		}
		if _, ok := gw.clients["client1"]; ok {
			t.Error("Client1 should have been removed")
		}
		if len(gw.groups["group1"].Clients) != 1 {
			t.Errorf("Expected 1 client in group1, got %d", len(gw.groups["group1"].Clients))
		}
		if containsSlice(gw.groups["group1"].Clients, "client1") {
			t.Error("Client1 should not be in group1 clients")
		}

		// Remove last client from group
		gw.removeClient(gw.clients["client2"])

		if len(gw.clients) != 0 {
			t.Errorf("Expected 0 clients, got %d", len(gw.clients))
		}
		if _, ok := gw.groups["group1"]; ok {
			t.Error("Group1 should have been removed")
		}

		// Test removing non-existent client
		gw.removeClient(&ClientConn{ID: "nonexistent"})
		if len(gw.clients) != 0 {
			t.Error("Client count should remain 0")
		}
	})

	// Memory credentials come from the clients, so they are removed with the last client
	t.Run("memory group credentials are removed when group becomes empty", func(t *testing.T) {
		// Create and add a client with group credentials
		mockConn3 := &mockConnection{
			clientID: "client3",
			groupID:  "temp-group",
			password: "temp-pass",
		}

		client3 := &ClientConn{
			ID:             "client3",
			GroupID:        "temp-group",
			Conn:           mockConn3,
			Conns:          make(map[string]*Conn),
			msgChans:       make(map[string]chan map[string]interface{}),
			ctx:            ctx,
			cancel:         cancel,
			portForwardMgr: gw.portForwardMgr,
		}

		// Register group credentials and add client
		err := gw.credentialMgr.RegisterGroup("temp-group", "temp-pass")
		if err != nil {
			t.Errorf("Failed to register group credentials: %v", err)
		}
		// Initialize group info
		gw.groupsMu.Lock()
		gw.groups["temp-group"] = &GroupInfo{
			Clients: make([]string, 0),
		}
		gw.groupsMu.Unlock()
		if err := gw.addClient(client3, "temp-pass"); err != nil {
			t.Fatalf("Failed to add client: %v", err)
		}

		// Verify group info exists
		groupInfo, exists := gw.groups["temp-group"]
		if !exists {
			t.Error("Expected group 'temp-group' to exist")
		} else if len(groupInfo.Clients) != 1 {
			t.Errorf("Expected 1 client, got %d", len(groupInfo.Clients))
		}

		// Remove the client (last one in the group)
		gw.removeClient(client3)

		// Verify group is cleaned up when empty
		gw.groupsMu.RLock()
		if _, exists := gw.groups["temp-group"]; exists {
			t.Error("Group should have been cleaned up when it became empty")
		}
		gw.groupsMu.RUnlock()

		if gw.credentialMgr.ValidateGroup("temp-group", "temp-pass") {
			t.Error("Memory credentials should be removed with the last client")
		}

		// A reconnecting client registers its password again
		err = gw.credentialMgr.RegisterGroup("temp-group", "new-pass")
		if err != nil {
			t.Errorf("Client should be able to register again: %v", err)
		}
		if !gw.credentialMgr.ValidateGroup("temp-group", "new-pass") {
			t.Error("New password should be valid in credential manager")
		}

		// Group info in gateway is only created when a client connects
		gw.groupsMu.RLock()
		if _, exists := gw.groups["temp-group"]; exists {
			t.Error("Group should not exist in gateway until a client connects")
		}
		gw.groupsMu.RUnlock()
	})

	// File credentials are pre-configured, so they stay when the group becomes empty
	t.Run("file group credentials persist when group becomes empty", func(t *testing.T) {
		fileMgr, err := credential.NewManager(&credential.Config{
			Type:     credential.File,
			FilePath: filepath.Join(t.TempDir(), "credentials.json"),
		})
		if err != nil {
			t.Fatalf("Failed to create file credential manager: %v", err)
		}
		if err := fileMgr.RegisterGroup("file-group", "file-pass"); err != nil {
			t.Fatalf("Failed to register group credentials: %v", err)
		}

		fileGw := &Gateway{
			clients:        make(map[string]*ClientConn),
			groups:         make(map[string]*GroupInfo),
			portForwardMgr: NewPortForwardManager(),
			credentialMgr:  fileMgr,
			config:         &config.GatewayConfig{},
			ctx:            ctx,
			cancel:         cancel,
		}

		client4 := &ClientConn{
			ID:             "client4",
			GroupID:        "file-group",
			Conn:           &mockConnection{clientID: "client4", groupID: "file-group"},
			Conns:          make(map[string]*Conn),
			msgChans:       make(map[string]chan map[string]interface{}),
			ctx:            ctx,
			cancel:         cancel,
			portForwardMgr: fileGw.portForwardMgr,
		}
		if err := fileGw.addClient(client4, "file-pass"); err != nil {
			t.Fatalf("Failed to add client: %v", err)
		}
		fileGw.removeClient(client4)

		fileGw.groupsMu.RLock()
		if _, exists := fileGw.groups["file-group"]; exists {
			t.Error("Group should have been cleaned up when it became empty")
		}
		fileGw.groupsMu.RUnlock()

		if !fileMgr.ValidateGroup("file-group", "file-pass") {
			t.Error("File credentials should persist when the group becomes empty")
		}
	})
}

func TestGateway_StartStop(t *testing.T) {
	// Register mock transport
	transport.RegisterTransportCreator("mock", func(authConfig *transport.AuthConfig) transport.Transport {
		return &mockTransport{}
	})

	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			ListenAddr: ":8080",
			Proxy: config.ProxyConfig{
				HTTP: config.HTTPConfig{
					ListenAddr: ":8081",
				},
			},
		},
	}

	t.Run("start and stop without TLS", func(t *testing.T) {
		gw, err := NewGateway(cfg, "mock")
		if err != nil {
			t.Fatalf("Failed to create gateway: %v", err)
		}

		// Replace transport with mock
		mockTrans := &mockTransport{}
		gw.transport = mockTrans

		// Replace proxies with mocks
		mockProxy := &mockProxy{}
		gw.proxies = []utils.GatewayProxy{mockProxy}

		// Start gateway
		err = gw.Start()
		if err != nil {
			t.Errorf("Start() error = %v", err)
		}

		// Verify transport was started
		if mockTrans.listenAddr != ":8080" {
			t.Errorf("Transport listen addr = %q, want %q", mockTrans.listenAddr, ":8080")
		}
		if !mockProxy.started {
			t.Error("Proxy should be started")
		}

		// Stop gateway
		err = gw.Stop()
		if err != nil {
			t.Errorf("Stop() error = %v", err)
		}

		if !mockTrans.closed {
			t.Error("Transport should be closed")
		}
		if !mockProxy.stopped {
			t.Error("Proxy should be stopped")
		}
	})

	t.Run("proxy start failure", func(t *testing.T) {
		gw, err := NewGateway(cfg, "mock")
		if err != nil {
			t.Fatalf("Failed to create gateway: %v", err)
		}

		// Replace transport with mock
		mockTrans := &mockTransport{}
		gw.transport = mockTrans

		// Replace proxies with failing mocks
		mockProxy1 := &mockProxy{}
		mockProxy2 := &mockProxy{startErr: errors.New("start failed")}
		gw.proxies = []utils.GatewayProxy{mockProxy1, mockProxy2}

		// Start gateway - should fail
		err = gw.Start()
		if err == nil {
			t.Error("Expected error when proxy fails to start")
		}

		// Verify first proxy was stopped on cleanup
		if !mockProxy1.stopped {
			t.Error("First proxy should be stopped on cleanup")
		}
	})
}

func TestGateway_HandleConnection(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create credential manager for testing
	credentialMgr, err := credential.NewManager(&credential.Config{Type: credential.Memory})
	if err != nil {
		t.Fatalf("Failed to create credential manager: %v", err)
	}

	gw := &Gateway{
		clients:        make(map[string]*ClientConn),
		groups:         make(map[string]*GroupInfo),
		portForwardMgr: NewPortForwardManager(),
		credentialMgr:  credentialMgr,
		config:         &config.GatewayConfig{}, // Add config to prevent nil pointer
		ctx:            ctx,
		cancel:         cancel,
	}

	t.Run("handle new connection", func(t *testing.T) {
		mockConn := &mockConnection{
			clientID: "test-client",
			groupID:  "test-group",
			password: "test-password",
			readChan: make(chan struct{}),
		}

		// Use a channel to signal when connection handling is done
		done := make(chan struct{})

		go func() {
			gw.handleConnection(mockConn)
			close(done)
		}()

		// Poll for client to be added
		var clientFound bool
		for i := 0; i < 20; i++ {
			time.Sleep(50 * time.Millisecond)
			gw.clientsMu.RLock()
			if _, ok := gw.clients["test-client"]; ok {
				clientFound = true
				gw.clientsMu.RUnlock()
				break
			}
			gw.clientsMu.RUnlock()
		}

		if !clientFound {
			t.Fatal("timeout waiting for client to be added")
		}

		// Verify client was added
		gw.clientsMu.RLock()
		if len(gw.clients) != 1 {
			t.Errorf("Expected 1 client, got %d", len(gw.clients))
		}
		if _, ok := gw.clients["test-client"]; !ok {
			t.Error("test-client not found in clients map")
		}
		gw.clientsMu.RUnlock()

		// Now trigger connection close by returning error on read
		mockConn.mu.Lock()
		mockConn.readErr = context.Canceled
		mockConn.mu.Unlock()
		close(mockConn.readChan)

		// Wait for connection handling to complete
		select {
		case <-done:
			// Success
		case <-time.After(2 * time.Second):
			t.Fatal("timeout waiting for connection handling to complete")
		}

		// Verify client was removed
		gw.clientsMu.RLock()
		if len(gw.clients) != 0 {
			t.Errorf("Expected 0 clients after cleanup, got %d", len(gw.clients))
		}
		gw.clientsMu.RUnlock()
	})
}

// addClient checks the password and joins the group under one lock, so a client
// never joins a memory group whose credentials were removed after its check.
func TestGateway_AddClientRegistersGroupPassword(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	credentialMgr, err := credential.NewManager(&credential.Config{Type: credential.Memory})
	if err != nil {
		t.Fatalf("Failed to create credential manager: %v", err)
	}
	gw := &Gateway{
		clients:        make(map[string]*ClientConn),
		groups:         make(map[string]*GroupInfo),
		portForwardMgr: NewPortForwardManager(),
		credentialMgr:  credentialMgr,
		config:         &config.GatewayConfig{},
		ctx:            ctx,
		cancel:         cancel,
	}
	newClient := func(id, groupID string) *ClientConn {
		return &ClientConn{
			ID:             id,
			GroupID:        groupID,
			Conn:           &mockConnection{clientID: id, groupID: groupID},
			Conns:          make(map[string]*Conn),
			msgChans:       make(map[string]chan map[string]interface{}),
			ctx:            ctx,
			cancel:         cancel,
			portForwardMgr: gw.portForwardMgr,
		}
	}

	// The last client leaves and its memory credentials go; the next client registers them again
	c1 := newClient("c1", "g1")
	if err := gw.addClient(c1, "pw1"); err != nil {
		t.Fatalf("Failed to add client: %v", err)
	}
	gw.removeClient(c1)
	if err := gw.addClient(newClient("c2", "g1"), "pw1"); err != nil {
		t.Fatalf("Failed to add client: %v", err)
	}
	if !credentialMgr.ValidateGroup("g1", "pw1") {
		t.Error("An online group must have its password registered")
	}

	// A client with a different password is rejected and leaves nothing behind
	if err := credentialMgr.RegisterGroup("g2", "pw2"); err != nil {
		t.Fatalf("Failed to register group credentials: %v", err)
	}
	if err := gw.addClient(newClient("c3", "g2"), "evil"); err == nil {
		t.Fatal("Client with a different password should be rejected")
	}
	if _, ok := gw.clients["c3"]; ok {
		t.Error("Rejected client should not be added")
	}
	if _, ok := gw.groups["g2"]; ok {
		t.Error("Rejected client should not create a group entry")
	}
	if !credentialMgr.ValidateGroup("g2", "pw2") || credentialMgr.ValidateGroup("g2", "evil") {
		t.Error("Rejected client must not change the group password")
	}
}

// A client without a group password is rejected for every store type, also for
// a group whose password is stored; otherwise it could join that group.
func TestGateway_EmptyGroupPassword(t *testing.T) {
	tests := []struct {
		name      string
		config    *credential.Config
		preconfig bool
	}{
		{name: "memory store", config: &credential.Config{Type: credential.Memory}, preconfig: true},
		{name: "file store, stored group", config: &credential.Config{Type: credential.File}, preconfig: true},
		{name: "file store, unknown group", config: &credential.Config{Type: credential.File}, preconfig: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cfg := *tt.config
			cfg.FilePath = filepath.Join(t.TempDir(), "credentials.json")
			credentialMgr, err := credential.NewManager(&cfg)
			if err != nil {
				t.Fatalf("Failed to create credential manager: %v", err)
			}
			if tt.preconfig {
				// For memory this is the password of a client that is already online;
				// for file it may be pre-configured or registered by another client
				if err := credentialMgr.RegisterGroup("g1", "pw1"); err != nil {
					t.Fatalf("Failed to register group credentials: %v", err)
				}
			}
			gw := &Gateway{
				clients:        make(map[string]*ClientConn),
				groups:         make(map[string]*GroupInfo),
				portForwardMgr: NewPortForwardManager(),
				credentialMgr:  credentialMgr,
				config:         &config.GatewayConfig{},
				ctx:            ctx,
				cancel:         cancel,
			}
			client := &ClientConn{
				ID:             "c1",
				GroupID:        "g1",
				Conn:           &mockConnection{clientID: "c1", groupID: "g1"},
				Conns:          make(map[string]*Conn),
				msgChans:       make(map[string]chan map[string]interface{}),
				ctx:            ctx,
				cancel:         cancel,
				portForwardMgr: gw.portForwardMgr,
			}

			if err := gw.addClient(client, ""); err == nil {
				t.Fatal("Client without a group password should be rejected")
			}
			if _, joined := gw.clients["c1"]; joined {
				t.Error("Rejected client should not join the group")
			}
		})
	}
}

// liveConn blocks reads until it is closed, like an open transport connection.
type liveConn struct {
	mockConnection
	done chan struct{}
	once sync.Once
}

func newLiveConn(clientID, groupID, password string) *liveConn {
	return &liveConn{
		mockConnection: mockConnection{clientID: clientID, groupID: groupID, password: password},
		done:           make(chan struct{}),
	}
}

func (c *liveConn) ReadMessage() ([]byte, error) {
	<-c.done
	return nil, io.EOF
}

func (c *liveConn) Close() error {
	c.once.Do(func() { close(c.done) })
	return nil
}

// A client that reconnects with the same ID before the gateway notices the old
// connection is gone must stay routable, also when it moves to another group.
func TestGateway_ReconnectWithSameClientID(t *testing.T) {
	tests := []struct {
		name     string
		newGroup string
		newPass  string
	}{
		{name: "same group", newGroup: "group-a", newPass: "pass-a"},
		{name: "other group", newGroup: "group-b", newPass: "pass-b"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			credentialMgr, err := credential.NewManager(&credential.Config{Type: credential.Memory})
			if err != nil {
				t.Fatalf("Failed to create credential manager: %v", err)
			}
			gw := &Gateway{
				clients:        make(map[string]*ClientConn),
				groups:         make(map[string]*GroupInfo),
				portForwardMgr: NewPortForwardManager(),
				credentialMgr:  credentialMgr,
				config:         &config.GatewayConfig{},
				ctx:            ctx,
				cancel:         cancel,
			}
			serve := func(conn *liveConn) chan struct{} {
				done := make(chan struct{})
				go func() {
					gw.handleConnection(conn)
					close(done)
				}()
				return done
			}
			waitDone := func(done chan struct{}, what string) {
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatalf("timeout waiting for %s", what)
				}
			}

			oldConn := newLiveConn("client-1", "group-a", "pass-a")
			oldDone := serve(oldConn)
			for i := 0; ; i++ {
				gw.clientsMu.RLock()
				_, added := gw.clients["client-1"]
				gw.clientsMu.RUnlock()
				if added {
					break
				}
				if i == 100 {
					t.Fatal("timeout waiting for old client to be added")
				}
				time.Sleep(10 * time.Millisecond)
			}

			newConn := newLiveConn("client-1", tt.newGroup, tt.newPass)
			newDone := serve(newConn)
			// The old connection is stopped and its handler cleans up
			waitDone(oldDone, "old connection to stop")

			client, err := gw.getClientByGroup(tt.newGroup)
			if err != nil || client.Conn != newConn {
				t.Fatalf("New connection should be routable, got client=%v err=%v", client, err)
			}
			gw.groupsMu.RLock()
			members := len(gw.groups[tt.newGroup].Clients)
			gw.groupsMu.RUnlock()
			if members != 1 {
				t.Errorf("Expected 1 member in %s, got %d", tt.newGroup, members)
			}
			if !credentialMgr.ValidateGroup(tt.newGroup, tt.newPass) {
				t.Errorf("Credentials of %s should stay while it has a client", tt.newGroup)
			}
			if tt.newGroup != "group-a" {
				if _, err := gw.getClientByGroup("group-a"); err == nil {
					t.Error("Old group should have no clients")
				}
				if credentialMgr.ValidateGroup("group-a", "pass-a") {
					t.Error("Old group credentials should be removed with its last client")
				}
			}

			_ = newConn.Close()
			waitDone(newDone, "new connection to stop")
		})
	}
}

// Helper functions
func containsString(s string, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func containsSlice(slice []string, str string) bool {
	for _, s := range slice {
		if s == str {
			return true
		}
	}
	return false
}

func TestGateway_RegisterGroupCredentials(t *testing.T) {
	// Create minimal config for testing
	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			ListenAddr:   ":8443",
			AuthUsername: "test",
			AuthPassword: "test",
			Proxy: config.ProxyConfig{
				HTTP: config.HTTPConfig{
					ListenAddr: ":8080",
				},
			},
		},
	}

	gateway, err := NewGateway(cfg, "grpc")
	if err != nil {
		t.Fatalf("Failed to create gateway: %v", err)
	}

	tests := []struct {
		name        string
		groupID     string
		password    string
		expectError bool
	}{
		{
			name:        "register new group",
			groupID:     "group1",
			password:    "pass1",
			expectError: false,
		},
		{
			name:        "register same group with same password",
			groupID:     "group1",
			password:    "pass1",
			expectError: false,
		},
		{
			name:        "register same group with different password",
			groupID:     "group1",
			password:    "pass2",
			expectError: true, // The group already has a different password
		},
		{
			name:        "register different group",
			groupID:     "group2",
			password:    "pass2",
			expectError: false,
		},
		{
			name:        "reject empty group ID",
			groupID:     "",
			password:    "somepass",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := gateway.credentialMgr.RegisterGroup(tt.groupID, tt.password)
			if (err != nil) != tt.expectError {
				t.Errorf("RegisterGroup() error = %v, expectError %v", err, tt.expectError)
			}
		})
	}
}

func TestGateway_ValidateGroupCredentials(t *testing.T) {
	// Create minimal config for testing
	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			ListenAddr:   ":8443",
			AuthUsername: "test",
			AuthPassword: "test",
			Proxy: config.ProxyConfig{
				HTTP: config.HTTPConfig{
					ListenAddr: ":8080",
				},
			},
		},
	}

	gateway, err := NewGateway(cfg, "grpc")
	if err != nil {
		t.Fatalf("Failed to create gateway: %v", err)
	}

	// Register some test groups
	gateway.credentialMgr.RegisterGroup("testgroup", "testpass")
	gateway.credentialMgr.RegisterGroup("anothergroup", "anotherpass")

	tests := []struct {
		name     string
		groupID  string
		password string
		expected bool
	}{
		{
			name:     "valid credentials",
			groupID:  "testgroup",
			password: "testpass",
			expected: true,
		},
		{
			name:     "invalid password",
			groupID:  "testgroup",
			password: "wrongpass",
			expected: false,
		},
		{
			name:     "unknown group",
			groupID:  "unknowngroup",
			password: "anypass",
			expected: false,
		},
		{
			name:     "another valid group",
			groupID:  "anothergroup",
			password: "anotherpass",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := gateway.credentialMgr.ValidateGroup(tt.groupID, tt.password)
			if result != tt.expected {
				t.Errorf("ValidateGroup() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

func TestGateway_NewGatewayCreation(t *testing.T) {
	tests := []struct {
		name          string
		config        *config.Config
		transportType string
		expectError   bool
	}{
		{
			name: "valid gateway with HTTP proxy",
			config: &config.Config{
				Gateway: config.GatewayConfig{
					ListenAddr:   ":8443",
					AuthUsername: "test",
					AuthPassword: "test",
					Proxy: config.ProxyConfig{
						HTTP: config.HTTPConfig{
							ListenAddr: ":8080",
						},
					},
				},
			},
			transportType: "grpc",
			expectError:   false,
		},
		{
			name: "valid gateway with SOCKS5 proxy",
			config: &config.Config{
				Gateway: config.GatewayConfig{
					ListenAddr:   ":8443",
					AuthUsername: "test",
					AuthPassword: "test",
					Proxy: config.ProxyConfig{
						SOCKS5: config.SOCKS5Config{
							ListenAddr: ":1080",
						},
					},
				},
			},
			transportType: "grpc",
			expectError:   false,
		},
		{
			name: "valid gateway with TUIC proxy",
			config: &config.Config{
				Gateway: config.GatewayConfig{
					ListenAddr:   ":8443",
					AuthUsername: "test",
					AuthPassword: "test",
					TLSCert:      "/path/to/cert.pem",
					TLSKey:       "/path/to/key.pem",
					Proxy: config.ProxyConfig{
						TUIC: config.TUICConfig{
							ListenAddr: ":9443",
						},
					},
				},
			},
			transportType: "grpc",
			expectError:   false,
		},
		{
			name: "no proxy configured",
			config: &config.Config{
				Gateway: config.GatewayConfig{
					ListenAddr:   ":8443",
					AuthUsername: "test",
					AuthPassword: "test",
					Proxy:        config.ProxyConfig{},
				},
			},
			transportType: "grpc",
			expectError:   true,
		},
		{
			name: "invalid transport type",
			config: &config.Config{
				Gateway: config.GatewayConfig{
					ListenAddr:   ":8443",
					AuthUsername: "test",
					AuthPassword: "test",
					Proxy: config.ProxyConfig{
						HTTP: config.HTTPConfig{
							ListenAddr: ":8080",
						},
					},
				},
			},
			transportType: "invalid",
			expectError:   true,
		},
		{
			name: "gateway with file credential",
			config: &config.Config{
				Gateway: config.GatewayConfig{
					ListenAddr:   ":8443",
					AuthUsername: "test",
					AuthPassword: "test",
					Credential: &config.CredentialConfig{
						Type:     "file",
						FilePath: "/tmp/test-creds.json",
					},
					Proxy: config.ProxyConfig{
						HTTP: config.HTTPConfig{
							ListenAddr: ":8080",
						},
					},
				},
			},
			transportType: "grpc",
			expectError:   false,
		},
		{
			name: "gateway with db credential",
			config: &config.Config{
				Gateway: config.GatewayConfig{
					ListenAddr:   ":8443",
					AuthUsername: "test",
					AuthPassword: "test",
					Credential: &config.CredentialConfig{
						Type: "db",
						DB: &config.CredentialDBConfig{
							Driver:     "sqlite", // modernc.org/sqlite uses "sqlite" not "sqlite3"
							DataSource: ":memory:",
							TableName:  "test_credentials",
						},
					},
					Proxy: config.ProxyConfig{
						HTTP: config.HTTPConfig{
							ListenAddr: ":8080",
						},
					},
				},
			},
			transportType: "grpc",
			expectError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gateway, err := NewGateway(tt.config, tt.transportType)
			if (err != nil) != tt.expectError {
				t.Errorf("NewGateway() error = %v, expectError %v", err, tt.expectError)
				return
			}
			if !tt.expectError && gateway == nil {
				t.Error("NewGateway() returned nil gateway without error")
			}
		})
	}
}

func TestGateway_ClientRestartWithNewPassword(t *testing.T) {
	// Create minimal config for testing
	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			ListenAddr:   ":8443",
			AuthUsername: "test",
			AuthPassword: "test",
			Proxy: config.ProxyConfig{
				HTTP: config.HTTPConfig{
					ListenAddr: ":8080",
				},
			},
		},
	}

	gateway, err := NewGateway(cfg, "grpc")
	if err != nil {
		t.Fatalf("Failed to create gateway: %v", err)
	}

	t.Run("client restart with new password should succeed", func(t *testing.T) {
		// Step 1: Client connects with initial password
		err := gateway.credentialMgr.RegisterGroup("test-group", "initial-password")
		if err != nil {
			t.Fatalf("Failed to register initial credentials: %v", err)
		}

		// Initialize group info
		gateway.groupsMu.Lock()
		gateway.groups["test-group"] = &GroupInfo{
			Clients: make([]string, 0),
		}
		gateway.groupsMu.Unlock()

		// Verify group exists with initial password
		gateway.groupsMu.RLock()
		if _, exists := gateway.groups["test-group"]; !exists {
			t.Fatal("Group should exist after registration")
		}
		gateway.groupsMu.RUnlock()

		// Step 2: Simulate client connection
		mockConn := &mockConnection{
			clientID: "test-client",
			groupID:  "test-group",
			password: "initial-password",
		}

		client := &ClientConn{
			ID:             "test-client",
			GroupID:        "test-group",
			Conn:           mockConn,
			Conns:          make(map[string]*Conn),
			msgChans:       make(map[string]chan map[string]interface{}),
			ctx:            context.Background(),
			cancel:         func() {},
			portForwardMgr: gateway.portForwardMgr,
		}

		if err := gateway.addClient(client, "initial-password"); err != nil {
			t.Fatalf("Failed to add client: %v", err)
		}

		// Verify client is added
		gateway.clientsMu.RLock()
		if len(gateway.clients) != 1 {
			t.Errorf("Expected 1 client, got %d", len(gateway.clients))
		}
		gateway.clientsMu.RUnlock()

		gateway.groupsMu.RLock()
		if len(gateway.groups["test-group"].Clients) != 1 {
			t.Errorf("Expected 1 client in group, got %d", len(gateway.groups["test-group"].Clients))
		}
		gateway.groupsMu.RUnlock()

		// A client with the new password is rejected while the old client is connected
		newConn := &mockConnection{
			clientID: "new-client",
			groupID:  "test-group",
			password: "new-password",
			readErr:  io.EOF,
		}
		gateway.handleConnection(newConn)
		if !gateway.credentialMgr.ValidateGroup("test-group", "initial-password") ||
			gateway.credentialMgr.ValidateGroup("test-group", "new-password") {
			t.Error("Rejected client must not change the group password")
		}

		// Step 3: Client disconnects (simulate client restart)
		gateway.removeClient(client)

		// Verify group is cleaned up when empty
		gateway.groupsMu.RLock()
		if _, exists := gateway.groups["test-group"]; exists {
			t.Error("Group should be cleaned up when it becomes empty")
		}
		gateway.groupsMu.RUnlock()

		gateway.clientsMu.RLock()
		if len(gateway.clients) != 0 {
			t.Errorf("Expected 0 clients after removal, got %d", len(gateway.clients))
		}
		gateway.clientsMu.RUnlock()

		// Memory credentials are removed with the last client of the group
		isValid := gateway.credentialMgr.ValidateGroup("test-group", "initial-password")
		if isValid {
			t.Error("Old password should be removed when the last client disconnects")
		}

		// Step 4: Client reconnects with NEW password (this registers it again)
		err = gateway.credentialMgr.RegisterGroup("test-group", "new-password")
		if err != nil {
			t.Errorf("Client should be able to register with new password: %v", err)
		}

		// Initialize new group info
		gateway.groupsMu.Lock()
		gateway.groups["test-group"] = &GroupInfo{
			Clients: make([]string, 0),
		}
		gateway.groupsMu.Unlock()

		// Verify new password is accepted
		gateway.groupsMu.RLock()
		if _, exists := gateway.groups["test-group"]; !exists {
			t.Error("New group should be registered")
		}
		gateway.groupsMu.RUnlock()

		// Step 5: Verify proxy authentication works with new password
		isValid = gateway.credentialMgr.ValidateGroup("test-group", "new-password")
		if !isValid {
			t.Error("New password should be valid for proxy authentication")
		}

		// Old password should no longer work after update
		isValid = gateway.credentialMgr.ValidateGroup("test-group", "initial-password")
		if isValid {
			t.Error("Old password should not be valid after password update")
		}
	})
}

func TestGateway_RaceConditionFix(t *testing.T) {
	// Create minimal config for testing
	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			ListenAddr:   ":8443",
			AuthUsername: "test",
			AuthPassword: "test",
			Proxy: config.ProxyConfig{
				HTTP: config.HTTPConfig{
					ListenAddr: ":8080",
				},
			},
		},
	}

	gateway, err := NewGateway(cfg, "grpc")
	if err != nil {
		t.Fatalf("Failed to create gateway: %v", err)
	}

	t.Run("credential manager rejects a different password while clients are active", func(t *testing.T) {
		// Step 1: Register first client with initial password
		err := gateway.credentialMgr.RegisterGroup("test-group", "initial-password")
		if err != nil {
			t.Fatalf("Failed to register initial credentials: %v", err)
		}

		// Initialize group info
		gateway.groupsMu.Lock()
		gateway.groups["test-group"] = &GroupInfo{
			Clients: make([]string, 0),
		}
		gateway.groupsMu.Unlock()

		// Step 2: Simulate adding the client
		mockConn := &mockConnection{
			clientID: "test-client",
			groupID:  "test-group",
			password: "initial-password",
		}

		client := &ClientConn{
			ID:             "test-client",
			GroupID:        "test-group",
			Conn:           mockConn,
			Conns:          make(map[string]*Conn),
			msgChans:       make(map[string]chan map[string]interface{}),
			ctx:            context.Background(),
			cancel:         func() {},
			portForwardMgr: gateway.portForwardMgr,
		}

		if err := gateway.addClient(client, "initial-password"); err != nil {
			t.Fatalf("Failed to add client: %v", err)
		}

		// Verify client is added and group has active clients
		gateway.clientsMu.RLock()
		if len(gateway.clients) != 1 {
			t.Errorf("Expected 1 client, got %d", len(gateway.clients))
		}
		gateway.clientsMu.RUnlock()

		gateway.groupsMu.RLock()
		if len(gateway.groups["test-group"].Clients) != 1 {
			t.Errorf("Expected 1 client in group, got %d", len(gateway.groups["test-group"].Clients))
		}
		gateway.groupsMu.RUnlock()

		// Step 3: Register with different password while client is active (rejected)
		err = gateway.credentialMgr.RegisterGroup("test-group", "new-password")
		if err == nil {
			t.Error("Credential manager should reject a different password for a registered group")
		}

		// The old client should still be connected with the gateway's group info unchanged
		gateway.groupsMu.RLock()
		if groupInfo, exists := gateway.groups["test-group"]; !exists {
			t.Error("Group should still exist")
		} else if len(groupInfo.Clients) != 1 {
			t.Errorf("Expected 1 client, got %d", len(groupInfo.Clients))
		}
		gateway.groupsMu.RUnlock()

		// Step 4: Verify credential manager kept the initial password
		isValid := gateway.credentialMgr.ValidateGroup("test-group", "new-password")
		if isValid {
			t.Error("Credential manager should not validate the rejected password")
		}

		isValid = gateway.credentialMgr.ValidateGroup("test-group", "initial-password")
		if !isValid {
			t.Error("Credential manager should still validate the initial password")
		}

		// Step 5: Remove client (simulate client disconnect)
		gateway.removeClient(client)

		// Step 6: Verify group is cleaned up
		gateway.groupsMu.RLock()
		if _, exists := gateway.groups["test-group"]; exists {
			t.Error("Group should be cleaned up when it becomes empty")
		}
		gateway.groupsMu.RUnlock()

		// Step 7: New client connecting with new password should work
		err = gateway.credentialMgr.RegisterGroup("test-group", "new-password")
		if err != nil {
			t.Errorf("Should succeed to register with existing password, got error: %v", err)
		}

		// Initialize new group info
		gateway.groupsMu.Lock()
		gateway.groups["test-group"] = &GroupInfo{
			Clients: make([]string, 0),
		}
		gateway.groupsMu.Unlock()

		// Verify new password is set
		gateway.groupsMu.RLock()
		if _, ok := gateway.groups["test-group"]; !ok {
			t.Error("Group should exist after new registration")
		}
		gateway.groupsMu.RUnlock()

		// Step 8: Verify proxy authentication works with new password
		isValid = gateway.credentialMgr.ValidateGroup("test-group", "new-password")
		if !isValid {
			t.Error("New password should be valid for authentication")
		}

		// Old password should no longer work
		isValid = gateway.credentialMgr.ValidateGroup("test-group", "initial-password")
		if isValid {
			t.Error("Old password should not be valid after password change")
		}
	})
}
