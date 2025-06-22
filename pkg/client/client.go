// Package client provides client implementation for AnyProxy.
package client

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/buhuipao/anyproxy/pkg/common/connection"
	"github.com/buhuipao/anyproxy/pkg/common/message"
	"github.com/buhuipao/anyproxy/pkg/common/monitoring"
	"github.com/buhuipao/anyproxy/pkg/common/protocol"
	"github.com/buhuipao/anyproxy/pkg/config"
	"github.com/buhuipao/anyproxy/pkg/logger"
	"github.com/buhuipao/anyproxy/pkg/transport"

	// Import gRPC transport for side effects (registration)
	_ "github.com/buhuipao/anyproxy/pkg/transport/grpc"
	_ "github.com/buhuipao/anyproxy/pkg/transport/quic"
	_ "github.com/buhuipao/anyproxy/pkg/transport/websocket"
)

// Client struct
type Client struct {
	ctx        context.Context
	cancel     context.CancelFunc
	config     *config.ClientConfig
	conn       transport.Connection // 🆕 Use transport layer connection
	transport  transport.Transport  // 🆕 Transport layer instance
	connMgr    *connection.Manager  // 🆕 Use shared connection manager
	wg         sync.WaitGroup
	actualID   string
	replicaIdx int

	// 🆕 Shared message handler
	msgHandler message.ExtendedMessageHandler

	// Enhanced host pattern matching
	forbiddenHostPatterns []*HostPattern // Enhanced forbidden host patterns
	allowedHostPatterns   []*HostPattern // Enhanced allowed host patterns

	// 🆕 Added for web server integration
	webServer interface{}
}

// NewClient creates a new proxy client
func NewClient(cfg *config.ClientConfig, transportType string, replicaIdx int) (*Client, error) {
	logger.Info("Creating new client", "client_id", cfg.ClientID, "replica_idx", replicaIdx, "gateway_addr", cfg.GatewayAddr, "group_id", cfg.GroupID, "transport_type", transportType, "allowed_hosts_count", len(cfg.AllowedHosts), "forbidden_hosts_count", len(cfg.ForbiddenHosts), "open_ports_count", len(cfg.OpenPorts), "auth_enabled", cfg.AuthUsername != "")

	// Log security policy details
	if len(cfg.ForbiddenHosts) > 0 {
		logger.Info("Security policy: forbidden hosts configured", "client_id", cfg.ClientID, "forbidden_hosts", cfg.ForbiddenHosts, "count", len(cfg.ForbiddenHosts))
	}

	if len(cfg.AllowedHosts) > 0 {
		logger.Info("Security policy: allowed hosts configured", "client_id", cfg.ClientID, "allowed_hosts", cfg.AllowedHosts, "count", len(cfg.AllowedHosts))
	} else {
		logger.Warn("Security policy: no allowed hosts configured, all non-forbidden hosts will be allowed", "client_id", cfg.ClientID)
	}

	// Log port forwarding configuration
	if len(cfg.OpenPorts) > 0 {
		logger.Info("Port forwarding configured", "client_id", cfg.ClientID, "port_count", len(cfg.OpenPorts))
		for i, port := range cfg.OpenPorts {
			logger.Debug("  Port forwarding entry", "index", i, "remote_port", port.RemotePort, "local_target", fmt.Sprintf("%s:%d", port.LocalHost, port.LocalPort), "protocol", port.Protocol)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())

	// 🆕 Create transport layer - the only new logic
	transportImpl := transport.CreateTransport(transportType, &transport.AuthConfig{
		Username: cfg.AuthUsername,
		Password: cfg.AuthPassword,
	})
	if transportImpl == nil {
		cancel()
		return nil, fmt.Errorf("unsupported transport type: %s", transportType)
	}

	// Generate unique client ID

	client := &Client{
		config:     cfg,
		transport:  transportImpl,
		replicaIdx: replicaIdx,                          // Fix: Set replica index
		connMgr:    connection.NewManager(cfg.ClientID), // Pass client ID
		ctx:        ctx,
		cancel:     cancel,
		// Regular expressions will be initialized in compileHostPatterns
	}

	// Fix: Pre-compile regular expressions for better performance
	if err := client.compileHostPatterns(); err != nil {
		cancel()
		return nil, fmt.Errorf("failed to compile host patterns: %v", err)
	}

	logger.Debug("Created client with compiled host patterns", "id", cfg.ClientID, "forbidden_patterns", len(client.forbiddenHostPatterns), "allowed_patterns", len(client.allowedHostPatterns))

	logger.Debug("Client initialization completed", "client_id", cfg.ClientID, "transport_type", transportType)

	return client, nil
}

// Start starts the client with automatic reconnection
func (c *Client) Start() error {
	logger.Info("Starting proxy client", "client_id", c.getClientID(), "gateway_addr", c.config.GatewayAddr, "group_id", c.config.GroupID)

	// Start performance metrics reporter (report every 30 seconds)
	monitoring.StartMetricsReporter(30 * time.Second)

	// 🆕 Start monitoring data cleanup process
	monitoring.StartCleanupProcess()

	// Start main connection loop
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.connectionLoop()
	}()

	logger.Info("Client started successfully", "client_id", c.getClientID())

	return nil
}

// Stop stops the client gracefully
func (c *Client) Stop() error {
	logger.Info("Initiating graceful client stop", "client_id", c.getClientID())

	// Step 1: Cancel context
	logger.Debug("Cancelling client context", "client_id", c.getClientID())
	c.cancel()

	// Step 2: Get connection count
	connectionCount := c.connMgr.GetConnectionCount()

	if connectionCount > 0 {
		logger.Info("Waiting for active connections to finish", "client_id", c.getClientID(), "connection_count", connectionCount)
	}

	// Wait for existing connections to finish
	select {
	case <-c.ctx.Done():
	case <-time.After(500 * time.Millisecond):
	}

	// Step 3: 🆕 Stop transport layer connection
	if c.conn != nil {
		logger.Debug("Stopping transport connection during cleanup", "client_id", c.getClientID())
		if err := c.conn.Close(); err != nil {
			logger.Debug("Error closing client connection during stop (expected)", "err", err)
		}
		logger.Debug("Transport connection stopped", "client_id", c.getClientID())
	}

	// Step 4: Close all connections
	logger.Debug("Closing all connections", "client_id", c.getClientID(), "connection_count", connectionCount)
	c.connMgr.CloseAllConnections()
	c.connMgr.CloseAllMessageChannels()
	if connectionCount > 0 {
		logger.Debug("All connections closed", "client_id", c.getClientID())
	}

	// Step 5: Wait for all goroutines to finish
	logger.Debug("Waiting for all goroutines to finish", "client_id", c.getClientID())
	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		logger.Debug("All client goroutines finished gracefully", "client_id", c.getClientID())
	case <-time.After(protocol.DefaultShutdownTimeout):
		logger.Warn("Timeout waiting for client goroutines to finish", "client_id", c.getClientID())
	}

	// Stop metrics reporter
	monitoring.StopMetricsReporter()

	// 🆕 Stop monitoring data cleanup process
	monitoring.StopCleanupProcess()

	logger.Info("Client shutdown completed", "client_id", c.getClientID(), "connections_closed", connectionCount)

	return nil
}

// UpdateClientMetrics updates client-specific metrics
func (c *Client) UpdateClientMetrics(bytesSent, bytesReceived int64, isError bool) {
	// 🆕 Use the actual client ID instead of configured prefix
	actualClientID := c.actualID
	if actualClientID == "" {
		actualClientID = c.config.ClientID // Fallback to config
	}

	monitoring.UpdateClientMetrics(actualClientID, c.config.GroupID, bytesSent, bytesReceived, isError)

	// 🆕 Update web server with actual client ID if web is enabled
	if c.webServer != nil {
		// Use reflection to call SetActualClientID method
		if webServerValue := reflect.ValueOf(c.webServer); webServerValue.IsValid() {
			if method := webServerValue.MethodByName("SetActualClientID"); method.IsValid() {
				method.Call([]reflect.Value{reflect.ValueOf(actualClientID)})
			}
		}
	}
}

// SetWebServer sets the web server reference for client ID updates
func (c *Client) SetWebServer(webServer interface{}) {
	c.webServer = webServer
}
