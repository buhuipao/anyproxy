// Package protocols provides TUIC v5 proxy implementation using sing-quic library
// This implementation is compatible with Clash Meta and sing-box clients
package protocols

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	commonctx "github.com/buhuipao/anyproxy/pkg/common/context"
	"github.com/buhuipao/anyproxy/pkg/common/monitoring"
	"github.com/buhuipao/anyproxy/pkg/common/utils"
	"github.com/buhuipao/anyproxy/pkg/config"
	"github.com/buhuipao/anyproxy/pkg/logger"
	"github.com/google/uuid"
	"github.com/sagernet/sing-quic/tuic"
	"github.com/sagernet/sing/common/auth"
	"github.com/sagernet/sing/common/buf"
	singLogger "github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	sTLS "github.com/sagernet/sing/common/tls"
)

// stdServerConfig wraps *tls.Config to implement sTLS.ServerConfig interface
type stdServerConfig struct {
	config *tls.Config
}

func newStdServerConfig(config *tls.Config) sTLS.ServerConfig {
	return &stdServerConfig{config: config}
}

func (s *stdServerConfig) ServerName() string {
	return s.config.ServerName
}

func (s *stdServerConfig) SetServerName(serverName string) {
	s.config.ServerName = serverName
}

func (s *stdServerConfig) NextProtos() []string {
	return s.config.NextProtos
}

func (s *stdServerConfig) SetNextProtos(nextProto []string) {
	s.config.NextProtos = nextProto
}

func (s *stdServerConfig) Config() (*sTLS.STDConfig, error) {
	return s.config, nil
}

func (s *stdServerConfig) Client(conn net.Conn) (sTLS.Conn, error) {
	tlsConn := tls.Client(conn, s.config)
	return &stdTLSConn{tlsConn}, nil
}

func (s *stdServerConfig) Clone() sTLS.Config {
	return &stdServerConfig{config: s.config.Clone()}
}

func (s *stdServerConfig) Start() error {
	return nil
}

func (s *stdServerConfig) Close() error {
	return nil
}

func (s *stdServerConfig) Server(conn net.Conn) (sTLS.Conn, error) {
	tlsConn := tls.Server(conn, s.config)
	return &stdTLSConn{tlsConn}, nil
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

// singLoggerAdapter adapts our slog logger to sing's logger.Logger interface
type singLoggerAdapter struct{}

// Ensure singLoggerAdapter implements singLogger.Logger at compile time
var _ singLogger.Logger = (*singLoggerAdapter)(nil)

func (l *singLoggerAdapter) Trace(args ...any) {
	logger.Debug("TUIC [trace]", "args", fmt.Sprint(args...))
}

func (l *singLoggerAdapter) Debug(args ...any) {
	logger.Debug("TUIC", "msg", fmt.Sprint(args...))
}

func (l *singLoggerAdapter) Info(args ...any) {
	logger.Info("TUIC", "msg", fmt.Sprint(args...))
}

func (l *singLoggerAdapter) Warn(args ...any) {
	logger.Warn("TUIC", "msg", fmt.Sprint(args...))
}

func (l *singLoggerAdapter) Error(args ...any) {
	logger.Error("TUIC", "msg", fmt.Sprint(args...))
}

func (l *singLoggerAdapter) Fatal(args ...any) {
	logger.Error("TUIC [fatal]", "msg", fmt.Sprint(args...))
}

func (l *singLoggerAdapter) Panic(args ...any) {
	logger.Error("TUIC [panic]", "msg", fmt.Sprint(args...))
}

// TUICv5Proxy implements TUIC v5 proxy using sing-quic library
// Compatible with Clash Meta and sing-box clients
type TUICv5Proxy struct {
	config   *config.TUICConfig
	tlsCert  string
	tlsKey   string
	running  bool
	mu       sync.Mutex
	stopCh   chan struct{}
	stopOnce sync.Once // 🛡️ Ensure stopCh is only closed once
	wg       sync.WaitGroup

	// UDP listener for QUIC
	udpConn *net.UDPConn

	// sing-quic TUIC service
	service *tuic.Service[string]

	// anyproxy integration
	dialFunc       func(ctx context.Context, network, addr string) (net.Conn, error)
	groupValidator func(string, string) bool

	// User management (group_id -> password)
	users   map[string]string
	usersMu sync.RWMutex
}

// TUICCredentialObserver interface for credential manager callback
// This avoids import cycle between protocols and credential packages
type TUICCredentialObserver interface {
	OnCredentialUpdate(groupID, password string)
	OnCredentialRemove(groupID string)
}

// TUICUserInitializer interface for initializing users from existing credentials
type TUICUserInitializer interface {
	InitializeUsers(users map[string]string)
}

// TUICv5Handler implements sing-quic's ServiceHandler interface
// Bridges TUIC connections to anyproxy's routing system
type TUICv5Handler struct {
	proxy *TUICv5Proxy
}

// Ensure TUICv5Handler implements the required interfaces
var _ N.TCPConnectionHandlerEx = (*TUICv5Handler)(nil)
var _ N.UDPConnectionHandlerEx = (*TUICv5Handler)(nil)

// OnCredentialUpdate implements credential.CredentialObserver interface
// This is called when group credentials are registered/updated in credential manager
func (p *TUICv5Proxy) OnCredentialUpdate(groupID, password string) {
	p.usersMu.Lock()
	defer p.usersMu.Unlock()

	// Store in local users map
	p.users[groupID] = password

	// Update sing-quic service users if service is running
	if p.service != nil {
		p.syncUsersToService()
		logger.Debug("TUIC v5 user credential updated", "group_id", groupID)
	} else {
		logger.Debug("TUIC v5 credential stored (service not started yet)", "group_id", groupID)
	}
}

// OnCredentialRemove implements credential.CredentialObserver interface
// This is called when group credentials are removed (e.g., client disconnects)
func (p *TUICv5Proxy) OnCredentialRemove(groupID string) {
	p.usersMu.Lock()
	defer p.usersMu.Unlock()

	// Remove from local users map
	delete(p.users, groupID)

	// Update sing-quic service users if service is running
	if p.service != nil {
		p.syncUsersToService()
		logger.Debug("TUIC v5 user credential removed", "group_id", groupID)
	}
}

// anyproxyTUICNamespace is a fixed namespace UUID for deriving TUIC user UUIDs
// from non-UUID group IDs using UUID v5 (SHA-1 based, deterministic).
var anyproxyTUICNamespace = uuid.MustParse("f1e2d3c4-b5a6-4789-8012-3456789abcde")

// groupIDToUUID converts groupID to TUIC UUID format.
// If groupID is already a valid UUID string, it is parsed directly.
// Otherwise, a deterministic UUID v5 is derived from the groupID.
func groupIDToUUID(groupID string) (result [16]byte, formatted string) {
	// Try parsing as UUID string (e.g., "901c0948-6d6a-4717-a9b3-a45d44568824")
	if parsed, err := uuid.Parse(groupID); err == nil {
		result = parsed
		return result, parsed.String()
	}

	// Derive deterministic UUID v5 from groupID
	derived := uuid.NewSHA1(anyproxyTUICNamespace, []byte(groupID))
	result = derived
	return result, derived.String()
}

// syncUsersToService syncs all users to sing-quic service
// Must be called with usersMu lock held
func (p *TUICv5Proxy) syncUsersToService() {
	var userList []string
	var uuidList [][16]byte
	var passwordList []string

	for groupID, password := range p.users {
		// Convert groupID to UUID (copy bytes, pad with zeros)
		uuid, uuidStr := groupIDToUUID(groupID)
		userList = append(userList, groupID)
		uuidList = append(uuidList, uuid)
		passwordList = append(passwordList, password)

		// Log the UUID mapping for user reference
		logger.Debug("TUIC user registered",
			"group_id", groupID,
			"uuid", uuidStr,
			"hint", "Use this UUID in TUIC client config")
	}

	if len(userList) > 0 {
		p.service.UpdateUsers(userList, uuidList, passwordList)
		logger.Debug("Synced users to TUIC service", "count", len(userList))
	}
}

// InitializeUsers loads initial credentials from credential manager
// This should be called after the service is started
func (p *TUICv5Proxy) InitializeUsers(users map[string]string) {
	p.usersMu.Lock()
	defer p.usersMu.Unlock()

	for groupID, password := range users {
		p.users[groupID] = password
	}

	if p.service != nil && len(p.users) > 0 {
		p.syncUsersToService()
		logger.Info("TUIC v5 initialized with existing users", "count", len(users))
	}
}

// NewTUICv5ProxyWithAuth creates a new TUIC v5 proxy with authentication
func NewTUICv5ProxyWithAuth(
	cfg *config.TUICConfig,
	dialFn func(context.Context, string, string) (net.Conn, error),
	groupValidator func(string, string) bool,
	tlsCert, tlsKey string,
) (utils.GatewayProxy, error) {
	if cfg.ListenAddr == "" {
		return nil, fmt.Errorf("TUIC v5 listen address is required")
	}

	proxy := &TUICv5Proxy{
		config:         cfg,
		tlsCert:        tlsCert,
		tlsKey:         tlsKey,
		dialFunc:       dialFn,
		groupValidator: groupValidator,
		users:          make(map[string]string),
		stopCh:         make(chan struct{}),
	}

	return proxy, nil
}

// Start starts the TUIC v5 proxy
func (p *TUICv5Proxy) Start() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.running {
		return fmt.Errorf("TUIC v5 proxy is already running")
	}

	// Load TLS certificate
	cert, err := tls.LoadX509KeyPair(p.tlsCert, p.tlsKey)
	if err != nil {
		return fmt.Errorf("failed to load TLS certificate: %w", err)
	}

	// Create standard TLS config
	stdTLSConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   p.config.GetALPN(),
		MinVersion:   tls.VersionTLS13,
	}

	// Wrap as sing TLS ServerConfig
	singTLSConfig := newStdServerConfig(stdTLSConfig)

	// Create UDP listener for QUIC
	udpAddr, err := net.ResolveUDPAddr("udp", p.config.ListenAddr)
	if err != nil {
		return fmt.Errorf("failed to resolve UDP address: %w", err)
	}

	udpConn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return fmt.Errorf("failed to create UDP listener: %w", err)
	}
	p.udpConn = udpConn

	// Create sing-quic TUIC service
	handler := &TUICv5Handler{proxy: p}
	service, err := tuic.NewService[string](tuic.ServiceOptions{
		Context:           context.Background(),
		Logger:            &singLoggerAdapter{}, // Required to prevent nil pointer panic
		Handler:           handler,
		TLSConfig:         singTLSConfig,
		CongestionControl: p.config.GetCongestionControl(),
		AuthTimeout:       time.Duration(p.config.GetAuthTimeout()) * time.Second,
		ZeroRTTHandshake:  p.config.ZeroRTT,
		Heartbeat:         time.Duration(p.config.GetHeartbeat()) * time.Second,
		UDPTimeout:        time.Duration(p.config.GetMaxIdleTimeout()) * time.Second,
	})
	if err != nil {
		_ = udpConn.Close()
		return fmt.Errorf("failed to create TUIC service: %w", err)
	}
	p.service = service

	// Start the service with UDP listener
	err = p.service.Start(udpConn)
	if err != nil {
		_ = udpConn.Close()
		return fmt.Errorf("failed to start TUIC service: %w", err)
	}

	p.running = true

	logger.Info("TUIC v5 proxy started",
		"listen", p.config.ListenAddr,
		"congestion", p.config.GetCongestionControl(),
		"udp_relay_mode", p.config.GetUDPRelayMode(),
		"zero_rtt", p.config.ZeroRTT,
	)

	return nil
}

// Stop stops the TUIC v5 proxy
func (p *TUICv5Proxy) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.running {
		return nil
	}

	p.running = false
	// 🛡️ Use sync.Once to prevent double-close panic
	p.stopOnce.Do(func() {
		close(p.stopCh)
	})

	if p.service != nil {
		if err := p.service.Close(); err != nil {
			logger.Error("Failed to close TUIC service", "err", err)
		}
	}

	if p.udpConn != nil {
		if err := p.udpConn.Close(); err != nil {
			logger.Error("Failed to close UDP listener", "err", err)
		}
	}

	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		logger.Debug("All TUIC v5 goroutines finished")
	case <-time.After(3 * time.Second):
		logger.Warn("Timeout waiting for TUIC v5 goroutines")
	}

	logger.Info("TUIC v5 proxy stopped")
	return nil
}

// IsRunning returns whether the proxy is running
func (p *TUICv5Proxy) IsRunning() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.running
}

// ============================================================================
// TUICv5Handler - implements sing-quic ServiceHandler interface
// ============================================================================

// NewConnectionEx handles TCP relay (TUIC Connect command)
// Implements N.TCPConnectionHandlerEx interface
func (h *TUICv5Handler) NewConnectionEx(ctx context.Context, conn net.Conn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	target := destination.String()
	startTime := time.Now()
	connID := "tuic-tcp-" + utils.GenerateConnID()

	// Extract groupID from sing-quic auth context
	groupID, ok := auth.UserFromContext[string](ctx)
	if !ok || groupID == "" {
		logger.Error("TUIC v5 TCP: missing user context", "conn_id", connID, "source", source.String(), "target", target)
		_ = conn.Close()
		if onClose != nil {
			onClose(nil)
		}
		return
	}

	logger.Info("TUIC v5 TCP: new connection", "conn_id", connID, "source", source.String(), "target", target, "group_id", groupID)

	// 🔧 FIX: Register TCP connection with monitoring for web UI statistics
	monitoring.CreateConnection(connID, groupID, "tuic-tcp:"+target)

	// Create context with our user context for dial function
	dialCtx := commonctx.WithUserContext(ctx, &utils.UserContext{GroupID: groupID})

	go func() {
		defer func() {
			duration := time.Since(startTime)
			// 🔧 FIX: Close connection in monitoring when TUIC TCP relay ends
			monitoring.CloseConnection(connID)
			logger.Debug("TUIC v5 TCP: connection closed", "conn_id", connID, "target", target, "group_id", groupID, "duration", duration)
			if onClose != nil {
				onClose(nil)
			}
		}()

		logger.Debug("TUIC v5 TCP: dialing tunnel", "conn_id", connID, "target", target, "group_id", groupID)
		targetConn, err := h.proxy.dialFunc(dialCtx, "tcp", target)
		if err != nil {
			logger.Error("TUIC v5 TCP: failed to dial tunnel", "conn_id", connID, "target", target, "group_id", groupID, "err", err)
			_ = conn.Close()
			return
		}
		defer func() { _ = targetConn.Close() }()
		defer func() { _ = conn.Close() }()

		logger.Info("TUIC v5 TCP: tunnel established, starting relay", "conn_id", connID, "target", target, "dial_duration", time.Since(startTime))

		// Bidirectional copy with monitoring.
		// When either direction finishes (e.g., Sunshine closes the RTSP connection),
		// we must close BOTH conn and targetConn so the other io.Copy unblocks.
		// Without this, wg.Wait() deadlocks: the stuck goroutine's conn.Read()
		// blocks forever because conn.Close() is in a defer AFTER wg.Wait().
		// This causes Moonlight to never see EOF on the TUIC stream, so it waits
		// 15 seconds instead of proceeding to start UDP video/audio streams.
		var wg sync.WaitGroup
		var toTargetBytes, fromTargetBytes int64
		done := make(chan struct{})
		var doneOnce sync.Once
		wg.Add(2)
		go func() {
			defer wg.Done()
			n, _ := io.Copy(targetConn, conn)
			toTargetBytes = n
			if n > 0 {
				monitoring.UpdateConnectionBytes(connID, groupID, n, 0)
			}
			doneOnce.Do(func() { close(done) })
		}()
		go func() {
			defer wg.Done()
			n, _ := io.Copy(conn, targetConn)
			fromTargetBytes = n
			if n > 0 {
				monitoring.UpdateConnectionBytes(connID, groupID, 0, n)
			}
			doneOnce.Do(func() { close(done) })
		}()
		<-done
		_ = conn.Close()
		_ = targetConn.Close()
		wg.Wait()

		logger.Debug("TUIC v5 TCP: relay finished", "conn_id", connID, "target", target, "to_target_bytes", toTargetBytes, "from_target_bytes", fromTargetBytes)
	}()
}

// NewPacketConnectionEx handles UDP relay (TUIC Packet command)
// Implements N.UDPConnectionHandlerEx interface
func (h *TUICv5Handler) NewPacketConnectionEx(ctx context.Context, conn N.PacketConn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	target := destination.String()
	startTime := time.Now()
	connID := "tuic-udp-" + utils.GenerateConnID()

	// Extract groupID from sing-quic auth context
	groupID, ok := auth.UserFromContext[string](ctx)
	if !ok || groupID == "" {
		logger.Error("TUIC v5 UDP: missing user context", "conn_id", connID, "source", source.String(), "target", target)
		// sing-quic passes a nil onClose, so close the association here or it is never released
		_ = conn.Close()
		if onClose != nil {
			onClose(nil)
		}
		return
	}

	logger.Info("TUIC v5 UDP: new connection", "conn_id", connID, "source", source.String(), "target", target, "group_id", groupID)

	// 🔧 FIX: Register UDP connection with monitoring for web UI statistics
	monitoring.CreateConnection(connID, groupID, "tuic-udp:"+target)

	// Create context with our user context for dial function
	dialCtx := commonctx.WithUserContext(ctx, &utils.UserContext{GroupID: groupID})

	go func() {
		defer func() {
			duration := time.Since(startTime)
			// 🔧 FIX: Close connection in monitoring when TUIC UDP relay ends
			monitoring.CloseConnection(connID)
			logger.Debug("TUIC v5 UDP: connection closed", "conn_id", connID, "target", target, "group_id", groupID, "duration", duration)
			if onClose != nil {
				onClose(nil)
			}
		}()

		h.relayUDPPackets(dialCtx, conn, connID, groupID)
		logger.Debug("TUIC v5 UDP: relay finished", "conn_id", connID, "target", target, "total_duration", time.Since(startTime))
	}()
}

const (
	// tuicUDPQueueSize is how many packets can wait for one destination's tunnel.
	tuicUDPQueueSize = 16
	// tuicUDPMaxDestinations caps the open destinations of one association.
	tuicUDPMaxDestinations = 256
)

// tuicUDPAssociation is the relay state of one TUIC UDP association (ASSOC_ID).
type tuicUDPAssociation struct {
	h           *TUICv5Handler
	ctx         context.Context
	client      N.PacketConn
	connID      string
	groupID     string
	idleTimeout time.Duration
	done        chan struct{} // closed when the association ends
	wg          sync.WaitGroup
	writeMu     sync.Mutex // serializes replies to client

	mu        sync.Mutex
	queues    map[string]chan []byte // packets waiting for each destination
	capLogged bool
}

// relayUDPPackets relays one TUIC UDP association (ASSOC_ID) through the tunnel.
// Like a UDP socket, one association can send to many destinations. Each
// destination has its own goroutine and tunnel connection, opened on the first
// packet to it, so a slow dial does not stall the other destinations. A
// destination is closed after it has been idle for the UDP timeout. Each reply
// is tagged with the destination it came from.
//
// The relay ends when the association is closed or has been idle for the
// service UDPTimeout. sing-quic does not end associations when the QUIC
// connection closes, so the idle timeout is what releases them in that case.
func (h *TUICv5Handler) relayUDPPackets(ctx context.Context, client N.PacketConn, connID string, groupID string) {
	logger.Debug("TUIC UDP relay: starting", "conn_id", connID)
	a := &tuicUDPAssociation{
		h:           h,
		ctx:         ctx,
		client:      client,
		connID:      connID,
		groupID:     groupID,
		idleTimeout: time.Duration(h.proxy.config.GetMaxIdleTimeout()) * time.Second,
		done:        make(chan struct{}),
		queues:      make(map[string]chan []byte),
	}

	// Stats for logging
	var clientToTargetPackets, clientToTargetBytes int64

	defer func() {
		// Closing the client makes sing-quic forget this association, so a
		// later packet with the same ASSOC_ID starts a new relay.
		_ = client.Close()
		close(a.done)
		a.wg.Wait()
		logger.Debug("TUIC UDP relay: finished",
			"conn_id", connID,
			"client_to_target_packets", clientToTargetPackets,
			"client_to_target_bytes", clientToTargetBytes)
	}()

	buffer := buf.NewSize(64 * 1024)
	defer buffer.Release()

	for {
		// ReadPacket blocks until a packet arrives. It fails only when the
		// association is closed or the idle timeout has passed.
		buffer.Reset()
		destination, err := client.ReadPacket(buffer)
		if err != nil {
			logger.Debug("TUIC UDP relay: association ended", "conn_id", connID, "err", err)
			return
		}
		if buffer.Len() == 0 || !a.enqueue(destination, buffer.Bytes()) {
			continue
		}
		clientToTargetPackets++
		clientToTargetBytes += int64(buffer.Len())
		// Sample logging: only log every 100 packets or first packet
		if clientToTargetPackets == 1 || clientToTargetPackets%100 == 0 {
			logger.Debug("TUIC UDP relay: client->target progress", "conn_id", connID, "total_packets", clientToTargetPackets, "total_bytes", clientToTargetBytes)
		}
	}
}

// enqueue hands a packet to its destination's goroutine and starts one for a
// new destination. It never blocks: the packet is dropped when the queue is
// full, or when it is for a new destination and the association is at the cap.
func (a *tuicUDPAssociation) enqueue(destination M.Socksaddr, data []byte) bool {
	key := destination.String()
	a.mu.Lock()
	defer a.mu.Unlock()
	queue, ok := a.queues[key]
	if !ok {
		if len(a.queues) >= tuicUDPMaxDestinations {
			if !a.capLogged {
				a.capLogged = true
				logger.Warn("TUIC UDP relay: too many destinations, dropping packets to new ones", "conn_id", a.connID, "max", tuicUDPMaxDestinations)
			}
			return false
		}
		queue = make(chan []byte, tuicUDPQueueSize)
		a.queues[key] = queue
		a.wg.Add(1)
		go a.relayDestination(destination, queue)
	}
	select {
	case queue <- bytes.Clone(data):
		return true
	default:
		return false
	}
}

// forget removes a destination, so the next packet to it starts over.
func (a *tuicUDPAssociation) forget(key string) {
	a.mu.Lock()
	delete(a.queues, key)
	a.mu.Unlock()
}

// forgetIdle removes a destination unless a packet is waiting for it.
func (a *tuicUDPAssociation) forgetIdle(key string, queue <-chan []byte) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(queue) > 0 {
		return false
	}
	delete(a.queues, key)
	return true
}

// relayDestination dials the tunnel for one destination, then writes its
// queued packets until the association ends, the tunnel fails, or there has
// been no traffic in either direction for the idle timeout.
func (a *tuicUDPAssociation) relayDestination(destination M.Socksaddr, queue <-chan []byte) {
	defer a.wg.Done()
	targetAddr := destination.String()
	logger.Debug("TUIC v5 UDP: dialing tunnel", "conn_id", a.connID, "target", targetAddr, "group_id", a.groupID)
	target, err := a.h.proxy.dialFunc(a.ctx, "udp", targetAddr)
	if err != nil {
		logger.Warn("TUIC v5 UDP: failed to dial tunnel", "conn_id", a.connID, "target", targetAddr, "group_id", a.groupID, "err", err)
		a.holdFailed(targetAddr, queue)
		return
	}
	logger.Info("TUIC v5 UDP: tunnel established", "conn_id", a.connID, "target", targetAddr, "type", fmt.Sprintf("%T", target))

	var active atomic.Int64 // time of the last traffic, in Unix nanoseconds
	active.Store(time.Now().UnixNano())
	replies := make(chan struct{})
	go func() {
		defer close(replies)
		a.relayReplies(target, destination, &active)
	}()
	defer func() {
		// BufferedConn.SetReadDeadline is a no-op, so closing the tunnel
		// connection is what unblocks the reply goroutine.
		_ = target.Close()
		<-replies
	}()

	idle := time.NewTimer(a.idleTimeout)
	defer idle.Stop()
	for {
		select {
		case data := <-queue:
			n, err := target.Write(data)
			if err != nil {
				// The tunnel is gone. Forget it so the next packet dials a new one.
				logger.Warn("TUIC UDP relay: tunnel write error", "conn_id", a.connID, "target", targetAddr, "err", err)
				a.forget(targetAddr)
				return
			}
			// Update monitoring for UDP bytes sent (client -> target)
			if n > 0 {
				monitoring.UpdateConnectionBytes(a.connID, a.groupID, int64(n), 0)
			}
			active.Store(time.Now().UnixNano())
		case <-idle.C:
			// Replies count as traffic too, so check the real idle time.
			if left := a.idleTimeout - time.Since(time.Unix(0, active.Load())); left > 0 {
				idle.Reset(left)
				continue
			}
			if a.forgetIdle(targetAddr, queue) {
				logger.Debug("TUIC UDP relay: destination idle, closing tunnel", "conn_id", a.connID, "target", targetAddr)
				return
			}
			idle.Reset(a.idleTimeout)
		case <-a.done:
			return
		}
	}
}

// holdFailed keeps a destination whose dial failed and drops its packets for
// one idle timeout, counted from the failure, so a dead target is dialed once
// per timeout instead of once per packet. Then a later packet dials again.
func (a *tuicUDPAssociation) holdFailed(key string, queue <-chan []byte) {
	hold := time.NewTimer(a.idleTimeout)
	defer hold.Stop()
	for {
		select {
		case <-queue:
		case <-hold.C:
			a.forget(key)
			return
		case <-a.done:
			return
		}
	}
}

// relayReplies copies replies from one destination's tunnel connection back
// to the TUIC client, tagged with that destination as the source.
func (a *tuicUDPAssociation) relayReplies(target net.Conn, destination M.Socksaddr, active *atomic.Int64) {
	targetAddr := destination.String()
	var targetToClientPackets, targetToClientBytes int64
	defer func() {
		// Recover from panic in case sing-quic panics on write after close
		if r := recover(); r != nil {
			logger.Warn("TUIC UDP relay: target->client recovered from panic", "conn_id", a.connID, "target", targetAddr, "panic", r)
		}
		logger.Debug("TUIC UDP relay: target->client goroutine exiting",
			"conn_id", a.connID,
			"target", targetAddr,
			"packets", targetToClientPackets,
			"bytes", targetToClientBytes)
	}()

	rawBuffer := make([]byte, 64*1024)
	for {
		// Read from tunnel - this blocks until data arrives or the connection is closed
		n, err := target.Read(rawBuffer)
		if err != nil {
			logger.Debug("TUIC UDP relay: tunnel closed", "conn_id", a.connID, "target", targetAddr, "err", err)
			return
		}
		if n == 0 {
			continue
		}

		// Update monitoring for UDP bytes received (target -> client)
		monitoring.UpdateConnectionBytes(a.connID, a.groupID, 0, int64(n))

		// The defer recover will catch any panic from sing-quic
		err = a.writeReply(rawBuffer[:n], destination)
		if err != nil {
			logger.Warn("TUIC UDP relay: client write error", "conn_id", a.connID, "target", targetAddr, "err", err)
			// The association can no longer take replies; end it. This
			// unblocks ReadPacket in relayUDPPackets.
			_ = a.client.Close()
			return
		}
		active.Store(time.Now().UnixNano())
		targetToClientPackets++
		targetToClientBytes += int64(n)
		// Sample logging: only log every 100 packets or first packet
		if targetToClientPackets == 1 || targetToClientPackets%100 == 0 {
			logger.Debug("TUIC UDP relay: target->client progress", "conn_id", a.connID, "target", targetAddr, "total_packets", targetToClientPackets, "total_bytes", targetToClientBytes)
		}
	}
}

// writeReply sends one reply to the TUIC client. sing-quic's packet conn is
// not safe for concurrent writers, so replies from all destinations take turns.
func (a *tuicUDPAssociation) writeReply(data []byte, destination M.Socksaddr) error {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	return a.client.WritePacket(buf.As(data), destination)
}
