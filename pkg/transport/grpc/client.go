// Package grpc provides gRPC transport implementation for AnyProxy.
package grpc

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"

	"github.com/buhuipao/anyproxy/pkg/logger"
	"github.com/buhuipao/anyproxy/pkg/transport"
)

// dialGRPCWithConfig connects to gRPC server with configuration
func (t *grpcTransport) dialGRPCWithConfig(addr string, config *transport.ClientConfig) (transport.Connection, error) {
	logger.Debug("Establishing gRPC connection to gateway", "client_id", config.ClientID, "gateway_addr", addr)

	// Set up connection options
	var opts []grpc.DialOption

	// 🚨 Fix: Configure gRPC client keepalive parameters to prevent unexpected connection drops
	opts = append(opts, grpc.WithKeepaliveParams(keepalive.ClientParameters{
		Time:                30 * time.Second, // Send keepalive ping every 30 seconds
		Timeout:             5 * time.Second,  // Keepalive response timeout
		PermitWithoutStream: true,             // Allow keepalive when no active streams
	}))

	// Configure TLS
	if config.TLSConfig != nil {
		creds := credentials.NewTLS(config.TLSConfig)
		opts = append(opts, grpc.WithTransportCredentials(creds))
		logger.Debug("gRPC TLS configured", "client_id", config.ClientID)
	} else {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
		logger.Debug("gRPC using insecure connection", "client_id", config.ClientID)
	}

	logger.Info("Connecting to gRPC endpoint", "client_id", config.ClientID, "addr", addr, "keepalive_time", "30s", "keepalive_timeout", "5s")

	// Establish gRPC connection using NewClient (updated API)
	conn, err := grpc.NewClient(addr, opts...)
	if err != nil {
		logger.Error("Failed to create gRPC client", "client_id", config.ClientID, "addr", addr, "err", err)
		return nil, fmt.Errorf("failed to create gRPC client: %v", err)
	}

	logger.Debug("gRPC connection established", "client_id", config.ClientID)

	// Create gRPC client
	client := NewTransportServiceClient(conn)

	// Set up metadata with client info and authentication
	md := metadata.New(map[string]string{
		"client-id":      config.ClientID,
		"group-id":       config.GroupID,
		"group-password": config.GroupPassword,
		"username":       config.Username, // Gateway transport auth username
		"password":       config.Password, // Gateway transport auth password
	})

	// Create context with metadata
	streamCtx := metadata.NewOutgoingContext(context.Background(), md)

	// Create bidirectional stream
	stream, err := client.BiStream(streamCtx)
	if err != nil {
		if closeErr := conn.Close(); closeErr != nil {
			logger.Warn("Error closing gRPC connection after stream failure", "err", closeErr)
		}
		logger.Error("Failed to create gRPC stream", "client_id", config.ClientID, "err", err)
		return nil, fmt.Errorf("failed to create gRPC stream: %v", err)
	}

	logger.Info("gRPC stream established successfully", "client_id", config.ClientID)

	// Create and return connection wrapper
	grpcConn := newGRPCConnection(stream, conn, config.ClientID, config.GroupID, config.GroupPassword)
	return grpcConn, nil
}
