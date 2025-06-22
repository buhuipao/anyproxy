// Package gateway provides gateway implementation for AnyProxy.
package gateway

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/buhuipao/anyproxy/pkg/common/connection"
	commonctx "github.com/buhuipao/anyproxy/pkg/common/context"
	"github.com/buhuipao/anyproxy/pkg/common/message"
	"github.com/buhuipao/anyproxy/pkg/common/monitoring"
	"github.com/buhuipao/anyproxy/pkg/common/protocol"
	"github.com/buhuipao/anyproxy/pkg/common/utils"
	"github.com/buhuipao/anyproxy/pkg/config"
	"github.com/buhuipao/anyproxy/pkg/logger"
	"github.com/buhuipao/anyproxy/pkg/transport"
)

// ClientConn client connection
type ClientConn struct {
	ID             string
	GroupID        string
	Conn           transport.Connection // 🆕 Use transport layer connection
	connMu         sync.RWMutex         // Fix: Use single lock to protect connection and message channels
	Conns          map[string]*Conn
	msgChans       map[string]chan map[string]interface{}
	ctx            context.Context
	cancel         context.CancelFunc
	stopOnce       sync.Once
	wg             sync.WaitGroup
	portForwardMgr *PortForwardManager

	// 🆕 Shared message handler
	msgHandler message.ExtendedMessageHandler
}

// Conn connection structure
type Conn struct {
	ID        string
	LocalConn net.Conn
	Done      chan struct{}
	once      sync.Once
}

// Stop stops the client connection and cleans up resources.
func (c *ClientConn) Stop() {
	c.stopOnce.Do(func() {
		logger.Info("Initiating graceful client stop", "client_id", c.ID)

		// Step 1: Cancel context
		logger.Debug("Cancelling client context", "client_id", c.ID)
		c.cancel()

		// Step 2: Get connection count
		c.connMu.RLock()
		connectionCount := len(c.Conns)
		c.connMu.RUnlock()

		if connectionCount > 0 {
			logger.Info("Waiting for active connections to finish", "client_id", c.ID, "connection_count", connectionCount)
		}

		// Wait for connections to finish current operations
		gracefulWait := func(duration time.Duration) {
			select {
			case <-c.ctx.Done():
				return
			case <-time.After(duration):
				return
			}
		}
		gracefulWait(500 * time.Millisecond)

		// Step 3: 🆕 Close transport layer connection
		if c.Conn != nil {
			logger.Debug("Closing transport connection", "client_id", c.ID)
			if err := c.Conn.Close(); err != nil {
				logger.Warn("Error closing transport connection", "client_id", c.ID, "err", err)
			}
			logger.Debug("Transport connection closed", "client_id", c.ID)
		}

		// Step 4: Close all proxy connections
		logger.Debug("Closing all proxy connections", "client_id", c.ID, "connection_count", connectionCount)
		c.connMu.Lock()
		for connID := range c.Conns {
			c.closeConnectionUnsafe(connID)
		}
		c.connMu.Unlock()
		if connectionCount > 0 {
			logger.Debug("All proxy connections closed", "client_id", c.ID)
		}

		// Step 5: Close all message channels
		// Fix: Now using the same lock, no need to lock again
		c.connMu.Lock()
		channelCount := len(c.msgChans)
		for connID, msgChan := range c.msgChans {
			close(msgChan)
			delete(c.msgChans, connID)
		}
		c.connMu.Unlock()
		if channelCount > 0 {
			logger.Debug("Closed message channels", "client_id", c.ID, "channel_count", channelCount)
		}

		// Step 6: Wait for all goroutines to finish
		logger.Debug("Waiting for client goroutines to finish", "client_id", c.ID)
		done := make(chan struct{})
		go func() {
			c.wg.Wait()
			close(done)
		}()

		select {
		case <-done:
			logger.Debug("All client goroutines finished gracefully", "client_id", c.ID)
		case <-time.After(2 * time.Second):
			logger.Warn("Timeout waiting for client goroutines to finish", "client_id", c.ID)
		}

		logger.Info("Client stop completed", "client_id", c.ID, "connections_closed", connectionCount, "channels_closed", channelCount)
	})
}

func (c *ClientConn) dialNetwork(ctx context.Context, network, addr string) (net.Conn, error) {
	// Prefer connID from context, generate new one if not available
	connID, ok := commonctx.GetConnID(ctx)
	if !ok {
		connID = utils.GenerateConnID()
		logger.Debug("Generated new connection ID", "client_id", c.ID, "conn_id", connID)
	}

	logger.Debug("Creating new network connection", "client_id", c.ID, "conn_id", connID, "network", network, "address", addr)

	// Create pipe to connect client and proxy
	pipe1, pipe2 := net.Pipe()

	// Create proxy connection
	proxyConn := &Conn{
		ID:        connID,
		Done:      make(chan struct{}),
		LocalConn: pipe2,
	}

	// Register connection
	c.connMu.Lock()
	c.Conns[connID] = proxyConn
	connCount := len(c.Conns)
	c.connMu.Unlock()

	// 🆕 Update connection metrics when connection is established
	// Register connection with monitoring
	monitoring.CreateConnection(connID, c.ID, addr)

	logger.Debug("Connection registered", "client_id", c.ID, "conn_id", connID, "total_connections", connCount)

	// 🆕 Send connection request to client (adapted to transport layer)
	// Send connection message using binary format
	err := c.writeConnectMessage(connID, network, addr)
	if err != nil {
		logger.Error("Failed to send connect message to client", "client_id", c.ID, "conn_id", connID, "err", err)
		c.closeConnection(connID)
		return nil, err
	}

	logger.Debug("Connect message sent to client", "client_id", c.ID, "conn_id", connID, "network", network, "address", addr)

	// Start connection handling
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.handleConnection(proxyConn)
	}()

	// Return wrapped connection with important address information wrapping
	connWrapper := connection.NewConnWrapper(pipe1, network, addr)
	connWrapper.SetConnID(connID)
	return connWrapper, nil
}

// handleMessage handles messages from client
func (c *ClientConn) handleMessage() {
	logger.Debug("Starting message handler for client", "client_id", c.ID)
	messageCount := 0

	for {
		select {
		case <-c.ctx.Done():
			logger.Debug("Message handler stopping due to context cancellation", "client_id", c.ID, "messages_processed", messageCount)
			return
		default:
		}

		// 🆕 Read message (using binary format)
		msg, err := c.readNextMessage()
		if err != nil {
			logger.Error("Transport read error", "client_id", c.ID, "messages_processed", messageCount, "err", err)
			return
		}

		messageCount++

		// Handle message type
		msgType, ok := msg["type"].(string)
		if !ok {
			logger.Error("Invalid message format from client - missing or invalid type field", "client_id", c.ID, "message_count", messageCount, "message_fields", utils.GetMessageFields(msg))
			continue
		}

		// Log message processing (but don't log high-frequency data messages)
		if msgType != protocol.MsgTypeData {
			logger.Debug("Processing message", "client_id", c.ID, "message_type", msgType, "message_count", messageCount)
		}

		switch msgType {
		case protocol.MsgTypeConnectResponse, protocol.MsgTypeData, protocol.MsgTypeClose:
			// Route all messages to per-connection channels
			c.routeMessage(msg)
		case protocol.MsgTypePortForwardReq:
			// Handle port forwarding request directly
			logger.Info("Received port forwarding request", "client_id", c.ID)
			c.handlePortForwardRequest(msg)
		default:
			logger.Warn("Unknown message type received", "client_id", c.ID, "message_type", msgType, "message_count", messageCount)
		}
	}
}

// Core message routing and connection management methods

// routeMessage routes messages to the appropriate connection's message channel
func (c *ClientConn) routeMessage(msg map[string]interface{}) {
	connID, ok := msg["id"].(string)
	if !ok {
		logger.Error("Invalid connection ID in message - missing or wrong type", "client_id", c.ID, "message_fields", utils.GetMessageFields(msg))
		return
	}

	msgType, _ := msg["type"].(string)

	// For connect_response messages, create channel first if needed
	if msgType == "connect_response" {
		logger.Debug("Creating message channel for connect response", "client_id", c.ID, "conn_id", connID)
		c.createMessageChannel(connID)
	}

	c.connMu.RLock()
	msgChan, exists := c.msgChans[connID]
	c.connMu.RUnlock()

	if !exists {
		// Connection doesn't exist, ignore message
		logger.Debug("Ignoring message for non-existent connection", "client_id", c.ID, "conn_id", connID, "message_type", msgType)
		return
	}

	// Send message to connection's channel (non-blocking with context awareness)
	select {
	case msgChan <- msg:
		// Successfully routed, don't log high-frequency data messages
		if msgType != protocol.MsgTypeData {
			logger.Debug("Message routed successfully", "client_id", c.ID, "conn_id", connID, "message_type", msgType)
		}
	case <-c.ctx.Done():
		logger.Debug("Message routing cancelled due to context", "client_id", c.ID, "conn_id", connID, "message_type", msgType)
		return
	default:
		// Fix: Close connection when channel is full, instead of silently dropping messages
		logger.Error("Message channel full for connection, closing connection to prevent protocol inconsistency", "client_id", c.ID, "conn_id", connID, "message_type", msgType, "channel_size", len(msgChan), "channel_cap", cap(msgChan))
		// Clean up connection asynchronously to avoid deadlock
		go c.closeConnection(connID)
		return
	}
}

// createMessageChannel creates a message channel for connection
func (c *ClientConn) createMessageChannel(connID string) {
	c.connMu.Lock()
	defer c.connMu.Unlock()

	// Check if channel already exists
	if _, exists := c.msgChans[connID]; exists {
		return
	}

	msgChan := make(chan map[string]interface{}, protocol.DefaultMessageChannelSize)
	c.msgChans[connID] = msgChan

	// Start message processor for this connection
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.processConnectionMessages(connID, msgChan)
	}()
}

// processConnectionMessages processes messages for a specific connection in order
func (c *ClientConn) processConnectionMessages(_ string, msgChan chan map[string]interface{}) {
	for {
		select {
		case <-c.ctx.Done():
			return
		case msg, ok := <-msgChan:
			if !ok {
				return
			}

			msgType, _ := msg["type"].(string)
			switch msgType {
			case protocol.MsgTypeConnectResponse:
				c.handleConnectResponseMessage(msg)
			case protocol.MsgTypeData:
				c.handleDataMessage(msg)
			case protocol.MsgTypeClose:
				c.handleCloseMessage(msg)
				return // Connection closed, stop processing
			}
		}
	}
}

// handleDataMessage handles data messages from client
func (c *ClientConn) handleDataMessage(msg map[string]interface{}) {
	// Extract connection ID and data
	connID, ok := msg["id"].(string)
	if !ok {
		logger.Error("Invalid connection ID in data message", "client_id", c.ID, "message_fields", utils.GetMessageFields(msg))
		return
	}

	var data []byte

	// First try to get byte data directly (binary protocol)
	if rawData, ok := msg["data"].([]byte); ok {
		data = rawData
	} else if dataStr, ok := msg["data"].(string); ok {
		// Compatible with old base64 format
		decoded, err := base64.StdEncoding.DecodeString(dataStr)
		if err != nil {
			logger.Error("Failed to decode base64 data", "client_id", c.ID, "conn_id", connID, "data_length", len(dataStr), "err", err)
			return
		}
		data = decoded
	} else {
		logger.Error("Invalid data format in data message", "client_id", c.ID, "conn_id", connID, "data_type", fmt.Sprintf("%T", msg["data"]))
		return
	}

	// Use log sampler to reduce noise
	if monitoring.ShouldLogData() && len(data) > 1000 {
		logger.Debug("Gateway received data chunk", "client_id", c.ID, "conn_id", connID, "bytes", len(data))
	}

	// Safely get connection
	c.connMu.RLock()
	proxyConn, ok := c.Conns[connID]
	totalConns := len(c.Conns)
	c.connMu.RUnlock()
	if !ok {
		logger.Warn("Data message for unknown connection - possible connection cleanup race", "client_id", c.ID, "conn_id", connID, "data_bytes", len(data), "active_connections", totalConns)
		// Do NOT update metrics for non-existent connections to avoid phantom data
		return
	}

	// Write data to local connection with context awareness
	deadline := time.Now().Add(protocol.DefaultWriteTimeout)
	if ctxDeadline, ok := c.ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := proxyConn.LocalConn.SetWriteDeadline(deadline); err != nil {
		logger.Warn("Failed to set write deadline", "client_id", c.ID, "conn_id", connID, "err", err)
	}

	n, err := proxyConn.LocalConn.Write(data)
	if err != nil {
		logger.Error("Failed to write data to local connection", "client_id", c.ID, "conn_id", connID, "data_bytes", len(data), "written_bytes", n, "err", err)
		// Do NOT update metrics for failed writes to avoid double counting
		c.closeConnection(connID)
		return
	}

	// ONLY update metrics on successful write - this is the single source of truth
	monitoring.UpdateConnectionBytes(connID, c.ID, 0, int64(n))

	// Only log larger transfers
	if n > 10000 {
		logger.Debug("Gateway successfully wrote large data chunk to local connection", "client_id", c.ID, "conn_id", connID, "bytes", n)
	}
}

// handleCloseMessage handles close messages from client
func (c *ClientConn) handleCloseMessage(msg map[string]interface{}) {
	// Extract connection ID
	connID, ok := msg["id"].(string)
	if !ok {
		logger.Error("Invalid connection ID in close message", "client_id", c.ID, "message_fields", utils.GetMessageFields(msg))
		return
	}

	logger.Info("Received close message from client", "client_id", c.ID, "conn_id", connID)
	c.closeConnection(connID)
}

// closeConnection closes connection and cleans up resources
func (c *ClientConn) closeConnection(connID string) {
	// Fix: Use single lock to atomically operate on both maps, avoiding race conditions
	c.connMu.Lock()
	proxyConn, exists := c.Conns[connID]
	if exists {
		delete(c.Conns, connID)
	}

	// Also clean up message channel
	if msgChan, exists := c.msgChans[connID]; exists {
		delete(c.msgChans, connID)
		// Need to close channel outside the lock to avoid deadlock
		defer close(msgChan)
	}
	c.connMu.Unlock()

	// Only clean up if connection exists
	if !exists {
		logger.Debug("Connection already removed", "conn_id", connID, "client_id", c.ID)
		return
	}

	// Close connection in monitoring
	monitoring.CloseConnection(connID)

	// Signal connection to stop (non-blocking, idempotent)
	select {
	case <-proxyConn.Done:
		// Already closed, continue cleanup
	default:
		close(proxyConn.Done)
	}

	// Close local connection
	logger.Debug("Closing local connection", "conn_id", proxyConn.ID)
	err := proxyConn.LocalConn.Close()
	if err != nil {
		logger.Debug("Connection close error (expected during shutdown)", "conn_id", proxyConn.ID, "err", err)
	}

	logger.Debug("Connection closed and cleaned up", "conn_id", proxyConn.ID, "client_id", c.ID)
}

// closeConnectionUnsafe unsafely closes connection (caller must hold lock)
func (c *ClientConn) closeConnectionUnsafe(connID string) {
	proxyConn, exists := c.Conns[connID]
	if !exists {
		return
	}

	delete(c.Conns, connID)

	// Signal connection to stop
	select {
	case <-proxyConn.Done:
		// Already closed
	default:
		close(proxyConn.Done)
	}

	// Close actual connection
	proxyConn.once.Do(func() {
		if err := proxyConn.LocalConn.Close(); err != nil {
			logger.Debug("Connection close error during unsafe close (expected)", "conn_id", proxyConn.ID, "err", err)
		}
	})
}

// handleConnectResponseMessage handles connection response messages from client
func (c *ClientConn) handleConnectResponseMessage(msg map[string]interface{}) {
	connID, ok := msg["id"].(string)
	if !ok {
		logger.Error("Invalid connection ID in connect response", "client_id", c.ID, "message_fields", utils.GetMessageFields(msg))
		return
	}

	success, ok := msg["success"].(bool)
	if !ok {
		logger.Error("Invalid success field in connect response", "client_id", c.ID, "conn_id", connID, "message_fields", utils.GetMessageFields(msg))
		return
	}

	if success {
		logger.Debug("Client successfully connected to target", "client_id", c.ID, "conn_id", connID)
	} else {
		errorMsg, _ := msg["error"].(string)

		// Use different log levels and formats based on error type
		if strings.Contains(strings.ToLower(errorMsg), "forbidden") || strings.Contains(strings.ToLower(errorMsg), "denied") {
			logger.Error("Connection blocked by client security policy", "client_id", c.ID, "conn_id", connID, "error", errorMsg, "action", "Connection rejected by client due to security policy")
		} else if strings.Contains(strings.ToLower(errorMsg), "timeout") {
			logger.Warn("Connection timeout", "client_id", c.ID, "conn_id", connID, "error", errorMsg, "action", "Connection timed out")
		} else {
			logger.Error("Connection failed", "client_id", c.ID, "conn_id", connID, "error", errorMsg, "action", "Client failed to establish connection")
		}

		c.closeConnection(connID)
	}
}

// handleConnection handles proxy connection data transfer
func (c *ClientConn) handleConnection(proxyConn *Conn) {
	logger.Debug("Starting connection handler", "client_id", c.ID, "conn_id", proxyConn.ID)

	// Increase buffer size for better performance
	buffer := make([]byte, protocol.DefaultBufferSize)
	totalBytes := 0
	readCount := 0
	startTime := time.Now()

	defer func() {
		elapsed := time.Since(startTime)
		logger.Debug("Connection handler finished", "client_id", c.ID, "conn_id", proxyConn.ID, "total_bytes", totalBytes, "read_operations", readCount, "duration", elapsed)
	}()

	for {
		select {
		case <-c.ctx.Done():
			logger.Debug("Connection handler stopping due to context cancellation", "client_id", c.ID, "conn_id", proxyConn.ID, "total_bytes", totalBytes)
			return
		case <-proxyConn.Done:
			logger.Debug("Connection handler stopping - connection marked as done", "client_id", c.ID, "conn_id", proxyConn.ID, "total_bytes", totalBytes)
			return
		default:
		}

		// Set read deadline based on context
		deadline := time.Now().Add(protocol.DefaultReadTimeout)
		if ctxDeadline, ok := c.ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
			deadline = ctxDeadline
		}
		if err := proxyConn.LocalConn.SetReadDeadline(deadline); err != nil {
			logger.Warn("Failed to set read deadline", "client_id", c.ID, "conn_id", proxyConn.ID, "error", err)
		}

		n, err := proxyConn.LocalConn.Read(buffer)
		readCount++

		if n > 0 {
			totalBytes += n
			// Only log larger transfers to reduce noise
			if totalBytes%100000 == 0 || n > 10000 {
				logger.Debug("Gateway read data from local connection", "client_id", c.ID, "conn_id", proxyConn.ID, "bytes_this_read", n, "total_bytes", totalBytes, "read_count", readCount)
			}

			// 🆕 Optimization: Use binary format to avoid base64 encoding
			writeErr := c.writeDataMessage(proxyConn.ID, buffer[:n])
			if writeErr != nil {
				logger.Error("Error writing data to client via transport", "client_id", c.ID, "conn_id", proxyConn.ID, "data_bytes", n, "total_bytes", totalBytes, "error", writeErr)
				c.closeConnection(proxyConn.ID)
				return
			}

			// Update connection metrics for data sent back to client (only after successful send)
			monitoring.UpdateConnectionBytes(proxyConn.ID, c.ID, int64(n), 0)

			// Only log larger transfers
			if n > 10000 {
				logger.Debug("Gateway successfully sent large data chunk to client", "client_id", c.ID, "conn_id", proxyConn.ID, "bytes", n, "total_bytes", totalBytes)
			}
		}

		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				// Check if timeout is due to context cancellation
				select {
				case <-c.ctx.Done():
					logger.Debug("Connection handler stopping due to context during timeout", "client_id", c.ID, "conn_id", proxyConn.ID)
					return
				case <-proxyConn.Done:
					logger.Debug("Connection handler stopping - done channel during timeout", "client_id", c.ID, "conn_id", proxyConn.ID)
					return
				default:
					continue // Continue timeout if context is still valid
				}
			}

			// Gracefully handle connection close errors
			if strings.Contains(err.Error(), "use of closed network connection") ||
				strings.Contains(err.Error(), "read/write on closed pipe") ||
				strings.Contains(err.Error(), "connection reset by peer") {
				logger.Debug("Local connection closed during read operation", "client_id", c.ID, "conn_id", proxyConn.ID, "total_bytes", totalBytes, "read_count", readCount)
			} else if err != io.EOF {
				logger.Error("Error reading from local connection", "client_id", c.ID, "conn_id", proxyConn.ID, "total_bytes", totalBytes, "read_count", readCount, "error", err)
			} else {
				logger.Debug("Local connection closed (EOF)", "client_id", c.ID, "conn_id", proxyConn.ID, "total_bytes", totalBytes, "read_count", readCount)
			}

			// 🆕 Send close message to client
			closeErr := c.writeCloseMessage(proxyConn.ID)
			if closeErr != nil {
				logger.Warn("Error sending close message to client", "client_id", c.ID, "conn_id", proxyConn.ID, "error", closeErr)
			} else {
				logger.Debug("Sent close message to client", "client_id", c.ID, "conn_id", proxyConn.ID)
			}

			c.closeConnection(proxyConn.ID)
			return
		}
	}
}

// handlePortForwardRequest handles port forwarding requests
func (c *ClientConn) handlePortForwardRequest(msg map[string]interface{}) {
	// Extract open ports from the message
	openPortsInterface, ok := msg["open_ports"]
	if !ok {
		logger.Error("No open_ports in port_forward_request", "client_id", c.ID)
		c.sendPortForwardResponse(false, "Missing open_ports field")
		return
	}

	// Convert to []config.OpenPort
	openPortsSlice, ok := openPortsInterface.([]interface{})
	if !ok {
		logger.Error("Invalid open_ports format", "client_id", c.ID)
		c.sendPortForwardResponse(false, "Invalid open_ports format")
		return
	}

	var openPorts []config.OpenPort
	for _, portInterface := range openPortsSlice {
		portMap, ok := portInterface.(map[string]interface{})
		if !ok {
			logger.Error("Invalid port configuration format", "client_id", c.ID)
			continue
		}

		// Extract port configuration
		var remotePort, localPort int

		// Handle both int and float64 types for remote_port
		switch v := portMap["remote_port"].(type) {
		case int:
			remotePort = v
		case float64:
			remotePort = int(v)
		default:
			logger.Error("Invalid remote_port type", "client_id", c.ID, "type", fmt.Sprintf("%T", v))
			continue
		}

		// Handle both int and float64 types for local_port
		switch v := portMap["local_port"].(type) {
		case int:
			localPort = v
		case float64:
			localPort = int(v)
		default:
			logger.Error("Invalid local_port type", "client_id", c.ID, "type", fmt.Sprintf("%T", v))
			continue
		}

		localHost, ok := portMap["local_host"].(string)
		if !ok {
			logger.Error("Invalid local_host", "client_id", c.ID)
			continue
		}

		protocol, ok := portMap["protocol"].(string)
		if !ok {
			protocol = "tcp" // Default to TCP
		}

		openPorts = append(openPorts, config.OpenPort{
			RemotePort: remotePort,
			LocalPort:  localPort,
			LocalHost:  localHost,
			Protocol:   protocol,
		})
	}

	if len(openPorts) == 0 {
		logger.Info("No valid ports to open", "client_id", c.ID)
		c.sendPortForwardResponse(true, "No ports to open")
		return
	}

	// Attempt to open the ports
	err := c.portForwardMgr.OpenPorts(c, openPorts)
	if err != nil {
		logger.Error("Failed to open ports", "client_id", c.ID, "err", err)
		c.sendPortForwardResponse(false, err.Error())
		return
	}

	logger.Info("Successfully opened ports", "client_id", c.ID, "port_count", len(openPorts))
	c.sendPortForwardResponse(true, "Ports opened successfully")
}

// sendPortForwardResponse sends port forwarding response (adapted to transport layer)
func (c *ClientConn) sendPortForwardResponse(success bool, message string) {
	// Send response using binary format
	var errorMsg string
	if !success {
		errorMsg = message
	}

	// Create status list (simplified version, only includes success status)
	var statuses []protocol.PortForwardStatus

	binaryMsg := protocol.PackPortForwardResponseMessage(success, errorMsg, statuses)
	if err := c.Conn.WriteMessage(binaryMsg); err != nil {
		logger.Error("Failed to send port forward response", "client_id", c.ID, "err", err)
	}
}
