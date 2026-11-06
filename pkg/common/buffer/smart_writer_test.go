package buffer

import (
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/buhuipao/anyproxy/pkg/transport"
)

// mockStreamBufferWriter implements transport.StreamBufferWriter.
type mockStreamBufferWriter struct {
	streamBufferWritten int
	err                 error
}

func (m *mockStreamBufferWriter) WriteStreamBuffer(sb transport.StreamBuffer) error {
	if m.err != nil {
		return m.err
	}
	m.streamBufferWritten++
	sb.Release()
	return nil
}

// mockDatagramWriter implements transport.DatagramWriter.
// SmartWriter calls SendDatagram from its own goroutine, so access is locked.
type mockDatagramWriter struct {
	mu            sync.Mutex
	datagramsSent [][]byte
	datagramErr   error
}

func (m *mockDatagramWriter) SendDatagram(data []byte) error {
	if m.datagramErr != nil {
		return m.datagramErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.datagramsSent = append(m.datagramsSent, append([]byte{}, data...))
	return nil
}

// sent returns a copy of the datagrams sent so far.
func (m *mockDatagramWriter) sent() [][]byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([][]byte(nil), m.datagramsSent...)
}

// mockFullWriter implements both StreamBufferWriter and DatagramWriter.
type mockFullWriter struct {
	mockStreamBufferWriter
	mockDatagramWriter
}

func (m *mockFullWriter) SendDatagram(data []byte) error {
	return m.mockDatagramWriter.SendDatagram(data)
}

// newTestStreamBuffer creates a StreamBuffer with the given data for testing.
func newTestStreamBuffer(data []byte) *StreamBuffer {
	sb := NewStreamBuffer(HeaderReserve + len(data))
	copy(sb.DataSlice(), data)
	sb.SetDataLen(len(data))
	return sb
}

func TestNewSmartWriter_CachesInterfaces(t *testing.T) {
	t.Run("with_stream_and_datagram", func(t *testing.T) {
		writer := &mockFullWriter{}
		sw := NewSmartWriter(writer, "test-owner")
		if sw.streamWriter == nil {
			t.Error("streamWriter should be cached")
		}
		if sw.datagramWriter == nil {
			t.Error("datagramWriter should be cached")
		}
	})

	t.Run("with_stream_only", func(t *testing.T) {
		writer := &mockStreamBufferWriter{}
		sw := NewSmartWriter(writer, "test-owner")
		if sw.streamWriter == nil {
			t.Error("streamWriter should be cached")
		}
		if sw.datagramWriter != nil {
			t.Error("datagramWriter should be nil for non-QUIC")
		}
	})

	t.Run("with_neither", func(t *testing.T) {
		type empty struct{}
		sw := NewSmartWriter(&empty{}, "test-owner")
		if sw.streamWriter != nil {
			t.Error("streamWriter should be nil")
		}
		if sw.datagramWriter != nil {
			t.Error("datagramWriter should be nil")
		}
	})
}

func TestSmartWriter_WriteStreamBuffer(t *testing.T) {
	t.Run("delegates_to_stream_writer", func(t *testing.T) {
		writer := &mockStreamBufferWriter{}
		sw := NewSmartWriter(writer, "test")
		sb := newTestStreamBuffer([]byte("testdata"))
		if err := sw.WriteStreamBuffer(sb); err != nil {
			t.Fatalf("WriteStreamBuffer failed: %v", err)
		}
		if writer.streamBufferWritten != 1 {
			t.Errorf("expected 1 write, got %d", writer.streamBufferWritten)
		}
	})

	t.Run("propagates_error", func(t *testing.T) {
		writer := &mockStreamBufferWriter{err: errors.New("write failed")}
		sw := NewSmartWriter(writer, "test")
		sb := newTestStreamBuffer([]byte("testdata"))
		if err := sw.WriteStreamBuffer(sb); err == nil {
			t.Error("expected error from WriteStreamBuffer")
		}
	})

	t.Run("returns_error_when_no_stream_writer", func(t *testing.T) {
		type empty struct{}
		sw := NewSmartWriter(&empty{}, "test")
		sb := newTestStreamBuffer([]byte("testdata"))
		err := sw.WriteStreamBuffer(sb)
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Errorf("expected io.ErrClosedPipe, got %v", err)
		}
	})
}

func TestSmartWriter_SendDatagram(t *testing.T) {
	t.Run("queues_when_supported", func(t *testing.T) {
		writer := &mockFullWriter{}
		sw := NewSmartWriter(writer, "test")
		defer sw.Close()
		sent, err := sw.SendDatagram([]byte("udp-data"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !sent {
			t.Error("expected sent=true")
		}
		// Wait briefly for the background goroutine to deliver
		time.Sleep(10 * time.Millisecond)
		got := writer.mockDatagramWriter.sent()
		if len(got) != 1 {
			t.Errorf("expected 1 datagram sent, got %d", len(got))
		}
		if len(got) > 0 && string(got[0]) != "udp-data" {
			t.Error("datagram data mismatch")
		}
	})

	t.Run("returns_false_when_not_supported", func(t *testing.T) {
		writer := &mockStreamBufferWriter{}
		sw := NewSmartWriter(writer, "test")
		defer sw.Close()
		sent, err := sw.SendDatagram([]byte("udp-data"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if sent {
			t.Error("expected sent=false when datagram not supported")
		}
	})

	t.Run("returns_false_when_queue_full", func(t *testing.T) {
		// Build a SmartWriter WITHOUT starting the loop, so nothing drains
		sw := &SmartWriter{
			stopCh:     make(chan struct{}),
			datagramCh: make(chan []byte, datagramQueueSize),
		}
		defer sw.Close()

		// Fill the queue completely
		for i := 0; i < datagramQueueSize; i++ {
			sw.datagramCh <- []byte("fill")
		}
		// Next send should return false (queue full)
		sent, _ := sw.SendDatagram([]byte("overflow"))
		if sent {
			t.Error("expected sent=false when queue is full")
		}
	})

	t.Run("close_stops_goroutine", func(t *testing.T) {
		writer := &mockFullWriter{}
		sw := NewSmartWriter(writer, "test")
		sw.Close()
		time.Sleep(5 * time.Millisecond)
		// After close, SendDatagram still returns false gracefully
		sent, _ := sw.SendDatagram([]byte("after-close"))
		if sent {
			t.Error("expected sent=false after close (channel likely full or goroutine stopped)")
		}
	})
}

// blockingDatagramWriter blocks until blockCh is closed, simulating a slow QUIC queue.
type blockingDatagramWriter struct {
	blockCh chan struct{}
}

func (b *blockingDatagramWriter) SendDatagram(data []byte) error {
	<-b.blockCh
	return nil
}

func BenchmarkSmartWriter_WriteStreamBuffer(b *testing.B) {
	writer := &mockStreamBufferWriter{}
	sw := NewSmartWriter(writer, "bench")
	defer sw.Close()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sb := newTestStreamBuffer([]byte("benchmark data"))
		sw.WriteStreamBuffer(sb)
	}
}

func BenchmarkSmartWriter_SendDatagram(b *testing.B) {
	writer := &mockFullWriter{}
	sw := NewSmartWriter(writer, "bench")
	defer sw.Close()
	data := []byte("benchmark udp")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sw.SendDatagram(data)
	}
}
