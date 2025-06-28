package client

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/buhuipao/anyproxy/pkg/logger"
)

// HostPattern represents a compiled host pattern for matching
type HostPattern struct {
	Type     string         // "regex", "cidr", "host_port", "host_wildcard", "port_wildcard"
	Regex    *regexp.Regexp // For regex patterns
	Network  *net.IPNet     // For CIDR patterns
	Host     string         // For host patterns
	Port     int            // For specific port patterns (-1 for wildcard)
	Original string         // Original pattern string for logging
}

// compileHostPatterns pre-compiles all host patterns with enhanced support for CIDR and port matching
func (c *Client) compileHostPatterns() error {
	// Compile forbidden hosts patterns
	c.forbiddenHostPatterns = make([]*HostPattern, 0, len(c.config.ForbiddenHosts))
	for _, pattern := range c.config.ForbiddenHosts {
		compiled, err := compileHostPattern(pattern)
		if err != nil {
			return fmt.Errorf("invalid forbidden host pattern '%s': %v", pattern, err)
		}
		c.forbiddenHostPatterns = append(c.forbiddenHostPatterns, compiled)
	}

	// Compile allowed hosts patterns
	c.allowedHostPatterns = make([]*HostPattern, 0, len(c.config.AllowedHosts))
	for _, pattern := range c.config.AllowedHosts {
		compiled, err := compileHostPattern(pattern)
		if err != nil {
			return fmt.Errorf("invalid allowed host pattern '%s': %v", pattern, err)
		}
		c.allowedHostPatterns = append(c.allowedHostPatterns, compiled)
	}

	return nil
}

// compileHostPattern compiles a single host pattern with support for CIDR, port matching, and regex
func compileHostPattern(pattern string) (*HostPattern, error) {
	original := pattern

	// Check for CIDR notation first
	if strings.Contains(pattern, "/") {
		return compileCIDRPattern(pattern, original)
	}

	// Check for wildcard patterns before host:port (wildcards take precedence)
	if strings.Contains(pattern, "*") && !isRegexPattern(pattern) {
		return compileWildcardPattern(pattern, original)
	}

	// Check for host:port patterns
	if strings.Contains(pattern, ":") && !isRegexPattern(pattern) {
		return compileHostPortPattern(pattern, original)
	}

	// Default to regex pattern for backward compatibility
	return compileRegexPattern(pattern, original)
}

// compileCIDRPattern compiles CIDR patterns like "192.168.1.0/24" or "192.168.1.0/24:22"
func compileCIDRPattern(pattern, original string) (*HostPattern, error) {
	// Check if CIDR has port specification
	if colonIndex := strings.LastIndex(pattern, ":"); colonIndex != -1 {
		// Verify it's not part of IPv6 address
		if beforeColon := pattern[:colonIndex]; strings.Contains(beforeColon, "/") {
			cidrPart := beforeColon
			portPart := pattern[colonIndex+1:]

			// Parse CIDR
			_, network, err := net.ParseCIDR(cidrPart)
			if err != nil {
				return nil, fmt.Errorf("invalid CIDR notation: %v", err)
			}

			// Parse port
			if portPart == "*" {
				return &HostPattern{
					Type:     "cidr",
					Network:  network,
					Port:     -1, // wildcard port
					Original: original,
				}, nil
			}

			port, err := strconv.Atoi(portPart)
			if err != nil || port < 1 || port > 65535 {
				return nil, fmt.Errorf("invalid port number: %s", portPart)
			}

			return &HostPattern{
				Type:     "cidr",
				Network:  network,
				Port:     port,
				Original: original,
			}, nil
		}
	}

	// Simple CIDR without port
	_, network, err := net.ParseCIDR(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid CIDR notation: %v", err)
	}

	return &HostPattern{
		Type:     "cidr",
		Network:  network,
		Port:     -1, // no port restriction
		Original: original,
	}, nil
}

// compileHostPortPattern compiles host:port patterns like "localhost:22", "example.com:80"
func compileHostPortPattern(pattern, original string) (*HostPattern, error) {
	parts := strings.Split(pattern, ":")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid host:port pattern")
	}

	host := parts[0]
	portStr := parts[1]

	// Handle wildcard port
	if portStr == "*" {
		return &HostPattern{
			Type:     "host_wildcard",
			Host:     host,
			Port:     -1,
			Original: original,
		}, nil
	}

	// Parse specific port
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("invalid port number: %s", portStr)
	}

	return &HostPattern{
		Type:     "host_port",
		Host:     host,
		Port:     port,
		Original: original,
	}, nil
}

// compileWildcardPattern compiles wildcard patterns like "*:80", "*.example.com:*"
func compileWildcardPattern(pattern, original string) (*HostPattern, error) {
	parts := strings.Split(pattern, ":")

	if len(parts) == 2 {
		host := parts[0]
		portStr := parts[1]

		// Handle *:port pattern (wildcard host, specific port)
		if host == "*" && portStr != "*" {
			port, err := strconv.Atoi(portStr)
			if err != nil || port < 1 || port > 65535 {
				return nil, fmt.Errorf("invalid port number: %s", portStr)
			}

			return &HostPattern{
				Type:     "port_wildcard",
				Port:     port,
				Original: original,
			}, nil
		}

		// Handle *:* pattern (wildcard host, wildcard port)
		if host == "*" && portStr == "*" {
			return &HostPattern{
				Type:     "wildcard_all",
				Port:     -1,
				Original: original,
			}, nil
		}

		// Handle host:* pattern (specific host, wildcard port) - but only if host has no wildcards
		if portStr == "*" && !strings.Contains(host, "*") {
			return &HostPattern{
				Type:     "host_wildcard",
				Host:     host,
				Port:     -1,
				Original: original,
			}, nil
		}

		// Handle complex patterns with wildcards (e.g., *.example.com:*, *.com:80, etc.)
		if strings.Contains(host, "*") || strings.Contains(portStr, "*") {
			// Convert to regex pattern for complex wildcards with proper escaping
			regexPattern := convertWildcardToRegex(pattern)
			return compileRegexPattern(regexPattern, original)
		}
	}

	// Convert to regex pattern for complex wildcards
	regexPattern := convertWildcardToRegex(pattern)
	return compileRegexPattern(regexPattern, original)
}

// convertWildcardToRegex converts a wildcard pattern to a proper regex pattern
func convertWildcardToRegex(pattern string) string {
	// Escape regex special characters except for *
	escaped := regexp.QuoteMeta(pattern)
	// Convert escaped \* back to .*
	regexPattern := strings.ReplaceAll(escaped, "\\*", ".*")
	// Add anchors to ensure full match
	return "^" + regexPattern + "$"
}

// compileRegexPattern compiles traditional regex patterns for backward compatibility
func compileRegexPattern(pattern, original string) (*HostPattern, error) {
	regex, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid regex pattern: %v", err)
	}

	return &HostPattern{
		Type:     "regex",
		Regex:    regex,
		Original: original,
	}, nil
}

// isRegexPattern checks if a pattern contains regex metacharacters
// It's smarter about distinguishing wildcard patterns from regex patterns
func isRegexPattern(pattern string) bool {
	// First pass: check for complex regex metacharacters (excluding . and *)
	regexChars := []string{"^", "$", "[", "]", "(", ")", "{", "}", "+", "?", "|", "\\"}
	for _, char := range regexChars {
		if strings.Contains(pattern, char) {
			return true
		}
	}

	// Second pass: check if it has escaped dots or other regex-specific dot patterns
	// Only treat as regex if dots are clearly regex metacharacters, not domain names
	if strings.Contains(pattern, ".") {
		// Patterns like example\.com (escaped dots) are clearly regex
		if strings.Contains(pattern, "\\.") {
			return true
		}

		// Patterns starting or ending with dots (like .* or .*.) are regex
		parts := strings.Split(pattern, ":")
		for _, part := range parts {
			if strings.HasPrefix(part, ".") && len(part) > 1 {
				return true
			}
			if strings.HasSuffix(part, ".") && len(part) > 1 && !strings.HasSuffix(part, "*.") {
				return true
			}
		}
	}

	return false
}

// matchesHostPattern checks if an address matches a compiled host pattern
func matchesHostPattern(pattern *HostPattern, address string) bool {
	switch pattern.Type {
	case "regex":
		return pattern.Regex.MatchString(address)

	case "cidr":
		return matchesCIDRPattern(pattern, address)

	case "host_port":
		return matchesHostPortPattern(pattern, address)

	case "host_wildcard":
		return matchesHostWildcardPattern(pattern, address)

	case "port_wildcard":
		return matchesPortWildcardPattern(pattern, address)

	case "wildcard_all":
		return true // matches everything

	default:
		return false
	}
}

// matchesCIDRPattern checks if address matches CIDR pattern
func matchesCIDRPattern(pattern *HostPattern, address string) bool {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		// Try without port
		host = address
		port = ""
	}

	// Parse IP address
	ip := net.ParseIP(host)
	if ip == nil {
		// Try to resolve hostname to IP
		ips, err := net.LookupIP(host)
		if err != nil || len(ips) == 0 {
			return false
		}
		ip = ips[0]
	}

	// Check if IP is in CIDR range
	if !pattern.Network.Contains(ip) {
		return false
	}

	// Check port if specified
	if pattern.Port != -1 {
		if port == "" {
			return false
		}
		portNum, err := strconv.Atoi(port)
		if err != nil {
			return false
		}
		return portNum == pattern.Port
	}

	return true
}

// matchesHostPortPattern checks if address matches host:port pattern
func matchesHostPortPattern(pattern *HostPattern, address string) bool {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}

	// Check host
	if host != pattern.Host {
		return false
	}

	// Check port
	portNum, err := strconv.Atoi(port)
	if err != nil {
		return false
	}

	return portNum == pattern.Port
}

// matchesHostWildcardPattern checks if address matches host:* pattern
func matchesHostWildcardPattern(pattern *HostPattern, address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		// Try without port
		host = address
	}

	// Support wildcard in host
	if strings.Contains(pattern.Host, "*") {
		regexPattern := strings.ReplaceAll(pattern.Host, "*", ".*")
		regex, err := regexp.Compile("^" + regexPattern + "$")
		if err != nil {
			return false
		}
		return regex.MatchString(host)
	}

	return host == pattern.Host
}

// matchesPortWildcardPattern checks if address matches *:port pattern
func matchesPortWildcardPattern(pattern *HostPattern, address string) bool {
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}

	portNum, err := strconv.Atoi(port)
	if err != nil {
		return false
	}

	return portNum == pattern.Port
}

// isConnectionAllowed checks if connection is allowed using enhanced pattern matching
func (c *Client) isConnectionAllowed(address string) bool {
	// First check if it's forbidden using new pattern system
	for _, pattern := range c.forbiddenHostPatterns {
		if matchesHostPattern(pattern, address) {
			logger.Warn("🚫 CONNECTION BLOCKED - Forbidden host", "client_id", c.getClientID(), "address", address, "pattern", pattern.Original, "pattern_type", pattern.Type, "action", "Connection rejected due to forbidden host policy")
			return false
		}
	}

	// If no allowed hosts are configured, allow all non-forbidden connections
	if len(c.allowedHostPatterns) == 0 {
		logger.Debug("Connection allowed - no allowed hosts configured", "client_id", c.getClientID(), "address", address)
		return true
	}

	// Check if it's in the allowed list using new pattern system
	for _, pattern := range c.allowedHostPatterns {
		if matchesHostPattern(pattern, address) {
			logger.Debug("Connection allowed - matches allowed pattern", "client_id", c.getClientID(), "address", address, "pattern", pattern.Original, "pattern_type", pattern.Type)
			return true
		}
	}

	logger.Warn("Connection blocked - not in allowed hosts", "client_id", c.getClientID(), "address", address, "action", "Connection rejected - host not in allowed list")
	return false
}

// createTLSConfig creates TLS configuration
func (c *Client) createTLSConfig() (*tls.Config, error) {
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}

	// If TLS certificate is provided, load it
	if c.config.Gateway.TLSCert != "" {
		certPEM, err := os.ReadFile(c.config.Gateway.TLSCert)
		if err != nil {
			return nil, fmt.Errorf("failed to read TLS certificate: %v", err)
		}

		certPool := x509.NewCertPool()
		if ok := certPool.AppendCertsFromPEM(certPEM); !ok {
			return nil, fmt.Errorf("failed to parse TLS certificate")
		}

		tlsConfig.RootCAs = certPool

		// Extract server name from certificate file path
		serverName := strings.TrimSuffix(c.config.Gateway.Addr, ":443")
		if colonIndex := strings.LastIndex(serverName, ":"); colonIndex != -1 {
			serverName = serverName[:colonIndex]
		}
		tlsConfig.ServerName = serverName
	}

	return tlsConfig, nil
}
