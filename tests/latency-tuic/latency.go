// Package main provides latency testing for TUIC v5 protocol
package main

import (
	"context"
	"encoding/binary"
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

// LatencyResult holds latency test results
type LatencyResult struct {
	Count       int
	Success     int
	Latencies   []time.Duration
	Min         time.Duration
	Max         time.Duration
	Avg         time.Duration
	P50         time.Duration
	P90         time.Duration
	P99         time.Duration
	Jitter      time.Duration // Standard deviation
	PacketLoss  float64       // Percentage of lost packets
}

// calculateStats computes statistics from latencies
func (r *LatencyResult) calculateStats() {
	if len(r.Latencies) == 0 {
		return
	}

	// Sort latencies
	sorted := make([]time.Duration, len(r.Latencies))
	copy(sorted, r.Latencies)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	// Calculate statistics
	var sum time.Duration
	for _, l := range sorted {
		sum += l
	}

	r.Success = len(sorted)
	r.Min = sorted[0]
	r.Max = sorted[len(sorted)-1]
	r.Avg = sum / time.Duration(len(sorted))
	r.P50 = sorted[len(sorted)*50/100]
	r.P90 = sorted[len(sorted)*90/100]
	if len(sorted) > 99 {
		r.P99 = sorted[len(sorted)*99/100]
	} else {
		r.P99 = r.Max
	}

	// Calculate packet loss
	if r.Count > 0 {
		r.PacketLoss = float64(r.Count-r.Success) / float64(r.Count) * 100
	}

	// Calculate jitter (mean absolute deviation from average)
	if len(sorted) > 1 {
		var sumDiff time.Duration
		for _, l := range sorted {
			diff := l - r.Avg
			if diff < 0 {
				diff = -diff
			}
			sumDiff += diff
		}
		r.Jitter = sumDiff / time.Duration(len(sorted))
	}
}

// runControlLatencyTest tests TCP control command latency
func runControlLatencyTest(client *TUICTestClient, echoAddr string, count int) *LatencyResult {
	result := &LatencyResult{Count: count}

	for i := 0; i < count; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		
		start := time.Now()
		conn, err := client.DialTCP(ctx, echoAddr)
		cancel()
		
		if err != nil {
			fmt.Printf("     [%2d] ❌ Connect: %v\n", i+1, err)
			continue
		}

		// Send HTTP-like control request
		req := fmt.Sprintf("GET /control HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", echoAddr)
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		_, err = conn.Write([]byte(req))
		if err != nil {
			conn.Close()
			fmt.Printf("     [%2d] ❌ Write: %v\n", i+1, err)
			continue
		}

		// Read response
		buf := make([]byte, 4096)
		n, err := conn.Read(buf)
		elapsed := time.Since(start)
		conn.Close()

		if err != nil && err != io.EOF {
			fmt.Printf("     [%2d] ❌ Read: %v\n", i+1, err)
			continue
		}

		if n > 0 && strings.Contains(string(buf[:n]), "HTTP/") {
			result.Latencies = append(result.Latencies, elapsed)
			fmt.Printf("     [%2d] ✓ %v\n", i+1, elapsed.Round(time.Microsecond))
		} else {
			fmt.Printf("     [%2d] ❌ Invalid response\n", i+1)
		}
	}

	result.calculateStats()
	return result
}

// runVideoLatencyTest tests UDP video packet latency (using native mode)
func runVideoLatencyTest(client *TUICTestClient, echoAddr string, count int) *LatencyResult {
	result := &LatencyResult{Count: count}

	ctx := context.Background()
	udpConn, err := client.DialUDP(ctx)
	if err != nil {
		fmt.Printf("     ❌ Failed to create UDP connection: %v\n", err)
		return result
	}
	defer udpConn.Close()

	// Resolve target address
	targetAddr, err := net.ResolveUDPAddr("udp", echoAddr)
	if err != nil {
		fmt.Printf("     ❌ Failed to resolve address: %v\n", err)
		return result
	}

	// Create video-sized packets (1200 bytes - Moonlight max packetSize, tests QUIC Datagram MTU)
	payload := make([]byte, VideoFrameSize)
	recvBuf := make([]byte, VideoFrameSize+256)

	for i := 0; i < count; i++ {
		// Add sequence number and timestamp
		binary.BigEndian.PutUint32(payload[0:4], uint32(i))
		binary.BigEndian.PutUint64(payload[4:12], uint64(time.Now().UnixNano()))

		start := time.Now()
		
		// Send packet
		_, err := udpConn.WriteTo(payload, targetAddr)
		if err != nil {
			fmt.Printf("     [%2d] ❌ Send: %v\n", i+1, err)
			continue
		}

		// Wait for echo
		udpConn.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, _, err := udpConn.ReadFrom(recvBuf)
		elapsed := time.Since(start)

		if err != nil {
			fmt.Printf("     [%2d] ❌ Recv: %v\n", i+1, err)
			continue
		}

		if n >= 12 {
			recvSeq := binary.BigEndian.Uint32(recvBuf[0:4])
			if recvSeq == uint32(i) {
				result.Latencies = append(result.Latencies, elapsed)
				fmt.Printf("     [%2d] ✓ %v (%d bytes)\n", i+1, elapsed.Round(time.Microsecond), n)
			} else {
				fmt.Printf("     [%2d] ⚠️  Sequence mismatch: got %d, expected %d\n", i+1, recvSeq, i)
			}
		} else {
			fmt.Printf("     [%2d] ❌ Short response: %d bytes\n", i+1, n)
		}

		// Small delay between tests
		time.Sleep(10 * time.Millisecond)
	}

	result.calculateStats()
	return result
}

// runAudioLatencyTest tests UDP audio packet latency
func runAudioLatencyTest(client *TUICTestClient, echoAddr string, count int) *LatencyResult {
	result := &LatencyResult{Count: count}

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

	// Create audio-sized packets (400 bytes typical)
	payload := make([]byte, AudioPacketSize)
	recvBuf := make([]byte, AudioPacketSize+256)

	for i := 0; i < count; i++ {
		binary.BigEndian.PutUint32(payload[0:4], uint32(i))
		binary.BigEndian.PutUint64(payload[4:12], uint64(time.Now().UnixNano()))

		start := time.Now()
		
		_, err := udpConn.WriteTo(payload, targetAddr)
		if err != nil {
			fmt.Printf("     [%2d] ❌ Send: %v\n", i+1, err)
			continue
		}

		udpConn.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, _, err := udpConn.ReadFrom(recvBuf)
		elapsed := time.Since(start)

		if err != nil {
			fmt.Printf("     [%2d] ❌ Recv: %v\n", i+1, err)
			continue
		}

		if n >= 12 {
			recvSeq := binary.BigEndian.Uint32(recvBuf[0:4])
			if recvSeq == uint32(i) {
				result.Latencies = append(result.Latencies, elapsed)
				fmt.Printf("     [%2d] ✓ %v (%d bytes)\n", i+1, elapsed.Round(time.Microsecond), n)
			}
		}

		time.Sleep(5 * time.Millisecond)
	}

	result.calculateStats()
	return result
}

// runDirectLatencyTest runs direct connection latency test for baseline comparison
func runDirectLatencyTest(echoAddr string, count int, packetSize int) *LatencyResult {
	result := &LatencyResult{Count: count}

	addr, err := net.ResolveUDPAddr("udp", echoAddr)
	if err != nil {
		fmt.Printf("     ❌ Failed to resolve address: %v\n", err)
		return result
	}

	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		fmt.Printf("     ❌ Failed to connect: %v\n", err)
		return result
	}
	defer conn.Close()

	conn.SetReadBuffer(4 * 1024 * 1024)
	conn.SetWriteBuffer(4 * 1024 * 1024)

	payload := make([]byte, packetSize)
	recvBuf := make([]byte, packetSize+256)

	for i := 0; i < count; i++ {
		binary.BigEndian.PutUint32(payload[0:4], uint32(i))
		binary.BigEndian.PutUint64(payload[4:12], uint64(time.Now().UnixNano()))

		start := time.Now()
		
		conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		_, err := conn.Write(payload)
		if err != nil {
			fmt.Printf("     [%2d] ❌ Send: %v\n", i+1, err)
			continue
		}

		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, err := conn.Read(recvBuf)
		elapsed := time.Since(start)

		if err != nil {
			fmt.Printf("     [%2d] ❌ Recv: %v\n", i+1, err)
			continue
		}

		if n >= 4 {
			result.Latencies = append(result.Latencies, elapsed)
			fmt.Printf("     [%2d] ✓ %v (%d bytes)\n", i+1, elapsed.Round(time.Microsecond), n)
		}

		time.Sleep(10 * time.Millisecond)
	}

	result.calculateStats()
	return result
}

// runDirectHTTPLatencyTest runs direct HTTP latency test for baseline
func runDirectHTTPLatencyTest(httpAddr string, count int) *LatencyResult {
	result := &LatencyResult{Count: count}
	url := "http://" + httpAddr + "/control"

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

		result.Latencies = append(result.Latencies, elapsed)
		fmt.Printf("     [%2d] ✓ %v\n", i+1, elapsed.Round(time.Microsecond))
	}

	result.calculateStats()
	return result
}

// printLatencyStats prints formatted latency statistics
func printLatencyStats(name string, result *LatencyResult) {
	if result == nil || len(result.Latencies) == 0 {
		fmt.Printf("\n     ⚠️  %s: No successful tests\n", name)
		return
	}

	// Helper to format duration with 4 decimal places in ms
	formatMs := func(d time.Duration) string {
		return fmt.Sprintf("%.4fms", float64(d.Microseconds())/1000.0)
	}

	fmt.Printf("\n     📊 %s (%d/%d success, %.4f%% loss)\n", name, result.Success, result.Count, result.PacketLoss)
	fmt.Printf("     ┌──────────────────────────────┐\n")
	fmt.Printf("     │ Min:    %18s  │\n", formatMs(result.Min))
	fmt.Printf("     │ Avg:    %18s  │\n", formatMs(result.Avg))
	fmt.Printf("     │ P50:    %18s  │\n", formatMs(result.P50))
	fmt.Printf("     │ P90:    %18s  │\n", formatMs(result.P90))
	fmt.Printf("     │ P99:    %18s  │\n", formatMs(result.P99))
	fmt.Printf("     │ Max:    %18s  │\n", formatMs(result.Max))
	fmt.Printf("     │ Jitter: %18s  │\n", formatMs(result.Jitter))
	fmt.Printf("     └──────────────────────────────┘\n")
}

// printLatencyComparison prints comparison between direct and TUIC results
func printLatencyComparison(name string, direct, tuic *LatencyResult) {
	if direct == nil || tuic == nil || len(direct.Latencies) == 0 || len(tuic.Latencies) == 0 {
		return
	}

	// Helper to format duration with 4 decimal places in ms
	formatMs := func(d time.Duration) string {
		return fmt.Sprintf("%.4fms", float64(d.Microseconds())/1000.0)
	}

	overhead := tuic.Avg - direct.Avg
	ratio := float64(tuic.Avg) / float64(direct.Avg)

	fmt.Println()
	fmt.Printf("  ╔═══════════════════════════════════════════════════════════════════════╗\n")
	fmt.Printf("  ║                    📈 %s COMPARISON                      ║\n", name)
	fmt.Printf("  ╠═══════════════════════════════════════════════════════════════════════╣\n")
	fmt.Printf("  ║  Direct:    %18s                                       ║\n", formatMs(direct.Avg))
	fmt.Printf("  ║  TUIC:      %18s                                       ║\n", formatMs(tuic.Avg))
	fmt.Printf("  ║  Overhead:  %18s                                       ║\n", formatMs(overhead))
	fmt.Printf("  ║  Ratio:     %18.4fx                                      ║\n", ratio)
	fmt.Printf("  ║  Jitter:    %18s → %18s                 ║\n", formatMs(direct.Jitter), formatMs(tuic.Jitter))
	fmt.Printf("  ╚═══════════════════════════════════════════════════════════════════════╝\n")

	// Performance assessment
	if overhead < 10*time.Millisecond {
		fmt.Println("  🟢 EXCELLENT - Minimal overhead for game streaming")
	} else if overhead < 30*time.Millisecond {
		fmt.Println("  🟡 GOOD - Acceptable for most games")
	} else if overhead < 50*time.Millisecond {
		fmt.Println("  🟠 FAIR - May notice lag in fast-paced games")
	} else {
		fmt.Println("  🔴 POOR - Significant overhead, investigate cause")
	}
}

// ConcurrentLatencyResult holds results for concurrent latency test
type ConcurrentLatencyResult struct {
	*LatencyResult
	Concurrency int
	TotalRPS    float64 // Requests per second
}

// runConcurrentLatencyTest tests latency under concurrent load
func runConcurrentLatencyTest(client *TUICTestClient, echoAddr string, concurrency, count int) *ConcurrentLatencyResult {
	result := &ConcurrentLatencyResult{
		LatencyResult: &LatencyResult{Count: count * concurrency},
		Concurrency:   concurrency,
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	var totalSuccess int64

	startTime := time.Now()

	for c := 0; c < concurrency; c++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			ctx := context.Background()
			udpConn, err := client.DialUDP(ctx)
			if err != nil {
				return
			}
			defer udpConn.Close()

			targetAddr, err := net.ResolveUDPAddr("udp", echoAddr)
			if err != nil {
				return
			}

			payload := make([]byte, VideoFrameSize)
			recvBuf := make([]byte, VideoFrameSize+256)

			for i := 0; i < count; i++ {
				seq := uint32(workerID*count + i)
				binary.BigEndian.PutUint32(payload[0:4], seq)

				start := time.Now()
				udpConn.WriteTo(payload, targetAddr)
				
				udpConn.SetReadDeadline(time.Now().Add(2 * time.Second))
				n, _, err := udpConn.ReadFrom(recvBuf)
				elapsed := time.Since(start)

				if err == nil && n >= 4 {
					atomic.AddInt64(&totalSuccess, 1)
					mu.Lock()
					result.Latencies = append(result.Latencies, elapsed)
					mu.Unlock()
				}
			}
		}(c)
	}

	wg.Wait()
	totalDuration := time.Since(startTime)

	result.calculateStats()
	result.TotalRPS = float64(totalSuccess) / totalDuration.Seconds()

	return result
}

func printConcurrentLatencyStats(result *ConcurrentLatencyResult) {
	if result == nil || len(result.Latencies) == 0 {
		fmt.Println("     ⚠️  No successful tests")
		return
	}

	fmt.Printf("\n     📊 Concurrent Test (%d workers, %d/%d success)\n", 
		result.Concurrency, result.Success, result.Count)
	fmt.Printf("     ┌────────────────────────────────────┐\n")
	fmt.Printf("     │ Throughput:  %8.1f RPS          │\n", result.TotalRPS)
	fmt.Printf("     │ Min:         %10v            │\n", result.Min.Round(time.Microsecond))
	fmt.Printf("     │ Avg:         %10v            │\n", result.Avg.Round(time.Microsecond))
	fmt.Printf("     │ P50:         %10v            │\n", result.P50.Round(time.Microsecond))
	fmt.Printf("     │ P90:         %10v            │\n", result.P90.Round(time.Microsecond))
	fmt.Printf("     │ P99:         %10v            │\n", result.P99.Round(time.Microsecond))
	fmt.Printf("     │ Max:         %10v            │\n", result.Max.Round(time.Microsecond))
	fmt.Printf("     │ Loss:        %8.2f%%            │\n", result.PacketLoss)
	fmt.Printf("     └────────────────────────────────────┘\n")
}
