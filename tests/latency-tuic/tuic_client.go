// Package main provides TUIC v5 test client for latency testing
// Uses sing-quic library to create a TUIC v5 client
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/sagernet/sing-quic/tuic"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	sTLS "github.com/sagernet/sing/common/tls"
)

// TUICClientOptions contains options for creating a TUIC client
type TUICClientOptions struct {
	ServerAddr     string        // TUIC server address (host:port)
	UUID           string        // User UUID (can be group_id)
	Password       string        // User password
	UDPRelayMode   string        // "native" (Datagram) or "quic" (Stream)
	Congestion     string        // Congestion control algorithm
	ZeroRTT        bool          // Enable 0-RTT handshake
	Heartbeat      time.Duration // Heartbeat interval
	TLSServerName  string        // TLS server name (SNI)
	TLSCertFile    string        // Path to server's TLS certificate (for verification)
	ConnectTimeout time.Duration // Connection timeout
}

// DefaultTUICClientOptions returns default options
func DefaultTUICClientOptions() TUICClientOptions {
	return TUICClientOptions{
		UDPRelayMode:   "native",
		Congestion:     "bbr",
		ZeroRTT:        true,
		Heartbeat:      10 * time.Second,
		ConnectTimeout: 10 * time.Second,
	}
}

// TUICTestClient wraps sing-quic TUIC client for testing
type TUICTestClient struct {
	opts    TUICClientOptions
	client  *tuic.Client
	rawConn net.PacketConn
	mu      sync.Mutex
	closed  bool
}

// stdClientConfig wraps *tls.Config to implement sTLS.Config interface
type stdClientConfig struct {
	config *tls.Config
}

func newStdClientConfig(config *tls.Config) sTLS.Config {
	return &stdClientConfig{config: config}
}

func (s *stdClientConfig) ServerName() string {
	return s.config.ServerName
}

func (s *stdClientConfig) SetServerName(serverName string) {
	s.config.ServerName = serverName
}

func (s *stdClientConfig) NextProtos() []string {
	return s.config.NextProtos
}

func (s *stdClientConfig) SetNextProtos(nextProto []string) {
	s.config.NextProtos = nextProto
}

func (s *stdClientConfig) Config() (*sTLS.STDConfig, error) {
	return s.config, nil
}

func (s *stdClientConfig) Client(conn net.Conn) (sTLS.Conn, error) {
	tlsConn := tls.Client(conn, s.config)
	return &stdTLSConn{tlsConn}, nil
}

func (s *stdClientConfig) Clone() sTLS.Config {
	return &stdClientConfig{config: s.config.Clone()}
}

// stdTLSConn wraps *tls.Conn to implement sTLS.Conn interface
type stdTLSConn struct {
	*tls.Conn
}

func (c *stdTLSConn) NetConn() net.Conn {
	return c.Conn.NetConn()
}

func (c *stdTLSConn) ConnectionState() sTLS.ConnectionState {
	return c.Conn.ConnectionState()
}

// NewTUICTestClient creates a new TUIC test client
func NewTUICTestClient(opts TUICClientOptions) (*TUICTestClient, error) {
	if opts.ServerAddr == "" {
		return nil, fmt.Errorf("server address is required")
	}
	if opts.UUID == "" {
		return nil, fmt.Errorf("UUID is required")
	}
	if opts.Password == "" {
		return nil, fmt.Errorf("password is required")
	}

	// Resolve server address
	serverAddr, err := net.ResolveUDPAddr("udp", opts.ServerAddr)
	if err != nil {
		return nil, fmt.Errorf("resolve server address: %w", err)
	}

	// Create UDP connection
	udpConn, err := net.ListenUDP("udp", &net.UDPAddr{Port: 0})
	if err != nil {
		return nil, fmt.Errorf("create UDP listener: %w", err)
	}

	// Set large buffer for high throughput
	udpConn.SetReadBuffer(16 * 1024 * 1024)
	udpConn.SetWriteBuffer(16 * 1024 * 1024)

	// Create TLS config
	tlsServerName := opts.TLSServerName
	if tlsServerName == "" {
		host, _, _ := net.SplitHostPort(opts.ServerAddr)
		tlsServerName = host
	}

	tlsConfig := &tls.Config{
		ServerName: tlsServerName,
		NextProtos: []string{"h3"}, // Match Gateway's default ALPN (blends with HTTP/3 traffic)
		MinVersion: tls.VersionTLS13,
	}

	// Load server certificate for verification if provided
	if opts.TLSCertFile != "" {
		certPEM, err := os.ReadFile(opts.TLSCertFile)
		if err != nil {
			udpConn.Close()
			return nil, fmt.Errorf("read TLS certificate: %w", err)
		}
		certPool := x509.NewCertPool()
		if !certPool.AppendCertsFromPEM(certPEM) {
			udpConn.Close()
			return nil, fmt.Errorf("failed to parse TLS certificate")
		}
		tlsConfig.RootCAs = certPool
	}

	// Convert UUID string to [16]byte
	var uuid [16]byte
	copy(uuid[:], opts.UUID)

	// Determine UDP relay mode: UDPStream=false means native (datagram), true means quic (stream)
	udpStream := opts.UDPRelayMode == "quic"

	// Create TUIC client
	client, err := tuic.NewClient(tuic.ClientOptions{
		Context:           context.Background(),
		Dialer:            &directDialer{conn: udpConn},
		ServerAddress:     M.SocksaddrFromNet(serverAddr),
		TLSConfig:         newStdClientConfig(tlsConfig),
		UUID:              uuid,
		Password:          opts.Password,
		CongestionControl: opts.Congestion,
		UDPStream:         udpStream,
		ZeroRTTHandshake:  opts.ZeroRTT,
		Heartbeat:         opts.Heartbeat,
	})
	if err != nil {
		udpConn.Close()
		return nil, fmt.Errorf("create TUIC client: %w", err)
	}

	return &TUICTestClient{
		opts:    opts,
		client:  client,
		rawConn: udpConn,
	}, nil
}

// directDialer implements N.Dialer for TUIC client
type directDialer struct {
	conn net.PacketConn
}

func (d *directDialer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	switch network {
	case "udp":
		return &packetConnWrapper{PacketConn: d.conn, addr: destination.UDPAddr()}, nil
	default:
		return net.Dial(network, destination.String())
	}
}

func (d *directDialer) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	return d.conn, nil
}

// packetConnWrapper wraps a PacketConn to implement net.Conn
type packetConnWrapper struct {
	net.PacketConn
	addr net.Addr
}

func (c *packetConnWrapper) Read(b []byte) (int, error) {
	n, _, err := c.PacketConn.ReadFrom(b)
	return n, err
}

func (c *packetConnWrapper) Write(b []byte) (int, error) {
	return c.PacketConn.WriteTo(b, c.addr)
}

func (c *packetConnWrapper) RemoteAddr() net.Addr {
	return c.addr
}

// DialTCP establishes a TCP connection through TUIC tunnel
func (c *TUICTestClient) DialTCP(ctx context.Context, target string) (net.Conn, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, fmt.Errorf("client is closed")
	}
	c.mu.Unlock()

	dest := M.ParseSocksaddr(target)
	if !dest.IsValid() {
		return nil, fmt.Errorf("invalid target address: %s", target)
	}

	conn, err := c.client.DialConn(ctx, dest)
	if err != nil {
		return nil, fmt.Errorf("dial through TUIC: %w", err)
	}

	return conn, nil
}

// DialUDP establishes a UDP connection through TUIC tunnel
// Returns a PacketConn that can send/receive UDP packets to any destination
func (c *TUICTestClient) DialUDP(ctx context.Context) (*TUICPacketConn, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, fmt.Errorf("client is closed")
	}
	c.mu.Unlock()

	conn, err := c.client.ListenPacket(ctx)
	if err != nil {
		return nil, fmt.Errorf("listen UDP through TUIC: %w", err)
	}

	return &TUICPacketConn{PacketConn: conn}, nil
}

// Close closes the TUIC client
func (c *TUICTestClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil
	}
	c.closed = true

	var errs []error
	if c.client != nil {
		if err := c.client.CloseWithError(errors.New("client closed")); err != nil {
			errs = append(errs, err)
		}
	}
	if c.rawConn != nil {
		if err := c.rawConn.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

// Mode returns the UDP relay mode
func (c *TUICTestClient) Mode() string {
	return c.opts.UDPRelayMode
}

// TUICPacketConn wraps net.PacketConn returned by TUIC client
type TUICPacketConn struct {
	net.PacketConn
}

// SetReadDeadline sets the read deadline
func (c *TUICPacketConn) SetReadDeadline(t time.Time) error {
	if conn, ok := c.PacketConn.(interface{ SetReadDeadline(time.Time) error }); ok {
		return conn.SetReadDeadline(t)
	}
	return nil
}

// SetWriteDeadline sets the write deadline
func (c *TUICPacketConn) SetWriteDeadline(t time.Time) error {
	if conn, ok := c.PacketConn.(interface{ SetWriteDeadline(time.Time) error }); ok {
		return conn.SetWriteDeadline(t)
	}
	return nil
}

// SetDeadline sets both read and write deadlines
func (c *TUICPacketConn) SetDeadline(t time.Time) error {
	if err := c.SetReadDeadline(t); err != nil {
		return err
	}
	return c.SetWriteDeadline(t)
}

// Ensure directDialer implements N.Dialer
var _ N.Dialer = (*directDialer)(nil)
