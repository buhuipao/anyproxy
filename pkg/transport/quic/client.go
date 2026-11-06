// Package quic provides QUIC transport implementation for AnyProxy.
package quic

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/sagernet/quic-go"

	"github.com/buhuipao/anyproxy/pkg/common/protocol"
	"github.com/buhuipao/anyproxy/pkg/logger"
	"github.com/buhuipao/anyproxy/pkg/transport"
)

// dialQUICWithConfig connects to QUIC server with configuration
func (t *quicTransport) dialQUICWithConfig(addr string, config *transport.ClientConfig) (transport.Connection, error) {
	logger.Debug("Establishing QUIC connection to gateway", "client_id", config.ClientID, "gateway_addr", addr)

	// Set up TLS configuration
	tlsConfig := &tls.Config{
		InsecureSkipVerify: config.SkipVerify, // nolint:gosec // User-configurable for development environments
		NextProtos:         []string{alpnProtocol},
	}

	// Use provided TLS config if available
	if config.TLSConfig != nil {
		tlsConfig = config.TLSConfig.Clone()
		if tlsConfig.NextProtos == nil {
			tlsConfig.NextProtos = []string{alpnProtocol}
		}
	}

	// QUIC requires SNI on every handshake. If the caller didn't supply a
	// ServerName (nil TLSConfig fallback, or a hand-crafted config), derive
	// it from the dial address. Without this, Go's TLS stack rejects with
	// "either ServerName or InsecureSkipVerify must be specified".
	if tlsConfig.ServerName == "" && !tlsConfig.InsecureSkipVerify {
		serverName := addr
		if idx := strings.Index(serverName, "://"); idx != -1 {
			serverName = serverName[idx+3:]
		}
		if host, _, err := net.SplitHostPort(serverName); err == nil {
			serverName = host
		}
		tlsConfig.ServerName = serverName
	}

	logger.Debug("QUIC TLS configuration prepared", "client_id", config.ClientID, "skip_verify", tlsConfig.InsecureSkipVerify)

	// 🚨 Fix: Configure QUIC keepalive and idle timeout for fast disconnect detection
	// 🔧 PERFORMANCE: Increased flow control windows for high-throughput streaming (1Gbps+)
	quicConfig := &quic.Config{
		KeepAlivePeriod:                5 * time.Second,    // Send PING keepalive every 5 seconds (fast detection)
		MaxIdleTimeout:                 15 * time.Second,   // 15-second idle timeout (fast reconnect on gateway restart)
		InitialStreamReceiveWindow:    16 * 1024 * 1024,   // 16MB per stream (increased from 6MB)
		MaxStreamReceiveWindow:        32 * 1024 * 1024,   // 32MB max per stream
		InitialConnectionReceiveWindow: 50 * 1024 * 1024,  // 50MB per connection (increased from 15MB)
		MaxConnectionReceiveWindow:    100 * 1024 * 1024,  // 100MB max per connection
		EnableDatagrams:               true,               // Enable QUIC Datagram for low-latency UDP
	}

	// Set connection timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	logger.Info("Connecting to QUIC endpoint", "client_id", config.ClientID, "addr", addr, "keepalive_period", "5s", "idle_timeout", "15s")

	// Establish QUIC connection
	conn, err := quic.DialAddr(ctx, addr, tlsConfig, quicConfig)
	if err != nil {
		logger.Error("Failed to connect to QUIC server", "client_id", config.ClientID, "addr", addr, "err", err)
		return nil, fmt.Errorf("failed to connect to QUIC server: %v", err)
	}

	logger.Info("QUIC connection established", "client_id", config.ClientID)

	// Open a stream for communication
	stream, err := conn.OpenStreamSync(context.Background())
	if err != nil {
		logger.Error("Failed to open QUIC stream", "client_id", config.ClientID, "err", err)
		if closeErr := conn.CloseWithError(0, "failed to open stream"); closeErr != nil {
			logger.Warn("Error closing QUIC connection after stream failure", "err", closeErr)
		}
		return nil, fmt.Errorf("failed to open stream: %v", err)
	}

	logger.Debug("QUIC stream opened", "client_id", config.ClientID)

	// 🚨 Fix: Send authentication message and wait for response
	// v0.59.0: OpenStreamSync already returns *Stream
	if err := t.authenticateClient(stream, config); err != nil {
		if closeErr := conn.CloseWithError(1, "authentication failed"); closeErr != nil {
			logger.Warn("Error closing QUIC connection after auth failure", "err", closeErr)
		}
		return nil, fmt.Errorf("authentication failed: %v", err)
	}

	// Create client connection
	// sagernet/quic-go: stream is Stream interface, conn is Connection interface
	quicConn := newQUICConnection(stream, conn, config.ClientID, config.GroupID, config.GroupPassword)

	logger.Info("QUIC connection established successfully", "client_id", config.ClientID)

	return quicConn, nil
}

// authenticateClient sends authentication message and waits for server response
func (t *quicTransport) authenticateClient(stream quic.Stream, config *transport.ClientConfig) error {
	logger.Debug("Starting QUIC client authentication", "client_id", config.ClientID, "group_id", config.GroupID)

	// Create authentication message using binary protocol
	authData := protocol.PackAuthMessage(config.ClientID, config.GroupID, config.Username, config.Password, config.GroupPassword)

	// Create temporary connection to send authentication message
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tempConn := &quicConnection{
		stream:    stream, // sagernet/quic-go: Stream is interface
		readChan:  make(chan []byte, 1),
		errorChan: make(chan error, 1),
		ctx:       ctx,
		cancel:    cancel,
		isClient:  true,
	}

	// Send authentication message
	if err := tempConn.writeData(authData); err != nil {
		return fmt.Errorf("failed to send auth message: %v", err)
	}

	logger.Debug("Auth message sent, waiting for response", "client_id", config.ClientID)

	// Start receive loop to read response
	// Do not close readChan/errorChan: this goroutine may still send after
	// we return. cancel() lets it exit; the GC frees the channels.
	go func() {
		data, readErr := tempConn.readData()
		if readErr != nil {
			select {
			case tempConn.errorChan <- readErr:
			case <-ctx.Done():
			}
		} else {
			select {
			case tempConn.readChan <- data:
			case <-ctx.Done():
			}
		}
	}()

	// Wait for authentication response (10-second timeout)
	timeout := time.After(10 * time.Second)

	var responseData []byte
	select {
	case responseData = <-tempConn.readChan:
		// Successfully received response
	case err := <-tempConn.errorChan:
		return fmt.Errorf("failed to read auth response: %v", err)
	case <-timeout:
		return fmt.Errorf("authentication response timeout")
	}

	// Check if response is binary protocol
	if !protocol.IsBinaryMessage(responseData) {
		return fmt.Errorf("received non-binary auth response")
	}

	// Parse binary authentication response
	version, msgType, data, err := protocol.UnpackBinaryHeader(responseData)
	if err != nil {
		return fmt.Errorf("failed to unpack auth response: %v", err)
	}

	_ = version // Version not used for now

	if msgType != protocol.BinaryMsgTypeAuthResponse {
		return fmt.Errorf("unexpected message type: 0x%02x", msgType)
	}

	status, reason, err := protocol.UnpackAuthResponseMessage(data)
	if err != nil {
		return fmt.Errorf("failed to parse auth response: %v", err)
	}

	if status != "success" {
		if reason == "" {
			reason = "unknown"
		}
		return fmt.Errorf("authentication failed: %s", reason)
	}

	logger.Info("QUIC client authentication successful", "client_id", config.ClientID, "group_id", config.GroupID)

	return nil
}
