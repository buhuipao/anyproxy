package gateway

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/buhuipao/anyproxy/pkg/common/message"
	"github.com/buhuipao/anyproxy/pkg/common/protocol"
)

// mockNetConn implements net.Conn for testing
type mockNetConn struct {
	readData   []byte
	readErr    error
	writeErr   error
	closed     bool
	localAddr  net.Addr
	remoteAddr net.Addr
}

func (m *mockNetConn) Read(b []byte) (n int, err error) {
	if m.readErr != nil {
		return 0, m.readErr
	}
	if len(m.readData) > 0 {
		n = copy(b, m.readData)
		m.readData = m.readData[n:]
		return n, nil
	}
	return 0, nil
}

func (m *mockNetConn) Write(b []byte) (n int, err error) {
	if m.writeErr != nil {
		return 0, m.writeErr
	}
	return len(b), nil
}

func (m *mockNetConn) Close() error {
	m.closed = true
	return nil
}

func (m *mockNetConn) LocalAddr() net.Addr {
	if m.localAddr != nil {
		return m.localAddr
	}
	return mockAddr{network: "tcp", address: "127.0.0.1:8080"}
}

func (m *mockNetConn) RemoteAddr() net.Addr {
	if m.remoteAddr != nil {
		return m.remoteAddr
	}
	return mockAddr{network: "tcp", address: "127.0.0.1:12345"}
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

// Create a test ClientConn
func createTestClientConn() (*ClientConn, *mockConnectionExt) {
	mockConn := &mockConnectionExt{
		clientID: "test-client",
		groupID:  "test-group",
	}

	ctx, cancel := context.WithCancel(context.Background())
	client := &ClientConn{
		ID:             "test-client",
		GroupID:        "test-group",
		Conn:           mockConn,
		Conns:          make(map[string]*Conn),
		msgChans:       make(map[string]chan map[string]interface{}),
		ctx:            ctx,
		cancel:         cancel,
		portForwardMgr: NewPortForwardManager(),
	}
	// Initialize msgHandler
	client.msgHandler = message.NewGatewayExtendedMessageHandler(mockConn)

	return client, mockConn
}

func TestClientConn_Stop(t *testing.T) {
	client, _ := createTestClientConn()

	// Add some connections
	client.Conns["conn1"] = &Conn{
		ID:        "conn1",
		LocalConn: &mockNetConn{},
		Done:      make(chan struct{}),
	}
	client.msgChans["conn1"] = make(chan map[string]interface{}, 1)

	// Test Stop
	client.Stop()

	// Verify context is canceled
	select {
	case <-client.ctx.Done():
		// Expected
	default:
		t.Error("Context should be canceled after Stop")
	}

	// Verify connections are closed
	if len(client.Conns) != 0 {
		t.Errorf("Expected 0 connections after Stop, got %d", len(client.Conns))
	}

	// Test idempotent Stop
	client.Stop() // Should not panic
}

func TestClientConn_DialNetwork(t *testing.T) {
	client, mockConn := createTestClientConn()

	// Set up mock to return successful connect response
	mockConn.messages = []map[string]interface{}{
		{
			"type":    protocol.MsgTypeConnectResponse,
			"id":      "", // Will be set by the actual request
			"success": true,
		},
	}
	mockConn.hasMessages = true

	// Use a channel to signal when the message handler is ready
	handlerReady := make(chan struct{})

	// Start message handling in background
	go func() {
		close(handlerReady) // Signal that handler is starting
		client.handleMessage()
	}()

	// Wait for handler to be ready
	<-handlerReady

	// Create a mock dialer that tracks the connection ID
	mockConn.writeMessageFunc = func(data []byte) error {
		// Parse the binary connect message to get the connection ID
		if protocol.IsBinaryMessage(data) {
			_, msgType, payload, err := protocol.UnpackBinaryHeader(data)
			if err == nil && msgType == protocol.BinaryMsgTypeConnect {
				connID, _, _, err := protocol.UnpackConnectMessage(payload)
				if err == nil {
					// Update the response message with the correct ID
					mockConn.messages[0]["id"] = connID
				}
			}
		}
		return nil
	}

	// Test dial
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	conn, err := client.dialNetwork(ctx, "tcp", "example.com:80")
	if err != nil {
		t.Fatalf("dialNetwork failed: %v", err)
	}

	if conn == nil {
		t.Fatal("Expected non-nil connection")
	}

	// Clean up
	conn.Close()
	client.Stop()
}

func TestClientConn_HandleMessage(t *testing.T) {
	tests := []struct {
		name     string
		messages []map[string]interface{}
		setup    func(*ClientConn)
		verify   func(*ClientConn, *testing.T)
	}{
		{
			name: "handle connect response",
			messages: []map[string]interface{}{
				{
					"type":    protocol.MsgTypeConnectResponse,
					"id":      "conn1",
					"success": true,
				},
			},
			setup: func(client *ClientConn) {
				// Create message channel
				client.createMessageChannel("conn1")
			},
			verify: func(client *ClientConn, t *testing.T) {
				// Message should have been routed
				// Use a timeout instead of sleep
				ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
				defer cancel()
				<-ctx.Done()
			},
		},
		{
			name: "handle data message",
			messages: []map[string]interface{}{
				{
					"type": protocol.MsgTypeData,
					"id":   "conn1",
					"data": base64.StdEncoding.EncodeToString([]byte("test data")),
				},
			},
			setup: func(client *ClientConn) {
				// Create a connection
				client.Conns["conn1"] = &Conn{
					ID:        "conn1",
					LocalConn: &mockNetConn{},
					Done:      make(chan struct{}),
				}
				client.createMessageChannel("conn1")
			},
			verify: func(client *ClientConn, t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
				defer cancel()
				<-ctx.Done()
			},
		},
		{
			name: "handle close message",
			messages: []map[string]interface{}{
				{
					"type": protocol.MsgTypeClose,
					"id":   "conn1",
				},
			},
			setup: func(client *ClientConn) {
				// Create a connection
				client.Conns["conn1"] = &Conn{
					ID:        "conn1",
					LocalConn: &mockNetConn{},
					Done:      make(chan struct{}),
				}
				client.createMessageChannel("conn1")
			},
			verify: func(client *ClientConn, t *testing.T) {
				// Wait for the message to be processed through the async pipeline
				// The close message goes through: routeMessage -> msgChan -> processConnectionMessages -> handleCloseMessage -> closeConnection
				for i := 0; i < 10; i++ {
					time.Sleep(100 * time.Millisecond)
					client.connMu.RLock()
					_, exists := client.Conns["conn1"]
					client.connMu.RUnlock()
					if !exists {
						// Connection was successfully removed
						return
					}
				}
				t.Error("Connection should have been removed after processing close message")
			},
		},
		{
			name: "handle port forward request",
			messages: []map[string]interface{}{
				{
					"type": protocol.MsgTypePortForwardReq,
					"open_ports": []interface{}{
						map[string]interface{}{
							"remote_port": float64(18080),
							"local_port":  float64(18080),
							"local_host":  "localhost",
							"protocol":    "tcp",
						},
					},
				},
			},
			setup: func(client *ClientConn) {},
			verify: func(client *ClientConn, t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
				defer cancel()
				<-ctx.Done()
			},
		},
		{
			name: "handle unknown message type",
			messages: []map[string]interface{}{
				{
					"type": "unknown",
					"id":   "conn1",
				},
			},
			setup:  func(client *ClientConn) {},
			verify: func(client *ClientConn, t *testing.T) {},
		},
		{
			name: "handle message without type",
			messages: []map[string]interface{}{
				{
					"id": "conn1",
				},
			},
			setup:  func(client *ClientConn) {},
			verify: func(client *ClientConn, t *testing.T) {},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a fresh client and mock connection for each test
			client, mockConn := createTestClientConn()

			// Set up test messages
			mockConn.messages = tt.messages
			mockConn.hasMessages = true
			mockConn.messageIndex = 0

			// Run setup with the client
			if tt.setup != nil {
				tt.setup(client)
			}

			// Handle messages with proper synchronization
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			handleMessageDone := make(chan struct{})
			go func() {
				defer close(handleMessageDone)
				client.handleMessage()
			}()

			// Give time for processing
			processingDone := make(chan struct{})
			go func() {
				defer close(processingDone)
				time.Sleep(300 * time.Millisecond) // Still need some time for async processing
			}()

			select {
			case <-processingDone:
				// Processing time completed
			case <-ctx.Done():
				t.Error("Processing timeout")
				return
			}

			// Run verification
			if tt.verify != nil {
				tt.verify(client, t)
			}

			// Stop message handling
			mockConn.mu.Lock()
			mockConn.readErr = context.Canceled
			mockConn.mu.Unlock()

			// Wait for handleMessage to finish or timeout
			select {
			case <-handleMessageDone:
				// Good, handleMessage exited
			case <-ctx.Done():
				t.Error("handleMessage did not exit in time")
			}

			// Now stop the client
			client.Stop()
		})
	}
}

func TestClientConn_RouteMessage(t *testing.T) {
	client, _ := createTestClientConn()

	tests := []struct {
		name    string
		msg     map[string]interface{}
		setup   func()
		wantErr bool
	}{
		{
			name: "route to existing channel",
			msg: map[string]interface{}{
				"type": protocol.MsgTypeData,
				"id":   "conn1",
				"data": "test",
			},
			setup: func() {
				client.msgChans["conn1"] = make(chan map[string]interface{}, 1)
			},
			wantErr: false,
		},
		{
			name: "create channel for connect response",
			msg: map[string]interface{}{
				"type": protocol.MsgTypeConnectResponse,
				"id":   "conn2",
			},
			setup:   func() {},
			wantErr: false,
		},
		{
			name: "route to non-existent channel",
			msg: map[string]interface{}{
				"type": protocol.MsgTypeData,
				"id":   "conn3",
			},
			setup:   func() {},
			wantErr: false, // Should be ignored silently
		},
		{
			name: "message without connection ID",
			msg: map[string]interface{}{
				"type": protocol.MsgTypeData,
			},
			setup:   func() {},
			wantErr: false, // Should be ignored with error log
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset
			client.msgChans = make(map[string]chan map[string]interface{})

			tt.setup()

			// Route message
			client.routeMessage(tt.msg)

			// Use synchronous verification instead of sleep
			// Verify channel creation for connect_response
			if tt.msg["type"] == protocol.MsgTypeConnectResponse {
				if connID, ok := tt.msg["id"].(string); ok {
					if _, exists := client.msgChans[connID]; !exists {
						t.Error("Channel should have been created for connect_response")
					}
				}
			}

			// For other message types, verify the message was handled
			if tt.msg["type"] == protocol.MsgTypeData {
				if connID, ok := tt.msg["id"].(string); ok && connID != "" {
					if ch, exists := client.msgChans[connID]; exists {
						// Check if message was sent to channel (non-blocking)
						select {
						case <-ch:
							// Message was received
						default:
							// Channel is empty, which is fine for non-existent connections
						}
					}
				}
			}
		})
	}
}

func TestClientConn_CloseConnection(t *testing.T) {
	client, _ := createTestClientConn()

	// Create a connection
	mockNetConn := &mockNetConn{}
	conn := &Conn{
		ID:        "conn1",
		LocalConn: mockNetConn,
		Done:      make(chan struct{}),
	}
	client.Conns["conn1"] = conn
	client.msgChans["conn1"] = make(chan map[string]interface{}, 1)

	// Close connection
	client.closeConnection("conn1")

	// Verify connection is removed
	if _, exists := client.Conns["conn1"]; exists {
		t.Error("Connection should have been removed")
	}

	// Verify message channel is removed
	if _, exists := client.msgChans["conn1"]; exists {
		t.Error("Message channel should have been removed")
	}

	// Verify net.Conn is closed
	if !mockNetConn.closed {
		t.Error("Network connection should have been closed")
	}

	// Test closing non-existent connection
	client.closeConnection("non-existent") // Should not panic
}

func TestClientConn_HandleDataMessage(t *testing.T) {
	client, _ := createTestClientConn()

	// Create a connection
	mockNetConn := &mockNetConn{}
	client.Conns["conn1"] = &Conn{
		ID:        "conn1",
		LocalConn: mockNetConn,
		Done:      make(chan struct{}),
	}

	tests := []struct {
		name     string
		msg      map[string]interface{}
		wantErr  bool
		checkErr func() bool
	}{
		{
			name: "valid base64 data",
			msg: map[string]interface{}{
				"id":   "conn1",
				"data": base64.StdEncoding.EncodeToString([]byte("test data")),
			},
			wantErr: false,
		},
		{
			name: "valid byte data",
			msg: map[string]interface{}{
				"id":   "conn1",
				"data": []byte("test data"),
			},
			wantErr: false,
		},
		{
			name: "invalid base64 data",
			msg: map[string]interface{}{
				"id":   "conn1",
				"data": "invalid-base64!",
			},
			wantErr: true,
		},
		{
			name: "missing connection ID",
			msg: map[string]interface{}{
				"data": base64.StdEncoding.EncodeToString([]byte("test data")),
			},
			wantErr: true,
		},
		{
			name: "non-existent connection",
			msg: map[string]interface{}{
				"id":   "conn2",
				"data": base64.StdEncoding.EncodeToString([]byte("test data")),
			},
			wantErr: true,
		},
		{
			name: "write error",
			msg: map[string]interface{}{
				"id":   "conn1",
				"data": base64.StdEncoding.EncodeToString([]byte("test data")),
			},
			wantErr: true,
			checkErr: func() bool {
				mockNetConn.writeErr = errors.New("write failed")
				return true
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset
			mockNetConn.writeErr = nil

			if tt.checkErr != nil {
				tt.checkErr()
			}

			// Handle data message
			client.handleDataMessage(tt.msg)

			// Give time for processing
			time.Sleep(50 * time.Millisecond)
		})
	}
}

func TestClientConn_HandleConnection(t *testing.T) {
	tests := []struct {
		name string
		test func(t *testing.T)
	}{
		{
			name: "handle new connection",
			test: func(t *testing.T) {
				client, mockTransportConn := createTestClientConn()

				// Create a connection with mock net.Conn
				mockNetConn := &mockNetConn{
					readData: []byte("test data from local connection"),
				}

				conn := &Conn{
					ID:        "conn1",
					LocalConn: mockNetConn,
					Done:      make(chan struct{}),
				}

				// Track written messages with proper synchronization
				var writtenMessages [][]byte
				var mu sync.Mutex
				var messageWritten sync.WaitGroup
				messageWritten.Add(1)

				mockTransportConn.writeMessageFunc = func(data []byte) error {
					mu.Lock()
					writtenMessages = append(writtenMessages, data)
					mu.Unlock()
					messageWritten.Done()
					return nil
				}

				// Start handling connection
				go client.handleConnection(conn)

				// Wait for at least one message to be written
				done := make(chan struct{})
				go func() {
					messageWritten.Wait()
					close(done)
				}()

				select {
				case <-done:
					// Message was written successfully
				case <-time.After(200 * time.Millisecond):
					t.Error("Timeout waiting for data to be written to transport")
				}

				// Verify data was sent
				mu.Lock()
				messageCount := len(writtenMessages)
				mu.Unlock()

				if messageCount == 0 {
					t.Error("Expected data to be written to transport")
				}

				// Close connection
				close(conn.Done)
				time.Sleep(50 * time.Millisecond)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, tt.test)
	}
}

// Update mockConnectionExt to support test scenarios
type mockConnectionExt struct {
	clientID         string
	groupID          string
	closed           bool
	mu               sync.Mutex
	readErr          error
	writeErr         error
	readChan         chan struct{}
	messages         []map[string]interface{}
	messageIndex     int
	hasMessages      bool
	writeMessageFunc func([]byte) error
}

func (m *mockConnectionExt) ReadMessage() ([]byte, error) {
	if m.readChan != nil {
		<-m.readChan
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.readErr != nil {
		return nil, m.readErr
	}

	// Return test messages if available
	if m.hasMessages && m.messageIndex < len(m.messages) {
		msg := m.messages[m.messageIndex]
		m.messageIndex++

		// Convert message to binary format based on type
		msgType, _ := msg["type"].(string)
		switch msgType {
		case protocol.MsgTypeConnectResponse:
			connID, _ := msg["id"].(string)
			success, _ := msg["success"].(bool)
			errorMsg, _ := msg["error"].(string)
			return protocol.PackConnectResponseMessage(connID, success, errorMsg), nil
		case protocol.MsgTypeData:
			connID, _ := msg["id"].(string)
			var data []byte
			if dataStr, ok := msg["data"].(string); ok {
				data, _ = base64.StdEncoding.DecodeString(dataStr)
			} else if dataBytes, ok := msg["data"].([]byte); ok {
				data = dataBytes
			}
			return protocol.PackDataMessage(connID, data), nil
		case protocol.MsgTypeClose:
			connID, _ := msg["id"].(string)
			return protocol.PackCloseMessage(connID), nil
		case protocol.MsgTypePortForwardReq:
			clientID, _ := msg["client_id"].(string)
			openPortsInterface, _ := msg["open_ports"].([]interface{})
			var ports []protocol.PortConfig
			for _, portInterface := range openPortsInterface {
				portMap, _ := portInterface.(map[string]interface{})
				remotePort, _ := portMap["remote_port"].(float64)
				localPort, _ := portMap["local_port"].(float64)
				localHost, _ := portMap["local_host"].(string)
				proto, _ := portMap["protocol"].(string)
				ports = append(ports, protocol.PortConfig{
					RemotePort: int(remotePort),
					LocalPort:  int(localPort),
					LocalHost:  localHost,
					Protocol:   proto,
				})
			}
			return protocol.PackPortForwardMessage(clientID, ports), nil
		default:
			// Return an empty binary message for unknown types
			return []byte{0xAB, 0xCD, 0xFF, 0x00, 0x00, 0x00, 0x00}, nil
		}
	}

	// Return EOF when no more messages
	return nil, io.EOF
}

func (m *mockConnectionExt) WriteMessage(data []byte) error {
	if m.writeMessageFunc != nil {
		return m.writeMessageFunc(data)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.writeErr
}

func (m *mockConnectionExt) Read(p []byte) (n int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return 0, m.readErr
}

func (m *mockConnectionExt) Write(p []byte) (n int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(p), m.writeErr
}

func (m *mockConnectionExt) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

func (m *mockConnectionExt) LocalAddr() net.Addr {
	return mockAddr{network: "tcp", address: "127.0.0.1:8080"}
}

func (m *mockConnectionExt) RemoteAddr() net.Addr {
	return mockAddr{network: "tcp", address: "127.0.0.1:12345"}
}

func (m *mockConnectionExt) GetClientID() string {
	return m.clientID
}

func (m *mockConnectionExt) GetGroupID() string {
	return m.groupID
}

func (m *mockConnectionExt) SetDeadline(t time.Time) error {
	return nil
}

func (m *mockConnectionExt) SetReadDeadline(t time.Time) error {
	return nil
}

func (m *mockConnectionExt) SetWriteDeadline(t time.Time) error {
	return nil
}
