// Package main provides local echo services for TUIC latency testing
// These services simulate Sunshine/Moonlight endpoints
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

// EchoServices provides TCP and UDP echo services for testing
type EchoServices struct {
	httpPort    int
	tcpPort     int
	udpPort     int
	httpLn      net.Listener
	tcpLn       net.Listener
	udpConn     *net.UDPConn
	wg          sync.WaitGroup
	stop        chan struct{}

	// Statistics
	udpPacketsRx int64
	udpPacketsTx int64
	udpBytesRx   int64
	udpBytesTx   int64
	tcpPacketsRx int64
	tcpPacketsTx int64
}

// Packet sizes for game streaming simulation
// Based on Moonlight/Sunshine actual packet sizes:
// - Video: Moonlight max packetSize = 1200 bytes (tests QUIC Datagram limit)
// - Audio: typically 400 bytes
// - QUIC Datagram MTU: ~1200-1252 bytes (packets > 1200 use reliable stream fallback)
const (
	VideoFrameSize  = 1200 // Moonlight max video packet size (tests QUIC Datagram MTU limit)
	AudioPacketSize = 400  // Typical audio packet size
	ControlCmdSize  = 128  // Control command size
)

// StartEchoServices starts HTTP, TCP, and UDP echo services
func StartEchoServices(httpPort, tcpPort, udpPort int) (*EchoServices, error) {
	s := &EchoServices{
		httpPort: httpPort,
		tcpPort:  tcpPort,
		udpPort:  udpPort,
		stop:     make(chan struct{}),
	}

	if err := s.startHTTP(); err != nil {
		return nil, fmt.Errorf("start http: %w", err)
	}

	if err := s.startTCP(); err != nil {
		s.Stop()
		return nil, fmt.Errorf("start tcp: %w", err)
	}

	if err := s.startUDP(); err != nil {
		s.Stop()
		return nil, fmt.Errorf("start udp: %w", err)
	}

	return s, nil
}

func (s *EchoServices) startHTTP() error {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", s.httpPort))
	if err != nil {
		return err
	}
	s.httpLn = ln

	mux := http.NewServeMux()

	// Control command endpoint - simulates gamepad input
	mux.HandleFunc("/control", func(w http.ResponseWriter, r *http.Request) {
		response := make([]byte, ControlCmdSize)
		ts := time.Now().UnixNano()
		for i := 0; i < 8 && i < len(response); i++ {
			response[i] = byte(ts >> (56 - i*8))
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(response)
	})

	// Echo endpoint - returns received data
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		data := make([]byte, 4096)
		n, _ := r.Body.Read(data)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(data[:n])
	})

	server := &http.Server{Handler: mux}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		server.Serve(ln)
	}()

	return nil
}

func (s *EchoServices) startTCP() error {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", s.tcpPort))
	if err != nil {
		return err
	}
	s.tcpLn = ln

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
			go s.handleTCPEcho(conn)
		}
	}()

	return nil
}

// handleTCPEcho handles TCP echo with length-prefixed protocol
// Format: [4-byte length][payload]
func (s *EchoServices) handleTCPEcho(conn net.Conn) {
	defer conn.Close()

	if tcpConn, ok := conn.(*net.TCPConn); ok {
		tcpConn.SetReadBuffer(4 * 1024 * 1024)
		tcpConn.SetWriteBuffer(4 * 1024 * 1024)
		tcpConn.SetNoDelay(true)
	}

	lenBuf := make([]byte, 4)
	dataBuf := make([]byte, 128*1024)

	for {
		select {
		case <-s.stop:
			return
		default:
		}

		conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		_, err := readFull(conn, lenBuf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			return
		}

		length := int(lenBuf[0])<<24 | int(lenBuf[1])<<16 | int(lenBuf[2])<<8 | int(lenBuf[3])
		if length <= 0 || length > len(dataBuf) {
			continue
		}

		conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		_, err = readFull(conn, dataBuf[:length])
		if err != nil {
			continue
		}

		atomic.AddInt64(&s.tcpPacketsRx, 1)

		conn.SetWriteDeadline(time.Now().Add(500 * time.Millisecond))
		_, err = conn.Write(lenBuf)
		if err != nil {
			continue
		}
		_, err = conn.Write(dataBuf[:length])
		if err == nil {
			atomic.AddInt64(&s.tcpPacketsTx, 1)
		}
	}
}

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

func (s *EchoServices) startUDP() error {
	addr := &net.UDPAddr{Port: s.udpPort}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return err
	}
	s.udpConn = conn

	// Large buffers for high throughput
	conn.SetReadBuffer(64 * 1024 * 1024)
	conn.SetWriteBuffer(64 * 1024 * 1024)

	// Start multiple workers for parallel processing
	numWorkers := runtime.NumCPU() * 4
	if numWorkers < 16 {
		numWorkers = 16
	}
	if numWorkers > 64 {
		numWorkers = 64
	}

	for i := 0; i < numWorkers; i++ {
		s.wg.Add(1)
		go s.udpWorker()
	}

	return nil
}

func (s *EchoServices) udpWorker() {
	defer s.wg.Done()

	buf := make([]byte, 65536)

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

			atomic.AddInt64(&s.udpPacketsRx, 1)
			atomic.AddInt64(&s.udpBytesRx, int64(n))

			// Echo back
			sent, err := s.udpConn.WriteToUDP(buf[:n], remoteAddr)
			if err == nil {
				atomic.AddInt64(&s.udpPacketsTx, 1)
				atomic.AddInt64(&s.udpBytesTx, int64(sent))
			}
		}
	}
}

// Stop stops all services
func (s *EchoServices) Stop() {
	close(s.stop)
	if s.httpLn != nil {
		s.httpLn.Close()
	}
	if s.tcpLn != nil {
		s.tcpLn.Close()
	}
	if s.udpConn != nil {
		s.udpConn.Close()
	}
	s.wg.Wait()
}

// HTTPAddr returns the HTTP service address
func (s *EchoServices) HTTPAddr() string {
	return fmt.Sprintf("127.0.0.1:%d", s.httpPort)
}

// TCPAddr returns the TCP echo service address
func (s *EchoServices) TCPAddr() string {
	return fmt.Sprintf("127.0.0.1:%d", s.tcpPort)
}

// UDPAddr returns the UDP echo service address
func (s *EchoServices) UDPAddr() string {
	return fmt.Sprintf("127.0.0.1:%d", s.udpPort)
}

// Stats returns current statistics
type EchoStats struct {
	UDPPacketsRx int64
	UDPPacketsTx int64
	UDPBytesRx   int64
	UDPBytesTx   int64
	TCPPacketsRx int64
	TCPPacketsTx int64
}

func (s *EchoServices) Stats() EchoStats {
	return EchoStats{
		UDPPacketsRx: atomic.LoadInt64(&s.udpPacketsRx),
		UDPPacketsTx: atomic.LoadInt64(&s.udpPacketsTx),
		UDPBytesRx:   atomic.LoadInt64(&s.udpBytesRx),
		UDPBytesTx:   atomic.LoadInt64(&s.udpBytesTx),
		TCPPacketsRx: atomic.LoadInt64(&s.tcpPacketsRx),
		TCPPacketsTx: atomic.LoadInt64(&s.tcpPacketsTx),
	}
}

// ResetStats resets all statistics
func (s *EchoServices) ResetStats() {
	atomic.StoreInt64(&s.udpPacketsRx, 0)
	atomic.StoreInt64(&s.udpPacketsTx, 0)
	atomic.StoreInt64(&s.udpBytesRx, 0)
	atomic.StoreInt64(&s.udpBytesTx, 0)
	atomic.StoreInt64(&s.tcpPacketsRx, 0)
	atomic.StoreInt64(&s.tcpPacketsTx, 0)
}
