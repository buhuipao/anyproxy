package client

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/buhuipao/anyproxy/pkg/common/message"
	"github.com/buhuipao/anyproxy/pkg/common/monitoring"
	"github.com/buhuipao/anyproxy/pkg/common/protocol"
	"github.com/buhuipao/anyproxy/pkg/logger"
	"github.com/buhuipao/anyproxy/pkg/transport"
)

// connectionLoop handles connection and reconnection logic using transport layer
func (c *Client) connectionLoop() {
	maxRetryDelay := 30 * time.Second
	currentDelay := 1 * time.Second
	maxConsecutiveFailures := 20
	consecutiveFailures := 0

	for {
		select {
		case <-c.ctx.Done():
			logger.Debug("Client context cancelled, stopping connection loop", "client_id", c.getClientID())
			return
		default:
		}

		// Attempt connection
		attemptStartTime := time.Now()

		logger.Debug("Attempting connection to gateway", "client_id", c.getClientID(), "attempt", consecutiveFailures+1, "max_consecutive_failures", maxConsecutiveFailures, "current_delay", currentDelay, "max_retry_delay", maxRetryDelay, "gateway_addr", c.config.Gateway.Addr)

		if err := c.connect(); err != nil {
			// generate new client ID for next connection attempt
			c.actualID = generateClientID(c.config.ClientID, c.replicaIdx)
			consecutiveFailures++
			elapsedTime := time.Since(attemptStartTime)

			if consecutiveFailures >= maxConsecutiveFailures {
				logger.Error("Maximum consecutive connection failures reached", "client_id", c.getClientID(), "consecutive_failures", consecutiveFailures, "max_consecutive_failures", maxConsecutiveFailures, "total_time_elapsed", elapsedTime, "gateway_addr", c.config.Gateway.Addr)
				return
			}

			// Log connection failure
			logger.Error("Connection attempt failed", "client_id", c.getClientID(), "err", err, "consecutive_failures", consecutiveFailures, "max_consecutive_failures", maxConsecutiveFailures, "time_elapsed", elapsedTime, "retry_delay", currentDelay, "gateway_addr", c.config.Gateway.Addr)

			// Wait before retry with exponential backoff
			select {
			case <-c.ctx.Done():
				logger.Debug("Client context cancelled during retry wait", "client_id", c.getClientID())
				return
			case <-time.After(currentDelay):
				currentDelay = time.Duration(float64(currentDelay) * 1.5)
				if currentDelay > maxRetryDelay {
					currentDelay = maxRetryDelay
				}
			}
			continue
		}

		// Reset on successful connection
		consecutiveFailures = 0
		currentDelay = 1 * time.Second
		logger.Info("Connection to gateway established successfully", "client_id", c.getClientID(), "gateway_addr", c.config.Gateway.Addr)

		// Connection successful - this will block until connection is lost
		c.handleMessages()

		// Connection lost - cleanup resources before retry
		logger.Warn("Connection to gateway lost, cleaning up resources before retry", "client_id", c.getClientID(), "gateway_addr", c.config.Gateway.Addr)
		c.cleanup()
	}
}

// connect establishes connection to the gateway
func (c *Client) connect() error {
	logger.Debug("Establishing connection to gateway", "client_id", c.getClientID(), "gateway_addr", c.config.Gateway.Addr)

	// Create TLS configuration if needed
	var tlsConfig *tls.Config
	var err error

	// Auto-detect TLS requirement
	// Check if TLS certificate is provided OR if using WSS/HTTPS scheme
	needsTLS := c.config.Gateway.TLSCert != "" || strings.HasPrefix(c.config.Gateway.Addr, "wss://")
	if needsTLS {
		tlsConfig, err = c.createTLSConfig()
		if err != nil {
			logger.Error("Failed to create TLS configuration", "client_id", c.actualID, "gateway_addr", c.config.Gateway.Addr, "err", err)
			return fmt.Errorf("failed to create TLS configuration: %v", err)
		}
		logger.Debug("TLS configuration created successfully", "client_id", c.actualID, "gateway_addr", c.config.Gateway.Addr)
	}

	// 🆕 Create transport configuration with client information
	transportConfig := &transport.ClientConfig{
		ClientID:      c.actualID,
		GroupID:       c.config.GroupID,
		Username:      c.config.Gateway.AuthUsername,
		Password:      c.config.Gateway.AuthPassword, // Gateway authentication
		GroupPassword: c.config.GroupPassword,        // Client group password for proxy auth
		TLSConfig:     tlsConfig,
		SkipVerify:    false, // Use proper certificate verification by default
	}

	logger.Debug("Transport configuration created", "client_id", c.actualID, "group_id", c.config.GroupID, "auth_enabled", c.config.Gateway.AuthUsername != "", "tls_enabled", tlsConfig != nil)

	// 🆕 Connect via transport layer
	conn, err := c.transport.DialWithConfig(c.config.Gateway.Addr, transportConfig)
	if err != nil {
		logger.Error("Failed to connect via transport layer", "client_id", c.actualID, "gateway_addr", c.config.Gateway.Addr, "err", err)
		return fmt.Errorf("failed to connect: %v", err)
	}

	c.conn = conn
	logger.Info("Transport connection established successfully", "client_id", c.actualID, "group_id", c.config.GroupID, "remote_addr", conn.RemoteAddr())

	// 🆕 Initialize message handler
	c.msgHandler = message.NewClientExtendedMessageHandler(conn)

	// 🆕 Update connection state to connected

	// Send port forwarding request
	if len(c.config.OpenPorts) > 0 {
		logger.Debug("Sending port forwarding request", "client_id", c.actualID, "port_count", len(c.config.OpenPorts))
		if err := c.sendPortForwardingRequest(); err != nil {
			logger.Error("Failed to send port forwarding request", "client_id", c.actualID, "err", err)
			// Continue execution, port forwarding is optional
		}
	} else {
		logger.Debug("No port forwarding configured", "client_id", c.actualID)
	}

	return nil
}

// cleanup cleans up resources after connection loss
func (c *Client) cleanup() {
	logger.Debug("Starting cleanup after connection loss", "client_id", c.getClientID())

	// 🆕 Stop transport layer connection first to stop new message processing
	if c.conn != nil {
		logger.Debug("Stopping transport connection during cleanup", "client_id", c.getClientID())
		if err := c.conn.Close(); err != nil {
			logger.Debug("Error closing client connection during stop (expected)", "err", err)
		}
		c.conn = nil // Reset connection to prevent double close
		logger.Debug("Transport connection stopped", "client_id", c.getClientID())
	}

	// Get connection count (using ConnectionManager)
	connectionCount := c.connMgr.GetConnectionCount()

	// Close all connections (using ConnectionManager)
	if connectionCount > 0 {
		logger.Debug("Closing connections during cleanup", "client_id", c.getClientID(), "connection_count", connectionCount)
		c.connMgr.CloseAllConnections()
		c.connMgr.CloseAllMessageChannels()
	}

	// Don't reset msgHandler here to avoid race conditions with ongoing goroutines
	// msgHandler will be replaced when new connection is established
	// This prevents nil pointer dereference while allowing proper cleanup

	logger.Debug("Cleanup completed", "client_id", c.getClientID(), "connections_closed", connectionCount)
}

// handleConnection handles data transfer for a single client connection
func (c *Client) handleConnection(connID string) {
	logger.Debug("Starting connection handler", "client_id", c.getClientID(), "conn_id", connID)

	// Get connection (using ConnectionManager)
	conn, exists := c.connMgr.GetConnection(connID)
	if !exists {
		logger.Error("Connection not found in connection handler", "client_id", c.getClientID(), "conn_id", connID)
		return
	}

	// Use buffered reading for better performance
	buffer := make([]byte, protocol.DefaultBufferSize)
	totalBytes := 0
	readCount := 0

	for {
		select {
		case <-c.ctx.Done():
			logger.Debug("Connection handler stopping due to context cancellation", "client_id", c.getClientID(), "conn_id", connID, "bytes_transferred", totalBytes)
			return
		default:
		}

		// Set read timeout with context awareness
		deadline := time.Now().Add(protocol.DefaultReadTimeout)
		if ctxDeadline, ok := c.ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
			deadline = ctxDeadline
		}
		if err := conn.SetReadDeadline(deadline); err != nil {
			logger.Warn("Failed to set read deadline", "client_id", c.getClientID(), "conn_id", connID, "err", err)
		}

		// Read data from local connection
		n, err := conn.Read(buffer)
		readCount++

		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				// Read timeout, continue
				continue
			}

			// Gracefully log connection close
			if strings.Contains(err.Error(), "use of closed network connection") ||
				strings.Contains(err.Error(), "connection reset by peer") ||
				err == io.EOF {
				logger.Debug("Local connection closed gracefully", "client_id", c.getClientID(), "conn_id", connID, "total_bytes", totalBytes, "read_count", readCount)
			} else {
				logger.Error("Error reading from local connection", "client_id", c.getClientID(), "conn_id", connID, "err", err, "total_bytes", totalBytes)
			}

			// Send close message to gateway
			if err := c.writeCloseMessage(connID); err != nil {
				logger.Warn("Failed to send close message to gateway", "client_id", c.getClientID(), "conn_id", connID, "err", err)
			}

			// Clean up connection (using ConnectionManager)
			c.cleanupConnection(connID)
			return
		}

		if n > 0 {
			totalBytes += n

			// Sample logs to reduce log volume
			if monitoring.ShouldLogData() && n > 1000 {
				logger.Debug("Read data from local connection", "client_id", c.getClientID(), "conn_id", connID, "bytes", n, "total_bytes", totalBytes)
			}

			// Send data to gateway (using binary protocol)
			if err := c.writeDataMessage(connID, buffer[:n]); err != nil {
				logger.Error("Failed to send data to gateway", "client_id", c.getClientID(), "conn_id", connID, "bytes", n, "err", err)
				c.cleanupConnection(connID)
				return
			}

			// Update connection and client metrics for data sent to gateway
			monitoring.UpdateConnectionBytes(connID, c.getClientID(), int64(n), 0)
		}
	}
}

// cleanupConnection cleans up connection and sends close message (using ConnectionManager)
func (c *Client) cleanupConnection(connID string) {
	logger.Debug("Cleaning up connection", "client_id", c.getClientID(), "conn_id", connID)

	// Close connection in monitoring
	monitoring.CloseConnection(connID)

	// Use ConnectionManager to clean up connection
	c.connMgr.CleanupConnection(connID)

	logger.Debug("Connection cleaned up", "client_id", c.getClientID(), "conn_id", connID)
}
