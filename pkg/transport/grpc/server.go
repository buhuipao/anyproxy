package grpc

import (
	"crypto/tls"
	"fmt"
	"net"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"

	"github.com/buhuipao/anyproxy/pkg/common/protocol"
	"github.com/buhuipao/anyproxy/pkg/logger"
	"github.com/buhuipao/anyproxy/pkg/transport"
)

// grpcTransport implements the Transport interface for gRPC
type grpcTransport struct {
	server     *grpc.Server
	listener   net.Listener
	handler    func(transport.Connection)
	mu         sync.Mutex
	running    bool
	authConfig *transport.AuthConfig
}

var _ transport.Transport = (*grpcTransport)(nil)

// NewGRPCTransport creates a new gRPC transport
func NewGRPCTransport() transport.Transport {
	return &grpcTransport{}
}

// NewGRPCTransportWithAuth creates a new gRPC transport with authentication
func NewGRPCTransportWithAuth(authConfig *transport.AuthConfig) transport.Transport {
	return &grpcTransport{
		authConfig: authConfig,
	}
}

// ListenAndServe implements Transport interface - serves gRPC without TLS
func (t *grpcTransport) ListenAndServe(addr string, handler func(transport.Connection)) error {
	return t.listenAndServe(addr, handler, nil)
}

// ListenAndServeWithTLS implements Transport interface - serves gRPC with TLS
func (t *grpcTransport) ListenAndServeWithTLS(addr string, handler func(transport.Connection), tlsConfig *tls.Config) error {
	return t.listenAndServe(addr, handler, tlsConfig)
}

// listenAndServe unified server startup logic
func (t *grpcTransport) listenAndServe(addr string, handler func(transport.Connection), tlsConfig *tls.Config) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.running {
		return nil
	}

	t.handler = handler

	protocol := "gRPC"
	if tlsConfig != nil {
		protocol = "gRPC/TLS"
	}
	logger.Info("Starting gRPC server", "listen_addr", addr, "protocol", protocol)

	// Create TCP listener
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		logger.Error("Failed to create TCP listener", "addr", addr, "err", err)
		return fmt.Errorf("failed to listen on %s: %v", addr, err)
	}
	t.listener = listener

	// Create gRPC server options
	var opts []grpc.ServerOption

	// Configure gRPC server-side keepalive parameters to prevent unexpected connection drops
	opts = append(opts, grpc.KeepaliveParams(keepalive.ServerParameters{
		MaxConnectionIdle:     24 * time.Hour,     // Close connection after 24 hours idle
		MaxConnectionAge:      7 * 24 * time.Hour, // Maximum connection lifetime of 7 days
		MaxConnectionAgeGrace: 5 * time.Minute,    // Connection close grace period of 5 minutes
		Time:                  30 * time.Second,   // Server keepalive sending interval
		Timeout:               5 * time.Second,    // Keepalive response timeout
	}))

	// Configure client keepalive policy
	opts = append(opts, grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
		MinTime:             30 * time.Second, // Minimum client keepalive interval
		PermitWithoutStream: true,             // Allow keepalive when no active streams
	}))

	// Configure TLS if provided
	if tlsConfig != nil {
		creds := credentials.NewTLS(tlsConfig)
		opts = append(opts, grpc.Creds(creds))
		logger.Debug("gRPC server TLS configured")
	}

	// Create gRPC server
	t.server = grpc.NewServer(opts...)

	// Register our transport service
	RegisterTransportServiceServer(t.server, &transportServer{
		transport: t,
	})

	logger.Info("gRPC server registered", "addr", addr, "keepalive_time", "30s", "max_idle", "5m", "max_age", "30m")

	// Start serving in a goroutine
	go func() {
		logger.Info("Starting gRPC server", "addr", addr, "protocol", protocol)
		if err := t.server.Serve(listener); err != nil {
			logger.Error("gRPC server error", "protocol", protocol, "err", err)
		} else {
			logger.Info("gRPC server stopped", "protocol", protocol)
		}
	}()

	t.running = true
	logger.Info("gRPC server started successfully", "addr", addr, "protocol", protocol)
	return nil
}

// DialWithConfig implements Transport interface - client connection
func (t *grpcTransport) DialWithConfig(addr string, config *transport.ClientConfig) (transport.Connection, error) {
	logger.Debug("gRPC transport dialing with config", "addr", addr, "client_id", config.ClientID, "group_id", config.GroupID, "tls_enabled", config.TLSConfig != nil)

	return t.dialGRPCWithConfig(addr, config)
}

// Close implements Transport interface
func (t *grpcTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.running {
		return nil
	}

	logger.Info("Stopping gRPC server")

	if t.server != nil {
		logger.Debug("Starting graceful gRPC server shutdown")

		gracefulDone := make(chan struct{})
		go func() {
			t.server.GracefulStop()
			close(gracefulDone)
		}()

		timeout := time.After(5 * time.Second)
		select {
		case <-gracefulDone:
			logger.Debug("gRPC server gracefully stopped")
		case <-timeout:
			logger.Warn("gRPC graceful shutdown timeout, forcing stop")
			t.server.Stop()
			logger.Debug("gRPC server forcefully stopped")
		}
	}

	defer func() {
		logger.Debug("Shutting down gRPC transport server")
		if err := t.listener.Close(); err != nil {
			logger.Warn("Error closing gRPC listener", "err", err)
		}
		logger.Debug("gRPC transport server shutdown completed")
	}()

	t.running = false
	logger.Info("gRPC server stopped successfully")
	return nil
}

// transportServer implements the gRPC service
type transportServer struct {
	UnimplementedTransportServiceServer
	transport *grpcTransport
}

// BiStream implements the bidirectional streaming RPC
func (s *transportServer) BiStream(stream TransportService_BiStreamServer) error {
	// Extract metadata from context
	md, ok := metadata.FromIncomingContext(stream.Context())
	if !ok {
		logger.Warn("gRPC connection rejected: missing metadata")
		return fmt.Errorf("missing metadata")
	}

	// Extract client information
	clientID := getMetadataValue(md, "client-id")
	groupID := getMetadataValue(md, "group-id")
	groupPassword := getMetadataValue(md, "group-password")
	username := getMetadataValue(md, "username")
	password := getMetadataValue(md, "password")

	if clientID == "" {
		logger.Warn("gRPC connection rejected: missing client ID")
		return fmt.Errorf("client ID is required")
	}

	logger.Debug("gRPC connection attempt", "client_id", clientID, "group_id", groupID)

	// Authentication check
	if s.transport.authConfig != nil && s.transport.authConfig.Username != "" {
		if username != s.transport.authConfig.Username || password != s.transport.authConfig.Password {
			logger.Warn("gRPC connection rejected: invalid credentials", "client_id", clientID, "username", username)
			return fmt.Errorf("unauthorized")
		}
		logger.Debug("Client authentication successful", "client_id", clientID)
	}

	logger.Info("Client connected via gRPC", "client_id", clientID, "group_id", groupID)

	// Create connection wrapper
	conn := newGRPCServerConnection(stream, clientID, groupID, groupPassword)

	// Call handler, let any issues surface
	// If bugs cause panic, fix the bug rather than hide it
	go func() {
		defer func() {
			// Only do necessary cleanup, don't hide panic
			if err := conn.Close(); err != nil {
				logger.Debug("Error closing connection (expected)", "err", err)
			}
		}()

		if s.transport.handler != nil {
			s.transport.handler(conn)
		} else {
			logger.Warn("No connection handler set, closing connection", "client_id", clientID)
		}
	}()

	// Wait for stream context cancellation (connection close)
	<-stream.Context().Done()

	logger.Debug("gRPC stream context cancelled", "client_id", clientID, "err", stream.Context().Err())

	logger.Info("Client disconnected from gRPC", "client_id", clientID, "group_id", groupID)

	return stream.Context().Err()
}

// getMetadataValue extracts a single value from gRPC metadata
func getMetadataValue(md metadata.MD, key string) string {
	values := md.Get(key)
	if len(values) > 0 {
		return values[0]
	}
	return ""
}

// Register the transport creator
func init() {
	// Fix: Use explicit constant, avoid empty string registration
	transport.RegisterTransportCreator(protocol.TransportTypeGRPC, NewGRPCTransportWithAuth)
}
