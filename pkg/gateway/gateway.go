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

// GroupInfo represents consolidated group information
type GroupInfo struct {
	Clients  []string // Ordered list of client IDs for round-robin
	Counter  int      // Round-robin counter
	Password string   // Group password for authentication
}

// Gateway represents the proxy gateway server
type Gateway struct {
	config         *config.GatewayConfig
	transport      transport.Transport  // 🆕 The only new abstraction
	proxies        []utils.GatewayProxy // Gateway proxy interfaces
	clientsMu      sync.RWMutex
	clients        map[string]*ClientConn
	groups         map[string]*GroupInfo // Consolidated group information
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
		httpProxy, err := protocols.NewHTTPProxyWithAuth(&cfg.Gateway.Proxy.HTTP, dialFn, gateway.validateGroupCredentials)
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
		socks5Proxy, err := protocols.NewSOCKS5ProxyWithAuth(&cfg.Gateway.Proxy.SOCKS5, dialFn, gateway.validateGroupCredentials)
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
		tuicProxy, err := protocols.NewTUICProxyWithAuth(&cfg.Gateway.Proxy.TUIC, dialFn, gateway.validateGroupCredentials, cfg.Gateway.TLSCert, cfg.Gateway.TLSKey)
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

	// Register group credentials
	if err := g.registerGroupCredentials(groupID, password); err != nil {
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

	// Group should already exist from registerGroupCredentials, but ensure it exists
	if _, ok := g.groups[client.GroupID]; !ok {
		logger.Error("Group not found when adding client - this should not happen", "client_id", client.ID, "group_id", client.GroupID)
		g.groups[client.GroupID] = &GroupInfo{
			Clients:  make([]string, 0),
			Counter:  0,
			Password: "", // This is problematic - password should be set
		}
	}

	// Add client to group's ordered list
	g.groups[client.GroupID].Clients = append(g.groups[client.GroupID].Clients, client.ID)

	// 🆕 Update client metrics when client connects
	monitoring.UpdateClientMetrics(client.ID, client.GroupID, 0, 0, false)

	groupSize := len(g.groups[client.GroupID].Clients)
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
	if groupInfo, ok := g.groups[client.GroupID]; ok {
		for i, id := range groupInfo.Clients {
			if id == clientID {
				groupInfo.Clients = append(groupInfo.Clients[:i], groupInfo.Clients[i+1:]...)
				break
			}
		}
	}

	// Clean up empty group
	if groupInfo, ok := g.groups[client.GroupID]; ok && len(groupInfo.Clients) == 0 {
		delete(g.groups, client.GroupID)
		logger.Debug("Removed empty group", "group_id", client.GroupID)
	}

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

// registerGroupCredentials registers or validates group credentials from a client
func (g *Gateway) registerGroupCredentials(groupID, password string) error {
	g.clientsMu.Lock()
	defer g.clientsMu.Unlock()

	// Validate group ID is non-empty
	if groupID == "" {
		return fmt.Errorf("group ID cannot be empty")
	}

	// Validate password is non-empty
	if password == "" {
		return fmt.Errorf("group password cannot be empty")
	}

	// Create group if it doesn't exist
	if _, exists := g.groups[groupID]; !exists {
		g.groups[groupID] = &GroupInfo{
			Clients:  make([]string, 0),
			Counter:  0,
			Password: password,
		}
		logger.Info("Registered credentials for new group", "group_id", groupID)
		return nil
	}

	// 🚨 CRITICAL FIX: Check if group has active clients
	groupInfo := g.groups[groupID]

	// Count actual active clients to handle race conditions
	actualActiveClients := 0
	for _, clientID := range groupInfo.Clients {
		if _, exists := g.clients[clientID]; exists {
			actualActiveClients++
		}
	}

	// If group has no active clients, allow password change (treat as new group)
	if actualActiveClients == 0 {
		logger.Info("Group has no active clients, allowing password change", "group_id", groupID, "listed_clients", len(groupInfo.Clients), "active_clients", actualActiveClients)
		groupInfo.Password = password
		groupInfo.Clients = make([]string, 0) // Reset client list
		groupInfo.Counter = 0                 // Reset counter
		logger.Info("Group credentials updated for reconnection", "group_id", groupID)
		return nil
	}

	// Validate existing group password only if there are active clients
	existingPassword := groupInfo.Password
	logger.Debug("Validating group credentials", "group_id", groupID, "existing_password_set", existingPassword != "", "passwords_match", existingPassword == password, "active_clients", actualActiveClients)

	if existingPassword != "" && existingPassword != password {
		logger.Error("Password mismatch detected", "group_id", groupID, "existing_password_length", len(existingPassword), "provided_password_length", len(password), "active_clients", actualActiveClients)
		return fmt.Errorf("password mismatch for group %s: different clients provided different passwords", groupID)
	}

	// Set password if not already set (this should not happen in normal flow)
	if existingPassword == "" {
		groupInfo.Password = password
		logger.Warn("Setting password for existing group with empty password - this should not happen", "group_id", groupID)
	} else {
		logger.Debug("Password validation successful", "group_id", groupID, "active_clients", actualActiveClients)
	}

	return nil
}

// validateGroupCredentials validates group credentials for proxy authentication
func (g *Gateway) validateGroupCredentials(groupID, password string) bool {
	g.clientsMu.RLock()
	defer g.clientsMu.RUnlock()

	groupInfo, exists := g.groups[groupID]
	if !exists {
		logger.Warn("Authentication failed: unknown group", "group_id", groupID)
		return false
	}

	isValid := groupInfo.Password == password
	if !isValid {
		logger.Warn("Authentication failed: invalid password", "group_id", groupID)
	}
	return isValid
}
