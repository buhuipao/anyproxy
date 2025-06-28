package quic

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/buhuipao/anyproxy/pkg/common/protocol"
	"github.com/buhuipao/anyproxy/pkg/logger"
	"github.com/buhuipao/anyproxy/pkg/transport"
)

const (
	authStatusSuccess = "success"
	authStatusFailed  = "failed"
)

// quicTransport implements the Transport interface for QUIC
type quicTransport struct {
	listener   *quic.Listener
	handler    func(transport.Connection)
	mu         sync.Mutex
	running    bool
	authConfig *transport.AuthConfig
}

var _ transport.Transport = (*quicTransport)(nil)

// NewQUICTransport creates a new QUIC transport
func NewQUICTransport() transport.Transport {
	return &quicTransport{}
}

// NewQUICTransportWithAuth creates a new QUIC transport with authentication
func NewQUICTransportWithAuth(authConfig *transport.AuthConfig) transport.Transport {
	return &quicTransport{
		authConfig: authConfig,
	}
}

// ListenAndServe implements Transport interface - serves QUIC without TLS (not supported)
func (t *quicTransport) ListenAndServe(addr string, handler func(transport.Connection)) error {
	// QUIC always requires TLS, so we'll use a self-signed certificate
	cert, err := generateSelfSignedCert()
	if err != nil {
		return fmt.Errorf("failed to generate self-signed certificate: %v", err)
	}

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{"anyproxy-quic"},
		MinVersion:   tls.VersionTLS12, // Enforce minimum TLS 1.2
	}

	return t.listenAndServe(addr, handler, tlsConfig)
}

// ListenAndServeWithTLS implements Transport interface - serves QUIC with TLS
func (t *quicTransport) ListenAndServeWithTLS(addr string, handler func(transport.Connection), tlsConfig *tls.Config) error {
	if tlsConfig == nil {
		return fmt.Errorf("TLS configuration is required for QUIC")
	}

	// Ensure NextProtos is set for QUIC
	if tlsConfig.NextProtos == nil {
		tlsConfig = tlsConfig.Clone()
		tlsConfig.NextProtos = []string{"anyproxy-quic"}
	}

	return t.listenAndServe(addr, handler, tlsConfig)
}

// listenAndServe unified server startup logic
func (t *quicTransport) listenAndServe(addr string, handler func(transport.Connection), tlsConfig *tls.Config) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.running {
		return nil
	}

	t.handler = handler

	logger.Info("Starting QUIC server", "listen_addr", addr)

	// 🚨 Fix: Configure QUIC heartbeat and idle timeout to prevent unexpected connection drops
	quicConfig := &quic.Config{
		KeepAlivePeriod: 30 * time.Second, // Send PING heartbeat every 30 seconds
		MaxIdleTimeout:  5 * time.Minute,  // 5-minute idle timeout
	}

	// Create QUIC listener
	listener, err := quic.ListenAddr(addr, tlsConfig, quicConfig)
	if err != nil {
		logger.Error("Failed to create QUIC listener", "addr", addr, "err", err)
		return fmt.Errorf("failed to listen on %s: %v", addr, err)
	}
	t.listener = listener

	logger.Info("QUIC listener created", "addr", addr, "keepalive_period", "30s", "idle_timeout", "5m")

	// Start accepting connections in a goroutine
	go func() {
		logger.Info("Starting QUIC server", "addr", addr)
		for {
			conn, err := listener.Accept(context.Background())
			if err != nil {
				logger.Error("QUIC server accept error", "err", err)
				return
			}

			// Handle connection in a separate goroutine
			go t.handleConnection(conn)
		}
	}()

	t.running = true
	logger.Info("QUIC server started successfully", "addr", addr)
	return nil
}

// handleConnection handles a new QUIC connection
func (t *quicTransport) handleConnection(conn quic.Connection) {
	logger.Debug("New QUIC connection accepted", "remote_addr", conn.RemoteAddr())

	// Accept the first stream
	stream, err := conn.AcceptStream(context.Background())
	if err != nil {
		logger.Error("Failed to accept QUIC stream", "err", err)
		if err := conn.CloseWithError(0, "failed to accept stream"); err != nil {
			logger.Warn("Error closing QUIC connection after stream accept failure", "err", err)
		}
		return
	}

	logger.Debug("QUIC stream accepted")

	// 🚨 Fix: Wait for and validate authentication message
	clientID, groupID, groupPassword, err := t.authenticateConnection(stream)
	if err != nil {
		logger.Warn("QUIC connection rejected during authentication", "remote_addr", conn.RemoteAddr(), "err", err)
		if err := conn.CloseWithError(1, "authentication failed"); err != nil {
			logger.Warn("Error closing QUIC connection after auth failure", "err", err)
		}
		return
	}

	logger.Info("Client connected via QUIC", "client_id", clientID, "group_id", groupID, "remote_addr", conn.RemoteAddr())

	// Create server connection
	quicConn := newQUICServerConnection(stream, conn, clientID, groupID, groupPassword)

	// Call connection handler, don't use recover to hide issues
	defer func() {
		if err := quicConn.Close(); err != nil {
			logger.Warn("Error closing QUIC connection", "err", err)
		}
		logger.Info("Client disconnected from QUIC", "client_id", clientID, "group_id", groupID)
	}()

	t.handler(quicConn)
}

// authenticateConnection authenticates QUIC connection and extracts client information
func (t *quicTransport) authenticateConnection(stream quic.Stream) (clientID, groupID, password string, err error) {
	// Create temporary connection to read authentication message
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tempConn := &quicConnection{
		stream:    stream,
		readChan:  make(chan []byte, 1),
		errorChan: make(chan error, 1),
		ctx:       ctx,
		cancel:    cancel,
		isClient:  false,
	}

	// 🆕 Ensure temporary channels are closed
	defer func() {
		close(tempConn.readChan)
		close(tempConn.errorChan)
	}()

	// Start receive loop to read the first message
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

	// Set authentication timeout
	timeout := time.After(10 * time.Second)

	var authData []byte
	select {
	case authData = <-tempConn.readChan:
		// Successfully received authentication data
	case err = <-tempConn.errorChan:
		return "", "", "", fmt.Errorf("failed to read auth message: %v", err)
	case <-timeout:
		return "", "", "", fmt.Errorf("authentication timeout")
	}

	// Verify if it's a binary protocol message
	if !protocol.IsBinaryMessage(authData) {
		return "", "", "", fmt.Errorf("received non-binary auth message")
	}

	// Parse binary message header
	version, msgType, data, err := protocol.UnpackBinaryHeader(authData)
	if err != nil {
		return "", "", "", fmt.Errorf("failed to unpack auth message: %v", err)
	}

	_ = version // Version not used for now

	if msgType != protocol.BinaryMsgTypeAuth {
		return "", "", "", fmt.Errorf("expected auth message, got: 0x%02x", msgType)
	}

	// Parse authentication message
	clientID, groupID, username, password, groupPassword, err := protocol.UnpackAuthMessage(data)
	if err != nil {
		return "", "", "", fmt.Errorf("failed to parse auth message: %v", err)
	}

	if clientID == "" {
		return "", "", "", fmt.Errorf("missing client_id")
	}

	// Verify authentication information (Gateway transport layer auth)
	var responseStatus, responseReason string
	if t.authConfig != nil && t.authConfig.Username != "" {
		if username != t.authConfig.Username || password != t.authConfig.Password {
			responseStatus = authStatusFailed
			responseReason = "invalid credentials"
		} else {
			responseStatus = authStatusSuccess
			logger.Debug("QUIC client authentication successful", "client_id", clientID)
		}
	} else {
		responseStatus = authStatusSuccess
	}

	// Build response message
	authResponse := protocol.PackAuthResponseMessage(responseStatus, responseReason)
	if writeErr := tempConn.writeData(authResponse); writeErr != nil {
		return "", "", "", fmt.Errorf("failed to send auth response: %v", writeErr)
	}

	if responseStatus != authStatusSuccess {
		return "", "", "", errors.New(responseReason)
	}

	logger.Debug("QUIC authentication completed successfully", "client_id", clientID, "group_id", groupID)

	return clientID, groupID, groupPassword, nil
}

// DialWithConfig implements Transport interface - client connection
func (t *quicTransport) DialWithConfig(addr string, config *transport.ClientConfig) (transport.Connection, error) {
	logger.Debug("QUIC transport dialing with config", "addr", addr, "client_id", config.ClientID, "group_id", config.GroupID, "tls_enabled", config.TLSConfig != nil)

	return t.dialQUICWithConfig(addr, config)
}

// Close implements Transport interface
func (t *quicTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.running {
		return nil
	}

	logger.Info("Stopping QUIC server")

	if t.listener != nil {
		err := t.listener.Close()
		if err != nil {
			logger.Warn("Error closing QUIC listener", "err", err)
		} else {
			logger.Debug("QUIC listener closed")
		}
	}

	t.running = false
	logger.Info("QUIC server stopped successfully")
	return nil
}

// generateSelfSignedCert generates a self-signed certificate for QUIC
func generateSelfSignedCert() (tls.Certificate, error) {
	// This is a simplified implementation
	// In production, you should use proper certificate generation
	return tls.Certificate{}, fmt.Errorf("self-signed certificate generation not implemented")
}

// Register the transport creator
func init() {
	// Fix: Use explicit constant for registration
	transport.RegisterTransportCreator(protocol.TransportTypeQUIC, NewQUICTransportWithAuth)
}
