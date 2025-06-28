// Package quic provides QUIC transport implementation for AnyProxy.
package quic

import (
	"context"
	"crypto/tls"
	"fmt"
	"time"

	"github.com/quic-go/quic-go"

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
		NextProtos:         []string{"anyproxy-quic"},
	}

	// Use provided TLS config if available
	if config.TLSConfig != nil {
		tlsConfig = config.TLSConfig.Clone()
		if tlsConfig.NextProtos == nil {
			tlsConfig.NextProtos = []string{"anyproxy-quic"}
		}
	}

	logger.Debug("QUIC TLS configuration prepared", "client_id", config.ClientID, "skip_verify", tlsConfig.InsecureSkipVerify)

	// 🚨 Fix: Configure QUIC keepalive and idle timeout to prevent unexpected connection drops
	quicConfig := &quic.Config{
		KeepAlivePeriod: 30 * time.Second, // Send PING keepalive every 30 seconds
		MaxIdleTimeout:  5 * time.Minute,  // 5-minute idle timeout
	}

	// Set connection timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	logger.Info("Connecting to QUIC endpoint", "client_id", config.ClientID, "addr", addr, "keepalive_period", "30s", "idle_timeout", "5m")

	// Establish QUIC connection
	conn, err := quic.DialAddr(ctx, addr, tlsConfig, quicConfig)
	if err != nil {
		logger.Error("Failed to connect to QUIC server", "client_id", config.ClientID, "addr", addr, "err", err)
		return nil, fmt.Errorf("failed to connect to QUIC server: %v", err)
	}

	logger.Debug("QUIC connection established", "client_id", config.ClientID)

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
	if err := t.authenticateClient(stream, config); err != nil {
		if closeErr := conn.CloseWithError(1, "authentication failed"); closeErr != nil {
			logger.Warn("Error closing QUIC connection after auth failure", "err", closeErr)
		}
		return nil, fmt.Errorf("authentication failed: %v", err)
	}

	// Create client connection
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
		stream:    stream,
		readChan:  make(chan []byte, 1),
		errorChan: make(chan error, 1),
		ctx:       ctx,
		cancel:    cancel,
		isClient:  true,
	}

	// 🆕 Ensure temporary channels are closed
	defer func() {
		close(tempConn.readChan)
		close(tempConn.errorChan)
	}()

	// Send authentication message
	if err := tempConn.writeData(authData); err != nil {
		return fmt.Errorf("failed to send auth message: %v", err)
	}

	logger.Debug("Auth message sent, waiting for response", "client_id", config.ClientID)

	// Start receive loop to read response
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

	logger.Debug("QUIC client authentication successful", "client_id", config.ClientID, "group_id", config.GroupID)

	return nil
}
