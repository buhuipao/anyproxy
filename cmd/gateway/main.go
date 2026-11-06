// Package main implements the AnyProxy gateway server application.
// This gateway supports multi-transport protocols (WebSocket, gRPC, QUIC).
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/buhuipao/anyproxy/pkg/common/monitoring"
	"github.com/buhuipao/anyproxy/pkg/config"
	"github.com/buhuipao/anyproxy/pkg/gateway"
	"github.com/buhuipao/anyproxy/pkg/logger"
	gatewayWeb "github.com/buhuipao/anyproxy/web/gateway"
)

func main() {
	// Parse command-line flags
	configFile := flag.String("config", "configs/config.yaml", "Path to the configuration file")
	flag.Parse()

	// Load configuration first (without logging, since logger is not initialized yet)
	cfg, err := config.LoadConfig(*configFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load configuration: %v\n", err)
		os.Exit(1)
	}

	// Initialize logger FIRST before any logging calls
	if err := logger.Init(&cfg.Log); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize logger: %v\n", err)
		os.Exit(1)
	}

	// Now we can use logger
	logger.Info("Configuration loaded", "file", *configFile)

	// Validate configuration (additional validation with clear error messages)
	if err := cfg.Validate(); err != nil {
		logger.Error("Configuration validation failed", "err", err)
		os.Exit(1)
	}

	// Log configuration summary
	logger.Info("Configuration validated successfully",
		"listen_addr", cfg.Gateway.ListenAddr,
		"transport_type", cfg.Gateway.TransportType,
		"http_proxy", cfg.Gateway.Proxy.HTTP.ListenAddr != "",
		"socks5_proxy", cfg.Gateway.Proxy.SOCKS5.ListenAddr != "",
		"tuic_proxy", cfg.Gateway.Proxy.TUIC.ListenAddr != "",
		"tls_enabled", cfg.Gateway.TLSCert != "",
		"log_level", cfg.Log.Level,
	)

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
		// Create web server
		webServer = gatewayWeb.NewGatewayWebServer(cfg.Gateway.Web.ListenAddr, cfg.Gateway.Web.StaticDir)

		// Configure authentication; the credentials also guard the platform API
		// even when the console login is disabled
		webServer.SetAuth(cfg.Gateway.Web.AuthEnabled, cfg.Gateway.Web.AuthUsername, cfg.Gateway.Web.AuthPassword)

		// Set credential manager for SaaS API
		webServer.SetCredentialManager(gw.GetCredentialManager())

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

	// Close logger to flush any buffered logs
	if err := logger.Close(); err != nil {
		// Use fmt since logger is being closed
		fmt.Fprintf(os.Stderr, "Error closing logger: %v\n", err)
	}
}
