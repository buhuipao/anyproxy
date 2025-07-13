// Package protocols provides HTTP proxy implementation for anyproxy.
package protocols

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	commonctx "github.com/buhuipao/anyproxy/pkg/common/context"
	"github.com/buhuipao/anyproxy/pkg/common/protocol"
	"github.com/buhuipao/anyproxy/pkg/common/utils"
	"github.com/buhuipao/anyproxy/pkg/config"
	"github.com/buhuipao/anyproxy/pkg/logger"
)

// Fix: Use buffer pool to reduce memory allocation
var bufferPool = sync.Pool{
	New: func() interface{} {
		// Allocate 32KB buffer
		buf := make([]byte, 32*1024)
		return &buf
	},
}

// HTTPProxy HTTP proxy implementation
type HTTPProxy struct {
	config         *config.HTTPConfig
	server         *http.Server
	dialFunc       func(ctx context.Context, network, addr string) (net.Conn, error)
	groupValidator func(string, string) bool // Function to validate group credentials
}

// NewHTTPProxyWithAuth creates a new HTTP proxy with authentication
func NewHTTPProxyWithAuth(config *config.HTTPConfig, dialFn func(context.Context, string, string) (net.Conn, error), groupValidator func(string, string) bool) (utils.GatewayProxy, error) {
	tlsEnabled := config.TLSCert != "" && config.TLSKey != ""
	logger.Info("Creating HTTP proxy", "listen_addr", config.ListenAddr, "auth_enabled", "group-based", "tls_enabled", tlsEnabled)

	proxy := &HTTPProxy{
		config:         config,
		dialFunc:       dialFn,
		groupValidator: groupValidator,
	}

	// 🚨 Fix: Don't use ServeMux as it can't handle CONNECT requests properly
	// Don't use ServeMux as it doesn't handle CONNECT requests properly
	proxy.server = &http.Server{
		Addr:    config.ListenAddr,
		Handler: proxy, // Use proxy itself directly as handler
		// Standard timeout configuration
		ReadTimeout:  60 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	logger.Info("HTTP proxy created successfully", "listen_addr", config.ListenAddr, "tls_enabled", tlsEnabled)
	return proxy, nil
}

// ServeHTTP implements http.Handler interface
// Enables HTTPProxy to serve directly as HTTP server handler, avoiding ServeMux CONNECT issues
func (p *HTTPProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.handleHTTP(w, r)
}

// Start starts the HTTP proxy server
func (p *HTTPProxy) Start() error {
	logger.Info("Starting HTTP proxy server", "listen_addr", p.config.ListenAddr)

	go func() {
		var err error
		// Check if TLS is configured
		if p.config.TLSCert != "" && p.config.TLSKey != "" {
			logger.Info("Starting HTTPS proxy server with TLS", "listen_addr", p.config.ListenAddr, "cert", p.config.TLSCert, "key", p.config.TLSKey)
			err = p.server.ListenAndServeTLS(p.config.TLSCert, p.config.TLSKey)
		} else {
			logger.Info("Starting HTTP proxy server without TLS", "listen_addr", p.config.ListenAddr)
			err = p.server.ListenAndServe()
		}

		if err != nil && err != http.ErrServerClosed {
			logger.Error("HTTP proxy server error", "listen_addr", p.config.ListenAddr, "err", err)
		} else {
			logger.Info("HTTP proxy server stopped")
		}
	}()

	logger.Info("HTTP proxy server started successfully", "listen_addr", p.config.ListenAddr, "tls_enabled", p.config.TLSCert != "" && p.config.TLSKey != "")
	return nil
}

// Stop stops the HTTP proxy server
func (p *HTTPProxy) Stop() error {
	logger.Info("Stopping HTTP proxy server", "listen_addr", p.config.ListenAddr)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := p.server.Shutdown(ctx)
	if err != nil {
		logger.Error("Error stopping HTTP proxy server", "listen_addr", p.config.ListenAddr, "err", err)
	} else {
		logger.Info("HTTP proxy server stopped successfully")
	}

	return err
}

// GetListenAddr returns the listen address
func (p *HTTPProxy) GetListenAddr() string {
	return p.config.ListenAddr
}

// handleHTTP handles HTTP requests
func (p *HTTPProxy) handleHTTP(w http.ResponseWriter, r *http.Request) {
	clientAddr := getClientIP(r)

	logger.Debug("HTTP request received", "method", r.Method, "url", r.URL.String(), "client", clientAddr, "user_agent", r.Header.Get("User-Agent"))

	// Authentication check
	var userCtx *utils.UserContext
	if p.groupValidator != nil {
		logger.Debug("Authentication required, checking credentials", "client", clientAddr)

		username, password, authenticated := p.authenticateAndExtractUser(r)
		if !authenticated {
			logger.Warn("HTTP proxy authentication failed", "client", clientAddr, "method", r.Method, "host", r.Host)
			w.Header().Set("Proxy-Authenticate", "Basic realm=\"Proxy\"")
			http.Error(w, "Proxy Authentication Required", http.StatusProxyAuthRequired)
			return
		}

		// Validate group credentials
		if !p.groupValidator(username, password) {
			logger.Warn("HTTP proxy group authentication failed", "client", clientAddr, "group_id", username)
			w.Header().Set("Proxy-Authenticate", "Basic realm=\"Proxy\"")
			http.Error(w, "Proxy Authentication Required", http.StatusProxyAuthRequired)
			return
		}

		// Set user context
		userCtx = &utils.UserContext{
			Username: username,
			GroupID:  username,
		}

		logger.Debug("HTTP proxy authentication successful", "username", username, "group_id", username, "client", clientAddr)
	} else {
		logger.Debug("No authentication required")
	}

	// Set user context to request
	if userCtx != nil {
		ctx := commonctx.WithUserContext(r.Context(), userCtx)
		r = r.WithContext(ctx)
	}

	// Handle CONNECT method
	if r.Method == http.MethodConnect {
		username := ""
		if userCtx != nil {
			username = userCtx.Username
		}
		logger.Info("Handling HTTPS CONNECT request", "target_host", r.Host, "client", clientAddr, "username", username)
		p.handleConnect(w, r, clientAddr)
		return
	}

	// Handle normal HTTP requests
	username := ""
	if userCtx != nil {
		username = userCtx.Username
	}
	logger.Info("Handling HTTP request", "method", r.Method, "url", r.URL.String(), "client", clientAddr, "username", username)
	p.handleRequest(w, r, clientAddr)
}

// authenticateAndExtractUser checks proxy authentication and returns username, password, and auth status
func (p *HTTPProxy) authenticateAndExtractUser(r *http.Request) (string, string, bool) {
	logger.Debug("Checking proxy authentication", "remote_addr", r.RemoteAddr, "method", r.Method, "host", r.Host)

	auth := r.Header.Get("Proxy-Authorization")
	if auth == "" {
		logger.Debug("No proxy authorization header found", "remote_addr", r.RemoteAddr)
		return "", "", false
	}

	// Parse Basic authentication
	const prefix = "Basic "
	if !strings.HasPrefix(auth, prefix) {
		logger.Warn("Invalid proxy authorization header format", "remote_addr", r.RemoteAddr, "auth_type", strings.SplitN(auth, " ", 2)[0])
		return "", "", false
	}

	decoded, err := base64.StdEncoding.DecodeString(auth[len(prefix):])
	if err != nil {
		logger.Warn("Failed to decode proxy authorization header", "remote_addr", r.RemoteAddr, "err", err)
		return "", "", false
	}

	credentials := string(decoded)
	parts := strings.SplitN(credentials, ":", 2)
	if len(parts) != 2 {
		logger.Warn("Invalid credentials format in proxy authorization", "remote_addr", r.RemoteAddr)
		return "", "", false
	}

	username, password := parts[0], parts[1]

	// Basic authentication parsing was successful, actual validation will be done by group validator
	logger.Debug("Proxy authentication credentials parsed", "remote_addr", r.RemoteAddr, "username", username)

	return username, password, true
}

// handleConnect handles CONNECT requests for HTTPS tunneling
func (p *HTTPProxy) handleConnect(w http.ResponseWriter, r *http.Request, clientAddr string) {
	// Generate connID at the beginning of the request, used throughout the request lifecycle
	connID := utils.GenerateConnID()
	logger.Info("HTTP CONNECT request started", "conn_id", connID, "target_host", r.Host, "client", clientAddr)

	// Add connID to context
	ctx := commonctx.WithConnID(r.Context(), connID)

	// Extract target host and port
	host := r.Host
	if host == "" {
		logger.Error("CONNECT request missing host", "conn_id", connID, "client", clientAddr, "url", r.URL.String())
		http.Error(w, "Missing host", http.StatusBadRequest)
		return
	}

	// Add default HTTPS port if not specified
	if !strings.Contains(host, ":") {
		host += ":443" // Default HTTPS port
		logger.Debug("Added default HTTPS port", "conn_id", connID, "original_host", r.Host, "target_host", host)
	}

	logger.Info("Processing CONNECT request", "conn_id", connID, "target_host", host, "client", clientAddr)

	// Hijack the connection first to handle raw TCP tunneling
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		logger.Error("Hijacking not supported by response writer", "conn_id", connID, "target_host", host)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	logger.Debug("Hijacking HTTP connection for tunnel", "conn_id", connID)
	clientConn, clientBuf, err := hijacker.Hijack()
	if err != nil {
		logger.Error("Failed to hijack HTTP connection", "conn_id", connID, "target_host", host, "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	defer func() {
		if err := clientConn.Close(); err != nil {
			logger.Warn("Error closing client connection", "conn_id", connID, "err", err)
		}
	}()

	logger.Debug("HTTP connection hijacked successfully", "conn_id", connID, "client", clientConn.RemoteAddr())

	// Create connection to target through the dial function
	logger.Debug("Dialing target host", "conn_id", connID, "target_host", host)
	targetConn, err := p.dialFunc(ctx, "tcp", host)

	if err != nil {
		logger.Error("Failed to connect to target host", "conn_id", connID, "target_host", host, "err", err)
		// Send error response manually since we've hijacked the connection
		if _, writeErr := clientConn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n")); writeErr != nil {
			logger.Warn("Failed to write error response to client", "conn_id", connID, "err", writeErr)
		}
		return
	}
	defer func() {
		if err := targetConn.Close(); err != nil {
			logger.Warn("Error closing target connection", "conn_id", connID, "err", err)
		}
	}()

	// Connection already established, no need to get ID from ConnWrapper again since we already have it

	logger.Debug("Connected to target host successfully", "conn_id", connID, "target_host", host, "target_addr", targetConn.RemoteAddr())

	// Send 200 Connection Established response manually
	_, err = clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	if err != nil {
		logger.Error("Failed to send CONNECT response to client", "conn_id", connID, "target_host", host, "err", err)
		return
	}
	logger.Debug("Sent CONNECT response to client", "conn_id", connID)

	// Handle any buffered data from the client
	if clientBuf != nil && clientBuf.Reader.Buffered() > 0 {
		bufferedBytes := clientBuf.Reader.Buffered()
		bufferedData := make([]byte, bufferedBytes)
		if _, readErr := clientBuf.Read(bufferedData); readErr == nil {
			if _, writeErr := targetConn.Write(bufferedData); writeErr == nil {
				logger.Debug("Forwarded buffered client data", "conn_id", connID, "bytes", bufferedBytes)
			}
		}
	}

	logger.Info("CONNECT tunnel established", "conn_id", connID, "target_host", host)

	// Start bidirectional data transfer
	go p.transfer(targetConn, clientConn, "target->client", connID)
	p.transfer(clientConn, targetConn, "client->target", connID)

	logger.Info("CONNECT tunnel closed", "conn_id", connID, "target_host", host)
}

// transfer copies data between two connections
func (p *HTTPProxy) transfer(dst, src net.Conn, direction string, connID string) {
	logger.Debug("Starting data transfer", "conn_id", connID, "direction", direction, "src_addr", src.RemoteAddr(), "dst_addr", dst.RemoteAddr())

	// Fix: Get buffer from buffer pool
	bufPtr := bufferPool.Get().(*[]byte)
	buffer := *bufPtr
	defer func() {
		// Return buffer to pool
		bufferPool.Put(bufPtr)
	}()

	totalBytes := int64(0)

	for {
		// Set read timeout to detect connection issues
		if err := src.SetReadDeadline(time.Now().Add(60 * time.Second)); err != nil {
			logger.Warn("Failed to set read deadline", "conn_id", connID, "direction", direction, "err", err)
		}

		n, err := src.Read(buffer)

		if n > 0 {
			totalBytes += int64(n)

			// Set write timeout
			if err := dst.SetWriteDeadline(time.Now().Add(60 * time.Second)); err != nil {
				logger.Warn("Failed to set write deadline", "conn_id", connID, "direction", direction, "err", err)
			}

			_, writeErr := dst.Write(buffer[:n])
			if writeErr != nil {
				logger.Error("Transfer write error", "conn_id", connID, "direction", direction, "bytes_written", n, "total_bytes", totalBytes, "err", writeErr)
				return
			}
		}

		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				// Continue on timeout to check for context cancellation
				continue
			}

			// Log connection close gracefully
			if strings.Contains(err.Error(), "use of closed network connection") ||
				strings.Contains(err.Error(), "connection reset by peer") ||
				err == io.EOF {
				logger.Debug("Connection closed during transfer", "conn_id", connID, "direction", direction, "total_bytes", totalBytes)
			} else {
				logger.Error("Transfer read error", "conn_id", connID, "direction", direction, "total_bytes", totalBytes, "err", err)
			}
			return
		}
	}
}

// handleRequest handles normal HTTP requests
func (p *HTTPProxy) handleRequest(w http.ResponseWriter, r *http.Request, clientAddr string) {
	// Generate connID at the beginning of the request, used throughout the request lifecycle
	connID := utils.GenerateConnID()
	logger.Info("HTTP request started", "conn_id", connID, "method", r.Method, "url", r.URL.String(), "client", clientAddr)

	// Add connID to context
	ctx := commonctx.WithConnID(r.Context(), connID)

	// Parse target URL
	targetURL := r.URL
	if !targetURL.IsAbs() {
		// If URL is not absolute, construct it from Host header
		scheme := "http"
		if r.TLS != nil {
			scheme = protocol.SchemeHTTPS
		}
		targetURL = &url.URL{
			Scheme:   scheme,
			Host:     r.Host,
			Path:     r.URL.Path,
			RawQuery: r.URL.RawQuery,
		}
		logger.Debug("Constructed absolute URL from relative URL", "conn_id", connID, "original_url", r.URL.String(), "target_url", targetURL.String())
	}

	logger.Info("Processing HTTP request", "conn_id", connID, "method", r.Method, "target_url", targetURL.String(), "client", clientAddr)

	// Create connection to target
	host := targetURL.Host
	if !strings.Contains(host, ":") {
		if targetURL.Scheme == protocol.SchemeHTTPS {
			host += ":443"
		} else {
			host += ":80"
		}
		logger.Debug("Added default port to host", "conn_id", connID, "original_host", targetURL.Host, "target_host", host, "scheme", targetURL.Scheme)
	}

	logger.Debug("Dialing target server", "conn_id", connID, "target_host", host)
	targetConn, err := p.dialFunc(ctx, "tcp", host)

	if err != nil {
		logger.Error("Failed to connect to target server", "conn_id", connID, "target_host", host, "err", err)
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
		return
	}
	defer func() {
		if err := targetConn.Close(); err != nil {
			logger.Warn("Error closing target connection", "conn_id", connID, "err", err)
		}
	}()

	// Connection already established, no need to get ID from ConnWrapper again since we already have it

	logger.Debug("Connected to target server successfully", "conn_id", connID, "target_host", host)

	// For HTTPS, wrap with TLS
	if targetURL.Scheme == protocol.SchemeHTTPS {
		logger.Debug("Wrapping connection with TLS", "conn_id", connID, "server_name", strings.Split(host, ":")[0])
		tlsConn := tls.Client(targetConn, &tls.Config{
			ServerName: strings.Split(host, ":")[0],
			MinVersion: tls.VersionTLS12, // Enforce minimum TLS 1.2
		})
		targetConn = tlsConn
	}

	// Remove proxy-specific headers
	r.Header.Del("Proxy-Authorization")
	r.Header.Del("Proxy-Connection")

	// Set Connection header for HTTP/1.1
	r.Header.Set("Connection", "close")

	// Write request to target server
	logger.Debug("Sending request to target server", "conn_id", connID)
	if err := r.Write(targetConn); err != nil {
		logger.Error("Failed to write request to target server", "conn_id", connID, "target_host", host, "err", err)
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
		return
	}

	// Read response from target server
	logger.Debug("Reading response from target server", "conn_id", connID)
	targetReader := bufio.NewReader(targetConn)
	response, err := http.ReadResponse(targetReader, r)

	if err != nil {
		logger.Error("Failed to read response from target server", "conn_id", connID, "target_host", host, "err", err)
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
		return
	}
	defer func() {
		if err := response.Body.Close(); err != nil {
			logger.Warn("Error closing response body", "conn_id", connID, "err", err)
		}
	}()

	logger.Debug("Response received from target server", "conn_id", connID, "status_code", response.StatusCode, "content_length", response.ContentLength)

	// Copy response headers
	for key, values := range response.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}

	// Set status code
	w.WriteHeader(response.StatusCode)

	// Copy response body
	logger.Debug("Copying response body to client", "conn_id", connID)
	bytesWritten, err := io.Copy(w, response.Body)

	if err != nil {
		logger.Error("Failed to copy response body to client", "conn_id", connID, "bytes_written", bytesWritten, "err", err)
	} else {
		logger.Debug("Response body copied successfully", "conn_id", connID, "bytes_written", bytesWritten)
	}

	logger.Info("HTTP request processing completed", "conn_id", connID, "method", r.Method, "target_url", targetURL.String(), "status_code", response.StatusCode, "bytes_written", bytesWritten)
}

// getClientIP extracts the client IP address
func getClientIP(r *http.Request) string {
	// Check X-Forwarded-For header
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		ips := strings.Split(xff, ",")
		if len(ips) > 0 {
			return strings.TrimSpace(ips[0])
		}
	}

	// Check X-Real-IP header
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}

	// Use RemoteAddr
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}

	return r.RemoteAddr
}
