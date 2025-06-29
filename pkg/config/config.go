// Package config provides configuration management for AnyProxy.
// It supports loading configuration from YAML files and provides
// structured configuration types for all components.
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v2"
)

// Config represents the main configuration
type Config struct {
	Log     LogConfig     `yaml:"log"`
	Gateway GatewayConfig `yaml:"gateway"`
	Client  ClientConfig  `yaml:"client"`
}

// LogConfig represents the logging configuration
type LogConfig struct {
	Level      string `yaml:"level"`       // debug, info, warn, error
	Format     string `yaml:"format"`      // text, json
	Output     string `yaml:"output"`      // stdout, stderr, file path
	File       string `yaml:"file"`        // log file path when output is file
	MaxSize    int    `yaml:"max_size"`    // maximum size in MB before rotation
	MaxBackups int    `yaml:"max_backups"` // maximum number of old log files to retain
	MaxAge     int    `yaml:"max_age"`     // maximum number of days to retain old log files
	Compress   bool   `yaml:"compress"`    // whether to compress rotated log files
}

// ProxyConfig represents the configuration for the proxy
type ProxyConfig struct {
	SOCKS5 SOCKS5Config `yaml:"socks5"`
	HTTP   HTTPConfig   `yaml:"http"`
	TUIC   TUICConfig   `yaml:"tuic"`
}

// GatewayConfig represents the configuration for the proxy gateway
type GatewayConfig struct {
	ListenAddr    string      `yaml:"listen_addr"`
	TransportType string      `yaml:"transport_type"`
	TLSCert       string      `yaml:"tls_cert"`
	TLSKey        string      `yaml:"tls_key"`
	AuthUsername  string      `yaml:"auth_username"`
	AuthPassword  string      `yaml:"auth_password"`
	Proxy         ProxyConfig `yaml:"proxy"`
	Web           WebConfig   `yaml:"web"`
}

// SOCKS5Config represents the configuration for the SOCKS5 proxy
type SOCKS5Config struct {
	ListenAddr string `yaml:"listen_addr"`
}

// HTTPConfig represents the configuration for the HTTP proxy
type HTTPConfig struct {
	ListenAddr string `yaml:"listen_addr"`
}

// TUICConfig represents the configuration for the TUIC proxy
// Note: TUIC now uses group_id as UUID and password as token dynamically
// TLS certificates are reused from Gateway configuration
type TUICConfig struct {
	ListenAddr string `yaml:"listen_addr"`
}

// OpenPort defines a port forwarding configuration
type OpenPort struct {
	RemotePort int    `yaml:"remote_port"` // Port to open on the gateway
	LocalPort  int    `yaml:"local_port"`  // Port to forward to on the client side
	LocalHost  string `yaml:"local_host"`  // Host to forward to on the client side
	Protocol   string `yaml:"protocol"`    // "tcp" or "udp"
}

// ClientConfig represents the configuration for the proxy client
type ClientConfig struct {
	ClientID       string              `yaml:"id"`
	GroupID        string              `yaml:"group_id"`
	GroupPassword  string              `yaml:"group_password"`
	Replicas       int                 `yaml:"replicas"`
	Gateway        ClientGatewayConfig `yaml:"gateway"`
	ForbiddenHosts []string            `yaml:"forbidden_hosts"`
	AllowedHosts   []string            `yaml:"allowed_hosts"`
	OpenPorts      []OpenPort          `yaml:"open_ports"`
	Web            WebConfig           `yaml:"web"`
}

// ClientGatewayConfig represents the gateway connection configuration for the client
type ClientGatewayConfig struct {
	Addr          string `yaml:"addr"`
	TransportType string `yaml:"transport_type"`
	TLSCert       string `yaml:"tls_cert"`
	AuthUsername  string `yaml:"auth_username"`
	AuthPassword  string `yaml:"auth_password"`
}

// WebConfig represents the configuration for the web management interface
type WebConfig struct {
	Enabled    bool   `yaml:"enabled"`
	ListenAddr string `yaml:"listen_addr"`
	StaticDir  string `yaml:"static_dir"`
	// Authentication settings
	AuthEnabled  bool   `yaml:"auth_enabled"`
	AuthUsername string `yaml:"auth_username"`
	AuthPassword string `yaml:"auth_password"`
	SessionKey   string `yaml:"session_key"`
}

var conf *Config

// LoadConfig loads configuration from a YAML file
func LoadConfig(filename string) (*Config, error) {
	data, err := os.ReadFile(filename) // nolint:gosec // Config file path is provided by user via command line
	if err != nil {
		return nil, err
	}

	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, err
	}

	// Validate configuration
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("configuration validation failed: %v", err)
	}

	conf = &config

	return &config, nil
}

// GetConfig returns the global configuration
func GetConfig() *Config {
	return conf
}

// Validate validates the configuration
func (c *Config) Validate() error {
	// Only validate client configuration if client ID is set (indicating client usage)
	if c.Client.ClientID != "" {
		if c.Client.GroupID == "" {
			return fmt.Errorf("client group_id cannot be empty")
		}

		if c.Client.GroupPassword == "" {
			return fmt.Errorf("client group_password cannot be empty")
		}
	}

	return nil
}
