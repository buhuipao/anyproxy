package client

import (
	"errors"
	"net"
	"testing"

	"github.com/buhuipao/anyproxy/pkg/config"
)

// mockConnForPortForward implements a minimal connection for port forward testing
type mockConnForPortForward struct {
	writeMessage []byte
	writeErr     error
	writeCalls   int
}

func TestSendPortForwardingRequest(t *testing.T) {
	tests := []struct {
		name       string
		openPorts  []config.OpenPort
		writeErr   error
		expectErr  bool
		expectCall bool
	}{
		{
			name:       "no open ports",
			openPorts:  []config.OpenPort{},
			expectErr:  false,
			expectCall: false,
		},
		{
			name: "single port",
			openPorts: []config.OpenPort{
				{
					RemotePort: 18080,
					LocalHost:  "localhost",
					LocalPort:  18080,
					Protocol:   "tcp",
				},
			},
			expectErr:  false,
			expectCall: true,
		},
		{
			name: "multiple ports",
			openPorts: []config.OpenPort{
				{RemotePort: 18080, LocalHost: "localhost", LocalPort: 18080, Protocol: "tcp"},
				{RemotePort: 18081, LocalHost: "127.0.0.1", LocalPort: 9090, Protocol: "tcp"},
				{RemotePort: 18082, LocalHost: "localhost", LocalPort: 18082, Protocol: "udp"},
			},
			expectErr:  false,
			expectCall: true,
		},
		{
			name: "write error",
			openPorts: []config.OpenPort{
				{RemotePort: 18080, LocalHost: "localhost", LocalPort: 18080, Protocol: "tcp"},
			},
			writeErr:   errors.New("write failed"),
			expectErr:  true,
			expectCall: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock connection
			mockConn := &mockConnForPortForward{
				writeErr: tt.writeErr,
			}

			// Create client
			client := &Client{
				config: &config.ClientConfig{
					ClientID:  "test-client",
					OpenPorts: tt.openPorts,
				},
				conn: mockConn,
			}

			// Send port forwarding request
			err := client.sendPortForwardingRequest()

			// Check error
			if (err != nil) != tt.expectErr {
				t.Errorf("sendPortForwardingRequest() error = %v, expectErr %v", err, tt.expectErr)
			}

			// Check if write was called
			if (mockConn.writeCalls > 0) != tt.expectCall {
				t.Errorf("write called = %v, expectCall %v", mockConn.writeCalls > 0, tt.expectCall)
			}

			// Note: We can't validate the binary message content directly in these tests
			// as the mock no longer stores the structured message
		})
	}
}

func TestHandlePortForwardResponse(t *testing.T) {
	tests := []struct {
		name      string
		msg       map[string]interface{}
		expectLog bool
	}{
		{
			name: "successful response",
			msg: map[string]interface{}{
				"success": true,
			},
			expectLog: true,
		},
		{
			name: "failed response with error",
			msg: map[string]interface{}{
				"success": false,
				"error":   "Port already in use",
			},
			expectLog: true,
		},
		{
			name: "response with port statuses",
			msg: map[string]interface{}{
				"success": true,
				"port_statuses": []interface{}{
					map[string]interface{}{
						"port":    float64(18080),
						"success": true,
					},
					map[string]interface{}{
						"port":    float64(18081),
						"success": false,
					},
				},
			},
			expectLog: true,
		},
		{
			name: "invalid response - missing success field",
			msg: map[string]interface{}{
				"error": "Something went wrong",
			},
			expectLog: true,
		},
		{
			name: "response with invalid port status format",
			msg: map[string]interface{}{
				"success": true,
				"port_statuses": []interface{}{
					"invalid status", // Invalid format
					map[string]interface{}{
						"port":    float64(18080),
						"success": true,
					},
				},
			},
			expectLog: true,
		},
		{
			name:      "empty response",
			msg:       map[string]interface{}{},
			expectLog: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create client
			client := &Client{
				config: &config.ClientConfig{
					ClientID: "test-client",
				},
			}

			// Handle port forward response
			// Note: This function only logs, so we can't test much without mocking the logger
			client.handlePortForwardResponse(tt.msg)

			// The test passes if no panic occurs
			// In a real scenario, we might want to mock the logger to verify log messages
		})
	}
}

// mockConn adds the missing methods to satisfy the transport.Connection interface
func (m *mockConnForPortForward) ReadMessage() ([]byte, error) {
	return nil, errors.New("not implemented")
}

func (m *mockConnForPortForward) WriteMessage(data []byte) error {
	m.writeCalls++
	if m.writeErr != nil {
		return m.writeErr
	}
	m.writeMessage = data
	return nil
}

func (m *mockConnForPortForward) Close() error {
	return nil
}

func (m *mockConnForPortForward) RemoteAddr() net.Addr {
	return mockAddr{network: "tcp", address: "mock:1234"}
}

func (m *mockConnForPortForward) LocalAddr() net.Addr {
	return mockAddr{network: "tcp", address: "mock:5678"}
}

func (m *mockConnForPortForward) GetClientID() string {
	return "test-client"
}

func (m *mockConnForPortForward) GetGroupID() string {
	return "test-group"
}
