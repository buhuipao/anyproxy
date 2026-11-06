// SOCKS5 Throughput & Large Data Transfer Test
// Tests TCP download speed and UDP streaming performance through SOCKS5 proxy
//
// Usage:
//   go run ./tests/throughput -proxy 47.107.181.88:1080 -user demo-group -pass demo-123
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
	"sync"
	"sync/atomic"
	"time"
)

var (
	proxyAddr = flag.String("proxy", "127.0.0.1:1080", "SOCKS5 proxy address")
	username  = flag.String("user", "demo-group", "SOCKS5 username (group_id)")
	password  = flag.String("pass", "demo-123", "SOCKS5 password (group_password)")
	testType  = flag.String("test", "all", "Test type: tcp, udp, stream, or all")
	dnsServer = flag.String("dns", "", "DNS server for UDP tests (auto-detect from system if empty)")
	duration  = flag.Duration("duration", 10*time.Second, "Test duration for streaming tests")
	timeout   = flag.Duration("timeout", 30*time.Second, "Timeout for download tests")
)

func main() {
	flag.Parse()

	// Auto-detect DNS server if not specified and UDP tests are needed
	if *dnsServer == "" && (*testType == "udp" || *testType == "stream" || *testType == "all") {
		detected := detectDNSServer()
		if detected == "" {
			fmt.Println("WARNING: Could not detect system DNS server, falling back to 8.8.8.8:53")
			*dnsServer = "8.8.8.8:53"
		} else {
			*dnsServer = detected
			fmt.Printf("Auto-detected DNS server: %s\n", *dnsServer)
		}
	}

	fmt.Println("╔═══════════════════════════════════════════════════╗")
	fmt.Println("║   SOCKS5 Throughput & Large Transfer Test         ║")
	fmt.Println("╚═══════════════════════════════════════════════════╝")
	fmt.Printf("Proxy: %s\n", *proxyAddr)
	fmt.Printf("User: %s\n", *username)
	fmt.Println()

	if *testType == "tcp" || *testType == "all" {
		fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		fmt.Println("  TCP Large File Download Test")
		fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		testTCPDownload()
		fmt.Println()
	}

	if *testType == "udp" || *testType == "all" {
		fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		fmt.Println("  UDP Burst Test (Multiple DNS queries)")
		fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		testUDPBurst()
		fmt.Println()
	}

	if *testType == "stream" || *testType == "all" {
		fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		fmt.Println("  UDP Streaming Simulation (Game Streaming)")
		fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		testUDPStreaming()
		fmt.Println()
	}
}

// testTCPDownload tests large file download through SOCKS5 TCP proxy
func testTCPDownload() {
	// Test with different file sizes
	tests := []struct {
		name string
		url  string
		host string
	}{
		{"1MB file", "/1000000", "httpbin.org:80"},
		{"5MB file", "/5000000", "httpbin.org:80"},
	}

	for _, test := range tests {
		fmt.Printf("\n  📥 Downloading %s...\n", test.name)

		conn, err := net.DialTimeout("tcp", *proxyAddr, *timeout)
		if err != nil {
			fmt.Printf("     ❌ Connect failed: %v\n", err)
			continue
		}

		if err := authenticate(conn); err != nil {
			conn.Close()
			fmt.Printf("     ❌ Auth failed: %v\n", err)
			continue
		}

		if err := sendConnect(conn, test.host); err != nil {
			conn.Close()
			fmt.Printf("     ❌ CONNECT failed: %v\n", err)
			continue
		}

		// Send HTTP request for random bytes
		host, _, _ := net.SplitHostPort(test.host)
		req := fmt.Sprintf("GET /bytes%s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", test.url, host)

		start := time.Now()
		conn.Write([]byte(req))

		// Read all data
		conn.SetReadDeadline(time.Now().Add(*timeout))
		data, err := io.ReadAll(conn)
		elapsed := time.Since(start)
		conn.Close()

		if err != nil {
			fmt.Printf("     ❌ Read failed: %v\n", err)
			continue
		}

		// Calculate throughput
		size := len(data)
		throughput := float64(size) / elapsed.Seconds() / 1024 / 1024 // MB/s

		fmt.Printf("     ✓ Downloaded: %d bytes (%.2f KB)\n", size, float64(size)/1024)
		fmt.Printf("     ✓ Time: %v\n", elapsed.Round(time.Millisecond))
		fmt.Printf("     ✓ Throughput: %.2f MB/s\n", throughput)

		// Rating
		if throughput > 5 {
			fmt.Printf("     🟢 Excellent throughput\n")
		} else if throughput > 1 {
			fmt.Printf("     🟡 Good throughput\n")
		} else {
			fmt.Printf("     🔴 Low throughput\n")
		}
	}
}

// testUDPBurst tests rapid UDP packet sending (simulates bursty game traffic)
func testUDPBurst() {
	// Setup UDP relay
	tcpConn, relayAddr, localUDP, err := setupUDPRelay()
	if err != nil {
		fmt.Printf("  ❌ Setup failed: %v\n", err)
		return
	}
	defer tcpConn.Close()
	defer localUDP.Close()

	relayUDP, _ := net.ResolveUDPAddr("udp", relayAddr)

	// Send burst of DNS queries
	burstSize := 50
	fmt.Printf("  📤 Sending %d UDP packets in burst...\n", burstSize)

	var sent, received int64
	var totalLatency time.Duration
	var wg sync.WaitGroup

	// Receiver goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 4096)
		localUDP.SetReadDeadline(time.Now().Add(10 * time.Second))
		for {
			n, _, err := localUDP.ReadFromUDP(buf)
			if err != nil {
				break
			}
			if n > 0 {
				atomic.AddInt64(&received, 1)
			}
		}
	}()

	// Send burst
	start := time.Now()
	for i := 0; i < burstSize; i++ {
		domain := fmt.Sprintf("test%d.example.com", i)
		dnsQuery := buildDNSQuery(domain)
		packet := buildUDPPacket(*dnsServer, dnsQuery)
		_, err := localUDP.WriteToUDP(packet, relayUDP)
		if err == nil {
			sent++
		}
		time.Sleep(5 * time.Millisecond) // 5ms interval = 200 packets/sec
	}
	totalLatency = time.Since(start)

	// Wait for responses
	time.Sleep(3 * time.Second)
	localUDP.SetReadDeadline(time.Now())
	wg.Wait()

	// Results
	fmt.Printf("\n  📊 UDP Burst Results:\n")
	fmt.Printf("  ┌─────────────────────────────────────┐\n")
	fmt.Printf("  │  Sent:      %3d packets             │\n", sent)
	fmt.Printf("  │  Received:  %3d packets             │\n", received)
	fmt.Printf("  │  Loss:      %3d packets (%.1f%%)     │\n", sent-received, float64(sent-received)/float64(sent)*100)
	fmt.Printf("  │  Duration:  %v                │\n", totalLatency.Round(time.Millisecond))
	fmt.Printf("  │  Rate:      %.0f pkt/s              │\n", float64(sent)/totalLatency.Seconds())
	fmt.Printf("  └─────────────────────────────────────┘\n")

	// Rating
	lossRate := float64(sent-received) / float64(sent) * 100
	if lossRate < 5 {
		fmt.Printf("  🟢 Excellent (< 5%% loss)\n")
	} else if lossRate < 20 {
		fmt.Printf("  🟡 Acceptable (< 20%% loss)\n")
	} else {
		fmt.Printf("  🔴 High packet loss (> 20%%)\n")
	}
}

// testUDPStreaming simulates continuous UDP streaming like game streaming
func testUDPStreaming() {
	fmt.Printf("  🎮 Simulating game streaming for %v...\n", *duration)
	fmt.Printf("     (60 packets/sec, ~1KB each, like video frames)\n\n")

	// Setup UDP relay
	tcpConn, relayAddr, localUDP, err := setupUDPRelay()
	if err != nil {
		fmt.Printf("  ❌ Setup failed: %v\n", err)
		return
	}
	defer tcpConn.Close()
	defer localUDP.Close()

	relayUDP, _ := net.ResolveUDPAddr("udp", relayAddr)

	var sent, received int64
	var latencies []time.Duration
	var latencyMu sync.Mutex

	// Receiver goroutine - track latencies
	done := make(chan struct{})
	go func() {
		buf := make([]byte, 4096)
		for {
			select {
			case <-done:
				return
			default:
				localUDP.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
				n, _, err := localUDP.ReadFromUDP(buf)
				if err != nil {
					continue
				}
				if n > 0 {
					atomic.AddInt64(&received, 1)
				}
			}
		}
	}()

	// Send continuous stream (60 fps simulation)
	ticker := time.NewTicker(16 * time.Millisecond) // ~60 fps
	defer ticker.Stop()

	timeout := time.After(*duration)
	frameNum := 0
	
	progressTicker := time.NewTicker(1 * time.Second)
	defer progressTicker.Stop()

	fmt.Printf("  Progress: ")
loop:
	for {
		select {
		case <-timeout:
			break loop
		case <-progressTicker.C:
			fmt.Printf(".")
		case <-ticker.C:
			// Build a larger packet (~1KB, simulating video frame)
			domain := fmt.Sprintf("frame%d.stream.local", frameNum)
			dnsQuery := buildDNSQuery(domain)
			// Pad to ~1KB
			padding := make([]byte, 800)
			for i := range padding {
				padding[i] = byte(i % 256)
			}
			dnsQuery = append(dnsQuery, padding...)
			packet := buildUDPPacket(*dnsServer, dnsQuery)

			start := time.Now()
			_, err := localUDP.WriteToUDP(packet, relayUDP)
			latency := time.Since(start)

			if err == nil {
				atomic.AddInt64(&sent, 1)
				latencyMu.Lock()
				latencies = append(latencies, latency)
				latencyMu.Unlock()
			}
			frameNum++
		}
	}
	fmt.Println(" done!")

	close(done)
	time.Sleep(500 * time.Millisecond) // Wait for final responses

	// Calculate statistics
	sentVal := atomic.LoadInt64(&sent)
	recvVal := atomic.LoadInt64(&received)

	fmt.Printf("\n  📊 UDP Streaming Results (%v):\n", *duration)
	fmt.Printf("  ┌───────────────────────────────────────────┐\n")
	fmt.Printf("  │  Frames Sent:     %5d                   │\n", sentVal)
	fmt.Printf("  │  Responses:       %5d                   │\n", recvVal)
	fmt.Printf("  │  Frame Rate:      %.1f fps               │\n", float64(sentVal)/duration.Seconds())
	fmt.Printf("  │  Data Sent:       %.2f MB                │\n", float64(sentVal)*1.0/1024)

	if len(latencies) > 0 {
		var sum time.Duration
		minL, maxL := latencies[0], latencies[0]
		for _, l := range latencies {
			sum += l
			if l < minL {
				minL = l
			}
			if l > maxL {
				maxL = l
			}
		}
		avgL := sum / time.Duration(len(latencies))

		fmt.Printf("  ├───────────────────────────────────────────┤\n")
		fmt.Printf("  │  Send Latency (local):                    │\n")
		fmt.Printf("  │    Min: %v                            │\n", minL.Round(time.Microsecond))
		fmt.Printf("  │    Avg: %v                            │\n", avgL.Round(time.Microsecond))
		fmt.Printf("  │    Max: %v                            │\n", maxL.Round(time.Microsecond))
	}
	fmt.Printf("  └───────────────────────────────────────────┘\n")

	// Overall rating for game streaming
	fmt.Printf("\n  🎮 Game Streaming Assessment:\n")
	
	fps := float64(sentVal) / duration.Seconds()
	if fps >= 55 {
		fmt.Printf("     Frame Rate: 🟢 Stable (%.1f fps)\n", fps)
	} else if fps >= 30 {
		fmt.Printf("     Frame Rate: 🟡 Acceptable (%.1f fps)\n", fps)
	} else {
		fmt.Printf("     Frame Rate: 🔴 Too low (%.1f fps)\n", fps)
	}

	// Note about QUIC Datagram
	fmt.Printf("\n  💡 Note: With QUIC Datagram enabled, UDP packets bypass\n")
	fmt.Printf("     retransmission logic for lower latency game streaming.\n")
}

// setupUDPRelay establishes UDP ASSOCIATE and returns connections
func setupUDPRelay() (net.Conn, string, *net.UDPConn, error) {
	tcpConn, err := net.DialTimeout("tcp", *proxyAddr, 10*time.Second)
	if err != nil {
		return nil, "", nil, fmt.Errorf("connect: %w", err)
	}

	if err := authenticate(tcpConn); err != nil {
		tcpConn.Close()
		return nil, "", nil, fmt.Errorf("auth: %w", err)
	}

	relayAddr, err := sendUDPAssociate(tcpConn)
	if err != nil {
		tcpConn.Close()
		return nil, "", nil, fmt.Errorf("udp associate: %w", err)
	}

	localUDP, err := net.ListenUDP("udp", &net.UDPAddr{Port: 0})
	if err != nil {
		tcpConn.Close()
		return nil, "", nil, fmt.Errorf("listen udp: %w", err)
	}

	fmt.Printf("  UDP Relay: %s\n", relayAddr)
	return tcpConn, relayAddr, localUDP, nil
}

// --- Helper functions ---

func authenticate(conn net.Conn) error {
	conn.SetDeadline(time.Now().Add(10 * time.Second))

	var methods []byte
	if *username != "" && *password != "" {
		methods = []byte{0x05, 0x02, 0x00, 0x02}
	} else {
		methods = []byte{0x05, 0x01, 0x00}
	}
	conn.Write(methods)

	resp := make([]byte, 2)
	io.ReadFull(conn, resp)

	if resp[0] != 0x05 {
		return fmt.Errorf("invalid SOCKS version")
	}

	if resp[1] == 0x02 {
		authReq := []byte{0x01, byte(len(*username))}
		authReq = append(authReq, []byte(*username)...)
		authReq = append(authReq, byte(len(*password)))
		authReq = append(authReq, []byte(*password)...)
		conn.Write(authReq)

		authResp := make([]byte, 2)
		io.ReadFull(conn, authResp)
		if authResp[1] != 0x00 {
			return fmt.Errorf("auth rejected")
		}
	} else if resp[1] != 0x00 {
		return fmt.Errorf("unsupported auth: %d", resp[1])
	}

	return nil
}

func sendConnect(conn net.Conn, addr string) error {
	host, portStr, _ := net.SplitHostPort(addr)
	port, _ := net.LookupPort("tcp", portStr)

	var req bytes.Buffer
	req.WriteByte(0x05)
	req.WriteByte(0x01)
	req.WriteByte(0x00)
	req.WriteByte(0x03)
	req.WriteByte(byte(len(host)))
	req.WriteString(host)

	portBytes := make([]byte, 2)
	binary.BigEndian.PutUint16(portBytes, uint16(port))
	req.Write(portBytes)

	conn.Write(req.Bytes())

	resp := make([]byte, 256)
	n, _ := conn.Read(resp)
	if n < 10 || resp[1] != 0x00 {
		return fmt.Errorf("failed: %d", resp[1])
	}

	return nil
}

func sendUDPAssociate(conn net.Conn) (string, error) {
	req := []byte{0x05, 0x03, 0x00, 0x01, 0, 0, 0, 0, 0, 0}
	conn.Write(req)

	resp := make([]byte, 256)
	n, _ := conn.Read(resp)
	if n < 10 || resp[1] != 0x00 {
		return "", fmt.Errorf("failed: %d", resp[1])
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
	p.Write([]byte{0x00, 0x00, 0x00})

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

// detectDNSServer reads the system DNS configuration and returns the first nameserver
func detectDNSServer() string {
	switch runtime.GOOS {
	case "darwin":
		out, err := exec.Command("scutil", "--dns").Output()
		if err == nil {
			scanner := bufio.NewScanner(bytes.NewReader(out))
			for scanner.Scan() {
				line := strings.TrimSpace(scanner.Text())
				if strings.HasPrefix(line, "nameserver[0]") {
					parts := strings.SplitN(line, ":", 2)
					if len(parts) == 2 {
						return strings.TrimSpace(parts[1]) + ":53"
					}
				}
			}
		}
	default:
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
