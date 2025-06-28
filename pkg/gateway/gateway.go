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

// Gateway represents the proxy gateway server
type Gateway struct {
	config        *config.GatewayConfig
	transport     transport.Transport  // 🆕 The only new abstraction
	proxies       []utils.GatewayProxy // Gateway proxy interfaces
	clientsMu     sync.RWMutex
	clients       map[string]*ClientConn
	groups        map[string]map[string]struct{}
	groupClients  map[string][]string // Fix: Maintain ordered client list for round-robin
	groupCounters map[string]int      // Fix: Round-robin counter for each group
	// Group credential management
	groupCredentials map[string]string // group_id -> password
	groupCredMu      sync.RWMutex      // Separate lock for group credentials
	portForwardMgr   *PortForwardManager
	ctx              context.Context
	cancel           context.CancelFunc
	wg               sync.WaitGroup
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
		config:           &cfg.Gateway,
		transport:        transportImpl,
		clients:          make(map[string]*ClientConn),
		groups:           make(map[string]map[string]struct{}),
		groupClients:     make(map[string][]string), // Fix: Initialize ordered client list
		groupCounters:    make(map[string]int),      // Fix: Initialize round-robin counter
		groupCredentials: make(map[string]string),
		groupCredMu:      sync.RWMutex{},
		portForwardMgr:   NewPortForwardManager(),
		ctx:              ctx,
		cancel:           cancel,
	}

	// Initialize default group
	gateway.groups[""] = make(map[string]struct{})
	logger.Debug("Initialized default group for gateway")

	// Create custom dial function
	dialFn := func(ctx context.Context, network, addr string) (net.Conn, error) {
		// Extract user information from context
		var groupID string
		if userCtx, ok := commonctx.GetUserContext(ctx); ok {
			logger.Debug("Dial function received user context", "group_id", userCtx.GroupID, "network", network, "address", addr)
			groupID = userCtx.GroupID
		} else {
			logger.Debug("Dial function using default group", "network", network, "address", addr)
		}

		// Get client
		client, err := gateway.getClientByGroup(groupID)
		if err != nil {
			logger.Error("Failed to get client by group for dial", "group_id", groupID, "network", network, "address", addr, "err", err)
			return nil, err
		}
		logger.Debug("Successfully selected client for dial", "client_id", client.ID, "group_id", groupID, "network", network, "address", addr)
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

	// Check if client already exists
	if existingClient, exists := g.clients[client.ID]; exists {
		logger.Warn("Replacing existing client connection", "client_id", client.ID, "old_group_id", existingClient.GroupID, "new_group_id", client.GroupID)
		existingClient.Stop()
	}

	g.clients[client.ID] = client
	if _, ok := g.groups[client.GroupID]; !ok {
		g.groups[client.GroupID] = make(map[string]struct{})
		g.groupClients[client.GroupID] = make([]string, 0) // Fix: Initialize ordered list
		g.groupCounters[client.GroupID] = 0                // Fix: Initialize counter
		logger.Debug("Created new group", "group_id", client.GroupID)
	}
	g.groups[client.GroupID][client.ID] = struct{}{}

	// Fix: Add to ordered list
	g.groupClients[client.GroupID] = append(g.groupClients[client.GroupID], client.ID)

	// 🆕 Update client metrics when client connects
	monitoring.UpdateClientMetrics(client.ID, client.GroupID, 0, 0, false)

	groupSize := len(g.groups[client.GroupID])
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
	delete(g.groups[client.GroupID], clientID)

	// Fix: Remove client from ordered list
	if clients, ok := g.groupClients[client.GroupID]; ok {
		for i, id := range clients {
			if id == clientID {
				g.groupClients[client.GroupID] = append(clients[:i], clients[i+1:]...)
				break
			}
		}
	}

	if len(g.groups[client.GroupID]) == 0 && client.GroupID != "" {
		delete(g.groups, client.GroupID)
		delete(g.groupClients, client.GroupID)  // Fix: Clean up ordered list
		delete(g.groupCounters, client.GroupID) // Fix: Clean up counter
		logger.Debug("Removed empty group", "group_id", client.GroupID)
	}

	remainingClients := len(g.clients)
	logger.Info("Client removed successfully", "client_id", clientID, "group_id", client.GroupID, "remaining_clients", remainingClients)
}

// getClientByGroup gets client by group
func (g *Gateway) getClientByGroup(groupID string) (*ClientConn, error) {
	g.clientsMu.Lock()
	defer g.clientsMu.Unlock()

	clients, exists := g.groupClients[groupID]
	if !exists || len(clients) == 0 {
		return nil, fmt.Errorf("no clients available in group: %s", groupID)
	}

	// Fix: Implement true round-robin load balancing
	// Get current counter value
	counter := g.groupCounters[groupID]

	// Try up to len(clients) times to find a healthy client
	for i := 0; i < len(clients); i++ {
		// Calculate current index
		idx := (counter + i) % len(clients)
		clientID := clients[idx]

		if client, exists := g.clients[clientID]; exists {
			// Update counter to next position
			g.groupCounters[groupID] = (idx + 1) % len(clients)
			logger.Info("Round-robin client selection", "group_id", groupID, "selected_client", clientID, "counter_before", counter, "counter_after", g.groupCounters[groupID], "total_clients", len(clients), "available_clients", clients)
			return client, nil
		}
		logger.Warn("Client not found in clients map during round-robin", "group_id", groupID, "target_client", clientID, "counter", counter, "idx", idx, "total_clients", len(clients), "available_clients", clients)
	}

	return nil, fmt.Errorf("no healthy clients available in group: %s", groupID)
}

// registerGroupCredentials registers or validates group credentials from a client
func (g *Gateway) registerGroupCredentials(groupID, password string) error {
	g.groupCredMu.Lock()
	defer g.groupCredMu.Unlock()

	if existingPassword, exists := g.groupCredentials[groupID]; exists {
		if existingPassword != password {
			return fmt.Errorf("password mismatch for group %s: different clients provided different passwords", groupID)
		}
		// Password matches, no need to update
		return nil
	}

	// Register new group credentials
	g.groupCredentials[groupID] = password
	logger.Info("Registered credentials for new group", "group_id", groupID)
	return nil
}

// validateGroupCredentials validates group credentials for proxy authentication
func (g *Gateway) validateGroupCredentials(groupID, password string) bool {
	g.groupCredMu.RLock()
	defer g.groupCredMu.RUnlock()

	expectedPassword, exists := g.groupCredentials[groupID]
	if !exists {
		logger.Warn("Authentication failed: unknown group", "group_id", groupID)
		return false
	}

	isValid := expectedPassword == password
	if !isValid {
		logger.Warn("Authentication failed: invalid password", "group_id", groupID)
	}
	return isValid
}
