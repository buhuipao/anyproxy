package protocols

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/buhuipao/anyproxy/pkg/common/connection"
	commonctx "github.com/buhuipao/anyproxy/pkg/common/context"
	"github.com/buhuipao/anyproxy/pkg/config"
)

func TestNewSOCKS5ProxyWithAuth(t *testing.T) {
	config := &config.SOCKS5Config{
		ListenAddr: ":1080",
	}

	proxy, err := NewSOCKS5ProxyWithAuth(config, mockDialFunc, mockGroupValidator)
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

	if socks5Proxy.groupValidator == nil {
		t.Error("Group validator was not set")
	}
}

func TestNewSOCKS5ProxyWithAuth_WithUDPSupport(t *testing.T) {
	cfg := &config.SOCKS5Config{
		ListenAddr: "127.0.0.1:0",
	}

	proxy, err := NewSOCKS5ProxyWithAuth(cfg, mockDialFunc, mockGroupValidator)
	if err != nil {
		t.Fatalf("Failed to create SOCKS5 proxy with UDP support: %v", err)
	}

	socks5Proxy, ok := proxy.(*SOCKS5Proxy)
	if !ok {
		t.Fatal("Proxy is not SOCKS5Proxy type")
	}

	// Start proxy to verify UDP listener is created
	err = proxy.Start()
	if err != nil {
		t.Fatalf("Failed to start proxy: %v", err)
	}
	defer proxy.Stop()

	// Give server time to start
	time.Sleep(100 * time.Millisecond)

	// Verify both TCP and UDP listeners are created
	if socks5Proxy.listener == nil {
		t.Error("TCP listener was not created")
	}
	if socks5Proxy.udpListener == nil {
		t.Error("UDP listener was not created")
	}
	if socks5Proxy.udpBindAddr == nil {
		t.Error("UDP bind address was not set")
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
		GroupValidator: mockGroupValidator,
	}

	tests := []struct {
		name     string
		username string
		password string
		expected bool
	}{
		{
			name:     "valid group credentials",
			username: "testgroup",
			password: "testpass",
			expected: true,
		},
		{
			name:     "invalid group",
			username: "wronggroup",
			password: "testpass",
			expected: false,
		},
		{
			name:     "invalid password",
			username: "testgroup",
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
		// Check if connID was added to context
		connID, ok := commonctx.GetConnID(ctx)
		if !ok || connID == "" {
			t.Error("Expected connID in context")
		}

		// Check if user context was added
		userCtx, ok := commonctx.GetUserContext(ctx)
		if !ok || userCtx == nil {
			t.Error("Expected user context in dial function")
		}

		return mockDialFunc(ctx, network, addr)
	}

	config := &config.SOCKS5Config{
		ListenAddr: "127.0.0.1:0",
	}

	proxy, err := NewSOCKS5ProxyWithAuth(config, testDialFunc, mockGroupValidator)
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

func TestSOCKS5Proxy_UDPSupport(t *testing.T) {
	// Test that SOCKS5 proxy is created with UDP support on same port as TCP
	cfg := &config.SOCKS5Config{
		ListenAddr: "127.0.0.1:0",
	}

	proxy, err := NewSOCKS5ProxyWithAuth(cfg, mockDialFunc, mockGroupValidator)
	if err != nil {
		t.Fatalf("Failed to create SOCKS5 proxy with UDP support: %v", err)
	}

	socks5Proxy, ok := proxy.(*SOCKS5Proxy)
	if !ok {
		t.Fatal("Proxy is not SOCKS5Proxy type")
	}

	// Start the proxy
	err = proxy.Start()
	if err != nil {
		t.Fatalf("Failed to start proxy: %v", err)
	}
	defer proxy.Stop()

	// Give server time to start
	time.Sleep(100 * time.Millisecond)

	// Verify both TCP and UDP listeners are created
	if socks5Proxy.listener == nil {
		t.Error("TCP listener was not created")
	}
	if socks5Proxy.udpListener == nil {
		t.Error("UDP listener was not created")
	}

	// Verify UDP bind address is set correctly
	if socks5Proxy.udpBindAddr == nil {
		t.Error("UDP bind address was not set")
	}

	// Verify UDP session manager is initialized (empty at start)
	sessionCount := socks5Proxy.udpSessionMgr.Len()
	if sessionCount != 0 {
		t.Errorf("Expected 0 UDP sessions at start, got %d", sessionCount)
	}

	// Note: Full UDP ASSOCIATE testing requires a SOCKS5 client that supports UDP.
	// This test verifies that the server is created with UDP configuration.
}

func TestSOCKS5Proxy_UDPListenerSamePort(t *testing.T) {
	// Test that UDP listener uses the same port as TCP listener
	cfg := &config.SOCKS5Config{
		ListenAddr: "127.0.0.1:0", // Use port 0 for automatic assignment
	}

	proxy, err := NewSOCKS5ProxyWithAuth(cfg, mockDialFunc, nil)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}

	err = proxy.Start()
	if err != nil {
		t.Fatalf("Failed to start proxy: %v", err)
	}
	defer proxy.Stop()

	time.Sleep(100 * time.Millisecond)

	socks5Proxy := proxy.(*SOCKS5Proxy)

	// Get TCP port from listener
	tcpAddr := socks5Proxy.listener.Addr().(*net.TCPAddr)
	tcpPort := tcpAddr.Port

	// Get UDP port from UDP listener
	udpPort := socks5Proxy.udpBindAddr.Port

	// Ports should be the same
	if tcpPort != udpPort {
		t.Errorf("TCP port (%d) and UDP port (%d) should be the same", tcpPort, udpPort)
	}
}

// With port 0, the TCP port the kernel picks can already be in use for UDP.
// Start must then try a new port instead of failing.
func TestSOCKS5Proxy_RetriesWhenUDPPortInUse(t *testing.T) {
	failed := 0
	listenUDP = func(network string, laddr *net.UDPAddr) (*net.UDPConn, error) {
		if failed < 2 {
			failed++
			return nil, errors.New("address already in use")
		}
		return net.ListenUDP(network, laddr)
	}
	defer func() { listenUDP = net.ListenUDP }()

	proxy, err := NewSOCKS5ProxyWithAuth(&config.SOCKS5Config{ListenAddr: "127.0.0.1:0"}, mockDialFunc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := proxy.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer proxy.Stop()

	p := proxy.(*SOCKS5Proxy)
	if tcpPort := p.listener.Addr().(*net.TCPAddr).Port; p.udpBindAddr.Port != tcpPort {
		t.Errorf("UDP ASSOCIATE would advertise port %d, TCP port is %d", p.udpBindAddr.Port, tcpPort)
	}
}

func TestUDPSessionManager_AdoptOnlyFromControlIP(t *testing.T) {
	mgr := NewUDPSessionManager(10, time.Minute)
	defer mgr.Stop()

	session := &UDPSession{
		ConnID:     "victim",
		ClientAddr: &net.UDPAddr{IP: net.ParseIP("10.0.0.1")},
		LastActive: time.Now(),
	}
	mgr.StoreTCPRef("tcp:10.0.0.1:40000", "tcp:10.0.0.1:40000", session)

	other := &net.UDPAddr{IP: net.ParseIP("10.0.0.2"), Port: 5000}
	if _, ok := mgr.LoadOrAdoptTCPSession(other.String(), other); ok {
		t.Fatal("datagram from another IP adopted the session")
	}

	owner := &net.UDPAddr{IP: net.ParseIP("10.0.0.1"), Port: 5000}
	if got, ok := mgr.LoadOrAdoptTCPSession(owner.String(), owner); !ok || got != session {
		t.Fatal("datagram from the control connection IP did not adopt the session")
	}
}

func TestSOCKS5Proxy_ConnectPassesDomainUnresolved(t *testing.T) {
	dialed := make(chan string, 1)
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialed <- addr
		return nil, errors.New("not needed")
	}
	proxy, err := NewSOCKS5ProxyWithAuth(&config.SOCKS5Config{ListenAddr: "127.0.0.1:0"}, dial, mockGroupValidator)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}

	client, server := net.Pipe()
	defer client.Close()
	go proxy.(*SOCKS5Proxy).server.ServeConn(server)

	socks5Connect(client, "localhost", 22)

	select {
	case addr := <-dialed:
		if addr != "localhost:22" {
			t.Fatalf("dial got %q, want %q", addr, "localhost:22")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("dial function was not called")
	}
}

// socks5Connect authenticates as testgroup and sends CONNECT host:port.
func socks5Connect(c net.Conn, host string, port byte) {
	reply := make([]byte, 2)
	c.Write([]byte{5, 1, 2})
	io.ReadFull(c, reply)
	c.Write(append(append([]byte{1, 9}, "testgroup"...), append([]byte{8}, "testpass"...)...))
	io.ReadFull(c, reply)
	c.Write(append(append([]byte{5, 1, 0, 3, byte(len(host))}, host...), 0, port))
}

func TestSOCKS5Proxy_ClientCloseClosesTunnel(t *testing.T) {
	tunnel, far := net.Pipe()
	defer far.Close()
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		return connection.NewConnWrapper(tunnel, network, addr), nil
	}
	proxy, err := NewSOCKS5ProxyWithAuth(&config.SOCKS5Config{ListenAddr: "127.0.0.1:0"}, dial, mockGroupValidator)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}

	client, server := net.Pipe()
	done := make(chan struct{})
	go func() {
		proxy.(*SOCKS5Proxy).server.ServeConn(server)
		close(done)
	}()

	socks5Connect(client, "idle.example", 22)
	reply := make([]byte, 10)
	io.ReadFull(client, reply)
	if reply[1] != 0 {
		t.Fatalf("CONNECT reply code = %d, want 0", reply[1])
	}
	client.Close()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("tunnel stayed open after the SOCKS5 client closed")
	}
}

func TestSOCKS5Proxy_TargetCloseClosesUser(t *testing.T) {
	tunnel, far := net.Pipe()
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		return connection.NewConnWrapper(tunnel, network, addr), nil
	}
	proxy, err := NewSOCKS5ProxyWithAuth(&config.SOCKS5Config{ListenAddr: "127.0.0.1:0"}, dial, mockGroupValidator)
	if err != nil {
		t.Fatalf("Failed to create proxy: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to listen: %v", err)
	}
	defer ln.Close()
	done := make(chan struct{})
	go func() {
		if conn, err := (fullCloseListener{ln}).Accept(); err == nil {
			proxy.(*SOCKS5Proxy).server.ServeConn(conn)
		}
		close(done)
	}()

	user, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("Failed to dial proxy: %v", err)
	}
	defer user.Close()
	socks5Connect(user, "idle.example", 22)
	reply := make([]byte, 10)
	io.ReadFull(user, reply)
	if reply[1] != 0 {
		t.Fatalf("CONNECT reply code = %d, want 0", reply[1])
	}

	// The target sends its last data and closes; the tunnel then reads io.EOF.
	far.Write([]byte("tail-data"))
	far.Close()

	user.SetReadDeadline(time.Now().Add(2 * time.Second))
	got, err := io.ReadAll(user)
	if string(got) != "tail-data" || err != nil {
		t.Fatalf("user got %q, err %v; want %q then EOF", got, err, "tail-data")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("SOCKS5 handler still running after the target closed")
	}
}
