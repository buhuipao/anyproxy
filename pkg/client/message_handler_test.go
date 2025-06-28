package client

import (
	"errors"
	"net"
	"testing"

	"github.com/buhuipao/anyproxy/pkg/common/message"
	"github.com/buhuipao/anyproxy/pkg/common/protocol"
)

// mockConnForHandler implements a minimal connection for message handler testing
type mockConnForHandler struct {
	readData  []byte
	readErr   error
	writeData []byte
	writeErr  error
}

func (m *mockConnForHandler) ReadMessage() ([]byte, error) {
	if m.readErr != nil {
		return nil, m.readErr
	}
	return m.readData, nil
}

func (m *mockConnForHandler) WriteMessage(data []byte) error {
	if m.writeErr != nil {
		return m.writeErr
	}
	m.writeData = data
	return nil
}

func (m *mockConnForHandler) Close() error {
	return nil
}

func (m *mockConnForHandler) RemoteAddr() net.Addr {
	return mockAddr{network: "tcp", address: "mock:1234"}
}

func (m *mockConnForHandler) LocalAddr() net.Addr {
	return mockAddr{network: "tcp", address: "mock:5678"}
}

func (m *mockConnForHandler) GetClientID() string {
	return "test-client"
}

func (m *mockConnForHandler) GetGroupID() string {
	return "test-group"
}

func (m *mockConnForHandler) GetPassword() string {
	return "test-password"
}

func TestReadNextMessage(t *testing.T) {
	tests := []struct {
		name       string
		readData   []byte
		readErr    error
		expectErr  bool
		expectType string
		validate   func(t *testing.T, msg map[string]interface{})
	}{
		{
			name:       "binary data message",
			readData:   protocol.PackDataMessage("conn-1", []byte("test data")),
			expectErr:  false,
			expectType: protocol.MsgTypeData,
			validate: func(t *testing.T, msg map[string]interface{}) {
				if msg["id"] != "conn-1" {
					t.Errorf("Expected id 'conn-1', got %v", msg["id"])
				}
				if data, ok := msg["data"].([]byte); !ok || string(data) != "test data" {
					t.Errorf("Expected data 'test data', got %v", msg["data"])
				}
				if !msg["_optimized"].(bool) {
					t.Error("Expected _optimized flag to be true")
				}
			},
		},
		{
			name:       "binary connect message",
			readData:   protocol.PackConnectMessage("conn-2", "tcp", "example.com:80"),
			expectErr:  false,
			expectType: protocol.MsgTypeConnect,
			validate: func(t *testing.T, msg map[string]interface{}) {
				if msg["id"] != "conn-2" {
					t.Errorf("Expected id 'conn-2', got %v", msg["id"])
				}
				if msg["network"] != "tcp" {
					t.Errorf("Expected network 'tcp', got %v", msg["network"])
				}
				if msg["address"] != "example.com:80" {
					t.Errorf("Expected address 'example.com:80', got %v", msg["address"])
				}
			},
		},
		{
			name:       "binary close message",
			readData:   protocol.PackCloseMessage("conn-3"),
			expectErr:  false,
			expectType: protocol.MsgTypeClose,
			validate: func(t *testing.T, msg map[string]interface{}) {
				if msg["id"] != "conn-3" {
					t.Errorf("Expected id 'conn-3', got %v", msg["id"])
				}
			},
		},
		{
			name: "binary port forward response",
			readData: protocol.PackPortForwardResponseMessage(true, "", []protocol.PortForwardStatus{
				{Port: 18080, Success: true},
				{Port: 18081, Success: false},
			}),
			expectErr:  false,
			expectType: "port_forward_response",
			validate: func(t *testing.T, msg map[string]interface{}) {
				if !msg["success"].(bool) {
					t.Error("Expected success to be true")
				}
				if ports, ok := msg["ports"].(map[int]bool); ok {
					if !ports[18080] {
						t.Error("Expected port 18080 to be successful")
					}
					if ports[18081] {
						t.Error("Expected port 18081 to be unsuccessful")
					}
				} else {
					t.Error("Expected ports to be map[int]bool")
				}
			},
		},
		{
			name:      "non-binary message",
			readData:  []byte("not a binary message"),
			expectErr: true,
		},
		{
			name:      "read error",
			readErr:   errors.New("connection error"),
			expectErr: true,
		},
		{
			name:      "invalid message format",
			readData:  []byte("invalid data"),
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create client with mock connection
			mockConn := &mockConnForHandler{
				readData: tt.readData,
				readErr:  tt.readErr,
			}
			client := &Client{
				conn: mockConn,
			}
			// Initialize msgHandler
			client.msgHandler = message.NewClientExtendedMessageHandler(mockConn)

			// Read message
			msg, err := client.readNextMessage()

			// Check error
			if (err != nil) != tt.expectErr {
				t.Errorf("readNextMessage() error = %v, expectErr %v", err, tt.expectErr)
				return
			}

			if !tt.expectErr && msg != nil {
				// Check message type
				if msgType, ok := msg["type"].(string); ok {
					if msgType != tt.expectType {
						t.Errorf("Expected type %s, got %s", tt.expectType, msgType)
					}
				}

				// Run additional validation
				if tt.validate != nil {
					tt.validate(t, msg)
				}
			}
		})
	}
}

func TestWriteDataMessage(t *testing.T) {
	tests := []struct {
		name      string
		connID    string
		data      []byte
		writeErr  error
		expectErr bool
	}{
		{
			name:      "successful write",
			connID:    "conn-1",
			data:      []byte("test data"),
			expectErr: false,
		},
		{
			name:      "empty data",
			connID:    "conn-2",
			data:      []byte{},
			expectErr: false,
		},
		{
			name:      "large data",
			connID:    "conn-3",
			data:      make([]byte, 10000),
			expectErr: false,
		},
		{
			name:      "write error",
			connID:    "conn-4",
			data:      []byte("test"),
			writeErr:  errors.New("write failed"),
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock connection
			mockConn := &mockConnForHandler{
				writeErr: tt.writeErr,
			}

			// Create client
			client := &Client{
				conn: mockConn,
			}
			// Initialize msgHandler
			client.msgHandler = message.NewClientExtendedMessageHandler(mockConn)

			// Write data message
			err := client.writeDataMessage(tt.connID, tt.data)

			// Check error
			if (err != nil) != tt.expectErr {
				t.Errorf("writeDataMessage() error = %v, expectErr %v", err, tt.expectErr)
			}

			// Verify binary message was written
			if !tt.expectErr && mockConn.writeData != nil {
				// Unpack and verify
				version, msgType, payload, err := protocol.UnpackBinaryHeader(mockConn.writeData)
				if err != nil {
					t.Fatalf("Failed to unpack written message: %v", err)
				}

				if version != protocol.BinaryProtocolVersion {
					t.Errorf("Expected version %d, got %d", protocol.BinaryProtocolVersion, version)
				}

				if msgType != protocol.BinaryMsgTypeData {
					t.Errorf("Expected message type %d, got %d", protocol.BinaryMsgTypeData, msgType)
				}

				unpackedConnID, unpackedData, err := protocol.UnpackDataMessage(payload)
				if err != nil {
					t.Fatalf("Failed to unpack data message: %v", err)
				}

				if unpackedConnID != tt.connID {
					t.Errorf("Expected connID %s, got %s", tt.connID, unpackedConnID)
				}

				if string(unpackedData) != string(tt.data) {
					t.Errorf("Expected data %s, got %s", tt.data, unpackedData)
				}
			}
		})
	}
}

func TestWriteConnectResponse(t *testing.T) {
	tests := []struct {
		name      string
		connID    string
		success   bool
		errorMsg  string
		writeErr  error
		expectErr bool
	}{
		{
			name:      "successful response",
			connID:    "conn-1",
			success:   true,
			errorMsg:  "",
			expectErr: false,
		},
		{
			name:      "error response",
			connID:    "conn-2",
			success:   false,
			errorMsg:  "Connection failed",
			expectErr: false,
		},
		{
			name:      "write error",
			connID:    "conn-3",
			success:   true,
			writeErr:  errors.New("write failed"),
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock connection
			mockConn := &mockConnForHandler{
				writeErr: tt.writeErr,
			}

			// Create client
			client := &Client{
				conn: mockConn,
			}
			// Initialize msgHandler
			client.msgHandler = message.NewClientExtendedMessageHandler(mockConn)

			// Write connect response
			err := client.writeConnectResponse(tt.connID, tt.success, tt.errorMsg)

			// Check error
			if (err != nil) != tt.expectErr {
				t.Errorf("writeConnectResponse() error = %v, expectErr %v", err, tt.expectErr)
			}

			// Verify binary message was written
			if !tt.expectErr && mockConn.writeData != nil {
				// Unpack and verify
				_, msgType, payload, err := protocol.UnpackBinaryHeader(mockConn.writeData)
				if err != nil {
					t.Fatalf("Failed to unpack written message: %v", err)
				}

				if msgType != protocol.BinaryMsgTypeConnectResponse {
					t.Errorf("Expected message type %d, got %d", protocol.BinaryMsgTypeConnectResponse, msgType)
				}

				unpackedConnID, unpackedSuccess, unpackedError, err := protocol.UnpackConnectResponseMessage(payload)
				if err != nil {
					t.Fatalf("Failed to unpack connect response: %v", err)
				}

				if unpackedConnID != tt.connID {
					t.Errorf("Expected connID %s, got %s", tt.connID, unpackedConnID)
				}

				if unpackedSuccess != tt.success {
					t.Errorf("Expected success %v, got %v", tt.success, unpackedSuccess)
				}

				if unpackedError != tt.errorMsg {
					t.Errorf("Expected error message %s, got %s", tt.errorMsg, unpackedError)
				}
			}
		})
	}
}

func TestWriteCloseMessage(t *testing.T) {
	tests := []struct {
		name      string
		connID    string
		writeErr  error
		expectErr bool
	}{
		{
			name:      "successful write",
			connID:    "conn-1",
			expectErr: false,
		},
		{
			name:      "write error",
			connID:    "conn-2",
			writeErr:  errors.New("write failed"),
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock connection
			mockConn := &mockConnForHandler{
				writeErr: tt.writeErr,
			}

			// Create client
			client := &Client{
				conn: mockConn,
			}
			// Initialize msgHandler
			client.msgHandler = message.NewClientExtendedMessageHandler(mockConn)

			// Write close message
			err := client.writeCloseMessage(tt.connID)

			// Check error
			if (err != nil) != tt.expectErr {
				t.Errorf("writeCloseMessage() error = %v, expectErr %v", err, tt.expectErr)
			}

			// Verify binary message was written
			if !tt.expectErr && mockConn.writeData != nil {
				// Unpack and verify
				_, msgType, payload, err := protocol.UnpackBinaryHeader(mockConn.writeData)
				if err != nil {
					t.Fatalf("Failed to unpack written message: %v", err)
				}

				if msgType != protocol.BinaryMsgTypeClose {
					t.Errorf("Expected message type %d, got %d", protocol.BinaryMsgTypeClose, msgType)
				}

				unpackedConnID, err := protocol.UnpackCloseMessage(payload)
				if err != nil {
					t.Fatalf("Failed to unpack close message: %v", err)
				}

				if unpackedConnID != tt.connID {
					t.Errorf("Expected connID %s, got %s", tt.connID, unpackedConnID)
				}
			}
		})
	}
}
