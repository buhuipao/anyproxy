// Package connection provides connection management utilities for the anyproxy system.
package connection

import (
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// NoCopyWriter is an interface for connections that support zero-copy writes
// Caller guarantees that data won't be modified after the call
type NoCopyWriter interface {
	WriteNoCopy(b []byte) (int, error)
}

// Buffer size constants for BufferedConn
// 🔧 GAMING OPTIMIZATION: Tuned buffer sizes for low latency in real-time scenarios
// Target: max queuing latency < 200ms at 100Mbps (200ms * 100Mbps / 1200B ≈ 2000 packets)
const (
	// TCPBufferSize is the buffer size for TCP connections
	// Blocks when full (backpressure), ensures reliable delivery
	// Larger buffer for TCP to handle bursty traffic while maintaining reliability
	TCPBufferSize = 5000

	// UDPBufferSize is the buffer size for UDP connections.
	// Uses drop-oldest strategy when full to prevent bufferbloat.
	// Sized for multi-tenant use: 2000 * 1400B ≈ 2.8MB per UDP target.
	// At 10000 a Moonlight session (3–4 UDP targets) reserved ~50MB of
	// per-tenant queue, which scales poorly on a shared gateway.
	UDPBufferSize = 2000
)

// closeControl is shared between a BufferedConn pair to ensure
// the closeChan is only closed once, even if both ends call Close()
type closeControl struct {
	closeChan chan struct{}
	closeOnce sync.Once
	closed    int32
}

// BufferedConn implements net.Conn with channel-based buffering
// This replaces net.Pipe() to provide non-blocking writes and avoid synchronous blocking
// 🔧 FIX: UDP uses drop-oldest strategy to prevent bufferbloat; TCP blocks for reliability
type BufferedConn struct {
	outChan chan []byte // Write() sends here
	inChan  chan []byte // Read() receives from here

	readBuf    []byte // partial read buffer
	readOffset int
	readMu     sync.Mutex // protects readBuf and readOffset

	ctrl *closeControl // shared close control between pair

	localAddr  net.Addr
	remoteAddr net.Addr

	isUDP bool // Protocol type (for future use, both now block)
}

// bufferedAddr implements net.Addr for BufferedConn
type bufferedAddr struct {
	network string
	address string
}

func (a *bufferedAddr) Network() string { return a.network }
func (a *bufferedAddr) String() string  { return a.address }

// NewBufferedConnPair creates a bidirectional buffered connection pair
// connA.Write() -> connB.Read()
// connB.Write() -> connA.Read()
func NewBufferedConnPair(bufSize int, isUDP bool) (*BufferedConn, *BufferedConn) {
	// Channel A->B: connA.Write() sends to connB.Read()
	chanAtoB := make(chan []byte, bufSize)
	// Channel B->A: connB.Write() sends to connA.Read()
	chanBtoA := make(chan []byte, bufSize)

	// Shared close control - ensures closeChan is only closed once
	ctrl := &closeControl{
		closeChan: make(chan struct{}),
	}

	localAddr := &bufferedAddr{network: "buffered", address: "local"}
	remoteAddr := &bufferedAddr{network: "buffered", address: "remote"}

	connA := &BufferedConn{
		outChan:    chanAtoB,
		inChan:     chanBtoA,
		ctrl:       ctrl,
		isUDP:      isUDP,
		localAddr:  localAddr,
		remoteAddr: remoteAddr,
	}

	connB := &BufferedConn{
		outChan:    chanBtoA,
		inChan:     chanAtoB,
		ctrl:       ctrl, // Same control - shared closeOnce
		isUDP:      isUDP,
		localAddr:  remoteAddr, // Swap for peer
		remoteAddr: localAddr,
	}

	return connA, connB
}

// Write implements net.Conn.Write
// For UDP: non-blocking, drops data if buffer is full (acceptable for real-time streaming)
// For TCP: blocks until data is sent (backpressure)
func (c *BufferedConn) Write(b []byte) (int, error) {
	if atomic.LoadInt32(&c.ctrl.closed) == 1 {
		return 0, io.ErrClosedPipe
	}

	if len(b) == 0 {
		return 0, nil
	}

	// Copy data to avoid race conditions
	data := make([]byte, len(b))
	copy(data, b)

	return c.writeInternal(data)
}

// WriteNoCopy writes data without copying - caller guarantees data won't be modified
// Use this when caller has already allocated independent memory (e.g., after copy)
// 🔧 PERFORMANCE: Avoids extra copy when caller guarantees data independence
func (c *BufferedConn) WriteNoCopy(b []byte) (int, error) {
	if atomic.LoadInt32(&c.ctrl.closed) == 1 {
		return 0, io.ErrClosedPipe
	}

	if len(b) == 0 {
		return 0, nil
	}

	return c.writeInternal(b)
}

// writeInternal is the common write implementation
// 🔧 FIX: UDP uses drop-oldest strategy to prevent bufferbloat (7+ second delays)
// TCP blocks until sent for reliable delivery
func (c *BufferedConn) writeInternal(data []byte) (int, error) {
	if c.isUDP {
		// UDP: non-blocking with drop-oldest strategy to prevent bufferbloat
		// For real-time streaming, it's better to drop old packets than accumulate latency
		select {
		case c.outChan <- data:
			return len(data), nil
		case <-c.ctrl.closeChan:
			return 0, io.ErrClosedPipe
		default:
			// Buffer full - drop oldest packet and retry
			select {
			case <-c.outChan: // Discard oldest packet
				// Note: In production, consider adding metrics here
				// atomic.AddInt64(&droppedPackets, 1)
			default:
				// Channel was drained by reader, try again
			}
			// Retry sending
			select {
			case c.outChan <- data:
				return len(data), nil
			case <-c.ctrl.closeChan:
				return 0, io.ErrClosedPipe
			default:
				// Still full after drop, silently discard (UDP semantics)
				return len(data), nil
			}
		}
	}

	// TCP: block until sent (reliable delivery with backpressure)
	select {
	case c.outChan <- data:
		return len(data), nil
	case <-c.ctrl.closeChan:
		return 0, io.ErrClosedPipe
	}
}

// Read implements net.Conn.Read
// Blocks until data is available or connection is closed.
// After Close, data already buffered is still returned, then io.EOF.
func (c *BufferedConn) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}

	c.readMu.Lock()
	defer c.readMu.Unlock()

	// Handle partial read from previous call
	if c.readOffset < len(c.readBuf) {
		n := copy(b, c.readBuf[c.readOffset:])
		c.readOffset += n
		if c.readOffset >= len(c.readBuf) {
			c.readBuf = nil
			c.readOffset = 0
		}
		return n, nil
	}

	// Wait for new data
	var data []byte
	select {
	case data = <-c.inChan:
	case <-c.ctrl.closeChan:
		// Closed: drain data written before Close, then report EOF
		select {
		case data = <-c.inChan:
		default:
			return 0, io.EOF
		}
	}

	n := copy(b, data)
	if n < len(data) {
		// Save remaining data for next Read() call
		c.readBuf = data
		c.readOffset = n
	}
	return n, nil
}

// Close implements net.Conn.Close
// Safe to call from both ends - only closes once due to shared closeOnce
func (c *BufferedConn) Close() error {
	c.ctrl.closeOnce.Do(func() {
		atomic.StoreInt32(&c.ctrl.closed, 1)
		close(c.ctrl.closeChan)
	})
	return nil
}

// SetDeadline implements net.Conn.SetDeadline
// No-op for buffered conn as it uses channels
func (c *BufferedConn) SetDeadline(_ time.Time) error {
	return nil
}

// SetReadDeadline implements net.Conn.SetReadDeadline
// No-op for buffered conn as it uses channels
func (c *BufferedConn) SetReadDeadline(_ time.Time) error {
	return nil
}

// SetWriteDeadline implements net.Conn.SetWriteDeadline
// No-op for buffered conn as it uses channels
func (c *BufferedConn) SetWriteDeadline(_ time.Time) error {
	return nil
}

// LocalAddr implements net.Conn.LocalAddr
func (c *BufferedConn) LocalAddr() net.Addr {
	return c.localAddr
}

// RemoteAddr implements net.Conn.RemoteAddr
func (c *BufferedConn) RemoteAddr() net.Addr {
	return c.remoteAddr
}

// IsClosed returns whether the connection is closed
func (c *BufferedConn) IsClosed() bool {
	return atomic.LoadInt32(&c.ctrl.closed) == 1
}

// Ensure BufferedConn implements net.Conn
var _ net.Conn = (*BufferedConn)(nil)
