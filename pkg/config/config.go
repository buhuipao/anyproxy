// Package config provides configuration management for AnyProxy.
// It supports loading configuration from YAML files and provides
// structured configuration types for all components.
package config

import (
	"os"

	"gopkg.in/yaml.v2"
)

// Config represents the main configuration
type Config struct {
	Proxy     ProxyConfig     `yaml:"proxy"`
	Transport TransportConfig `yaml:"transport"`
	Gateway   GatewayConfig   `yaml:"gateway"`
	Client    ClientConfig    `yaml:"client"`
	Log       LogConfig       `yaml:"log"`
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
	ListenAddr   string    `yaml:"listen_addr"`
	TLSCert      string    `yaml:"tls_cert"`
	TLSKey       string    `yaml:"tls_key"`
	AuthUsername string    `yaml:"auth_username"`
	AuthPassword string    `yaml:"auth_password"`
	Web          WebConfig `yaml:"web"`
}

// SOCKS5Config represents the configuration for the SOCKS5 proxy
type SOCKS5Config struct {
	ListenAddr   string `yaml:"listen_addr"`
	AuthUsername string `yaml:"auth_username"`
	AuthPassword string `yaml:"auth_password"`
}

// TransportConfig represents the configuration for the transport layer.
// It specifies which transport protocol to use (websocket, grpc, or quic).
type TransportConfig struct {
	Type string `yaml:"type"`
}

// HTTPConfig represents the configuration for the HTTP proxy
type HTTPConfig struct {
	ListenAddr   string `yaml:"listen_addr"`
	AuthUsername string `yaml:"auth_username"`
	AuthPassword string `yaml:"auth_password"`
}

// TUICConfig represents the configuration for the TUIC proxy
type TUICConfig struct {
	ListenAddr string `yaml:"listen_addr"`
	Token      string `yaml:"token"`     // TUIC token for authentication
	UUID       string `yaml:"uuid"`      // TUIC UUID for client identification
	CertFile   string `yaml:"cert_file"` // TLS certificate file (TUIC requires TLS)
	KeyFile    string `yaml:"key_file"`  // TLS private key file
}

// ServiceLimit defines allowed services for the client
type ServiceLimit struct {
	Name     string `yaml:"name"`
	Addr     string `yaml:"addr"`
	Protocol string `yaml:"protocol"` // "tcp" or "udp"
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
	GatewayAddr    string     `yaml:"gateway_addr"`
	GatewayTLSCert string     `yaml:"gateway_tls_cert"`
	ClientID       string     `yaml:"client_id"`
	GroupID        string     `yaml:"group_id"`
	Replicas       int        `yaml:"replicas"`
	AuthUsername   string     `yaml:"auth_username"`
	AuthPassword   string     `yaml:"auth_password"`
	ForbiddenHosts []string   `yaml:"forbidden_hosts"`
	AllowedHosts   []string   `yaml:"allowed_hosts"`
	OpenPorts      []OpenPort `yaml:"open_ports"`
	Web            WebConfig  `yaml:"web"`
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

	conf = &config

	return &config, nil
}

// GetConfig returns the global configuration
func GetConfig() *Config {
	return conf
}
