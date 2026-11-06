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
	"github.com/buhuipao/anyproxy/pkg/transport"
)

// handleMessages handles messages from gateway until the connection ends.
// It returns the gateway's error if the gateway sent one; the gateway does
// that when it rejects the client, right before closing the connection.
func (c *Client) handleMessages() error {
	logger.Debug("Starting message handler for gateway messages", "client_id", c.getClientID())
	messageCount := 0
	var gatewayErr error

	for {
		select {
		case <-c.ctx.Done():
			logger.Debug("Message handler stopping due to context cancellation", "client_id", c.getClientID(), "messages_processed", messageCount)
			return gatewayErr
		default:
		}

		// 🆕 Read message (using binary format)
		msg, err := c.readNextMessage()
		if err != nil {
			logger.Error("Transport read error", "client_id", c.getClientID(), "messages_processed", messageCount, "err", err)
			// Connection failed, exit to trigger reconnection
			return gatewayErr
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
			logger.Info("Received port forwarding response", "client_id", c.getClientID())
			c.handlePortForwardResponse(msg)
		case protocol.MsgTypeError:
			// Handle gateway-level errors (e.g., authentication failures)
			gatewayErr = fmt.Errorf("gateway error: %v", msg["error_message"])
			if errorMsg, ok := msg["error_message"].(string); ok {
				logger.Error("Gateway error received", "client_id", c.getClientID(), "error_message", errorMsg, "message_count", messageCount)
			} else {
				logger.Error("Gateway error received with invalid format", "client_id", c.getClientID(), "message_count", messageCount, "message_fields", utils.GetMessageFields(msg))
			}
		case protocol.MsgTypeHeartbeatAck:
			// 🆕 Handle heartbeat acknowledgment from gateway (just log for debugging)
			if timestamp, ok := msg["timestamp"].(int64); ok {
				latency := time.Now().UnixNano() - timestamp
				logger.Debug("Heartbeat ack received", "client_id", c.getClientID(), "latency_ns", latency)
			}
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
		// Only log occasionally to avoid log spam during high-throughput cleanup
		if msgType != protocol.MsgTypeData && msgType != protocol.MsgTypeClose {
			logger.Debug("Ignoring message for non-existent connection", "client_id", c.getClientID(), "conn_id", connID, "message_type", msgType)
		}
		return
	}

	// Send message to connection's channel (non-blocking first, then blocking for data)
	select {
	case msgChan <- msg:
		// Successfully routed, don't log high-frequency data messages
		if msgType != protocol.MsgTypeData {
			logger.Debug("Message routed to connection handler", "client_id", c.getClientID(), "conn_id", connID, "message_type", msgType)
		}
	case <-c.ctx.Done():
		logger.Debug("Message routing canceled due to context", "client_id", c.getClientID(), "conn_id", connID, "message_type", msgType)
		return
	default:
		// 🔧 FIX: Channel full - for data messages, try blocking wait instead of dropping
		// This prevents packet loss during high-throughput UDP transfers
		if msgType == protocol.MsgTypeData {
			// Try blocking send with context awareness for data messages
			select {
			case msgChan <- msg:
				// Success after wait
			case <-c.ctx.Done():
				logger.Debug("Message routing canceled during blocking wait", "client_id", c.getClientID(), "conn_id", connID)
				return
			}
		} else {
			// For control messages, close connection to prevent protocol inconsistency
			logger.Warn("Message channel full for control message, closing connection", "client_id", c.getClientID(), "conn_id", connID, "message_type", msgType)
		go c.cleanupConnection(connID)
		return
		}
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

	// IPv6 is opt-in: without allow_ipv6 only IPv4 is resolved and dialed,
	// so IPv4-only forbidden lists still cover every target
	dialNetwork := network
	if !c.config.AllowIPv6 {
		switch network {
		case "tcp":
			dialNetwork = "tcp4"
		case "udp":
			dialNetwork = "udp4"
		}
	}

	ctx, cancel := context.WithTimeout(c.ctx, protocol.DefaultConnectTimeout)
	defer cancel()

	// Check the name rules first so a denied name is never looked up
	if !c.passesNameRules(address) {
		c.rejectForbiddenTarget(connID, address)
		return
	}

	// Resolve once: the IP rules check these IPs and only they are dialed
	ips, port, err := resolveTarget(ctx, dialNetwork, address)
	if err != nil {
		logger.Error("Failed to resolve target", "client_id", c.getClientID(), "conn_id", connID, "network", network, "address", address, "err", err)
		if sendErr := c.sendConnectResponse(connID, false, err.Error()); sendErr != nil {
			logger.Error("Failed to send connect response for resolve error", "client_id", c.getClientID(), "conn_id", connID, "original_error", err, "send_error", sendErr)
		}
		monitoring.IncrementErrors()
		return
	}

	// Check if the connection is allowed
	if !c.isConnectionAllowed(address, ips...) {
		c.rejectForbiddenTarget(connID, address)
		return
	}
	logger.Debug("Connection allowed by host filtering rules", "client_id", c.getClientID(), "conn_id", connID, "address", address)

	// Establish connection to target
	logger.Debug("Establishing connection to target", "client_id", c.getClientID(), "conn_id", connID, "network", network, "address", address)

	connectStart := time.Now()
	conn, err := dialIPs(ctx, dialNetwork, ips, port)
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

// minDialAttempt is the shortest time one IP gets before trying the next,
// the same floor net.Dialer uses for a name with several addresses
const minDialAttempt = 2 * time.Second

// dialIPs dials the checked IPs in order, not the name again. Like
// net.Dialer, each attempt gets an equal share of the time left (at least
// minDialAttempt), so one unreachable IP cannot use the whole timeout.
func dialIPs(ctx context.Context, network string, ips []net.IP, port string) (net.Conn, error) {
	var d net.Dialer
	var err error
	for i, ip := range ips {
		if deadline, ok := ctx.Deadline(); ok {
			d.Timeout = max(time.Until(deadline)/time.Duration(len(ips)-i), minDialAttempt)
		}
		var conn net.Conn
		conn, err = d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
	}
	return nil, err
}

// rejectForbiddenTarget answers a connect request denied by the host rules
func (c *Client) rejectForbiddenTarget(connID, address string) {
	errorMsg := fmt.Sprintf("Connection denied - host '%s' is forbidden", address)
	logger.Error("Connection rejected - forbidden host", "client_id", c.getClientID(), "conn_id", connID, "address", address, "reason", "Host is in forbidden list or not in allowed list", "allowed_hosts", c.config.AllowedHosts, "forbidden_hosts", c.config.ForbiddenHosts)

	if err := c.sendConnectResponse(connID, false, errorMsg); err != nil {
		logger.Error("Failed to send connect response for forbidden host", "client_id", c.getClientID(), "conn_id", connID, "err", err)
	}
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

	// 🔍 Debug: Verify data integrity on Client side
	if isUDP, _ := msg["_is_udp"].(bool); isUDP && len(data) >= 8 {
		seqNum := uint32(data[0])<<24 | uint32(data[1])<<16 | uint32(data[2])<<8 | uint32(data[3])
		expectedChecksum := data[4]
		var actualChecksum byte
		for i := 8; i < len(data); i++ {
			actualChecksum ^= data[i]
		}
		if expectedChecksum != actualChecksum && seqNum < 10 {
			logger.Error("UDP data corruption ON CLIENT", "conn_id", connID, "seq", seqNum,
				"exp_cs", fmt.Sprintf("0x%02x", expectedChecksum), "act_cs", fmt.Sprintf("0x%02x", actualChecksum))
		}
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

// =============================================================================
// QUIC Datagram Handler (for low-latency UDP relay)
// =============================================================================

// handleDatagrams receives and processes QUIC Datagram messages
// 🎮 GAMING OPTIMIZATION: This handles unreliable but low-latency UDP data
// Used for TUIC v5 native UDP relay mode
func (c *Client) handleDatagrams(dr transport.DatagramReader) {
	logger.Debug("Starting datagram handler", "client_id", c.getClientID())
	datagramCount := 0

	for {
		select {
		case <-c.ctx.Done():
			logger.Debug("Datagram handler stopping", "client_id", c.getClientID(), "datagrams_processed", datagramCount)
			return
		default:
		}

		// Receive datagram (unreliable)
		data, err := dr.ReceiveDatagram()
		if err != nil {
			// Any receive error on a QUIC connection means the connection is
			// gone (conn close, peer close, ctx cancel). Continuing would spin
			// tightly on the dead conn forever, because c.ctx is the top-level
			// Client context — it does NOT cancel on reconnect. Returning lets
			// this goroutine exit; connectionLoop starts a fresh handleDatagrams
			// on the next successful connection. Without this, every reconnect
			// permanently leaks a goroutine + reference to the old quicConnection
			// (including its 2000-slot writeChan/readChan) — the slow-OOM source.
			logger.Debug("Datagram receive error, exiting handler", "client_id", c.getClientID(), "datagrams_processed", datagramCount, "err", err)
			return
		}

		datagramCount++

		// Parse the datagram as binary message using binary protocol
		version, msgType, payload, err := protocol.UnpackBinaryHeader(data)
		if err != nil {
			logger.Debug("Failed to parse datagram header", "client_id", c.getClientID(), "err", err)
			continue
		}

		// Version check
		if version != protocol.BinaryProtocolVersion {
			logger.Debug("Datagram version mismatch", "client_id", c.getClientID(), "version", version)
			continue
		}

		// Handle UDP data messages (BinaryMsgTypeUDPData = 0x11)
		if msgType == protocol.BinaryMsgTypeUDPData {
			connID, targetAddr, udpPayload, err := protocol.UnpackUDPDataMessage(payload)
			if err != nil {
				logger.Debug("Failed to unpack UDP data", "client_id", c.getClientID(), "err", err)
				continue
			}

			// Pass raw []byte directly — handleDataMessage's fast path
			// consumes it without decoding. The prior base64 round-trip
			// wasted ~2.6KB of allocation churn per UDP packet for nothing.
			msg := map[string]interface{}{
				"type":        protocol.MsgTypeData,
				"id":          connID,
				"target_addr": targetAddr,
				"data":        udpPayload,
			}
			c.routeMessage(msg)
		}
	}
}
