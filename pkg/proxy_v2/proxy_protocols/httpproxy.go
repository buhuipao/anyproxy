package proxy_protocols

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"encoding/base64"

	"crypto/tls"

	"github.com/buhuipao/anyproxy/pkg/config"
	"github.com/buhuipao/anyproxy/pkg/proxy_v2/common"
)

// HTTPProxy HTTP proxy implementation (based on v1 design)
type HTTPProxy struct {
	config         *config.HTTPConfig
	server         *http.Server
	dialFunc       func(ctx context.Context, network, addr string) (net.Conn, error)
	groupExtractor func(string) string
}

// NewHTTPProxyWithAuth creates a new HTTP proxy with authentication (same as v1)
func NewHTTPProxyWithAuth(config *config.HTTPConfig, dialFn func(context.Context, string, string) (net.Conn, error), groupExtractor func(string) string) (common.GatewayProxy, error) {
	slog.Info("Creating HTTP proxy",
		"listen_addr", config.ListenAddr,
		"auth_enabled", config.AuthUsername != "")

	proxy := &HTTPProxy{
		config:         config,
		dialFunc:       dialFn,
		groupExtractor: groupExtractor,
	}

	// 🚨 Fix: Don't use ServeMux as it can't handle CONNECT requests properly (consistent with v1)
	// Don't use ServeMux as it doesn't handle CONNECT requests properly
	proxy.server = &http.Server{
		Addr:    config.ListenAddr,
		Handler: proxy, // Use proxy itself directly as handler
		// Same timeout configuration as v1
		ReadTimeout:  60 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	slog.Info("HTTP proxy created successfully", "listen_addr", config.ListenAddr)
	return proxy, nil
}

// ServeHTTP implements http.Handler interface (🆕 migrated from v1)
// Enables HTTPProxy to serve directly as HTTP server handler, avoiding ServeMux CONNECT issues
func (p *HTTPProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.handleHTTP(w, r)
}

// Start starts the HTTP proxy server (same as v1)
func (p *HTTPProxy) Start() error {
	slog.Info("Starting HTTP proxy server", "listen_addr", p.config.ListenAddr)

	go func() {
		if err := p.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("HTTP proxy server error",
				"listen_addr", p.config.ListenAddr,
				"error", err)
		} else {
			slog.Info("HTTP proxy server stopped")
		}
	}()

	slog.Info("HTTP proxy server started successfully", "listen_addr", p.config.ListenAddr)
	return nil
}

// Stop stops the HTTP proxy server (same as v1)
func (p *HTTPProxy) Stop() error {
	slog.Info("Stopping HTTP proxy server", "listen_addr", p.config.ListenAddr)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := p.server.Shutdown(ctx)
	if err != nil {
		slog.Error("Error stopping HTTP proxy server",
			"listen_addr", p.config.ListenAddr,
			"error", err)
	} else {
		slog.Info("HTTP proxy server stopped successfully")
	}

	return err
}

// GetListenAddr returns the listen address (same as v1)
func (p *HTTPProxy) GetListenAddr() string {
	return p.config.ListenAddr
}

// handleHTTP handles HTTP requests (based on v1 logic)
func (p *HTTPProxy) handleHTTP(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	clientAddr := getClientIP(r)

	slog.Debug("HTTP request received",
		"method", r.Method,
		"url", r.URL.String(),
		"client_addr", clientAddr,
		"user_agent", r.Header.Get("User-Agent"))

	// Authentication check (🆕 Fix: Follow v1 logic completely)
	var userCtx *common.UserContext
	if p.config.AuthUsername != "" && p.config.AuthPassword != "" {
		slog.Debug("Authentication required, checking credentials",
			"client_addr", clientAddr)

		username, _, authenticated := p.authenticateAndExtractUser(r)
		if !authenticated {
			slog.Warn("HTTP proxy authentication failed",
				"client_addr", clientAddr,
				"method", r.Method,
				"host", r.Host)
			w.Header().Set("Proxy-Authenticate", "Basic realm=\"Proxy\"")
			http.Error(w, "Proxy Authentication Required", http.StatusProxyAuthRequired)
			return
		}

		// Extract group ID (same as v1)
		groupID := ""
		if p.groupExtractor != nil {
			groupID = p.groupExtractor(username)
			slog.Debug("Extracted group ID from username",
				"username", username,
				"group_id", groupID)
		}

		// Set user context (same as v1)
		userCtx = &common.UserContext{
			Username: username,
			GroupID:  groupID,
		}

		slog.Debug("HTTP proxy authentication successful",
			"username", username,
			"group_id", groupID,
			"client_addr", clientAddr)
	} else {
		slog.Debug("No authentication required")
	}

	// Set user context to request
	if userCtx != nil {
		ctx := context.WithValue(r.Context(), "user", userCtx)
		r = r.WithContext(ctx)
	}

	// Handle CONNECT method (same as v1)
	if r.Method == http.MethodConnect {
		slog.Info("Handling HTTPS CONNECT request",
			"target_host", r.Host,
			"client_addr", clientAddr,
			"username", func() string {
				if userCtx != nil {
					return userCtx.Username
				}
				return ""
			}())
		p.handleConnect(w, r, clientAddr, startTime)
		return
	}

	// Handle normal HTTP requests (same as v1)
	slog.Info("Handling HTTP request",
		"method", r.Method,
		"url", r.URL.String(),
		"client_addr", clientAddr,
		"username", func() string {
			if userCtx != nil {
				return userCtx.Username
			}
			return ""
		}())
	p.handleRequest(w, r, clientAddr, startTime)
}

// authenticateAndExtractUser checks proxy authentication and returns username, password, and auth status (🆕 complete migration from v1)
func (p *HTTPProxy) authenticateAndExtractUser(r *http.Request) (string, string, bool) {
	slog.Debug("Checking proxy authentication",
		"remote_addr", r.RemoteAddr,
		"method", r.Method,
		"host", r.Host)

	auth := r.Header.Get("Proxy-Authorization")
	if auth == "" {
		slog.Debug("No proxy authorization header found",
			"remote_addr", r.RemoteAddr)
		return "", "", false
	}

	// Parse Basic authentication
	const prefix = "Basic "
	if !strings.HasPrefix(auth, prefix) {
		slog.Warn("Invalid proxy authorization header format",
			"remote_addr", r.RemoteAddr,
			"auth_type", strings.SplitN(auth, " ", 2)[0])
		return "", "", false
	}

	decoded, err := base64.StdEncoding.DecodeString(auth[len(prefix):])
	slog.Debug("Decoded proxy authorization header",
		"remote_addr", r.RemoteAddr,
		"decoded", string(decoded),
		"error", err)
	if err != nil {
		slog.Warn("Failed to decode proxy authorization header",
			"remote_addr", r.RemoteAddr,
			"error", err)
		return "", "", false
	}

	credentials := string(decoded)
	parts := strings.SplitN(credentials, ":", 2)
	if len(parts) != 2 {
		slog.Warn("Invalid credentials format in proxy authorization",
			"remote_addr", r.RemoteAddr)
		return "", "", false
	}

	username, password := parts[0], parts[1]

	// Extract the base username (without group_id) for authentication
	baseUsername := extractBaseUsername(username)

	// Authenticate using the base username and provided password
	authenticated := baseUsername == p.config.AuthUsername && password == p.config.AuthPassword

	if authenticated {
		slog.Debug("Proxy authentication successful",
			"remote_addr", r.RemoteAddr,
			"username", username,
			"base_username", baseUsername)
	} else {
		slog.Warn("Proxy authentication failed",
			"remote_addr", r.RemoteAddr,
			"username", username,
			"base_username", baseUsername)
	}

	return username, password, authenticated
}

// handleConnect handles CONNECT requests for HTTPS tunneling (based on v1 logic)
func (p *HTTPProxy) handleConnect(w http.ResponseWriter, r *http.Request, clientAddr string, startTime time.Time) {
	requestID := fmt.Sprintf("%d", time.Now().UnixNano())

	// Extract target host and port (same as v1)
	host := r.Host
	if host == "" {
		slog.Error("CONNECT request missing host",
			"request_id", requestID,
			"client_addr", clientAddr,
			"url", r.URL.String())
		http.Error(w, "Missing host", http.StatusBadRequest)
		return
	}

	// Add default HTTPS port if not specified (same as v1)
	if !strings.Contains(host, ":") {
		host += ":443" // Default HTTPS port
		slog.Debug("Added default HTTPS port",
			"request_id", requestID,
			"original_host", r.Host,
			"target_host", host)
	}

	slog.Info("Processing CONNECT request",
		"request_id", requestID,
		"target_host", host,
		"client_addr", clientAddr)

	// Hijack the connection first to handle raw TCP tunneling (same as v1)
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		slog.Error("Hijacking not supported by response writer",
			"request_id", requestID,
			"target_host", host)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	slog.Debug("Hijacking HTTP connection for tunnel", "request_id", requestID)
	clientConn, clientBuf, err := hijacker.Hijack()
	if err != nil {
		slog.Error("Failed to hijack HTTP connection",
			"request_id", requestID,
			"target_host", host,
			"error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	defer clientConn.Close()

	slog.Debug("HTTP connection hijacked successfully",
		"request_id", requestID,
		"client_addr", clientConn.RemoteAddr())

	// Create connection to target through the dial function (same as v1)
	slog.Debug("Dialing target host",
		"request_id", requestID,
		"target_host", host)
	dialStart := time.Now()
	targetConn, err := p.dialFunc(r.Context(), "tcp", host)
	dialDuration := time.Since(dialStart)

	if err != nil {
		slog.Error("Failed to connect to target host",
			"request_id", requestID,
			"target_host", host,
			"dial_duration", dialDuration,
			"error", err)
		// Send error response manually since we've hijacked the connection (same as v1)
		clientConn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		return
	}
	defer targetConn.Close()

	slog.Debug("Connected to target host successfully",
		"request_id", requestID,
		"target_host", host,
		"dial_duration", dialDuration,
		"target_addr", targetConn.RemoteAddr())

	// Send 200 Connection Established response manually (same as v1)
	_, err = clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	if err != nil {
		slog.Error("Failed to send CONNECT response to client",
			"request_id", requestID,
			"target_host", host,
			"error", err)
		return
	}
	slog.Debug("Sent CONNECT response to client", "request_id", requestID)

	// Handle any buffered data from the client (same as v1)
	if clientBuf != nil && clientBuf.Reader.Buffered() > 0 {
		bufferedBytes := clientBuf.Reader.Buffered()
		bufferedData := make([]byte, bufferedBytes)
		clientBuf.Reader.Read(bufferedData)
		targetConn.Write(bufferedData)
		slog.Debug("Forwarded buffered client data",
			"request_id", requestID,
			"bytes", bufferedBytes)
	}

	setupDuration := time.Since(startTime)
	slog.Info("CONNECT tunnel established",
		"request_id", requestID,
		"target_host", host,
		"setup_duration", setupDuration,
		"dial_duration", dialDuration)

	// Start bidirectional data transfer (same as v1)
	transferStart := time.Now()
	go p.transfer(targetConn, clientConn, "target->client", requestID)
	p.transfer(clientConn, targetConn, "client->target", requestID)

	transferDuration := time.Since(transferStart)
	totalDuration := time.Since(startTime)
	slog.Info("CONNECT tunnel closed",
		"request_id", requestID,
		"target_host", host,
		"transfer_duration", transferDuration,
		"total_duration", totalDuration)
}

// transfer copies data between two connections (🆕 migrated from v1)
func (p *HTTPProxy) transfer(dst, src net.Conn, direction string, requestID string) {
	slog.Debug("Starting data transfer",
		"request_id", requestID,
		"direction", direction,
		"src_addr", src.RemoteAddr(),
		"dst_addr", dst.RemoteAddr())

	buffer := make([]byte, 32*1024) // 32KB buffer
	totalBytes := int64(0)
	transferCount := 0
	startTime := time.Now()

	defer func() {
		elapsed := time.Since(startTime)
		slog.Debug("Data transfer completed",
			"request_id", requestID,
			"direction", direction,
			"total_bytes", totalBytes,
			"transfer_count", transferCount,
			"duration", elapsed)
	}()

	for {
		// Set read timeout to detect connection issues
		src.SetReadDeadline(time.Now().Add(60 * time.Second))

		n, err := src.Read(buffer)
		transferCount++

		if n > 0 {
			totalBytes += int64(n)

			// Log progress for large transfers
			if totalBytes%1000000 == 0 { // Every 1MB
				slog.Debug("Transfer progress",
					"request_id", requestID,
					"direction", direction,
					"bytes_transferred", totalBytes)
			}

			// Set write timeout
			dst.SetWriteDeadline(time.Now().Add(60 * time.Second))

			_, writeErr := dst.Write(buffer[:n])
			if writeErr != nil {
				slog.Error("Transfer write error",
					"request_id", requestID,
					"direction", direction,
					"bytes_written", n,
					"total_bytes", totalBytes,
					"error", writeErr)
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
				slog.Debug("Connection closed during transfer",
					"request_id", requestID,
					"direction", direction,
					"total_bytes", totalBytes,
					"transfer_count", transferCount)
			} else {
				slog.Error("Transfer read error",
					"request_id", requestID,
					"direction", direction,
					"total_bytes", totalBytes,
					"transfer_count", transferCount,
					"error", err)
			}
			return
		}
	}
}

// handleRequest handles normal HTTP requests (based on v1 logic)
func (p *HTTPProxy) handleRequest(w http.ResponseWriter, r *http.Request, clientAddr string, startTime time.Time) {
	requestID := fmt.Sprintf("%d", time.Now().UnixNano())

	// Parse target URL (same as v1)
	targetURL := r.URL
	if !targetURL.IsAbs() {
		// If URL is not absolute, construct it from Host header (same as v1)
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		targetURL = &url.URL{
			Scheme:   scheme,
			Host:     r.Host,
			Path:     r.URL.Path,
			RawQuery: r.URL.RawQuery,
		}
		slog.Debug("Constructed absolute URL from relative URL",
			"request_id", requestID,
			"original_url", r.URL.String(),
			"target_url", targetURL.String())
	}

	slog.Info("Processing HTTP request",
		"request_id", requestID,
		"method", r.Method,
		"target_url", targetURL.String(),
		"client_addr", clientAddr)

	// Create connection to target (same as v1)
	host := targetURL.Host
	if !strings.Contains(host, ":") {
		if targetURL.Scheme == "https" {
			host += ":443"
		} else {
			host += ":80"
		}
		slog.Debug("Added default port to host",
			"request_id", requestID,
			"original_host", targetURL.Host,
			"target_host", host,
			"scheme", targetURL.Scheme)
	}

	slog.Debug("Dialing target server",
		"request_id", requestID,
		"target_host", host)
	dialStart := time.Now()
	targetConn, err := p.dialFunc(r.Context(), "tcp", host)
	dialDuration := time.Since(dialStart)

	if err != nil {
		slog.Error("Failed to connect to target server",
			"request_id", requestID,
			"target_host", host,
			"dial_duration", dialDuration,
			"error", err)
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
		return
	}
	defer targetConn.Close()

	slog.Debug("Connected to target server successfully",
		"request_id", requestID,
		"target_host", host,
		"dial_duration", dialDuration)

	// For HTTPS, wrap with TLS (same as v1)
	if targetURL.Scheme == "https" {
		slog.Debug("Wrapping connection with TLS",
			"request_id", requestID,
			"server_name", strings.Split(host, ":")[0])
		tlsConn := tls.Client(targetConn, &tls.Config{
			ServerName: strings.Split(host, ":")[0],
		})
		targetConn = tlsConn
	}

	// Remove proxy-specific headers (🆕 migrated from v1)
	originalProxyAuth := r.Header.Get("Proxy-Authorization")
	originalProxyConn := r.Header.Get("Proxy-Connection")
	r.Header.Del("Proxy-Authorization")
	r.Header.Del("Proxy-Connection")

	// Set Connection header for HTTP/1.1 (same as v1)
	r.Header.Set("Connection", "close")

	slog.Debug("Modified request headers for forwarding",
		"request_id", requestID,
		"removed_proxy_auth", originalProxyAuth != "",
		"removed_proxy_conn", originalProxyConn != "")

	// Write request to target server (same as v1)
	slog.Debug("Sending request to target server", "request_id", requestID)
	writeStart := time.Now()
	if err := r.Write(targetConn); err != nil {
		writeDuration := time.Since(writeStart)
		slog.Error("Failed to write request to target server",
			"request_id", requestID,
			"target_host", host,
			"write_duration", writeDuration,
			"error", err)
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
		return
	}
	writeDuration := time.Since(writeStart)
	slog.Debug("Request sent to target server",
		"request_id", requestID,
		"write_duration", writeDuration)

	// Read response from target server (same as v1)
	slog.Debug("Reading response from target server", "request_id", requestID)
	targetReader := bufio.NewReader(targetConn)
	readStart := time.Now()
	response, err := http.ReadResponse(targetReader, r)
	readDuration := time.Since(readStart)

	if err != nil {
		slog.Error("Failed to read response from target server",
			"request_id", requestID,
			"target_host", host,
			"read_duration", readDuration,
			"error", err)
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()

	slog.Debug("Response received from target server",
		"request_id", requestID,
		"status_code", response.StatusCode,
		"content_length", response.ContentLength,
		"read_duration", readDuration)

	// Copy response headers (same as v1)
	headerCount := 0
	for key, values := range response.Header {
		for _, value := range values {
			w.Header().Add(key, value)
			headerCount++
		}
	}
	slog.Debug("Copied response headers",
		"request_id", requestID,
		"header_count", headerCount)

	// Set status code (same as v1)
	w.WriteHeader(response.StatusCode)

	// Copy response body (same as v1)
	slog.Debug("Copying response body to client", "request_id", requestID)
	copyStart := time.Now()
	bytesWritten, err := io.Copy(w, response.Body)
	copyDuration := time.Since(copyStart)

	if err != nil {
		slog.Error("Failed to copy response body to client",
			"request_id", requestID,
			"bytes_written", bytesWritten,
			"copy_duration", copyDuration,
			"error", err)
	} else {
		slog.Debug("Response body copied successfully",
			"request_id", requestID,
			"bytes_written", bytesWritten,
			"copy_duration", copyDuration)
	}

	totalDuration := time.Since(startTime)
	slog.Info("HTTP request processing completed",
		"request_id", requestID,
		"method", r.Method,
		"target_url", targetURL.String(),
		"status_code", response.StatusCode,
		"bytes_written", bytesWritten,
		"total_duration", totalDuration,
		"dial_duration", dialDuration,
		"write_duration", writeDuration,
		"read_duration", readDuration,
		"copy_duration", copyDuration)
}

// getClientIP extracts the client IP address (same as v1)
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
