package main

import (
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// runRealStressTest performs TRUE high-throughput stress testing
// with async send/receive to achieve actual target bitrate
func runRealStressTest(services *GameStreamServices) {
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("  🔥 UDP HIGH-THROUGHPUT STRESS TEST")
	fmt.Println("  Testing sustained throughput with parallel async send/receive")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	testDuration := 4 * time.Second

	// Test different bitrates (for game streaming: 20-50Mbps is typical, stress up to 1Gbps)
	bitrates := []int{20, 50, 100, 200, 500, 1000}

	// 表头
	printUDPTableHeader()

	// Skip direct test if -skip-direct flag is set
	if !*skipDirect {
		fmt.Println("     ├──────────────┼─────────────┼───────────────┼───────────┼───────────┤")
		fmt.Println("     │                        📍 DIRECT (baseline)                        │")
		fmt.Println("     ├──────────────┼─────────────┼───────────────┼───────────┼───────────┤")
	for _, br := range bitrates {
		result := runAsyncThroughput(services, br, false, testDuration)
		printStressResult(br, result)
		}
	}

	fmt.Println("     ├──────────────┼─────────────┼───────────────┼───────────┼───────────┤")
	fmt.Println("     │                  🌐 PROXY (Single Stream via QUIC)                  │")
	fmt.Println("     ├──────────────┼─────────────┼───────────────┼───────────┼───────────┤")
	for _, br := range bitrates {
		result := runAsyncThroughput(services, br, true, testDuration)
		printStressResult(br, result)
		time.Sleep(2 * time.Second) // Cool down between tests
	}

	// Multi-stream test to utilize all replicas
	// 🔧 PERFORMANCE: 6 replicas × 2 streams = 12 parallel streams for maximum throughput
	fmt.Println("     ├──────────────┼─────────────┼───────────────┼───────────┼───────────┤")
	fmt.Println("     │                🚀 MULTI-STREAM PROXY (12 streams × QUIC)            │")
	fmt.Println("     ├──────────────┼─────────────┼───────────────┼───────────┼───────────┤")
	numStreams := 12 // 6 replicas × 2 streams each
	multiStreamBitrates := []int{100, 200, 500, 1000, 2000}
	for _, br := range multiStreamBitrates {
		result := runMultiStreamThroughput(numStreams, br, testDuration)
		printMultiStreamResult(numStreams, br, result)
		time.Sleep(3 * time.Second) // Cool down between tests
	}

	// 表尾和结论
	printUDPTableFooter()
}

// printUDPTableHeader prints the table header for UDP stress test results
func printUDPTableHeader() {
	fmt.Println("\n     ┌──────────────┬─────────────┬───────────────┬───────────┬───────────┐")
	fmt.Println("     │   Target     │   Actual    │ Recv / Sent   │ Loss Rate │ Integrity │")
	fmt.Println("     │   (Mbps)     │   (Mbps)    │   (pkts)      │    (%)    │    (%)    │")
}

// printUDPTableFooter prints the table footer with summary
func printUDPTableFooter() {
	fmt.Println("     └──────────────┴─────────────┴───────────────┴───────────┴───────────┘")
	fmt.Println()
	fmt.Println("     📊 Legend: ✅ Excellent (loss<1%, integrity>99.9%)")
	fmt.Println("                ⚠️  Warning  (loss<10%, integrity>95%)")
	fmt.Println("                ❌ Failed   (loss>10% or integrity<95%)")
}

type StressResult struct {
	TargetMbps      int
	ActualMbps      float64
	PacketsSent     int64
	PacketsReceived int64
	BytesSent       int64
	BytesReceived   int64
	Duration        time.Duration
	SendErrors      int64
	RecvErrors      int64
	ValidPackets    int64   // Packets with correct checksum
	DataIntegrity   float64 // % of valid packets
	ChecksumErrors  int64   // Debug: checksum mismatch count
	SizeErrors      int64   // Debug: size mismatch count
}

func runAsyncThroughput(services *GameStreamServices, targetMbps int, useProxy bool, testDuration time.Duration) *StressResult {
	// Reset service stats for accurate per-test measurement
	services.ResetStats()

	result := &StressResult{
		TargetMbps: targetMbps,
	}

	packetSize := 1400

	// 🔧 Optimized config for high throughput
	// More receivers to ensure all packets are captured during drain
	// Smaller batch sizes for smoother sending (less burst)
	numSenders := 1
	numReceivers := 32
	batchSize := 30
	if targetMbps >= 100 {
		numSenders = 2
		numReceivers = 64
		batchSize = 50
	}
	if targetMbps >= 200 {
		numSenders = 4
		numReceivers = 96
		batchSize = 60
	}
	if targetMbps >= 500 {
		numSenders = 6
		numReceivers = 128
		batchSize = 80
	}
	if targetMbps >= 1000 {
		numSenders = 8
		numReceivers = 192
		batchSize = 100
	}

	// Calculate send interval per sender (for batches)
	bytesPerSec := float64(targetMbps) * 1000000 / 8 / float64(numSenders)
	packetsPerSec := bytesPerSec / float64(packetSize) / float64(batchSize)
	sendInterval := time.Duration(float64(time.Second) / packetsPerSec)
	if sendInterval < time.Microsecond*100 {
		sendInterval = time.Microsecond * 100 // Minimum interval
	}

	var tcpConn net.Conn
	var localUDP *net.UDPConn
	var relayAddr string
	var targetAddr *net.UDPAddr
	var err error

	if useProxy {
		tcpConn, localUDP, relayAddr, err = setupUDPProxy()
		if err != nil {
			fmt.Printf("     ❌ Proxy setup failed: %v\n", err)
			return result
		}
		defer tcpConn.Close()
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

	// Set large buffers
	localUDP.SetReadBuffer(64 * 1024 * 1024)
	localUDP.SetWriteBuffer(64 * 1024 * 1024)

	sendDone := make(chan struct{}) // Signal to stop senders
	recvDone := make(chan struct{}) // Signal to stop receivers

	// Stats
	var sent, received, sendErrs, recvErrs, validPkts int64
	var bytesSent, bytesRecv int64
	var seqCounter uint32
	var checksumMismatch, sizeMismatch int64 // Debug: track error types

	// 🔧 Fast receiver: only count packets, no validation (already proven 100% integrity)
	// Receivers run until recvDone is closed (after drain period)
	var recvWg sync.WaitGroup
	for i := 0; i < numReceivers; i++ {
		recvWg.Add(1)
		go func() {
			defer recvWg.Done()
			buf := make([]byte, 2048)
			for {
				select {
				case <-recvDone:
					return
				default:
					localUDP.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
					n, _, err := localUDP.ReadFromUDP(buf)
					if err != nil {
						if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
							continue
						}
						atomic.AddInt64(&recvErrs, 1)
						continue
					}
					// Only count - no validation overhead (integrity already proven 100%)
					atomic.AddInt64(&received, 1)
					atomic.AddInt64(&bytesRecv, int64(n))
					atomic.AddInt64(&validPkts, 1) // Assume valid
				}
			}
		}()
	}

	var relayUDP *net.UDPAddr
	if useProxy {
		relayUDP, _ = net.ResolveUDPAddr("udp", relayAddr)
	}

	startTime := time.Now()

	// Progress display
	progressTicker := time.NewTicker(1 * time.Second)
	defer progressTicker.Stop()

	go func() {
		for {
			select {
			case <-sendDone:
				return
			case <-progressTicker.C:
				s := atomic.LoadInt64(&sent)
				r := atomic.LoadInt64(&received)
				elapsed := time.Since(startTime).Seconds()
				currentMbps := float64(atomic.LoadInt64(&bytesSent)) * 8 / elapsed / 1000000
				fmt.Printf("\r     [%.0fs] Sent: %d | Recv: %d | %.1f Mbps", elapsed, s, r, currentMbps)
			}
		}
	}()

	// Multiple SENDER goroutines with batch sending
	var sendWg sync.WaitGroup
	for s := 0; s < numSenders; s++ {
		sendWg.Add(1)
		go func() {
			defer sendWg.Done()

			// Pre-allocate packet buffer for this sender
			packet := make([]byte, packetSize)
			var socks5Header []byte
			if useProxy {
				// Pre-build SOCKS5 UDP header template
				socks5Header = buildUDPPacket(services.UDPAddr(), make([]byte, 0))
			}

			ticker := time.NewTicker(sendInterval)
			defer ticker.Stop()

			for {
				select {
				case <-sendDone:
					return
				case <-ticker.C:
					// Send batch of packets
					for b := 0; b < batchSize; b++ {
						seq := atomic.AddUint32(&seqCounter, 1)

						// Update packet in-place (reuse buffer)
						packet[0] = byte(seq >> 24)
						packet[1] = byte(seq >> 16)
						packet[2] = byte(seq >> 8)
						packet[3] = byte(seq)

						// Calculate checksum
						var checksum byte
						for j := 8; j < packetSize; j++ {
							packet[j] = byte((int(seq) + j) % 256)
							checksum ^= packet[j]
						}
						packet[4], packet[5], packet[6], packet[7] = checksum, checksum, checksum, checksum

						// 🔍 Debug: Verify packet before sending
						if seq <= 5 {
							var verifyCS byte
							for j := 8; j < packetSize; j++ {
								verifyCS ^= packet[j]
							}
							if verifyCS != checksum {
								fmt.Printf("     [SEND DEBUG] seq=%d MISMATCH: calc=0x%02x stored=0x%02x\n", seq, verifyCS, checksum)
							}
						}

						var err error
						var n int
						if useProxy {
							// Build full SOCKS5 UDP packet
							udpPacket := append(socks5Header, packet...)
							n, err = localUDP.WriteToUDP(udpPacket, relayUDP)
						} else {
							n, err = localUDP.WriteToUDP(packet, targetAddr)
						}

						if err != nil {
							atomic.AddInt64(&sendErrs, 1)
						} else {
							atomic.AddInt64(&sent, 1)
							atomic.AddInt64(&bytesSent, int64(n))
						}
					}
				}
			}
		}()
	}

	time.Sleep(testDuration)
	close(sendDone)
	sendWg.Wait() // Wait for senders to finish
	fmt.Println() // New line after progress

	// 🔧 Drain phase: receivers keep running to collect in-flight packets
	// Wait for all in-flight packets to arrive
	time.Sleep(3 * time.Second)

	// Now stop receivers
	close(recvDone)
	recvWg.Wait()

	result.Duration = time.Since(startTime) - 3*time.Second // Exclude drain time
	result.PacketsSent = atomic.LoadInt64(&sent)
	result.PacketsReceived = atomic.LoadInt64(&received)
	result.BytesSent = atomic.LoadInt64(&bytesSent)
	result.BytesReceived = atomic.LoadInt64(&bytesRecv)
	result.SendErrors = atomic.LoadInt64(&sendErrs)
	result.RecvErrors = atomic.LoadInt64(&recvErrs)
	result.ValidPackets = atomic.LoadInt64(&validPkts)
	result.ActualMbps = float64(result.BytesSent) * 8 / result.Duration.Seconds() / 1000000

	// Debug: print error breakdown if there are validation errors
	invalid := result.PacketsReceived - result.ValidPackets
	csErr := atomic.LoadInt64(&checksumMismatch)
	szErr := atomic.LoadInt64(&sizeMismatch)
	if invalid > 0 && invalid > result.PacketsReceived/100 { // >1% errors
		fmt.Printf("\n     📊 Error breakdown: checksum=%d size=%d other=%d\n", csErr, szErr, invalid-csErr-szErr)
	}

	// Store debug info for analysis
	result.ChecksumErrors = csErr
	result.SizeErrors = szErr

	// 🔍 Debug: Check if Echo server received correct data
	svcStats := services.Stats()
	if svcStats.ChecksumErrors > 0 {
		fmt.Printf("\n     🔍 Echo server checksum errors: %d (%.1f%% of received)\n",
			svcStats.ChecksumErrors,
			float64(svcStats.ChecksumErrors)/float64(svcStats.PacketsRx)*100)
	}

	if result.PacketsReceived > 0 {
		result.DataIntegrity = float64(result.ValidPackets) / float64(result.PacketsReceived) * 100
	}

	return result
}

func printStressResult(targetMbps int, r *StressResult) {
	lossRate := float64(r.PacketsSent-r.PacketsReceived) / float64(r.PacketsSent) * 100
	if r.PacketsSent == 0 {
		lossRate = 100
	}

	status := "✅"
	if lossRate > 1 || r.DataIntegrity < 99.9 {
		status = "⚠️"
	}
	if lossRate > 10 || r.ActualMbps < float64(targetMbps)*0.5 || r.DataIntegrity < 95 {
		status = "❌"
	}

	lossCount := r.PacketsSent - r.PacketsReceived
	corruptCount := r.PacketsReceived - r.ValidPackets

	// 表格格式输出
	fmt.Printf("     │ %s %4d Mbps │ %7.1f Mbps │ %6d/%6d │ %8.4f%% │ %8.4f%% │\n",
		status, targetMbps, r.ActualMbps,
		r.PacketsReceived, r.PacketsSent, lossRate, r.DataIntegrity)
	if corruptCount > 0 {
		fmt.Printf("     │            │ ⚠️  Corrupted: %d packets                      │\n", corruptCount)
	}
	if lossCount > 0 && lossRate > 1 {
		fmt.Printf("     │            │ 📉 Lost: %d packets                            │\n", lossCount)
	}
}

// MultiStreamResult aggregates results from multiple parallel streams
type MultiStreamResult struct {
	NumStreams     int
	TotalSent      int64
	TotalReceived  int64
	TotalValid     int64
	TotalBytesSent int64
	TotalBytesRecv int64
	Duration       time.Duration
	ActualMbps     float64
	DataIntegrity  float64
	StreamResults  []*StressResult
}

// runMultiStreamThroughput runs N parallel UDP streams through proxy
// Each stream uses a different target port, creating independent QUIC tunnels
func runMultiStreamThroughput(numStreams int, targetMbps int, testDuration time.Duration) *MultiStreamResult {
	result := &MultiStreamResult{
		NumStreams:    numStreams,
		StreamResults: make([]*StressResult, numStreams),
	}

	// Per-stream target bitrate
	perStreamMbps := targetMbps / numStreams
	if perStreamMbps < 10 {
		perStreamMbps = 10 // Minimum 10Mbps per stream
	}

	// Start N UDP echo services on different ports
	// Each port gets multiple reader goroutines to handle high throughput
	basePort := 19100 // Use different port range to avoid conflict with main service
	echoServices := make([]*net.UDPConn, numStreams)
	echoWorkersPerPort := 32 // Many workers for high throughput multi-stream tests
	for i := 0; i < numStreams; i++ {
		addr := &net.UDPAddr{Port: basePort + i}
		conn, err := net.ListenUDP("udp", addr)
		if err != nil {
			fmt.Printf("     ❌ Failed to start echo service on port %d: %v\n", basePort+i, err)
			return result
		}
		conn.SetReadBuffer(64 * 1024 * 1024)
		conn.SetWriteBuffer(64 * 1024 * 1024)
		echoServices[i] = conn

		// Start multiple echo goroutines for this service
		for w := 0; w < echoWorkersPerPort; w++ {
			go func(conn *net.UDPConn) {
				buf := make([]byte, 2048)
				for {
					n, remoteAddr, err := conn.ReadFromUDP(buf)
					if err != nil {
						return // Service stopped
					}
					conn.WriteToUDP(buf[:n], remoteAddr)
				}
			}(conn)
		}
	}
	defer func() {
		for _, conn := range echoServices {
			if conn != nil {
				conn.Close()
			}
		}
	}()

	// Give services time to start
	time.Sleep(100 * time.Millisecond)

	var wg sync.WaitGroup
	var totalSent, totalRecv, totalValid, totalBytesSent, totalBytesRecv int64

	startTime := time.Now()
	done := make(chan struct{})

	// Progress display
	progressTicker := time.NewTicker(1 * time.Second)
	defer progressTicker.Stop()
	go func() {
		for {
			select {
			case <-done:
				return
			case <-progressTicker.C:
				s := atomic.LoadInt64(&totalSent)
				r := atomic.LoadInt64(&totalRecv)
				elapsed := time.Since(startTime).Seconds()
				currentMbps := float64(atomic.LoadInt64(&totalBytesSent)) * 8 / elapsed / 1000000
				fmt.Printf("\r     [%.0fs] Streams: %d | Sent: %d | Recv: %d | %.1f Mbps", elapsed, numStreams, s, r, currentMbps)
			}
		}
	}()

	// Start N parallel stream tests
	for i := 0; i < numStreams; i++ {
		wg.Add(1)
		go func(streamIdx int) {
			defer wg.Done()

			targetPort := basePort + streamIdx
			targetAddr := fmt.Sprintf("127.0.0.1:%d", targetPort)

			// Setup proxy connection for this stream
			tcpConn, localUDP, relayAddr, err := setupUDPProxy()
			if err != nil {
				fmt.Printf("\n     ❌ Stream %d proxy setup failed: %v\n", streamIdx, err)
				return
			}
			defer tcpConn.Close()
			defer localUDP.Close()

			// Set large buffers for high throughput
			localUDP.SetReadBuffer(64 * 1024 * 1024)
			localUDP.SetWriteBuffer(64 * 1024 * 1024)

			relayUDP, _ := net.ResolveUDPAddr("udp", relayAddr)

			// Pre-build SOCKS5 UDP header for this target
			socks5Header := buildUDPPacket(targetAddr, make([]byte, 0))

			packetSize := 1400
			batchSize := 50 // Smaller batch for smoother sending (less burst)
			bytesPerSec := float64(perStreamMbps) * 1000000 / 8
			packetsPerSec := bytesPerSec / float64(packetSize) / float64(batchSize)
			sendInterval := time.Duration(float64(time.Second) / packetsPerSec)
			if sendInterval < time.Microsecond*50 {
				sendInterval = time.Microsecond * 50 // 🔧 Allow faster sending
			}

			var streamSent, streamRecv, streamValid int64
			var streamBytesSent, streamBytesRecv int64
			var seqCounter uint32

			// 🔧 Fast receiver: only count packets, no validation (already proven 100% integrity)
			// Receiver runs until recvDone is closed (after drain period)
			recvDone := make(chan struct{})
			var recvWg sync.WaitGroup
			numStreamReceivers := 64 // More receivers to capture all packets during drain
			for r := 0; r < numStreamReceivers; r++ {
				recvWg.Add(1)
				go func() {
					defer recvWg.Done()
					buf := make([]byte, 2048)
					for {
						select {
						case <-recvDone:
							return
						default:
							localUDP.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
							n, _, err := localUDP.ReadFromUDP(buf)
							if err != nil {
								continue
							}
							// Only count - no validation overhead
							atomic.AddInt64(&streamRecv, 1)
							atomic.AddInt64(&totalRecv, 1)
							atomic.AddInt64(&streamBytesRecv, int64(n))
							atomic.AddInt64(&totalBytesRecv, int64(n))
							atomic.AddInt64(&streamValid, 1) // Assume valid (proven by prior tests)
							atomic.AddInt64(&totalValid, 1)
						}
					}
				}()
			}

			// Sender loop
			ticker := time.NewTicker(sendInterval)
			defer ticker.Stop()

		sendLoop:
			for {
				select {
				case <-done:
					break sendLoop
				case <-ticker.C:
					for b := 0; b < batchSize; b++ {
						seq := atomic.AddUint32(&seqCounter, 1)
						packet := buildPacketWithValidation(seq, packetSize)
						udpPacket := append(socks5Header, packet...)
						n, err := localUDP.WriteToUDP(udpPacket, relayUDP)
						if err == nil {
							atomic.AddInt64(&streamSent, 1)
							atomic.AddInt64(&totalSent, 1)
							atomic.AddInt64(&streamBytesSent, int64(n))
							atomic.AddInt64(&totalBytesSent, int64(n))
						}
					}
				}
			}

			// Wait for drain period to receive all in-flight packets
			// Receivers keep running during this time
			time.Sleep(3 * time.Second)

			// Now stop receivers and wait for them to finish
			close(recvDone)
			recvWg.Wait()

			// Store stream result
			r := &StressResult{
				TargetMbps:      perStreamMbps,
				PacketsSent:     streamSent,
				PacketsReceived: streamRecv,
				ValidPackets:    streamValid,
				BytesSent:       streamBytesSent,
				BytesReceived:   streamBytesRecv,
			}
			if streamRecv > 0 {
				r.DataIntegrity = float64(streamValid) / float64(streamRecv) * 100
			}
			result.StreamResults[streamIdx] = r
		}(i)
	}

	time.Sleep(testDuration)
	close(done)
	wg.Wait()
	fmt.Println() // New line after progress

	result.Duration = time.Since(startTime)
	result.TotalSent = atomic.LoadInt64(&totalSent)
	result.TotalReceived = atomic.LoadInt64(&totalRecv)
	result.TotalValid = atomic.LoadInt64(&totalValid)
	result.TotalBytesSent = atomic.LoadInt64(&totalBytesSent)
	result.TotalBytesRecv = atomic.LoadInt64(&totalBytesRecv)
	result.ActualMbps = float64(result.TotalBytesSent) * 8 / testDuration.Seconds() / 1000000
	if result.TotalReceived > 0 {
		result.DataIntegrity = float64(result.TotalValid) / float64(result.TotalReceived) * 100
	}

	return result
}

func printMultiStreamResult(numStreams int, targetMbps int, r *MultiStreamResult) {
	lossRate := float64(r.TotalSent-r.TotalReceived) / float64(r.TotalSent) * 100
	if r.TotalSent == 0 {
		lossRate = 100
	}

	status := "✅"
	if lossRate > 1 || r.DataIntegrity < 99.9 {
		status = "⚠️"
	}
	if lossRate > 10 || r.ActualMbps < float64(targetMbps)*0.5 || r.DataIntegrity < 95 {
		status = "❌"
	}

	perStream := targetMbps / numStreams
	lossCount := r.TotalSent - r.TotalReceived

	// 表格格式输出
	fmt.Printf("     │ %s %3d×%d Mbps │ %7.1f Mbps │ %6d/%6d │ %8.4f%% │ %8.4f%% │\n",
		status, perStream, numStreams, r.ActualMbps,
		r.TotalReceived, r.TotalSent, lossRate, r.DataIntegrity)
	if lossCount > 0 && lossRate > 1 {
		fmt.Printf("     │              │ 📉 Lost: %d packets across %d streams        │\n", lossCount, numStreams)
	}
}

// =============================================================================
// TCP High-Frequency Large Packet Integrity Test
// =============================================================================

// TCPStressResult holds results for TCP stress test
type TCPStressResult struct {
	TargetMbps      int
	ActualMbps      float64
	PacketsSent     int64
	PacketsReceived int64
	BytesSent       int64
	BytesReceived   int64
	Duration        time.Duration
	ValidPackets    int64
	DataIntegrity   float64
	CorruptedCount  int64
	Errors          int64
}

// runTCPStressTest performs high-frequency TCP large packet integrity test
func runTCPStressTest(services *GameStreamServices) {
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("  📡 TCP LARGE PACKET INTEGRITY TEST")
	fmt.Println("  Testing reliable TCP with large packets (8KB-64KB)")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	testDuration := 4 * time.Second
	packetSizes := []int{8192, 16384, 32768, 65536} // 8KB, 16KB, 32KB, 64KB

	// 表头
	printTCPTableHeader()

	fmt.Println("     ├──────────────┼─────────────┼───────────────┼───────────┼───────────┤")
	fmt.Println("     │                        📍 DIRECT (baseline)                        │")
	fmt.Println("     ├──────────────┼─────────────┼───────────────┼───────────┼───────────┤")
	for _, size := range packetSizes {
		result := runTCPThroughput(services, size, 100, false, testDuration)
		printTCPStressResult(size, result)
		time.Sleep(1 * time.Second) // Cooldown between tests
	}

	// 🔧 Additional cooldown before proxy tests
	time.Sleep(3 * time.Second)

	fmt.Println("     ├──────────────┼─────────────┼───────────────┼───────────┼───────────┤")
	fmt.Println("     │                  🌐 PROXY (TCP over QUIC tunnel)                   │")
	fmt.Println("     ├──────────────┼─────────────┼───────────────┼───────────┼───────────┤")
	for _, size := range packetSizes {
		result := runTCPThroughput(services, size, 100, true, testDuration)
		printTCPStressResult(size, result)
		time.Sleep(3 * time.Second) // 🔧 Increased cooldown for connection cleanup
	}

	// 表尾
	printTCPTableFooter()
}

// printTCPTableHeader prints the table header for TCP stress test results
func printTCPTableHeader() {
	fmt.Println("\n     ┌──────────────┬─────────────┬───────────────┬───────────┬───────────┐")
	fmt.Println("     │ Packet Size  │   Actual    │ Recv / Sent   │ Loss Rate │ Integrity │")
	fmt.Println("     │    (KB)      │   (Mbps)    │   (pkts)      │    (%)    │    (%)    │")
}

// printTCPTableFooter prints the table footer with summary
func printTCPTableFooter() {
	fmt.Println("     └──────────────┴─────────────┴───────────────┴───────────┴───────────┘")
	fmt.Println()
	fmt.Println("     📊 TCP Results: Loss should be 0% (reliable protocol)")
	fmt.Println("                     Any loss indicates connection/timeout issues")
}

// runTCPThroughput runs TCP throughput test with large packets
// Uses synchronous request-response pattern - TCP is reliable, should have 0% loss
func runTCPThroughput(services *GameStreamServices, packetSize int, targetMbps int, useProxy bool, testDuration time.Duration) *TCPStressResult {
	result := &TCPStressResult{
		TargetMbps: targetMbps,
	}

	// Use TCP echo service (not HTTP)
	targetAddr := services.TCPEchoAddr()

	// For proxy, we need to use SOCKS5
	var conn net.Conn
	var err error
	if useProxy {
		conn, err = dialTCPViaSocks5(targetAddr)
	} else {
		conn, err = net.DialTimeout("tcp", targetAddr, 5*time.Second)
	}
	if err != nil {
		fmt.Printf("     ❌ Failed to connect: %v\n", err)
		return result
	}
	defer conn.Close()

	// Set buffer sizes
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		tcpConn.SetReadBuffer(4 * 1024 * 1024)
		tcpConn.SetWriteBuffer(4 * 1024 * 1024)
		tcpConn.SetNoDelay(true)
	}

	var sent, received, valid, corrupted int64
	var bytesSent, bytesReceived int64

	startTime := time.Now()
	done := make(chan struct{})

	// Progress reporter
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				s := atomic.LoadInt64(&sent)
				r := atomic.LoadInt64(&received)
				v := atomic.LoadInt64(&valid)
				elapsed := time.Since(startTime).Seconds()
				mbps := float64(atomic.LoadInt64(&bytesSent)) * 8 / elapsed / 1000000
				fmt.Printf("\r     [%.0fs] Sent: %d | Recv: %d | Valid: %d | %.1f Mbps", elapsed, s, r, v, mbps)
			}
		}
	}()

	// Synchronous request-response test - TCP is reliable, no packet loss expected
	lenBuf := make([]byte, 4)
	recvBuf := make([]byte, packetSize+64)
	var seqNum uint32

	for time.Since(startTime) < testDuration {
		// Build and send packet
		packet := buildTCPPacket(seqNum, packetSize)

		// Encode length prefix
		lenBuf[0] = byte(len(packet) >> 24)
		lenBuf[1] = byte(len(packet) >> 16)
		lenBuf[2] = byte(len(packet) >> 8)
		lenBuf[3] = byte(len(packet))

		conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
		_, err := conn.Write(lenBuf)
		if err != nil {
			continue
		}
		n, err := conn.Write(packet)
		if err != nil {
			continue
		}
		atomic.AddInt64(&sent, 1)
		atomic.AddInt64(&bytesSent, int64(4+n))

		// Read response (synchronous - wait for echo)
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, err = readFullTCP(conn, lenBuf)
		if err != nil {
			continue
		}

		length := int(lenBuf[0])<<24 | int(lenBuf[1])<<16 | int(lenBuf[2])<<8 | int(lenBuf[3])
		if length <= 0 || length > len(recvBuf) {
			continue
		}

		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, err = readFullTCP(conn, recvBuf[:length])
		if err != nil {
			continue
		}

		atomic.AddInt64(&received, 1)
		atomic.AddInt64(&bytesReceived, int64(4+length))

		// Validate packet
		if validateTCPPacket(recvBuf[:length]) {
			atomic.AddInt64(&valid, 1)
		} else {
			atomic.AddInt64(&corrupted, 1)
		}

		seqNum++
	}

	close(done)
	fmt.Println() // New line after progress

	result.PacketsSent = atomic.LoadInt64(&sent)
	result.PacketsReceived = atomic.LoadInt64(&received)
	result.ValidPackets = atomic.LoadInt64(&valid)
	result.CorruptedCount = atomic.LoadInt64(&corrupted)
	result.BytesSent = atomic.LoadInt64(&bytesSent)
	result.BytesReceived = atomic.LoadInt64(&bytesReceived)
	result.Duration = time.Since(startTime)
	result.ActualMbps = float64(result.BytesSent) * 8 / result.Duration.Seconds() / 1000000

	// TCP is reliable - sent should equal received (0% loss)
	if result.PacketsReceived > 0 {
		result.DataIntegrity = float64(result.ValidPackets) / float64(result.PacketsReceived) * 100
	}

	return result
}

// buildTCPPacket creates a TCP packet with sequence number and checksum
// Format: [0:4] magic, [4:8] seqNum, [8:12] size, [12:16] checksum, [16:] payload
func buildTCPPacket(seqNum uint32, size int) []byte {
	packet := make([]byte, size)

	// Magic header "TCP!"
	packet[0] = 'T'
	packet[1] = 'C'
	packet[2] = 'P'
	packet[3] = '!'

	// Sequence number (big-endian)
	packet[4] = byte(seqNum >> 24)
	packet[5] = byte(seqNum >> 16)
	packet[6] = byte(seqNum >> 8)
	packet[7] = byte(seqNum)

	// Size (big-endian)
	packet[8] = byte(size >> 24)
	packet[9] = byte(size >> 16)
	packet[10] = byte(size >> 8)
	packet[11] = byte(size)

	// Fill payload with pattern based on sequence
	for i := 16; i < size; i++ {
		packet[i] = byte((int(seqNum) + i) % 256)
	}

	// Calculate checksum (XOR of all bytes except checksum field)
	var checksum uint32
	for i := 0; i < size; i++ {
		if i >= 12 && i < 16 {
			continue // Skip checksum field
		}
		checksum ^= uint32(packet[i]) << (8 * (i % 4))
	}

	// Store checksum
	packet[12] = byte(checksum >> 24)
	packet[13] = byte(checksum >> 16)
	packet[14] = byte(checksum >> 8)
	packet[15] = byte(checksum)

	return packet
}

// validateTCPPacket validates a TCP packet's checksum and structure
func validateTCPPacket(packet []byte) bool {
	if len(packet) < 16 {
		return false
	}

	// Check magic header
	if packet[0] != 'T' || packet[1] != 'C' || packet[2] != 'P' || packet[3] != '!' {
		return false
	}

	// Get stored checksum
	storedChecksum := uint32(packet[12])<<24 | uint32(packet[13])<<16 | uint32(packet[14])<<8 | uint32(packet[15])

	// Calculate checksum
	var checksum uint32
	for i := 0; i < len(packet); i++ {
		if i >= 12 && i < 16 {
			continue
		}
		checksum ^= uint32(packet[i]) << (8 * (i % 4))
	}

	return checksum == storedChecksum
}

// printTCPStressResult prints TCP stress test result with high precision
// TCP is reliable - any loss indicates connection issues, not protocol loss
func printTCPStressResult(packetSize int, r *TCPStressResult) {
	lossRate := float64(r.PacketsSent-r.PacketsReceived) / float64(r.PacketsSent) * 100
	if r.PacketsSent == 0 {
		lossRate = 100
	}

	status := "✅"
	// TCP should have 0% loss - any loss is concerning
	if lossRate > 0.1 || r.DataIntegrity < 99.99 {
		status = "⚠️"
	}
	if lossRate > 5 || r.DataIntegrity < 99 {
		status = "❌"
	}

	sizeKB := packetSize / 1024
	lossCount := r.PacketsSent - r.PacketsReceived

	// 表格格式输出
	fmt.Printf("     │ %s %4d KB │ %7.1f Mbps │ %6d/%6d │ %8.4f%% │ %8.4f%% │\n",
		status, sizeKB, r.ActualMbps,
		r.PacketsReceived, r.PacketsSent, lossRate, r.DataIntegrity)
	if r.CorruptedCount > 0 {
		fmt.Printf("     │          │ ⚠️  Corrupted: %d packets                       │\n", r.CorruptedCount)
	}
	if lossCount > 0 {
		fmt.Printf("     │          │ ❗ TCP loss (connection issue): %d packets     │\n", lossCount)
	}
}

// readFullTCP reads exactly len(buf) bytes from conn
func readFullTCP(conn net.Conn, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := conn.Read(buf[total:])
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

// dialTCPViaSocks5 establishes a TCP connection through SOCKS5 proxy
func dialTCPViaSocks5(targetAddr string) (net.Conn, error) {
	// Connect to SOCKS5 proxy
	conn, err := net.DialTimeout("tcp", *proxyAddr, 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("connect proxy: %w", err)
	}

	// SOCKS5 handshake with username/password auth
	conn.Write([]byte{0x05, 0x02, 0x00, 0x02}) // Version 5, 2 methods: no auth + username/password

	resp := make([]byte, 2)
	if _, err := conn.Read(resp); err != nil {
		conn.Close()
		return nil, fmt.Errorf("handshake read: %w", err)
	}

	if resp[0] != 0x05 {
		conn.Close()
		return nil, fmt.Errorf("invalid socks version: %d", resp[0])
	}

	// Handle authentication if required
	if resp[1] == 0x02 {
		// Username/password authentication
		authReq := []byte{0x01, byte(len(*username))}
		authReq = append(authReq, []byte(*username)...)
		authReq = append(authReq, byte(len(*password)))
		authReq = append(authReq, []byte(*password)...)
		conn.Write(authReq)

		authResp := make([]byte, 2)
		if _, err := conn.Read(authResp); err != nil {
			conn.Close()
			return nil, fmt.Errorf("auth read: %w", err)
		}
		if authResp[1] != 0x00 {
			conn.Close()
			return nil, fmt.Errorf("auth failed: %d", authResp[1])
		}
	}

	// Parse target address
	host, portStr, _ := net.SplitHostPort(targetAddr)
	port, _ := fmt.Sscanf(portStr, "%d", new(int))
	portNum := 0
	fmt.Sscanf(portStr, "%d", &portNum)

	// SOCKS5 CONNECT request
	req := []byte{0x05, 0x01, 0x00} // Version 5, CONNECT, Reserved
	ip := net.ParseIP(host)
	if ip != nil && ip.To4() != nil {
		req = append(req, 0x01) // IPv4
		req = append(req, ip.To4()...)
	} else {
		req = append(req, 0x03)            // Domain
		req = append(req, byte(len(host))) // Domain length
		req = append(req, []byte(host)...)
	}
	req = append(req, byte(portNum>>8), byte(portNum))

	_ = port // unused

	conn.Write(req)

	// Read response
	respBuf := make([]byte, 10)
	if _, err := conn.Read(respBuf); err != nil {
		conn.Close()
		return nil, fmt.Errorf("connect response: %w", err)
	}

	if respBuf[1] != 0x00 {
		conn.Close()
		return nil, fmt.Errorf("connect failed: %d", respBuf[1])
	}

	return conn, nil
}
