package grpc

import (
	"context"
	"crypto/tls"
	"io"
	"strings"
	"sync"
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

func waitForServerReady(t *testing.T, server *grpcTransport, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		server.mu.Lock()
		listener := server.listener
		running := server.running
		server.mu.Unlock()

		if listener != nil && running {
			return listener.Addr().String()
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Server did not start in time")
	return ""
}

func TestGRPCTransport_DialWithConfig(t *testing.T) {
	// Create server
	server := NewGRPCTransport()
	clientConnected := make(chan struct{})
	serverReady := make(chan struct{})

	// Start server on localhost (use 127.0.0.1 to avoid IPv6 issues)
	go func() {
		err := server.ListenAndServe("127.0.0.1:0", func(conn transport.Connection) {
			// Signal client connected
			select {
			case <-clientConnected:
				// Already closed
			default:
				close(clientConnected)
			}

			// Keep connection alive for test
			time.Sleep(500 * time.Millisecond)
			conn.Close()
		})
		if err != nil {
			t.Errorf("Server failed: %v", err)
		}
	}()

	// Wait for server to be ready
	grpcServer := server.(*grpcTransport)
	serverAddr := waitForServerReady(t, grpcServer, 2*time.Second)
	close(serverReady)

	t.Logf("Server listening on %s", serverAddr)

	// Create client config
	clientConfig := &transport.ClientConfig{
		ClientID: "test-client",
		GroupID:  "test-group",
	}

	// Create client and connect with retry
	client := NewGRPCTransport()
	var conn transport.Connection
	var err error

	// Retry connection a few times to handle race conditions
	for i := 0; i < 3; i++ {
		conn, err = client.DialWithConfig(serverAddr, clientConfig)
		if err == nil {
			break
		}
		t.Logf("Connection attempt %d failed: %v, retrying...", i+1, err)
		time.Sleep(100 * time.Millisecond)
	}

	if err != nil {
		// Clean up server before failing
		server.Close()
		t.Fatalf("Failed to dial after retries: %v", err)
	}
	defer conn.Close()

	// Wait for connection to be established
	select {
	case <-clientConnected:
		t.Log("Client connected successfully")
	case <-time.After(5 * time.Second):
		server.Close()
		t.Fatal("Client connection timeout")
	}

	// Clean up
	err = server.Close()
	if err != nil {
		t.Errorf("Failed to close server: %v", err)
	}
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

// fakeServerStream is a BiStream server stream that accepts every Send.
type fakeServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *fakeServerStream) Context() context.Context      { return s.ctx }
func (s *fakeServerStream) Send(*StreamMessage) error     { return nil }
func (s *fakeServerStream) Recv() (*StreamMessage, error) { <-s.ctx.Done(); return nil, io.EOF }

// Close used to close writeChan while writers were still sending on it,
// which panicked with "send on closed channel".
func TestGRPCConnection_CloseWhileWriting(t *testing.T) {
	for round := 0; round < 50; round++ {
		ctx, cancel := context.WithCancel(context.Background())
		conn := newGRPCServerConnection(&fakeServerStream{ctx: ctx}, "c", "g", "")

		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for conn.WriteMessage([]byte("x")) == nil {
				}
			}()
		}
		time.Sleep(time.Millisecond)
		_ = conn.Close()
		wg.Wait()
		cancel()
	}
}

// BiStream used to report success before the server checked auth, so a
// rejected client looked connected and reconnected without backoff.
func TestGRPCTransport_AuthFailureFailsDial(t *testing.T) {
	server := NewGRPCTransportWithAuth(&transport.AuthConfig{Username: "u", Password: "right"})
	if err := server.ListenAndServe("127.0.0.1:0", func(transport.Connection) {}); err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer server.Close()
	addr := waitForServerReady(t, server.(*grpcTransport), 2*time.Second)

	_, err := NewGRPCTransport().DialWithConfig(addr, &transport.ClientConfig{
		ClientID: "c", GroupID: "g", Username: "u", Password: "wrong",
	})
	if err == nil || !strings.Contains(err.Error(), "Unauthenticated") {
		t.Fatalf("expected Unauthenticated dial error, got %v", err)
	}
}

// Closing the server side used to leave the RPC open: the client never saw
// the queued message or the end of the stream.
func TestGRPCTransport_ServerCloseEndsStream(t *testing.T) {
	server := NewGRPCTransport()
	err := server.ListenAndServe("127.0.0.1:0", func(conn transport.Connection) {
		_ = conn.WriteMessage([]byte("bye"))
		_ = conn.Close()
	})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer server.Close()
	addr := waitForServerReady(t, server.(*grpcTransport), 2*time.Second)

	conn, err := NewGRPCTransport().DialWithConfig(addr, &transport.ClientConfig{ClientID: "c", GroupID: "g"})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	type result struct {
		msg []byte
		err error
	}
	results := make(chan result, 2)
	go func() {
		for i := 0; i < 2; i++ {
			msg, err := conn.ReadMessage()
			results <- result{msg, err}
		}
	}()
	for i, want := range []string{"bye", ""} {
		select {
		case r := <-results:
			if want != "" && (r.err != nil || string(r.msg) != want) {
				t.Fatalf("read %d: got %q, %v; want %q", i, r.msg, r.err, want)
			}
			if want == "" && r.err == nil {
				t.Fatalf("read %d: expected an error after the server closed", i)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("read %d: client still sees an open stream", i)
		}
	}
}
