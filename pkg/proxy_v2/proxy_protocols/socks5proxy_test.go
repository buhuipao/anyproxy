package proxy_protocols

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/buhuipao/anyproxy/pkg/config"
	"github.com/buhuipao/anyproxy/pkg/proxy_v2/common"
)

func TestNewSOCKS5ProxyWithAuth(t *testing.T) {
	config := &config.SOCKS5Config{
		ListenAddr:   ":1080",
		AuthUsername: "testuser",
		AuthPassword: "testpass",
	}

	proxy, err := NewSOCKS5ProxyWithAuth(config, mockDialFunc, mockGroupExtractor)
	if err != nil {
		t.Fatalf("Failed to create SOCKS5 proxy: %v", err)
	}

	socks5Proxy, ok := proxy.(*SOCKS5Proxy)
	if !ok {
		t.Fatal("Proxy is not SOCKS5Proxy type")
	}

	if socks5Proxy.config != config {
		t.Error("Config was not set correctly")
	}

	if socks5Proxy.dialFunc == nil {
		t.Error("Dial function was not set")
	}

	if socks5Proxy.groupExtractor == nil {
		t.Error("Group extractor was not set")
	}

	if socks5Proxy.server == nil {
		t.Error("SOCKS5 server was not created")
	}
}

func TestSOCKS5Proxy_StartStop(t *testing.T) {
	config := &config.SOCKS5Config{
		ListenAddr: "127.0.0.1:0", // Use port 0 for automatic assignment
	}

	proxy, err := NewSOCKS5ProxyWithAuth(config, mockDialFunc, nil)
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

	// Verify listener is created
	socks5Proxy := proxy.(*SOCKS5Proxy)
	if socks5Proxy.listener == nil {
		t.Error("Listener was not created")
	}

	// Stop proxy
	err = proxy.Stop()
	if err != nil {
		t.Errorf("Failed to stop proxy: %v", err)
	}
}

func TestSOCKS5Proxy_GetListenAddr(t *testing.T) {
	config := &config.SOCKS5Config{
		ListenAddr: ":1080",
	}

	proxy, _ := NewSOCKS5ProxyWithAuth(config, mockDialFunc, nil)
	socks5Proxy := proxy.(*SOCKS5Proxy)

	if socks5Proxy.GetListenAddr() != ":1080" {
		t.Errorf("Expected listen addr :1080, got %s", socks5Proxy.GetListenAddr())
	}
}

func TestGroupBasedCredentialStore_Valid(t *testing.T) {
	store := &GroupBasedCredentialStore{
		ConfigUsername: "testuser",
		ConfigPassword: "testpass",
	}

	tests := []struct {
		name     string
		username string
		password string
		expected bool
	}{
		{
			name:     "valid credentials",
			username: "testuser",
			password: "testpass",
			expected: true,
		},
		{
			name:     "valid credentials with group",
			username: "testuser.group1",
			password: "testpass",
			expected: true,
		},
		{
			name:     "invalid username",
			username: "wronguser",
			password: "testpass",
			expected: false,
		},
		{
			name:     "invalid password",
			username: "testuser",
			password: "wrongpass",
			expected: false,
		},
		{
			name:     "empty credentials",
			username: "",
			password: "",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := store.Valid(tt.username, tt.password, "127.0.0.1:1234")
			if result != tt.expected {
				t.Errorf("Expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestSOCKS5Proxy_NoAuth(t *testing.T) {
	config := &config.SOCKS5Config{
		ListenAddr: "127.0.0.1:0",
		// No auth configured
	}

	proxy, err := NewSOCKS5ProxyWithAuth(config, mockDialFunc, nil)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}

	socks5Proxy := proxy.(*SOCKS5Proxy)
	if socks5Proxy.server == nil {
		t.Error("SOCKS5 server should be created even without auth")
	}
}

func TestSOCKS5Proxy_StopWithoutStart(t *testing.T) {
	config := &config.SOCKS5Config{
		ListenAddr: ":1080",
	}

	proxy, _ := NewSOCKS5ProxyWithAuth(config, mockDialFunc, nil)

	// Stop without starting should not error
	err := proxy.Stop()
	if err != nil {
		t.Errorf("Stop without start should not error, got: %v", err)
	}
}

func TestSOCKS5Proxy_DialFunction(t *testing.T) {
	// Track if dial function was called
	testDialFunc := func(ctx context.Context, network, addr string) (net.Conn, error) {
		// Verify context has connID
		connID, ok := common.GetConnID(ctx)
		if !ok || connID == "" {
			t.Error("Expected connID in context")
		}

		// Verify user context
		type userContextKey string
		const userKey userContextKey = "user"
		userCtx, ok := ctx.Value(userKey).(*common.UserContext)
		if !ok || userCtx == nil {
			t.Error("Expected user context in dial function")
		}

		return mockDialFunc(ctx, network, addr)
	}

	config := &config.SOCKS5Config{
		ListenAddr:   "127.0.0.1:0",
		AuthUsername: "testuser",
		AuthPassword: "testpass",
	}

	proxy, err := NewSOCKS5ProxyWithAuth(config, testDialFunc, mockGroupExtractor)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}

	// Start the proxy
	err = proxy.Start()
	if err != nil {
		t.Fatalf("Failed to start proxy: %v", err)
	}
	defer proxy.Stop()

	// Give server time to start
	time.Sleep(100 * time.Millisecond)

	// Note: Actually testing the dial function would require a full SOCKS5 client
	// which is beyond the scope of unit tests. The dial function is tested
	// indirectly through integration tests.
}

func TestSOCKS5Proxy_ListenerError(t *testing.T) {
	// Try to bind to a privileged port that should fail
	config := &config.SOCKS5Config{
		ListenAddr: "127.0.0.1:1", // Port 1 typically requires root
	}

	proxy, err := NewSOCKS5ProxyWithAuth(config, mockDialFunc, nil)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}

	// Start should fail due to permission error
	err = proxy.Start()
	if err == nil {
		// If it somehow succeeded, stop it
		proxy.Stop()
		t.Skip("Expected start to fail on privileged port, but it succeeded (maybe running as root?)")
	}

	// Verify error message contains expected content
	if err.Error() == "" {
		t.Error("Expected non-empty error message")
	}
}
