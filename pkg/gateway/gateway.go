// Package gateway provides gateway implementation for AnyProxy.
package gateway

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"sync"
	"time"

	commonctx "github.com/buhuipao/anyproxy/pkg/common/context"
	"github.com/buhuipao/anyproxy/pkg/common/credential"
	"github.com/buhuipao/anyproxy/pkg/common/message"
	"github.com/buhuipao/anyproxy/pkg/common/monitoring"
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
	Clients []string // Ordered list of client IDs for round-robin
	Counter int      // Round-robin counter
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

	// 🆕 Create transport layer - the only new logic
	transportImpl := transport.CreateTransport(transportType, &transport.AuthConfig{
		Username: cfg.Gateway.AuthUsername,
		Password: cfg.Gateway.AuthPassword,
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

	// Create TUIC proxy
	if cfg.Gateway.Proxy.TUIC.ListenAddr != "" {
		logger.Info("Configuring TUIC proxy", "listen_addr", cfg.Gateway.Proxy.TUIC.ListenAddr)
		tuicProxy, err := protocols.NewTUICProxyWithAuth(&cfg.Gateway.Proxy.TUIC, dialFn, gateway.credentialMgr.ValidateGroup, cfg.Gateway.TLSCert, cfg.Gateway.TLSKey)
		if err != nil {
			cancel()
			logger.Error("Failed to create TUIC proxy", "listen_addr", cfg.Gateway.Proxy.TUIC.ListenAddr, "err", err)
			return nil, fmt.Errorf("failed to create TUIC proxy: %v", err)
		}
		proxies = append(proxies, tuicProxy)
		logger.Info("TUIC proxy configured successfully", "listen_addr", cfg.Gateway.Proxy.TUIC.ListenAddr, "using_gateway_tls", true)
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

	// Only register group credentials if password is provided
	// For file/db credential storage, passwords are pre-configured
	if password != "" {
		if err := g.credentialMgr.RegisterGroup(groupID, password); err != nil {
			logger.Error("Failed to register group credentials", "client_id", clientID, "group_id", groupID, "err", err)
			// Send the error message to the client using proper error message type
			msgHandler := message.NewGatewayExtendedMessageHandler(conn)
			if writeErr := msgHandler.WriteErrorMessage(err.Error()); writeErr != nil {
				logger.Error("Failed to send error message to client", "client_id", clientID, "group_id", groupID, "original_error", err, "write_error", writeErr)
			} else {
				logger.Debug("Authentication error message sent to client", "client_id", clientID, "group_id", groupID, "error_message", err.Error())
			}
			_ = conn.Close()
			return
		}
		logger.Debug("Registered group credentials from client", "client_id", clientID, "group_id", groupID)
	} else {
		logger.Debug("No password provided by client, using pre-configured credentials", "client_id", clientID, "group_id", groupID)
	}

	// Initialize group info if it doesn't exist
	g.groupsMu.Lock()
	if _, exists := g.groups[groupID]; !exists {
		g.groups[groupID] = &GroupInfo{
			Clients: make([]string, 0),
			Counter: 0,
		}
		logger.Debug("Initialized group info", "group_id", groupID)
	}
	g.groupsMu.Unlock()

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

	// 🆕 Initialize message handler
	client.msgHandler = message.NewGatewayExtendedMessageHandler(conn)

	g.addClient(client)

	// 🚨 Fix: Handle messages directly, block until connection closes
	// This ensures BiStream method doesn't return prematurely
	defer func() {
		client.Stop()
		g.removeClient(client.ID)
		logger.Info("Client disconnected and cleaned up", "client_id", client.ID, "group_id", client.GroupID)
	}()

	// Handle client messages - this will block until connection closes
	client.handleMessage()
}

// addClient adds a client to the gateway
func (g *Gateway) addClient(client *ClientConn) {
	g.clientsMu.Lock()
	defer g.clientsMu.Unlock()

	// Validate group ID is non-empty
	if client.GroupID == "" {
		logger.Error("Cannot add client with empty group ID", "client_id", client.ID)
		return
	}

	// Check if client already exists
	if existingClient, exists := g.clients[client.ID]; exists {
		logger.Warn("Replacing existing client connection", "client_id", client.ID, "old_group_id", existingClient.GroupID, "new_group_id", client.GroupID)
		existingClient.Stop()
	}

	g.clients[client.ID] = client

	// Add client to group's ordered list
	g.groupsMu.Lock()
	// Group should already exist from registerGroupCredentials, but ensure it exists
	if _, ok := g.groups[client.GroupID]; !ok {
		logger.Error("Group not found when adding client - this should not happen", "client_id", client.ID, "group_id", client.GroupID)
		g.groups[client.GroupID] = &GroupInfo{
			Clients: make([]string, 0),
			Counter: 0,
		}
	}

	// Add client to group's ordered list
	g.groups[client.GroupID].Clients = append(g.groups[client.GroupID].Clients, client.ID)
	groupSize := len(g.groups[client.GroupID].Clients)
	g.groupsMu.Unlock()

	// 🆕 Update client metrics when client connects
	monitoring.UpdateClientMetrics(client.ID, client.GroupID, 0, 0, false)

	totalClients := len(g.clients)
	logger.Debug("Client added successfully", "client_id", client.ID, "group_id", client.GroupID, "group_size", groupSize, "total_clients", totalClients)
}

// removeClient removes a client from the gateway
func (g *Gateway) removeClient(clientID string) {
	g.clientsMu.Lock()
	defer g.clientsMu.Unlock()

	client, exists := g.clients[clientID]
	if !exists {
		logger.Debug("Attempted to remove non-existent client", "client_id", clientID)
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
	if groupInfo, ok := g.groups[client.GroupID]; ok {
		for i, id := range groupInfo.Clients {
			if id == clientID {
				groupInfo.Clients = append(groupInfo.Clients[:i], groupInfo.Clients[i+1:]...)
				break
			}
		}

		// Clean up empty group
		if len(groupInfo.Clients) == 0 {
			delete(g.groups, client.GroupID)
			// Only remove from credential manager if using memory storage
			// For file/db storage, credentials are persistent
			if g.config.Credential == nil || g.config.Credential.Type == "memory" || g.config.Credential.Type == "" {
				if err := g.credentialMgr.RemoveGroup(client.GroupID); err != nil {
					logger.Error("Failed to remove group from credential manager", "group_id", client.GroupID, "err", err)
				}
				logger.Debug("Removed group credentials from memory", "group_id", client.GroupID)
			} else {
				logger.Debug("Keeping persistent group credentials", "group_id", client.GroupID, "storage_type", g.config.Credential.Type)
			}
		}
	}
	g.groupsMu.Unlock()

	remainingClients := len(g.clients)
	logger.Info("Client removed successfully", "client_id", clientID, "group_id", client.GroupID, "remaining_clients", remainingClients)
}

// getClientByGroup gets client by group
func (g *Gateway) getClientByGroup(groupID string) (*ClientConn, error) {
	g.clientsMu.Lock()
	defer g.clientsMu.Unlock()

	groupInfo, exists := g.groups[groupID]
	if !exists || len(groupInfo.Clients) == 0 {
		return nil, fmt.Errorf("no clients available in group: %s", groupID)
	}

	clients := groupInfo.Clients
	counter := groupInfo.Counter

	// Try up to len(clients) times to find a healthy client
	for i := 0; i < len(clients); i++ {
		// Calculate current index
		idx := (counter + i) % len(clients)
		clientID := clients[idx]

		if client, exists := g.clients[clientID]; exists {
			// Update counter to next position
			groupInfo.Counter = (idx + 1) % len(clients)
			logger.Info("Round-robin client selection", "group_id", groupID, "selected_client", clientID, "counter_before", counter, "counter_after", groupInfo.Counter, "total_clients", len(clients), "available_clients", clients)
			return client, nil
		}
		logger.Warn("Client not found in clients map during round-robin", "group_id", groupID, "target_client", clientID, "counter", counter, "idx", idx, "total_clients", len(clients), "available_clients", clients)
	}

	return nil, fmt.Errorf("no healthy clients available in group: %s", groupID)
}
