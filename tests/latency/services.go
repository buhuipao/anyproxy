package main

import (
	"fmt"
	"net"
	"net/http"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// GameStreamServices simulates Sunshine/Moonlight services
// Optimized for high-throughput testing (500Mbps+)
type GameStreamServices struct {
	httpPort    int
	udpPort     int
	tcpEchoPort int // TCP echo service for large packet tests
	httpLn      net.Listener
	tcpEchoLn   net.Listener
	udpConn     *net.UDPConn
	wg          sync.WaitGroup
	stop        chan struct{}

	// Atomic counters for lock-free statistics
	udpPacketsReceived int64
	udpBytesReceived   int64
	udpPacketsSent     int64
	udpBytesSent       int64
	udpValidPackets    int64 // Packets with correct checksum
	udpChecksumErrors  int64 // Packets with incorrect checksum

	// TCP echo stats
	tcpPacketsReceived int64
	tcpBytesReceived   int64
	tcpPacketsSent     int64
	tcpBytesSent       int64
}

// Realistic Moonlight packet sizes (per UDP packet, not per frame):
// - Video: ~1200 bytes per packet (tests QUIC Datagram MTU limit)
// - Audio: ~400 bytes per packet
// - Control: ~100-200 bytes
const (
	// Realistic video packet size (single UDP packet from fragmented frame)
	// Based on Moonlight/Sunshine: max packetSize = 1200 bytes (tests QUIC Datagram MTU)
	// QUIC Datagram MTU: ~1200-1252 bytes (packets > 1200 use reliable stream fallback)
	VideoFrameSize = 1200
	// Realistic audio packet size
	AudioPacketSize = 400
	// Control command size
	ControlCmdSize = 128
	// Larger test packet for throughput testing (within MTU)
	LargePacketSize = 8000
	// Max UDP packet size
	MaxUDPPacketSize = 65535
)

// Buffer pools to reduce GC pressure during high-throughput tests
var (
	// Pool for small packets (control, audio)
	smallBufferPool = sync.Pool{
		New: func() interface{} {
			buf := make([]byte, 2048)
			return &buf
		},
	}
	// Pool for medium packets (video)
	mediumBufferPool = sync.Pool{
		New: func() interface{} {
			buf := make([]byte, 16384)
			return &buf
		},
	}
	// Pool for large packets (stress test)
	largeBufferPool = sync.Pool{
		New: func() interface{} {
			buf := make([]byte, MaxUDPPacketSize)
			return &buf
		},
	}
)

func getBuffer(size int) *[]byte {
	if size <= 2048 {
		return smallBufferPool.Get().(*[]byte)
	} else if size <= 16384 {
		return mediumBufferPool.Get().(*[]byte)
	}
	return largeBufferPool.Get().(*[]byte)
}

func putBuffer(buf *[]byte) {
	size := len(*buf)
	if size <= 2048 {
		smallBufferPool.Put(buf)
	} else if size <= 16384 {
		mediumBufferPool.Put(buf)
	} else {
		largeBufferPool.Put(buf)
	}
}

func StartGameServices(httpPort, udpPort int) (*GameStreamServices, error) {
	s := &GameStreamServices{
		httpPort:    httpPort,
		udpPort:     udpPort,
		tcpEchoPort: httpPort + 1, // TCP echo on httpPort+1 (19081)
		stop:        make(chan struct{}),
	}

	if err := s.startHTTP(); err != nil {
		return nil, fmt.Errorf("start http: %w", err)
	}

	if err := s.startUDP(); err != nil {
		s.Stop()
		return nil, fmt.Errorf("start udp: %w", err)
	}

	if err := s.startTCPEcho(); err != nil {
		s.Stop()
		return nil, fmt.Errorf("start tcp echo: %w", err)
	}

	return s, nil
}

func (s *GameStreamServices) startHTTP() error {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", s.httpPort))
	if err != nil {
		return err
	}
	s.httpLn = ln

	mux := http.NewServeMux()

	mux.HandleFunc("/control", func(w http.ResponseWriter, r *http.Request) {
		response := make([]byte, ControlCmdSize)
		ts := time.Now().UnixNano()
		for i := 0; i < 8; i++ {
			response[i] = byte(ts >> (56 - i*8))
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(response)
	})

	mux.HandleFunc("/video", func(w http.ResponseWriter, r *http.Request) {
		frame := generateVideoFrame()
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(frame)))
		w.Write(frame)
	})

	server := &http.Server{Handler: mux}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		server.Serve(ln)
	}()

	return nil
}

func (s *GameStreamServices) startUDP() error {
	addr := &net.UDPAddr{Port: s.udpPort}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return err
	}
	s.udpConn = conn

	// Maximum buffers for high-throughput
	conn.SetReadBuffer(64 * 1024 * 1024)  // 64MB read buffer
	conn.SetWriteBuffer(64 * 1024 * 1024) // 64MB write buffer

	// Start many worker goroutines for parallel processing
	// More workers = better parallelism for high throughput
	numWorkers := runtime.NumCPU() * 4 // 4x CPU cores
	if numWorkers < 16 {
		numWorkers = 16
	}
	if numWorkers > 64 {
		numWorkers = 64
	}

	for i := 0; i < numWorkers; i++ {
		s.wg.Add(1)
		go s.udpWorker(i)
	}

	return nil
}

// Packet format for validation:
// [0:4]   - Sequence number (big-endian uint32)
// [4:8]   - Checksum (XOR of all payload bytes, repeated 4 times)
// [8:]    - Payload data
const PacketHeaderSize = 8

// udpWorker processes UDP packets in parallel with data validation
func (s *GameStreamServices) udpWorker(id int) {
	defer s.wg.Done()

	// Each worker has its own receive buffer from pool
	bufPtr := largeBufferPool.Get().(*[]byte)
	buf := *bufPtr
	defer largeBufferPool.Put(bufPtr)

	for {
		select {
		case <-s.stop:
			return
		default:
			s.udpConn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			n, remoteAddr, err := s.udpConn.ReadFromUDP(buf)
			if err != nil {
				continue
			}

			// Atomic statistics update (lock-free)
			atomic.AddInt64(&s.udpPacketsReceived, 1)
			atomic.AddInt64(&s.udpBytesReceived, int64(n))

			// Validate packet if it has header
			valid := true
			if n >= PacketHeaderSize {
				// Verify checksum
				expectedChecksum := buf[4]
				actualChecksum := calcChecksum(buf[PacketHeaderSize:n])
				if expectedChecksum != actualChecksum {
					valid = false
					errCount := atomic.AddInt64(&s.udpChecksumErrors, 1)
					// Debug: Log first few errors to understand the pattern
					if errCount <= 3 {
						seqNum := uint32(buf[0])<<24 | uint32(buf[1])<<16 | uint32(buf[2])<<8 | uint32(buf[3])
						// 🔍 Find first byte that doesn't match expected value
						var firstMismatchIdx int = -1
						for i := 8; i < n; i++ {
							expected := byte((int(seqNum) + i) % 256)
							if buf[i] != expected {
								firstMismatchIdx = i
								break
							}
						}
						if firstMismatchIdx >= 0 {
							fmt.Printf("     [ECHO DEBUG] seq=%d len=%d FIRST MISMATCH at index %d\n", seqNum, n, firstMismatchIdx)
							fmt.Printf("                  buf[%d]=%02x, expected=%02x\n", firstMismatchIdx, buf[firstMismatchIdx], byte((int(seqNum)+firstMismatchIdx)%256))
						} else {
							fmt.Printf("     [ECHO DEBUG] seq=%d len=%d: PAYLOAD 100%% CORRECT but checksum mismatch!\n", seqNum, n)
							fmt.Printf("                  exp checksum=0x%02x, actual=0x%02x, diff=0x%02x\n",
								expectedChecksum, actualChecksum, expectedChecksum^actualChecksum)
						}
					}
				}
			}

			// Echo back the packet (for latency measurement and validation)
			// Response format: [0:8] = original header, [8:] = original payload
			sent, err := s.udpConn.WriteToUDP(buf[:n], remoteAddr)
			if err == nil {
				atomic.AddInt64(&s.udpPacketsSent, 1)
				atomic.AddInt64(&s.udpBytesSent, int64(sent))
				if valid {
					atomic.AddInt64(&s.udpValidPackets, 1)
				}
			}
		}
	}
}

// calcChecksum computes XOR checksum of data
func calcChecksum(data []byte) byte {
	var checksum byte
	for _, b := range data {
		checksum ^= b
	}
	return checksum
}

// startTCPEcho starts a TCP echo service for large packet integrity testing
func (s *GameStreamServices) startTCPEcho() error {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", s.tcpEchoPort))
	if err != nil {
		return err
	}
	s.tcpEchoLn = ln

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				select {
				case <-s.stop:
					return
				default:
					continue
				}
			}
			// Handle each connection in a goroutine
			go s.handleTCPEchoConnection(conn)
		}
	}()

	return nil
}

// handleTCPEchoConnection handles a single TCP echo connection
// Uses length-prefixed protocol: [4-byte length][payload]
func (s *GameStreamServices) handleTCPEchoConnection(conn net.Conn) {
	defer conn.Close()

	// Set buffer sizes
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		tcpConn.SetReadBuffer(4 * 1024 * 1024)
		tcpConn.SetWriteBuffer(4 * 1024 * 1024)
		tcpConn.SetNoDelay(true)
	}

	lenBuf := make([]byte, 4)
	dataBuf := make([]byte, 128*1024) // 128KB buffer

	for {
		select {
		case <-s.stop:
			return
		default:
		}

		// Read length prefix (4 bytes, big-endian)
		conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		_, err := readFull(conn, lenBuf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			return
		}

		// Decode length
		length := int(lenBuf[0])<<24 | int(lenBuf[1])<<16 | int(lenBuf[2])<<8 | int(lenBuf[3])
		if length <= 0 || length > len(dataBuf) {
			continue // Invalid length
		}

		// Read payload
		conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		_, err = readFull(conn, dataBuf[:length])
		if err != nil {
			continue
		}

		atomic.AddInt64(&s.tcpPacketsReceived, 1)
		atomic.AddInt64(&s.tcpBytesReceived, int64(4+length))

		// Echo back with length prefix
		conn.SetWriteDeadline(time.Now().Add(500 * time.Millisecond))
		_, err = conn.Write(lenBuf) // Length prefix
		if err != nil {
			continue
		}
		_, err = conn.Write(dataBuf[:length]) // Payload
		if err == nil {
			atomic.AddInt64(&s.tcpPacketsSent, 1)
			atomic.AddInt64(&s.tcpBytesSent, int64(4+length))
		}
	}
}

// readFull reads exactly len(buf) bytes from conn
func readFull(conn net.Conn, buf []byte) (int, error) {
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

func (s *GameStreamServices) Stop() {
	close(s.stop)
	if s.httpLn != nil {
		s.httpLn.Close()
	}
	if s.tcpEchoLn != nil {
		s.tcpEchoLn.Close()
	}
	if s.udpConn != nil {
		s.udpConn.Close()
	}
	s.wg.Wait()
}

func (s *GameStreamServices) HTTPAddr() string {
	return fmt.Sprintf("127.0.0.1:%d", s.httpPort)
}

func (s *GameStreamServices) TCPEchoAddr() string {
	return fmt.Sprintf("127.0.0.1:%d", s.tcpEchoPort)
}

func (s *GameStreamServices) UDPAddr() string {
	return fmt.Sprintf("127.0.0.1:%d", s.udpPort)
}

// UDPStats contains UDP statistics
type UDPStats struct {
	PacketsRx      int64
	BytesRx        int64
	PacketsTx      int64
	BytesTx        int64
	ValidPackets   int64
	ChecksumErrors int64
}

func (s *GameStreamServices) Stats() UDPStats {
	return UDPStats{
		PacketsRx:      atomic.LoadInt64(&s.udpPacketsReceived),
		BytesRx:        atomic.LoadInt64(&s.udpBytesReceived),
		PacketsTx:      atomic.LoadInt64(&s.udpPacketsSent),
		BytesTx:        atomic.LoadInt64(&s.udpBytesSent),
		ValidPackets:   atomic.LoadInt64(&s.udpValidPackets),
		ChecksumErrors: atomic.LoadInt64(&s.udpChecksumErrors),
	}
}

// ResetStats resets all statistics counters
func (s *GameStreamServices) ResetStats() {
	atomic.StoreInt64(&s.udpPacketsReceived, 0)
	atomic.StoreInt64(&s.udpBytesReceived, 0)
	atomic.StoreInt64(&s.udpPacketsSent, 0)
	atomic.StoreInt64(&s.udpBytesSent, 0)
	atomic.StoreInt64(&s.udpValidPackets, 0)
	atomic.StoreInt64(&s.udpChecksumErrors, 0)
}

func generateVideoFrame() []byte {
	frame := make([]byte, VideoFrameSize)
	ts := time.Now().UnixNano()
	for i := 0; i < 8; i++ {
		frame[i] = byte(ts >> (56 - i*8))
	}
	for i := 8; i < len(frame); i++ {
		frame[i] = byte(i % 256)
	}
	return frame
}
