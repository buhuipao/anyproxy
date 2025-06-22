package client

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"time"

	"github.com/buhuipao/anyproxy/pkg/common/monitoring"
	"github.com/buhuipao/anyproxy/pkg/common/protocol"
	"github.com/buhuipao/anyproxy/pkg/common/utils"
	"github.com/buhuipao/anyproxy/pkg/logger"
)

// handleMessages handles messages from gateway
func (c *Client) handleMessages() {
	logger.Debug("Starting message handler for gateway messages", "client_id", c.getClientID())
	messageCount := 0

	for {
		select {
		case <-c.ctx.Done():
			logger.Debug("Message handler stopping due to context cancellation", "client_id", c.getClientID(), "messages_processed", messageCount)
			return
		default:
		}

		// 🆕 Read message (using binary format)
		msg, err := c.readNextMessage()
		if err != nil {
			logger.Error("Transport read error", "client_id", c.getClientID(), "messages_processed", messageCount, "err", err)
			// Connection failed, exit to trigger reconnection
			return
		}

		messageCount++

		// Handle message based on type
		msgType, ok := msg["type"].(string)
		if !ok {
			logger.Error("Invalid message format from gateway", "client_id", c.getClientID(), "message_count", messageCount, "message_fields", utils.GetMessageFields(msg))
			continue
		}

		// Log message processing (but not high-frequency data messages)
		if msgType != protocol.MsgTypeData {
			logger.Debug("Processing gateway message", "client_id", c.getClientID(), "message_type", msgType, "message_count", messageCount)
		}

		switch msgType {
		case protocol.MsgTypeConnect, protocol.MsgTypeData, protocol.MsgTypeClose:
			// Route all messages to each connection's channel
			c.routeMessage(msg)
		case protocol.MsgTypePortForwardResp:
			// Handle port forwarding response directly
			logger.Debug("Received port forwarding response", "client_id", c.getClientID())
			c.handlePortForwardResponse(msg)
		default:
			logger.Warn("Unknown message type from gateway", "client_id", c.getClientID(), "message_type", msgType, "message_count", messageCount)
		}
	}
}

// routeMessage routes messages to appropriate connection's message channel
func (c *Client) routeMessage(msg map[string]interface{}) {
	// Minimal fix: recover from potential panic due to race condition with closed channels
	defer func() {
		if r := recover(); r != nil {
			if connID, ok := msg["id"].(string); ok {
				logger.Error("Recovered from panic in routeMessage - channel likely closed during send", "client_id", c.getClientID(), "conn_id", connID)
			}
		}
	}()

	connID, ok := msg["id"].(string)
	if !ok {
		logger.Error("Invalid connection ID in message from gateway", "client_id", c.getClientID(), "message_fields", utils.GetMessageFields(msg))
		return
	}

	msgType, _ := msg["type"].(string)

	// For connection messages, create channel first
	if msgType == protocol.MsgTypeConnect {
		logger.Debug("Creating message channel for new connection request", "client_id", c.getClientID(), "conn_id", connID)
		c.createMessageChannel(connID)
	}

	msgChan, exists := c.connMgr.GetMessageChannel(connID)
	if !exists {
		// Connection doesn't exist, ignore message
		logger.Debug("Ignoring message for non-existent connection", "client_id", c.getClientID(), "conn_id", connID, "message_type", msgType)
		return
	}

	// Send message to connection's channel (non-blocking, with context awareness)
	select {
	case msgChan <- msg:
		// Successfully routed, don't log high-frequency data messages
		if msgType != protocol.MsgTypeData {
			logger.Debug("Message routed to connection handler", "client_id", c.getClientID(), "conn_id", connID, "message_type", msgType)
		}
	case <-c.ctx.Done():
		logger.Debug("Message routing cancelled due to context", "client_id", c.getClientID(), "conn_id", connID, "message_type", msgType)
		return
	default:
		// Fix: Close connection when channel is full, rather than silently dropping messages
		logger.Error("Message channel full for connection, closing connection to prevent protocol inconsistency", "client_id", c.getClientID(), "conn_id", connID, "message_type", msgType, "channel_size", len(msgChan), "channel_cap", cap(msgChan))
		// Clean up connection asynchronously to avoid deadlock
		go c.cleanupConnection(connID)
		return
	}
}

// createMessageChannel creates message channel for connection
func (c *Client) createMessageChannel(connID string) {
	msgChan := c.connMgr.CreateMessageChannel(connID, protocol.DefaultMessageChannelSize)

	logger.Debug("Created message channel for connection", "client_id", c.getClientID(), "conn_id", connID, "buffer_size", protocol.DefaultMessageChannelSize)

	// Start message processor for this connection
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.processConnectionMessages(connID, msgChan)
	}()
}

// processConnectionMessages processes messages for specific connection in order
func (c *Client) processConnectionMessages(connID string, msgChan chan map[string]interface{}) {
	logger.Debug("Starting connection message processor", "client_id", c.getClientID(), "conn_id", connID)

	messagesProcessed := 0

	defer func() {
		logger.Debug("Connection message processor finished", "client_id", c.getClientID(), "conn_id", connID, "messages_processed", messagesProcessed)
	}()

	for {
		select {
		case <-c.ctx.Done():
			logger.Debug("Connection message processor stopping due to context", "client_id", c.getClientID(), "conn_id", connID, "messages_processed", messagesProcessed)
			return
		case msg, ok := <-msgChan:
			if !ok {
				logger.Debug("Message channel closed for connection", "client_id", c.getClientID(), "conn_id", connID, "messages_processed", messagesProcessed)
				return
			}

			messagesProcessed++
			msgType, _ := msg["type"].(string)

			switch msgType {
			case protocol.MsgTypeConnect:
				c.handleConnectMessage(msg)
			case protocol.MsgTypeData:
				c.handleDataMessage(msg)
			case protocol.MsgTypeClose:
				logger.Debug("Received close message, stopping connection processor", "client_id", c.getClientID(), "conn_id", connID, "messages_processed", messagesProcessed)
				c.handleCloseMessage(msg)
				return // Connection closed, stop processing
			default:
				logger.Warn("Unknown message type in connection processor", "client_id", c.getClientID(), "conn_id", connID, "message_type", msgType)
			}
		}
	}
}

// handleConnectMessage handles connection messages from gateway
func (c *Client) handleConnectMessage(msg map[string]interface{}) {
	// Extract connection information
	connID, ok := msg["id"].(string)
	if !ok {
		logger.Error("Invalid connection ID in connect message", "client_id", c.getClientID(), "message_fields", utils.GetMessageFields(msg))
		return
	}

	network, ok := msg["network"].(string)
	if !ok {
		logger.Error("Invalid network in connect message", "client_id", c.getClientID(), "conn_id", connID, "message_fields", utils.GetMessageFields(msg))
		return
	}

	address, ok := msg["address"].(string)
	if !ok {
		logger.Error("Invalid address in connect message", "client_id", c.getClientID(), "conn_id", connID, "message_fields", utils.GetMessageFields(msg))
		return
	}

	logger.Info("Processing connect request from gateway", "client_id", c.getClientID(), "conn_id", connID, "network", network, "address", address)

	// Check if the connection is allowed
	if !c.isConnectionAllowed(address) {
		errorMsg := fmt.Sprintf("Connection denied - host '%s' is forbidden", address)
		logger.Error("Connection rejected - forbidden host", "client_id", c.getClientID(), "conn_id", connID, "address", address, "reason", "Host is in forbidden list or not in allowed list", "allowed_hosts", c.config.AllowedHosts, "forbidden_hosts", c.config.ForbiddenHosts)

		if err := c.sendConnectResponse(connID, false, errorMsg); err != nil {
			logger.Error("Failed to send connect response for forbidden host", "client_id", c.getClientID(), "conn_id", connID, "err", err)
		}
		return
	}
	logger.Debug("Connection allowed by host filtering rules", "client_id", c.getClientID(), "conn_id", connID, "address", address)

	// Establish connection to target
	logger.Debug("Establishing connection to target", "client_id", c.getClientID(), "conn_id", connID, "network", network, "address", address)

	var d net.Dialer
	ctx, cancel := context.WithTimeout(c.ctx, protocol.DefaultConnectTimeout)
	defer cancel()

	connectStart := time.Now()
	conn, err := d.DialContext(ctx, network, address)
	connectDuration := time.Since(connectStart)

	if err != nil {
		logger.Error("Failed to establish connection to target", "client_id", c.getClientID(), "conn_id", connID, "network", network, "address", address, "connect_duration", connectDuration, "err", err)
		if sendErr := c.sendConnectResponse(connID, false, err.Error()); sendErr != nil {
			logger.Error("Failed to send connect response for connection error", "client_id", c.getClientID(), "conn_id", connID, "original_error", err, "send_error", sendErr)
		}
		// Update failure metrics
		monitoring.IncrementErrors()
		return
	}

	logger.Info("Successfully connected to target", "client_id", c.getClientID(), "conn_id", connID, "network", network, "address", address, "connect_duration", connectDuration)

	// Register connection (using ConnectionManager)
	c.connMgr.AddConnection(connID, conn)
	connectionCount := c.connMgr.GetConnectionCount()

	// Create connection record in monitoring
	monitoring.CreateConnection(connID, c.getClientID(), address)

	logger.Debug("Connection registered", "client_id", c.getClientID(), "conn_id", connID, "total_connections", connectionCount)

	// Send success response
	if err := c.sendConnectResponse(connID, true, ""); err != nil {
		logger.Error("Error sending connect_response to gateway", "client_id", c.getClientID(), "conn_id", connID, "err", err)
		c.cleanupConnection(connID)
		return
	}

	// Start handling connection
	logger.Debug("Starting connection handler", "client_id", c.getClientID(), "conn_id", connID)
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.handleConnection(connID)
	}()
}

// sendConnectResponse sends connection response to gateway (using binary format)
func (c *Client) sendConnectResponse(connID string, success bool, errorMsg string) error {
	logger.Debug("Sending connect response to gateway", "client_id", c.getClientID(), "conn_id", connID, "success", success, "error_message", errorMsg)

	err := c.writeConnectResponse(connID, success, errorMsg)
	if err != nil {
		logger.Error("Failed to write connect response to transport", "client_id", c.getClientID(), "conn_id", connID, "success", success, "err", err)
	} else {
		logger.Debug("Connect response sent successfully", "client_id", c.getClientID(), "conn_id", connID, "success", success)
	}

	return err
}

// handleDataMessage handles data messages from gateway
func (c *Client) handleDataMessage(msg map[string]interface{}) {
	// Extract message information
	connID, ok := msg["id"].(string)
	if !ok {
		logger.Error("Invalid connection ID in data message", "client_id", c.getClientID(), "message_fields", utils.GetMessageFields(msg))
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
			logger.Error("Failed to decode base64 data", "client_id", c.getClientID(), "conn_id", connID, "data_length", len(dataStr), "err", err)
			return
		}
		data = decoded
	} else {
		logger.Error("Invalid data format in data message", "client_id", c.getClientID(), "conn_id", connID, "data_type", fmt.Sprintf("%T", msg["data"]))
		return
	}

	// Only log larger transfers to reduce noise
	if len(data) > 10000 {
		logger.Debug("Client received large data chunk from gateway", "client_id", c.getClientID(), "conn_id", connID, "bytes", len(data))
	}

	// Get connection (using ConnectionManager)
	conn, ok := c.connMgr.GetConnection(connID)
	if !ok {
		logger.Warn("Data message for unknown connection - possible connection cleanup race", "client_id", c.getClientID(), "conn_id", connID, "data_bytes", len(data), "active_connections", c.connMgr.GetConnectionCount())
		// Do NOT update metrics for non-existent connections to avoid phantom data
		return
	}

	// Write data to the connection with context awareness - use longer timeout for proxy connections
	deadline := time.Now().Add(protocol.DefaultWriteTimeout)
	if ctxDeadline, ok := c.ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := conn.SetWriteDeadline(deadline); err != nil {
		logger.Warn("Failed to set write deadline", "client_id", c.getClientID(), "conn_id", connID, "err", err)
	}

	n, err := conn.Write(data)
	if err != nil {
		logger.Error("Failed to write data to target connection", "client_id", c.getClientID(), "conn_id", connID, "data_bytes", len(data), "written_bytes", n, "err", err, "total_connections", c.connMgr.GetConnectionCount())
		// Do NOT update metrics for failed writes to avoid double counting
		c.cleanupConnection(connID)
		return
	}

	// ONLY update metrics on successful write - this is the single source of truth
	monitoring.UpdateConnectionBytes(connID, c.getClientID(), 0, int64(n))

	// Only log larger transfers
	if n > 10000 {
		logger.Debug("Client successfully wrote large data chunk to target connection", "client_id", c.getClientID(), "conn_id", connID, "bytes", n)
	}
}

// handleCloseMessage handles close messages from gateway
func (c *Client) handleCloseMessage(msg map[string]interface{}) {
	connID, ok := msg["id"].(string)
	if !ok {
		logger.Error("Invalid connection ID in close message", "client_id", c.getClientID(), "message_fields", utils.GetMessageFields(msg))
		return
	}

	logger.Info("Received close message from gateway", "client_id", c.getClientID(), "conn_id", connID)
	c.cleanupConnection(connID)
}
