// Package protocols provides SOCKS5 proxy implementation for anyproxy.
package protocols

import (
	"context"
	"fmt"
	"log"
	"net"
	"strings"

	commonctx "github.com/buhuipao/anyproxy/pkg/common/context"
	"github.com/buhuipao/anyproxy/pkg/common/utils"
	"github.com/buhuipao/anyproxy/pkg/config"
	"github.com/buhuipao/anyproxy/pkg/logger"
	"github.com/things-go/go-socks5"
)

// SOCKS5Proxy SOCKS5 proxy implementation
type SOCKS5Proxy struct {
	config         *config.SOCKS5Config
	server         *socks5.Server
	dialFunc       func(ctx context.Context, network, addr string) (net.Conn, error)
	groupValidator func(string, string) bool // Function to validate group credentials
	listener       net.Listener
}

// NewSOCKS5ProxyWithAuth creates a new SOCKS5 proxy with authentication
func NewSOCKS5ProxyWithAuth(cfg *config.SOCKS5Config, dialFn func(context.Context, string, string) (net.Conn, error), groupValidator func(string, string) bool) (utils.GatewayProxy, error) {
	logger.Info("Creating SOCKS5 proxy", "listen_addr", cfg.ListenAddr, "auth_enabled", "group-based")

	proxy := &SOCKS5Proxy{
		config:         cfg,
		dialFunc:       dialFn,
		groupValidator: groupValidator,
	}

	// Configure authentication methods
	socks5Auths := []socks5.Authenticator{}

	if groupValidator != nil {
		logger.Debug("Configuring SOCKS5 group-based authentication")

		// Use built-in UserPassAuthenticator with custom credential store
		credStore := &GroupBasedCredentialStore{
			GroupValidator: groupValidator,
		}
		socks5Auths = append(socks5Auths, socks5.UserPassAuthenticator{
			Credentials: credStore,
		})
		logger.Debug("SOCKS5 group-based authentication configured")
	} else {
		logger.Debug("No authentication configured for SOCKS5 proxy")
	}

	// Create wrapped dial function with group information extraction support
	wrappedDialFunc := func(ctx context.Context, network, addr string, request *socks5.Request) (net.Conn, error) {
		// Generate new connection ID
		connID := utils.GenerateConnID()

		clientAddr := "unknown"
		if request != nil {
			clientAddr = request.RemoteAddr.String()
		}
		logger.Info("SOCKS5 dial request received", "conn_id", connID, "network", network, "address", addr, "client", clientAddr)

		// Add connection ID to context
		ctx = commonctx.WithConnID(ctx, connID)

		var userCtx *utils.UserContext

		// Extract user information from request's AuthContext
		if request.AuthContext != nil && request.AuthContext.Payload != nil {
			if username, exists := request.AuthContext.Payload["username"]; exists {
				userCtx = &utils.UserContext{
					Username: username,
					GroupID:  username,
				}
				logger.Info("SOCKS5 user context extracted from authentication", "conn_id", connID, "username", username, "group_id", username, "target_addr", addr)
			} else {
				logger.Debug("No username found in SOCKS5 authentication context", "conn_id", connID)
			}
		}

		// If no user context extracted, create default one
		if userCtx == nil {
			userCtx = &utils.UserContext{
				Username: "socks5-user", // Default username for SOCKS5
				GroupID:  "",            // Default group
			}
			logger.Debug("Using default user context for SOCKS5 request", "conn_id", connID, "default_username", userCtx.Username, "target_addr", addr)
		}

		// Add user context to context
		ctx = commonctx.WithUserContext(ctx, userCtx)

		logger.Debug("Calling dial function for SOCKS5 request", "conn_id", connID, "network", network, "address", addr, "username", userCtx.Username, "group_id", userCtx.GroupID)

		conn, err := dialFn(ctx, network, addr)

		if err != nil {
			logger.Error("SOCKS5 dial failed", "conn_id", connID, "network", network, "address", addr, "username", userCtx.Username, "group_id", userCtx.GroupID, "err", err)
			return nil, err
		}

		// Connection already established, no need to get ID from ConnWrapper again since we already have it

		logger.Info("SOCKS5 dial successful", "conn_id", connID, "network", network, "address", addr, "username", userCtx.Username, "group_id", userCtx.GroupID)

		return conn, nil

		// 🚨 Fix: Wrap the connection to include the remote address information
		// wrappedConn := connection.NewConnWrapper(conn, network, addr)
		// return wrappedConn, nil
	}

	logger.Debug("Configuring SOCKS5 server", "listen_addr", cfg.ListenAddr, "auth_methods_count", len(socks5Auths))

	// Create SOCKS5 server
	server := socks5.NewServer(
		socks5.WithAuthMethods(socks5Auths),
		socks5.WithDialAndRequest(wrappedDialFunc),
		socks5.WithLogger(socks5.NewLogger(log.Default())),
	)

	proxy.server = server
	logger.Info("SOCKS5 proxy created successfully", "listen_addr", cfg.ListenAddr)
	return proxy, nil
}

// Start starts the SOCKS5 proxy server
func (p *SOCKS5Proxy) Start() error {
	logger.Info("Starting SOCKS5 proxy server", "listen_addr", p.config.ListenAddr)

	// Create listener
	logger.Debug("Creating TCP listener for SOCKS5", "address", p.config.ListenAddr)
	listener, err := net.Listen("tcp", p.config.ListenAddr)
	if err != nil {
		logger.Error("Failed to create TCP listener for SOCKS5 proxy", "listen_addr", p.config.ListenAddr, "err", err)
		return fmt.Errorf("failed to listen on %s: %v", p.config.ListenAddr, err)
	}
	p.listener = listener
	logger.Debug("TCP listener created successfully for SOCKS5", "listen_addr", p.config.ListenAddr)

	// Start SOCKS5 server in separate goroutine
	go func() {
		logger.Info("SOCKS5 server starting to serve requests", "listen_addr", p.config.ListenAddr)
		if err := p.server.Serve(listener); err != nil {
			// Check if the error is due to listener being closed (normal shutdown)
			if strings.Contains(err.Error(), "use of closed network connection") {
				logger.Info("SOCKS5 server stopped normally", "listen_addr", p.config.ListenAddr)
			} else {
				logger.Error("SOCKS5 server terminated unexpectedly", "listen_addr", p.config.ListenAddr, "err", err)
			}
		} else {
			logger.Info("SOCKS5 server stopped", "listen_addr", p.config.ListenAddr)
		}
	}()

	logger.Info("SOCKS5 proxy server started successfully", "listen_addr", p.config.ListenAddr)

	return nil
}

// Stop stops the SOCKS5 proxy server
func (p *SOCKS5Proxy) Stop() error {
	logger.Info("Initiating SOCKS5 proxy server shutdown", "listen_addr", p.config.ListenAddr)

	if p.listener != nil {
		logger.Debug("Closing SOCKS5 listener", "listen_addr", p.config.ListenAddr)
		err := p.listener.Close()

		if err != nil {
			logger.Error("Error closing SOCKS5 listener", "listen_addr", p.config.ListenAddr, "err", err)
			return err
		}

		logger.Info("SOCKS5 proxy server shutdown completed", "listen_addr", p.config.ListenAddr)
		return nil
	}

	logger.Debug("SOCKS5 listener was nil, nothing to close", "listen_addr", p.config.ListenAddr)
	return nil
}

// GetListenAddr returns the listen address
func (p *SOCKS5Proxy) GetListenAddr() string {
	return p.config.ListenAddr
}

// GroupBasedCredentialStore implements CredentialStore interface with support for group-based usernames
type GroupBasedCredentialStore struct {
	GroupValidator func(string, string) bool
}

// Valid implements the CredentialStore interface
// Supports usernames in format "username.group_id" by extracting the group for authentication
func (g *GroupBasedCredentialStore) Valid(user, password, userAddr string) bool {
	logger.Debug("SOCKS5 authentication attempt", "username", user, "client", userAddr)

	// Verify credentials using group validator
	isValid := g.GroupValidator != nil && g.GroupValidator(user, password)

	if isValid {
		logger.Debug("SOCKS5 authentication successful", "username", user, "group_id", user, "client", userAddr)
	} else {
		logger.Warn("SOCKS5 authentication failed", "username", user, "group_id", user, "client", userAddr)
	}

	return isValid
}
