// Package main provides frame loss testing for TUIC v5 protocol
// Simulates RTSP video streaming scenario (Sunshine/Moonlight)
package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"net"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// FrameLossResult holds frame loss test results
type FrameLossResult struct {
	TotalFrames     int64
	ReceivedFrames  int64
	LossRate        float64
	OutOfOrder      int64
	MaxGap          int
	Latencies       []time.Duration
	AvgLatency      time.Duration
	MinLatency      time.Duration
	MaxLatency      time.Duration
	P50Latency      time.Duration
	P90Latency      time.Duration
	P99Latency      time.Duration
	Jitter          time.Duration
	Duration        time.Duration
	EffectiveFPS    float64
	TargetFPS       int
}

// calculateStats computes statistics for frame loss results
func (r *FrameLossResult) calculateStats() {
	if len(r.Latencies) == 0 {
		return
	}

	sorted := make([]time.Duration, len(r.Latencies))
	copy(sorted, r.Latencies)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	var sum time.Duration
	for _, l := range sorted {
		sum += l
	}

	r.ReceivedFrames = int64(len(sorted))
	r.MinLatency = sorted[0]
	r.MaxLatency = sorted[len(sorted)-1]
	r.AvgLatency = sum / time.Duration(len(sorted))
	r.P50Latency = sorted[len(sorted)*50/100]
	r.P90Latency = sorted[len(sorted)*90/100]
	if len(sorted) > 99 {
		r.P99Latency = sorted[len(sorted)*99/100]
	} else {
		r.P99Latency = r.MaxLatency
	}

	if r.TotalFrames > 0 {
		r.LossRate = float64(r.TotalFrames-r.ReceivedFrames) / float64(r.TotalFrames) * 100
	}

	if r.Duration > 0 {
		r.EffectiveFPS = float64(r.ReceivedFrames) / r.Duration.Seconds()
	}

	if len(sorted) > 1 {
		var sumDiff time.Duration
		for _, l := range sorted {
			diff := l - r.AvgLatency
			if diff < 0 {
				diff = -diff
			}
			sumDiff += diff
		}
		r.Jitter = sumDiff / time.Duration(len(sorted))
	}
}

// runRTSPFrameTest simulates RTSP video streaming with specified FPS
func runRTSPFrameTest(client *TUICTestClient, echoAddr string, fps int, duration time.Duration) *FrameLossResult {
	result := &FrameLossResult{
		TargetFPS: fps,
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

	frameInterval := time.Second / time.Duration(fps)

	var seqCounter uint32
	var receivedFrames int64
	var outOfOrder int64
	var lastReceivedSeq uint32
	var mu sync.Mutex
	latencies := make([]time.Duration, 0, int(duration.Seconds())*fps)

	frameTimestamps := sync.Map{}

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

				if n >= 12 {
					recvSeq := binary.BigEndian.Uint32(recvBuf[0:4])

					if sentTimeI, ok := frameTimestamps.LoadAndDelete(recvSeq); ok {
						sentTime := sentTimeI.(time.Time)
						latency := time.Since(sentTime)

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

	progressTicker := time.NewTicker(1 * time.Second)
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
				lossRate := float64(int64(sent)-recv) / float64(sent) * 100
				if sent == 0 {
					lossRate = 0
				}
				fmt.Printf("\r     [%v] Sent: %d | Recv: %d | FPS: %.1f | Loss: %.1f%%",
					elapsed.Round(time.Second), sent, recv, currentFPS, lossRate)
			}
		}
	}()

	packet := make([]byte, VideoFrameSize)
	ticker := time.NewTicker(frameInterval)
	timeout := time.After(duration)

sendLoop:
	for {
		select {
		case <-timeout:
			break sendLoop
		case <-ticker.C:
			seq := atomic.AddUint32(&seqCounter, 1)

			binary.BigEndian.PutUint32(packet[0:4], seq)
			binary.BigEndian.PutUint64(packet[4:12], uint64(time.Now().UnixNano()))

			for i := 12; i < VideoFrameSize; i++ {
				packet[i] = byte((int(seq) + i) % 256)
			}

			frameTimestamps.Store(seq, time.Now())

			_, err := udpConn.WriteTo(packet, targetAddr)
			if err != nil {
				frameTimestamps.Delete(seq)
			}
		}
	}

	ticker.Stop()
	close(sendDone)
	progressTicker.Stop()

	drainTimeout := time.Second * 3
	time.Sleep(drainTimeout)
	close(recvDone)
	recvWg.Wait()

	fmt.Println()

	result.TotalFrames = int64(atomic.LoadUint32(&seqCounter))
	result.OutOfOrder = atomic.LoadInt64(&outOfOrder)
	result.Duration = duration

	mu.Lock()
	result.Latencies = latencies
	mu.Unlock()

	result.MaxGap = calculateMaxGap(result.TotalFrames, &frameTimestamps)

	result.calculateStats()
	return result
}

// calculateMaxGap finds the maximum number of consecutive lost frames
func calculateMaxGap(totalFrames int64, sentFrames *sync.Map) int {
	var lostSeqs []uint32
	sentFrames.Range(func(key, value interface{}) bool {
		lostSeqs = append(lostSeqs, key.(uint32))
		return true
	})

	if len(lostSeqs) == 0 {
		return 0
	}

	sort.Slice(lostSeqs, func(i, j int) bool { return lostSeqs[i] < lostSeqs[j] })

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

	return maxGap
}

// runDirectFrameTest runs direct frame test for baseline comparison
func runDirectFrameTest(echoAddr string, fps int, duration time.Duration) *FrameLossResult {
	result := &FrameLossResult{
		TargetFPS: fps,
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

	conn.SetReadBuffer(16 * 1024 * 1024)
	conn.SetWriteBuffer(16 * 1024 * 1024)

	frameInterval := time.Second / time.Duration(fps)

	var seqCounter uint32
	var receivedFrames int64
	var outOfOrder int64
	var lastReceivedSeq uint32
	var mu sync.Mutex
	latencies := make([]time.Duration, 0, int(duration.Seconds())*fps)

	frameTimestamps := sync.Map{}

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
				conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
				n, _, err := conn.ReadFromUDP(recvBuf)
				if err != nil {
					continue
				}

				if n >= 12 {
					recvSeq := binary.BigEndian.Uint32(recvBuf[0:4])

					if sentTimeI, ok := frameTimestamps.LoadAndDelete(recvSeq); ok {
						sentTime := sentTimeI.(time.Time)
						latency := time.Since(sentTime)

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

	progressTicker := time.NewTicker(1 * time.Second)
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
				fmt.Printf("\r     [%v] Sent: %d | Recv: %d | FPS: %.1f",
					elapsed.Round(time.Second), sent, recv, currentFPS)
			}
		}
	}()

	packet := make([]byte, VideoFrameSize)
	ticker := time.NewTicker(frameInterval)
	timeout := time.After(duration)

sendLoop:
	for {
		select {
		case <-timeout:
			break sendLoop
		case <-ticker.C:
			seq := atomic.AddUint32(&seqCounter, 1)

			binary.BigEndian.PutUint32(packet[0:4], seq)
			binary.BigEndian.PutUint64(packet[4:12], uint64(time.Now().UnixNano()))
			for i := 12; i < VideoFrameSize; i++ {
				packet[i] = byte((int(seq) + i) % 256)
			}

			frameTimestamps.Store(seq, time.Now())
			_, err := conn.WriteToUDP(packet, targetAddr)
			if err != nil {
				frameTimestamps.Delete(seq)
			}
		}
	}

	ticker.Stop()
	close(sendDone)
	progressTicker.Stop()

	time.Sleep(time.Second)
	close(recvDone)
	recvWg.Wait()

	fmt.Println()

	result.TotalFrames = int64(atomic.LoadUint32(&seqCounter))
	result.OutOfOrder = atomic.LoadInt64(&outOfOrder)
	result.Duration = duration

	mu.Lock()
	result.Latencies = latencies
	mu.Unlock()

	result.MaxGap = calculateMaxGap(result.TotalFrames, &frameTimestamps)
	result.calculateStats()
	return result
}

// printFrameLossResult prints frame loss test results
func printFrameLossResult(name string, result *FrameLossResult) {
	if result == nil || result.TotalFrames == 0 {
		fmt.Printf("\n     ⚠️  %s: No frames sent\n", name)
		return
	}

	// Helper to format duration with 4 decimal places in ms
	formatMs := func(d time.Duration) string {
		return fmt.Sprintf("%.4fms", float64(d.Microseconds())/1000.0)
	}

	fmt.Printf("\n     📺 %s (%dfps, %v)\n", name, result.TargetFPS, result.Duration)
	fmt.Printf("     ┌─────────────────────────────────────────────────────┐\n")
	fmt.Printf("     │ Frames Sent:     %8d                           │\n", result.TotalFrames)
	fmt.Printf("     │ Frames Received: %8d                           │\n", result.ReceivedFrames)
	fmt.Printf("     │ Loss Rate:       %12.4f%%                       │\n", result.LossRate)
	fmt.Printf("     │ Out of Order:    %8d                           │\n", result.OutOfOrder)
	fmt.Printf("     │ Max Gap:         %8d frames                    │\n", result.MaxGap)
	fmt.Printf("     │ Effective FPS:   %12.4f                        │\n", result.EffectiveFPS)
	fmt.Printf("     ├─────────────────────────────────────────────────────┤\n")
	fmt.Printf("     │ Min Latency:     %16s                  │\n", formatMs(result.MinLatency))
	fmt.Printf("     │ Avg Latency:     %16s                  │\n", formatMs(result.AvgLatency))
	fmt.Printf("     │ P50 Latency:     %16s                  │\n", formatMs(result.P50Latency))
	fmt.Printf("     │ P90 Latency:     %16s                  │\n", formatMs(result.P90Latency))
	fmt.Printf("     │ P99 Latency:     %16s                  │\n", formatMs(result.P99Latency))
	fmt.Printf("     │ Max Latency:     %16s                  │\n", formatMs(result.MaxLatency))
	fmt.Printf("     │ Jitter:          %16s                  │\n", formatMs(result.Jitter))
	fmt.Printf("     └─────────────────────────────────────────────────────┘\n")

	if result.LossRate > 5 {
		fmt.Printf("     ⚠️  WARNING: Frame loss (%.4f%%) exceeds 5%% threshold\n", result.LossRate)
	}
	if result.MaxGap > 3 {
		fmt.Printf("     ⚠️  WARNING: Max consecutive frame loss (%d) may cause visible stutter\n", result.MaxGap)
	}
	if result.Jitter > 20*time.Millisecond {
		fmt.Printf("     ⚠️  WARNING: High jitter (%s) may affect smoothness\n", formatMs(result.Jitter))
	}
}

// printFrameLossComparison prints comparison between direct and TUIC results
func printFrameLossComparison(direct, tuic *FrameLossResult) {
	if direct == nil || tuic == nil || direct.TotalFrames == 0 || tuic.TotalFrames == 0 {
		return
	}

	// Helper to format duration with 4 decimal places in ms
	formatMs := func(d time.Duration) string {
		return fmt.Sprintf("%.4fms", float64(d.Microseconds())/1000.0)
	}

	fmt.Println()
	fmt.Println("  ╔═════════════════════════════════════════════════════════════════════════════════╗")
	fmt.Println("  ║                           📺 FRAME LOSS COMPARISON                              ║")
	fmt.Println("  ╠═════════════════════════════════════════════════════════════════════════════════╣")
	fmt.Printf("  ║  Loss Rate:    Direct %10.4f%% → TUIC %10.4f%% (%+10.4f%%)         ║\n",
		direct.LossRate, tuic.LossRate, tuic.LossRate-direct.LossRate)
	fmt.Printf("  ║  Avg Latency:  Direct %14s → TUIC %14s              ║\n",
		formatMs(direct.AvgLatency), formatMs(tuic.AvgLatency))
	fmt.Printf("  ║  Jitter:       Direct %14s → TUIC %14s              ║\n",
		formatMs(direct.Jitter), formatMs(tuic.Jitter))
	fmt.Printf("  ║  Out of Order: Direct %8d → TUIC %8d                               ║\n",
		direct.OutOfOrder, tuic.OutOfOrder)
	fmt.Printf("  ║  Max Gap:      Direct %8d → TUIC %8d                               ║\n",
		direct.MaxGap, tuic.MaxGap)
	fmt.Println("  ╚═════════════════════════════════════════════════════════════════════════════════╝")

	if tuic.LossRate < 2 && tuic.Jitter < 15*time.Millisecond {
		fmt.Println("  🟢 EXCELLENT - Suitable for smooth game streaming")
	} else if tuic.LossRate < 5 && tuic.Jitter < 30*time.Millisecond {
		fmt.Println("  🟡 GOOD - Playable with occasional frame drops")
	} else if tuic.LossRate < 10 {
		fmt.Println("  🟠 FAIR - Noticeable frame drops, may affect gameplay")
	} else {
		fmt.Println("  🔴 POOR - Significant frame loss, not suitable for gaming")
	}
}

// GameStreamSimulation simulates full Sunshine/Moonlight session
type GameStreamSimulation struct {
	VideoResult   *FrameLossResult
	AudioResult   *FrameLossResult
	ControlResult *LatencyResult
	Duration      time.Duration
	TotalBitrate  float64
}

// runGameStreamSimulation runs a full game streaming simulation
func runGameStreamSimulation(client *TUICTestClient, echoAddr string, duration time.Duration) *GameStreamSimulation {
	result := &GameStreamSimulation{
		Duration: duration,
	}

	fmt.Println("\n     🎮 Starting game stream simulation...")
	fmt.Printf("     Video: 60fps x 1.4KB | Audio: 100pps x 400B | Control: 20pps\n")
	fmt.Println()

	var wg sync.WaitGroup
	var mu sync.Mutex

	wg.Add(1)
	go func() {
		defer wg.Done()
		videoResult := runRTSPFrameTest(client, echoAddr, 60, duration)
		mu.Lock()
		result.VideoResult = videoResult
		mu.Unlock()
	}()

	wg.Wait()

	fmt.Println("\n     🔊 Testing audio stream...")
	result.AudioResult = runAudioStreamTest(client, echoAddr, 100, duration)

	videoMbps := float64(result.VideoResult.ReceivedFrames) * float64(VideoFrameSize) * 8 / duration.Seconds() / 1000000
	audioMbps := float64(result.AudioResult.ReceivedFrames) * float64(AudioPacketSize) * 8 / duration.Seconds() / 1000000
	result.TotalBitrate = videoMbps + audioMbps

	return result
}

// runAudioStreamTest tests audio stream (100 packets per second)
func runAudioStreamTest(client *TUICTestClient, echoAddr string, pps int, duration time.Duration) *FrameLossResult {
	result := &FrameLossResult{
		TargetFPS: pps,
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
		return result
	}

	packetInterval := time.Second / time.Duration(pps)

	var seqCounter uint32
	var receivedFrames int64
	var mu sync.Mutex
	latencies := make([]time.Duration, 0, int(duration.Seconds())*pps)
	frameTimestamps := sync.Map{}

	sendDone := make(chan struct{})
	recvDone := make(chan struct{})

	var recvWg sync.WaitGroup
	recvWg.Add(1)
	go func() {
		defer recvWg.Done()
		recvBuf := make([]byte, 1024)

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

				if n >= 12 {
					recvSeq := binary.BigEndian.Uint32(recvBuf[0:4])
					if sentTimeI, ok := frameTimestamps.LoadAndDelete(recvSeq); ok {
						sentTime := sentTimeI.(time.Time)
						latency := time.Since(sentTime)
						mu.Lock()
						latencies = append(latencies, latency)
						mu.Unlock()
						atomic.AddInt64(&receivedFrames, 1)
					}
				}
			}
		}
	}()

	progressTicker := time.NewTicker(1 * time.Second)
	go func() {
		for {
			select {
			case <-sendDone:
				return
			case <-progressTicker.C:
				sent := atomic.LoadUint32(&seqCounter)
				recv := atomic.LoadInt64(&receivedFrames)
				fmt.Printf("\r     [Audio] Sent: %d | Recv: %d", sent, recv)
			}
		}
	}()

	packet := make([]byte, AudioPacketSize)
	ticker := time.NewTicker(packetInterval)
	timeout := time.After(duration)

sendLoop:
	for {
		select {
		case <-timeout:
			break sendLoop
		case <-ticker.C:
			seq := atomic.AddUint32(&seqCounter, 1)
			binary.BigEndian.PutUint32(packet[0:4], seq)
			binary.BigEndian.PutUint64(packet[4:12], uint64(time.Now().UnixNano()))

			frameTimestamps.Store(seq, time.Now())
			udpConn.WriteTo(packet, targetAddr)
		}
	}

	ticker.Stop()
	close(sendDone)
	progressTicker.Stop()

	time.Sleep(time.Second)
	close(recvDone)
	recvWg.Wait()

	fmt.Println()

	result.TotalFrames = int64(atomic.LoadUint32(&seqCounter))
	result.Duration = duration
	mu.Lock()
	result.Latencies = latencies
	mu.Unlock()
	result.calculateStats()
	return result
}

func printGameStreamSimulationResult(sim *GameStreamSimulation) {
	// Helper to format duration with 4 decimal places in ms
	formatMs := func(d time.Duration) string {
		return fmt.Sprintf("%.4fms", float64(d.Microseconds())/1000.0)
	}

	fmt.Println()
	fmt.Println("  ╔═════════════════════════════════════════════════════════════════════════════════╗")
	fmt.Println("  ║                    🎮 GAME STREAMING SIMULATION RESULTS                         ║")
	fmt.Println("  ╠═════════════════════════════════════════════════════════════════════════════════╣")

	if sim.VideoResult != nil {
		videoQuality := "🟢 GOOD"
		if sim.VideoResult.LossRate > 5 {
			videoQuality = "🔴 POOR"
		} else if sim.VideoResult.LossRate > 2 {
			videoQuality = "🟡 FAIR"
		}
		fmt.Printf("  ║  VIDEO:  Loss %10.4f%% | Latency %14s | %s              ║\n",
			sim.VideoResult.LossRate, formatMs(sim.VideoResult.AvgLatency), videoQuality)
	}

	if sim.AudioResult != nil {
		audioQuality := "🟢 GOOD"
		if sim.AudioResult.LossRate > 5 {
			audioQuality = "🔴 POOR"
		} else if sim.AudioResult.LossRate > 2 {
			audioQuality = "🟡 FAIR"
		}
		fmt.Printf("  ║  AUDIO:  Loss %10.4f%% | Latency %14s | %s              ║\n",
			sim.AudioResult.LossRate, formatMs(sim.AudioResult.AvgLatency), audioQuality)
	}

	fmt.Printf("  ║  BITRATE: %10.4f Mbps effective                                              ║\n", sim.TotalBitrate)
	fmt.Println("  ╚═════════════════════════════════════════════════════════════════════════════════╝")

	if sim.VideoResult != nil && sim.AudioResult != nil {
		totalLoss := (sim.VideoResult.LossRate + sim.AudioResult.LossRate) / 2
		avgLatency := (sim.VideoResult.AvgLatency + sim.AudioResult.AvgLatency) / 2

		if totalLoss < 2 && avgLatency < 50*time.Millisecond {
			fmt.Println("  🎮 RATING: EXCELLENT - Perfect for competitive gaming")
		} else if totalLoss < 5 && avgLatency < 80*time.Millisecond {
			fmt.Println("  🎮 RATING: GOOD - Suitable for most games")
		} else if totalLoss < 10 && avgLatency < 150*time.Millisecond {
			fmt.Println("  🎮 RATING: FAIR - Playable but may notice lag")
		} else {
			fmt.Println("  🎮 RATING: POOR - Not recommended for gaming")
		}
	}
}

// calculateStdDev calculates standard deviation of durations
func calculateStdDev(values []time.Duration, mean time.Duration) time.Duration {
	if len(values) < 2 {
		return 0
	}

	var sumSquares float64
	for _, v := range values {
		diff := float64(v - mean)
		sumSquares += diff * diff
	}

	variance := sumSquares / float64(len(values)-1)
	return time.Duration(math.Sqrt(variance))
}
