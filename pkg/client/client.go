// Package client provides client implementation for AnyProxy.
package client

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/buhuipao/anyproxy/pkg/common/buffer"
	"github.com/buhuipao/anyproxy/pkg/common/connection"
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

// ConnectionState represents the state of the client connection
type ConnectionState int

const (
	// ConnectionStateDisconnected indicates the client is not connected
	ConnectionStateDisconnected ConnectionState = iota
	// ConnectionStateConnecting indicates the client is attempting to connect
	ConnectionStateConnecting
	// ConnectionStateConnected indicates the client is connected
	ConnectionStateConnected
)

// ConnectionStateCallback is called when the connection state changes
type ConnectionStateCallback func(state ConnectionState, err error)

// connState holds connection-related state that changes together atomically
// Using a single struct reduces atomic operations and keeps related state consistent
type connState struct {
	conn        transport.Connection
	smartWriter *buffer.SmartWriter
}

// Client struct
type Client struct {
	ctx        context.Context
	cancel     context.CancelFunc
	config     *config.ClientConfig
	transport  transport.Transport // 🆕 Transport layer instance
	connMgr    *connection.Manager // 🆕 Use shared connection manager
	wg         sync.WaitGroup
	actualID   string
	replicaIdx int

	// 🚀 Use atomic.Pointer for lock-free access to connection state
	// This is much faster than RWMutex for high-frequency read operations (~1-2ns vs ~10-20ns)
	connStatePtr atomic.Pointer[connState]

	// Enhanced host pattern matching
	forbiddenHostPatterns []*HostPattern // Enhanced forbidden host patterns
	allowedHostPatterns   []*HostPattern // Enhanced allowed host patterns

	// 🆕 Added for web server integration
	webServer interface{}

	// 🆕 Heartbeat control
	heartbeatStop     chan struct{}
	heartbeatStopOnce sync.Once // 🛡️ Ensure heartbeatStop is closed only once

	// 🆕 Connection state callback for external monitoring
	connectionStateCallback ConnectionStateCallback
	connectionStateMu       sync.RWMutex
}

// getConnState returns the current connection state atomically (lock-free, ~1-2ns)
func (c *Client) getConnState() *connState {
	return c.connStatePtr.Load()
}

// setConnState sets the connection state atomically
func (c *Client) setConnState(state *connState) {
	c.connStatePtr.Store(state)
}

// NewClient creates a new proxy client
func NewClient(cfg *config.ClientConfig, transportType string, replicaIdx int) (*Client, error) {
	// Validate configuration
	if cfg.GroupID == "" {
		return nil, fmt.Errorf("group_id cannot be empty")
	}
	// Note: group_password is required; the gateway rejects a client without it
	// for every credential store type

	logger.Info("Creating new client", "client_id", cfg.ClientID, "replica_idx", replicaIdx, "gateway_addr", cfg.Gateway.Addr, "group_id", cfg.GroupID, "transport_type", transportType, "allow_ipv6", cfg.AllowIPv6, "allowed_hosts_count", len(cfg.AllowedHosts), "forbidden_hosts_count", len(cfg.ForbiddenHosts), "open_ports_count", len(cfg.OpenPorts), "auth_enabled", cfg.Gateway.AuthUsername != "")

	// Log security policy details
	if len(cfg.ForbiddenHosts) > 0 {
		logger.Debug("Forbidden hosts configured", "client_id", cfg.ClientID, "patterns", cfg.ForbiddenHosts)
	}
	if len(cfg.AllowedHosts) > 0 {
		logger.Debug("Allowed hosts configured", "client_id", cfg.ClientID, "patterns", cfg.AllowedHosts)
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

	// 🆕 Create transport layer
	transport := transport.CreateTransport(transportType, &transport.AuthConfig{
		Username: cfg.Gateway.AuthUsername,
		Password: cfg.Gateway.AuthPassword,
	})
	if transport == nil {
		return nil, fmt.Errorf("failed to create transport: %s", transportType)
	}

	ctx, cancel := context.WithCancel(context.Background())

	client := &Client{
		config:     cfg,
		actualID:   generateClientID(cfg.ClientID, replicaIdx), // Generate unique client ID
		transport:  transport,
		replicaIdx: replicaIdx,
		connMgr:    connection.NewManager(cfg.ClientID),
		ctx:        ctx,
		cancel:     cancel,
		// Regular expressions will be initialized in compileHostPatterns
	}

	// Compile host patterns
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
	logger.Info("Starting proxy client", "client_id", c.getClientID(), "gateway_addr", c.config.Gateway.Addr, "group_id", c.config.GroupID)

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
	logger.Debug("Canceling client context", "client_id", c.getClientID())
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

	// Step 3: Cleanup all resources
	logger.Debug("Performing cleanup", "client_id", c.getClientID())
	c.cleanup()

	// Step 4: Wait for all goroutines to finish
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

	// Step 5: Stop metrics reporter
	monitoring.StopMetricsReporter()

	// Step 6: Stop monitoring data cleanup process
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

// SetConnectionStateCallback sets the callback function for connection state changes
// The callback will be invoked when the connection state changes (connecting, connected, disconnected)
func (c *Client) SetConnectionStateCallback(callback ConnectionStateCallback) {
	c.connectionStateMu.Lock()
	defer c.connectionStateMu.Unlock()
	c.connectionStateCallback = callback
}

// notifyConnectionState notifies the callback of connection state changes
func (c *Client) notifyConnectionState(state ConnectionState, err error) {
	c.connectionStateMu.RLock()
	callback := c.connectionStateCallback
	c.connectionStateMu.RUnlock()

	if callback != nil {
		callback(state, err)
	}
}
