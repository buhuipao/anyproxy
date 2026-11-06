// Package main implements the AnyProxy client application.
// This client supports multi-transport protocols (WebSocket, gRPC, QUIC).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/buhuipao/anyproxy/pkg/client"
	"github.com/buhuipao/anyproxy/pkg/common/monitoring"
	"github.com/buhuipao/anyproxy/pkg/config"
	"github.com/buhuipao/anyproxy/pkg/logger"
	clientWeb "github.com/buhuipao/anyproxy/web/client"
)

func main() {
	// Parse command-line flags
	configFile := flag.String("config", "configs/config.yaml", "Path to the configuration file")
	serverURL := flag.String("server", "", "Remote server URL for fetching configuration")
	token := flag.String("token", "", "Authentication token for remote configuration")
	flag.Parse()

	var cfg *config.Config
	var err error

	// Load configuration first (without logging, since logger is not initialized yet)
	if *serverURL != "" && *token != "" {
		cfg, err = fetchRemoteConfig(*serverURL, *token)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to fetch remote configuration: %v\n", err)
			os.Exit(1)
		}
	} else {
		cfg, err = config.LoadConfig(*configFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to load configuration: %v\n", err)
			os.Exit(1)
		}
	}

	// Initialize logger FIRST before any logging calls
	if err := logger.Init(&cfg.Log); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize logger: %v\n", err)
		os.Exit(1)
	}

	// Now we can use logger - log the config source
	if *serverURL != "" && *token != "" {
		logger.Info("Configuration fetched from remote server", "url", *serverURL)
	} else {
		logger.Info("Configuration loaded from file", "file", *configFile)
	}

	// Validate configuration (additional validation with clear error messages)
	if err := cfg.Validate(); err != nil {
		logger.Error("Configuration validation failed", "err", err)
		os.Exit(1)
	}

	// Log configuration summary
	logger.Info("Configuration validated successfully",
		"client_id", cfg.Client.ClientID,
		"group_id", cfg.Client.GroupID,
		"gateway_addr", cfg.Client.Gateway.Addr,
		"transport_type", cfg.Client.Gateway.TransportType,
		"replicas", cfg.Client.Replicas,
		"open_ports", len(cfg.Client.OpenPorts),
		"log_level", cfg.Log.Level,
	)

	// 🆕 Start monitoring cleanup process
	monitoring.StartCleanupProcess()
	logger.Info("Monitoring cleanup process started")

	// Initialize web services if enabled
	var webServer *clientWeb.WebServer
	if cfg.Client.Web.Enabled {
		// Create web server
		webServer = clientWeb.NewClientWebServer(cfg.Client.Web.ListenAddr, cfg.Client.Web.StaticDir, cfg.Client.ClientID)

		// Configure authentication if enabled
		if cfg.Client.Web.AuthEnabled {
			webServer.SetAuth(cfg.Client.Web.AuthEnabled, cfg.Client.Web.AuthUsername, cfg.Client.Web.AuthPassword)
		}

		// Set configurations for clash profile generation
		webServer.SetConfigurations(cfg)

		// Start web server in a separate goroutine
		go func() {
			if err := webServer.Start(); err != nil {
				logger.Error("Client web server failed", "err", err)
			}
		}()

		logger.Info("Client web server started", "listen_addr", cfg.Client.Web.ListenAddr, "auth_enabled", cfg.Client.Web.AuthEnabled)
	}

	var clients []*client.Client
	for i := 0; i < cfg.Client.Replicas; i++ {
		// Create and start client using the transport type from client gateway config
		// Fix: Pass replica index i to ensure each client has unique ID
		proxyClient, err := client.NewClient(&cfg.Client, cfg.Client.Gateway.TransportType, i)
		if err != nil {
			logger.Error("Failed to create client", "replica_idx", i, "err", err)
			os.Exit(1)
		}

		// 🆕 Set web server reference in client for ID updates
		if webServer != nil {
			proxyClient.SetWebServer(webServer)
		}

		// Start client (non-blocking)
		if err := proxyClient.Start(); err != nil {
			logger.Error("Failed to start client", "err", err)
			os.Exit(1)
		}

		clients = append(clients, proxyClient)
	}

	// Log active mode
	if len(cfg.Client.OpenPorts) > 0 {
		logger.Info("Client Mode: Premium (Direct Port Mapping Active)", "mapped_ports_count", len(cfg.Client.OpenPorts))
		for _, p := range cfg.Client.OpenPorts {
			logger.Info("Port Mapping Active", "remote", p.RemotePort, "local", p.LocalPort, "protocol", p.Protocol)
		}
	} else {
		logger.Info("Client Mode: Standard (Tunnel Proxy Only)", "gateway_addr", cfg.Client.Gateway.Addr)
	}

	logger.Info("Started clients", "count", cfg.Client.Replicas, "gateway_addr", cfg.Client.Gateway.Addr)

	// Handle signals for graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Wait for termination signal
	<-sigCh
	logger.Info("Shutting down...")

	// Stop web server if running
	if webServer != nil {
		if err := webServer.Stop(); err != nil {
			logger.Error("Error shutting down web server", "err", err)
		}
	}

	// Stop all clients concurrently
	var stopWg sync.WaitGroup
	for _, proxyClient := range clients {
		stopWg.Add(1)
		go func(c *client.Client) {
			defer stopWg.Done()
			if err := c.Stop(); err != nil {
				logger.Error("Error shutting down client", "err", err)
			}
		}(proxyClient)
	}

	// Wait for all clients to stop
	stopWg.Wait()
	logger.Info("All clients stopped")

	// Close logger to flush any buffered logs
	if err := logger.Close(); err != nil {
		// Use fmt since logger is being closed
		fmt.Fprintf(os.Stderr, "Error closing logger: %v\n", err)
	}
}

// fetchRemoteConfig fetches configuration from remote server
func fetchRemoteConfig(serverURL, token string) (*config.Config, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	// Trim trailing slash from server URL if present
	if len(serverURL) > 0 && serverURL[len(serverURL)-1] == '/' {
		serverURL = serverURL[:len(serverURL)-1]
	}
	url := fmt.Sprintf("%s/api/v1/client/config", serverURL)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %v", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("server error (status %d): %s", resp.StatusCode, string(body))
	}

	// Define a wrapper struct to match common API response format
	type APIResponse struct {
		Code int           `json:"code"`
		Msg  string        `json:"msg"`
		Data config.Config `json:"data"`
	}

	var apiResp APIResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %v", err)
	}

	if apiResp.Code != 0 && apiResp.Code != 200 {
		return nil, fmt.Errorf("api error (%d): %s", apiResp.Code, apiResp.Msg)
	}

	return &apiResp.Data, nil
}
