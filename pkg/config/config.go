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
	Log     LogConfig     `yaml:"log" json:"log"`
	Gateway GatewayConfig `yaml:"gateway" json:"gateway"`
	Client  ClientConfig  `yaml:"client" json:"client"`
}

// LogConfig represents the logging configuration
type LogConfig struct {
	Level      string `yaml:"level" json:"level"`             // debug, info, warn, error
	Format     string `yaml:"format" json:"format"`           // text, json
	Output     string `yaml:"output" json:"output"`           // stdout, stderr, file path
	File       string `yaml:"file" json:"file"`               // log file path when output is file
	MaxSize    int    `yaml:"max_size" json:"max_size"`       // maximum size in MB before rotation
	MaxBackups int    `yaml:"max_backups" json:"max_backups"` // maximum number of old log files to retain
	MaxAge     int    `yaml:"max_age" json:"max_age"`         // maximum number of days to retain old log files
	Compress   bool   `yaml:"compress" json:"compress"`       // whether to compress rotated log files
}

// ProxyConfig represents the configuration for the proxy
type ProxyConfig struct {
	SOCKS5 SOCKS5Config `yaml:"socks5"`
	HTTP   HTTPConfig   `yaml:"http"`
	TUIC   TUICConfig   `yaml:"tuic"`
}

// CredentialConfig represents the credential storage configuration
type CredentialConfig struct {
	Type     string              `yaml:"type"`      // "memory", "file", or "db"
	FilePath string              `yaml:"file_path"` // Only used for file type
	DB       *CredentialDBConfig `yaml:"db"`        // Only used for db type
}

// CredentialDBConfig represents database configuration for credential storage
type CredentialDBConfig struct {
	Driver     string `yaml:"driver"`      // Database driver: mysql, postgres, sqlite
	DataSource string `yaml:"data_source"` // Connection string
	TableName  string `yaml:"table_name"`  // Table name for credentials (optional, defaults to "credentials")
}

// GatewayConfig represents the configuration for the proxy gateway
type GatewayConfig struct {
	ListenAddr    string            `yaml:"listen_addr"`
	TransportType string            `yaml:"transport_type"`
	TLSCert       string            `yaml:"tls_cert"`
	TLSKey        string            `yaml:"tls_key"`
	AuthUsername  string            `yaml:"auth_username"`
	AuthPassword  string            `yaml:"auth_password"`
	Credential    *CredentialConfig `yaml:"credential"` // Add credential configuration
	Proxy         ProxyConfig       `yaml:"proxy"`
	Web           WebConfig         `yaml:"web"`
}

// SOCKS5Config represents the configuration for the SOCKS5 proxy
type SOCKS5Config struct {
	ListenAddr string `yaml:"listen_addr"`
}

// HTTPConfig represents the configuration for the HTTP proxy
type HTTPConfig struct {
	ListenAddr string `yaml:"listen_addr"`
	TLSCert    string `yaml:"tls_cert"` // Path to TLS certificate file for HTTPS proxy
	TLSKey     string `yaml:"tls_key"`  // Path to TLS key file for HTTPS proxy
}

// TUICConfig represents the configuration for the TUIC v5 proxy
// Note: TUIC uses group_id as UUID and password as token dynamically
// TLS certificates are reused from Gateway configuration
// This configuration is compatible with Clash Meta and sing-box clients
type TUICConfig struct {
	ListenAddr string `yaml:"listen_addr"`

	// TUIC v5 standard options (matching sing-box/Clash Meta config)
	// CongestionControl specifies the congestion control algorithm
	// Options: "bbr" (recommended for gaming), "cubic", "new_reno"
	CongestionControl string `yaml:"congestion_control"`

	// UDPRelayMode specifies how UDP packets are relayed
	// "native": QUIC Datagram - low latency, may lose packets (for gaming)
	// "quic": QUIC Stream - reliable, higher latency
	UDPRelayMode string `yaml:"udp_relay_mode"`

	// ZeroRTT enables 0-RTT handshake for faster connection establishment
	// Warning: vulnerable to replay attacks, use with caution
	ZeroRTT bool `yaml:"zero_rtt"`

	// ALPN specifies Application-Layer Protocol Negotiation protocols
	// Default: ["h3"] to blend with HTTP/3 traffic
	ALPN []string `yaml:"alpn"`

	// MaxIdleTimeout is the idle time in seconds after which a TUIC UDP association, or one destination inside it, is closed
	// Default: 30 seconds
	MaxIdleTimeout int `yaml:"max_idle_timeout"`

	// AuthTimeout is the maximum time in seconds to wait for authentication
	// Default: 3 seconds
	AuthTimeout int `yaml:"auth_timeout"`

	// Heartbeat interval in seconds to keep connection alive
	// Default: 10 seconds
	Heartbeat int `yaml:"heartbeat"`
}

// GetCongestionControl returns the congestion control algorithm with default
func (c *TUICConfig) GetCongestionControl() string {
	if c.CongestionControl == "" {
		return "bbr" // Default to BBR for better gaming performance
	}
	return c.CongestionControl
}

// GetUDPRelayMode returns the UDP relay mode with default
func (c *TUICConfig) GetUDPRelayMode() string {
	if c.UDPRelayMode == "" {
		return "native" // Default to native for low latency gaming
	}
	return c.UDPRelayMode
}

// GetALPN returns the ALPN protocols with default
func (c *TUICConfig) GetALPN() []string {
	if len(c.ALPN) == 0 {
		return []string{"h3"} // Default to h3 to blend with HTTP/3
	}
	return c.ALPN
}

// GetMaxIdleTimeout returns the max idle timeout with default
func (c *TUICConfig) GetMaxIdleTimeout() int {
	if c.MaxIdleTimeout <= 0 {
		return 30 // Default 30 seconds
	}
	return c.MaxIdleTimeout
}

// GetAuthTimeout returns the auth timeout with default
func (c *TUICConfig) GetAuthTimeout() int {
	if c.AuthTimeout <= 0 {
		return 3 // Default 3 seconds
	}
	return c.AuthTimeout
}

// GetHeartbeat returns the heartbeat interval with default
func (c *TUICConfig) GetHeartbeat() int {
	if c.Heartbeat <= 0 {
		return 10 // Default 10 seconds
	}
	return c.Heartbeat
}

// OpenPort defines a port forwarding configuration
type OpenPort struct {
	RemotePort int    `yaml:"remote_port" json:"remote_port"` // Port to open on the gateway
	LocalPort  int    `yaml:"local_port" json:"local_port"`   // Port to forward to on the client side
	LocalHost  string `yaml:"local_host" json:"local_host"`   // Host to forward to on the client side
	Protocol   string `yaml:"protocol" json:"protocol"`       // "tcp" or "udp"
}

// ClientConfig represents the configuration for the proxy client
type ClientConfig struct {
	ClientID       string              `yaml:"id" json:"id"`
	GroupID        string              `yaml:"group_id" json:"group_id"`
	GroupPassword  string              `yaml:"group_password" json:"group_password"`
	Replicas       int                 `yaml:"replicas" json:"replicas"`
	Gateway        ClientGatewayConfig `yaml:"gateway" json:"gateway"`
	AllowIPv6      bool                `yaml:"allow_ipv6" json:"allow_ipv6"` // Dial IPv6 targets too; default false resolves and dials IPv4 only
	ForbiddenHosts []string            `yaml:"forbidden_hosts" json:"forbidden_hosts"`
	AllowedHosts   []string            `yaml:"allowed_hosts" json:"allowed_hosts"`
	OpenPorts      []OpenPort          `yaml:"open_ports" json:"open_ports"`
	Web            WebConfig           `yaml:"web" json:"web"`
}

// ClientGatewayConfig represents the gateway connection configuration for the client
type ClientGatewayConfig struct {
	Addr          string `yaml:"addr" json:"addr"`
	TransportType string `yaml:"transport_type" json:"transport_type"`
	TLSCert       string `yaml:"tls_cert" json:"tls_cert"`
	AuthUsername  string `yaml:"auth_username" json:"auth_username"`
	AuthPassword  string `yaml:"auth_password" json:"auth_password"`
}

// WebConfig represents the configuration for the web management interface
type WebConfig struct {
	Enabled    bool   `yaml:"enabled" json:"enabled"`
	ListenAddr string `yaml:"listen_addr" json:"listen_addr"`
	StaticDir  string `yaml:"static_dir" json:"static_dir"`
	// Authentication settings
	AuthEnabled  bool   `yaml:"auth_enabled" json:"auth_enabled"`
	AuthUsername string `yaml:"auth_username" json:"auth_username"`
	AuthPassword string `yaml:"auth_password" json:"auth_password"`
	SessionKey   string `yaml:"session_key" json:"session_key"`
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

		// Note: group_password is required; the gateway rejects a client without it
		// for every credential store type
	}

	return nil
}
