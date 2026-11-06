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

	"golang.org/x/time/rate"

	"github.com/buhuipao/anyproxy/pkg/common/buffer"
	"github.com/buhuipao/anyproxy/pkg/common/connection"
	commonctx "github.com/buhuipao/anyproxy/pkg/common/context"
	"github.com/buhuipao/anyproxy/pkg/common/monitoring"
	"github.com/buhuipao/anyproxy/pkg/common/protocol"
	"github.com/buhuipao/anyproxy/pkg/common/utils"
	"github.com/buhuipao/anyproxy/pkg/config"
	"github.com/buhuipao/anyproxy/pkg/logger"
	"github.com/buhuipao/anyproxy/pkg/transport"
)

// fullChannelGrace is how long the client's shared read loop waits for room in
// a full TCP connection channel before it closes that connection. A channel
// can fill only because its processor was not scheduled while the read loop
// drained a backlog; that processor frees room well within this time. A
// processor that stays blocked has a full BufferedConn behind it: about 7000
// messages the user has not read.
// The wait ends as soon as one slot frees. So only a user that reads nothing
// for this long is closed. A user that keeps reading, but slower than the
// target sends, is not closed: each of its messages can hold the read loop up
// to this long, and the client's other connections move at its pace.
// ponytail: per-connection flow control in the protocol would fix that.
const fullChannelGrace = 100 * time.Millisecond

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

	// 🆕 SmartWriter for unified zero-copy writes with automatic degradation
	smartWriter *buffer.SmartWriter

	// Bandwidth limiter for per-group rate limiting (nil = unlimited)
	bandwidthLimiter *rate.Limiter
}

// Conn connection structure
type Conn struct {
	ID         string
	LocalConn  net.Conn
	Done       chan struct{}
	once       sync.Once // Protects LocalConn.Close()
	doneOnce   sync.Once // 🛡️ Protects Done channel close
	IsUDP      bool      // 🆕 Whether this is a UDP connection (for QUIC Datagram routing)
	TargetAddr string    // 🆕 Target address (needed for UDP data messages)
}

// Stop stops the client connection and cleans up resources.
func (c *ClientConn) Stop() {
	c.stopOnce.Do(func() {
		logger.Info("Initiating graceful client stop", "client_id", c.ID)

		// Step 1: Cancel context
		logger.Debug("Canceling client context", "client_id", c.ID)
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

		// Step 3a: Stop SmartWriter's background datagram goroutine
		if c.smartWriter != nil {
			c.smartWriter.Close()
		}

		// Step 3b: Close transport layer connection
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

		// Step 5: Remove all message channels. They are never closed, so the
		// read loop can never send on a closed channel; their processors stop
		// on the canceled context.
		c.connMu.Lock()
		channelCount := len(c.msgChans)
		for connID := range c.msgChans {
			delete(c.msgChans, connID)
		}
		c.connMu.Unlock()
		if channelCount > 0 {
			logger.Debug("Removed message channels", "client_id", c.ID, "channel_count", channelCount)
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

	// 🆕 Determine if this is a UDP connection for QUIC Datagram routing
	isUDP := network == "udp" || network == "udp4" || network == "udp6"

	// 🆕 Replace net.Pipe() with BufferedConnPair for non-blocking writes
	bufSize := connection.TCPBufferSize // TCP: blocks when full (backpressure)
	if isUDP {
		bufSize = connection.UDPBufferSize // UDP: drop-oldest when full (prevents bufferbloat)
	}
	writerConn, readerConn := connection.NewBufferedConnPair(bufSize, isUDP)

	if isUDP {
		logger.Debug("Creating UDP connection (will use QUIC Datagram + BufferedConn)", "client_id", c.ID, "conn_id", connID, "target", addr, "buffer_size", bufSize)
	} else {
		logger.Debug("Creating TCP connection (BufferedConn)", "client_id", c.ID, "conn_id", connID, "target", addr, "buffer_size", bufSize)
	}

	// Create proxy connection
	proxyConn := &Conn{
		ID:         connID,
		Done:       make(chan struct{}),
		LocalConn:  readerConn, // handleConnection reads from here (non-blocking peer)
		IsUDP:      isUDP,      // Mark UDP connections for QUIC Datagram routing
		TargetAddr: addr,       // Store target address for UDP data messages
	}

	// Register the connection and its message channel before sending connect,
	// so the connect_response always finds the channel. Until that response
	// arrives, dialNetwork is the only reader of msgChan.
	msgChan := make(chan map[string]interface{}, protocol.DefaultMessageChannelSize)
	c.connMu.Lock()
	c.Conns[connID] = proxyConn
	c.msgChans[connID] = msgChan
	// Register with monitoring under the same lock, so a Stop that removes
	// this connection always finds the record to close
	monitoring.CreateConnection(connID, c.ID, addr)
	connCount := len(c.Conns)
	c.connMu.Unlock()

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

	// Wait for connect_response with timeout
	connectTimeout := protocol.DefaultConnectTimeout
	timer := time.NewTimer(connectTimeout)
	defer timer.Stop()

	select {
	case <-proxyConn.Done:
		// closeConnection or Stop closed the connection while we waited
		return nil, fmt.Errorf("connection %s closed while waiting for connect_response", connID)
	case msg := <-msgChan:
		// The client always sends connect_response first for a connection
		msgType, _ := msg["type"].(string)
		if msgType != protocol.MsgTypeConnectResponse {
			logger.Error("Unexpected message while waiting for connect_response", "client_id", c.ID, "conn_id", connID, "message_type", msgType)
			c.closeConnection(connID)
			return nil, fmt.Errorf("unexpected %q message while waiting for connect_response", msgType)
		}
		if success, _ := msg["success"].(bool); !success {
			errorMsg, _ := msg["error"].(string)
			logger.Error("Client failed to connect to target", "client_id", c.ID, "conn_id", connID, "err", errorMsg)
			c.closeConnection(connID)
			return nil, fmt.Errorf("client connection failed: %s", errorMsg)
		}
		logger.Debug("Connect response received, connection ready", "client_id", c.ID, "conn_id", connID)
	case <-timer.C:
		logger.Error("Timeout waiting for connect_response", "client_id", c.ID, "conn_id", connID, "timeout", connectTimeout)
		c.abandonDial(connID)
		return nil, fmt.Errorf("connect timeout after %v", connectTimeout)
	case <-ctx.Done():
		logger.Debug("Dial canceled while waiting for connect_response", "client_id", c.ID, "conn_id", connID, "err", ctx.Err())
		c.abandonDial(connID)
		return nil, ctx.Err()
	case <-c.ctx.Done():
		c.closeConnection(connID)
		return nil, c.ctx.Err()
	}

	// NOW start message processing and connection handling
	// (after Client has confirmed connection)
	c.wg.Add(2)
	go func() {
		defer c.wg.Done()
		c.processConnectionMessages(proxyConn.Done, msgChan)
	}()
	go func() {
		defer c.wg.Done()
		c.handleConnection(proxyConn)
	}()

	// Return wrapped connection with important address information wrapping
	// 🆕 writerConn is the protocol layer's end (SOCKS5 writes here, non-blocking)
	connWrapper := connection.NewConnWrapper(writerConn, network, addr)
	connWrapper.SetConnID(connID)
	return connWrapper, nil
}

// abandonDial gives up on a dial that has no connect_response yet. The client
// may still open the target later, so it is told to close the connection.
func (c *ClientConn) abandonDial(connID string) {
	if err := c.writeCloseMessage(connID); err != nil {
		logger.Warn("Error sending close message", "client_id", c.ID, "conn_id", connID, "err", err)
	}
	c.closeConnection(connID)
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
		case protocol.MsgTypeHeartbeat:
			// 🆕 Handle heartbeat message from client
			c.handleHeartbeat(msg)
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

	c.connMu.RLock()
	msgChan, exists := c.msgChans[connID]
	proxyConn := c.Conns[connID]
	c.connMu.RUnlock()
	isUDP := proxyConn != nil && proxyConn.IsUDP

	if !exists {
		// Connection doesn't exist, ignore message
		// Only log non-data/close messages to avoid log spam during cleanup
		if msgType != protocol.MsgTypeData && msgType != protocol.MsgTypeClose {
			logger.Debug("Ignoring message for non-existent connection", "client_id", c.ID, "conn_id", connID, "message_type", msgType)
		}
		return
	}

	// 🔧 Queue depth monitoring to detect potential bufferbloat
	// (logged once when the queue crosses 80%, not for every message above it)
	queueDepth := len(msgChan)
	queueCap := cap(msgChan)
	if queueDepth == queueCap*80/100 {
		logger.Warn("Message channel near capacity (potential bufferbloat)",
			"client_id", c.ID, "conn_id", connID, "depth", queueDepth, "capacity", queueCap,
			"usage_pct", queueDepth*100/queueCap)
	}

	// Send message to connection's channel (message channels are never closed)
	select {
	case msgChan <- msg:
		// Successfully routed, don't log high-frequency data messages
		if msgType != protocol.MsgTypeData {
			logger.Debug("Message routed successfully", "client_id", c.ID, "conn_id", connID, "message_type", msgType)
		}
	case <-c.ctx.Done():
		logger.Debug("Message routing canceled due to context", "client_id", c.ID, "conn_id", connID, "message_type", msgType)
	default:
		// Channel full. This read loop is shared by all connections of the
		// client, so it waits at most fullChannelGrace.
		if msgType == protocol.MsgTypeData && isUDP {
			// UDP: drop the packet, loss is acceptable
			return
		}
		timer := time.NewTimer(fullChannelGrace)
		defer timer.Stop()
		select {
		case msgChan <- msg:
			return
		case <-c.ctx.Done():
			return
		case <-timer.C:
		}
		// TCP data or a control message: close the connection
		logger.Warn("Message channel full, closing connection", "client_id", c.ID, "conn_id", connID, "message_type", msgType)
		if err := c.writeCloseMessage(connID); err != nil {
			logger.Warn("Error sending close message", "client_id", c.ID, "conn_id", connID, "err", err)
		}
		c.closeConnection(connID)
	}
}

// processConnectionMessages processes messages for a specific connection in order.
// It stops once the connection is closed (done): nothing queued can be
// delivered to a closed connection.
func (c *ClientConn) processConnectionMessages(done <-chan struct{}, msgChan chan map[string]interface{}) {
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-done:
			return
		case msg := <-msgChan:
			select {
			case <-done:
				return
			default:
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
		// The local end is closed: drop the data. handleConnection still reads
		// the user's last bytes, sends the close message and cleans up;
		// closing here would stop it before that.
		logger.Debug("Dropping data for closed local connection", "client_id", c.ID, "conn_id", connID, "data_bytes", len(data), "written_bytes", n, "err", err)
		// Do NOT update metrics for failed writes to avoid double counting
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

	// Also remove the message channel. It is not closed: the read loop may
	// still be sending to it, and its processor stops on proxyConn.Done.
	delete(c.msgChans, connID)
	c.connMu.Unlock()

	// Only clean up if connection exists
	if !exists {
		logger.Debug("Connection already removed", "conn_id", connID, "client_id", c.ID)
		return
	}

	// Close connection in monitoring
	monitoring.CloseConnection(connID)

	// 🛡️ Signal connection to stop using sync.Once to prevent double-close
	proxyConn.doneOnce.Do(func() {
		close(proxyConn.Done)
	})

	// Close local connection (use once to ensure idempotent close)
	proxyConn.once.Do(func() {
		logger.Debug("Closing local connection", "conn_id", proxyConn.ID)
		if err := proxyConn.LocalConn.Close(); err != nil {
			logger.Debug("Connection close error (expected during shutdown)", "conn_id", proxyConn.ID, "err", err)
		}
	})

	logger.Debug("Connection closed and cleaned up", "conn_id", proxyConn.ID, "client_id", c.ID)
}

// closeConnectionUnsafe unsafely closes connection (caller must hold lock)
func (c *ClientConn) closeConnectionUnsafe(connID string) {
	proxyConn, exists := c.Conns[connID]
	if !exists {
		return
	}

	delete(c.Conns, connID)
	monitoring.CloseConnection(connID)

	// 🛡️ Signal connection to stop using sync.Once to prevent double-close
	proxyConn.doneOnce.Do(func() {
		close(proxyConn.Done)
	})

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
			logger.Error("Connection blocked by client security policy", "client_id", c.ID, "conn_id", connID, "err", errorMsg, "action", "Connection rejected by client due to security policy")
		} else if strings.Contains(strings.ToLower(errorMsg), "timeout") {
			logger.Warn("Connection timeout", "client_id", c.ID, "conn_id", connID, "err", errorMsg, "action", "Connection timed out")
		} else {
			logger.Error("Connection failed", "client_id", c.ID, "conn_id", connID, "err", errorMsg, "action", "Client failed to establish connection")
		}

		c.closeConnection(connID)
	}
}

// handleConnection handles proxy connection data transfer using StreamBuffer for zero-copy performance.
// All transports (QUIC, WebSocket, gRPC) implement StreamBufferWriter, so this always uses the
// optimized writev path to minimize memory copies.
func (c *ClientConn) handleConnection(proxyConn *Conn) {
	logger.Debug("Starting StreamBuffer connection handler", "client_id", c.ID, "conn_id", proxyConn.ID)

	totalBytes := 0
	readCount := 0
	startTime := time.Now()

	defer func() {
		elapsed := time.Since(startTime)
		logger.Debug("StreamBuffer connection handler finished", "client_id", c.ID, "conn_id", proxyConn.ID, "total_bytes", totalBytes, "read_operations", readCount, "duration", elapsed)
	}()

	for {
		select {
		case <-c.ctx.Done():
			logger.Debug("StreamBuffer handler stopping due to context cancellation", "client_id", c.ID, "conn_id", proxyConn.ID, "total_bytes", totalBytes)
			return
		case <-proxyConn.Done:
			logger.Debug("StreamBuffer handler stopping - connection marked as done", "client_id", c.ID, "conn_id", proxyConn.ID, "total_bytes", totalBytes)
			return
		default:
		}

		// Set read deadline based on context
		deadline := time.Now().Add(protocol.DefaultReadTimeout)
		if ctxDeadline, ok := c.ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
			deadline = ctxDeadline
		}
		if err := proxyConn.LocalConn.SetReadDeadline(deadline); err != nil {
			logger.Warn("Failed to set read deadline", "client_id", c.ID, "conn_id", proxyConn.ID, "err", err)
		}

		// Acquire a StreamBuffer from the pool
		sb := buffer.AcquireLarge() // 64KB for max UDP datagrams

		// Log UDP connections entering read (to debug blocked reads)
		if proxyConn.IsUDP && readCount == 0 {
			logger.Debug("Gateway UDP: entering first Read", "client_id", c.ID, "conn_id", proxyConn.ID, "target", proxyConn.TargetAddr)
		}

		// Read directly into the StreamBuffer's data slice
		n, err := proxyConn.LocalConn.Read(sb.DataSlice())
		readCount++

		// Log UDP read results
		if proxyConn.IsUDP {
			logger.Debug("Gateway UDP: Read returned", "client_id", c.ID, "conn_id", proxyConn.ID, "bytes", n, "err", err, "read_count", readCount)
		}

		if n > 0 {
			sb.SetDataLen(n)
			totalBytes += n

			// Enforce bandwidth limit if configured
			if c.bandwidthLimiter != nil {
				if err := c.bandwidthLimiter.WaitN(c.ctx, n); err != nil {
					sb.Release()
					logger.Debug("Bandwidth limiter interrupted", "client_id", c.ID, "conn_id", proxyConn.ID, "err", err)
					return
				}
			}

			// Always log UDP data transfers for debugging, reduce TCP logging
			if proxyConn.IsUDP {
				logger.Debug("Gateway read UDP data (StreamBuffer)", "client_id", c.ID, "conn_id", proxyConn.ID, "bytes", n, "total_bytes", totalBytes, "target", proxyConn.TargetAddr)
			} else if totalBytes%100000 == 0 || n > 10000 {
				logger.Debug("Gateway read data (StreamBuffer)", "client_id", c.ID, "conn_id", proxyConn.ID, "bytes_this_read", n, "total_bytes", totalBytes, "is_udp", proxyConn.IsUDP)
			}

			// Route UDP/TCP data using StreamBuffer
			var writeErr error
			if proxyConn.IsUDP {
				// UDP data - transfer StreamBuffer ownership
				logger.Debug("Gateway sending UDP data to client", "client_id", c.ID, "conn_id", proxyConn.ID, "bytes", n, "target", proxyConn.TargetAddr)
				writeErr = c.writeUDPDataMessageWithStreamBuffer(proxyConn.ID, proxyConn.TargetAddr, sb)
				if writeErr == nil {
					logger.Debug("Gateway UDP data sent successfully", "client_id", c.ID, "conn_id", proxyConn.ID, "bytes", n)
				}
			} else {
				// TCP data - transfer StreamBuffer ownership
				writeErr = c.writeDataMessageWithStreamBuffer(proxyConn.ID, sb)
			}

			// Note: StreamBuffer ownership has been transferred, don't release here

			if writeErr != nil {
				logger.Error("Error writing data via StreamBuffer", "client_id", c.ID, "conn_id", proxyConn.ID, "data_bytes", n, "total_bytes", totalBytes, "is_udp", proxyConn.IsUDP, "err", writeErr)
				c.closeConnection(proxyConn.ID)
				return
			}

			// Update connection metrics
			monitoring.UpdateConnectionBytes(proxyConn.ID, c.ID, int64(n), 0)
		} else {
			// No data read, release the buffer
			sb.Release()
		}

		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				// Check if timeout is due to context cancellation
				select {
				case <-c.ctx.Done():
					return
				case <-proxyConn.Done:
					return
				default:
					continue
				}
			}

			// Gracefully handle connection close errors
			if strings.Contains(err.Error(), "use of closed network connection") ||
				strings.Contains(err.Error(), "read/write on closed pipe") ||
				strings.Contains(err.Error(), "connection reset by peer") {
				logger.Debug("Local connection closed during StreamBuffer read", "client_id", c.ID, "conn_id", proxyConn.ID, "total_bytes", totalBytes)
			} else if err != io.EOF {
				logger.Error("Error reading from local connection (StreamBuffer)", "client_id", c.ID, "conn_id", proxyConn.ID, "total_bytes", totalBytes, "err", err)
			} else {
				logger.Debug("Local connection closed (EOF, StreamBuffer)", "client_id", c.ID, "conn_id", proxyConn.ID, "total_bytes", totalBytes)
			}

			// Send close message to client
			closeErr := c.writeCloseMessage(proxyConn.ID)
			if closeErr != nil {
				logger.Warn("Error sending close message", "client_id", c.ID, "conn_id", proxyConn.ID, "err", closeErr)
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

// handleDatagrams receives QUIC Datagram messages from the tunnel client and
// routes them through the same per-connection sink used by the reliable
// stream. This closes the latency gap for game streaming: without it, the
// Sunshine→Moonlight direction was forced through the reliable stream's
// congestion-controlled path, adding 1600ms+ of queuing latency and letting
// the transport writeChan accumulate hundreds of MB of packet data.
//
// The datagram carries BinaryMsgTypeData framing (same as stream data). The
// UDP-framed variant (BinaryMsgTypeUDPData) is also accepted for forward
// compatibility, though the current client emits BinaryMsgTypeData.
func (c *ClientConn) handleDatagrams(dr transport.DatagramReader) {
	logger.Debug("Starting gateway datagram handler", "client_id", c.ID)
	datagramCount := 0

	for {
		select {
		case <-c.ctx.Done():
			logger.Debug("Gateway datagram handler stopping", "client_id", c.ID, "datagrams_processed", datagramCount)
			return
		default:
		}

		data, err := dr.ReceiveDatagram()
		if err != nil {
			// Any receive error on this QUIC connection means the peer is
			// gone; spinning with `continue` would burn CPU until the defer
			// in Gateway.handleConnection eventually calls client.Stop() and
			// cancels c.ctx. Returning exits the goroutine immediately so the
			// old client's resources can be GC'd without a transient CPU spike.
			logger.Debug("Datagram receive error, exiting handler", "client_id", c.ID, "datagrams_processed", datagramCount, "err", err)
			return
		}

		datagramCount++

		version, msgType, payload, err := protocol.UnpackBinaryHeader(data)
		if err != nil {
			logger.Debug("Failed to parse datagram header", "client_id", c.ID, "err", err)
			continue
		}
		if version != protocol.BinaryProtocolVersion {
			logger.Debug("Datagram version mismatch", "client_id", c.ID, "version", version)
			continue
		}

		switch msgType {
		case protocol.BinaryMsgTypeData:
			connID, udpPayload, err := protocol.UnpackDataMessage(payload)
			if err != nil {
				logger.Debug("Failed to unpack datagram data", "client_id", c.ID, "err", err)
				continue
			}
			c.routeDatagramData(connID, udpPayload)
		case protocol.BinaryMsgTypeUDPData:
			connID, _, udpPayload, err := protocol.UnpackUDPDataMessage(payload)
			if err != nil {
				logger.Debug("Failed to unpack UDP datagram data", "client_id", c.ID, "err", err)
				continue
			}
			c.routeDatagramData(connID, udpPayload)
		default:
			logger.Debug("Unexpected datagram message type", "client_id", c.ID, "msg_type", msgType)
		}
	}
}

// routeDatagramData writes the UDP payload to the proxy connection's
// LocalConn, matching the routing used by the reliable-stream path in
// handleDataMessage. Unknown conn IDs are dropped silently — they usually
// represent late datagrams arriving after the connection was torn down.
func (c *ClientConn) routeDatagramData(connID string, payload []byte) {
	c.connMu.RLock()
	proxyConn, ok := c.Conns[connID]
	c.connMu.RUnlock()
	if !ok {
		return
	}

	if _, err := proxyConn.LocalConn.Write(payload); err != nil {
		logger.Debug("Failed to write datagram payload to local conn", "client_id", c.ID, "conn_id", connID, "err", err)
		return
	}

	monitoring.UpdateConnectionBytes(connID, c.ID, 0, int64(len(payload)))
}

// handleHeartbeat handles heartbeat message from client
// This maintains the client's presence in gateway memory and keeps auth credentials active
func (c *ClientConn) handleHeartbeat(msg map[string]interface{}) {
	clientID, _ := msg["client_id"].(string)
	timestamp, _ := msg["timestamp"].(int64)

	// Log heartbeat received (use debug level to avoid log spam)
	logger.Debug("Heartbeat received", "client_id", c.ID, "msg_client_id", clientID, "timestamp", timestamp)

	// Update last heartbeat time (could be used for monitoring)
	monitoring.UpdateClientHeartbeat(c.ID, c.GroupID)

	// Send heartbeat acknowledgment back to client
	ackMsg := protocol.PackHeartbeatAckMessage(timestamp)
	if err := c.Conn.WriteMessage(ackMsg); err != nil {
		logger.Warn("Failed to send heartbeat ack", "client_id", c.ID, "err", err)
	}
}
