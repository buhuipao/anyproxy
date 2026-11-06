// Package main provides throughput testing for TUIC v5 protocol
package main

import (
	"context"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// ThroughputResult holds throughput test results
type ThroughputResult struct {
	TargetMbps      int
	ActualMbps      float64
	PacketsSent     int64
	PacketsReceived int64
	BytesSent       int64
	BytesReceived   int64
	Duration        time.Duration
	PacketLoss      float64
	DataIntegrity   float64
	ValidPackets    int64
}

// FrameIntervalStats holds frame interval statistics for detecting network-induced stutters
type FrameIntervalStats struct {
	MinInterval   time.Duration
	MaxInterval   time.Duration
	AvgInterval   time.Duration
	StdDeviation  time.Duration
	P50Interval   time.Duration
	P90Interval   time.Duration
	P95Interval   time.Duration
	P99Interval   time.Duration
	LateFrames    int64   // Frames that arrived later than expected frame time budget
	OnTimeFrames  int64   // Frames that arrived within expected frame time budget
	LateFrameRate float64 // Percentage of late frames
	MaxGapFrames  int     // Maximum consecutive frames with large intervals (potential stutter)
}

// runNativeModeTest tests QUIC Datagram mode throughput
func runNativeModeTest(client *TUICTestClient, echoAddr string, targetMbps int, duration time.Duration) *ThroughputResult {
	result := &ThroughputResult{
		TargetMbps: targetMbps,
	}

	ctx := context.Background()
	udpConn, err := client.DialUDP(ctx)
	if err != nil {
		fmt.Printf("     ❌ Failed to create UDP connection: %v\n", err)
		return result
	}
	defer udpConn.Close()

	targetAddr, err := net.ResolveUDPAddr("udp", echoAddr)
	if err != nil {
		fmt.Printf("     ❌ Failed to resolve address: %v\n", err)
		return result
	}

	packetSize := 1200 // Moonlight max packetSize, tests QUIC Datagram MTU limit

	// Calculate sending parameters
	bytesPerSec := float64(targetMbps) * 1000000 / 8
	packetsPerSec := bytesPerSec / float64(packetSize)
	sendInterval := time.Duration(float64(time.Second) / packetsPerSec)

	// For high bitrates, we need much smaller intervals
	// 100 Mbps @ 1200B = 10416 pps = 96us interval
	// 500 Mbps @ 1200B = 52083 pps = 19us interval
	// 1000 Mbps @ 1200B = 104166 pps = 9.6us interval
	minInterval := time.Nanosecond * 500 // 500ns minimum for very high bitrates
	if sendInterval < minInterval {
		sendInterval = minInterval
	}

	// Calculate batch size for very high bitrates (>100 Mbps)
	batchSize := 1
	if targetMbps > 100 {
		// Send multiple packets per tick for high bitrates
		batchSize = (targetMbps / 100)
		if batchSize > 10 {
			batchSize = 10
		}
		sendInterval = time.Microsecond * 10 // 10us interval with batching
	}

	var sent, received, valid int64
	var bytesSent, bytesReceived int64
	var seqCounter uint32

	sendDone := make(chan struct{})
	recvDone := make(chan struct{})

	// Receiver goroutine
	var recvWg sync.WaitGroup
	recvWg.Add(1)
	go func() {
		defer recvWg.Done()
		recvBuf := make([]byte, 2048)
		for {
			select {
			case <-recvDone:
				return
			default:
				udpConn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
				n, _, err := udpConn.ReadFrom(recvBuf)
				if err != nil {
					continue
				}
				atomic.AddInt64(&received, 1)
				atomic.AddInt64(&bytesReceived, int64(n))

				// Verify packet
				if n >= 8 && verifyThroughputPacket(recvBuf[:n]) {
					atomic.AddInt64(&valid, 1)
				}
			}
		}
	}()

	startTime := time.Now()

	// Progress display
	progressTicker := time.NewTicker(1 * time.Second)
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

	// Sender goroutine with batching support for high bitrates
	ticker := time.NewTicker(sendInterval)
	timeout := time.After(duration)
sendLoop:
	for {
		select {
		case <-timeout:
			break sendLoop
		case <-ticker.C:
			// Send batch of packets for high bitrate tests
			for b := 0; b < batchSize; b++ {
				seq := atomic.AddUint32(&seqCounter, 1)
				packet := buildThroughputPacket(seq, packetSize)

				n, err := udpConn.WriteTo(packet, targetAddr)
				if err == nil {
					atomic.AddInt64(&sent, 1)
					atomic.AddInt64(&bytesSent, int64(n))
				}
			}
		}
	}

	ticker.Stop()
	close(sendDone)
	progressTicker.Stop()
	fmt.Println()

	// Drain period
	time.Sleep(2 * time.Second)
	close(recvDone)
	recvWg.Wait()

	result.Duration = time.Since(startTime) - 2*time.Second
	result.PacketsSent = atomic.LoadInt64(&sent)
	result.PacketsReceived = atomic.LoadInt64(&received)
	result.ValidPackets = atomic.LoadInt64(&valid)
	result.BytesSent = atomic.LoadInt64(&bytesSent)
	result.BytesReceived = atomic.LoadInt64(&bytesReceived)
	result.ActualMbps = float64(result.BytesSent) * 8 / result.Duration.Seconds() / 1000000

	if result.PacketsSent > 0 {
		result.PacketLoss = float64(result.PacketsSent-result.PacketsReceived) / float64(result.PacketsSent) * 100
	}
	if result.PacketsReceived > 0 {
		result.DataIntegrity = float64(result.ValidPackets) / float64(result.PacketsReceived) * 100
	}

	return result
}

// runDirectThroughputTest tests direct UDP throughput for baseline
func runDirectThroughputTest(echoAddr string, targetMbps int, duration time.Duration) *ThroughputResult {
	result := &ThroughputResult{
		TargetMbps: targetMbps,
	}

	targetAddr, err := net.ResolveUDPAddr("udp", echoAddr)
	if err != nil {
		fmt.Printf("     ❌ Failed to resolve address: %v\n", err)
		return result
	}

	conn, err := net.ListenUDP("udp", &net.UDPAddr{Port: 0})
	if err != nil {
		fmt.Printf("     ❌ Failed to create UDP socket: %v\n", err)
		return result
	}
	defer conn.Close()

	conn.SetReadBuffer(64 * 1024 * 1024)
	conn.SetWriteBuffer(64 * 1024 * 1024)

	packetSize := 1400 // Direct UDP - no QUIC MTU limit
	bytesPerSec := float64(targetMbps) * 1000000 / 8
	packetsPerSec := bytesPerSec / float64(packetSize)
	sendInterval := time.Duration(float64(time.Second) / packetsPerSec)

	minInterval := time.Nanosecond * 500
	if sendInterval < minInterval {
		sendInterval = minInterval
	}

	// Batch size for high bitrates
	batchSize := 1
	if targetMbps > 100 {
		batchSize = (targetMbps / 100)
		if batchSize > 10 {
			batchSize = 10
		}
		sendInterval = time.Microsecond * 10
	}

	var sent, received, valid int64
	var bytesSent, bytesReceived int64
	var seqCounter uint32

	sendDone := make(chan struct{})
	recvDone := make(chan struct{})

	// Receiver
	var recvWg sync.WaitGroup
	recvWg.Add(1)
	go func() {
		defer recvWg.Done()
		recvBuf := make([]byte, 2048)
		for {
			select {
			case <-recvDone:
				return
			default:
				conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
				n, _, err := conn.ReadFromUDP(recvBuf)
				if err != nil {
					continue
				}
				atomic.AddInt64(&received, 1)
				atomic.AddInt64(&bytesReceived, int64(n))
				if n >= 8 && verifyThroughputPacket(recvBuf[:n]) {
					atomic.AddInt64(&valid, 1)
				}
			}
		}
	}()

	startTime := time.Now()

	// Progress display
	progressTicker := time.NewTicker(1 * time.Second)
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

	// Sender with batching
	ticker := time.NewTicker(sendInterval)
	timeout := time.After(duration)
sendLoop:
	for {
		select {
		case <-timeout:
			break sendLoop
		case <-ticker.C:
			for b := 0; b < batchSize; b++ {
				seq := atomic.AddUint32(&seqCounter, 1)
				packet := buildThroughputPacket(seq, packetSize)

				n, err := conn.WriteToUDP(packet, targetAddr)
				if err == nil {
					atomic.AddInt64(&sent, 1)
					atomic.AddInt64(&bytesSent, int64(n))
				}
			}
		}
	}

	ticker.Stop()
	close(sendDone)
	progressTicker.Stop()
	fmt.Println()

	time.Sleep(1 * time.Second)
	close(recvDone)
	recvWg.Wait()

	result.Duration = time.Since(startTime) - 1*time.Second
	result.PacketsSent = atomic.LoadInt64(&sent)
	result.PacketsReceived = atomic.LoadInt64(&received)
	result.ValidPackets = atomic.LoadInt64(&valid)
	result.BytesSent = atomic.LoadInt64(&bytesSent)
	result.BytesReceived = atomic.LoadInt64(&bytesReceived)
	result.ActualMbps = float64(result.BytesSent) * 8 / result.Duration.Seconds() / 1000000

	if result.PacketsSent > 0 {
		result.PacketLoss = float64(result.PacketsSent-result.PacketsReceived) / float64(result.PacketsSent) * 100
	}
	if result.PacketsReceived > 0 {
		result.DataIntegrity = float64(result.ValidPackets) / float64(result.PacketsReceived) * 100
	}

	return result
}

// buildThroughputPacket creates a packet with sequence number and checksum
func buildThroughputPacket(seqNum uint32, size int) []byte {
	packet := make([]byte, size)

	// Sequence number (big-endian)
	packet[0] = byte(seqNum >> 24)
	packet[1] = byte(seqNum >> 16)
	packet[2] = byte(seqNum >> 8)
	packet[3] = byte(seqNum)

	// Fill payload with pattern
	for i := 8; i < size; i++ {
		packet[i] = byte((int(seqNum) + i) % 256)
	}

	// Calculate checksum
	var checksum byte
	for i := 8; i < size; i++ {
		checksum ^= packet[i]
	}

	// Store checksum (repeated 4 times)
	packet[4] = checksum
	packet[5] = checksum
	packet[6] = checksum
	packet[7] = checksum

	return packet
}

// verifyThroughputPacket verifies a throughput test packet
func verifyThroughputPacket(data []byte) bool {
	if len(data) < 8 {
		return false
	}

	expectedChecksum := data[4]

	var actualChecksum byte
	for i := 8; i < len(data); i++ {
		actualChecksum ^= data[i]
	}

	return expectedChecksum == actualChecksum
}

// printThroughputTableHeader prints the throughput test table header
func printThroughputTableHeader() {
	fmt.Println("\n     ┌──────────────┬────────────────┬───────────────┬────────────┬────────────┐")
	fmt.Println("     │   Target     │     Actual     │ Recv / Sent   │  Loss Rate │  Integrity │")
	fmt.Println("     │   (Mbps)     │     (Mbps)     │   (pkts)      │     (%)    │     (%)    │")
}

// printThroughputTableFooter prints the throughput test table footer
func printThroughputTableFooter() {
	fmt.Println("     └──────────────┴────────────────┴───────────────┴────────────┴────────────┘")
	fmt.Println()
	fmt.Println("     📊 Legend: ✅ Excellent (loss<1%, integrity>99%)")
	fmt.Println("                ⚠️  Warning  (loss<5%, integrity>95%)")
	fmt.Println("                ❌ Failed   (loss>5% or integrity<95%)")
}

// printThroughputResult prints a single throughput test result
func printThroughputResult(result *ThroughputResult) {
	status := "✅"
	if result.PacketLoss > 1 || result.DataIntegrity < 99 {
		status = "⚠️"
	}
	if result.PacketLoss > 5 || result.DataIntegrity < 95 {
		status = "❌"
	}

	fmt.Printf("     │ %s %4d Mbps │ %10.4f Mbps │ %6d/%6d │ %9.4f%% │ %9.4f%% │\n",
		status, result.TargetMbps, result.ActualMbps,
		result.PacketsReceived, result.PacketsSent, result.PacketLoss, result.DataIntegrity)
}

// runThroughputComparison runs throughput tests with comparison
func runThroughputComparison(client *TUICTestClient, echoAddr string, bitrates []int, duration time.Duration) {
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("  🔥 THROUGHPUT TEST")
	fmt.Printf("  Testing sustained throughput (TUIC mode: %s)\n", client.Mode())
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	printThroughputTableHeader()

	// Direct baseline tests
	fmt.Println("     ├──────────────┼─────────────┼───────────────┼───────────┼───────────┤")
	fmt.Println("     │                        📍 DIRECT (baseline)                        │")
	fmt.Println("     ├──────────────┼─────────────┼───────────────┼───────────┼───────────┤")

	for _, br := range bitrates {
		result := runDirectThroughputTest(echoAddr, br, duration)
		printThroughputResult(result)
		time.Sleep(1 * time.Second)
	}

	// TUIC throughput tests
	fmt.Println("     ├──────────────┼─────────────┼───────────────┼───────────┼───────────┤")
	fmt.Printf("     │                   🌐 TUIC (%s mode)                          │\n", client.Mode())
	fmt.Println("     ├──────────────┼─────────────┼───────────────┼───────────┼───────────┤")

	for _, br := range bitrates {
		result := runNativeModeTest(client, echoAddr, br, duration)
		printThroughputResult(result)
		time.Sleep(2 * time.Second)
	}

	printThroughputTableFooter()
}

// MultiStreamThroughputResult holds multi-stream test results
type MultiStreamThroughputResult struct {
	NumStreams     int
	TotalSent      int64
	TotalReceived  int64
	TotalValid     int64
	TotalBytesSent int64
	Duration       time.Duration
	ActualMbps     float64
	PacketLoss     float64
	DataIntegrity  float64
}

// runMultiStreamThroughputTest tests throughput with multiple parallel streams
func runMultiStreamThroughputTest(serverAddr string, opts TUICClientOptions, echoAddr string, numStreams int, targetMbps int, duration time.Duration) *MultiStreamThroughputResult {
	result := &MultiStreamThroughputResult{
		NumStreams: numStreams,
	}

	perStreamMbps := targetMbps / numStreams
	if perStreamMbps < 10 {
		perStreamMbps = 10
	}

	var wg sync.WaitGroup
	var totalSent, totalReceived, totalValid, totalBytesSent int64

	startTime := time.Now()
	done := make(chan struct{})

	// Progress display
	progressTicker := time.NewTicker(1 * time.Second)
	go func() {
		for {
			select {
			case <-done:
				return
			case <-progressTicker.C:
				s := atomic.LoadInt64(&totalSent)
				r := atomic.LoadInt64(&totalReceived)
				elapsed := time.Since(startTime).Seconds()
				currentMbps := float64(atomic.LoadInt64(&totalBytesSent)) * 8 / elapsed / 1000000
				fmt.Printf("\r     [%.0fs] Streams: %d | Sent: %d | Recv: %d | %.1f Mbps",
					elapsed, numStreams, s, r, currentMbps)
			}
		}
	}()

	// Start multiple parallel streams
	for i := 0; i < numStreams; i++ {
		wg.Add(1)
		go func(streamID int) {
			defer wg.Done()

			// Create dedicated client for this stream
			client, err := NewTUICTestClient(opts)
			if err != nil {
				fmt.Printf("\n     ❌ Stream %d: client creation failed: %v\n", streamID, err)
				return
			}
			defer client.Close()

			ctx := context.Background()
			udpConn, err := client.DialUDP(ctx)
			if err != nil {
				fmt.Printf("\n     ❌ Stream %d: UDP dial failed: %v\n", streamID, err)
				return
			}
			defer udpConn.Close()

			targetAddr, err := net.ResolveUDPAddr("udp", echoAddr)
			if err != nil {
				return
			}

			packetSize := 1200 // Moonlight max packetSize for QUIC Datagram
			bytesPerSec := float64(perStreamMbps) * 1000000 / 8
			packetsPerSec := bytesPerSec / float64(packetSize)
			sendInterval := time.Duration(float64(time.Second) / packetsPerSec)
			if sendInterval < time.Microsecond*100 {
				sendInterval = time.Microsecond * 100
			}

			var streamSent, streamReceived, streamValid int64
			var streamBytesSent int64
			var seqCounter uint32

			recvDone := make(chan struct{})
			var recvWg sync.WaitGroup
			recvWg.Add(1)
			go func() {
				defer recvWg.Done()
				recvBuf := make([]byte, 2048)
				for {
					select {
					case <-recvDone:
						return
					default:
						udpConn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
						n, _, err := udpConn.ReadFrom(recvBuf)
						if err != nil {
							continue
						}
						atomic.AddInt64(&streamReceived, 1)
						atomic.AddInt64(&totalReceived, 1)
						if n >= 8 && verifyThroughputPacket(recvBuf[:n]) {
							atomic.AddInt64(&streamValid, 1)
							atomic.AddInt64(&totalValid, 1)
						}
					}
				}
			}()

			ticker := time.NewTicker(sendInterval)
		sendLoop:
			for {
				select {
				case <-done:
					break sendLoop
				case <-ticker.C:
					seq := atomic.AddUint32(&seqCounter, 1)
					packet := buildThroughputPacket(seq, packetSize)
					n, err := udpConn.WriteTo(packet, targetAddr)
					if err == nil {
						atomic.AddInt64(&streamSent, 1)
						atomic.AddInt64(&totalSent, 1)
						atomic.AddInt64(&streamBytesSent, int64(n))
						atomic.AddInt64(&totalBytesSent, int64(n))
					}
				}
			}
			ticker.Stop()

			// Drain period
			time.Sleep(2 * time.Second)
			close(recvDone)
			recvWg.Wait()
		}(i)
	}

	time.Sleep(duration)
	close(done)
	progressTicker.Stop()
	wg.Wait()
	fmt.Println()

	result.Duration = time.Since(startTime)
	result.TotalSent = atomic.LoadInt64(&totalSent)
	result.TotalReceived = atomic.LoadInt64(&totalReceived)
	result.TotalValid = atomic.LoadInt64(&totalValid)
	result.TotalBytesSent = atomic.LoadInt64(&totalBytesSent)
	result.ActualMbps = float64(result.TotalBytesSent) * 8 / duration.Seconds() / 1000000

	if result.TotalSent > 0 {
		result.PacketLoss = float64(result.TotalSent-result.TotalReceived) / float64(result.TotalSent) * 100
	}
	if result.TotalReceived > 0 {
		result.DataIntegrity = float64(result.TotalValid) / float64(result.TotalReceived) * 100
	}

	return result
}

func printMultiStreamResult(result *MultiStreamThroughputResult) {
	status := "✅"
	if result.PacketLoss > 1 || result.DataIntegrity < 99 {
		status = "⚠️"
	}
	if result.PacketLoss > 5 || result.DataIntegrity < 95 {
		status = "❌"
	}

	fmt.Printf("     │ %s %2d streams │ %10.4f Mbps │ %6d/%6d │ %9.4f%% │ %9.4f%% │\n",
		status, result.NumStreams, result.ActualMbps,
		result.TotalReceived, result.TotalSent, result.PacketLoss, result.DataIntegrity)
}

// HighThroughputResult holds comprehensive stress test results
type HighThroughputResult struct {
	TargetMbps    int
	ActualMbps    float64
	PacketsSent   int64
	PacketsRecv   int64
	PacketLoss    float64
	DataIntegrity float64

	// Latency metrics
	MinLatency time.Duration
	AvgLatency time.Duration
	P50Latency time.Duration
	P90Latency time.Duration
	P99Latency time.Duration
	MaxLatency time.Duration
	Jitter     time.Duration

	// Frame loss simulation
	FramesSent   int64
	FramesRecv   int64
	FrameLoss    float64
	EffectiveFPS float64
	MaxGap       int
	OutOfOrder   int64
	TargetFPS    int

	// Frame interval statistics (for detecting network-induced stutters)
	FrameIntervals *FrameIntervalStats
	RecvTimestamps []time.Time // For calculating frame intervals
}

// runHighThroughputStressTest runs comprehensive stress tests at high bitrates
func runHighThroughputStressTest(client *TUICTestClient, echoAddr string, opts TUICClientOptions, bitrates []int, duration time.Duration, targetFPS int) {
	fmt.Println("\n  ╔═══════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════╗")
	fmt.Println("  ║                                       🔥 HIGH THROUGHPUT STRESS TEST RESULTS                                              ║")
	fmt.Println("  ╠═══════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════╣")
	fmt.Println("  ║  Bitrate  │     Actual     │  Pkt Loss   │  Integrity  │   Avg Lat    │   P99 Lat    │   Max Lat    │    Jitter    ║")
	fmt.Println("  ╠═══════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════╣")

	results := make([]*HighThroughputResult, 0, len(bitrates))

	for _, br := range bitrates {
		result := runSingleHighThroughputTest(client, echoAddr, br, duration)
		results = append(results, result)
		printHighThroughputRow(result)
		time.Sleep(2 * time.Second) // Cool down between tests
	}

	fmt.Println("  ╠═══════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════╣")
	fmt.Println("  ║  Legend: ✅ Excellent (loss<1%)  ⚠️ Warning (loss 1-5%)  ❌ Poor (loss>5%)                                                 ║")
	fmt.Println("  ╚═══════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════╝")

	// Test multiple FPS scenarios: 60fps (1080p), 120fps (2K), 240fps (4K high refresh), 360fps (competitive)
	fpsScenarios := []int{60, 120, 240, 360}

	for _, fps := range fpsScenarios {
		fmt.Printf("\n  ╔═══════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════╗\n")
		fmt.Printf("  ║                        📺 FRAME LOSS & INTERVAL TEST (%dfps @ high bitrates)                                              ║\n", fps)
		fmt.Println("  ╠═══════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════╣")
		fmt.Println("  ║  Bitrate  │ Frames Sent │ Frames Recv │ Frame Loss  │  Eff. FPS  │ Max Gap │ Out of Order │")
		fmt.Println("  ╠═══════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════╣")

		var frameResults []*HighThroughputResult
		for _, br := range bitrates {
			frameResult := runFrameLossAtBitrateWithIntervals(client, echoAddr, br, fps, duration)
			frameResults = append(frameResults, frameResult)
			printFrameLossRow(frameResult, br)
			time.Sleep(1 * time.Second)
		}

		fmt.Println("  ╠═══════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════╣")
		fmt.Printf("  ║  Frame Time Budget @ %dfps: %.4fms (frames delayed beyond this cause stutter)                                            ║\n",
			fps, 1000.0/float64(fps))
		fmt.Println("  ╚═══════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════╝")

		// Print frame interval statistics
		printFrameIntervalTable(frameResults, bitrates, fps)
	}

	// Summary and recommendations
	printStressTestSummary(results, bitrates)
}

// printFrameIntervalTable prints frame interval statistics table
func printFrameIntervalTable(results []*HighThroughputResult, bitrates []int, fps int) {
	expectedInterval := time.Duration(float64(time.Second) / float64(fps))

	fmt.Printf("\n  ╔═══════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════╗\n")
	fmt.Printf("  ║                        ⏱️  FRAME INTERVAL ANALYSIS (%dfps, expected: %.4fms)                                              ║\n", fps, float64(expectedInterval.Microseconds())/1000.0)
	fmt.Println("  ╠═══════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════╣")
	fmt.Println("  ║  Bitrate  │  Min Interval  │  Avg Interval  │  P95 Interval  │  P99 Interval  │  Max Interval  │ Late Frames │ Late Rate  ║")
	fmt.Println("  ╠═══════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════╣")

	for i, result := range results {
		if result == nil || result.FrameIntervals == nil {
			continue
		}

		stats := result.FrameIntervals
		status := "✅"
		if stats.LateFrameRate > 1 {
			status = "⚠️"
		}
		if stats.LateFrameRate > 5 || stats.MaxInterval > expectedInterval*10 {
			status = "❌"
		}

		minMs := float64(stats.MinInterval.Microseconds()) / 1000.0
		avgMs := float64(stats.AvgInterval.Microseconds()) / 1000.0
		p95Ms := float64(stats.P95Interval.Microseconds()) / 1000.0
		p99Ms := float64(stats.P99Interval.Microseconds()) / 1000.0
		maxMs := float64(stats.MaxInterval.Microseconds()) / 1000.0

		fmt.Printf("  ║ %s%4d Mbps │ %12.4fms │ %12.4fms │ %12.4fms │ %12.4fms │ %12.4fms │ %11d │ %9.4f%% ║\n",
			status, bitrates[i], minMs, avgMs, p95Ms, p99Ms, maxMs, stats.LateFrames, stats.LateFrameRate)
	}

	fmt.Println("  ╠═══════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════╣")
	fmt.Println("  ║  Late Frame = interval > expected + 50%                                                                                    ║")
	fmt.Println("  ║  ✅ Excellent (<1% late)  ⚠️ Warning (1-5% late)  ❌ Poor (>5% late or max>10x expected)                                  ║")
	fmt.Println("  ╚═══════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════╝")
}

// runSingleHighThroughputTest runs a single high throughput test with latency tracking
func runSingleHighThroughputTest(client *TUICTestClient, echoAddr string, targetMbps int, duration time.Duration) *HighThroughputResult {
	result := &HighThroughputResult{
		TargetMbps: targetMbps,
	}

	ctx := context.Background()
	udpConn, err := client.DialUDP(ctx)
	if err != nil {
		fmt.Printf("\n     ❌ Failed to create UDP connection for %d Mbps: %v\n", targetMbps, err)
		return result
	}
	defer udpConn.Close()

	targetAddr, err := net.ResolveUDPAddr("udp", echoAddr)
	if err != nil {
		return result
	}

	packetSize := 1200 // Max QUIC Datagram size
	bytesPerSec := float64(targetMbps) * 1000000 / 8
	packetsPerSec := bytesPerSec / float64(packetSize)
	sendInterval := time.Duration(float64(time.Second) / packetsPerSec)

	minInterval := time.Nanosecond * 500
	if sendInterval < minInterval {
		sendInterval = minInterval
	}

	// Batch size for high bitrates (>100 Mbps)
	batchSize := 1
	if targetMbps > 100 {
		batchSize = (targetMbps / 100)
		if batchSize > 10 {
			batchSize = 10
		}
		sendInterval = time.Microsecond * 10
	}

	var sent, received, valid int64
	var bytesSent int64
	var seqCounter uint32

	// Latency tracking
	var mu sync.Mutex
	latencies := make([]time.Duration, 0, int(duration.Seconds())*int(packetsPerSec))
	timestamps := sync.Map{}

	sendDone := make(chan struct{})
	recvDone := make(chan struct{})

	// Receiver goroutine with latency tracking
	var recvWg sync.WaitGroup
	recvWg.Add(1)
	go func() {
		defer recvWg.Done()
		recvBuf := make([]byte, 2048)
		for {
			select {
			case <-recvDone:
				return
			default:
				udpConn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
				n, _, err := udpConn.ReadFrom(recvBuf)
				if err != nil {
					continue
				}
				atomic.AddInt64(&received, 1)

				// Track latency
				if n >= 8 {
					seq := uint32(recvBuf[0])<<24 | uint32(recvBuf[1])<<16 | uint32(recvBuf[2])<<8 | uint32(recvBuf[3])
					if sentTimeI, ok := timestamps.LoadAndDelete(seq); ok {
						latency := time.Since(sentTimeI.(time.Time))
						mu.Lock()
						latencies = append(latencies, latency)
						mu.Unlock()
					}

					if verifyThroughputPacket(recvBuf[:n]) {
						atomic.AddInt64(&valid, 1)
					}
				}
			}
		}
	}()

	startTime := time.Now()

	// Progress display
	progressTicker := time.NewTicker(2 * time.Second)
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
				loss := float64(s-r) / float64(s) * 100
				if s == 0 {
					loss = 0
				}
				fmt.Printf("\r  │ %4d Mbps │ Testing... %.0f Mbps | Loss: %.1f%% | %ds     ",
					targetMbps, currentMbps, loss, int(elapsed))
			}
		}
	}()

	// Sender goroutine with batching support
	ticker := time.NewTicker(sendInterval)
	timeout := time.After(duration)
sendLoop:
	for {
		select {
		case <-timeout:
			break sendLoop
		case <-ticker.C:
			for b := 0; b < batchSize; b++ {
				seq := atomic.AddUint32(&seqCounter, 1)
				packet := buildThroughputPacket(seq, packetSize)

				timestamps.Store(seq, time.Now())

				n, err := udpConn.WriteTo(packet, targetAddr)
				if err == nil {
					atomic.AddInt64(&sent, 1)
					atomic.AddInt64(&bytesSent, int64(n))
				} else {
					timestamps.Delete(seq)
				}
			}
		}
	}

	ticker.Stop()
	close(sendDone)
	progressTicker.Stop()

	// Drain period
	time.Sleep(3 * time.Second)
	close(recvDone)
	recvWg.Wait()

	// Calculate results
	result.PacketsSent = atomic.LoadInt64(&sent)
	result.PacketsRecv = atomic.LoadInt64(&received)
	result.ActualMbps = float64(bytesSent) * 8 / duration.Seconds() / 1000000

	if result.PacketsSent > 0 {
		result.PacketLoss = float64(result.PacketsSent-result.PacketsRecv) / float64(result.PacketsSent) * 100
	}
	if result.PacketsRecv > 0 {
		result.DataIntegrity = float64(valid) / float64(result.PacketsRecv) * 100
	}

	// Calculate latency stats
	mu.Lock()
	if len(latencies) > 0 {
		sorted := make([]time.Duration, len(latencies))
		copy(sorted, latencies)
		sortDurations(sorted)

		var sum time.Duration
		for _, l := range sorted {
			sum += l
		}

		result.MinLatency = sorted[0]
		result.MaxLatency = sorted[len(sorted)-1]
		result.AvgLatency = sum / time.Duration(len(sorted))
		result.P50Latency = sorted[len(sorted)*50/100]
		result.P90Latency = sorted[len(sorted)*90/100]
		if len(sorted) > 99 {
			result.P99Latency = sorted[len(sorted)*99/100]
		} else {
			result.P99Latency = result.MaxLatency
		}

		// Calculate jitter
		var sumDiff time.Duration
		for _, l := range sorted {
			diff := l - result.AvgLatency
			if diff < 0 {
				diff = -diff
			}
			sumDiff += diff
		}
		result.Jitter = sumDiff / time.Duration(len(sorted))
	}
	mu.Unlock()

	return result
}

// sortDurations sorts a slice of durations
func sortDurations(d []time.Duration) {
	for i := 0; i < len(d)-1; i++ {
		for j := i + 1; j < len(d); j++ {
			if d[j] < d[i] {
				d[i], d[j] = d[j], d[i]
			}
		}
	}
}

// runFrameLossAtBitrate runs frame loss test while generating background traffic at specified bitrate
func runFrameLossAtBitrate(client *TUICTestClient, echoAddr string, targetMbps int, fps int, duration time.Duration) *HighThroughputResult {
	result := &HighThroughputResult{
		TargetMbps: targetMbps,
	}

	ctx := context.Background()
	udpConn, err := client.DialUDP(ctx)
	if err != nil {
		return result
	}
	defer udpConn.Close()

	targetAddr, err := net.ResolveUDPAddr("udp", echoAddr)
	if err != nil {
		return result
	}

	frameInterval := time.Second / time.Duration(fps)

	// Calculate packet size to achieve target bitrate at target FPS
	// packets_per_sec = fps, so packet_size = (bitrate_bps / 8) / fps
	packetSize := int(float64(targetMbps) * 1000000 / 8 / float64(fps))
	if packetSize > 1200 {
		packetSize = 1200 // Cap at QUIC Datagram MTU
	}
	if packetSize < 100 {
		packetSize = 100
	}

	var seqCounter uint32
	var receivedFrames, outOfOrder int64
	var lastReceivedSeq uint32
	var mu sync.Mutex
	latencies := make([]time.Duration, 0, int(duration.Seconds())*fps)
	timestamps := sync.Map{}

	sendDone := make(chan struct{})
	recvDone := make(chan struct{})

	var recvWg sync.WaitGroup
	recvWg.Add(1)
	go func() {
		defer recvWg.Done()
		recvBuf := make([]byte, 2048)

		for {
			select {
			case <-recvDone:
				return
			default:
				udpConn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
				n, _, err := udpConn.ReadFrom(recvBuf)
				if err != nil {
					continue
				}

				if n >= 8 {
					recvSeq := uint32(recvBuf[0])<<24 | uint32(recvBuf[1])<<16 | uint32(recvBuf[2])<<8 | uint32(recvBuf[3])

					if sentTimeI, ok := timestamps.LoadAndDelete(recvSeq); ok {
						latency := time.Since(sentTimeI.(time.Time))

						mu.Lock()
						latencies = append(latencies, latency)
						if recvSeq < lastReceivedSeq && lastReceivedSeq-recvSeq < 1000 {
							atomic.AddInt64(&outOfOrder, 1)
						}
						lastReceivedSeq = recvSeq
						mu.Unlock()

						atomic.AddInt64(&receivedFrames, 1)
					}
				}
			}
		}
	}()

	startTime := time.Now()

	progressTicker := time.NewTicker(2 * time.Second)
	go func() {
		for {
			select {
			case <-sendDone:
				return
			case <-progressTicker.C:
				sent := atomic.LoadUint32(&seqCounter)
				recv := atomic.LoadInt64(&receivedFrames)
				elapsed := time.Since(startTime)
				currentFPS := float64(recv) / elapsed.Seconds()
				loss := float64(int64(sent)-recv) / float64(sent) * 100
				if sent == 0 {
					loss = 0
				}
				fmt.Printf("\r  │ %4d Mbps │ Testing... FPS: %.1f | Loss: %.1f%% | %ds     ",
					targetMbps, currentFPS, loss, int(elapsed.Seconds()))
			}
		}
	}()

	packet := make([]byte, packetSize)
	ticker := time.NewTicker(frameInterval)
	timeout := time.After(duration)

sendLoop:
	for {
		select {
		case <-timeout:
			break sendLoop
		case <-ticker.C:
			seq := atomic.AddUint32(&seqCounter, 1)

			packet[0] = byte(seq >> 24)
			packet[1] = byte(seq >> 16)
			packet[2] = byte(seq >> 8)
			packet[3] = byte(seq)

			timestamps.Store(seq, time.Now())

			_, err := udpConn.WriteTo(packet, targetAddr)
			if err != nil {
				timestamps.Delete(seq)
			}
		}
	}

	ticker.Stop()
	close(sendDone)
	progressTicker.Stop()

	time.Sleep(3 * time.Second)
	close(recvDone)
	recvWg.Wait()

	result.FramesSent = int64(atomic.LoadUint32(&seqCounter))
	result.FramesRecv = atomic.LoadInt64(&receivedFrames)
	result.OutOfOrder = atomic.LoadInt64(&outOfOrder)

	if result.FramesSent > 0 {
		result.FrameLoss = float64(result.FramesSent-result.FramesRecv) / float64(result.FramesSent) * 100
		result.EffectiveFPS = float64(result.FramesRecv) / duration.Seconds()
	}

	// Calculate max gap
	var lostSeqs []uint32
	timestamps.Range(func(key, value interface{}) bool {
		lostSeqs = append(lostSeqs, key.(uint32))
		return true
	})
	if len(lostSeqs) > 0 {
		// Sort and find max consecutive
		for i := 0; i < len(lostSeqs)-1; i++ {
			for j := i + 1; j < len(lostSeqs); j++ {
				if lostSeqs[j] < lostSeqs[i] {
					lostSeqs[i], lostSeqs[j] = lostSeqs[j], lostSeqs[i]
				}
			}
		}
		maxGap := 1
		currentGap := 1
		for i := 1; i < len(lostSeqs); i++ {
			if lostSeqs[i] == lostSeqs[i-1]+1 {
				currentGap++
				if currentGap > maxGap {
					maxGap = currentGap
				}
			} else {
				currentGap = 1
			}
		}
		result.MaxGap = maxGap
	}

	return result
}

// runFrameLossAtBitrateWithIntervals runs frame loss test with frame interval tracking
func runFrameLossAtBitrateWithIntervals(client *TUICTestClient, echoAddr string, targetMbps int, fps int, duration time.Duration) *HighThroughputResult {
	result := &HighThroughputResult{
		TargetMbps:     targetMbps,
		TargetFPS:      fps,
		RecvTimestamps: make([]time.Time, 0, int(duration.Seconds())*fps),
	}

	ctx := context.Background()
	udpConn, err := client.DialUDP(ctx)
	if err != nil {
		return result
	}
	defer udpConn.Close()

	targetAddr, err := net.ResolveUDPAddr("udp", echoAddr)
	if err != nil {
		return result
	}

	frameInterval := time.Second / time.Duration(fps)
	expectedIntervalNs := frameInterval.Nanoseconds()

	// Calculate packet size to achieve target bitrate at target FPS
	packetSize := int(float64(targetMbps) * 1000000 / 8 / float64(fps))
	if packetSize > 1200 {
		packetSize = 1200
	}
	if packetSize < 100 {
		packetSize = 100
	}

	var seqCounter uint32
	var receivedFrames, outOfOrder int64
	var lastReceivedSeq uint32
	var mu sync.Mutex
	recvTimestamps := make([]time.Time, 0, int(duration.Seconds())*fps)
	timestamps := sync.Map{}

	sendDone := make(chan struct{})
	recvDone := make(chan struct{})

	var recvWg sync.WaitGroup
	recvWg.Add(1)
	go func() {
		defer recvWg.Done()
		recvBuf := make([]byte, 2048)

		for {
			select {
			case <-recvDone:
				return
			default:
				udpConn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
				n, _, err := udpConn.ReadFrom(recvBuf)
				if err != nil {
					continue
				}

				recvTime := time.Now()

				if n >= 8 {
					recvSeq := uint32(recvBuf[0])<<24 | uint32(recvBuf[1])<<16 | uint32(recvBuf[2])<<8 | uint32(recvBuf[3])

					if _, ok := timestamps.LoadAndDelete(recvSeq); ok {
						mu.Lock()
						recvTimestamps = append(recvTimestamps, recvTime)
						if recvSeq < lastReceivedSeq && lastReceivedSeq-recvSeq < 1000 {
							atomic.AddInt64(&outOfOrder, 1)
						}
						lastReceivedSeq = recvSeq
						mu.Unlock()

						atomic.AddInt64(&receivedFrames, 1)
					}
				}
			}
		}
	}()

	startTime := time.Now()

	progressTicker := time.NewTicker(2 * time.Second)
	go func() {
		for {
			select {
			case <-sendDone:
				return
			case <-progressTicker.C:
				sent := atomic.LoadUint32(&seqCounter)
				recv := atomic.LoadInt64(&receivedFrames)
				elapsed := time.Since(startTime)
				currentFPS := float64(recv) / elapsed.Seconds()
				loss := float64(int64(sent)-recv) / float64(sent) * 100
				if sent == 0 {
					loss = 0
				}
				fmt.Printf("\r  │ %4d Mbps @ %dfps │ Testing... FPS: %.1f | Loss: %.4f%% | %ds     ",
					targetMbps, fps, currentFPS, loss, int(elapsed.Seconds()))
			}
		}
	}()

	packet := make([]byte, packetSize)
	ticker := time.NewTicker(frameInterval)
	timeout := time.After(duration)

sendLoop:
	for {
		select {
		case <-timeout:
			break sendLoop
		case <-ticker.C:
			seq := atomic.AddUint32(&seqCounter, 1)

			packet[0] = byte(seq >> 24)
			packet[1] = byte(seq >> 16)
			packet[2] = byte(seq >> 8)
			packet[3] = byte(seq)

			timestamps.Store(seq, time.Now())

			_, err := udpConn.WriteTo(packet, targetAddr)
			if err != nil {
				timestamps.Delete(seq)
			}
		}
	}

	ticker.Stop()
	close(sendDone)
	progressTicker.Stop()

	time.Sleep(3 * time.Second)
	close(recvDone)
	recvWg.Wait()

	result.FramesSent = int64(atomic.LoadUint32(&seqCounter))
	result.FramesRecv = atomic.LoadInt64(&receivedFrames)
	result.OutOfOrder = atomic.LoadInt64(&outOfOrder)

	if result.FramesSent > 0 {
		result.FrameLoss = float64(result.FramesSent-result.FramesRecv) / float64(result.FramesSent) * 100
		result.EffectiveFPS = float64(result.FramesRecv) / duration.Seconds()
	}

	// Calculate max gap
	var lostSeqs []uint32
	timestamps.Range(func(key, value interface{}) bool {
		lostSeqs = append(lostSeqs, key.(uint32))
		return true
	})
	if len(lostSeqs) > 0 {
		sortUint32s(lostSeqs)
		maxGap := 1
		currentGap := 1
		for i := 1; i < len(lostSeqs); i++ {
			if lostSeqs[i] == lostSeqs[i-1]+1 {
				currentGap++
				if currentGap > maxGap {
					maxGap = currentGap
				}
			} else {
				currentGap = 1
			}
		}
		result.MaxGap = maxGap
	}

	// Calculate frame interval statistics
	mu.Lock()
	result.RecvTimestamps = recvTimestamps
	mu.Unlock()

	if len(result.RecvTimestamps) > 1 {
		result.FrameIntervals = calculateFrameIntervalStats(result.RecvTimestamps, expectedIntervalNs)
	}

	return result
}

// sortUint32s sorts a slice of uint32
func sortUint32s(s []uint32) {
	for i := 0; i < len(s)-1; i++ {
		for j := i + 1; j < len(s); j++ {
			if s[j] < s[i] {
				s[i], s[j] = s[j], s[i]
			}
		}
	}
}

// calculateFrameIntervalStats calculates frame interval statistics
func calculateFrameIntervalStats(timestamps []time.Time, expectedIntervalNs int64) *FrameIntervalStats {
	if len(timestamps) < 2 {
		return nil
	}

	stats := &FrameIntervalStats{}

	// Calculate intervals between consecutive frames
	intervals := make([]time.Duration, 0, len(timestamps)-1)
	for i := 1; i < len(timestamps); i++ {
		interval := timestamps[i].Sub(timestamps[i-1])
		intervals = append(intervals, interval)
	}

	if len(intervals) == 0 {
		return stats
	}

	// Sort intervals for percentile calculation
	sortedIntervals := make([]time.Duration, len(intervals))
	copy(sortedIntervals, intervals)
	sortDurations(sortedIntervals)

	// Calculate basic stats
	stats.MinInterval = sortedIntervals[0]
	stats.MaxInterval = sortedIntervals[len(sortedIntervals)-1]

	var sum int64
	for _, interval := range sortedIntervals {
		sum += interval.Nanoseconds()
	}
	stats.AvgInterval = time.Duration(sum / int64(len(sortedIntervals)))

	// Percentiles
	stats.P50Interval = sortedIntervals[len(sortedIntervals)*50/100]
	stats.P90Interval = sortedIntervals[len(sortedIntervals)*90/100]
	stats.P95Interval = sortedIntervals[len(sortedIntervals)*95/100]
	if len(sortedIntervals) > 99 {
		stats.P99Interval = sortedIntervals[len(sortedIntervals)*99/100]
	} else {
		stats.P99Interval = stats.MaxInterval
	}

	// Calculate standard deviation
	var sumSquares float64
	avgNs := float64(stats.AvgInterval.Nanoseconds())
	for _, interval := range intervals {
		diff := float64(interval.Nanoseconds()) - avgNs
		sumSquares += diff * diff
	}
	if len(intervals) > 1 {
		variance := sumSquares / float64(len(intervals)-1)
		stats.StdDeviation = time.Duration(int64(sqrtFloat64(variance)))
	}

	// Count late frames (interval > expected + 50%)
	lateThreshold := time.Duration(expectedIntervalNs * 3 / 2) // 150% of expected
	for _, interval := range intervals {
		if interval > lateThreshold {
			stats.LateFrames++
		} else {
			stats.OnTimeFrames++
		}
	}
	stats.LateFrameRate = float64(stats.LateFrames) / float64(len(intervals)) * 100

	// Calculate max consecutive large intervals (potential stutter detection)
	stutterThreshold := time.Duration(expectedIntervalNs * 2) // 200% of expected
	maxGapFrames := 0
	currentGapFrames := 0
	for _, interval := range intervals {
		if interval > stutterThreshold {
			currentGapFrames++
			if currentGapFrames > maxGapFrames {
				maxGapFrames = currentGapFrames
			}
		} else {
			currentGapFrames = 0
		}
	}
	stats.MaxGapFrames = maxGapFrames

	return stats
}

// sqrtFloat64 calculates square root
func sqrtFloat64(x float64) float64 {
	if x <= 0 {
		return 0
	}
	z := x
	for i := 0; i < 100; i++ {
		z = (z + x/z) / 2
		if z*z-x < 0.0001 && x-z*z < 0.0001 {
			break
		}
	}
	return z
}

func printHighThroughputRow(result *HighThroughputResult) {
	status := "✅"
	if result.PacketLoss > 1 {
		status = "⚠️"
	}
	if result.PacketLoss > 5 {
		status = "❌"
	}

	// Format latencies with microsecond precision (4 decimal places for ms)
	avgLatMs := float64(result.AvgLatency.Microseconds()) / 1000.0
	p99LatMs := float64(result.P99Latency.Microseconds()) / 1000.0
	maxLatMs := float64(result.MaxLatency.Microseconds()) / 1000.0
	jitterMs := float64(result.Jitter.Microseconds()) / 1000.0

	fmt.Printf("\r  ║ %s%4d Mbps │ %10.4f Mbps │ %9.4f%% │ %9.4f%% │ %9.4fms │ %9.4fms │ %9.4fms │ %9.4fms ║\n",
		status, result.TargetMbps, result.ActualMbps, result.PacketLoss, result.DataIntegrity,
		avgLatMs, p99LatMs, maxLatMs, jitterMs)
}

func printFrameLossRow(result *HighThroughputResult, bitrate int) {
	status := "✅"
	if result.FrameLoss > 1 {
		status = "⚠️"
	}
	if result.FrameLoss > 5 {
		status = "❌"
	}

	fmt.Printf("\r  ║ %s%4d Mbps │ %11d │ %11d │ %9.4f%% │ %10.4f │ %7d │ %18d ║\n",
		status, bitrate, result.FramesSent, result.FramesRecv, result.FrameLoss,
		result.EffectiveFPS, result.MaxGap, result.OutOfOrder)
}

func printStressTestSummary(results []*HighThroughputResult, bitrates []int) {
	fmt.Println("\n  ╔═══════════════════════════════════════════════════════════════════════════════════════════════╗")
	fmt.Println("  ║                              📊 STRESS TEST SUMMARY                                           ║")
	fmt.Println("  ╠═══════════════════════════════════════════════════════════════════════════════════════════════╣")

	for i, result := range results {
		if result == nil || result.PacketsSent == 0 {
			continue
		}

		rating := "🟢 EXCELLENT"
		recommendation := "Perfect for game streaming"

		if result.PacketLoss > 1 || result.P99Latency > 20*time.Millisecond {
			rating = "🟡 GOOD"
			recommendation = "Suitable for most scenarios"
		}
		if result.PacketLoss > 3 || result.P99Latency > 50*time.Millisecond {
			rating = "🟠 FAIR"
			recommendation = "May experience occasional stutters"
		}
		if result.PacketLoss > 5 || result.P99Latency > 100*time.Millisecond {
			rating = "🔴 POOR"
			recommendation = "Not recommended for real-time streaming"
		}

		fmt.Printf("  ║  %4d Mbps: %s - %s                  ║\n", bitrates[i], rating, recommendation)
	}

	fmt.Println("  ╠═══════════════════════════════════════════════════════════════════════════════════════════════╣")
	fmt.Println("  ║  💡 Note: Native mode (QUIC Datagram) prioritizes latency over reliability.                   ║")
	fmt.Println("  ║     For >500Mbps sustained throughput with reliability, consider multiple streams.           ║")
	fmt.Println("  ╚═══════════════════════════════════════════════════════════════════════════════════════════════╝")
}
