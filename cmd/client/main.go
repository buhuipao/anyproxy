// Package main implements the AnyProxy client application.
// This client supports multi-transport protocols (WebSocket, gRPC, QUIC).
package main

import (
	"flag"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/buhuipao/anyproxy/pkg/client"
	"github.com/buhuipao/anyproxy/pkg/common/monitoring"
	"github.com/buhuipao/anyproxy/pkg/common/ratelimit"
	"github.com/buhuipao/anyproxy/pkg/config"
	"github.com/buhuipao/anyproxy/pkg/logger"
	clientWeb "github.com/buhuipao/anyproxy/web/client"
)

func main() {
	// Parse command-line flags
	configFile := flag.String("config", "configs/config.yaml", "Path to the configuration file")
	flag.Parse()

	// Load configuration
	cfg, err := config.LoadConfig(*configFile)
	if err != nil {
		logger.Error("Failed to load configuration", "err", err)
		os.Exit(1)
	}

	// Initialize logger
	if err := logger.Init(&cfg.Log); err != nil {
		logger.Error("Failed to initialize logger", "err", err)
		os.Exit(1)
	}

	// 🆕 Start monitoring cleanup process
	monitoring.StartCleanupProcess()
	logger.Info("Monitoring cleanup process started")

	// Initialize web services if enabled
	var webServer *clientWeb.WebServer
	if cfg.Client.Web.Enabled {
		// Initialize rate limiter (without storage)
		rateLimiter := ratelimit.NewRateLimiter(nil)

		// Create web server
		webServer = clientWeb.NewClientWebServer(cfg.Client.Web.ListenAddr, cfg.Client.Web.StaticDir, cfg.Client.ClientID, rateLimiter)

		// Configure authentication if enabled
		if cfg.Client.Web.AuthEnabled {
			webServer.SetAuth(cfg.Client.Web.AuthEnabled, cfg.Client.Web.AuthUsername, cfg.Client.Web.AuthPassword)
		}

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
}
