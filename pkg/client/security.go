package client

import (
	"context"
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

// resolveTarget resolves the host of address once. The host rules are checked
// against these IPs and only these IPs are dialed, so a second DNS answer
// cannot change the destination. A "tcp4"/"udp4" network keeps IPv4 only.
func resolveTarget(ctx context.Context, network, address string) ([]net.IP, string, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, "", err
	}

	ipNetwork := "ip"
	if strings.HasSuffix(network, "4") {
		ipNetwork = "ip4"
	} else if strings.HasSuffix(network, "6") {
		ipNetwork = "ip6"
	}

	ips, err := net.DefaultResolver.LookupIP(ctx, ipNetwork, host)
	if err != nil {
		return nil, "", err
	}
	return ips, port, nil
}

// bracketedHostRule matches "[host]" and "[host]:port" rules
var bracketedHostRule = regexp.MustCompile(`^\[([^\]]*)\](:.*)?$`)

// Host pattern types
const (
	patternTypeRegex        = "regex"
	patternTypeCIDR         = "cidr"
	patternTypeHostPort     = "host_port"
	patternTypeHostWildcard = "host_wildcard"
	patternTypePortWildcard = "port_wildcard"
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

	// A bare IP is a single-address CIDR, so a name that resolves to it matches too
	if ip := net.ParseIP(pattern); ip != nil {
		return singleIPPattern(ip, -1, original), nil
	}

	// "[::1]", "[::1]:22" and "[::1]:*" are IPv6 addresses; their brackets are
	// not a regex. As a regex, "[...]" would match almost any target.
	if m := bracketedHostRule.FindStringSubmatch(pattern); m != nil {
		ip := net.ParseIP(m[1])
		if ip == nil {
			return nil, fmt.Errorf("%q in brackets is not an IP address", m[1])
		}
		if m[2] == "" {
			return singleIPPattern(ip, -1, original), nil
		}
		return compileHostPortPattern(pattern, original)
	}

	// Check for wildcard patterns before host:port (wildcards take precedence)
	if strings.Contains(pattern, "*") && !isRegexPattern(pattern) {
		return compileWildcardPattern(pattern, original)
	}

	// Check for host:port patterns
	if strings.Contains(pattern, ":") && !isRegexPattern(pattern) {
		return compileHostPortPattern(pattern, original)
	}

	// A plain host name matches that host and its subdomains, on any port.
	// The match stops at a label boundary, so "example.com" does not match
	// "evilexample.com" or "example.com.attacker.io".
	if !isRegexPattern(pattern) {
		return &HostPattern{
			Type:     patternTypeHostWildcard,
			Host:     pattern,
			Regex:    regexp.MustCompile(`(?i)^(.+\.)?` + regexp.QuoteMeta(pattern) + `$`),
			Port:     -1,
			Original: original,
		}, nil
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
					Type:     patternTypeCIDR,
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
				Type:     patternTypeCIDR,
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
		Type:     patternTypeCIDR,
		Network:  network,
		Port:     -1, // no port restriction
		Original: original,
	}, nil
}

// singleIPPattern compiles one IP address, with port -1 for any port, as a
// CIDR pattern, so a name that resolves to the IP matches too
func singleIPPattern(ip net.IP, port int, original string) *HostPattern {
	bits := 8 * net.IPv6len
	if ip4 := ip.To4(); ip4 != nil {
		ip, bits = ip4, 8*net.IPv4len
	}
	return &HostPattern{
		Type:     patternTypeCIDR,
		Network:  &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)},
		Port:     port,
		Original: original,
	}
}

// compileHostPortPattern compiles host:port patterns like "localhost:22", "example.com:80"
func compileHostPortPattern(pattern, original string) (*HostPattern, error) {
	host, portStr, err := net.SplitHostPort(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid host:port pattern")
	}

	// Handle wildcard port
	if portStr == "*" {
		if ip := net.ParseIP(host); ip != nil {
			return singleIPPattern(ip, -1, original), nil
		}
		return &HostPattern{
			Type:     patternTypeHostWildcard,
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

	if ip := net.ParseIP(host); ip != nil {
		return singleIPPattern(ip, port, original), nil
	}

	return &HostPattern{
		Type:     patternTypeHostPort,
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
				Type:     patternTypePortWildcard,
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
			return compileHostPortPattern(pattern, original)
		}

		// Handle complex patterns with wildcards (e.g., *.example.com:*, *.com:80, etc.)
		if strings.Contains(host, "*") || strings.Contains(portStr, "*") {
			// Convert to regex pattern for complex wildcards with proper escaping
			regexPattern := convertWildcardToRegex(pattern)
			return compileRegexPattern(regexPattern, original)
		}
	}

	// A wildcard without a port matches the host part only, on any port
	if len(parts) == 1 {
		regex, err := regexp.Compile(convertWildcardToRegex(pattern))
		if err != nil {
			return nil, fmt.Errorf("invalid wildcard pattern: %v", err)
		}
		return &HostPattern{
			Type:     patternTypeHostWildcard,
			Regex:    regex,
			Port:     -1,
			Original: original,
		}, nil
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
	// Add anchors to ensure full match; host names are case-insensitive
	return "(?i)^" + regexPattern + "$"
}

// compileRegexPattern compiles traditional regex patterns for backward compatibility
func compileRegexPattern(pattern, original string) (*HostPattern, error) {
	regex, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid regex pattern: %v", err)
	}

	return &HostPattern{
		Type:     patternTypeRegex,
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

// trimHostDot drops one trailing dot from the host of address, so
// "example.com.:443" is checked like "example.com:443" by every pattern
func trimHostDot(address string) string {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		// Address without port
		return strings.TrimSuffix(address, ".")
	}
	return net.JoinHostPort(strings.TrimSuffix(host, "."), port)
}

// targetHost returns the host part of address
func targetHost(address string) string {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		// Address without port
		return address
	}
	return host
}

// matchesHostPattern checks if an address matches a compiled host pattern.
// ip is the resolved address that will be dialed; CIDR patterns check it.
func matchesHostPattern(pattern *HostPattern, address string, ip net.IP) bool {
	switch pattern.Type {
	case patternTypeRegex:
		return pattern.Regex.MatchString(address)

	case patternTypeCIDR:
		return matchesCIDRPattern(pattern, address, ip)

	case patternTypeHostPort:
		return matchesHostPortPattern(pattern, address)

	case patternTypeHostWildcard:
		return matchesHostWildcardPattern(pattern, address)

	case patternTypePortWildcard:
		return matchesPortWildcardPattern(pattern, address)

	case "wildcard_all":
		return true // matches everything

	default:
		return false
	}
}

// matchesCIDRPattern checks if the resolved ip of address matches CIDR pattern
func matchesCIDRPattern(pattern *HostPattern, address string, ip net.IP) bool {
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		port = ""
	}

	// Check if IP is in CIDR range
	if ip == nil || !pattern.Network.Contains(ip) {
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
	if !strings.EqualFold(host, pattern.Host) {
		return false
	}

	// Check port
	portNum, err := strconv.Atoi(port)
	if err != nil {
		return false
	}

	return portNum == pattern.Port
}

// matchesHostWildcardPattern checks if the host of address matches a host,
// host:* or port-less wildcard pattern, on any port
func matchesHostWildcardPattern(pattern *HostPattern, address string) bool {
	host := targetHost(address)
	if pattern.Regex != nil {
		return pattern.Regex.MatchString(host)
	}
	return strings.EqualFold(host, pattern.Host)
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

// isConnectionAllowed checks if connection is allowed using enhanced pattern matching.
// ips are the resolved addresses that will be dialed; every one must pass, so
// one forbidden IP denies the target. Without ips, an IP literal host is used.
func (c *Client) isConnectionAllowed(address string, ips ...net.IP) bool {
	address = trimHostDot(address)
	if len(ips) == 0 {
		// nil for a host name: CIDR patterns cannot match it
		ips = []net.IP{net.ParseIP(targetHost(address))}
	}
	for _, ip := range ips {
		if !c.isTargetAllowed(address, ip) {
			return false
		}
	}
	return true
}

// passesNameRules checks address against the rules that need no DNS, so a
// target denied by name is never looked up. CIDR and IP rules are checked
// later, against the resolved IPs.
func (c *Client) passesNameRules(address string) bool {
	address = trimHostDot(address)
	for _, pattern := range c.forbiddenHostPatterns {
		if matchesHostPattern(pattern, address, nil) {
			return false
		}
	}
	if len(c.allowedHostPatterns) == 0 {
		return true
	}
	for _, pattern := range c.allowedHostPatterns {
		// A CIDR or IP rule may still allow the target once it is resolved
		if pattern.Type == patternTypeCIDR || matchesHostPattern(pattern, address, nil) {
			return true
		}
	}
	return false
}

// isTargetAllowed checks address, dialed at ip, against the host patterns
func (c *Client) isTargetAllowed(address string, ip net.IP) bool {
	// First check if it's forbidden using new pattern system
	for _, pattern := range c.forbiddenHostPatterns {
		if matchesHostPattern(pattern, address, ip) {
			logger.Warn("🚫 CONNECTION BLOCKED - Forbidden host", "client_id", c.getClientID(), "address", address, "ip", ip, "pattern", pattern.Original, "pattern_type", pattern.Type, "action", "Connection rejected due to forbidden host policy")
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
		if matchesHostPattern(pattern, address, ip) {
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

	// ServerName is required for SNI on every TLS handshake, not just when a
	// custom CA is pinned. QUIC refuses to dial without it.
	serverName := c.config.Gateway.Addr
	if idx := strings.Index(serverName, "://"); idx != -1 {
		serverName = serverName[idx+3:]
	}
	if host, _, err := net.SplitHostPort(serverName); err == nil {
		serverName = host
	}
	tlsConfig.ServerName = serverName

	// If a TLS certificate is provided, use it as the only RootCA. Otherwise
	// leave RootCAs nil so the Go TLS stack falls back to the system trust
	// store (needed when the gateway serves a publicly-trusted cert).
	if c.config.Gateway.TLSCert != "" {
		var certPEM []byte
		var err error

		// Check if it's PEM content or file path
		if strings.Contains(c.config.Gateway.TLSCert, "-----BEGIN CERTIFICATE-----") {
			certPEM = []byte(c.config.Gateway.TLSCert)
		} else {
			certPEM, err = os.ReadFile(c.config.Gateway.TLSCert)
			if err != nil {
				return nil, fmt.Errorf("failed to read TLS certificate: %v", err)
			}
		}

		certPool := x509.NewCertPool()
		if ok := certPool.AppendCertsFromPEM(certPEM); !ok {
			return nil, fmt.Errorf("failed to parse TLS certificate")
		}

		tlsConfig.RootCAs = certPool
	}

	return tlsConfig, nil
}
