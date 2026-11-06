package gateway

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/buhuipao/anyproxy/pkg/common/buffer"
	"github.com/buhuipao/anyproxy/pkg/common/connection"
	"github.com/buhuipao/anyproxy/pkg/common/monitoring"
	"github.com/buhuipao/anyproxy/pkg/common/protocol"
	"github.com/buhuipao/anyproxy/pkg/transport"
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
		smartWriter:    buffer.NewSmartWriter(mockConn, "test-client"),
	}
	return client, mockConn
}

// createMessageChannel registers a message channel for connID and starts its
// processor, as dialNetwork does after a successful connect_response.
func (c *ClientConn) createMessageChannel(connID string) {
	msgChan := make(chan map[string]interface{}, protocol.DefaultMessageChannelSize)
	var done chan struct{}
	c.connMu.Lock()
	c.msgChans[connID] = msgChan
	if proxyConn, ok := c.Conns[connID]; ok {
		done = proxyConn.Done
	}
	c.connMu.Unlock()

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.processConnectionMessages(done, msgChan)
	}()
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
	monitoring.CreateConnection("conn1", client.ID, "example.com:80")

	// Test Stop
	client.Stop()

	// Verify the connection is removed from monitoring too
	if _, exists := monitoring.GetAllConnectionMetrics()["conn1"]; exists {
		t.Error("Expected connection to be removed from monitoring after Stop")
	}

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

	// ReadMessage blocks until the connect message is sent, so the response
	// carries the real connection ID
	mockConn.readChan = make(chan struct{})

	// Create a mock dialer that tracks the connection ID
	mockConn.writeMessageFunc = func(data []byte) error {
		// Parse the binary connect message to get the connection ID
		if protocol.IsBinaryMessage(data) {
			_, msgType, payload, err := protocol.UnpackBinaryHeader(data)
			if err == nil && msgType == protocol.BinaryMsgTypeConnect {
				connID, _, _, err := protocol.UnpackConnectMessage(payload)
				if err == nil {
					// Update the response message with the correct ID
					mockConn.mu.Lock()
					mockConn.messages[0]["id"] = connID
					mockConn.mu.Unlock()
					close(mockConn.readChan)
				}
			}
		}
		return nil
	}

	// Start message handling in background
	go client.handleMessage()

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

// dialNetwork must be the only reader of the connect_response: a success must
// return a connection, a failure must return an error without panicking, and
// the caller's ctx must end the wait.
func TestClientConn_DialNetworkConnectResponse(t *testing.T) {
	for _, success := range []bool{true, false} {
		client, mockConn := createTestClientConn()
		mockConn.writeMessageFunc = func(data []byte) error {
			_, msgType, payload, err := protocol.UnpackBinaryHeader(data)
			if err == nil && msgType == protocol.BinaryMsgTypeConnect {
				connID, _, _, _ := protocol.UnpackConnectMessage(payload)
				go client.routeMessage(map[string]interface{}{
					"type":    protocol.MsgTypeConnectResponse,
					"id":      connID,
					"success": success,
					"error":   "connection refused",
				})
			}
			return nil
		}

		for i := 0; i < 100; i++ {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			conn, err := client.dialNetwork(ctx, "tcp", "example.com:80")
			cancel()
			if success != (err == nil) {
				t.Fatalf("success=%v dial %d: unexpected result: %v", success, i, err)
			}
			if conn != nil {
				conn.Close()
			}
		}
		client.Stop()
	}

	// No response at all: the caller's ctx ends the dial
	client, _ := createTestClientConn()
	defer client.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := client.dialNetwork(ctx, "tcp", "example.com:80"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context deadline error, got %v", err)
	}
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
			name: "ignore connect response for unknown connection",
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
			// A late connect_response (the dial was already given up) must
			// not create a channel nobody will clean up
			if tt.msg["type"] == protocol.MsgTypeConnectResponse {
				if connID, ok := tt.msg["id"].(string); ok {
					if _, exists := client.msgChans[connID]; exists {
						t.Error("Channel should not be created for connect_response of unknown connection")
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

// Connections whose users stopped reading must not block the client's shared
// read loop: each is closed shortly after its message channel is full and the
// client is told, while other connections keep receiving data. A full UDP
// channel only drops the packet.
func TestClientConn_RouteMessageStalledConnection(t *testing.T) {
	client, mockConn := createTestClientConn()
	defer client.Stop()

	closeSent := make(chan string, 10)
	mockConn.writeMessageFunc = func(data []byte) error {
		_, msgType, payload, err := protocol.UnpackBinaryHeader(data)
		if err == nil && msgType == protocol.BinaryMsgTypeClose {
			connID, _ := protocol.UnpackCloseMessage(payload)
			closeSent <- connID
		}
		return nil
	}

	addConn := func(connID string) net.Conn {
		userSide, gatewaySide := connection.NewBufferedConnPair(connection.TCPBufferSize, false)
		client.connMu.Lock()
		client.Conns[connID] = &Conn{ID: connID, LocalConn: gatewaySide, Done: make(chan struct{})}
		client.connMu.Unlock()
		client.createMessageChannel(connID)
		return userSide
	}
	addConn("slow1") // users never read
	addConn("slow2")
	fast := addConn("fast")

	// Act as the shared read loop: overfill both slow connections, then route
	// one message to the fast connection.
	routed := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		for i := 0; i < connection.TCPBufferSize+protocol.DefaultMessageChannelSize+10; i++ {
			client.routeMessage(map[string]interface{}{"type": protocol.MsgTypeData, "id": "slow1", "data": []byte("x")})
			client.routeMessage(map[string]interface{}{"type": protocol.MsgTypeData, "id": "slow2", "data": []byte("x")})
		}
		client.routeMessage(map[string]interface{}{"type": protocol.MsgTypeData, "id": "fast", "data": []byte("hello")})
		routed <- time.Since(start)
	}()

	select {
	case d := <-routed:
		if d > 2*time.Second {
			t.Errorf("shared read loop was blocked for %v by the stalled connections", d)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("shared read loop is still blocked by the stalled connections")
	}

	closed := map[string]bool{}
	for len(closeSent) > 0 {
		closed[<-closeSent] = true
	}
	if !closed["slow1"] || !closed["slow2"] || len(closed) != 2 {
		t.Errorf("close sent for %v, want slow1 and slow2", closed)
	}

	client.connMu.RLock()
	_, slow1Exists := client.Conns["slow1"]
	_, slow2Exists := client.Conns["slow2"]
	client.connMu.RUnlock()
	if slow1Exists || slow2Exists {
		t.Error("stalled connections should have been closed")
	}

	got := make(chan string, 1)
	go func() {
		buf := make([]byte, 16)
		n, _ := fast.Read(buf)
		got <- string(buf[:n])
	}()
	select {
	case data := <-got:
		if data != "hello" {
			t.Errorf("fast connection got %q, want \"hello\"", data)
		}
	case <-time.After(2 * time.Second):
		t.Error("fast connection did not receive its data")
	}

	// UDP: a full channel drops the packet and keeps the connection
	client.connMu.Lock()
	client.Conns["udp"] = &Conn{ID: "udp", LocalConn: &mockNetConn{}, Done: make(chan struct{}), IsUDP: true}
	client.msgChans["udp"] = make(chan map[string]interface{}, 1)
	client.connMu.Unlock()
	for i := 0; i < 2; i++ {
		client.routeMessage(map[string]interface{}{"type": protocol.MsgTypeData, "id": "udp", "data": []byte("x")})
	}
	client.connMu.RLock()
	_, udpExists := client.Conns["udp"]
	client.connMu.RUnlock()
	if !udpExists || len(closeSent) > 0 {
		t.Error("UDP connection should stay open when its channel is full")
	}
}

// A channel can also fill when its processor was not scheduled yet while the
// read loop drained a backlog. A processor that is only late must not get its
// connection closed.
func TestClientConn_RouteMessageProcessorLate(t *testing.T) {
	client, mockConn := createTestClientConn()
	defer client.Stop()

	closeSent := false
	mockConn.writeMessageFunc = func(data []byte) error {
		_, msgType, _, err := protocol.UnpackBinaryHeader(data)
		if err == nil && msgType == protocol.BinaryMsgTypeClose {
			closeSent = true
		}
		return nil
	}

	_, gatewaySide := connection.NewBufferedConnPair(connection.TCPBufferSize, false)
	proxyConn := &Conn{ID: "conn1", LocalConn: gatewaySide, Done: make(chan struct{})}
	msgChan := make(chan map[string]interface{}, protocol.DefaultMessageChannelSize)
	client.connMu.Lock()
	client.Conns["conn1"] = proxyConn
	client.msgChans["conn1"] = msgChan
	client.connMu.Unlock()

	// The read loop fills the channel before the processor runs
	for i := 0; i < protocol.DefaultMessageChannelSize; i++ {
		client.routeMessage(map[string]interface{}{"type": protocol.MsgTypeData, "id": "conn1", "data": []byte("x")})
	}
	time.AfterFunc(10*time.Millisecond, func() {
		client.processConnectionMessages(proxyConn.Done, msgChan)
	})
	client.routeMessage(map[string]interface{}{"type": protocol.MsgTypeData, "id": "conn1", "data": []byte("x")})

	client.connMu.RLock()
	_, exists := client.Conns["conn1"]
	client.connMu.RUnlock()
	if !exists || closeSent {
		t.Error("connection with a late processor should not be closed")
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

// The user writes its last bytes and closes while the client is still
// sending data. The failed local write must not stop handleConnection
// from forwarding those bytes and telling the client to close.
func TestClientConn_HandleDataMessageAfterUserClose(t *testing.T) {
	client, mockConn := createTestClientConn()
	defer client.Stop()

	var forwarded []byte
	closeSent := false
	mockConn.writeMessageFunc = func(data []byte) error {
		_, msgType, payload, err := protocol.UnpackBinaryHeader(data)
		if err != nil {
			return nil
		}
		switch msgType {
		case protocol.BinaryMsgTypeData:
			_, d, _ := protocol.UnpackDataMessage(payload)
			forwarded = append(forwarded, d...)
		case protocol.BinaryMsgTypeClose:
			closeSent = true
		}
		return nil
	}

	userSide, gatewaySide := connection.NewBufferedConnPair(connection.TCPBufferSize, false)
	proxyConn := &Conn{ID: "conn1", LocalConn: gatewaySide, Done: make(chan struct{})}
	client.Conns["conn1"] = proxyConn

	if _, err := userSide.Write([]byte("last bytes")); err != nil {
		t.Fatal(err)
	}
	userSide.Close()

	// Data from the target arrives after the user closed
	client.handleDataMessage(map[string]interface{}{"id": "conn1", "data": []byte("server data")})

	client.handleConnection(proxyConn)

	if string(forwarded) != "last bytes" {
		t.Errorf("forwarded %q, want \"last bytes\"", forwarded)
	}
	if !closeSent {
		t.Error("client was not told to close the connection")
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

// WriteStreamBuffer implements transport.StreamBufferWriter for the test mock.
// Delegates to WriteMessage after extracting the full buffer contents.
func (m *mockConnectionExt) WriteStreamBuffer(sb transport.StreamBuffer) error {
	data := make([]byte, sb.TotalLen())
	copy(data, sb.Full())
	sb.Release()
	return m.WriteMessage(data)
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

func (m *mockConnectionExt) GetPassword() string {
	return "test-password" // Default test password
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

// ==================== Heartbeat Tests ====================

func TestClientConn_HandleHeartbeat(t *testing.T) {
	// Initialize port forward manager
	portForwardMgr := NewPortForwardManager()

	t.Run("handle heartbeat message", func(t *testing.T) {
		clientID := "test-client-heartbeat"
		groupID := "test-group"
		timestamp := time.Now().UnixNano()

		// Create mock connection that will capture written messages
		var writtenData []byte
		mockConn := &mockConnectionExt{
			clientID: clientID,
			groupID:  groupID,
			writeMessageFunc: func(data []byte) error {
				writtenData = data
				return nil
			},
		}

		// Create client connection
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		client := &ClientConn{
			ID:             clientID,
			GroupID:        groupID,
			Conn:           mockConn,
			Conns:          make(map[string]*Conn),
			msgChans:       make(map[string]chan map[string]interface{}),
			ctx:            ctx,
			cancel:         cancel,
			portForwardMgr: portForwardMgr,
		}

		// Create heartbeat message
		heartbeatMsg := map[string]interface{}{
			"type":      protocol.MsgTypeHeartbeat,
			"client_id": clientID,
			"timestamp": timestamp,
		}

		// Handle heartbeat
		client.handleHeartbeat(heartbeatMsg)

		// Verify heartbeat ack was sent
		if len(writtenData) == 0 {
			t.Error("Expected heartbeat ack to be written")
		}

		// Verify the written data is a heartbeat ack
		if protocol.IsBinaryMessage(writtenData) {
			_, msgType, payload, err := protocol.UnpackBinaryHeader(writtenData)
			if err != nil {
				t.Fatalf("Failed to unpack written data: %v", err)
			}

			if msgType != protocol.BinaryMsgTypeHeartbeatAck {
				t.Errorf("Expected heartbeat ack message type %d, got %d", protocol.BinaryMsgTypeHeartbeatAck, msgType)
			}

			// Verify timestamp in ack
			ackTimestamp, err := protocol.UnpackHeartbeatAckMessage(payload)
			if err != nil {
				t.Fatalf("Failed to unpack heartbeat ack: %v", err)
			}

			if ackTimestamp != timestamp {
				t.Errorf("Expected timestamp %d, got %d", timestamp, ackTimestamp)
			}
		}
	})

	t.Run("handle heartbeat with mismatched client ID", func(t *testing.T) {
		clientID := "test-client"
		groupID := "test-group"
		differentClientID := "different-client"
		timestamp := time.Now().UnixNano()

		var writtenData []byte
		mockConn := &mockConnectionExt{
			clientID: clientID,
			groupID:  groupID,
			writeMessageFunc: func(data []byte) error {
				writtenData = data
				return nil
			},
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		client := &ClientConn{
			ID:             clientID,
			GroupID:        groupID,
			Conn:           mockConn,
			Conns:          make(map[string]*Conn),
			msgChans:       make(map[string]chan map[string]interface{}),
			ctx:            ctx,
			cancel:         cancel,
			portForwardMgr: portForwardMgr,
		}

		// Create heartbeat message with different client ID
		heartbeatMsg := map[string]interface{}{
			"type":      protocol.MsgTypeHeartbeat,
			"client_id": differentClientID, // Different from connection's client ID
			"timestamp": timestamp,
		}

		// Handle heartbeat - should still respond
		client.handleHeartbeat(heartbeatMsg)

		// Verify heartbeat ack was still sent
		if len(writtenData) == 0 {
			t.Error("Expected heartbeat ack to be written even with mismatched client ID")
		}
	})

	t.Run("handle heartbeat with write error", func(t *testing.T) {
		clientID := "test-client"
		groupID := "test-group"
		timestamp := time.Now().UnixNano()

		mockConn := &mockConnectionExt{
			clientID: clientID,
			groupID:  groupID,
			writeMessageFunc: func(data []byte) error {
				return fmt.Errorf("write error")
			},
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		client := &ClientConn{
			ID:             clientID,
			GroupID:        groupID,
			Conn:           mockConn,
			Conns:          make(map[string]*Conn),
			msgChans:       make(map[string]chan map[string]interface{}),
			ctx:            ctx,
			cancel:         cancel,
			portForwardMgr: portForwardMgr,
		}

		// Create heartbeat message
		heartbeatMsg := map[string]interface{}{
			"type":      protocol.MsgTypeHeartbeat,
			"client_id": clientID,
			"timestamp": timestamp,
		}

		// Handle heartbeat - should not panic even if write fails
		client.handleHeartbeat(heartbeatMsg)
		// Test passes if no panic
	})
}
