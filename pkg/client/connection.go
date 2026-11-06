package client

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/buhuipao/anyproxy/pkg/common/buffer"
	"github.com/buhuipao/anyproxy/pkg/common/monitoring"
	"github.com/buhuipao/anyproxy/pkg/common/protocol"
	"github.com/buhuipao/anyproxy/pkg/logger"
	"github.com/buhuipao/anyproxy/pkg/transport"
)

// connectionLoop handles connection and reconnection logic using transport layer
func (c *Client) connectionLoop() {
	maxRetryDelay := 30 * time.Second
	currentDelay := 1 * time.Second
	consecutiveFailures := 0

	// retry waits with exponential backoff before the next attempt and
	// returns false only when the client context ends. The client never
	// gives up on its own: it keeps retrying, at most every maxRetryDelay,
	// until it is stopped.
	retry := func(err error, attemptStartTime time.Time) bool {
		consecutiveFailures++
		elapsedTime := time.Since(attemptStartTime)

		// Notify disconnected state with error
		c.notifyConnectionState(ConnectionStateDisconnected, err)

		// Log connection failure
		logger.Error("Connection attempt failed", "client_id", c.getClientID(), "err", err, "consecutive_failures", consecutiveFailures, "time_elapsed", elapsedTime, "retry_delay", currentDelay, "gateway_addr", c.config.Gateway.Addr)

		// Wait before retry with exponential backoff
		select {
		case <-c.ctx.Done():
			logger.Debug("Client context canceled during retry wait", "client_id", c.getClientID())
			return false
		case <-time.After(currentDelay):
			currentDelay = time.Duration(float64(currentDelay) * 1.5)
			if currentDelay > maxRetryDelay {
				currentDelay = maxRetryDelay
			}
		}
		return true
	}

	for {
		select {
		case <-c.ctx.Done():
			logger.Debug("Client context canceled, stopping connection loop", "client_id", c.getClientID())
			c.notifyConnectionState(ConnectionStateDisconnected, nil)
			return
		default:
		}

		// Notify connecting state before attempting connection
		c.notifyConnectionState(ConnectionStateConnecting, nil)

		// Attempt connection
		attemptStartTime := time.Now()

		logger.Info("Attempting connection to gateway", "client_id", c.getClientID(), "attempt", consecutiveFailures+1, "gateway_addr", c.config.Gateway.Addr)

		if err := c.connect(); err != nil {
			// generate new client ID for next connection attempt
			c.actualID = generateClientID(c.config.ClientID, c.replicaIdx)
			if !retry(err, attemptStartTime) {
				return
			}
			continue
		}

		logger.Info("Connection to gateway established successfully", "client_id", c.getClientID(), "gateway_addr", c.config.Gateway.Addr)

		// Notify connected state
		c.notifyConnectionState(ConnectionStateConnected, nil)

		// 🆕 Start heartbeat goroutine (1 second interval)
		// Reset heartbeatStopOnce for new connection cycle
		c.heartbeatStopOnce = sync.Once{}
		c.heartbeatStop = make(chan struct{})
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			c.heartbeatLoop()
		}()

		// 🎮 GAMING OPTIMIZATION: Start datagram handler for low-latency UDP relay
		// Only start if connection supports QUIC Datagram
		if state := c.getConnState(); state != nil {
			if dr, ok := state.conn.(transport.DatagramReader); ok {
				c.wg.Add(1)
				go func() {
					defer c.wg.Done()
					c.handleDatagrams(dr)
				}()
			}
		}

		// Connection successful - this will block until connection is lost
		gatewayErr := c.handleMessages()

		// 🆕 Stop heartbeat before cleanup (use sync.Once to prevent double-close)
		c.stopHeartbeat()

		// Connection lost - cleanup resources before retry
		logger.Warn("Connection to gateway lost, cleaning up resources before retry", "client_id", c.getClientID(), "gateway_addr", c.config.Gateway.Addr)

		if gatewayErr != nil {
			// The gateway rejected this client: a failed attempt, not a session.
			// Keep the client ID; goroutines of the old session may still read it.
			c.cleanup()
			if !retry(gatewayErr, attemptStartTime) {
				return
			}
			continue
		}

		// Notify disconnected state (connection lost)
		c.notifyConnectionState(ConnectionStateDisconnected, nil)

		c.cleanup()

		// Reset after a session the gateway accepted
		consecutiveFailures = 0
		currentDelay = 1 * time.Second
	}
}

// connect establishes connection to the gateway
func (c *Client) connect() error {
	logger.Debug("Establishing connection to gateway", "client_id", c.getClientID(), "gateway_addr", c.config.Gateway.Addr)

	// Create TLS configuration if needed
	var tlsConfig *tls.Config
	var err error

	// Auto-detect TLS requirement
	// Check if TLS certificate is provided OR if using WSS/HTTPS scheme OR
	// if the transport inherently mandates TLS (QUIC always does).
	needsTLS := c.config.Gateway.TLSCert != "" ||
		strings.HasPrefix(c.config.Gateway.Addr, "wss://") ||
		c.config.Gateway.TransportType == protocol.TransportTypeQUIC
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

	// 🚀 Set connection state atomically (lock-free)
	c.setConnState(&connState{
		conn:        conn,
		smartWriter: buffer.NewSmartWriter(conn, c.actualID),
	})

	logger.Info("Transport connection established successfully", "client_id", c.actualID, "group_id", c.config.GroupID, "remote_addr", conn.RemoteAddr())

	// 🆕 Update connection state to connected

	// Send port forwarding request
	if len(c.config.OpenPorts) > 0 {
		logger.Info("Sending port forwarding request", "client_id", c.actualID, "port_count", len(c.config.OpenPorts))
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

	// 🚀 Atomically swap out the connection state (lock-free)
	// This ensures no concurrent access issues
	oldState := c.connStatePtr.Swap(nil)

	if oldState != nil {
		// Stop SmartWriter's background datagram goroutine before closing transport
		if oldState.smartWriter != nil {
			oldState.smartWriter.Close()
		}
		if oldState.conn != nil {
			logger.Debug("Stopping transport connection during cleanup", "client_id", c.getClientID())
			if err := oldState.conn.Close(); err != nil {
				logger.Debug("Error closing client connection during stop (expected)", "err", err)
			}
			logger.Debug("Transport connection stopped", "client_id", c.getClientID())
		}
	}

	// Get connection count (using ConnectionManager)
	connectionCount := c.connMgr.GetConnectionCount()

	// Close all connections (using ConnectionManager)
	if connectionCount > 0 {
		logger.Debug("Closing connections during cleanup", "client_id", c.getClientID(), "connection_count", connectionCount)
		c.connMgr.CloseAllConnections()
		c.connMgr.CloseAllMessageChannels()
	}

	logger.Debug("Cleanup completed", "client_id", c.getClientID(), "connections_closed", connectionCount)
}

// handleConnection handles data transfer for a single client connection using StreamBuffer
// for zero-copy performance. All transports (QUIC, WebSocket, gRPC) implement StreamBufferWriter,
// so this always uses the optimized writev path to minimize memory copies.
func (c *Client) handleConnection(connID string) {
	logger.Debug("Starting connection handler", "client_id", c.getClientID(), "conn_id", connID)

	// Get connection (using ConnectionManager)
	conn, exists := c.connMgr.GetConnection(connID)
	if !exists {
		logger.Error("Connection not found in connection handler", "client_id", c.getClientID(), "conn_id", connID)
		return
	}

	c.handleConnectionStreamBuffer(connID, conn)
}

// handleConnectionStreamBuffer handles connection using StreamBuffer for zero-copy performance.
// This is the optimized path that uses writev to minimize memory copies.
func (c *Client) handleConnectionStreamBuffer(connID string, conn net.Conn) {
	logger.Debug("Starting StreamBuffer connection handler", "client_id", c.getClientID(), "conn_id", connID)

	// UDP target conns route their payload through QUIC Datagram (fire-and-forget,
	// no congestion control) instead of the reliable stream. The reliable stream
	// imposes head-of-line blocking and congestion-controlled backpressure that
	// is fatal for real-time UDP flows like 4K120 game streaming — observed as
	// 1600ms+ latency and 1.6GB client RSS as the writeChan absorbed the stall.
	// TCP still uses the reliable stream unchanged.
	_, isUDP := conn.LocalAddr().(*net.UDPAddr)

	totalBytes := 0
	readCount := 0

	for {
		select {
		case <-c.ctx.Done():
			logger.Debug("StreamBuffer handler stopping due to context cancellation", "client_id", c.getClientID(), "conn_id", connID, "bytes_transferred", totalBytes)
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

		// Acquire a StreamBuffer from the pool
		sb := buffer.AcquireLarge() // 64KB for max data

		// Read directly into the StreamBuffer's data slice
		n, err := conn.Read(sb.DataSlice())
		readCount++

		if err != nil {
			sb.Release() // Release buffer on error

			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				// Read timeout, continue
				continue
			}

			// Gracefully log connection close
			if strings.Contains(err.Error(), "use of closed network connection") ||
				strings.Contains(err.Error(), "connection reset by peer") ||
				err == io.EOF {
				logger.Debug("Local connection closed gracefully (StreamBuffer)", "client_id", c.getClientID(), "conn_id", connID, "total_bytes", totalBytes, "read_count", readCount)
			} else {
				logger.Error("Error reading from local connection (StreamBuffer)", "client_id", c.getClientID(), "conn_id", connID, "err", err, "total_bytes", totalBytes)
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
			sb.SetDataLen(n)
			totalBytes += n

			// Sample logs to reduce log volume
			if monitoring.ShouldLogData() && n > 1000 {
				logger.Debug("Read data (StreamBuffer)", "client_id", c.getClientID(), "conn_id", connID, "bytes", n, "total_bytes", totalBytes)
			}

			// Send data to gateway. UDP target conns use QUIC Datagram
			// (low-latency, unreliable); TCP uses the reliable stream. In
			// both cases ownership of the StreamBuffer transfers to the
			// callee, so we must not release it here.
			var sendErr error
			if isUDP {
				sendErr = c.writeDataMessageDatagram(connID, sb)
			} else {
				sendErr = c.writeDataMessageWithStreamBuffer(connID, sb)
			}
			if sendErr != nil {
				logger.Error("Failed to send data to gateway", "client_id", c.getClientID(), "conn_id", connID, "bytes", n, "is_udp", isUDP, "err", sendErr)
				c.cleanupConnection(connID)
				return
			}

			// Update connection and client metrics for data sent to gateway
			monitoring.UpdateConnectionBytes(connID, c.getClientID(), int64(n), 0)
		} else {
			// No data read, release the buffer
			sb.Release()
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

// stopHeartbeat safely stops the heartbeat loop using sync.Once to prevent double-close panic
func (c *Client) stopHeartbeat() {
	c.heartbeatStopOnce.Do(func() {
		if c.heartbeatStop != nil {
			close(c.heartbeatStop)
		}
	})
}

// heartbeatLoop sends heartbeat messages to gateway every 1 second
// This maintains the client's presence in gateway memory and keeps auth credentials active
func (c *Client) heartbeatLoop() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	logger.Debug("Heartbeat loop started", "client_id", c.getClientID(), "interval", "1s")

	for {
		select {
		case <-c.heartbeatStop:
			logger.Debug("Heartbeat loop stopped", "client_id", c.getClientID())
			return
		case <-c.ctx.Done():
			logger.Debug("Heartbeat loop stopped due to context cancellation", "client_id", c.getClientID())
			return
		case <-ticker.C:
			if err := c.sendHeartbeat(); err != nil {
				logger.Warn("Failed to send heartbeat", "client_id", c.getClientID(), "err", err)
				// Don't return on error - connection handler will detect disconnect
			}
		}
	}
}

// sendHeartbeat sends a heartbeat message to the gateway
func (c *Client) sendHeartbeat() error {
	// 🚀 Lock-free access to connection
	state := c.getConnState()
	if state == nil || state.conn == nil {
		return fmt.Errorf("connection not established")
	}

	timestamp := time.Now().UnixNano()
	heartbeatMsg := protocol.PackHeartbeatMessage(c.actualID, timestamp)

	if err := state.conn.WriteMessage(heartbeatMsg); err != nil {
		return fmt.Errorf("failed to write heartbeat: %v", err)
	}

	return nil
}
