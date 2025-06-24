package grpc

import (
	"crypto/tls"
	"testing"
	"time"

	"github.com/buhuipao/anyproxy/pkg/transport"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestNewGRPCTransport(t *testing.T) {
	trans := NewGRPCTransport()

	if trans == nil {
		t.Fatal("Expected non-nil transport")
	}

	grpcTrans, ok := trans.(*grpcTransport)
	if !ok {
		t.Fatal("Transport is not grpcTransport type")
	}

	if grpcTrans.authConfig != nil {
		t.Error("Auth config should be nil for default transport")
	}
}

func TestNewGRPCTransportWithAuth(t *testing.T) {
	authConfig := &transport.AuthConfig{
		Username: "testuser",
		Password: "testpass",
	}

	trans := NewGRPCTransportWithAuth(authConfig)

	if trans == nil {
		t.Fatal("Expected non-nil transport")
	}

	grpcTrans, ok := trans.(*grpcTransport)
	if !ok {
		t.Fatal("Transport is not grpcTransport type")
	}

	if grpcTrans.authConfig == nil {
		t.Fatal("Auth config should not be nil")
	}

	if grpcTrans.authConfig.Username != "testuser" {
		t.Errorf("Expected username 'testuser', got '%s'", grpcTrans.authConfig.Username)
	}
}

func TestGRPCTransport_ListenAndServe(t *testing.T) {
	trans := NewGRPCTransport()

	// Start server in goroutine
	go func() {
		err := trans.ListenAndServe(":0", func(conn transport.Connection) {
			// Just close the connection
			conn.Close()
		})
		if err != nil {
			t.Errorf("ListenAndServe failed: %v", err)
		}
	}()

	// Wait a bit for server to start
	time.Sleep(100 * time.Millisecond)

	// Get the actual server address using the transport's internal state
	grpcTrans := trans.(*grpcTransport)
	grpcTrans.mu.Lock()
	listener := grpcTrans.listener
	grpcTrans.mu.Unlock()

	if listener == nil {
		t.Fatal("Listener not started")
	}

	// Test that we can connect
	conn, err := grpc.Dial(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("Failed to dial: %v", err)
	}
	conn.Close()

	// Stop server
	err = trans.Close()
	if err != nil {
		t.Errorf("Failed to close transport: %v", err)
	}
}

func TestGRPCTransport_ListenAndServeWithTLS(t *testing.T) {
	// Create test TLS config
	tlsConfig := &tls.Config{
		InsecureSkipVerify: true,
	}

	trans := NewGRPCTransport()

	// Start server in goroutine
	go func() {
		err := trans.ListenAndServeWithTLS(":0", func(conn transport.Connection) {
			// Just close the connection
			conn.Close()
		}, tlsConfig)
		if err != nil {
			t.Errorf("ListenAndServeWithTLS failed: %v", err)
		}
	}()

	// Wait a bit for server to start
	time.Sleep(100 * time.Millisecond)

	// Get the actual server address
	grpcTrans := trans.(*grpcTransport)
	grpcTrans.mu.Lock()
	listener := grpcTrans.listener
	grpcTrans.mu.Unlock()

	if listener == nil {
		t.Fatal("Listener not started")
	}

	// Stop server
	err := trans.Close()
	if err != nil {
		t.Errorf("Failed to close transport: %v", err)
	}
}

func TestGRPCTransport_DialWithConfig(t *testing.T) {
	// Create server
	server := NewGRPCTransport()
	clientConnected := make(chan struct{})

	// Start server
	go func() {
		err := server.ListenAndServe(":0", func(conn transport.Connection) {
			// Signal client connected
			close(clientConnected)

			// Keep connection alive for test
			time.Sleep(100 * time.Millisecond)
			conn.Close()
		})
		if err != nil {
			t.Errorf("Server failed: %v", err)
		}
	}()

	// Wait for server to be ready and get address
	time.Sleep(100 * time.Millisecond)
	grpcServer := server.(*grpcTransport)
	grpcServer.mu.Lock()
	listener := grpcServer.listener
	grpcServer.mu.Unlock()

	if listener == nil {
		t.Fatal("Server listener not started")
	}

	serverAddr := listener.Addr().String()

	// Create client
	clientConfig := &transport.ClientConfig{
		ClientID: "test-client",
		GroupID:  "test-group",
	}

	client := NewGRPCTransport()
	conn, err := client.DialWithConfig(serverAddr, clientConfig)
	if err != nil {
		t.Fatalf("Failed to dial: %v", err)
	}
	defer conn.Close()

	// Wait for connection to be established
	select {
	case <-clientConnected:
		// Success
	case <-time.After(2 * time.Second):
		t.Fatal("Client connection timeout")
	}

	// Clean up
	server.Close()
}

func TestGRPCTransport_Close(t *testing.T) {
	trans := NewGRPCTransport()

	// Start server
	err := trans.ListenAndServe(":0", func(conn transport.Connection) {
		conn.Close()
	})
	if err != nil {
		t.Fatalf("Failed to start server: %v", err)
	}

	// Wait for server to start
	time.Sleep(100 * time.Millisecond)

	// Close should work without error
	err = trans.Close()
	if err != nil {
		t.Errorf("Close failed: %v", err)
	}

	// Double close should be safe
	err = trans.Close()
	if err != nil {
		t.Errorf("Double close failed: %v", err)
	}
}

func TestGRPCTransport_Authentication(t *testing.T) {
	// Test authentication configuration is properly stored and used
	authConfig := &transport.AuthConfig{
		Username: "testuser",
		Password: "testpass",
	}

	trans := NewGRPCTransportWithAuth(authConfig)
	grpcTrans := trans.(*grpcTransport)

	if grpcTrans.authConfig == nil {
		t.Fatal("Auth config should not be nil")
	}

	if grpcTrans.authConfig.Username != authConfig.Username {
		t.Errorf("Expected username %s, got %s", authConfig.Username, grpcTrans.authConfig.Username)
	}

	if grpcTrans.authConfig.Password != authConfig.Password {
		t.Errorf("Expected password %s, got %s", authConfig.Password, grpcTrans.authConfig.Password)
	}

	// Test that the transport can handle authentication in config
	config := &transport.ClientConfig{
		ClientID:   "test-client",
		GroupID:    "test-group",
		Username:   "override-user", // This should be used over authConfig
		Password:   "override-pass", // This should be used over authConfig
		SkipVerify: true,
	}

	// Test dial with invalid address to ensure auth config is processed
	_, err := trans.DialWithConfig("invalid-address:99999", config)
	if err == nil {
		t.Error("Expected error when dialing invalid address")
	}

	// The important verification is that auth config is stored and would be used
	// in actual connections. Since we can't easily test gRPC auth without a full
	// server setup, we verify the configuration is properly managed.

	// Test creating transport without auth
	transNoAuth := NewGRPCTransport()
	grpcTransNoAuth := transNoAuth.(*grpcTransport)

	if grpcTransNoAuth.authConfig != nil {
		t.Error("Auth config should be nil for transport without auth")
	}
}
