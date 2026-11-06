// Package gateway provides gateway implementation for AnyProxy.
package gateway

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"

	"github.com/buhuipao/anyproxy/pkg/common/buffer"
	commonctx "github.com/buhuipao/anyproxy/pkg/common/context"
	"github.com/buhuipao/anyproxy/pkg/common/credential"
	"github.com/buhuipao/anyproxy/pkg/common/monitoring"
	"github.com/buhuipao/anyproxy/pkg/common/protocol"
	"github.com/buhuipao/anyproxy/pkg/common/utils"
	"github.com/buhuipao/anyproxy/pkg/config"
	"github.com/buhuipao/anyproxy/pkg/logger"
	"github.com/buhuipao/anyproxy/pkg/protocols"
	"github.com/buhuipao/anyproxy/pkg/transport"

	// Import gRPC transport for side effects (registration)
	_ "github.com/buhuipao/anyproxy/pkg/transport/grpc"
	_ "github.com/buhuipao/anyproxy/pkg/transport/quic"
	_ "github.com/buhuipao/anyproxy/pkg/transport/websocket"
)

// GroupInfo holds information about a group
type GroupInfo struct {
	Clients []string     // Ordered list of client IDs for round-robin
	Counter atomic.Int64 // P-001: Atomic counter for lock-free round-robin
}

// Gateway represents the proxy gateway server
type Gateway struct {
	config         *config.GatewayConfig
	transport      transport.Transport  // 🆕 The only new abstraction
	proxies        []utils.GatewayProxy // Gateway proxy interfaces
	clientsMu      sync.RWMutex         // Mutex for clients map
	groupsMu       sync.RWMutex         // Mutex for groups map
	clients        map[string]*ClientConn
	groups         map[string]*GroupInfo // Consolidated group information
	credentialMgr  *credential.Manager   // Credential manager
	portForwardMgr *PortForwardManager
	ctx            context.Context
	cancel         context.CancelFunc
	wg             sync.WaitGroup
}

// NewGateway creates a new proxy gateway
func NewGateway(cfg *config.Config, transportType string) (*Gateway, error) {
	// Use transport type from config if available, otherwise use parameter
	if cfg.Gateway.TransportType != "" {
		transportType = cfg.Gateway.TransportType
	}

	// Default to websocket if no transport type specified
	if transportType == "" {
		transportType = "websocket"
		logger.Debug("Using default transport type", "transport_type", transportType)
	}

	logger.Info("Creating new gateway", "listen_addr", cfg.Gateway.ListenAddr, "http_proxy_enabled", cfg.Gateway.Proxy.HTTP.ListenAddr != "", "socks5_proxy_enabled", cfg.Gateway.Proxy.SOCKS5.ListenAddr != "", "transport_type", transportType, "auth_enabled", cfg.Gateway.AuthUsername != "")

	ctx, cancel := context.WithCancel(context.Background())

	// Create credential manager
	var credentialMgr *credential.Manager
	var err error

	// Create credential manager based on configuration
	var credConfig *credential.Config

	if cfg.Gateway.Credential != nil {
		credConfig = &credential.Config{
			Type: credential.Type(cfg.Gateway.Credential.Type),
		}

		// Configure based on credential type
		switch cfg.Gateway.Credential.Type {
		case "file":
			credConfig.FilePath = cfg.Gateway.Credential.FilePath
		case "db":
			if cfg.Gateway.Credential.DB != nil {
				credConfig.DB = &credential.DBConfig{
					Driver:     cfg.Gateway.Credential.DB.Driver,
					DataSource: cfg.Gateway.Credential.DB.DataSource,
					TableName:  cfg.Gateway.Credential.DB.TableName,
				}
			}
		case "memory":
			// No additional configuration needed for memory storage
		default:
			// Use memory as default for unknown types
			logger.Warn("Unknown credential type, defaulting to memory", "type", cfg.Gateway.Credential.Type)
			credConfig.Type = credential.Memory
		}
	} else {
		// Default to memory storage when no credential config is provided
		credConfig = &credential.Config{Type: credential.Memory}
	}

	credentialMgr, err = credential.NewManager(credConfig)

	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create credential manager: %v", err)
	}

	// 🆕 Create transport layer with dynamic client auth support
	transportImpl := transport.CreateTransport(transportType, &transport.AuthConfig{
		Username:        cfg.Gateway.AuthUsername,
		Password:        cfg.Gateway.AuthPassword,
		ClientValidator: credentialMgr.ValidateClient, // Enable dynamic client authentication
	})
	if transportImpl == nil {
		cancel()
		return nil, fmt.Errorf("failed to create transport: %s", transportType)
	}

	gateway := &Gateway{
		config:         &cfg.Gateway,
		transport:      transportImpl,
		clients:        make(map[string]*ClientConn),
		groups:         make(map[string]*GroupInfo),
		credentialMgr:  credentialMgr,
		portForwardMgr: NewPortForwardManager(),
		ctx:            ctx,
		cancel:         cancel,
	}

	// Create custom dial function
	dialFn := func(ctx context.Context, network, addr string) (net.Conn, error) {
		// Extract user information from context
		userCtx, ok := commonctx.GetUserContext(ctx)
		if !ok || userCtx.GroupID == "" {
			logger.Error("Dial function requires valid group context", "network", network, "address", addr, "has_context", ok)
			return nil, fmt.Errorf("missing or invalid group context")
		}

		logger.Debug("Dial function received user context", "group_id", userCtx.GroupID, "network", network, "address", addr)

		// Get client
		client, err := gateway.getClientByGroup(userCtx.GroupID)
		if err != nil {
			logger.Error("Failed to get client by group for dial", "group_id", userCtx.GroupID, "network", network, "address", addr, "err", err)
			return nil, err
		}
		logger.Debug("Successfully selected client for dial", "client_id", client.ID, "group_id", userCtx.GroupID, "network", network, "address", addr)
		return client.dialNetwork(ctx, network, addr)
	}

	// Initialize proxy protocols
	var proxies []utils.GatewayProxy

	// Create HTTP proxy
	if cfg.Gateway.Proxy.HTTP.ListenAddr != "" {
		logger.Info("Configuring HTTP proxy", "listen_addr", cfg.Gateway.Proxy.HTTP.ListenAddr)
		httpProxy, err := protocols.NewHTTPProxyWithAuth(&cfg.Gateway.Proxy.HTTP, dialFn, gateway.credentialMgr.ValidateGroup)
		if err != nil {
			cancel()
			logger.Error("Failed to create HTTP proxy", "listen_addr", cfg.Gateway.Proxy.HTTP.ListenAddr, "err", err)
			return nil, fmt.Errorf("failed to create HTTP proxy: %v", err)
		}
		proxies = append(proxies, httpProxy)
		logger.Info("HTTP proxy configured successfully", "listen_addr", cfg.Gateway.Proxy.HTTP.ListenAddr)
	}

	// Create SOCKS5 proxy
	if cfg.Gateway.Proxy.SOCKS5.ListenAddr != "" {
		logger.Info("Configuring SOCKS5 proxy", "listen_addr", cfg.Gateway.Proxy.SOCKS5.ListenAddr)
		socks5Proxy, err := protocols.NewSOCKS5ProxyWithAuth(&cfg.Gateway.Proxy.SOCKS5, dialFn, gateway.credentialMgr.ValidateGroup)
		if err != nil {
			cancel()
			logger.Error("Failed to create SOCKS5 proxy", "listen_addr", cfg.Gateway.Proxy.SOCKS5.ListenAddr, "err", err)
			return nil, fmt.Errorf("failed to create SOCKS5 proxy: %v", err)
		}
		proxies = append(proxies, socks5Proxy)
		logger.Info("SOCKS5 proxy configured successfully", "listen_addr", cfg.Gateway.Proxy.SOCKS5.ListenAddr)
	}

	// Create TUIC v5 proxy (standard-compliant, compatible with Clash Meta/sing-box)
	if cfg.Gateway.Proxy.TUIC.ListenAddr != "" {
		logger.Info("Configuring TUIC v5 proxy", "listen_addr", cfg.Gateway.Proxy.TUIC.ListenAddr)
		tuicProxy, err := protocols.NewTUICv5ProxyWithAuth(&cfg.Gateway.Proxy.TUIC, dialFn, gateway.credentialMgr.ValidateGroup, cfg.Gateway.TLSCert, cfg.Gateway.TLSKey)
		if err != nil {
			cancel()
			logger.Error("Failed to create TUIC v5 proxy", "listen_addr", cfg.Gateway.Proxy.TUIC.ListenAddr, "err", err)
			return nil, fmt.Errorf("failed to create TUIC v5 proxy: %v", err)
		}

		// Register TUIC proxy as credential observer for dynamic user sync
		if tuicObserver, ok := tuicProxy.(protocols.TUICCredentialObserver); ok {
			credentialMgr.RegisterObserver(tuicObserver)
			logger.Debug("TUIC proxy registered as credential observer")
		}

		proxies = append(proxies, tuicProxy)
		logger.Info("TUIC v5 proxy configured successfully", "listen_addr", cfg.Gateway.Proxy.TUIC.ListenAddr, "using_gateway_tls", true)
	}

	// Ensure at least one proxy is configured
	if len(proxies) == 0 {
		cancel()
		logger.Error("No proxy configured - at least one proxy type must be enabled", "http_addr", cfg.Gateway.Proxy.HTTP.ListenAddr, "socks5_addr", cfg.Gateway.Proxy.SOCKS5.ListenAddr, "tuic_addr", cfg.Gateway.Proxy.TUIC.ListenAddr)
		return nil, fmt.Errorf("no proxy configured: please configure at least one of HTTP, SOCKS5, or TUIC proxy")
	}

	gateway.proxies = proxies
	logger.Info("Gateway created successfully", "proxy_count", len(proxies), "listen_addr", cfg.Gateway.ListenAddr)

	return gateway, nil
}

// GetCredentialManager returns the credential manager instance
func (g *Gateway) GetCredentialManager() *credential.Manager {
	return g.credentialMgr
}

// Start starts the gateway
func (g *Gateway) Start() error {
	logger.Info("Starting gateway server", "listen_addr", g.config.ListenAddr, "proxy_count", len(g.proxies))

	// 🆕 Start monitoring data cleanup process
	monitoring.StartCleanupProcess()

	// 🆕 Check and configure TLS
	var tlsConfig *tls.Config
	if g.config.TLSCert != "" && g.config.TLSKey != "" {
		logger.Debug("Loading TLS certificates", "cert_file", g.config.TLSCert, "key_file", g.config.TLSKey)

		// Load TLS certificate and key
		cert, err := tls.LoadX509KeyPair(g.config.TLSCert, g.config.TLSKey)
		if err != nil {
			logger.Error("Failed to load TLS certificate", "cert_file", g.config.TLSCert, "key_file", g.config.TLSKey, "err", err)
			return fmt.Errorf("failed to load TLS certificate: %v", err)
		}
		logger.Debug("TLS certificates loaded successfully")

		// Configure TLS
		tlsConfig = &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}
		logger.Debug("TLS configuration created", "min_version", "TLS 1.2")
	}

	// 🆕 Start transport layer server - support TLS
	logger.Info("Starting transport server for client connections")
	if tlsConfig != nil {
		logger.Info("Starting secure transport server (HTTPS/WSS)")
		if err := g.transport.ListenAndServeWithTLS(g.config.ListenAddr, g.handleConnection, tlsConfig); err != nil {
			logger.Error("Failed to start secure transport server", "listen_addr", g.config.ListenAddr, "err", err)
			return err
		}
		logger.Info("Secure transport server started successfully", "listen_addr", g.config.ListenAddr)
	} else {
		logger.Info("Starting transport server (HTTP/WS)")
		if err := g.transport.ListenAndServe(g.config.ListenAddr, g.handleConnection); err != nil {
			logger.Error("Failed to start transport server", "listen_addr", g.config.ListenAddr, "err", err)
			return err
		}
		logger.Info("Transport server started successfully", "listen_addr", g.config.ListenAddr)
	}

	// Start all proxy servers
	logger.Info("Starting proxy servers", "count", len(g.proxies))
	for i, proxy := range g.proxies {
		logger.Debug("Starting proxy server", "index", i, "type", fmt.Sprintf("%T", proxy))
		if err := proxy.Start(); err != nil {
			logger.Error("Failed to start proxy server", "index", i, "type", fmt.Sprintf("%T", proxy), "err", err)
			// Stop already started proxies
			logger.Warn("Stopping previously started proxies due to failure", "stopping_count", i)
			for j := 0; j < i; j++ {
				if stopErr := g.proxies[j].Stop(); stopErr != nil {
					logger.Error("Failed to stop proxy during cleanup", "index", j, "err", stopErr)
				}
			}
			return fmt.Errorf("failed to start proxy %d: %v", i, err)
		}

		// Initialize TUIC proxy with existing credentials after it starts
		if tuicInit, ok := proxy.(protocols.TUICUserInitializer); ok {
			existingUsers := g.credentialMgr.GetAllGroups()
			if len(existingUsers) > 0 {
				tuicInit.InitializeUsers(existingUsers)
				logger.Debug("Initialized TUIC with existing credentials", "count", len(existingUsers))
			}
		}

		logger.Debug("Proxy server started successfully", "index", i, "type", fmt.Sprintf("%T", proxy))
	}

	logger.Info("Gateway started successfully", "transport_addr", g.config.ListenAddr, "proxy_count", len(g.proxies))

	return nil
}

// Stop stops the gateway gracefully
func (g *Gateway) Stop() error {
	logger.Info("Initiating graceful gateway shutdown...")

	// Step 1: Cancel context
	logger.Debug("Signaling all goroutines to stop")
	g.cancel()

	// Step 2: 🆕 Stop transport layer server
	logger.Info("Shutting down transport server")
	if err := g.transport.Close(); err != nil {
		logger.Error("Error shutting down transport server", "err", err)
	} else {
		logger.Info("Transport server shutdown completed")
	}

	// Step 3: Stop all proxy servers
	logger.Info("Stopping proxy servers", "count", len(g.proxies))
	for i, proxy := range g.proxies {
		logger.Debug("Stopping proxy server", "index", i, "type", fmt.Sprintf("%T", proxy))
		if err := proxy.Stop(); err != nil {
			logger.Error("Error stopping proxy server", "index", i, "type", fmt.Sprintf("%T", proxy), "err", err)
		} else {
			logger.Debug("Proxy server stopped successfully", "index", i)
		}
	}
	logger.Info("All proxy servers stopped")

	// Step 4: Stop port forwarding manager
	logger.Debug("Stopping port forwarding manager")
	g.portForwardMgr.Stop()
	logger.Debug("Port forwarding manager stopped")

	// Step 5: Wait for client processing to complete
	logger.Info("Waiting for clients to finish processing...")
	select {
	case <-g.ctx.Done():
	case <-time.After(500 * time.Millisecond):
	}

	// Step 6: Stop all client connections
	g.clientsMu.RLock()
	clientCount := len(g.clients)
	g.clientsMu.RUnlock()

	if clientCount > 0 {
		logger.Info("Stopping client connections", "client_count", clientCount)
		g.clientsMu.RLock()
		for clientID, client := range g.clients {
			logger.Debug("Stopping client connection", "client_id", clientID)
			client.Stop()
		}
		g.clientsMu.RUnlock()
		logger.Info("All client connections stopped")
	} else {
		logger.Debug("No active client connections to stop")
	}

	// Step 7: Wait for all goroutines to finish
	logger.Debug("Waiting for all goroutines to finish...")
	done := make(chan struct{})
	go func() {
		g.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		logger.Info("All gateway goroutines finished gracefully")
	case <-time.After(8 * time.Second):
		logger.Warn("Timeout waiting for gateway goroutines to finish")
	}

	// 🆕 Stop monitoring data cleanup process
	monitoring.StopCleanupProcess()

	logger.Info("Gateway shutdown completed", "final_client_count", clientCount)

	return nil
}

// handleConnection handles transport layer connection adapted to transport layer abstraction
func (g *Gateway) handleConnection(conn transport.Connection) {
	// Extract client information from connection (now formal part of interface)
	clientID := conn.GetClientID()
	groupID := conn.GetGroupID()
	password := conn.GetPassword()

	logger.Info("Client connected", "client_id", clientID, "group_id", groupID, "remote_addr", conn.RemoteAddr())

	// Create client connection context
	ctx, cancel := context.WithCancel(g.ctx)

	// Create client connection
	client := &ClientConn{
		ID:             clientID,
		GroupID:        groupID,
		Conn:           conn, // 🆕 Use transport layer connection
		Conns:          make(map[string]*Conn),
		msgChans:       make(map[string]chan map[string]interface{}),
		ctx:            ctx,
		cancel:         cancel,
		portForwardMgr: g.portForwardMgr,
	}

	// 🆕 Initialize SmartWriter for unified zero-copy writes
	client.smartWriter = buffer.NewSmartWriter(conn, clientID)

	// Initialize bandwidth limiter if group has a limit configured
	if bwLimit := g.credentialMgr.GetBandwidthLimit(groupID); bwLimit > 0 {
		// bwLimit is bytes/sec; burst allows short spikes up to 2x the rate for 1 second
		client.bandwidthLimiter = rate.NewLimiter(rate.Limit(bwLimit), int(bwLimit*2))
		logger.Info("Bandwidth limit applied", "client_id", clientID, "group_id", groupID, "bytes_per_sec", bwLimit)
	}

	if err := g.addClient(client, password); err != nil {
		logger.Error("Failed to register group credentials", "client_id", clientID, "group_id", groupID, "err", err)
		// Send the error message to the client using proper error message type
		if writeErr := conn.WriteMessage(protocol.PackErrorMessage(err.Error())); writeErr != nil {
			logger.Error("Failed to send error message to client", "client_id", clientID, "group_id", groupID, "original_error", err, "write_error", writeErr)
		} else {
			logger.Debug("Authentication error message sent to client", "client_id", clientID, "group_id", groupID, "error_message", err.Error())
		}
		client.Stop()
		return
	}

	// 🎮 GAMING: if the transport supports QUIC Datagrams, start a receiver
	// goroutine. Required to close the client→gateway latency gap for UDP
	// (Sunshine→Moonlight video path). Without this the reliable stream
	// was the only path, causing 1600ms+ latency at 4K120.
	if dr, ok := conn.(transport.DatagramReader); ok {
		client.wg.Add(1)
		go func() {
			defer client.wg.Done()
			client.handleDatagrams(dr)
		}()
	}

	// 🚨 Fix: Handle messages directly, block until connection closes
	// This ensures BiStream method doesn't return prematurely
	defer func() {
		client.Stop()
		g.removeClient(client)
		logger.Info("Client disconnected and cleaned up", "client_id", client.ID, "group_id", client.GroupID)
	}()

	// Handle client messages - this will block until connection closes
	client.handleMessage()
}

// addClient registers the client's group password and adds the client to the
// gateway. A client that reuses the ID of a connected client replaces it, and
// the old connection is stopped.
func (g *Gateway) addClient(client *ClientConn, password string) error {
	// Validate group ID is non-empty
	if client.GroupID == "" {
		logger.Error("Cannot add client with empty group ID", "client_id", client.ID)
		return fmt.Errorf("group ID cannot be empty")
	}

	// Check the group password (registering it for a new group). Memory credentials
	// can be removed by leaveGroupLocked, so check them under clientsMu right before
	// the join below. File and db credentials are never removed there, so check them
	// before taking the lock to keep store I/O off the dial path.
	if g.credentialMgr.IsMemoryStore() {
		g.clientsMu.Lock()
		if err := g.credentialMgr.CheckGroupPassword(client.GroupID, password); err != nil {
			g.clientsMu.Unlock()
			return err
		}
	} else {
		if err := g.credentialMgr.CheckGroupPassword(client.GroupID, password); err != nil {
			return err
		}
		g.clientsMu.Lock()
	}
	old := g.clients[client.ID]
	g.clients[client.ID] = client

	// Add client to group's ordered list
	g.groupsMu.Lock()
	if _, ok := g.groups[client.GroupID]; !ok {
		g.groups[client.GroupID] = &GroupInfo{
			Clients: make([]string, 0),
			// Counter is zero-valued by default (atomic.Int64)
		}
	}

	// A replaced connection in the same group already holds this ID in the list
	if old == nil || old.GroupID != client.GroupID {
		if old != nil {
			g.leaveGroupLocked(old.ID, old.GroupID)
		}
		g.groups[client.GroupID].Clients = append(g.groups[client.GroupID].Clients, client.ID)
	}
	groupSize := len(g.groups[client.GroupID].Clients)
	g.groupsMu.Unlock()

	totalClients := len(g.clients)
	g.clientsMu.Unlock()

	// Stop the replaced connection without holding the locks; stopping can take seconds
	if old != nil {
		logger.Warn("Replacing existing client connection", "client_id", client.ID, "old_group_id", old.GroupID, "new_group_id", client.GroupID)
		// The ports belong to the old connection; the new one opens them again.
		// Stop it first, so a late port request on it cannot open them after the close.
		old.Stop()
		g.portForwardMgr.CloseClientPorts(client.ID)
	}

	// 🆕 Update client metrics when client connects
	monitoring.UpdateClientMetrics(client.ID, client.GroupID, 0, 0, false)

	logger.Debug("Client added successfully", "client_id", client.ID, "group_id", client.GroupID, "group_size", groupSize, "total_clients", totalClients)
	return nil
}

// removeClient removes the given connection from the gateway. It does nothing
// when the connection was already replaced by a newer one with the same ID.
func (g *Gateway) removeClient(client *ClientConn) {
	g.clientsMu.Lock()
	defer g.clientsMu.Unlock()

	clientID := client.ID
	if g.clients[clientID] != client {
		logger.Debug("Client already removed or replaced", "client_id", clientID)
		return
	}

	// Clean up port forwarding for the client
	logger.Debug("Closing port forwarding for client", "client_id", clientID)
	g.portForwardMgr.CloseClientPorts(clientID)

	// 🆕 Mark client as offline immediately in monitoring metrics
	monitoring.MarkClientOffline(clientID)

	delete(g.clients, clientID)

	// Remove client from group's ordered list
	g.groupsMu.Lock()
	g.leaveGroupLocked(clientID, client.GroupID)
	g.groupsMu.Unlock()

	remainingClients := len(g.clients)
	logger.Info("Client removed successfully", "client_id", clientID, "group_id", client.GroupID, "remaining_clients", remainingClients)
}

// leaveGroupLocked removes clientID from the group's list and deletes the group
// when it becomes empty. The caller must hold groupsMu.
func (g *Gateway) leaveGroupLocked(clientID, groupID string) {
	groupInfo, ok := g.groups[groupID]
	if !ok {
		return
	}
	for i, id := range groupInfo.Clients {
		if id == clientID {
			groupInfo.Clients = append(groupInfo.Clients[:i], groupInfo.Clients[i+1:]...)
			break
		}
	}

	// Clean up empty group. Memory credentials were registered by the clients,
	// so remove them too (this notifies TUIC to remove the user); a reconnecting
	// client registers them again. File and db credentials stay, whether they were
	// pre-configured or registered by a client.
	if len(groupInfo.Clients) == 0 {
		delete(g.groups, groupID)
		if g.credentialMgr.IsMemoryStore() {
			if err := g.credentialMgr.RemoveGroup(groupID); err != nil {
				logger.Warn("Failed to remove group credentials", "group_id", groupID, "err", err)
			} else {
				logger.Debug("Group became empty, removed credentials", "group_id", groupID)
			}
		}
	}
}

// getClientByGroup gets client by group
// F-004 & P-001: Use consistent lock order (clientsMu -> groupsMu) and atomic counter
func (g *Gateway) getClientByGroup(groupID string) (*ClientConn, error) {
	// F-004: Use consistent lock order: clientsMu -> groupsMu (same as addClient/removeClient)
	// P-001: Use RLock for read-only operations
	g.clientsMu.RLock()
	defer g.clientsMu.RUnlock()
	g.groupsMu.RLock()
	defer g.groupsMu.RUnlock()

	groupInfo, exists := g.groups[groupID]
	if !exists || len(groupInfo.Clients) == 0 {
		return nil, fmt.Errorf("no clients available in group: %s", groupID)
	}

	clients := groupInfo.Clients
	numClients := len(clients)

	// P-001: Use atomic counter for lock-free round-robin
	// Increment counter atomically and get the previous value
	counter := groupInfo.Counter.Add(1) - 1

	// Try up to numClients times to find a healthy client
	for i := int64(0); i < int64(numClients); i++ {
		// Calculate current index using atomic counter
		idx := int((counter + i) % int64(numClients))
		clientID := clients[idx]

		if client, exists := g.clients[clientID]; exists {
			logger.Debug("Round-robin client selection", "group_id", groupID, "selected_client", clientID, "counter", counter, "total_clients", numClients)
			return client, nil
		}
		logger.Warn("Client not found in clients map during round-robin", "group_id", groupID, "target_client", clientID, "counter", counter, "idx", idx, "total_clients", numClients)
	}

	return nil, fmt.Errorf("no healthy clients available in group: %s", groupID)
}
