// Game Streaming Latency Test Tool
// Simulates Sunshine/Moonlight traffic patterns with high-throughput stress testing
//
// Test scenarios:
// 1. Control commands (TCP) - small packets, low latency critical
// 2. Video packets (UDP) - 1.4KB packets at various rates (60fps to 500Mbps stress)
// 3. Audio packets (UDP) - 400B packets at 100pps
// 4. Stress test - sustained high throughput (100Mbps, 200Mbps, 500Mbps)
//
// Usage:
//
//	go run ./tests/latency -proxy 47.107.181.88:1080 -user demo-group -pass demo-123 -test stress
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	proxyAddr  = flag.String("proxy", "127.0.0.1:1080", "SOCKS5 proxy address")
	username   = flag.String("user", "demo-group", "SOCKS5 username (group_id)")
	password   = flag.String("pass", "demo-123", "SOCKS5 password (group_password)")
	testType   = flag.String("test", "all", "Test type: control, video, audio, gamestream, stress, or all")
	httpPort   = flag.Int("http-port", 19080, "Local HTTP service port")
	udpPort    = flag.Int("udp-port", 19053, "Local UDP service port")
	count      = flag.Int("count", 20, "Number of test iterations")
	duration   = flag.Duration("duration", 10*time.Second, "Duration for streaming tests")
	timeout    = flag.Duration("timeout", 10*time.Second, "Timeout per request")
	bitrate    = flag.Int("bitrate", 100, "Target bitrate in Mbps for stress test")
	skipDirect = flag.Bool("skip-direct", false, "Skip direct throughput test (only run proxy test)")
)

func main() {
	flag.Parse()

	fmt.Println("╔══════════════════════════════════════════════════════════════════╗")
	fmt.Println("║   Game Streaming Latency & Throughput Test (500Mbps capable)     ║")
	fmt.Println("╠══════════════════════════════════════════════════════════════════╣")
	fmt.Println("║  Test modes:                                                     ║")
	fmt.Println("║  • control:    TCP control commands (128B)                       ║")
	fmt.Println("║  • video:      UDP video packets (1.4KB)                         ║")
	fmt.Println("║  • audio:      UDP audio packets (400B)                          ║")
	fmt.Println("║  • gamestream: Full simulation (60fps video + audio + control)   ║")
	fmt.Println("║  • stress:     High-throughput stress test (up to 500Mbps)       ║")
	fmt.Println("╚══════════════════════════════════════════════════════════════════╝")
	fmt.Printf("\nProxy: %s | User: %s\n", *proxyAddr, *username)
	fmt.Println()

	// Start local game streaming services
	fmt.Println("🎮 Starting local Sunshine-like services...")
	services, err := StartGameServices(*httpPort, *udpPort)
	if err != nil {
		fmt.Printf("❌ Failed to start services: %v\n", err)
		return
	}
	defer services.Stop()
	fmt.Printf("   HTTP (Control): http://%s\n", services.HTTPAddr())
	fmt.Printf("   TCP Echo (Large Packets): tcp://%s\n", services.TCPEchoAddr())
	fmt.Printf("   UDP (Video/Audio): udp://%s\n", services.UDPAddr())
	fmt.Println()
	time.Sleep(300 * time.Millisecond)

	switch *testType {
	case "control":
		runControlTest(services)
	case "video":
		runVideoTest(services)
	case "audio":
		runAudioTest(services)
	case "gamestream":
		runFullGameStreamTest(services)
	case "stress":
		runRealStressTest(services)
	case "oldstress":
		runStressTest(services)
	case "all":
		runControlTest(services)
		fmt.Println()
		runVideoTest(services)
		fmt.Println()
		runAudioTest(services)
		fmt.Println()
		runRealStressTest(services)
		fmt.Println()
		runTCPStressTest(services)
	default:
		fmt.Printf("Unknown test type: %s\n", *testType)
	}
}

// runStressTest performs high-throughput stress testing
func runStressTest(services *GameStreamServices) {
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("  🔥 HIGH-THROUGHPUT STRESS TEST")
	fmt.Println("  Testing sustained throughput at various bitrates")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	// Test different bitrates
	bitrates := []int{50, 100, 200, 300, 500}

	fmt.Println("\n  📍 DIRECT THROUGHPUT TEST (localhost baseline):")
	for _, br := range bitrates {
		result := runThroughputTest(services, br, false, 5*time.Second)
		printThroughputResult(br, result)
	}

	fmt.Println("\n  🌐 PROXY THROUGHPUT TEST (Gateway -> QUIC -> Client):")
	for _, br := range bitrates {
		result := runThroughputTest(services, br, true, 5*time.Second)
		printThroughputResult(br, result)
	}
}

type ThroughputResult struct {
	TargetBitrate   int
	ActualBitrate   float64
	PacketsSent     int64
	PacketsReceived int64
	BytesSent       int64
	Duration        time.Duration
	AvgLatency      time.Duration
	MaxLatency      time.Duration
	PacketLoss      float64
	ChecksumErrors  int64   // Packets with data corruption
	DataIntegrity   float64 // % of packets with correct data
}

// buildPacketWithValidation creates a packet with sequence number and checksum
// Format: [0:4] seqNum, [4:8] checksum (repeated), [8:] payload
func buildPacketWithValidation(seqNum uint32, payloadSize int) []byte {
	packet := make([]byte, payloadSize)

	// Sequence number (big-endian)
	packet[0] = byte(seqNum >> 24)
	packet[1] = byte(seqNum >> 16)
	packet[2] = byte(seqNum >> 8)
	packet[3] = byte(seqNum)

	// Fill payload with pattern based on sequence (for verification)
	for i := 8; i < payloadSize; i++ {
		packet[i] = byte((int(seqNum) + i) % 256)
	}

	// Calculate checksum of payload
	var checksum byte
	for i := 8; i < payloadSize; i++ {
		checksum ^= packet[i]
	}

	// Store checksum (repeated 4 times for redundancy)
	packet[4] = checksum
	packet[5] = checksum
	packet[6] = checksum
	packet[7] = checksum

	return packet
}

// verifyPacket checks if received packet has correct data
func verifyPacket(data []byte) (seqNum uint32, valid bool) {
	return verifyPacketWithOffset(data, 0)
}

// verifyPacketProxy handles SOCKS5 UDP response which has header before payload
// SOCKS5 UDP response: [rsv(2)][frag(1)][atyp(1)][addr(4/16)][port(2)][payload]
func verifyPacketProxy(data []byte) (seqNum uint32, valid bool) {
	if len(data) < 10 {
		return 0, false
	}

	// Determine offset based on address type
	offset := 0
	atyp := data[3]
	switch atyp {
	case 0x01: // IPv4
		offset = 10 // 2+1+1+4+2
	case 0x04: // IPv6
		offset = 22 // 2+1+1+16+2
	case 0x03: // Domain
		domainLen := int(data[4])
		offset = 5 + domainLen + 2
	default:
		return 0, false
	}

	return verifyPacketWithOffset(data, offset)
}

func verifyPacketWithOffset(data []byte, offset int) (seqNum uint32, valid bool) {
	seqNum, valid, _ = verifyPacketWithOffsetDebug(data, offset)
	return
}

var debugOnce sync.Once
var checksumDebugCount int32

func verifyPacketWithOffsetDebug(data []byte, offset int) (seqNum uint32, valid bool, errType int) {
	if len(data) < offset+8 {
		return 0, false, 2 // size error
	}

	payload := data[offset:]
	seqNum = uint32(payload[0])<<24 | uint32(payload[1])<<16 | uint32(payload[2])<<8 | uint32(payload[3])
	expectedChecksum := payload[4]

	// Verify payload pattern
	var actualChecksum byte
	for i := 8; i < len(payload); i++ {
		actualChecksum ^= payload[i]
	}

	if expectedChecksum != actualChecksum {
		// Debug: print first few checksum errors to understand the issue
		if atomic.AddInt32(&checksumDebugCount, 1) <= 5 {
			// Check if payload pattern matches expected
			patternMismatch := false
			for i := 8; i < len(payload) && i < 20; i++ {
				expected := byte((int(seqNum) + i) % 256)
				if payload[i] != expected {
					patternMismatch = true
					fmt.Printf("\n     [DEBUG] Checksum error seq=%d: offset=%d len=%d expected_cs=0x%02x actual_cs=0x%02x pattern_mismatch=%v\n",
						seqNum, offset, len(payload), expectedChecksum, actualChecksum, patternMismatch)
					fmt.Printf("             payload[8:16]=%x expected=%x\n",
						payload[8:min(16, len(payload))],
						[]byte{byte((int(seqNum) + 8) % 256), byte((int(seqNum) + 9) % 256), byte((int(seqNum) + 10) % 256), byte((int(seqNum) + 11) % 256)})
					break
				}
			}
		}
		return seqNum, false, 1 // checksum error
	}
	return seqNum, true, 0
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Debug versions that return error type
func verifyPacketDebug(data []byte) (seqNum uint32, valid bool, errType int) {
	return verifyPacketWithOffsetDebug(data, 0)
}

var proxyDebugOnce sync.Once

func verifyPacketProxyDebug(data []byte) (seqNum uint32, valid bool, errType int) {
	if len(data) < 10 {
		return 0, false, 2
	}

	offset := 0
	atyp := data[3]
	switch atyp {
	case 0x01:
		offset = 10
	case 0x04:
		offset = 22
	case 0x03:
		domainLen := int(data[4])
		offset = 5 + domainLen + 2
	default:
		// Debug: print unknown ATYP
		proxyDebugOnce.Do(func() {
			fmt.Printf("\n     [DEBUG] Unknown ATYP: 0x%02x, data[:20]=%x\n", atyp, data[:min(20, len(data))])
		})
		return 0, false, 2
	}

	seqNum, valid, errType = verifyPacketWithOffsetDebug(data, offset)

	// Additional debug for checksum errors
	if errType == 1 && atomic.LoadInt32(&checksumDebugCount) <= 5 {
		fmt.Printf("     [DEBUG-PROXY] offset=%d atyp=0x%02x header=%x\n", offset, atyp, data[:offset])
	}

	return
}

func runThroughputTest(services *GameStreamServices, targetMbps int, useProxy bool, testDuration time.Duration) *ThroughputResult {
	result := &ThroughputResult{
		TargetBitrate: targetMbps,
	}

	packetSize := 1400 // bytes

	// Scale workers based on target throughput
	numSenders := 4
	numReceivers := 4
	batchSize := 1

	if targetMbps >= 50 {
		numSenders = 8
		numReceivers = 8
		batchSize = 5
	}
	if targetMbps >= 100 {
		numSenders = 16
		numReceivers = 8
		batchSize = 10
	}
	if targetMbps >= 200 {
		numSenders = 32
		numReceivers = 16
		batchSize = 20
	}
	if targetMbps >= 400 {
		numSenders = 64
		numReceivers = 32
		batchSize = 50
	}

	// Calculate send interval
	bytesPerSec := float64(targetMbps) * 1000000 / 8 / float64(numSenders)
	packetsPerSec := bytesPerSec / float64(packetSize) / float64(batchSize)
	sendInterval := time.Duration(float64(time.Second) / packetsPerSec)
	if sendInterval < time.Microsecond*100 {
		sendInterval = time.Microsecond * 100
	}

	var conn net.Conn
	var localUDP *net.UDPConn
	var relayAddr string
	var targetAddr *net.UDPAddr
	var err error

	if useProxy {
		conn, localUDP, relayAddr, err = setupUDPProxy()
		if err != nil {
			fmt.Printf("     ❌ Proxy setup failed: %v\n", err)
			return result
		}
		defer conn.Close()
		defer localUDP.Close()
	} else {
		targetAddr, _ = net.ResolveUDPAddr("udp", services.UDPAddr())
		localUDP, err = net.ListenUDP("udp", &net.UDPAddr{Port: 0})
		if err != nil {
			fmt.Printf("     ❌ UDP setup failed: %v\n", err)
			return result
		}
		defer localUDP.Close()
	}

	// Large buffers for high throughput
	localUDP.SetReadBuffer(64 * 1024 * 1024)
	localUDP.SetWriteBuffer(64 * 1024 * 1024)

	var sent, received, validReceived int64
	done := make(chan struct{})
	var wg sync.WaitGroup
	var seqCounter uint32

	// Multiple receiver goroutines
	for i := 0; i < numReceivers; i++ {
		wg.Add(1)
		go func(isProxy bool) {
			defer wg.Done()
			buf := make([]byte, 2048)
			for {
				select {
				case <-done:
					return
				default:
					localUDP.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
					n, _, err := localUDP.ReadFromUDP(buf)
					if err != nil {
						continue
					}
					if n > 0 {
						atomic.AddInt64(&received, 1)
						var valid bool
						if isProxy {
							_, valid = verifyPacketProxy(buf[:n])
						} else {
							_, valid = verifyPacket(buf[:n])
						}
						if valid {
							atomic.AddInt64(&validReceived, 1)
						}
					}
				}
			}
		}(useProxy)
	}

	var relayUDP *net.UDPAddr
	if useProxy {
		relayUDP, _ = net.ResolveUDPAddr("udp", relayAddr)
	}

	startTime := time.Now()

	// Multiple sender goroutines with batch sending
	for s := 0; s < numSenders; s++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			// Pre-allocate packet buffer
			packet := make([]byte, packetSize)
			var socks5Header []byte
			if useProxy {
				socks5Header = buildUDPPacket(services.UDPAddr(), make([]byte, 0))
			}

			ticker := time.NewTicker(sendInterval)
			defer ticker.Stop()

			for {
				select {
				case <-done:
					return
				case <-ticker.C:
					// Send batch of packets
					for b := 0; b < batchSize; b++ {
						seq := atomic.AddUint32(&seqCounter, 1)

						// Update packet in-place
						packet[0] = byte(seq >> 24)
						packet[1] = byte(seq >> 16)
						packet[2] = byte(seq >> 8)
						packet[3] = byte(seq)

						var checksum byte
						for j := 8; j < packetSize; j++ {
							packet[j] = byte((int(seq) + j) % 256)
							checksum ^= packet[j]
						}
						packet[4], packet[5], packet[6], packet[7] = checksum, checksum, checksum, checksum

						if useProxy {
							udpPacket := append(socks5Header, packet...)
							localUDP.WriteToUDP(udpPacket, relayUDP)
						} else {
							localUDP.WriteToUDP(packet, targetAddr)
						}

						atomic.AddInt64(&sent, 1)
					}
				}
			}
		}()
	}

	// Progress reporting
	progressTicker := time.NewTicker(time.Second)
	go func() {
		for {
			select {
			case <-done:
				return
			case <-progressTicker.C:
				s := atomic.LoadInt64(&sent)
				r := atomic.LoadInt64(&received)
				elapsed := time.Since(startTime).Seconds()
				currentMbps := float64(s) * float64(packetSize) * 8 / elapsed / 1000000
				fmt.Printf("\r     [%ds] Sent: %d | Recv: %d | %.1f Mbps    ",
					int(elapsed), s, r, currentMbps)
			}
		}
	}()

	time.Sleep(testDuration)
	close(done)
	progressTicker.Stop()

	// 🔧 Drain phase: receive remaining in-flight packets
	drainStart := time.Now()
	drainTimeout := 500 * time.Millisecond
	buf := make([]byte, 2048)
	for time.Since(drainStart) < drainTimeout {
		localUDP.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
		n, _, err := localUDP.ReadFromUDP(buf)
		if err != nil {
			continue
		}
		atomic.AddInt64(&received, 1)
		var valid bool
		if useProxy {
			_, valid = verifyPacketProxy(buf[:n])
		} else {
			_, valid = verifyPacket(buf[:n])
		}
		if valid {
			atomic.AddInt64(&validReceived, 1)
		}
	}

	wg.Wait()

	fmt.Println() // New line after progress

	result.Duration = time.Since(startTime) - drainTimeout
	result.PacketsSent = atomic.LoadInt64(&sent)
	result.PacketsReceived = atomic.LoadInt64(&received)
	result.BytesSent = result.PacketsSent * int64(packetSize)
	result.ActualBitrate = float64(result.BytesSent) * 8 / result.Duration.Seconds() / 1000000

	if result.PacketsSent > 0 {
		result.PacketLoss = float64(result.PacketsSent-result.PacketsReceived) / float64(result.PacketsSent) * 100
	}

	// Data integrity stats
	valid := atomic.LoadInt64(&validReceived)
	if result.PacketsReceived > 0 {
		result.DataIntegrity = float64(valid) / float64(result.PacketsReceived) * 100
		result.ChecksumErrors = result.PacketsReceived - valid
	}

	return result
}

func printThroughputResult(targetMbps int, r *ThroughputResult) {
	status := "✅"
	if r.PacketLoss > 5 || r.DataIntegrity < 99 {
		status = "⚠️"
	}
	if r.PacketLoss > 20 || r.ActualBitrate < float64(targetMbps)*0.5 || r.DataIntegrity < 90 {
		status = "❌"
	}

	integrityStr := "N/A"
	if r.PacketsReceived > 0 {
		integrityStr = fmt.Sprintf("%.1f%%", r.DataIntegrity)
	}

	fmt.Printf("     %s %3dMbps: %.1fMbps actual | %d/%d pkts (%.1f%% loss) | integrity: %s\n",
		status, targetMbps, r.ActualBitrate,
		r.PacketsReceived, r.PacketsSent, r.PacketLoss, integrityStr)
}

// runControlTest tests TCP control command latency (gamepad inputs)
func runControlTest(services *GameStreamServices) {
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("  🎮 Control Command Latency Test (128B TCP)")
	fmt.Println("  Simulates: Gamepad button press -> Server ack")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	target := services.HTTPAddr() + "/control"

	fmt.Println("\n  📍 DIRECT (localhost):")
	directLatencies := testHTTPDirect(target, *count)
	printStats("Direct", directLatencies)

	fmt.Println("\n  🌐 VIA PROXY (Gateway -> QUIC -> Client -> localhost):")
	proxyLatencies := testHTTPProxy(target, *count)
	printStats("Proxy", proxyLatencies)

	printComparison("Control Command", directLatencies, proxyLatencies)
}

// runVideoTest tests UDP video frame latency
func runVideoTest(services *GameStreamServices) {
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("  📺 Video Packet Latency Test (1.4KB UDP)")
	fmt.Println("  Simulates: Video packet transmission")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	target := services.UDPAddr()

	fmt.Println("\n  📍 DIRECT (localhost):")
	directLatencies := testUDPDirect(target, VideoFrameSize, *count)
	printStats("Direct", directLatencies)

	fmt.Println("\n  🌐 VIA PROXY (Gateway -> QUIC -> Client -> localhost):")
	proxyLatencies := testUDPProxy(target, VideoFrameSize, *count)
	printStats("Proxy", proxyLatencies)

	printComparison("Video Packet", directLatencies, proxyLatencies)
}

// runAudioTest tests UDP audio packet latency
func runAudioTest(services *GameStreamServices) {
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("  🔊 Audio Packet Latency Test (400B UDP)")
	fmt.Println("  Simulates: Audio stream packets")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	target := services.UDPAddr()

	fmt.Println("\n  📍 DIRECT (localhost):")
	directLatencies := testUDPDirect(target, AudioPacketSize, *count)
	printStats("Direct", directLatencies)

	fmt.Println("\n  🌐 VIA PROXY (Gateway -> QUIC -> Client -> localhost):")
	proxyLatencies := testUDPProxy(target, AudioPacketSize, *count)
	printStats("Proxy", proxyLatencies)

	printComparison("Audio Packet", directLatencies, proxyLatencies)
}

// runFullGameStreamTest simulates full game streaming session
func runFullGameStreamTest(services *GameStreamServices) {
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Printf("  🎮 Full Game Streaming Simulation (%v)\n", *duration)
	fmt.Println("  Traffic: 60fps video + 100pps audio + control")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	fmt.Println("\n  📍 DIRECT STREAMING (localhost):")
	directStats := runStreamingSession(services, false)

	fmt.Println("\n  🌐 PROXY STREAMING (Gateway -> QUIC -> Client):")
	proxyStats := runStreamingSession(services, true)

	printStreamingComparison(directStats, proxyStats)
}

type StreamStats struct {
	VideoFramesSent      int64
	VideoFramesReceived  int64
	VideoLatencies       []time.Duration
	AudioPacketsSent     int64
	AudioPacketsReceived int64
	AudioLatencies       []time.Duration
	ControlCmdsSent      int64
	ControlCmdsReceived  int64
	ControlLatencies     []time.Duration
	TotalBytesSent       int64
	Duration             time.Duration
}

func runStreamingSession(services *GameStreamServices, useProxy bool) *StreamStats {
	stats := &StreamStats{}
	var wg sync.WaitGroup
	done := make(chan struct{})
	var mu sync.Mutex

	startTime := time.Now()

	// Video streaming goroutine (60fps)
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(16666 * time.Microsecond)
		defer ticker.Stop()

		var conn net.Conn
		var localUDP *net.UDPConn
		var relayAddr string

		if useProxy {
			var err error
			conn, localUDP, relayAddr, err = setupUDPProxy()
			if err != nil {
				return
			}
			defer conn.Close()
			defer localUDP.Close()
		}

		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				frame := make([]byte, VideoFrameSize)
				start := time.Now()
				var err error

				if useProxy {
					err = sendUDPViaProxy(localUDP, relayAddr, services.UDPAddr(), frame)
				} else {
					err = sendUDPDirect(services.UDPAddr(), frame)
				}

				atomic.AddInt64(&stats.VideoFramesSent, 1)
				atomic.AddInt64(&stats.TotalBytesSent, int64(len(frame)))

				if err == nil {
					latency := time.Since(start)
					mu.Lock()
					stats.VideoLatencies = append(stats.VideoLatencies, latency)
					mu.Unlock()
					atomic.AddInt64(&stats.VideoFramesReceived, 1)
				}
			}
		}
	}()

	// Audio streaming goroutine (100pps)
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()

		var conn net.Conn
		var localUDP *net.UDPConn
		var relayAddr string

		if useProxy {
			var err error
			conn, localUDP, relayAddr, err = setupUDPProxy()
			if err != nil {
				return
			}
			defer conn.Close()
			defer localUDP.Close()
		}

		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				packet := make([]byte, AudioPacketSize)
				start := time.Now()
				var err error

				if useProxy {
					err = sendUDPViaProxy(localUDP, relayAddr, services.UDPAddr(), packet)
				} else {
					err = sendUDPDirect(services.UDPAddr(), packet)
				}

				atomic.AddInt64(&stats.AudioPacketsSent, 1)
				atomic.AddInt64(&stats.TotalBytesSent, int64(len(packet)))

				if err == nil {
					latency := time.Since(start)
					mu.Lock()
					stats.AudioLatencies = append(stats.AudioLatencies, latency)
					mu.Unlock()
					atomic.AddInt64(&stats.AudioPacketsReceived, 1)
				}
			}
		}
	}()

	// Control command goroutine (20/sec)
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()

		target := services.HTTPAddr() + "/control"

		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				start := time.Now()
				var err error

				if useProxy {
					err = doHTTPViaProxy(target)
				} else {
					resp, e := http.Get("http://" + target)
					if e == nil {
						io.ReadAll(resp.Body)
						resp.Body.Close()
					}
					err = e
				}

				atomic.AddInt64(&stats.ControlCmdsSent, 1)
				atomic.AddInt64(&stats.TotalBytesSent, ControlCmdSize)

				if err == nil {
					latency := time.Since(start)
					mu.Lock()
					stats.ControlLatencies = append(stats.ControlLatencies, latency)
					mu.Unlock()
					atomic.AddInt64(&stats.ControlCmdsReceived, 1)
				}
			}
		}
	}()

	// Progress
	progressTicker := time.NewTicker(1 * time.Second)
	go func() {
		for {
			select {
			case <-done:
				return
			case <-progressTicker.C:
				elapsed := time.Since(startTime)
				vSent := atomic.LoadInt64(&stats.VideoFramesSent)
				aSent := atomic.LoadInt64(&stats.AudioPacketsSent)
				cSent := atomic.LoadInt64(&stats.ControlCmdsSent)
				fmt.Printf("\r     [%v] Video: %d | Audio: %d | Control: %d",
					elapsed.Round(time.Second), vSent, aSent, cSent)
			}
		}
	}()

	time.Sleep(*duration)
	close(done)
	progressTicker.Stop()
	wg.Wait()

	stats.Duration = time.Since(startTime)
	fmt.Println()

	vRecv := atomic.LoadInt64(&stats.VideoFramesReceived)
	vSent := atomic.LoadInt64(&stats.VideoFramesSent)
	aRecv := atomic.LoadInt64(&stats.AudioPacketsReceived)
	aSent := atomic.LoadInt64(&stats.AudioPacketsSent)
	cRecv := atomic.LoadInt64(&stats.ControlCmdsReceived)
	cSent := atomic.LoadInt64(&stats.ControlCmdsSent)

	fmt.Printf("     📊 Session Results:\n")
	fmt.Printf("     ┌──────────────────────────────────────────────────┐\n")
	fmt.Printf("     │ Video:   %4d/%4d (%.1f%% success, %.1f fps)      │\n",
		vRecv, vSent, float64(vRecv)/float64(vSent)*100, float64(vSent)/stats.Duration.Seconds())
	fmt.Printf("     │ Audio:   %4d/%4d (%.1f%% success)                │\n",
		aRecv, aSent, float64(aRecv)/float64(aSent)*100)
	fmt.Printf("     │ Control: %4d/%4d (%.1f%% success)                │\n",
		cRecv, cSent, float64(cRecv)/float64(cSent)*100)
	fmt.Printf("     │ Data:    %.2f MB sent                            │\n",
		float64(stats.TotalBytesSent)/1024/1024)
	fmt.Printf("     └──────────────────────────────────────────────────┘\n")

	return stats
}

func printStreamingComparison(direct, proxy *StreamStats) {
	fmt.Println()
	fmt.Println("  ╔════════════════════════════════════════════════════════════════╗")
	fmt.Println("  ║               🎮 GAME STREAMING COMPARISON                     ║")
	fmt.Println("  ╠════════════════════════════════════════════════════════════════╣")

	if len(direct.VideoLatencies) > 0 && len(proxy.VideoLatencies) > 0 {
		directAvg := avgLatency(direct.VideoLatencies)
		proxyAvg := avgLatency(proxy.VideoLatencies)
		overhead := proxyAvg - directAvg
		fmt.Printf("  ║ Video:   Direct %8v | Proxy %8v | +%v      ║\n",
			directAvg.Round(time.Millisecond), proxyAvg.Round(time.Millisecond), overhead.Round(time.Millisecond))
	}

	if len(direct.AudioLatencies) > 0 && len(proxy.AudioLatencies) > 0 {
		directAvg := avgLatency(direct.AudioLatencies)
		proxyAvg := avgLatency(proxy.AudioLatencies)
		overhead := proxyAvg - directAvg
		fmt.Printf("  ║ Audio:   Direct %8v | Proxy %8v | +%v      ║\n",
			directAvg.Round(time.Millisecond), proxyAvg.Round(time.Millisecond), overhead.Round(time.Millisecond))
	}

	if len(direct.ControlLatencies) > 0 && len(proxy.ControlLatencies) > 0 {
		directAvg := avgLatency(direct.ControlLatencies)
		proxyAvg := avgLatency(proxy.ControlLatencies)
		overhead := proxyAvg - directAvg
		fmt.Printf("  ║ Control: Direct %8v | Proxy %8v | +%v      ║\n",
			directAvg.Round(time.Millisecond), proxyAvg.Round(time.Millisecond), overhead.Round(time.Millisecond))
	}

	fmt.Println("  ╠════════════════════════════════════════════════════════════════╣")

	directVideoLoss := 100 - float64(direct.VideoFramesReceived)/float64(direct.VideoFramesSent)*100
	proxyVideoLoss := 100 - float64(proxy.VideoFramesReceived)/float64(proxy.VideoFramesSent)*100

	fmt.Printf("  ║ Video Loss: Direct %.1f%% | Proxy %.1f%%                        ║\n",
		directVideoLoss, proxyVideoLoss)

	fmt.Println("  ╚════════════════════════════════════════════════════════════════╝")

	if len(proxy.ControlLatencies) > 0 {
		proxyControlAvg := avgLatency(proxy.ControlLatencies)
		if proxyControlAvg < 100*time.Millisecond && proxyVideoLoss < 10 {
			fmt.Println("  🟢 EXCELLENT - Suitable for game streaming")
		} else if proxyControlAvg < 200*time.Millisecond && proxyVideoLoss < 30 {
			fmt.Println("  🟡 GOOD - Playable with occasional lag")
		} else if proxyControlAvg < 300*time.Millisecond {
			fmt.Println("  🟠 FAIR - Noticeable input lag")
		} else {
			fmt.Println("  🔴 POOR - Not suitable for game streaming")
		}
	}
}

// --- Test helper functions ---

func testHTTPDirect(target string, count int) []time.Duration {
	var latencies []time.Duration
	url := "http://" + target

	for i := 0; i < count; i++ {
		start := time.Now()
		resp, err := http.Get(url)
		if err != nil {
			fmt.Printf("     [%2d] ❌ %v\n", i+1, err)
			continue
		}
		io.ReadAll(resp.Body)
		resp.Body.Close()
		elapsed := time.Since(start)
		latencies = append(latencies, elapsed)
		fmt.Printf("     [%2d] ✓ %v\n", i+1, elapsed.Round(time.Microsecond))
	}
	return latencies
}

func testHTTPProxy(target string, count int) []time.Duration {
	var latencies []time.Duration

	for i := 0; i < count; i++ {
		start := time.Now()
		err := doHTTPViaProxy(target)
		elapsed := time.Since(start)

		if err != nil {
			fmt.Printf("     [%2d] ❌ %v\n", i+1, err)
			continue
		}
		latencies = append(latencies, elapsed)
		fmt.Printf("     [%2d] ✓ %v\n", i+1, elapsed.Round(time.Microsecond))
	}
	return latencies
}

func testUDPDirect(target string, size, count int) []time.Duration {
	var latencies []time.Duration

	addr, _ := net.ResolveUDPAddr("udp", target)
	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		fmt.Printf("     ❌ Connect failed: %v\n", err)
		return nil
	}
	defer conn.Close()
	conn.SetReadBuffer(2 * 1024 * 1024)
	conn.SetWriteBuffer(2 * 1024 * 1024)

	payload := make([]byte, size)

	for i := 0; i < count; i++ {
		start := time.Now()
		conn.SetWriteDeadline(time.Now().Add(*timeout))
		conn.Write(payload)

		conn.SetReadDeadline(time.Now().Add(*timeout))
		buf := make([]byte, size+1024)
		n, err := conn.Read(buf)
		elapsed := time.Since(start)

		if err != nil || n < 8 {
			fmt.Printf("     [%2d] ❌ %v\n", i+1, err)
			continue
		}

		latencies = append(latencies, elapsed)
		fmt.Printf("     [%2d] ✓ %v (%d bytes)\n", i+1, elapsed.Round(time.Microsecond), n)
	}
	return latencies
}

func testUDPProxy(target string, size, count int) []time.Duration {
	var latencies []time.Duration

	tcpConn, localUDP, relayAddr, err := setupUDPProxy()
	if err != nil {
		fmt.Printf("     ❌ Setup failed: %v\n", err)
		return nil
	}
	defer tcpConn.Close()
	defer localUDP.Close()

	relayUDP, _ := net.ResolveUDPAddr("udp", relayAddr)
	payload := make([]byte, size)

	for i := 0; i < count; i++ {
		packet := buildUDPPacket(target, payload)

		start := time.Now()
		localUDP.WriteToUDP(packet, relayUDP)

		localUDP.SetReadDeadline(time.Now().Add(*timeout))
		buf := make([]byte, size+1024)
		n, _, err := localUDP.ReadFromUDP(buf)
		elapsed := time.Since(start)

		if err != nil || n < 10 {
			fmt.Printf("     [%2d] ❌ %v\n", i+1, err)
			continue
		}

		latencies = append(latencies, elapsed)
		fmt.Printf("     [%2d] ✓ %v (%d bytes)\n", i+1, elapsed.Round(time.Microsecond), n)
	}
	return latencies
}

func sendUDPDirect(target string, data []byte) error {
	addr, _ := net.ResolveUDPAddr("udp", target)
	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		return err
	}
	defer conn.Close()

	conn.SetWriteDeadline(time.Now().Add(1 * time.Second))
	conn.Write(data)

	conn.SetReadDeadline(time.Now().Add(1 * time.Second))
	buf := make([]byte, len(data)+1024)
	_, err = conn.Read(buf)
	return err
}

func sendUDPViaProxy(localUDP *net.UDPConn, relayAddr, target string, data []byte) error {
	relayUDP, _ := net.ResolveUDPAddr("udp", relayAddr)
	packet := buildUDPPacket(target, data)

	localUDP.SetWriteDeadline(time.Now().Add(5 * time.Second))
	localUDP.WriteToUDP(packet, relayUDP)

	// Longer timeout for large packets that may go through QUIC Stream (reliable but slower)
	localUDP.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, len(data)+1024)
	_, _, err := localUDP.ReadFromUDP(buf)
	return err
}

func setupUDPProxy() (net.Conn, *net.UDPConn, string, error) {
	tcpConn, err := net.DialTimeout("tcp", *proxyAddr, 10*time.Second)
	if err != nil {
		return nil, nil, "", fmt.Errorf("connect: %w", err)
	}

	if err := authenticate(tcpConn); err != nil {
		tcpConn.Close()
		return nil, nil, "", fmt.Errorf("auth: %w", err)
	}

	relayAddr, err := sendUDPAssociate(tcpConn)
	if err != nil {
		tcpConn.Close()
		return nil, nil, "", fmt.Errorf("udp associate: %w", err)
	}

	localUDP, err := net.ListenUDP("udp", &net.UDPAddr{Port: 0})
	if err != nil {
		tcpConn.Close()
		return nil, nil, "", fmt.Errorf("listen udp: %w", err)
	}
	// 🔧 PERFORMANCE: Increased buffer for high-throughput tests
	localUDP.SetReadBuffer(64 * 1024 * 1024)
	localUDP.SetWriteBuffer(64 * 1024 * 1024)

	// 🔧 Wait briefly for server UDP session to be fully ready
	time.Sleep(10 * time.Millisecond)

	return tcpConn, localUDP, relayAddr, nil
}

func doHTTPViaProxy(target string) error {
	conn, err := net.DialTimeout("tcp", *proxyAddr, 10*time.Second)
	if err != nil {
		return fmt.Errorf("connect proxy: %w", err)
	}
	defer conn.Close()

	if err := authenticate(conn); err != nil {
		return fmt.Errorf("auth: %w", err)
	}

	host := target
	port := "80"
	if idx := strings.Index(target, "/"); idx > 0 {
		host = target[:idx]
	}
	if h, p, err := net.SplitHostPort(host); err == nil {
		host = h
		port = p
	}

	if err := sendConnect(conn, net.JoinHostPort(host, port)); err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	path := "/"
	if idx := strings.Index(target, "/"); idx > 0 {
		path = target[idx:]
	}

	req := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", path, host)
	conn.Write([]byte(req))

	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	resp, err := io.ReadAll(conn)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}

	if !strings.Contains(string(resp), "HTTP/") {
		return fmt.Errorf("invalid response")
	}
	return nil
}

func printStats(name string, latencies []time.Duration) {
	if len(latencies) == 0 {
		fmt.Printf("     ⚠️  No successful tests\n")
		return
	}

	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })

	var sum time.Duration
	for _, l := range latencies {
		sum += l
	}
	avg := sum / time.Duration(len(latencies))
	minL := latencies[0]
	maxL := latencies[len(latencies)-1]
	p50 := latencies[len(latencies)*50/100]
	p90 := latencies[len(latencies)*90/100]

	fmt.Printf("\n     📊 %s (%d/%d success)\n", name, len(latencies), *count)
	fmt.Printf("     ┌────────────────────────┐\n")
	fmt.Printf("     │ Min:  %10v       │\n", minL.Round(time.Microsecond))
	fmt.Printf("     │ Avg:  %10v       │\n", avg.Round(time.Microsecond))
	fmt.Printf("     │ P50:  %10v       │\n", p50.Round(time.Microsecond))
	fmt.Printf("     │ P90:  %10v       │\n", p90.Round(time.Microsecond))
	fmt.Printf("     │ Max:  %10v       │\n", maxL.Round(time.Microsecond))
	fmt.Printf("     └────────────────────────┘\n")
}

func printComparison(name string, direct, proxy []time.Duration) {
	if len(direct) == 0 || len(proxy) == 0 {
		return
	}

	directAvg := avgLatency(direct)
	proxyAvg := avgLatency(proxy)
	overhead := proxyAvg - directAvg
	ratio := float64(proxyAvg) / float64(directAvg)

	fmt.Println()
	fmt.Printf("  ╔═══════════════════════════════════════════════════════╗\n")
	fmt.Printf("  ║          📈 %s COMPARISON                ║\n", name)
	fmt.Printf("  ╠═══════════════════════════════════════════════════════╣\n")
	fmt.Printf("  ║  Direct:    %10v                                ║\n", directAvg.Round(time.Microsecond))
	fmt.Printf("  ║  Proxy:     %10v                                ║\n", proxyAvg.Round(time.Microsecond))
	fmt.Printf("  ║  Overhead:  %10v                                ║\n", overhead.Round(time.Microsecond))
	fmt.Printf("  ║  Ratio:     %.2fx                                     ║\n", ratio)
	fmt.Printf("  ╚═══════════════════════════════════════════════════════╝\n")

	if overhead < 50*time.Millisecond {
		fmt.Println("  🟢 Excellent proxy performance")
	} else if overhead < 150*time.Millisecond {
		fmt.Println("  🟡 Good proxy performance")
	} else if overhead < 300*time.Millisecond {
		fmt.Println("  🟠 Acceptable proxy performance")
	} else {
		fmt.Println("  🔴 High proxy overhead")
	}
}

func avgLatency(latencies []time.Duration) time.Duration {
	if len(latencies) == 0 {
		return 0
	}
	var sum time.Duration
	for _, l := range latencies {
		sum += l
	}
	return sum / time.Duration(len(latencies))
}

// --- SOCKS5 helpers ---

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
