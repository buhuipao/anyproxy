// Package buffer provides zero-copy buffer management and smart writing utilities.
package buffer

import (
	"io"
	"sync/atomic"
	"time"

	"github.com/buhuipao/anyproxy/pkg/transport"
)

// datagramQueueSize is the capacity of the non-blocking datagram send queue.
// Sized to absorb short bursts without dropping. When full, callers fall back
// to the reliable stream — this is the intended backpressure signal.
const datagramQueueSize = 256

// mtuWarmupDuration is how long after SmartWriter creation we allow large
// datagrams even before a confirmed success. QUIC path MTU discovery on
// high-MTU paths (loopback, LAN) completes well within this window.
const mtuWarmupDuration = 500 * time.Millisecond

// SmartWriter provides unified write operations for transport connections.
// Caches interface assertions at construction for performance.
//
// The datagram path uses a dedicated goroutine so that the QUIC library's
// potentially-blocking SendDatagram (blocks when the internal 32-item queue
// is full) never stalls the caller's handleConnection goroutine.
//
// MTU awareness: datagramLoop tracks whether any QUIC datagram succeeds,
// then sets mtuReady to allow callers to send game-streaming-sized datagrams
// (1200B video + 40B header = 1240B) via the low-latency datagram path.
// After a warmup period (500ms), large datagrams are always attempted since
// QUIC path MTU discovery should be complete by then.
type SmartWriter struct {
	streamWriter   transport.StreamBufferWriter
	datagramWriter transport.DatagramWriter
	datagramCh     chan []byte  // non-blocking send queue; nil when datagrams unsupported
	stopCh         chan struct{}
	mtuReady       int32     // atomic: 1 once any datagram succeeds
	createdAt      time.Time // for warmup period
}

// NewSmartWriter creates a SmartWriter that caches transport interface assertions.
// The owner ID argument is accepted for backward compatibility but unused.
func NewSmartWriter(conn interface{}, _ string) *SmartWriter {
	sw := &SmartWriter{
		stopCh:    make(chan struct{}),
		createdAt: time.Now(),
	}
	sw.streamWriter, _ = conn.(transport.StreamBufferWriter)
	sw.datagramWriter, _ = conn.(transport.DatagramWriter)
	if sw.datagramWriter != nil {
		sw.datagramCh = make(chan []byte, datagramQueueSize)
		go sw.datagramLoop()
	}
	return sw
}

// Close stops the background datagram goroutine. Safe to call multiple times.
func (sw *SmartWriter) Close() {
	select {
	case <-sw.stopCh:
	default:
		close(sw.stopCh)
	}
}

// MTUReady returns true when large datagrams (>1200B) can be attempted safely.
// This is true when either:
//   - Any datagram has been sent successfully (QUIC connection supports datagrams), or
//   - The warmup period (500ms) has elapsed (path MTU discovery should be complete).
func (sw *SmartWriter) MTUReady() bool {
	if atomic.LoadInt32(&sw.mtuReady) == 1 {
		return true
	}
	return time.Since(sw.createdAt) > mtuWarmupDuration
}

// datagramLoop drains the datagram channel and calls the (potentially blocking)
// QUIC SendDatagram in its own goroutine so callers never block.
// Tracks successful sends to confirm datagram support.
func (sw *SmartWriter) datagramLoop() {
	for {
		select {
		case <-sw.stopCh:
			return
		case data, ok := <-sw.datagramCh:
			if !ok {
				return
			}
			err := sw.datagramWriter.SendDatagram(data)
			if err == nil {
				atomic.StoreInt32(&sw.mtuReady, 1)
			}
		}
	}
}

// WriteStreamBuffer writes using StreamBuffer (writev).
// All production transports implement StreamBufferWriter.
// Returns io.ErrClosedPipe if the connection lacks StreamBufferWriter support.
func (sw *SmartWriter) WriteStreamBuffer(sb *StreamBuffer) error {
	if sw.streamWriter == nil {
		sb.Release()
		return io.ErrClosedPipe
	}
	return sw.streamWriter.WriteStreamBuffer(sb)
}

// SupportsDatagram reports whether the transport can send datagrams (QUIC
// can; WebSocket and gRPC cannot). SendDatagram returns false both for a
// transport without datagrams and for a full queue; this tells them apart.
func (sw *SmartWriter) SupportsDatagram() bool {
	return sw.datagramCh != nil
}

// SendDatagram attempts a non-blocking send via QUIC Datagram (unreliable, low-latency).
// Returns (false, nil) when datagrams are unsupported, closed, or the send queue is full,
// signaling the caller to fall back to the reliable stream.
func (sw *SmartWriter) SendDatagram(data []byte) (sent bool, err error) {
	if sw.datagramCh == nil {
		return false, nil
	}
	// Check if closed first (avoids queueing after shutdown)
	select {
	case <-sw.stopCh:
		return false, nil
	default:
	}
	dataCopy := make([]byte, len(data))
	copy(dataCopy, data)
	select {
	case sw.datagramCh <- dataCopy:
		return true, nil
	default:
		// Queue full — backpressure signal. Caller should use stream.
		return false, nil
	}
}
