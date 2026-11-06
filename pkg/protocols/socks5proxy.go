// Package protocols provides SOCKS5 proxy implementation for anyproxy.
package protocols

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/buhuipao/anyproxy/pkg/common/connection"
	commonctx "github.com/buhuipao/anyproxy/pkg/common/context"
	"github.com/buhuipao/anyproxy/pkg/common/monitoring"
	"github.com/buhuipao/anyproxy/pkg/common/protocol"
	"github.com/buhuipao/anyproxy/pkg/common/utils"
	"github.com/buhuipao/anyproxy/pkg/config"
	"github.com/buhuipao/anyproxy/pkg/logger"
	"github.com/things-go/go-socks5"
	"github.com/things-go/go-socks5/statute"
)

// P-003: Buffer pool for UDP operations to reduce memory allocation
var udpBufferPool = sync.Pool{
	New: func() interface{} {
		// 64KB buffer for UDP datagrams (max UDP packet size is ~64KB)
		buf := make([]byte, 64*1024)
		return &buf
	},
}

// 🔧 PERFORMANCE: UDP work item for worker pool
type udpWorkItem struct {
	session    *UDPSession
	clientAddr *net.UDPAddr
	pk         statute.Datagram
}

// 🔧 PERFORMANCE: UDP worker pool size - increased for high throughput (1Gbps+)
const udpWorkerPoolSize = 256

// 🔧 PERFORMANCE: Concurrent UDP read workers on a single UDP socket.
// Multi-stream tests are bursty; a single ReadFromUDP loop can become a bottleneck and cause kernel drops.
// Note: Too many readers cause goroutine scheduling overhead and socket lock contention.
// Optimal is typically 2-4x CPU cores.
const udpReadWorkerPoolSize = 128

// UDPSession represents a UDP ASSOCIATE session
type UDPSession struct {
	ConnID      string             // Session connection ID
	ClientAddr  *net.UDPAddr       // SOCKS5 client UDP address
	UserContext *utils.UserContext // User authentication context
	CreatedAt   time.Time          // Session creation time
	LastActive  time.Time          // Last activity time

	// For tunnel connection management per target
	tunnelConns sync.Map // map[targetAddr]*tunnelConnInfo
}

// tunnelConnInfo holds tunnel connection info for a specific target
type tunnelConnInfo struct {
	conn      net.Conn
	connID    string
	stopChan  chan struct{}
	readyChan chan struct{} // 🔧 PERFORMANCE: Signal when connection is ready
	closeOnce sync.Once     // 🔧 Fix: Ensure stopChan is only closed once
}

// SOCKS5Proxy SOCKS5 proxy implementation
type SOCKS5Proxy struct {
	config         *config.SOCKS5Config
	server         *socks5.Server
	dialFunc       func(ctx context.Context, network, addr string) (net.Conn, error)
	groupValidator func(string, string) bool // Function to validate group credentials
	listener       net.Listener

	// UDP support with LRU eviction and timeout cleanup
	udpListener   *net.UDPConn
	udpSessionMgr *UDPSessionManager // F-001: Use session manager with LRU and timeout
	udpBindAddr   *net.UDPAddr

	// 🔧 PERFORMANCE: UDP worker pool for high throughput
	udpWorkChan chan *udpWorkItem
	udpStopChan chan struct{}
	udpStopOnce sync.Once
}

// NewSOCKS5ProxyWithAuth creates a new SOCKS5 proxy with authentication
func NewSOCKS5ProxyWithAuth(cfg *config.SOCKS5Config, dialFn func(context.Context, string, string) (net.Conn, error), groupValidator func(string, string) bool) (utils.GatewayProxy, error) {
	logger.Info("Creating SOCKS5 proxy with UDP tunnel support", "listen_addr", cfg.ListenAddr, "auth_enabled", "group-based")

	proxy := &SOCKS5Proxy{
		config:         cfg,
		dialFunc:       dialFn,
		groupValidator: groupValidator,
		// F-001: Initialize UDP session manager with LRU eviction and timeout cleanup
		udpSessionMgr: NewUDPSessionManager(DefaultMaxUDPSessions, DefaultUDPSessionTimeout),
	}

	// Configure authentication methods
	socks5Auths := []socks5.Authenticator{}

	if groupValidator != nil {
		logger.Debug("Configuring SOCKS5 group-based authentication")

		// Use built-in UserPassAuthenticator with custom credential store
		credStore := &GroupBasedCredentialStore{
			GroupValidator: groupValidator,
		}
		socks5Auths = append(socks5Auths, socks5.UserPassAuthenticator{
			Credentials: credStore,
		})
		logger.Debug("SOCKS5 group-based authentication configured")
	} else {
		logger.Debug("No authentication configured for SOCKS5 proxy")
	}

	// Create wrapped dial function with group information extraction support
	// This is used for TCP CONNECT command
	wrappedDialFunc := func(ctx context.Context, network, addr string, request *socks5.Request) (net.Conn, error) {
		return proxy.dialWithRequest(ctx, network, addr, request, dialFn)
	}

	// Create custom UDP ASSOCIATE handler that routes through Client tunnel
	customAssociateHandle := func(ctx context.Context, writer io.Writer, request *socks5.Request) error {
		return proxy.handleAssociate(ctx, writer, request)
	}

	logger.Debug("Configuring SOCKS5 server", "listen_addr", cfg.ListenAddr, "auth_methods_count", len(socks5Auths))

	// Build server options
	serverOpts := []socks5.Option{
		socks5.WithAuthMethods(socks5Auths),
		socks5.WithResolver(tunnelResolver{}),
		socks5.WithDialAndRequest(wrappedDialFunc),
		socks5.WithAssociateHandle(customAssociateHandle), // Custom UDP handler
		socks5.WithLogger(socks5Logger{}),
	}

	// Create SOCKS5 server with UDP support
	server := socks5.NewServer(serverOpts...)

	proxy.server = server
	logger.Info("SOCKS5 proxy created successfully with UDP support through tunnel", "listen_addr", cfg.ListenAddr)
	return proxy, nil
}

// tunnelResolver leaves domain names unresolved, so the dial function gets
// "domain:port" and the client at the far end of the tunnel does the lookup.
type tunnelResolver struct{}

// Resolve implements socks5.NameResolver.
func (tunnelResolver) Resolve(ctx context.Context, _ string) (context.Context, net.IP, error) {
	return ctx, nil, nil
}

// socks5Logger sends go-socks5 logs to the project logger. go-socks5 logs
// every CONNECT that ends because one side closed as an error; those go to
// Debug. Its Logger only has Errorf, so look for the error among the args.
type socks5Logger struct{}

func (socks5Logger) Errorf(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	for _, arg := range args {
		if err, ok := arg.(error); ok && isClosedConnErr(err) {
			logger.Debug("SOCKS5 connection closed", "detail", msg)
			return
		}
	}
	logger.Error("SOCKS5 server error", "detail", msg)
}

// dialWithRequest handles dial requests with user context extraction
func (p *SOCKS5Proxy) dialWithRequest(ctx context.Context, network, addr string, request *socks5.Request, dialFn func(context.Context, string, string) (net.Conn, error)) (net.Conn, error) {
	// Generate new connection ID
	connID := utils.GenerateConnID()

	clientAddr := "unknown"
	if request != nil {
		clientAddr = request.RemoteAddr.String()
	}
	logger.Info("SOCKS5 dial request received", "conn_id", connID, "network", network, "address", addr, "client", clientAddr)

	// Add connection ID to context
	ctx = commonctx.WithConnID(ctx, connID)

	userCtx, err := p.extractUserContext(request, connID, addr, clientAddr)
	if err != nil {
		return nil, err
	}

	// Add user context to context
	ctx = commonctx.WithUserContext(ctx, userCtx)

	logger.Debug("Calling dial function for SOCKS5 request", "conn_id", connID, "network", network, "address", addr, "username", userCtx.Username, "group_id", userCtx.GroupID)

	conn, err := dialFn(ctx, network, addr)

	if err != nil {
		logger.Error("SOCKS5 dial failed", "conn_id", connID, "network", network, "address", addr, "username", userCtx.Username, "group_id", userCtx.GroupID, "err", err)
		return nil, err
	}

	logger.Info("SOCKS5 dial successful", "conn_id", connID, "network", network, "address", addr, "username", userCtx.Username, "group_id", userCtx.GroupID)

	return conn, nil
}

// extractUserContext extracts user context from SOCKS5 request
func (p *SOCKS5Proxy) extractUserContext(request *socks5.Request, connID, addr, clientAddr string) (*utils.UserContext, error) {
	var userCtx *utils.UserContext

	// Extract user information from request's AuthContext
	if request != nil && request.AuthContext != nil && request.AuthContext.Payload != nil {
		if username, exists := request.AuthContext.Payload["username"]; exists {
			userCtx = &utils.UserContext{
				Username: username,
				GroupID:  username,
			}
			logger.Info("SOCKS5 user context extracted from authentication", "conn_id", connID, "username", username, "group_id", username, "target_addr", addr)
		} else {
			logger.Debug("No username found in SOCKS5 authentication context", "conn_id", connID)
		}
	}

	// Require authentication - no default group allowed
	if userCtx == nil {
		logger.Error("SOCKS5 request requires authentication", "conn_id", connID, "target_addr", addr, "client", clientAddr)
		return nil, fmt.Errorf("authentication required")
	}

	return userCtx, nil
}

// handleAssociate handles UDP ASSOCIATE command, routing UDP through Client tunnel
func (p *SOCKS5Proxy) handleAssociate(_ context.Context, writer io.Writer, request *socks5.Request) error {
	connID := utils.GenerateConnID()

	clientAddr := "unknown"
	if request != nil {
		clientAddr = request.RemoteAddr.String()
	}
	logger.Info("SOCKS5 UDP ASSOCIATE request received", "conn_id", connID, "client", clientAddr)

	// Extract user context for routing through correct Client
	userCtx, err := p.extractUserContext(request, connID, "udp-associate", clientAddr)
	if err != nil {
		if err := socks5.SendReply(writer, statute.RepServerFailure, nil); err != nil {
			return fmt.Errorf("failed to send reply: %v", err)
		}
		return err
	}

	// Check if UDP listener is available
	if p.udpListener == nil || p.udpBindAddr == nil {
		logger.Error("UDP listener not available", "conn_id", connID)
		if err := socks5.SendReply(writer, statute.RepServerFailure, nil); err != nil {
			return fmt.Errorf("failed to send reply: %v", err)
		}
		return fmt.Errorf("UDP listener not available")
	}

	// Create UDP session
	session := &UDPSession{
		ConnID:      connID,
		UserContext: userCtx,
		CreatedAt:   time.Now(),
		LastActive:  time.Now(),
	}

	// RFC 1928 section 7: only datagrams from the IP of this TCP control
	// connection may use the association. The declared IP is not trusted;
	// the declared port is used when it is not zero.
	session.ClientAddr = &net.UDPAddr{Port: request.DestAddr.Port}
	if tcpAddr, ok := request.RemoteAddr.(*net.TCPAddr); ok {
		session.ClientAddr.IP = tcpAddr.IP
		session.ClientAddr.Zone = tcpAddr.Zone
	}
	sessionKey := ""
	tcpRefKey := ""

	// If client provides its UDP port, register the session now
	if session.ClientAddr.Port != 0 {
		sessionKey = session.ClientAddr.String()
		// F-001: Use session manager instead of sync.Map
		p.udpSessionMgr.Store(sessionKey, session)
		logger.Debug("Registered UDP session with declared port", "conn_id", connID, "session_key", sessionKey)
	} else {
		// Client will send UDP from dynamic address, we'll register when we see the first packet
		// For now, store with a temporary key based on TCP connection
		tcpRefKey = fmt.Sprintf("tcp:%s", clientAddr)
		sessionKey = tcpRefKey
		// F-001: Use StoreTCPRef for TCP-referenced sessions (atomic operation)
		p.udpSessionMgr.StoreTCPRef(tcpRefKey, sessionKey, session)
		logger.Debug("Registered UDP session with TCP reference", "conn_id", connID, "session_key", sessionKey, "will_update_on_first_packet", true)
	}

	logger.Info("UDP ASSOCIATE session created", "conn_id", connID, "bind_addr", p.udpBindAddr.String(), "session_key", sessionKey)

	// Send success reply with fixed UDP port
	if err := socks5.SendReply(writer, statute.RepSuccess, p.udpBindAddr); err != nil {
		p.udpSessionMgr.Delete(sessionKey)
		return fmt.Errorf("failed to send reply: %v", err)
	}

	// Keep TCP connection alive - when it closes, UDP session should be cleaned up
	buf := make([]byte, 1)
	for {
		_, err := request.Reader.Read(buf)
		if err != nil {
			// Clean up session
			p.cleanupUDPSession(sessionKey, session)

			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				logger.Debug("SOCKS5 UDP ASSOCIATE TCP connection closed normally", "conn_id", connID)
				return nil
			}
			logger.Debug("SOCKS5 UDP ASSOCIATE TCP connection error", "conn_id", connID, "err", err)
			return err
		}
	}
}

// cleanupUDPSession cleans up a UDP session and its tunnel connections
func (p *SOCKS5Proxy) cleanupUDPSession(sessionKey string, session *UDPSession) {
	logger.Debug("Cleaning up UDP session", "conn_id", session.ConnID, "session_key", sessionKey)

	// Close all tunnel connections
	session.tunnelConns.Range(func(_, value any) bool {
		if info, ok := value.(*tunnelConnInfo); ok && info != nil {
			// 🔧 Fix: Use closeOnce to prevent double-close panic
			info.closeOnce.Do(func() {
				if info.stopChan != nil {
					close(info.stopChan)
				}
			})
			// 🔧 Fix: Check conn is not nil before closing
			if info.conn != nil {
				_ = info.conn.Close()
			}
		}
		return true
	})

	// F-001: Use session manager instead of sync.Map
	p.udpSessionMgr.Delete(sessionKey)

	// Also try to delete with client address if different
	if session.ClientAddr != nil {
		altKey := session.ClientAddr.String()
		if altKey != sessionKey {
			p.udpSessionMgr.Delete(altKey)
		}
	}

	logger.Debug("UDP session cleaned up", "conn_id", session.ConnID)
}

// runUDPListener runs the shared UDP listener for all SOCKS5 UDP sessions
func (p *SOCKS5Proxy) runUDPListener() {
	logger.Info("Starting shared UDP listener", "listen_addr", p.udpListener.LocalAddr())

	// 🔧 PERFORMANCE: Initialize worker pool with large buffer for high throughput
	p.udpWorkChan = make(chan *udpWorkItem, 100000) // Increased for 1Gbps+ traffic
	p.udpStopChan = make(chan struct{})

	// Start worker goroutines
	for i := 0; i < udpWorkerPoolSize; i++ {
		go p.udpWorker()
	}
	logger.Info("UDP worker pool started", "workers", udpWorkerPoolSize)

	// 🔧 PERFORMANCE: Start concurrent UDP read loops to avoid single-thread read bottleneck
	for i := 0; i < udpReadWorkerPoolSize; i++ {
		go p.udpReadLoop()
	}
	logger.Info("UDP read worker pool started", "readers", udpReadWorkerPoolSize)

	// Block until stopped
	<-p.udpStopChan
}

// udpReadLoop reads UDP datagrams and dispatches them to the worker pool.
func (p *SOCKS5Proxy) udpReadLoop() {
	// P-003: Get buffer from pool for reduced GC pressure
	bufPtr := udpBufferPool.Get().(*[]byte)
	buf := *bufPtr
	defer udpBufferPool.Put(bufPtr)

	for {
		n, srcAddr, err := p.udpListener.ReadFromUDP(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				p.udpStopOnce.Do(func() {
					logger.Info("UDP listener closed")
					close(p.udpStopChan) // Stop workers & readers
				})
				return
			}
			// Ignore transient errors; keep the loop hot
			logger.Debug("UDP read error", "err", err)
			continue
		}

		// Parse SOCKS5 UDP datagram header
		pk, err := statute.ParseDatagram(buf[:n])
		if err != nil {
			logger.Debug("Failed to parse UDP datagram", "src", srcAddr, "err", err)
			continue
		}

		// Find the session for this client
		sessionKey := srcAddr.String()
		session, ok := p.udpSessionMgr.LoadOrAdoptTCPSession(sessionKey, srcAddr)
		if !ok || session == nil {
			logger.Debug("No UDP session found for client", "client", srcAddr)
			continue
		}

		// F-001: Use session manager to update last active time
		p.udpSessionMgr.UpdateLastActive(sessionKey)

		// 🔧 CRITICAL FIX: Copy pk.Data before async processing (buffer is reused)
		dataCopy := make([]byte, len(pk.Data))
		copy(dataCopy, pk.Data)
		pk.Data = dataCopy

		// Dispatch to worker pool. If the queue is full, this will block; we prefer backpressure over drops.
		select {
		case p.udpWorkChan <- &udpWorkItem{session: session, clientAddr: srcAddr, pk: pk}:
		case <-p.udpStopChan:
			return
		}
	}
}

// udpWorker processes UDP work items from the worker pool
func (p *SOCKS5Proxy) udpWorker() {
	for {
		select {
		case <-p.udpStopChan:
			return
		case item := <-p.udpWorkChan:
			if item != nil {
				p.handleUDPDatagram(item.session, item.clientAddr, item.pk)
			}
		}
	}
}

// handleUDPDatagram handles a single UDP datagram from a client
func (p *SOCKS5Proxy) handleUDPDatagram(session *UDPSession, clientAddr *net.UDPAddr, pk statute.Datagram) {
	targetAddr := pk.DstAddr.String()

	// 🔧 FIX: Use LoadOrStore pattern to prevent race condition
	// Multiple goroutines may try to create connection simultaneously for the same target
	var info *tunnelConnInfo
	if existing, ok := session.tunnelConns.Load(targetAddr); ok {
		info = existing.(*tunnelConnInfo)
		// 🔧 PERFORMANCE: Wait for connection to be ready using channel instead of sleep loop
		select {
		case <-info.readyChan:
			// Connection is ready
		case <-time.After(1 * time.Second):
			logger.Error("Timeout waiting for existing UDP tunnel connection", "target", targetAddr)
			return
		}
		if info.conn == nil {
			logger.Error("UDP tunnel connection not ready", "target", targetAddr)
			return
		}
	} else {
		// Create placeholder to prevent other goroutines from creating duplicate connections
		placeholder := &tunnelConnInfo{
			stopChan:  make(chan struct{}),
			readyChan: make(chan struct{}),
		}

		// Try to store placeholder - if another goroutine already stored, use that one
		if actual, loaded := session.tunnelConns.LoadOrStore(targetAddr, placeholder); loaded {
			// Another goroutine won the race, use their connection
			info = actual.(*tunnelConnInfo)
			// 🔧 PERFORMANCE: Wait for connection to be ready using channel
			select {
			case <-info.readyChan:
				// Connection is ready
			case <-time.After(1 * time.Second):
				logger.Error("Timeout waiting for UDP tunnel connection", "target", targetAddr)
				return
			}
			if info.conn == nil {
				logger.Error("UDP tunnel connection not ready", "target", targetAddr)
				return
			}
		} else {
			// We won the race, create the actual connection
			connID := utils.GenerateConnID()
			ctx := context.Background()
			ctx = commonctx.WithConnID(ctx, connID)
			ctx = commonctx.WithUserContext(ctx, session.UserContext)

			logger.Info("Creating UDP tunnel connection", "conn_id", connID, "session_id", session.ConnID, "target", targetAddr, "group_id", session.UserContext.GroupID)

			conn, err := p.dialFunc(ctx, protocol.ProtocolUDP, targetAddr)
			if err != nil {
				logger.Error("Failed to create UDP tunnel connection", "conn_id", connID, "target", targetAddr, "err", err)
				session.tunnelConns.Delete(targetAddr)
				close(placeholder.readyChan) // Signal failure
				return
			}

			// Update placeholder with real connection info
			placeholder.conn = conn
			placeholder.connID = connID
			info = placeholder

			// 🔧 PERFORMANCE: Signal that connection is ready
			close(info.readyChan)

			// 🔧 FIX: Register UDP connection with monitoring for web UI statistics
			monitoring.CreateConnection(connID, session.UserContext.GroupID, "socks5-udp:"+targetAddr)

			// Start goroutine to relay responses back to client
			go p.relayUDPResponses(session, clientAddr, targetAddr, pk.Header(), info)

			logger.Info("UDP tunnel connection established", "conn_id", connID, "target", targetAddr)
		}
	}

	// Debug: Verify data integrity (test harness packet format only)
	// Guarded by DebugEnabled to skip O(n) XOR computation in production
	if logger.DebugEnabled() && len(pk.Data) >= 8 {
		seqNum := uint32(pk.Data[0])<<24 | uint32(pk.Data[1])<<16 | uint32(pk.Data[2])<<8 | uint32(pk.Data[3])
		expectedChecksum := pk.Data[4]
		var actualChecksum byte
		for i := 8; i < len(pk.Data); i++ {
			actualChecksum ^= pk.Data[i]
		}
		if expectedChecksum != actualChecksum && seqNum < 10 {
			logger.Error("UDP data corruption BEFORE tunnel", "conn_id", info.connID, "seq", seqNum,
				"exp_cs", fmt.Sprintf("0x%02x", expectedChecksum), "act_cs", fmt.Sprintf("0x%02x", actualChecksum))
		}
	}

	// Send UDP data through tunnel
	// 🔧 PERFORMANCE: Use WriteNoCopy to avoid extra copy since data is already copied
	var writeErr error
	var bytesWritten int
	if ncw, ok := info.conn.(connection.NoCopyWriter); ok {
		bytesWritten, writeErr = ncw.WriteNoCopy(pk.Data)
	} else {
		bytesWritten, writeErr = info.conn.Write(pk.Data)
	}
	if writeErr != nil {
		logger.Error("Failed to write UDP data to tunnel", "conn_id", info.connID, "target", targetAddr, "err", writeErr)
		// 🔧 Fix: Use closeOnce to prevent double-close panic
		info.closeOnce.Do(func() {
			close(info.stopChan)
		})
		_ = info.conn.Close()
		// 🔧 FIX: Close connection in monitoring when UDP tunnel fails
		monitoring.CloseConnection(info.connID)
		session.tunnelConns.Delete(targetAddr)
		return
	}

	// 🔧 FIX: Update monitoring for UDP bytes sent (client -> target)
	if bytesWritten > 0 {
		monitoring.UpdateConnectionBytes(info.connID, session.UserContext.GroupID, int64(bytesWritten), 0)
	}
}

// relayUDPResponses reads UDP responses from tunnel and sends back to client
func (p *SOCKS5Proxy) relayUDPResponses(session *UDPSession, clientAddr *net.UDPAddr, targetAddr string, header []byte, info *tunnelConnInfo) {
	// P-003: Get buffer from pool for reduced GC pressure
	bufPtr := udpBufferPool.Get().(*[]byte)
	buf := *bufPtr
	defer udpBufferPool.Put(bufPtr)

	// P-003: Pre-allocate response buffer from pool for building responses
	respBufPtr := udpBufferPool.Get().(*[]byte)
	respBuf := *respBufPtr
	defer udpBufferPool.Put(respBufPtr)

	defer func() {
		_ = info.conn.Close()
		// 🔧 FIX: Close connection in monitoring when UDP relay stops
		monitoring.CloseConnection(info.connID)
		session.tunnelConns.Delete(targetAddr)
		logger.Debug("UDP response relay stopped", "conn_id", info.connID, "target", targetAddr)
	}()

	headerLen := len(header)

	for {
		select {
		case <-info.stopChan:
			return
		default:
		}

		// Set read deadline to allow checking stopChan
		_ = info.conn.SetReadDeadline(time.Now().Add(5 * time.Second))

		n, err := info.conn.Read(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				return
			}
			logger.Warn("UDP tunnel read error", "conn_id", info.connID, "err", err)
			return
		}

		// 🔧 FIX: Update monitoring for UDP bytes received (target -> client)
		if n > 0 {
			monitoring.UpdateConnectionBytes(info.connID, session.UserContext.GroupID, 0, int64(n))
		}

		// P-003: Build SOCKS5 UDP response using pre-allocated buffer
		responseLen := headerLen + n
		copy(respBuf[:headerLen], header)
		copy(respBuf[headerLen:responseLen], buf[:n])

		// Send back to client
		if _, err := p.udpListener.WriteTo(respBuf[:responseLen], clientAddr); err != nil {
			logger.Error("Failed to send UDP response to client", "conn_id", info.connID, "client", clientAddr, "err", err)
			return
		}

		session.LastActive = time.Now()
	}
}

// fullCloseListener hands go-socks5 user connections whose CloseWrite is a
// full Close. When the target side of a CONNECT ends, go-socks5 half-closes
// the user and waits for the user to close; the tunnel has no half-close, so
// close the user side as well instead of waiting on a client that may never
// close. Target data already written is still delivered before the FIN.
type fullCloseListener struct{ net.Listener }

func (l fullCloseListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		return fullCloseConn{tcpConn}, err
	}
	return conn, err
}

// fullCloseConn embeds *net.TCPConn so its ReadFrom/WriteTo fast paths stay.
type fullCloseConn struct{ *net.TCPConn }

func (c fullCloseConn) CloseWrite() error { return c.Close() }

// listenUDP is net.ListenUDP. Tests replace it to simulate a UDP port that is in use.
var listenUDP = net.ListenUDP

// socks5ListenAttempts is how many ports Start tries when the listen port is 0.
const socks5ListenAttempts = 5

// Start starts the SOCKS5 proxy server
func (p *SOCKS5Proxy) Start() error {
	logger.Info("Starting SOCKS5 proxy server", "listen_addr", p.config.ListenAddr)

	// Parse listen address to get host
	host, port, err := net.SplitHostPort(p.config.ListenAddr)
	if err != nil {
		return fmt.Errorf("invalid listen address: %v", err)
	}

	// UDP uses the same port as TCP. With port 0 the kernel picks the TCP
	// port, and that port can already be in use for UDP, so try a new port.
	var listener net.Listener
	var udpListener *net.UDPConn
	for attempt := 1; ; attempt++ {
		// Create TCP listener
		logger.Debug("Creating TCP listener for SOCKS5", "address", p.config.ListenAddr)
		listener, err = net.Listen("tcp", p.config.ListenAddr)
		if err != nil {
			logger.Error("Failed to create TCP listener for SOCKS5 proxy", "listen_addr", p.config.ListenAddr, "err", err)
			return fmt.Errorf("failed to listen on %s: %v", p.config.ListenAddr, err)
		}
		p.listener = listener

		// Get actual port from TCP listener (important when using port 0)
		tcpAddr := listener.Addr().(*net.TCPAddr)
		actualPort := tcpAddr.Port
		logger.Debug("TCP listener created successfully for SOCKS5", "listen_addr", tcpAddr.String())

		// Create UDP listener on the same port as TCP
		udpAddr := &net.UDPAddr{
			IP:   net.ParseIP(host),
			Port: actualPort,
		}
		if udpAddr.IP == nil {
			udpAddr.IP = tcpAddr.IP
		}

		udpListener, err = listenUDP("udp", udpAddr)
		if err == nil {
			break
		}
		_ = p.listener.Close()
		if port == "0" && attempt < socks5ListenAttempts {
			logger.Debug("UDP listen failed on the SOCKS5 TCP port, trying a new port", "listen_addr", udpAddr.String(), "err", err)
			continue
		}
		logger.Error("Failed to create UDP listener for SOCKS5 proxy", "listen_addr", udpAddr.String(), "err", err)
		return fmt.Errorf("failed to listen UDP on %s: %v", udpAddr.String(), err)
	}
	// 🔧 FIX: Set large UDP buffers to prevent packet loss at 1Gbps throughput
	_ = udpListener.SetReadBuffer(64 * 1024 * 1024)  // 64MB read buffer
	_ = udpListener.SetWriteBuffer(64 * 1024 * 1024) // 64MB write buffer
	p.udpListener = udpListener
	p.udpBindAddr = udpListener.LocalAddr().(*net.UDPAddr)
	logger.Debug("UDP listener created successfully for SOCKS5", "listen_addr", p.udpBindAddr)

	// Start UDP listener goroutine
	go p.runUDPListener()

	// Start SOCKS5 server in separate goroutine
	go func() {
		logger.Info("SOCKS5 server starting to serve requests (TCP + UDP through tunnel)", "listen_addr", p.config.ListenAddr)
		if err := p.server.Serve(fullCloseListener{listener}); err != nil {
			// Check if the error is due to listener being closed (normal shutdown)
			if strings.Contains(err.Error(), "use of closed network connection") {
				logger.Info("SOCKS5 server stopped normally", "listen_addr", p.config.ListenAddr)
			} else {
				logger.Error("SOCKS5 server terminated unexpectedly", "listen_addr", p.config.ListenAddr, "err", err)
			}
		} else {
			logger.Info("SOCKS5 server stopped", "listen_addr", p.config.ListenAddr)
		}
	}()

	logger.Info("SOCKS5 proxy server started successfully", "listen_addr", p.config.ListenAddr)

	return nil
}

// Stop stops the SOCKS5 proxy server
func (p *SOCKS5Proxy) Stop() error {
	logger.Info("Initiating SOCKS5 proxy server shutdown", "listen_addr", p.config.ListenAddr)

	// Close UDP listener first
	if p.udpListener != nil {
		logger.Debug("Closing SOCKS5 UDP listener", "listen_addr", p.config.ListenAddr)
		if err := p.udpListener.Close(); err != nil {
			logger.Error("Error closing SOCKS5 UDP listener", "listen_addr", p.config.ListenAddr, "err", err)
		}
	}

	// F-001: Stop the UDP session manager (handles LRU cleanup and timeout goroutine)
	if p.udpSessionMgr != nil {
		p.udpSessionMgr.Stop()
	}

	// Close TCP listener
	if p.listener != nil {
		logger.Debug("Closing SOCKS5 TCP listener", "listen_addr", p.config.ListenAddr)
		err := p.listener.Close()

		if err != nil {
			logger.Error("Error closing SOCKS5 TCP listener", "listen_addr", p.config.ListenAddr, "err", err)
			return err
		}

		logger.Info("SOCKS5 proxy server shutdown completed", "listen_addr", p.config.ListenAddr)
		return nil
	}

	logger.Debug("SOCKS5 listener was nil, nothing to close", "listen_addr", p.config.ListenAddr)
	return nil
}

// GetListenAddr returns the listen address
func (p *SOCKS5Proxy) GetListenAddr() string {
	return p.config.ListenAddr
}

// GroupBasedCredentialStore implements CredentialStore interface with support for group-based usernames
type GroupBasedCredentialStore struct {
	GroupValidator func(string, string) bool
}

// Valid implements the CredentialStore interface
// Supports usernames in format "username.group_id" by extracting the group for authentication
func (g *GroupBasedCredentialStore) Valid(user, password, userAddr string) bool {
	logger.Debug("SOCKS5 authentication attempt", "username", user, "client", userAddr)

	// Verify credentials using group validator
	isValid := g.GroupValidator != nil && g.GroupValidator(user, password)

	if isValid {
		logger.Info("SOCKS5 authentication successful", "username", user, "group_id", user, "client", userAddr)
	} else {
		logger.Warn("SOCKS5 authentication failed", "username", user, "group_id", user, "client", userAddr)
	}

	return isValid
}
