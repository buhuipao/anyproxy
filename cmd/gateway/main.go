// Package main implements the AnyProxy gateway server application.
// This gateway supports multi-transport protocols (WebSocket, gRPC, QUIC).
package main

import (
	"flag"
	"os"
	"os/signal"
	"syscall"

	"github.com/buhuipao/anyproxy/pkg/common/monitoring"
	"github.com/buhuipao/anyproxy/pkg/common/ratelimit"
	"github.com/buhuipao/anyproxy/pkg/config"
	"github.com/buhuipao/anyproxy/pkg/gateway"
	"github.com/buhuipao/anyproxy/pkg/logger"
	gatewayWeb "github.com/buhuipao/anyproxy/web/gateway"
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

	// Validate configuration (additional validation with clear error messages)
	if err := cfg.Validate(); err != nil {
		logger.Error("Configuration validation failed", "err", err)
		os.Exit(1)
	}

	// Initialize logger
	if err := logger.Init(&cfg.Log); err != nil {
		logger.Error("Failed to initialize logger", "err", err)
		os.Exit(1)
	}

	// Create and start gateway (using WebSocket transport layer)
	gw, err := gateway.NewGateway(cfg, cfg.Gateway.TransportType)
	if err != nil {
		logger.Error("Failed to create gateway", "err", err)
		os.Exit(1)
	}

	// 🆕 Start monitoring cleanup process
	monitoring.StartCleanupProcess()
	logger.Info("Monitoring cleanup process started")

	// Initialize web services if enabled
	var webServer *gatewayWeb.WebServer
	if cfg.Gateway.Web.Enabled {
		// Initialize rate limiter (without storage)
		rateLimiter := ratelimit.NewRateLimiter(nil)

		// Create web server
		webServer = gatewayWeb.NewGatewayWebServer(cfg.Gateway.Web.ListenAddr, cfg.Gateway.Web.StaticDir, rateLimiter)

		// Configure authentication if enabled
		if cfg.Gateway.Web.AuthEnabled {
			webServer.SetAuth(true, cfg.Gateway.Web.AuthUsername, cfg.Gateway.Web.AuthPassword)
		}

		// Start web server in a separate goroutine
		go func() {
			if err := webServer.Start(); err != nil {
				logger.Error("Web server failed", "err", err)
			}
		}()

		logger.Info("Gateway web server started", "listen_addr", cfg.Gateway.Web.ListenAddr, "auth_enabled", cfg.Gateway.Web.AuthEnabled)
	}

	// Handle signals for graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Start gateway in a separate goroutine
	go func() {
		if err := gw.Start(); err != nil {
			logger.Error("Gateway failed", "err", err)
			os.Exit(1)
		}
	}()

	logger.Info("Gateway started", "listen_addr", cfg.Gateway.ListenAddr)

	// Wait for termination signal
	<-sigCh
	logger.Info("Shutting down...")

	// Stop web server if running
	if webServer != nil {
		if err := webServer.Stop(); err != nil {
			logger.Error("Error shutting down web server", "err", err)
		}
	}

	// Stop gateway
	if err := gw.Stop(); err != nil {
		logger.Error("Error shutting down gateway", "err", err)
	}

	logger.Info("Gateway stopped")
}
