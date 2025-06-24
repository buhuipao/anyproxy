package client

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/buhuipao/anyproxy/pkg/common/connection"
	"github.com/buhuipao/anyproxy/pkg/common/monitoring"
	"github.com/buhuipao/anyproxy/pkg/common/protocol"
	"github.com/buhuipao/anyproxy/pkg/config"
	"github.com/buhuipao/anyproxy/pkg/transport"
)

// mockTransportForConn implements transport.Transport for connection testing
type mockTransportForConn struct {
	dialErr       error
	dialDelay     time.Duration
	dialCallCount int
	mu            sync.Mutex
}

func (m *mockTransportForConn) ListenAndServe(addr string, handler func(transport.Connection)) error {
	return nil
}

func (m *mockTransportForConn) ListenAndServeWithTLS(addr string, handler func(transport.Connection), tlsConfig *tls.Config) error {
	return nil
}

func (m *mockTransportForConn) DialWithConfig(addr string, config *transport.ClientConfig) (transport.Connection, error) {
	m.mu.Lock()
	m.dialCallCount++
	delay := m.dialDelay
	err := m.dialErr
	m.mu.Unlock()

	if delay > 0 {
		time.Sleep(delay)
	}

	if err != nil {
		return nil, err
	}

	return &mockConnectionForTest{
		clientID: config.ClientID,
		groupID:  config.GroupID,
		msgChan:  make(chan map[string]interface{}, 10),
	}, nil
}

func (m *mockTransportForConn) Close() error {
	return nil
}

// mockConnectionForTest implements transport.Connection for testing
type mockConnectionForTest struct {
	clientID  string
	groupID   string
	closed    bool
	msgChan   chan map[string]interface{}
	readErr   error
	writeErr  error
	readCount int // Add counter to track read calls
	mu        sync.Mutex
}

func (m *mockConnectionForTest) ReadMessage() ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Increment read count
	m.readCount++

	if m.readErr != nil {
		return nil, m.readErr
	}

	// After a few reads, return EOF to simulate connection close
	// This prevents infinite retry loops in tests
	if m.readCount > 3 {
		return nil, io.EOF
	}

	// Return a valid binary message format (ping message)
	// Binary format: magic(2) + type(1) + length(4) = 7 bytes minimum
	return []byte{0xAB, 0xCD, 0x01, 0x00, 0x00, 0x00, 0x00}, nil
}

func (m *mockConnectionForTest) WriteMessage(data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.writeErr
}

func (m *mockConnectionForTest) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	close(m.msgChan)
	return nil
}

func (m *mockConnectionForTest) RemoteAddr() net.Addr {
	return mockAddr{network: "tcp", address: "mock:1234"}
}

func (m *mockConnectionForTest) LocalAddr() net.Addr {
	return mockAddr{network: "tcp", address: "mock:5678"}
}

func (m *mockConnectionForTest) GetClientID() string {
	return m.clientID
}

func (m *mockConnectionForTest) GetGroupID() string {
	return m.groupID
}

// Helper function to simulate sending a message
func (m *mockConnectionForTest) simulateMessage(msg map[string]interface{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.closed && m.msgChan != nil {
		m.msgChan <- msg
	}
}

func TestConnectionLoop(t *testing.T) {
	tests := []struct {
		name                string
		dialErr             error
		connectRetries      int
		expectCleanup       bool
		simulateCancel      bool
		simulateCancelAfter time.Duration
	}{
		{
			name:                "immediate cancel",
			dialErr:             nil,
			connectRetries:      0,
			expectCleanup:       true,
			simulateCancel:      true,
			simulateCancelAfter: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock transport
			mockTransport := &mockTransportForConn{
				dialErr: tt.dialErr,
			}

			// Register mock transport
			transport.RegisterTransportCreator("test-conn", func(authConfig *transport.AuthConfig) transport.Transport {
				return mockTransport
			})

			// Create client
			config := &config.ClientConfig{
				ClientID:    "test-client",
				GroupID:     "test-group",
				GatewayAddr: "localhost:8080",
			}

			client, err := NewClient(config, "test-conn", 0)
			if err != nil {
				t.Fatalf("Failed to create client: %v", err)
			}

			// Use proper synchronization
			loopStarted := make(chan struct{})
			loopDone := make(chan struct{})

			// Start connection loop in background
			go func() {
				defer close(loopDone)
				close(loopStarted) // Signal loop has started
				client.connectionLoop()
			}()

			// Wait for loop to start
			<-loopStarted

			// Simulate cancel immediately or after delay
			if tt.simulateCancel {
				if tt.simulateCancelAfter > 0 {
					time.Sleep(tt.simulateCancelAfter)
				}
				client.cancel()
			}

			// Wait for cleanup with timeout
			select {
			case <-loopDone:
				// Loop completed successfully
			case <-time.After(3 * time.Second):
				t.Error("Connection loop did not exit in time")
				client.cancel() // Force cancel
				<-loopDone      // Wait for exit
			}

			// Check retry count
			mockTransport.mu.Lock()
			actualRetries := mockTransport.dialCallCount
			mockTransport.mu.Unlock()

			if tt.dialErr != nil && actualRetries < tt.connectRetries {
				t.Errorf("Expected at least %d retries, got %d", tt.connectRetries, actualRetries)
			}
		})
	}
}

func TestConnect(t *testing.T) {
	tests := []struct {
		name              string
		dialErr           error
		sendPortsErr      error
		expectErr         bool
		openPorts         []config.OpenPort
		simulateResponses []map[string]interface{}
	}{
		{
			name:      "successful connection without ports",
			dialErr:   nil,
			expectErr: false,
		},
		{
			name:      "successful connection with port forwarding",
			dialErr:   nil,
			expectErr: false,
			openPorts: []config.OpenPort{
				{RemotePort: 8080, LocalHost: "localhost", LocalPort: 8080, Protocol: "tcp"},
			},
			simulateResponses: []map[string]interface{}{
				{
					"type":    "port_forward_response",
					"success": true,
				},
			},
		},
		{
			name:      "dial failure",
			dialErr:   errors.New("dial failed"),
			expectErr: true,
		},
		{
			name:      "port forwarding failure",
			dialErr:   nil,
			expectErr: false, // Port forwarding failure doesn't fail the connection
			openPorts: []config.OpenPort{
				{RemotePort: 8080, LocalHost: "localhost", LocalPort: 8080, Protocol: "tcp"},
			},
			simulateResponses: []map[string]interface{}{
				{
					"type":    "port_forward_response",
					"success": false,
					"message": "Port already in use",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock transport
			mockTransport := &mockTransportForConn{
				dialErr: tt.dialErr,
			}

			// Register mock transport
			transport.RegisterTransportCreator("test-connect", func(authConfig *transport.AuthConfig) transport.Transport {
				return mockTransport
			})

			// Create client
			config := &config.ClientConfig{
				ClientID:    "test-client",
				GroupID:     "test-group",
				GatewayAddr: "localhost:8080",
				OpenPorts:   tt.openPorts,
			}

			client, err := NewClient(config, "test-connect", 0)
			if err != nil {
				t.Fatalf("Failed to create client: %v", err)
			}

			// Connect
			err = client.connect()

			// Check error
			if (err != nil) != tt.expectErr {
				t.Errorf("connect() error = %v, expectErr %v", err, tt.expectErr)
			}

			// Simulate responses if connection successful
			if err == nil && client.conn != nil && len(tt.simulateResponses) > 0 {
				mockConn := client.conn.(*mockConnectionForTest)
				for _, resp := range tt.simulateResponses {
					mockConn.simulateMessage(resp)
				}
			}

			// Cleanup
			if client.conn != nil {
				client.conn.Close()
			}
		})
	}
}

func TestCloseAllConnections(t *testing.T) {
	// Create client
	config := &config.ClientConfig{
		ClientID:    "test-client",
		GroupID:     "test-group",
		GatewayAddr: "localhost:8080",
	}

	client := &Client{
		config:  config,
		connMgr: connection.NewManager(config.ClientID),
		ctx:     context.Background(),
	}

	// Create mock connections
	numConns := 5
	for i := 0; i < numConns; i++ {
		connID := fmt.Sprintf("conn-%d", i)

		// Add mock connection
		mockConn := &mockNetConn{id: connID}
		client.connMgr.AddConnection(connID, mockConn)

		// Add message channel
		msgChan := client.connMgr.CreateMessageChannel(connID, protocol.DefaultMessageChannelSize)

		// Add test message to channel
		msgChan <- map[string]interface{}{"test": "data"}
	}

	// Close all connections
	client.connMgr.CloseAllConnections()
	client.connMgr.CloseAllMessageChannels()

	// Verify all connections are closed
	if client.connMgr.GetConnectionCount() != 0 {
		t.Errorf("Expected 0 connections after closeAllConnections, got %d", client.connMgr.GetConnectionCount())
	}

	// Verify all message channels are closed
	if client.connMgr.GetMessageChannelCount() != 0 {
		t.Errorf("Expected 0 message channels after closeAllConnections, got %d", client.connMgr.GetMessageChannelCount())
	}
}

// mockNetConn implements net.Conn for testing
type mockNetConn struct {
	id     string
	closed bool
	mu     sync.Mutex
}

func (m *mockNetConn) Read(b []byte) (n int, err error) {
	return 0, io.EOF
}

func (m *mockNetConn) Write(b []byte) (n int, err error) {
	return len(b), nil
}

func (m *mockNetConn) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

func (m *mockNetConn) LocalAddr() net.Addr {
	return mockAddr{network: "tcp", address: "local:1234"}
}

func (m *mockNetConn) RemoteAddr() net.Addr {
	return mockAddr{network: "tcp", address: "remote:5678"}
}

func (m *mockNetConn) SetDeadline(t time.Time) error {
	return nil
}

func (m *mockNetConn) SetReadDeadline(t time.Time) error {
	return nil
}

func (m *mockNetConn) SetWriteDeadline(t time.Time) error {
	return nil
}

func TestHandleConnection(t *testing.T) {
	tests := []struct {
		name           string
		connID         string
		targetAddr     string
		forbiddenHost  bool
		dialErr        error
		expectClose    bool
		simulateData   [][]byte
		simulateErrors []error
	}{
		{
			name:        "successful connection handling",
			connID:      "test-conn-1",
			targetAddr:  "example.com:80",
			expectClose: true,
			simulateData: [][]byte{
				[]byte("request data"),
				[]byte("response data"),
			},
		},
		{
			name:          "forbidden host",
			connID:        "test-conn-2",
			targetAddr:    "forbidden.com:80",
			forbiddenHost: true,
			expectClose:   true,
		},
		{
			name:        "dial failure",
			connID:      "test-conn-3",
			targetAddr:  "example.com:80",
			dialErr:     errors.New("connection refused"),
			expectClose: true,
		},
		{
			name:        "connection with errors",
			connID:      "test-conn-4",
			targetAddr:  "example.com:80",
			expectClose: true,
			simulateErrors: []error{
				errors.New("read error"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Register mock transport for this test
			transport.RegisterTransportCreator("websocket", func(authConfig *transport.AuthConfig) transport.Transport {
				return &mockTransportForConn{}
			})

			// Create client
			config := &config.ClientConfig{
				ClientID:    "test-client",
				GroupID:     "test-group",
				GatewayAddr: "localhost:8080",
			}

			if tt.forbiddenHost {
				config.ForbiddenHosts = []string{"forbidden.com"}
			}

			client, err := NewClient(config, "websocket", 0)
			if err != nil {
				t.Fatalf("Failed to create client: %v", err)
			}
			client.ctx = context.Background()

			// Create mock connection
			mockConn := &mockConnectionForTest{
				clientID: config.ClientID,
				groupID:  config.GroupID,
			}

			// Set up transport connection
			client.conn = mockConn

			// Create message channel
			msgChan := client.connMgr.CreateMessageChannel(tt.connID, protocol.DefaultMessageChannelSize)

			// Send connect message
			msgChan <- map[string]interface{}{
				"type":    "connect",
				"id":      tt.connID,
				"network": "tcp",
				"address": tt.targetAddr,
			}

			// Handle connection in background
			done := make(chan struct{})
			processingStarted := make(chan struct{})

			go func() {
				defer close(done)
				close(processingStarted) // Signal processing started
				client.handleConnection(tt.connID)
			}()

			// Wait for processing to start
			<-processingStarted

			// Give a moment for initial processing
			time.Sleep(50 * time.Millisecond)

			// Send close message to end the connection
			select {
			case msgChan <- map[string]interface{}{
				"type": "close",
				"id":   tt.connID,
			}:
			case <-time.After(200 * time.Millisecond):
				// If channel is full or closed, continue
			}

			// Wait for completion
			select {
			case <-done:
				// Connection handling completed
			case <-time.After(2 * time.Second):
				t.Error("handleConnection did not complete in time")
			}

			// Verify cleanup
			if _, exists := client.connMgr.GetConnection(tt.connID); exists && tt.expectClose {
				t.Error("Connection was not cleaned up")
			}
		})
	}
}

func TestCleanupConnection(t *testing.T) {
	// Create client
	client := &Client{
		config: &config.ClientConfig{
			ClientID: "test-client",
		},
		connMgr: connection.NewManager("test-client"),
	}

	// Test cleanup of existing connection
	connID := "test-conn"
	mockConn := &mockNetConn{id: connID}
	client.connMgr.AddConnection(connID, mockConn)

	client.connMgr.CreateMessageChannel(connID, protocol.DefaultMessageChannelSize)

	// Add connection metrics
	monitoring.IncrementActiveConnections()

	// Cleanup connection
	client.cleanupConnection(connID)

	// Verify connection removed
	if _, exists := client.connMgr.GetConnection(connID); exists {
		t.Error("Connection not removed from map")
	}

	// Verify channel removed
	if client.connMgr.GetMessageChannelCount() != 0 {
		t.Error("Message channel not removed from map")
	}

	// Verify connection closed
	if !mockConn.closed {
		t.Error("Connection not closed")
	}

	// Test cleanup of non-existent connection (should not panic)
	client.cleanupConnection("non-existent")
}

func TestConnectionConcurrency(t *testing.T) {
	// Create client
	client := &Client{
		config: &config.ClientConfig{
			ClientID: "test-client",
		},
		connMgr: connection.NewManager("test-client"),
		ctx:     context.Background(),
	}

	// Test concurrent access to connections
	var wg sync.WaitGroup
	numGoroutines := 10
	numConnsPerGoroutine := 5

	// Add connections concurrently
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(routineID int) {
			defer wg.Done()

			for j := 0; j < numConnsPerGoroutine; j++ {
				connID := fmt.Sprintf("conn-%d-%d", routineID, j)

				// Add connection
				client.connMgr.AddConnection(connID, &mockNetConn{id: connID})

				// Add message channel
				client.connMgr.CreateMessageChannel(connID, protocol.DefaultMessageChannelSize)

				// Simulate some work
				time.Sleep(time.Millisecond)

				// Remove connection
				client.cleanupConnection(connID)
			}
		}(i)
	}

	wg.Wait()

	// Verify all connections cleaned up
	if client.connMgr.GetConnectionCount() != 0 {
		t.Errorf("Expected 0 connections after concurrent cleanup, got %d", client.connMgr.GetConnectionCount())
	}

	if client.connMgr.GetMessageChannelCount() != 0 {
		t.Errorf("Expected 0 message channels after concurrent cleanup, got %d", client.connMgr.GetMessageChannelCount())
	}
}
