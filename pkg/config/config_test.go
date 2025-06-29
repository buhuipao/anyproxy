package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfig(t *testing.T) {
	tests := []struct {
		name        string
		configYAML  string
		wantErr     bool
		expectedCfg *Config
	}{
		{
			name: "valid complete config",
			configYAML: `
gateway:
  listen_addr: "0.0.0.0:8443"
  tls_cert: "/path/to/cert.pem"
  tls_key: "/path/to/key.pem"
  auth_username: "gatewayuser"
  auth_password: "gatewaypass"
  proxy:
    socks5:
      listen_addr: "127.0.0.1:1080"
    http:
      listen_addr: "127.0.0.1:8080"
client:
  id: "test-client-id"
  group_id: "test-group"
  group_password: "clientpass"
  replicas: 3
  gateway:
    addr: "gateway.example.com:8443"
    tls_cert: "/path/to/gateway-cert.pem"
    auth_username: "clientuser"
    auth_password: "clientpass"
  forbidden_hosts:
    - "forbidden.example.com"
    - "blocked.test"
`,
			wantErr: false,
			expectedCfg: &Config{
				Gateway: GatewayConfig{
					ListenAddr:   "0.0.0.0:8443",
					TLSCert:      "/path/to/cert.pem",
					TLSKey:       "/path/to/key.pem",
					AuthUsername: "gatewayuser",
					AuthPassword: "gatewaypass",
					Proxy: ProxyConfig{
						SOCKS5: SOCKS5Config{
							ListenAddr: "127.0.0.1:1080",
						},
						HTTP: HTTPConfig{
							ListenAddr: "127.0.0.1:8080",
						},
					},
				},
				Client: ClientConfig{
					ClientID:      "test-client-id",
					GroupID:       "test-group",
					GroupPassword: "clientpass",
					Replicas:      3,
					Gateway: ClientGatewayConfig{
						Addr:         "gateway.example.com:8443",
						TLSCert:      "/path/to/gateway-cert.pem",
						AuthUsername: "clientuser",
						AuthPassword: "clientpass",
					},
					ForbiddenHosts: []string{"forbidden.example.com", "blocked.test"},
				},
			},
		},
		{
			name: "minimal config",
			configYAML: `
gateway:
  listen_addr: "0.0.0.0:8443"
  auth_username: "admin"
  auth_password: "secret"
  proxy:
    http:
      listen_addr: "127.0.0.1:8080"
client:
  id: "minimal-client"
  group_id: "default"
  group_password: "pass"
  gateway:
    addr: "localhost:8443"
    auth_username: "admin"
    auth_password: "secret"
`,
			wantErr: false,
			expectedCfg: &Config{
				Gateway: GatewayConfig{
					ListenAddr:   "0.0.0.0:8443",
					AuthUsername: "admin",
					AuthPassword: "secret",
					Proxy: ProxyConfig{
						HTTP: HTTPConfig{
							ListenAddr: "127.0.0.1:8080",
						},
					},
				},
				Client: ClientConfig{
					ClientID:      "minimal-client",
					GroupID:       "default",
					GroupPassword: "pass",
					Gateway: ClientGatewayConfig{
						Addr:         "localhost:8443",
						AuthUsername: "admin",
						AuthPassword: "secret",
					},
				},
			},
		},
		{
			name:        "empty config",
			configYAML:  ``,
			wantErr:     false,
			expectedCfg: &Config{},
		},
		{
			name: "config with only logging",
			configYAML: `
log:
  level: "debug"
  format: "text"
  output: "stderr"
  max_size: 50
  max_backups: 5
  max_age: 7
  compress: false
`,
			wantErr: false,
			expectedCfg: &Config{
				Log: LogConfig{
					Level:      "debug",
					Format:     "text",
					Output:     "stderr",
					MaxSize:    50,
					MaxBackups: 5,
					MaxAge:     7,
					Compress:   false,
				},
			},
		},
		{
			name: "invalid YAML",
			configYAML: `
proxy:
  socks5:
    listen_addr: "127.0.0.1:1080"
    invalid_indent_here
gateway:
  listen_addr: "0.0.0.0:8443"
`,
			wantErr: true,
		},
		{
			name:       "invalid YAML structure",
			configYAML: `[this is not a valid config structure]`,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temporary file with test YAML content
			tmpDir := t.TempDir()
			configFile := filepath.Join(tmpDir, "config.yaml")

			err := os.WriteFile(configFile, []byte(tt.configYAML), 0600)
			require.NoError(t, err)

			// Test LoadConfig
			cfg, err := LoadConfig(configFile)

			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, cfg)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, cfg)
				assert.Equal(t, tt.expectedCfg, cfg)
			}
		})
	}
}

func TestLoadConfig_FileNotFound(t *testing.T) {
	// Test with non-existent file
	cfg, err := LoadConfig("non-existent-file.yaml")
	assert.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), "no such file or directory")
}

func TestLoadConfig_EmptyFilename(t *testing.T) {
	// Test with empty filename
	cfg, err := LoadConfig("")
	assert.Error(t, err)
	assert.Nil(t, cfg)
}

func TestLoadConfig_Directory(t *testing.T) {
	// Test with directory instead of file
	tmpDir := t.TempDir()
	cfg, err := LoadConfig(tmpDir)
	assert.Error(t, err)
	assert.Nil(t, cfg)
}

func TestLoadConfig_PermissionDenied(t *testing.T) {
	// Skip this test on Windows as it behaves differently with file permissions
	if os.Getenv("GOOS") == "windows" {
		t.Skip("Skipping permission test on Windows")
	}

	// Create a file without read permissions
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "config.yaml")

	err := os.WriteFile(configFile, []byte("test: config"), 0000)
	require.NoError(t, err)

	cfg, err := LoadConfig(configFile)
	assert.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), "permission denied")
}

func TestGetConfig(t *testing.T) {
	// Reset global config
	conf = nil

	// Test GetConfig when no config is loaded
	cfg := GetConfig()
	assert.Nil(t, cfg)

	// Load a config
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "config.yaml")
	configYAML := `
log:
  level: "debug"
  format: "json"
`

	err := os.WriteFile(configFile, []byte(configYAML), 0600)
	require.NoError(t, err)

	loadedCfg, err := LoadConfig(configFile)
	require.NoError(t, err)
	require.NotNil(t, loadedCfg)

	// Test GetConfig returns the loaded config
	cfg = GetConfig()
	assert.NotNil(t, cfg)
	assert.Equal(t, loadedCfg, cfg)
	assert.Equal(t, "debug", cfg.Log.Level)
	assert.Equal(t, "json", cfg.Log.Format)

	// Load another config and verify GetConfig returns the new one
	newConfigYAML := `
log:
  level: "error"
  format: "text"
gateway:
  listen_addr: "127.0.0.1:9999"
`

	newConfigFile := filepath.Join(tmpDir, "new_config.yaml")
	err = os.WriteFile(newConfigFile, []byte(newConfigYAML), 0600)
	require.NoError(t, err)

	newLoadedCfg, err := LoadConfig(newConfigFile)
	require.NoError(t, err)
	require.NotNil(t, newLoadedCfg)

	// Verify GetConfig now returns the new config
	cfg = GetConfig()
	assert.NotNil(t, cfg)
	assert.Equal(t, newLoadedCfg, cfg)
	assert.Equal(t, "error", cfg.Log.Level)
	assert.Equal(t, "text", cfg.Log.Format)
	assert.Equal(t, "127.0.0.1:9999", cfg.Gateway.ListenAddr)
}

func TestConfigStructs(t *testing.T) {
	// Test that all config structs can be instantiated
	t.Run("Config instantiation", func(t *testing.T) {
		cfg := &Config{
			Gateway: GatewayConfig{
				ListenAddr:   "0.0.0.0:8443",
				TLSCert:      "/cert.pem",
				TLSKey:       "/key.pem",
				AuthUsername: "gw",
				AuthPassword: "gwpass",
				Proxy: ProxyConfig{
					SOCKS5: SOCKS5Config{
						ListenAddr: "127.0.0.1:1080",
					},
					HTTP: HTTPConfig{
						ListenAddr: "127.0.0.1:8080",
					},
				},
			},
			Client: ClientConfig{
				ClientID:      "client1",
				GroupID:       "group1",
				GroupPassword: "pass",
				Replicas:      2,
				Gateway: ClientGatewayConfig{
					Addr:         "gw.example.com:8443",
					TLSCert:      "/gw-cert.pem",
					AuthUsername: "user",
					AuthPassword: "pass",
				},
				ForbiddenHosts: []string{"blocked.com"},
				AllowedHosts:   []string{"allowed.com"},
			},
		}

		if cfg.Gateway.ListenAddr != "0.0.0.0:8443" {
			t.Errorf("Gateway.ListenAddr = %s, want 0.0.0.0:8443", cfg.Gateway.ListenAddr)
		}
		if cfg.Client.ClientID != "client1" {
			t.Errorf("Client.ClientID = %s, want client1", cfg.Client.ClientID)
		}
		if cfg.Gateway.Proxy.HTTP.ListenAddr != "127.0.0.1:8080" {
			t.Errorf("Gateway.Proxy.HTTP.ListenAddr = %s, want 127.0.0.1:8080", cfg.Gateway.Proxy.HTTP.ListenAddr)
		}
		if cfg.Client.Gateway.Addr != "gw.example.com:8443" {
			t.Errorf("Client.Gateway.Addr = %s, want gw.example.com:8443", cfg.Client.Gateway.Addr)
		}
	})

	t.Run("Zero values", func(t *testing.T) {
		cfg := &Config{}
		assert.Equal(t, "", cfg.Gateway.Proxy.SOCKS5.ListenAddr)
		assert.Equal(t, "", cfg.Gateway.ListenAddr)
		assert.Equal(t, "", cfg.Client.Gateway.Addr)
		assert.Equal(t, "", cfg.Log.Level)
		assert.Equal(t, 0, cfg.Client.Replicas)
		assert.Nil(t, cfg.Client.ForbiddenHosts)
		assert.Nil(t, cfg.Client.AllowedHosts)
		assert.False(t, cfg.Log.Compress)
	})
}

func TestConfigYAMLTags(t *testing.T) {
	// Test that YAML tags work correctly by loading and comparing configs
	configYAML := `
gateway:
  listen_addr: "test:8443"
  tls_cert: "test.crt"
  tls_key: "test.key"
  auth_username: "gwuser"
  auth_password: "gwpass"
  proxy:
    socks5:
      listen_addr: "test:1080"
    http:
      listen_addr: "test:8080"
client:
  id: "test-client"
  group_id: "test-group"
  group_password: "clientpass"
  replicas: 5
  gateway:
    addr: "gw.test:8443"
    tls_cert: "gw.crt"
    auth_username: "clientuser"
    auth_password: "clientpass"
  forbidden_hosts: ["bad1.com", "bad2.com"]
  allowed_hosts: ["good1.com", "good2.com"]
log:
  level: "debug"
  format: "text"
  output: "file"
  file: "test.log"
  max_size: 200
  max_backups: 10
  max_age: 60
  compress: true
`

	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "test_config.yaml")

	err := os.WriteFile(configFile, []byte(configYAML), 0600)
	require.NoError(t, err)

	cfg, err := LoadConfig(configFile)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Verify all fields are loaded correctly
	assert.Equal(t, "test:1080", cfg.Gateway.Proxy.SOCKS5.ListenAddr)
	assert.Equal(t, "test:8080", cfg.Gateway.Proxy.HTTP.ListenAddr)

	assert.Equal(t, "test:8443", cfg.Gateway.ListenAddr)
	assert.Equal(t, "test.crt", cfg.Gateway.TLSCert)
	assert.Equal(t, "test.key", cfg.Gateway.TLSKey)
	assert.Equal(t, "gwuser", cfg.Gateway.AuthUsername)
	assert.Equal(t, "gwpass", cfg.Gateway.AuthPassword)

	assert.Equal(t, "gw.test:8443", cfg.Client.Gateway.Addr)
	assert.Equal(t, "gw.crt", cfg.Client.Gateway.TLSCert)
	assert.Equal(t, "test-client", cfg.Client.ClientID)
	assert.Equal(t, "test-group", cfg.Client.GroupID)
	assert.Equal(t, "clientpass", cfg.Client.GroupPassword)
	assert.Equal(t, 5, cfg.Client.Replicas)
	assert.Equal(t, "clientuser", cfg.Client.Gateway.AuthUsername)
	assert.Equal(t, "clientpass", cfg.Client.Gateway.AuthPassword)
	assert.Equal(t, []string{"bad1.com", "bad2.com"}, cfg.Client.ForbiddenHosts)
	assert.Equal(t, []string{"good1.com", "good2.com"}, cfg.Client.AllowedHosts)

	assert.Equal(t, "debug", cfg.Log.Level)
	assert.Equal(t, "text", cfg.Log.Format)
	assert.Equal(t, "file", cfg.Log.Output)
	assert.Equal(t, "test.log", cfg.Log.File)
	assert.Equal(t, 200, cfg.Log.MaxSize)
	assert.Equal(t, 10, cfg.Log.MaxBackups)
	assert.Equal(t, 60, cfg.Log.MaxAge)
	assert.True(t, cfg.Log.Compress)
}

// TestConcurrentAccess tests that GetConfig is safe for concurrent access
func TestConcurrentAccess(t *testing.T) {
	// Reset global config
	conf = nil

	// Load initial config
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "config.yaml")
	configYAML := `
log:
  level: "info"
`

	err := os.WriteFile(configFile, []byte(configYAML), 0600)
	require.NoError(t, err)

	_, err = LoadConfig(configFile)
	require.NoError(t, err)

	// Test concurrent access to GetConfig
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func() {
			defer func() { done <- true }()
			for j := 0; j < 100; j++ {
				cfg := GetConfig()
				assert.NotNil(t, cfg)
				assert.Equal(t, "info", cfg.Log.Level)
			}
		}()
	}

	// Wait for all goroutines to complete
	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		config  Config
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid client config",
			config: Config{
				Client: ClientConfig{
					ClientID:      "test-client",
					GroupID:       "test-group",
					GroupPassword: "test-password",
				},
			},
			wantErr: false,
		},
		{
			name: "empty config (no client ID)",
			config: Config{
				Client: ClientConfig{
					ClientID:      "",
					GroupID:       "",
					GroupPassword: "",
				},
			},
			wantErr: false, // Should pass since ClientID is empty
		},
		{
			name: "client with empty group ID",
			config: Config{
				Client: ClientConfig{
					ClientID:      "test-client",
					GroupID:       "",
					GroupPassword: "test-password",
				},
			},
			wantErr: true,
			errMsg:  "client group_id cannot be empty",
		},
		{
			name: "client with empty group password",
			config: Config{
				Client: ClientConfig{
					ClientID:      "test-client",
					GroupID:       "test-group",
					GroupPassword: "",
				},
			},
			wantErr: true,
			errMsg:  "client group_password cannot be empty",
		},
		{
			name: "client with both group fields empty",
			config: Config{
				Client: ClientConfig{
					ClientID:      "test-client",
					GroupID:       "",
					GroupPassword: "",
				},
			},
			wantErr: true,
			errMsg:  "client group_id cannot be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Config.Validate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && err.Error() != tt.errMsg {
				t.Errorf("Config.Validate() error message = %v, want %v", err.Error(), tt.errMsg)
			}
		})
	}
}
