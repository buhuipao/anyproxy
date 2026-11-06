// SOCKS5 Proxy Test Tool
// Usage:
//
// Prerequisites:
//
//  1. Start Gateway (Terminal 1):
//     ./bin/anyproxy-gateway -config tests/socks5/gateway-test.yaml
//
//  2. Start Client (Terminal 2):
//     ./bin/anyproxy-client -config tests/socks5/client-test.yaml
//
//  3. Run tests:
//     go run ./tests/socks5 -proxy 127.0.0.1:11080 -user test-group -pass test-password -test all
//     go run ./tests/socks5 -proxy 127.0.0.1:11080 -user test-group -pass test-password -test tcp
//     go run ./tests/socks5 -proxy 127.0.0.1:11080 -user test-group -pass test-password -test udp
package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

var (
	proxyAddr = flag.String("proxy", "127.0.0.1:11080", "SOCKS5 proxy address")
	username  = flag.String("user", "", "SOCKS5 username (group_id)")
	password  = flag.String("pass", "", "SOCKS5 password (group_password)")
	testType  = flag.String("test", "all", "Test type: tcp, udp, or all")
	target    = flag.String("target", "httpbin.org:80", "Target address for TCP test")
	dnsServer = flag.String("dns", "", "DNS server for UDP test (auto-detect from system if empty)")
	domain    = flag.String("domain", "example.com", "Domain to query for UDP test")
	timeout   = flag.Duration("timeout", 10*time.Second, "Timeout")
)

func main() {
	flag.Parse()

	// Auto-detect DNS server if not specified
	if *dnsServer == "" && (*testType == "udp" || *testType == "all") {
		detected := detectDNSServer()
		if detected == "" {
			fmt.Println("WARNING: Could not detect system DNS server, falling back to 8.8.8.8:53")
			*dnsServer = "8.8.8.8:53"
		} else {
			*dnsServer = detected
			fmt.Printf("Auto-detected DNS server: %s\n", *dnsServer)
		}
	}

	fmt.Println("========================================")
	fmt.Println("SOCKS5 Proxy Test Tool")
	fmt.Println("========================================")
	fmt.Printf("Proxy: %s\n", *proxyAddr)
	fmt.Printf("User: %s\n", *username)
	fmt.Printf("Test: %s\n", *testType)
	fmt.Println()

	var tcpOK, udpOK bool

	if *testType == "tcp" || *testType == "all" {
		fmt.Println("--- TCP Test ---")
		if err := testTCP(); err != nil {
			fmt.Printf("TCP FAILED: %v\n", err)
		} else {
			fmt.Println("TCP PASSED!")
			tcpOK = true
		}
		fmt.Println()
	}

	if *testType == "udp" || *testType == "all" {
		fmt.Println("--- UDP Test ---")
		if err := testUDP(); err != nil {
			fmt.Printf("UDP FAILED: %v\n", err)
		} else {
			fmt.Println("UDP PASSED!")
			udpOK = true
		}
		fmt.Println()
	}

	fmt.Println("========================================")
	fmt.Println("Summary")
	fmt.Println("========================================")
	if *testType == "tcp" || *testType == "all" {
		if tcpOK {
			fmt.Println("TCP: ✓ PASSED")
		} else {
			fmt.Println("TCP: ✗ FAILED")
		}
	}
	if *testType == "udp" || *testType == "all" {
		if udpOK {
			fmt.Println("UDP: ✓ PASSED")
		} else {
			fmt.Println("UDP: ✗ FAILED")
		}
	}

	if (*testType == "tcp" && !tcpOK) || (*testType == "udp" && !udpOK) ||
		(*testType == "all" && (!tcpOK || !udpOK)) {
		os.Exit(1)
	}
}

func testTCP() error {
	fmt.Printf("Connecting to proxy %s...\n", *proxyAddr)
	conn, err := net.DialTimeout("tcp", *proxyAddr, *timeout)
	if err != nil {
		return fmt.Errorf("failed to connect to proxy: %w", err)
	}
	defer conn.Close()
	fmt.Println("Connected to proxy")

	// Auth
	fmt.Println("Authenticating...")
	if err := authenticate(conn); err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}
	fmt.Println("Authenticated")

	// CONNECT
	fmt.Printf("Sending CONNECT to %s...\n", *target)
	if err := sendConnect(conn, *target); err != nil {
		return fmt.Errorf("CONNECT failed: %w", err)
	}
	fmt.Println("CONNECT successful")

	// Send HTTP request
	fmt.Println("Sending HTTP request...")
	host, _, _ := net.SplitHostPort(*target)
	req := fmt.Sprintf("GET /ip HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", host)
	conn.Write([]byte(req))

	// Read response
	conn.SetReadDeadline(time.Now().Add(*timeout))
	resp, err := io.ReadAll(conn)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	fmt.Println("Response received:")
	fmt.Println(string(resp))

	if !strings.Contains(string(resp), "200 OK") && !strings.Contains(string(resp), "origin") {
		return fmt.Errorf("unexpected response")
	}

	return nil
}

func testUDP() error {
	fmt.Printf("Connecting to proxy %s...\n", *proxyAddr)
	tcpConn, err := net.DialTimeout("tcp", *proxyAddr, *timeout)
	if err != nil {
		return fmt.Errorf("failed to connect to proxy: %w", err)
	}
	defer tcpConn.Close()
	fmt.Println("Connected to proxy")

	// Auth
	fmt.Println("Authenticating...")
	if err := authenticate(tcpConn); err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}
	fmt.Println("Authenticated")

	// UDP ASSOCIATE
	fmt.Println("Sending UDP ASSOCIATE...")
	relayAddr, err := sendUDPAssociate(tcpConn)
	if err != nil {
		return fmt.Errorf("UDP ASSOCIATE failed: %w", err)
	}
	fmt.Printf("UDP relay address: %s\n", relayAddr)

	// Create UDP socket
	fmt.Println("Creating local UDP socket...")
	localUDP, err := net.ListenUDP("udp", &net.UDPAddr{Port: 0})
	if err != nil {
		return fmt.Errorf("failed to create UDP socket: %w", err)
	}
	defer localUDP.Close()
	fmt.Printf("Local UDP: %s\n", localUDP.LocalAddr())

	// Build and send DNS query
	fmt.Printf("Sending DNS query for %s to %s...\n", *domain, *dnsServer)
	dnsQuery := buildDNSQuery(*domain)
	packet := buildUDPPacket(*dnsServer, dnsQuery)

	relayUDP, _ := net.ResolveUDPAddr("udp", relayAddr)
	_, err = localUDP.WriteToUDP(packet, relayUDP)
	if err != nil {
		return fmt.Errorf("failed to send UDP: %w", err)
	}
	fmt.Println("DNS query sent")

	// Receive response
	fmt.Println("Waiting for DNS response...")
	localUDP.SetReadDeadline(time.Now().Add(*timeout))
	respBuf := make([]byte, 4096)
	n, _, err := localUDP.ReadFromUDP(respBuf)
	if err != nil {
		return fmt.Errorf("failed to receive UDP: %w", err)
	}
	fmt.Printf("Received %d bytes\n", n)

	// Parse SOCKS5 UDP header
	dnsResp, err := parseUDPResponse(respBuf[:n])
	if err != nil {
		return fmt.Errorf("failed to parse UDP response: %w", err)
	}

	// Parse DNS response
	ips, err := parseDNSResponse(dnsResp)
	if err != nil {
		return fmt.Errorf("failed to parse DNS response: %w", err)
	}

	fmt.Printf("DNS resolved %s to: %v\n", *domain, ips)
	return nil
}

func authenticate(conn net.Conn) error {
	conn.SetDeadline(time.Now().Add(*timeout))

	// Send auth methods
	var methods []byte
	if *username != "" && *password != "" {
		methods = []byte{0x05, 0x02, 0x00, 0x02}
	} else {
		methods = []byte{0x05, 0x01, 0x00}
	}
	conn.Write(methods)

	// Read response
	resp := make([]byte, 2)
	io.ReadFull(conn, resp)

	if resp[0] != 0x05 {
		return fmt.Errorf("invalid SOCKS version")
	}

	if resp[1] == 0x02 {
		// Username/password auth
		authReq := []byte{0x01, byte(len(*username))}
		authReq = append(authReq, []byte(*username)...)
		authReq = append(authReq, byte(len(*password)))
		authReq = append(authReq, []byte(*password)...)
		conn.Write(authReq)

		authResp := make([]byte, 2)
		io.ReadFull(conn, authResp)
		if authResp[1] != 0x00 {
			return fmt.Errorf("authentication rejected")
		}
	} else if resp[1] != 0x00 {
		return fmt.Errorf("unsupported auth method: %d", resp[1])
	}

	return nil
}

func sendConnect(conn net.Conn, addr string) error {
	host, portStr, _ := net.SplitHostPort(addr)
	port, _ := net.LookupPort("tcp", portStr)

	var req bytes.Buffer
	req.WriteByte(0x05) // Version
	req.WriteByte(0x01) // CONNECT
	req.WriteByte(0x00) // Reserved

	if ip := net.ParseIP(host); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			req.WriteByte(0x01)
			req.Write(ip4)
		} else {
			req.WriteByte(0x04)
			req.Write(ip.To16())
		}
	} else {
		req.WriteByte(0x03)
		req.WriteByte(byte(len(host)))
		req.WriteString(host)
	}

	portBytes := make([]byte, 2)
	binary.BigEndian.PutUint16(portBytes, uint16(port))
	req.Write(portBytes)

	conn.Write(req.Bytes())

	resp := make([]byte, 256)
	n, _ := conn.Read(resp)
	if n < 10 || resp[1] != 0x00 {
		return fmt.Errorf("CONNECT failed with code: %d", resp[1])
	}

	return nil
}

func sendUDPAssociate(conn net.Conn) (string, error) {
	req := []byte{0x05, 0x03, 0x00, 0x01, 0, 0, 0, 0, 0, 0}
	conn.Write(req)

	resp := make([]byte, 256)
	n, _ := conn.Read(resp)
	if n < 10 || resp[1] != 0x00 {
		return "", fmt.Errorf("UDP ASSOCIATE failed with code: %d", resp[1])
	}

	var host string
	var port uint16
	switch resp[3] {
	case 0x01:
		host = net.IP(resp[4:8]).String()
		port = binary.BigEndian.Uint16(resp[8:10])
	case 0x04:
		host = net.IP(resp[4:20]).String()
		port = binary.BigEndian.Uint16(resp[20:22])
	}

	if host == "0.0.0.0" || host == "::" {
		h, _, _ := net.SplitHostPort(*proxyAddr)
		host = h
	}

	return fmt.Sprintf("%s:%d", host, port), nil
}

func buildUDPPacket(addr string, data []byte) []byte {
	host, portStr, _ := net.SplitHostPort(addr)
	port, _ := net.LookupPort("udp", portStr)

	var p bytes.Buffer
	p.Write([]byte{0x00, 0x00, 0x00}) // RSV + FRAG

	if ip := net.ParseIP(host); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			p.WriteByte(0x01)
			p.Write(ip4)
		} else {
			p.WriteByte(0x04)
			p.Write(ip.To16())
		}
	} else {
		p.WriteByte(0x03)
		p.WriteByte(byte(len(host)))
		p.WriteString(host)
	}

	portBytes := make([]byte, 2)
	binary.BigEndian.PutUint16(portBytes, uint16(port))
	p.Write(portBytes)
	p.Write(data)

	return p.Bytes()
}

func parseUDPResponse(data []byte) ([]byte, error) {
	if len(data) < 10 {
		return nil, fmt.Errorf("response too short")
	}

	offset := 3 // RSV + FRAG
	switch data[offset] {
	case 0x01:
		offset += 4 + 2 + 1 // IPv4 + port + type
	case 0x04:
		offset += 16 + 2 + 1 // IPv6 + port + type
	case 0x03:
		offset += int(data[offset+1]) + 2 + 2 // domain len + domain + port + type
	}

	// Adjust for simpler parsing
	offset = 3
	if data[offset] == 0x01 {
		offset += 1 + 4 + 2
	}

	return data[offset:], nil
}

func buildDNSQuery(domain string) []byte {
	var q bytes.Buffer
	q.Write([]byte{0xAB, 0xCD, 0x01, 0x00})
	q.Write([]byte{0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00})

	for _, label := range strings.Split(domain, ".") {
		q.WriteByte(byte(len(label)))
		q.WriteString(label)
	}
	q.WriteByte(0x00)
	q.Write([]byte{0x00, 0x01, 0x00, 0x01})

	return q.Bytes()
}

func parseDNSResponse(data []byte) ([]string, error) {
	if len(data) < 12 {
		return nil, fmt.Errorf("DNS response too short")
	}

	answerCount := binary.BigEndian.Uint16(data[6:8])
	if answerCount == 0 {
		return nil, fmt.Errorf("no DNS answers")
	}

	offset := 12
	for offset < len(data) && data[offset] != 0 {
		if data[offset]&0xC0 == 0xC0 {
			offset += 2
			break
		}
		offset += int(data[offset]) + 1
	}
	if offset < len(data) && data[offset] == 0 {
		offset++
	}
	offset += 4

	var ips []string
	for i := 0; i < int(answerCount) && offset < len(data); i++ {
		if data[offset]&0xC0 == 0xC0 {
			offset += 2
		} else {
			for offset < len(data) && data[offset] != 0 {
				offset += int(data[offset]) + 1
			}
			offset++
		}

		if offset+10 > len(data) {
			break
		}

		aType := binary.BigEndian.Uint16(data[offset : offset+2])
		offset += 2 + 2 + 4
		dataLen := binary.BigEndian.Uint16(data[offset : offset+2])
		offset += 2

		if aType == 1 && dataLen == 4 && offset+4 <= len(data) {
			ips = append(ips, net.IP(data[offset:offset+4]).String())
		}
		offset += int(dataLen)
	}

	if len(ips) == 0 {
		return nil, fmt.Errorf("no A records")
	}
	return ips, nil
}

// detectDNSServer reads the system DNS configuration and returns the first nameserver
func detectDNSServer() string {
	switch runtime.GOOS {
	case "darwin":
		// macOS: use scutil --dns
		out, err := exec.Command("scutil", "--dns").Output()
		if err == nil {
			scanner := bufio.NewScanner(bytes.NewReader(out))
			for scanner.Scan() {
				line := strings.TrimSpace(scanner.Text())
				if strings.HasPrefix(line, "nameserver[0]") {
					parts := strings.SplitN(line, ":", 2)
					if len(parts) == 2 {
						ns := strings.TrimSpace(parts[1])
						return ns + ":53"
					}
				}
			}
		}
	default:
		// Linux/other: read /etc/resolv.conf
		data, err := os.ReadFile("/etc/resolv.conf")
		if err == nil {
			scanner := bufio.NewScanner(bytes.NewReader(data))
			for scanner.Scan() {
				line := strings.TrimSpace(scanner.Text())
				if strings.HasPrefix(line, "nameserver") {
					fields := strings.Fields(line)
					if len(fields) >= 2 {
						return fields[1] + ":53"
					}
				}
			}
		}
	}
	return ""
}
