package client

import (
	"crypto/tls"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/buhuipao/anyproxy/pkg/config"
)

func TestCompileHostPatterns(t *testing.T) {
	tests := []struct {
		name           string
		forbiddenHosts []string
		allowedHosts   []string
		expectErr      bool
		errorContains  string
	}{
		{
			name:           "valid patterns",
			forbiddenHosts: []string{"evil\\.com", ".*\\.bad\\.com", "192\\.168\\.1\\..*"},
			allowedHosts:   []string{"good\\.com", ".*\\.trusted\\.com", "10\\.0\\.0\\..*"},
			expectErr:      false,
		},
		{
			name:           "empty patterns",
			forbiddenHosts: []string{},
			allowedHosts:   []string{},
			expectErr:      false,
		},
		{
			name:           "invalid forbidden pattern",
			forbiddenHosts: []string{"valid.com", "[invalid regex"},
			allowedHosts:   []string{"good.com"},
			expectErr:      true,
			errorContains:  "invalid forbidden host pattern",
		},
		{
			name:           "invalid allowed pattern",
			forbiddenHosts: []string{"bad.com"},
			allowedHosts:   []string{"good.com", "[invalid regex"},
			expectErr:      true,
			errorContains:  "invalid allowed host pattern",
		},
		{
			name:           "complex valid patterns",
			forbiddenHosts: []string{"^evil\\.(com|org|net)$", ".*\\.malware\\..*"},
			allowedHosts:   []string{"^(www\\.)?example\\.(com|org)$", "localhost(:\\d+)?"},
			expectErr:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &Client{
				config: &config.ClientConfig{
					ForbiddenHosts: tt.forbiddenHosts,
					AllowedHosts:   tt.allowedHosts,
				},
			}

			err := client.compileHostPatterns()

			if (err != nil) != tt.expectErr {
				t.Errorf("compileHostPatterns() error = %v, expectErr %v", err, tt.expectErr)
			}

			if err != nil && tt.errorContains != "" {
				if !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("Expected error to contain '%s', got '%s'", tt.errorContains, err.Error())
				}
			}

			if !tt.expectErr {
				// Verify patterns were compiled
				if len(client.forbiddenHostPatterns) != len(tt.forbiddenHosts) {
					t.Errorf("Expected %d forbidden patterns, got %d",
						len(tt.forbiddenHosts), len(client.forbiddenHostPatterns))
				}

				if len(client.allowedHostPatterns) != len(tt.allowedHosts) {
					t.Errorf("Expected %d allowed patterns, got %d",
						len(tt.allowedHosts), len(client.allowedHostPatterns))
				}
			}
		})
	}
}

func TestIsConnectionAllowed(t *testing.T) {
	tests := []struct {
		name           string
		forbiddenHosts []string
		allowedHosts   []string
		address        string
		expectAllowed  bool
	}{
		{
			name:           "no restrictions",
			forbiddenHosts: []string{},
			allowedHosts:   []string{},
			address:        "example.com:80",
			expectAllowed:  true,
		},
		{
			name:           "forbidden host - exact match",
			forbiddenHosts: []string{"evil\\.com"},
			allowedHosts:   []string{},
			address:        "evil.com:80",
			expectAllowed:  false,
		},
		{
			name:           "forbidden host - pattern match",
			forbiddenHosts: []string{".*\\.evil\\.com"},
			allowedHosts:   []string{},
			address:        "subdomain.evil.com:443",
			expectAllowed:  false,
		},
		{
			name:           "allowed host - exact match",
			forbiddenHosts: []string{},
			allowedHosts:   []string{"good\\.com"},
			address:        "good.com:80",
			expectAllowed:  true,
		},
		{
			name:           "allowed host - pattern match",
			forbiddenHosts: []string{},
			allowedHosts:   []string{".*\\.trusted\\.com"},
			address:        "api.trusted.com:443",
			expectAllowed:  true,
		},
		{
			name:           "not in allowed list",
			forbiddenHosts: []string{},
			allowedHosts:   []string{"good\\.com", "trusted\\.com"},
			address:        "unknown.com:80",
			expectAllowed:  false,
		},
		{
			name:           "forbidden takes precedence",
			forbiddenHosts: []string{".*\\.evil\\.com"},
			allowedHosts:   []string{".*\\.evil\\.com"}, // Even if allowed
			address:        "sub.evil.com:80",
			expectAllowed:  false,
		},
		{
			name:           "IP address patterns",
			forbiddenHosts: []string{"192\\.168\\.1\\..*"},
			allowedHosts:   []string{"10\\.0\\.0\\..*"},
			address:        "10.0.0.5:22",
			expectAllowed:  true,
		},
		{
			name:           "complex patterns",
			forbiddenHosts: []string{".*\\.internal\\..*"},
			allowedHosts:   []string{".*\\.public\\..*"},
			address:        "api.public.example.com:443",
			expectAllowed:  true,
		},
		{
			name:           "port in pattern",
			forbiddenHosts: []string{},
			allowedHosts:   []string{"localhost:8080"},
			address:        "localhost:8080",
			expectAllowed:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &Client{
				config: &config.ClientConfig{
					ClientID:       "test-client",
					ForbiddenHosts: tt.forbiddenHosts,
					AllowedHosts:   tt.allowedHosts,
				},
			}

			// Compile patterns
			if err := client.compileHostPatterns(); err != nil {
				t.Fatalf("Failed to compile patterns: %v", err)
			}

			// Test connection
			allowed := client.isConnectionAllowed(tt.address)

			if allowed != tt.expectAllowed {
				t.Errorf("isConnectionAllowed(%s) = %v, want %v",
					tt.address, allowed, tt.expectAllowed)
			}
		})
	}
}

func TestCreateTLSConfig(t *testing.T) {
	// Create a temporary certificate file for testing
	certPEM := `-----BEGIN CERTIFICATE-----
MIICmDCCAYACCQC9DFfwPSMx9jANBgkqhkiG9w0BAQsFADAOMQwwCgYDVQQDDANm
b28wHhcNMjMwMTAxMDAwMDAwWhcNMzMwMTAxMDAwMDAwWjAOMQwwCgYDVQQDDANm
b28wggEiMA0GCSqGSIb3DQEBAQUAA4IBDwAwggEKAoIBAQDJkesre9Nb3pnMNxJl
rW4YnwbGzDQbwmsU8G9rOQQYl5xTQVHHFOOBLqRr0GcQ2549hEq2HVMfzCJJJWwL
+ODQALgc2yGqjyQNvBOP0DAGGYu6HjHvmJRFYwvbQcKnC2Z2SWBcJYCZLZ1Q2XAa
NF7HsgXmGway3xKQYjknBxEvb8xdEf/5pyJ8F2CY1GTccEkb6bjZqNBdUbVRBExs
vGwxyv9+d6ftJRLbCCVB4rMNUuHFMJFwDL3LsYJIJnYhXbjJfIuKsT0MlHHQlGpa
1PmmMq7lXHpGIuSJcCw5QJJij6LBWzHKQGSNBahlUj8MOmtfKdnjqqNWDEbjpoIB
7/07AgMBAAEwDQYJKoZIhvcNAQELBQADggEBAK7x8RXPRjcHSNSH0Wj6kx7l6Kn5
OwSGGLRbfzMOlRCaPfMxIDeXpgJsXvwmM4Nwb7jMz9tLcdnlLiNfgPh3iTJUDrKE
fFNxe4p6b/hpbHnmXc7s7Uv+qO9+alTGNxCL8G3PFnrQoeT7FdahCJFQ2sUHmWgB
aP3xKs5KphYQSK2S5Z4B7CUTqUQ5lJHxWM5Zk/Q5X3XIVQPC7sXJT6ctyNonTstu
FZPGV0nEIIKRnioZMYqJLRqVAD1Y0x1hvV8Wn4dXlDKq2JH7ukCF8qJv5XAIbpFf
kZuMN5z5SlANRxCDg1oXhRfO3P8Yq7EBRRF8CZMixMFyP+9apYqtH6qAg3w=
-----END CERTIFICATE-----`

	tmpDir := t.TempDir()
	certFile := filepath.Join(tmpDir, "test.crt")
	if err := os.WriteFile(certFile, []byte(certPEM), 0644); err != nil {
		t.Fatalf("Failed to write test certificate: %v", err)
	}

	tests := []struct {
		name           string
		gatewayAddr    string
		tlsCertPath    string
		expectErr      bool
		expectRootCAs  bool
		expectServName string
	}{
		{
			name:          "no TLS cert",
			gatewayAddr:   "gateway.example.com:8080",
			tlsCertPath:   "",
			expectErr:     false,
			expectRootCAs: false,
		},
		{
			name:           "with valid TLS cert",
			gatewayAddr:    "gateway.example.com:8080",
			tlsCertPath:    certFile,
			expectErr:      false,
			expectRootCAs:  true,
			expectServName: "gateway.example.com",
		},
		{
			name:           "gateway address with port 443",
			gatewayAddr:    "secure.gateway.com:443",
			tlsCertPath:    certFile,
			expectErr:      false,
			expectRootCAs:  true,
			expectServName: "secure.gateway.com",
		},
		{
			name:           "gateway address without port",
			gatewayAddr:    "gateway.example.com",
			tlsCertPath:    certFile,
			expectErr:      false,
			expectRootCAs:  true,
			expectServName: "gateway.example.com",
		},
		{
			name:        "non-existent cert file",
			gatewayAddr: "gateway.example.com:8080",
			tlsCertPath: "/non/existent/cert.pem",
			expectErr:   true,
		},
		{
			name:        "invalid cert content",
			gatewayAddr: "gateway.example.com:8080",
			tlsCertPath: func() string {
				invalidCert := filepath.Join(tmpDir, "invalid.crt")
				os.WriteFile(invalidCert, []byte("invalid cert content"), 0644)
				return invalidCert
			}(),
			expectErr: true,
		},
		{
			name:           "IP address as gateway",
			gatewayAddr:    "192.168.1.1:8080",
			tlsCertPath:    certFile,
			expectErr:      false,
			expectRootCAs:  true,
			expectServName: "192.168.1.1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &Client{
				config: &config.ClientConfig{
					Gateway: config.ClientGatewayConfig{
						Addr:    tt.gatewayAddr,
						TLSCert: tt.tlsCertPath,
					},
				},
			}

			tlsConfig, err := client.createTLSConfig()

			if (err != nil) != tt.expectErr {
				t.Errorf("createTLSConfig() error = %v, expectErr %v", err, tt.expectErr)
			}

			if !tt.expectErr && tlsConfig != nil {
				// Check minimum TLS version
				if tlsConfig.MinVersion != tls.VersionTLS12 {
					t.Errorf("Expected MinVersion TLS 1.2, got %v", tlsConfig.MinVersion)
				}

				// Check RootCAs
				if tt.expectRootCAs && tlsConfig.RootCAs == nil {
					t.Error("Expected RootCAs to be set, but it's nil")
				}
				if !tt.expectRootCAs && tlsConfig.RootCAs != nil {
					t.Error("Expected RootCAs to be nil, but it's set")
				}

				// Check ServerName
				if tt.expectServName != "" && tlsConfig.ServerName != tt.expectServName {
					t.Errorf("Expected ServerName %s, got %s", tt.expectServName, tlsConfig.ServerName)
				}
			}
		})
	}
}

func TestEnhancedHostPatterns(t *testing.T) {
	tests := []struct {
		name           string
		forbiddenHosts []string
		allowedHosts   []string
		address        string
		expectAllowed  bool
	}{
		// CIDR Pattern Tests
		{
			name:           "CIDR - IPv4 block allowed",
			forbiddenHosts: []string{},
			allowedHosts:   []string{"192.168.1.0/24"},
			address:        "192.168.1.10:80",
			expectAllowed:  true,
		},
		{
			name:           "CIDR - IPv4 block forbidden",
			forbiddenHosts: []string{"192.168.1.0/24"},
			allowedHosts:   []string{},
			address:        "192.168.1.10:80",
			expectAllowed:  false,
		},
		{
			name:           "CIDR - Outside IPv4 block",
			forbiddenHosts: []string{},
			allowedHosts:   []string{"192.168.1.0/24"},
			address:        "192.168.2.10:80",
			expectAllowed:  false,
		},
		{
			name:           "CIDR with port - exact match",
			forbiddenHosts: []string{},
			allowedHosts:   []string{"192.168.1.0/24:22"},
			address:        "192.168.1.10:22",
			expectAllowed:  true,
		},
		{
			name:           "CIDR with port - wrong port",
			forbiddenHosts: []string{},
			allowedHosts:   []string{"192.168.1.0/24:22"},
			address:        "192.168.1.10:80",
			expectAllowed:  false,
		},
		{
			name:           "CIDR with wildcard port",
			forbiddenHosts: []string{},
			allowedHosts:   []string{"192.168.1.0/24:*"},
			address:        "192.168.1.10:8080",
			expectAllowed:  true,
		},
		{
			name:           "CIDR - Private network block",
			forbiddenHosts: []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"},
			allowedHosts:   []string{},
			address:        "10.1.1.1:80",
			expectAllowed:  false,
		},

		// Host:Port Pattern Tests
		{
			name:           "Host:Port - exact match",
			forbiddenHosts: []string{},
			allowedHosts:   []string{"localhost:22"},
			address:        "localhost:22",
			expectAllowed:  true,
		},
		{
			name:           "Host:Port - wrong port",
			forbiddenHosts: []string{},
			allowedHosts:   []string{"localhost:22"},
			address:        "localhost:80",
			expectAllowed:  false,
		},
		{
			name:           "Host:Port - wrong host",
			forbiddenHosts: []string{},
			allowedHosts:   []string{"localhost:22"},
			address:        "example.com:22",
			expectAllowed:  false,
		},

		// Wildcard Pattern Tests
		{
			name:           "Host wildcard port",
			forbiddenHosts: []string{},
			allowedHosts:   []string{"localhost:*"},
			address:        "localhost:8080",
			expectAllowed:  true,
		},
		{
			name:           "Port wildcard host",
			forbiddenHosts: []string{},
			allowedHosts:   []string{"*:22"},
			address:        "example.com:22",
			expectAllowed:  true,
		},
		{
			name:           "Port wildcard - wrong port",
			forbiddenHosts: []string{},
			allowedHosts:   []string{"*:22"},
			address:        "example.com:80",
			expectAllowed:  false,
		},
		{
			name:           "Wildcard all",
			forbiddenHosts: []string{},
			allowedHosts:   []string{"*:*"},
			address:        "anything.com:12345",
			expectAllowed:  true,
		},
		{
			name:           "Subdomain wildcard",
			forbiddenHosts: []string{},
			allowedHosts:   []string{"*.example.com:*"},
			address:        "api.example.com:443",
			expectAllowed:  true,
		},
		{
			name:           "Subdomain wildcard - wrong domain",
			forbiddenHosts: []string{},
			allowedHosts:   []string{"*.example.com:*"},
			address:        "api.other.com:443",
			expectAllowed:  false,
		},

		// Mixed Pattern Tests
		{
			name:           "Mixed - CIDR forbidden, host:port allowed",
			forbiddenHosts: []string{"192.168.1.0/24"},
			allowedHosts:   []string{"localhost:22", "example.com:80"},
			address:        "localhost:22",
			expectAllowed:  true,
		},
		{
			name:           "Mixed - CIDR forbidden takes precedence",
			forbiddenHosts: []string{"192.168.1.0/24"},
			allowedHosts:   []string{"192.168.1.10:80"}, // Specific host in forbidden CIDR
			address:        "192.168.1.10:80",
			expectAllowed:  false,
		},

		// Backward Compatibility - Regex Pattern Tests
		{
			name:           "Regex - dot escape",
			forbiddenHosts: []string{},
			allowedHosts:   []string{"example\\.com:80"},
			address:        "example.com:80",
			expectAllowed:  true,
		},
		{
			name:           "Regex - wildcard pattern",
			forbiddenHosts: []string{},
			allowedHosts:   []string{".*\\.trusted\\.com"},
			address:        "api.trusted.com:443",
			expectAllowed:  true,
		},
		{
			name:           "Regex - complex pattern",
			forbiddenHosts: []string{"^evil\\.(com|org|net):.*$"},
			allowedHosts:   []string{},
			address:        "evil.com:80",
			expectAllowed:  false,
		},

		// Edge Cases
		{
			name:           "IPv6 address",
			forbiddenHosts: []string{},
			allowedHosts:   []string{"[::1]:*"},
			address:        "[::1]:8080",
			expectAllowed:  true,
		},
		{
			name:           "Address without port",
			forbiddenHosts: []string{},
			allowedHosts:   []string{"localhost:*"},
			address:        "localhost",
			expectAllowed:  true,
		},
		{
			name:           "High port number",
			forbiddenHosts: []string{},
			allowedHosts:   []string{"*:65535"},
			address:        "example.com:65535",
			expectAllowed:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &Client{
				config: &config.ClientConfig{
					ClientID:       "test-client",
					ForbiddenHosts: tt.forbiddenHosts,
					AllowedHosts:   tt.allowedHosts,
				},
			}

			// Compile patterns
			if err := client.compileHostPatterns(); err != nil {
				t.Fatalf("Failed to compile patterns: %v", err)
			}

			// Test connection
			allowed := client.isConnectionAllowed(tt.address)

			if allowed != tt.expectAllowed {
				t.Errorf("isConnectionAllowed(%s) = %v, want %v",
					tt.address, allowed, tt.expectAllowed)
			}
		})
	}
}

func TestCompileHostPatternTypes(t *testing.T) {
	tests := []struct {
		name          string
		pattern       string
		expectedType  string
		expectErr     bool
		errorContains string
	}{
		// CIDR Patterns
		{
			name:         "Valid IPv4 CIDR",
			pattern:      "192.168.1.0/24",
			expectedType: "cidr",
			expectErr:    false,
		},
		{
			name:         "Valid IPv6 CIDR",
			pattern:      "2001:db8::/32",
			expectedType: "cidr",
			expectErr:    false,
		},
		{
			name:         "CIDR with port",
			pattern:      "192.168.1.0/24:22",
			expectedType: "cidr",
			expectErr:    false,
		},
		{
			name:         "CIDR with wildcard port",
			pattern:      "192.168.1.0/24:*",
			expectedType: "cidr",
			expectErr:    false,
		},
		{
			name:          "Invalid CIDR",
			pattern:       "192.168.1.0/99",
			expectedType:  "",
			expectErr:     true,
			errorContains: "invalid CIDR notation",
		},

		// Host:Port Patterns
		{
			name:         "Host with port",
			pattern:      "localhost:22",
			expectedType: "host_port",
			expectErr:    false,
		},
		{
			name:         "Host with wildcard port",
			pattern:      "localhost:*",
			expectedType: "host_wildcard",
			expectErr:    false,
		},
		{
			name:          "Host with invalid port",
			pattern:       "localhost:99999",
			expectedType:  "",
			expectErr:     true,
			errorContains: "invalid port number",
		},
		{
			name:          "Host with non-numeric port",
			pattern:       "localhost:abc",
			expectedType:  "",
			expectErr:     true,
			errorContains: "invalid port number",
		},

		// Wildcard Patterns
		{
			name:         "Wildcard host with port",
			pattern:      "*:80",
			expectedType: "port_wildcard",
			expectErr:    false,
		},
		{
			name:         "Wildcard all",
			pattern:      "*:*",
			expectedType: "wildcard_all",
			expectErr:    false,
		},
		{
			name:         "Subdomain wildcard",
			pattern:      "*.example.com:*",
			expectedType: "regex",
			expectErr:    false,
		},

		// Regex Patterns (backward compatibility)
		{
			name:         "Regex with dots",
			pattern:      "example\\.com",
			expectedType: "regex",
			expectErr:    false,
		},
		{
			name:         "Regex with brackets",
			pattern:      "example\\.(com|org)",
			expectedType: "regex",
			expectErr:    false,
		},
		{
			name:          "Invalid regex",
			pattern:       "[invalid regex",
			expectedType:  "",
			expectErr:     true,
			errorContains: "invalid regex pattern",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pattern, err := compileHostPattern(tt.pattern)

			if (err != nil) != tt.expectErr {
				t.Errorf("compileHostPattern() error = %v, expectErr %v", err, tt.expectErr)
			}

			if err != nil && tt.errorContains != "" {
				if !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("Expected error to contain '%s', got '%s'", tt.errorContains, err.Error())
				}
			}

			if !tt.expectErr {
				if pattern.Type != tt.expectedType {
					t.Errorf("Expected pattern type '%s', got '%s'", tt.expectedType, pattern.Type)
				}
				if pattern.Original != tt.pattern {
					t.Errorf("Expected original pattern '%s', got '%s'", tt.pattern, pattern.Original)
				}
			}
		})
	}
}
