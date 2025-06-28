package websocket

import (
	"crypto/tls"
	"net/http"
	"sync"
	"time"

	"github.com/buhuipao/anyproxy/pkg/common/protocol"
	"github.com/buhuipao/anyproxy/pkg/logger"
	"github.com/buhuipao/anyproxy/pkg/transport"
	"github.com/gorilla/websocket"
)

// webSocketTransport WebSocket transport layer implementation
type webSocketTransport struct {
	server     *http.Server
	handler    func(transport.Connection)
	upgrader   websocket.Upgrader
	mu         sync.Mutex
	running    bool
	authConfig *transport.AuthConfig // Add authentication configuration
}

var _ transport.Transport = (*webSocketTransport)(nil)

// NewWebSocketTransport creates a new WebSocket transport layer
func NewWebSocketTransport() transport.Transport {
	return &webSocketTransport{
		upgrader: websocket.Upgrader{
			CheckOrigin: func(_ *http.Request) bool {
				return true // Allow all origins, should be restricted in production
			},
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
		},
	}
}

// NewWebSocketTransportWithAuth creates a WebSocket transport layer with authentication
func NewWebSocketTransportWithAuth(authConfig *transport.AuthConfig) transport.Transport {
	return &webSocketTransport{
		upgrader: websocket.Upgrader{
			CheckOrigin: func(_ *http.Request) bool {
				return true
			},
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
		},
		authConfig: authConfig,
	}
}

// ListenAndServe implements Transport interface - server side listening (HTTP)
func (s *webSocketTransport) ListenAndServe(addr string, handler func(transport.Connection)) error {
	return s.listenAndServe(addr, handler, nil)
}

// ListenAndServeWithTLS implements Transport interface - server side listening (HTTPS/WSS)
func (s *webSocketTransport) ListenAndServeWithTLS(addr string, handler func(transport.Connection), tlsConfig *tls.Config) error {
	return s.listenAndServe(addr, handler, tlsConfig)
}

// listenAndServe unified server startup logic (🆕 supports TLS)
func (s *webSocketTransport) listenAndServe(addr string, handler func(transport.Connection), tlsConfig *tls.Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return nil
	}

	s.handler = handler

	protocol := "HTTP"
	if tlsConfig != nil {
		protocol = "HTTPS"
	}
	logger.Info("Starting WebSocket server", "listen_addr", addr, "protocol", protocol)

	// Create HTTP server
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handleWebSocket)

	s.server = &http.Server{
		Addr:              addr,
		Handler:           mux,
		TLSConfig:         tlsConfig,        // 🆕 Set TLS configuration
		ReadHeaderTimeout: 10 * time.Second, // Prevent Slowloris attacks
	}

	// Start server
	go func() {
		var err error
		if tlsConfig != nil {
			logger.Info("Starting HTTPS WebSocket server (WSS)", "addr", addr)
			// 🆕 Start server with TLS
			err = s.server.ListenAndServeTLS("", "")
		} else {
			logger.Info("Starting HTTP WebSocket server (WS)", "addr", addr)
			err = s.server.ListenAndServe()
		}

		if err != nil && err != http.ErrServerClosed {
			logger.Error("WebSocket server error", "protocol", protocol, "err", err)
		} else {
			logger.Info("WebSocket server stopped", "protocol", protocol)
		}
	}()

	s.running = true
	logger.Info("WebSocket server started successfully", "addr", addr, "protocol", protocol)
	return nil
}

// DialWithConfig connects to server using configuration (🆕 using high-performance connection)
func (s *webSocketTransport) DialWithConfig(addr string, config *transport.ClientConfig) (transport.Connection, error) {
	logger.Debug("WebSocket transport dialing with config", "addr", addr, "client_id", config.ClientID, "group_id", config.GroupID, "tls_enabled", config.TLSConfig != nil)

	// 🆕 Use high-performance WebSocket connection implementation
	return s.dialWebSocketWithConfig(addr, config)
}

// Close implements Transport interface - close transport layer
func (s *webSocketTransport) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return nil
	}

	logger.Info("Stopping WebSocket server")

	err := s.server.Close()
	if err != nil {
		logger.Error("Error closing WebSocket server", "err", err)
	} else {
		logger.Info("WebSocket server stopped successfully")
	}

	s.running = false
	return err
}

// handleWebSocket handles WebSocket connection upgrade
func (s *webSocketTransport) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	// Get client ID
	clientID := r.Header.Get("X-Client-ID")
	if clientID == "" {
		logger.Warn("WebSocket connection rejected: missing client ID", "remote_addr", r.RemoteAddr, "user_agent", r.Header.Get("User-Agent"))
		http.Error(w, "Client ID is required", http.StatusBadRequest)
		return
	}

	// Get group ID and group password
	groupID := r.Header.Get("X-Group-ID")
	groupPassword := r.Header.Get("X-Group-Password") // Get group password from header
	logger.Debug("WebSocket connection attempt", "client_id", clientID, "group_id", groupID, "remote_addr", r.RemoteAddr)

	// Authentication check (Gateway transport layer auth)
	if s.authConfig != nil && s.authConfig.Username != "" {
		username, password, ok := r.BasicAuth()
		if !ok {
			logger.Warn("WebSocket connection rejected: missing authentication", "client_id", clientID, "remote_addr", r.RemoteAddr)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		if username != s.authConfig.Username || password != s.authConfig.Password {
			logger.Warn("WebSocket connection rejected: invalid credentials", "client_id", clientID, "username", username, "remote_addr", r.RemoteAddr)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		logger.Debug("Client authentication successful", "client_id", clientID)
	}

	// Upgrade to WebSocket
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.Error("Failed to upgrade WebSocket connection", "client_id", clientID, "remote_addr", r.RemoteAddr, "err", err)
		return
	}

	logger.Debug("WebSocket connection upgraded successfully", "client_id", clientID)

	// Create connection wrapper with client information
	wsConn := NewWebSocketConnectionWithInfo(conn, clientID, groupID, groupPassword)

	logger.Info("Client connected", "client_id", clientID, "group_id", groupID, "remote_addr", r.RemoteAddr)

	// Call connection handler, don't use recover to hide issues
	defer func() {
		if err := wsConn.Close(); err != nil {
			logger.Warn("Error closing websocket connection", "err", err)
		}
		logger.Info("Client disconnected from WebSocket", "client_id", clientID, "group_id", groupID)
	}()

	// Call connection handler
	s.handler(wsConn)
}

func init() {
	// Register WebSocket transport layer creator
	transport.RegisterTransportCreator(protocol.TransportTypeWebSocket, NewWebSocketTransportWithAuth)
}
