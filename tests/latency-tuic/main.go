// TUIC v5 Latency Test Tool for Sunshine/Moonlight Game Streaming
//
// This tool tests TUIC v5 protocol performance for game streaming scenarios,
// including latency, throughput, and frame loss measurements.
//
// Usage:
//
//	go run ./tests/latency-tuic \
//	  -server 47.107.181.88:9443 \
//	  -uuid demo-group \
//	  -password demo-123 \
//	  -test all
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

var (
	// Server connection options
	serverAddr    = flag.String("server", "", "TUIC server address (host:port)")
	uuid          = flag.String("uuid", "", "User UUID (can be group_id)")
	password      = flag.String("password", "", "User password")
	udpRelayMode  = flag.String("mode", "native", "UDP relay mode: native (Datagram) or quic (Stream)")
	congestion    = flag.String("congestion", "bbr", "Congestion control: bbr, cubic, or new_reno")
	zeroRTT       = flag.Bool("0rtt", true, "Enable 0-RTT handshake")
	tlsServerName = flag.String("tls-server-name", "", "TLS server name (SNI)")
	tlsCertFile   = flag.String("tls-cert", "", "Path to server's TLS certificate (for verification)")

	// Test options
	testType   = flag.String("test", "all", "Test type: latency, throughput, frameloss, gamestream, stress, all")
	count      = flag.Int("count", 20, "Number of test iterations for latency tests")
	duration   = flag.Duration("duration", 10*time.Second, "Duration for streaming tests")
	fps        = flag.Int("fps", 60, "Target FPS for frame loss test")
	bitrate    = flag.Int("bitrate", 50, "Target bitrate in Mbps for throughput test")
	concurrent = flag.Int("concurrent", 4, "Number of concurrent connections for load test")
	skipDirect = flag.Bool("skip-direct", false, "Skip direct baseline tests")
	
	// High throughput stress test options
	stressBitrates = flag.String("stress-bitrates", "200,500,1000", "Comma-separated bitrates for stress test (Mbps)")

	// Local service options
	httpPort = flag.Int("http-port", 19080, "Local HTTP echo service port")
	tcpPort  = flag.Int("tcp-port", 19081, "Local TCP echo service port")
	udpPort  = flag.Int("udp-port", 19053, "Local UDP echo service port")

	// Output options
	verbose = flag.Bool("verbose", false, "Enable verbose output")
)

func main() {
	flag.Parse()

	if *serverAddr == "" {
		fmt.Println("Error: -server is required")
		fmt.Println("\nUsage:")
		flag.PrintDefaults()
		os.Exit(1)
	}
	if *uuid == "" {
		fmt.Println("Error: -uuid is required")
		os.Exit(1)
	}
	if *password == "" {
		fmt.Println("Error: -password is required")
		os.Exit(1)
	}

	printBanner()

	fmt.Println("🔧 Starting local echo services...")
	services, err := StartEchoServices(*httpPort, *tcpPort, *udpPort)
	if err != nil {
		fmt.Printf("❌ Failed to start echo services: %v\n", err)
		os.Exit(1)
	}
	defer services.Stop()

	fmt.Printf("   HTTP Echo: %s\n", services.HTTPAddr())
	fmt.Printf("   TCP Echo:  %s\n", services.TCPAddr())
	fmt.Printf("   UDP Echo:  %s\n", services.UDPAddr())
	fmt.Println()

	time.Sleep(200 * time.Millisecond)

	opts := TUICClientOptions{
		ServerAddr:     *serverAddr,
		UUID:           *uuid,
		Password:       *password,
		UDPRelayMode:   *udpRelayMode,
		Congestion:     *congestion,
		ZeroRTT:        *zeroRTT,
		TLSServerName:  *tlsServerName,
		TLSCertFile:    *tlsCertFile,
		ConnectTimeout: 15 * time.Second,
		Heartbeat:      10 * time.Second,
	}

	fmt.Println("🔗 Connecting to TUIC server...")
	client, err := NewTUICTestClient(opts)
	if err != nil {
		fmt.Printf("❌ Failed to create TUIC client: %v\n", err)
		os.Exit(1)
	}
	defer client.Close()

	fmt.Printf("   Server: %s\n", *serverAddr)
	fmt.Printf("   Mode: %s\n", *udpRelayMode)
	fmt.Printf("   Congestion: %s\n", *congestion)
	fmt.Println()

	switch strings.ToLower(*testType) {
	case "latency":
		runLatencyTests(client, services)
	case "throughput":
		runThroughputTests(client, services)
	case "frameloss":
		runFrameLossTests(client, services)
	case "gamestream":
		runGameStreamTests(client, services)
	case "stress":
		runStressTests(client, services, opts)
	case "all":
		runLatencyTests(client, services)
		fmt.Println()
		runThroughputTests(client, services)
		fmt.Println()
		runFrameLossTests(client, services)
		fmt.Println()
		runGameStreamTests(client, services)
		fmt.Println()
		runStressTests(client, services, opts)
	case "basic":
		// Basic tests without stress (faster)
		runLatencyTests(client, services)
		fmt.Println()
		runThroughputTests(client, services)
		fmt.Println()
		runFrameLossTests(client, services)
	default:
		fmt.Printf("Unknown test type: %s\n", *testType)
		fmt.Println("Available: latency, throughput, frameloss, gamestream, stress, basic, all")
		fmt.Println("  all   - Run ALL tests including stress (comprehensive)")
		fmt.Println("  basic - Run latency, throughput, frameloss only (faster)")
		os.Exit(1)
	}

	fmt.Println()
	fmt.Println("✅ All tests completed")
}

func printBanner() {
	fmt.Println("╔══════════════════════════════════════════════════════════════════════╗")
	fmt.Println("║          TUIC v5 Latency Test for Sunshine/Moonlight                 ║")
	fmt.Println("╠══════════════════════════════════════════════════════════════════════╣")
	fmt.Printf("║  Server: %-57s ║\n", *serverAddr)
	fmt.Printf("║  Mode: %-8s | UUID: %-38s ║\n", *udpRelayMode, *uuid)
	fmt.Println("╚══════════════════════════════════════════════════════════════════════╝")
	fmt.Println()
}

func runLatencyTests(client *TUICTestClient, services *EchoServices) {
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("  🎮 LATENCY TESTS")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	fmt.Printf("\n  📍 Control Command Latency (TCP, %dB)\n", ControlCmdSize)

	if !*skipDirect {
		fmt.Println("\n     🔹 Direct (localhost baseline):")
		directControl := runDirectHTTPLatencyTest(services.HTTPAddr(), *count)
		printLatencyStats("Direct Control", directControl)
	}

	fmt.Println("\n     🔹 Via TUIC:")
	tuicControl := runControlLatencyTest(client, services.HTTPAddr(), *count)
	printLatencyStats("TUIC Control", tuicControl)

	fmt.Printf("\n  📺 Video Packet Latency (UDP, %dB)\n", VideoFrameSize)

	var directVideo *LatencyResult
	if !*skipDirect {
		fmt.Println("\n     🔹 Direct (localhost baseline):")
		directVideo = runDirectLatencyTest(services.UDPAddr(), *count, VideoFrameSize)
		printLatencyStats("Direct Video", directVideo)
	}

	fmt.Println("\n     🔹 Via TUIC:")
	tuicVideo := runVideoLatencyTest(client, services.UDPAddr(), *count)
	printLatencyStats("TUIC Video", tuicVideo)

	if directVideo != nil && tuicVideo != nil {
		printLatencyComparison("VIDEO PACKET", directVideo, tuicVideo)
	}

	fmt.Printf("\n  🔊 Audio Packet Latency (UDP, %dB)\n", AudioPacketSize)

	var directAudio *LatencyResult
	if !*skipDirect {
		fmt.Println("\n     🔹 Direct (localhost baseline):")
		directAudio = runDirectLatencyTest(services.UDPAddr(), *count, AudioPacketSize)
		printLatencyStats("Direct Audio", directAudio)
	}

	fmt.Println("\n     🔹 Via TUIC:")
	tuicAudio := runAudioLatencyTest(client, services.UDPAddr(), *count)
	printLatencyStats("TUIC Audio", tuicAudio)

	if directAudio != nil && tuicAudio != nil {
		printLatencyComparison("AUDIO PACKET", directAudio, tuicAudio)
	}

	fmt.Printf("\n  ⚡ Concurrent Load Test (%d connections)\n", *concurrent)
	concurrentResult := runConcurrentLatencyTest(client, services.UDPAddr(), *concurrent, *count)
	printConcurrentLatencyStats(concurrentResult)
}

func runThroughputTests(client *TUICTestClient, services *EchoServices) {
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("  🔥 THROUGHPUT TESTS")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	// Standard bitrates covering common gaming scenarios:
	// 20 Mbps - 1080p60, 40 Mbps - 1080p120/2K60, 80 Mbps - 2K120, 150 Mbps - 4K120
	bitrates := []int{20, 40, 80}
	if *bitrate > 80 && *bitrate <= 150 {
		bitrates = append(bitrates, *bitrate)
	} else if *bitrate > 150 {
		bitrates = append(bitrates, 150, *bitrate)
	}

	runThroughputComparison(client, services.UDPAddr(), bitrates, *duration)
}

func runFrameLossTests(client *TUICTestClient, services *EchoServices) {
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Printf("  📺 FRAME LOSS TESTS (%dfps, %v)\n", *fps, *duration)
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	var directResult *FrameLossResult
	if !*skipDirect {
		fmt.Println("\n  📍 Direct Frame Test (localhost baseline):")
		directResult = runDirectFrameTest(services.UDPAddr(), *fps, *duration)
		printFrameLossResult("Direct", directResult)
	}

	fmt.Println("\n  🌐 TUIC Frame Test:")
	tuicResult := runRTSPFrameTest(client, services.UDPAddr(), *fps, *duration)
	printFrameLossResult("TUIC", tuicResult)

	if directResult != nil && tuicResult != nil {
		printFrameLossComparison(directResult, tuicResult)
	}
}

func runGameStreamTests(client *TUICTestClient, services *EchoServices) {
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Printf("  🎮 GAME STREAMING SIMULATION (%v)\n", *duration)
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	sim := runGameStreamSimulation(client, services.UDPAddr(), *duration)
	printGameStreamSimulationResult(sim)
}

func runStressTests(client *TUICTestClient, services *EchoServices, opts TUICClientOptions) {
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("  🔥 HIGH THROUGHPUT STRESS TEST")
	fmt.Printf("  Mode: %s | Duration: %v per bitrate\n", *udpRelayMode, *duration)
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	
	// Parse stress bitrates
	bitrateStrs := strings.Split(*stressBitrates, ",")
	bitrates := make([]int, 0, len(bitrateStrs))
	for _, s := range bitrateStrs {
		s = strings.TrimSpace(s)
		var br int
		fmt.Sscanf(s, "%d", &br)
		if br > 0 {
			bitrates = append(bitrates, br)
		}
	}
	
	if len(bitrates) == 0 {
		bitrates = []int{200, 500, 1000}
	}
	
	fmt.Printf("\n  📊 Testing bitrates: %v Mbps\n", bitrates)
	fmt.Println()
	
	// Run comprehensive stress tests
	runHighThroughputStressTest(client, services.UDPAddr(), opts, bitrates, *duration, *fps)
}
