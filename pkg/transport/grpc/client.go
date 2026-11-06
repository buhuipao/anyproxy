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

	// Configure gRPC client keepalive parameters for fast disconnect detection
	opts = append(opts, grpc.WithKeepaliveParams(keepalive.ClientParameters{
		Time:                5 * time.Second,  // Send keepalive ping every 5 seconds (fast detection)
		Timeout:             10 * time.Second, // Keepalive response timeout
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

	logger.Info("Connecting to gRPC endpoint", "client_id", config.ClientID, "addr", addr, "keepalive_time", "5s", "keepalive_timeout", "10s")

	// Use passthrough resolver to connect directly to the address without DNS resolution
	// This is required for grpc.NewClient to work correctly with IP:port addresses
	target := "passthrough:///" + addr

	// Create gRPC client connection using the new API (grpc.NewClient)
	conn, err := grpc.NewClient(target, opts...)
	if err != nil {
		logger.Error("Failed to create gRPC client", "client_id", config.ClientID, "addr", addr, "err", err)
		return nil, fmt.Errorf("failed to create gRPC client: %v", err)
	}

	logger.Info("gRPC connection established", "client_id", config.ClientID)

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
	if err == nil {
		// The server checks auth when the stream starts and sends headers only
		// if it accepts. A rejected stream ends without headers, and Recv
		// returns its status (e.g. Unauthenticated). Fail the dial in that case,
		// like a websocket bad handshake.
		accepted := make(chan metadata.MD, 1)
		go func() {
			hdr, _ := stream.Header()
			accepted <- hdr
		}()
		select {
		case hdr := <-accepted:
			if hdr == nil {
				_, err = stream.Recv()
			}
		case <-time.After(10 * time.Second):
			// Older gateways send headers only with their first message but
			// reject at once, so a stream still open here was accepted.
		}
	}
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
