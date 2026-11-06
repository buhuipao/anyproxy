package protocols

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/buhuipao/anyproxy/pkg/common/monitoring"
	"github.com/buhuipao/anyproxy/pkg/config"
	"github.com/sagernet/sing/common/auth"
	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

func tuicMockDialFunc(ctx context.Context, network, addr string) (net.Conn, error) {
	return nil, nil
}

func tuicMockGroupValidator(groupID, password string) bool {
	return groupID == "testgroup" && password == "testpass"
}

func TestNewTUICv5ProxyWithAuth(t *testing.T) {
	cfg := &config.TUICConfig{
		ListenAddr:        ":8443",
		CongestionControl: "bbr",
		UDPRelayMode:      "native",
		ZeroRTT:           true,
	}

	proxy, err := NewTUICv5ProxyWithAuth(cfg, tuicMockDialFunc, tuicMockGroupValidator, "", "")
	if err != nil {
		t.Fatalf("Failed to create TUIC v5 proxy: %v", err)
	}

	tuicProxy, ok := proxy.(*TUICv5Proxy)
	if !ok {
		t.Fatal("Proxy is not TUICv5Proxy type")
	}

	if tuicProxy.config != cfg {
		t.Error("Config was not set correctly")
	}
}

func TestNewTUICv5ProxyWithAuth_EmptyListenAddr(t *testing.T) {
	cfg := &config.TUICConfig{ListenAddr: ""}
	_, err := NewTUICv5ProxyWithAuth(cfg, tuicMockDialFunc, tuicMockGroupValidator, "", "")
	if err == nil {
		t.Error("Expected error for empty listen address")
	}
}

func TestTUICv5Proxy_IsRunning(t *testing.T) {
	cfg := &config.TUICConfig{ListenAddr: ":8443"}
	proxy, _ := NewTUICv5ProxyWithAuth(cfg, tuicMockDialFunc, nil, "", "")
	tuicProxy := proxy.(*TUICv5Proxy)
	if tuicProxy.IsRunning() {
		t.Error("Proxy should not be running initially")
	}
}

func TestTUICv5Proxy_StopWithoutStart(t *testing.T) {
	cfg := &config.TUICConfig{ListenAddr: ":8443"}
	proxy, _ := NewTUICv5ProxyWithAuth(cfg, tuicMockDialFunc, nil, "", "")
	if err := proxy.Stop(); err != nil {
		t.Errorf("Stop without start should not error: %v", err)
	}
}

func TestTUICv5Proxy_StartWithoutTLS(t *testing.T) {
	cfg := &config.TUICConfig{ListenAddr: "127.0.0.1:0"}
	proxy, _ := NewTUICv5ProxyWithAuth(cfg, tuicMockDialFunc, nil, "", "")
	err := proxy.Start()
	if err == nil {
		proxy.Stop()
		t.Error("Expected error when starting without TLS certificate")
	}
}

func TestTUICv5Proxy_DoubleStart(t *testing.T) {
	cfg := &config.TUICConfig{ListenAddr: ":8443"}
	proxy, _ := NewTUICv5ProxyWithAuth(cfg, tuicMockDialFunc, nil, "", "")
	tuicProxy := proxy.(*TUICv5Proxy)
	tuicProxy.mu.Lock()
	tuicProxy.running = true
	tuicProxy.mu.Unlock()
	if err := proxy.Start(); err == nil {
		t.Error("Expected error for double start")
	}
}

func TestStdServerConfig(t *testing.T) {
	tlsCfg := &tls.Config{ServerName: "test.example.com", NextProtos: []string{"h3"}}
	wrapper := &stdServerConfig{config: tlsCfg}

	if wrapper.ServerName() != "test.example.com" {
		t.Errorf("Expected 'test.example.com', got '%s'", wrapper.ServerName())
	}
	wrapper.SetServerName("new.example.com")
	if wrapper.ServerName() != "new.example.com" {
		t.Error("SetServerName failed")
	}
	if len(wrapper.NextProtos()) != 1 {
		t.Error("NextProtos failed")
	}
	wrapper.SetNextProtos([]string{"h3", "tuic"})
	if len(wrapper.NextProtos()) != 2 {
		t.Error("SetNextProtos failed")
	}
	if err := wrapper.Start(); err != nil {
		t.Error("Start should not error")
	}
	if err := wrapper.Close(); err != nil {
		t.Error("Close should not error")
	}
	if wrapper.Clone() == nil {
		t.Error("Clone should not return nil")
	}
	if cfg, _ := wrapper.Config(); cfg == nil {
		t.Error("Config should return non-nil")
	}
}

func TestTUICv5Handler_Creation(t *testing.T) {
	cfg := &config.TUICConfig{ListenAddr: ":8443"}
	proxy, _ := NewTUICv5ProxyWithAuth(cfg, tuicMockDialFunc, nil, "", "")
	tuicProxy := proxy.(*TUICv5Proxy)
	handler := &TUICv5Handler{proxy: tuicProxy}
	if handler.proxy != tuicProxy {
		t.Error("Handler proxy reference is incorrect")
	}
}

func TestTUICConfig_Defaults(t *testing.T) {
	cfg := &config.TUICConfig{ListenAddr: ":8443"}
	if cfg.GetCongestionControl() != "bbr" {
		t.Error("Default congestion control should be bbr")
	}
	if cfg.GetUDPRelayMode() != "native" {
		t.Error("Default UDP relay mode should be native")
	}
	if len(cfg.GetALPN()) == 0 {
		t.Error("Default ALPN should not be empty")
	}
	if cfg.GetAuthTimeout() <= 0 {
		t.Error("Default auth timeout should be positive")
	}
	if cfg.GetHeartbeat() <= 0 {
		t.Error("Default heartbeat should be positive")
	}
}

func TestGroupIDToUUID_ValidUUID(t *testing.T) {
	// A valid UUID string should be parsed directly
	input := "901c0948-6d6a-4717-a9b3-a45d44568824"
	result, formatted := groupIDToUUID(input)

	if formatted != input {
		t.Errorf("Expected formatted=%q, got %q", input, formatted)
	}

	// Verify the binary matches the parsed UUID
	expected := [16]byte{
		0x90, 0x1c, 0x09, 0x48,
		0x6d, 0x6a, 0x47, 0x17,
		0xa9, 0xb3, 0xa4, 0x5d,
		0x44, 0x56, 0x88, 0x24,
	}
	if result != expected {
		t.Errorf("Binary UUID mismatch: got %x, want %x", result, expected)
	}
}

func TestGroupIDToUUID_NonUUID(t *testing.T) {
	// A non-UUID groupID should produce a deterministic UUID v5
	result1, formatted1 := groupIDToUUID("example-group")
	result2, formatted2 := groupIDToUUID("example-group")

	// Same input must produce same output (deterministic)
	if result1 != result2 || formatted1 != formatted2 {
		t.Error("groupIDToUUID is not deterministic for non-UUID input")
	}

	// Must be a valid UUID format (8-4-4-4-12 hex with dashes)
	if len(formatted1) != 36 {
		t.Errorf("Expected 36-char UUID string, got %d chars: %q", len(formatted1), formatted1)
	}

	// Different inputs must produce different UUIDs
	result3, _ := groupIDToUUID("other-group")
	if result1 == result3 {
		t.Error("Different groupIDs should produce different UUIDs")
	}
}

func TestGroupIDToUUID_UppercaseUUID(t *testing.T) {
	// Uppercase UUID should also be parsed correctly
	input := "901C0948-6D6A-4717-A9B3-A45D44568824"
	_, formatted := groupIDToUUID(input)

	// google/uuid normalizes to lowercase
	expected := "901c0948-6d6a-4717-a9b3-a45d44568824"
	if formatted != expected {
		t.Errorf("Expected formatted=%q, got %q", expected, formatted)
	}
}

func TestTUICv5Proxy_Initialization(t *testing.T) {
	cfg := &config.TUICConfig{ListenAddr: ":8443"}
	proxy, _ := NewTUICv5ProxyWithAuth(cfg, tuicMockDialFunc, nil, "", "")
	tuicProxy := proxy.(*TUICv5Proxy)
	if tuicProxy.stopCh == nil {
		t.Error("stopCh should be initialized")
	}
	if tuicProxy.users == nil {
		t.Error("users map should be initialized")
	}
}

func TestTUICv5Proxy_StopTimeout(t *testing.T) {
	cfg := &config.TUICConfig{ListenAddr: ":8443"}
	proxy, _ := NewTUICv5ProxyWithAuth(cfg, tuicMockDialFunc, nil, "", "")
	tuicProxy := proxy.(*TUICv5Proxy)
	tuicProxy.mu.Lock()
	tuicProxy.running = true
	tuicProxy.stopCh = make(chan struct{})
	tuicProxy.mu.Unlock()
	tuicProxy.wg.Add(1)
	go func() {
		defer tuicProxy.wg.Done()
		time.Sleep(10 * time.Second)
	}()
	done := make(chan error)
	go func() { done <- proxy.Stop() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Error("Stop did not complete within timeout")
	}
}

// fakeTUICPacket is one UDP packet on the TUIC side of an association.
type fakeTUICPacket struct {
	data string
	addr M.Socksaddr
	err  error // returned by ReadPacket instead of a packet
}

// fakeTUICClient is the TUIC side of one UDP association.
type fakeTUICClient struct {
	N.PacketConn // methods the relay does not use
	in           chan fakeTUICPacket
	replies      chan fakeTUICPacket
	closed       chan struct{}
	closeOnce    sync.Once
}

func newFakeTUICClient() *fakeTUICClient {
	return &fakeTUICClient{
		in:      make(chan fakeTUICPacket, 4),
		replies: make(chan fakeTUICPacket, 2),
		closed:  make(chan struct{}),
	}
}

func (c *fakeTUICClient) ReadPacket(buffer *buf.Buffer) (M.Socksaddr, error) {
	p, ok := <-c.in
	if !ok {
		return M.Socksaddr{}, io.EOF
	}
	if p.err != nil {
		return M.Socksaddr{}, p.err
	}
	_, err := buffer.WriteString(p.data)
	return p.addr, err
}

func (c *fakeTUICClient) WritePacket(buffer *buf.Buffer, destination M.Socksaddr) error {
	c.replies <- fakeTUICPacket{data: string(buffer.Bytes()), addr: destination}
	return nil
}

func (c *fakeTUICClient) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

func recvWithin[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting on channel")
	}
	var zero T
	return zero
}

// One association sends to two destinations. Each packet must reach its own
// destination, and each reply must be tagged with the destination it came from.
func TestTUICUDPRelay_PerDestination(t *testing.T) {
	type dialed struct {
		addr string
		far  net.Conn
	}
	dials := make(chan dialed, 2)
	h := &TUICv5Handler{proxy: &TUICv5Proxy{config: &config.TUICConfig{}, dialFunc: func(_ context.Context, _, addr string) (net.Conn, error) {
		near, far := net.Pipe()
		dials <- dialed{addr: addr, far: far}
		return near, nil
	}}}
	client := newFakeTUICClient()
	done := make(chan struct{})
	go func() {
		h.relayUDPPackets(context.Background(), client, "test", "group")
		close(done)
	}()

	for _, dest := range []string{"10.0.0.1:1000", "10.0.0.2:2000"} {
		client.in <- fakeTUICPacket{data: "to " + dest, addr: M.ParseSocksaddr(dest)}
		d := recvWithin(t, dials)
		if d.addr != dest {
			t.Fatalf("dialed %s, want %s", d.addr, dest)
		}
		b := make([]byte, 64)
		n, err := d.far.Read(b)
		if err != nil || string(b[:n]) != "to "+dest {
			t.Fatalf("tunnel for %s got %q, err %v", dest, b[:n], err)
		}
		if _, err := d.far.Write([]byte("from " + dest)); err != nil {
			t.Fatal(err)
		}
		r := recvWithin(t, client.replies)
		if r.addr.String() != dest || r.data != "from "+dest {
			t.Fatalf("reply %q tagged %s, want %q tagged %s", r.data, r.addr, "from "+dest, dest)
		}
	}

	close(client.in)
	recvWithin(t, done)
}

// sing-quic's ReadPacket returns a deadline error once the association has
// been idle for UDPTimeout. The relay must then end, close the association
// and release its tunnels. It used to retry the read forever.
func TestTUICUDPRelay_EndsOnIdleTimeout(t *testing.T) {
	tunnels := make(chan net.Conn, 1)
	h := &TUICv5Handler{proxy: &TUICv5Proxy{config: &config.TUICConfig{}, dialFunc: func(context.Context, string, string) (net.Conn, error) {
		near, far := net.Pipe()
		tunnels <- far
		return near, nil
	}}}
	client := newFakeTUICClient()
	done := make(chan struct{})
	go func() {
		h.relayUDPPackets(context.Background(), client, "test", "group")
		close(done)
	}()

	client.in <- fakeTUICPacket{data: "ping", addr: M.ParseSocksaddr("10.0.0.1:1000")}
	far := recvWithin(t, tunnels)
	b := make([]byte, 64)
	if _, err := far.Read(b); err != nil {
		t.Fatal(err)
	}

	client.in <- fakeTUICPacket{err: os.ErrDeadlineExceeded}
	recvWithin(t, done)
	recvWithin(t, client.closed)
	if _, err := far.Read(b); err == nil {
		t.Fatal("tunnel still open after the association timed out")
	}
}

// A slow dial for one destination must not stall packets to another one.
func TestTUICUDPRelay_SlowDialDoesNotStall(t *testing.T) {
	release := make(chan struct{})
	tunnels := make(chan net.Conn, 2)
	h := &TUICv5Handler{proxy: &TUICv5Proxy{config: &config.TUICConfig{}, dialFunc: func(_ context.Context, _, addr string) (net.Conn, error) {
		if addr == "10.0.0.1:1000" {
			<-release
		}
		near, far := net.Pipe()
		tunnels <- far
		return near, nil
	}}}
	client := newFakeTUICClient()
	done := make(chan struct{})
	go func() {
		h.relayUDPPackets(context.Background(), client, "test", "group")
		close(done)
	}()

	client.in <- fakeTUICPacket{data: "to slow", addr: M.ParseSocksaddr("10.0.0.1:1000")}
	client.in <- fakeTUICPacket{data: "to fast", addr: M.ParseSocksaddr("10.0.0.2:2000")}
	far := recvWithin(t, tunnels)
	_ = far.SetReadDeadline(time.Now().Add(2 * time.Second))
	b := make([]byte, 64)
	n, err := far.Read(b)
	if err != nil || string(b[:n]) != "to fast" {
		t.Fatalf("fast destination got %q, err %v", b[:n], err)
	}

	close(release)
	slow := recvWithin(t, tunnels)
	_ = slow.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err = slow.Read(b)
	if err != nil || string(b[:n]) != "to slow" {
		t.Fatalf("slow destination got %q, err %v", b[:n], err)
	}
	close(client.in)
	recvWithin(t, done)
}

// A destination with no traffic for the idle timeout must close its tunnel
// while the association stays open, and a later packet must dial again.
func TestTUICUDPRelay_EvictsIdleDestination(t *testing.T) {
	tunnels := make(chan net.Conn, 2)
	h := &TUICv5Handler{proxy: &TUICv5Proxy{config: &config.TUICConfig{MaxIdleTimeout: 1}, dialFunc: func(context.Context, string, string) (net.Conn, error) {
		near, far := net.Pipe()
		tunnels <- far
		return near, nil
	}}}
	client := newFakeTUICClient()
	done := make(chan struct{})
	go func() {
		h.relayUDPPackets(context.Background(), client, "test", "group")
		close(done)
	}()

	dest := M.ParseSocksaddr("10.0.0.1:1000")
	b := make([]byte, 64)
	for _, data := range []string{"one", "two"} {
		client.in <- fakeTUICPacket{data: data, addr: dest}
		far := recvWithin(t, tunnels)
		_ = far.SetReadDeadline(time.Now().Add(3 * time.Second))
		if n, err := far.Read(b); err != nil || string(b[:n]) != data {
			t.Fatalf("tunnel got %q, err %v, want %q", b[:n], err, data)
		}
		if _, err := far.Read(b); err != io.EOF {
			t.Fatalf("idle tunnel was not closed: %v", err)
		}
	}

	close(client.in)
	recvWithin(t, done)
}

// A destination whose dial fails must not be dialed again for every packet.
// It is dialed again only after the idle timeout.
func TestTUICUDPRelay_FailedDialHeldForIdleTimeout(t *testing.T) {
	var dials atomic.Int32
	h := &TUICv5Handler{proxy: &TUICv5Proxy{config: &config.TUICConfig{MaxIdleTimeout: 1}, dialFunc: func(context.Context, string, string) (net.Conn, error) {
		dials.Add(1)
		return nil, errors.New("no client online")
	}}}
	client := newFakeTUICClient()
	done := make(chan struct{})
	go func() {
		h.relayUDPPackets(context.Background(), client, "test", "group")
		close(done)
	}()

	dest := M.ParseSocksaddr("10.0.0.1:1000")
	for i := 0; i < 5; i++ {
		client.in <- fakeTUICPacket{data: "x", addr: dest}
		time.Sleep(20 * time.Millisecond)
	}
	if n := dials.Load(); n != 1 {
		t.Fatalf("%d dials for 5 packets, want 1", n)
	}

	time.Sleep(time.Second)
	client.in <- fakeTUICPacket{data: "x", addr: dest}
	deadline := time.Now().Add(2 * time.Second)
	for dials.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := dials.Load(); n != 2 {
		t.Fatalf("%d dials after the idle timeout, want 2", n)
	}
	close(client.in)
	recvWithin(t, done)
}

// Two UDP associations opened in the same second must get different
// connection IDs. The IDs used to be only the time part of an xid, so the
// second association was dropped from monitoring as a duplicate.
func TestTUICUDP_ConnIDsAreUnique(t *testing.T) {
	h := &TUICv5Handler{proxy: &TUICv5Proxy{config: &config.TUICConfig{}}}
	ctx := auth.ContextWithUser(context.Background(), "group")
	target := M.ParseSocksaddr("10.9.9.9:9999")
	clients := []*fakeTUICClient{newFakeTUICClient(), newFakeTUICClient()}
	before := monitoring.GetAllConnectionMetrics() // a previous run may not have closed its entries yet
	for _, c := range clients {
		h.NewPacketConnectionEx(ctx, c, M.Socksaddr{}, target, nil)
	}

	ids := 0
	for id, conn := range monitoring.GetAllConnectionMetrics() {
		if _, old := before[id]; !old && conn.TargetHost == "tuic-udp:"+target.String() {
			ids++
		}
	}
	for _, c := range clients {
		close(c.in)
		recvWithin(t, c.closed)
	}
	if ids != 2 {
		t.Fatalf("monitoring has %d connections for 2 associations, want 2", ids)
	}
}
