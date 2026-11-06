// Package buffer provides zero-copy buffer management for high-performance data transfer.
// It implements StreamBuffer with pre-allocated header space to avoid memory copies
// when prepending protocol headers.
package buffer

import (
	"io"
	"net"
)

// HeaderReserve is the space reserved at the beginning of each buffer for headers.
// This allows prepending protocol headers without copying the data payload.
//
// It must fit the largest header, the UDP data header (287 bytes):
// - Protocol version + type: 2 bytes
// - ConnID: 20 bytes
// - TargetAddr length: 2 bytes
// - TargetAddr: up to 263 bytes (255-byte domain, "[]" from JoinHostPort, ":65535")
const HeaderReserve = 288

// StreamBuffer is a buffer with pre-allocated header space for zero-copy operations.
// It allows reading data into the payload section and prepending headers without copying.
//
// Buffer layout:
// ┌────────────────────────────────────────────────────┐
// │  Header (HeaderReserve) │  Data Payload            │
// ├─────────────────────────┼──────────────────────────┤
// │  [unused][Proto][ID]    │  [User Data...]          │
// │          ↑ headerEnd    │  ← dataLen →             │
// └────────────────────────────────────────────────────┘
type StreamBuffer struct {
	buf       []byte      // Full buffer: [HeaderReserve bytes header space][data payload]
	headerEnd int         // Where headers end (starts at HeaderReserve, decreases as headers prepend)
	dataLen   int         // Length of data payload
	pool      *BufferPool // Pool to return to (nil if not from pool)
}

// NewStreamBuffer creates a new StreamBuffer with the specified total capacity.
// The capacity includes both header reserve and data payload space.
func NewStreamBuffer(capacity int) *StreamBuffer {
	if capacity < HeaderReserve {
		capacity = HeaderReserve + 1024 // Minimum useful size
	}
	return &StreamBuffer{
		buf:       make([]byte, capacity),
		headerEnd: HeaderReserve,
		dataLen:   0,
		pool:      nil,
	}
}

// DataSlice returns the slice where data should be read into.
// Use this with syscalls like ReadFromUDP or Read.
// After reading, call SetDataLen to set the actual data length.
func (sb *StreamBuffer) DataSlice() []byte {
	return sb.buf[HeaderReserve:]
}

// SetDataLen sets the length of data that was read into DataSlice.
// Must be called after reading data to properly track the data boundaries.
func (sb *StreamBuffer) SetDataLen(n int) {
	if n < 0 {
		n = 0
	}
	maxLen := len(sb.buf) - HeaderReserve
	if n > maxLen {
		n = maxLen
	}
	sb.dataLen = n
}

// PrependHeader prepends a header to the buffer without copying the data.
// Headers are added from right to left in the reserved header space.
// Returns false if there's not enough header space.
func (sb *StreamBuffer) PrependHeader(header []byte) bool {
	headerLen := len(header)
	if headerLen > sb.headerEnd {
		return false // Not enough header space
	}
	sb.headerEnd -= headerLen
	copy(sb.buf[sb.headerEnd:], header)
	return true
}

// HeaderSlice returns the current header portion of the buffer.
// This includes all prepended headers.
func (sb *StreamBuffer) HeaderSlice() []byte {
	return sb.buf[sb.headerEnd:HeaderReserve]
}

// HeaderLen returns the current total header length.
func (sb *StreamBuffer) HeaderLen() int {
	return HeaderReserve - sb.headerEnd
}

// DataPortion returns the data payload portion of the buffer.
func (sb *StreamBuffer) DataPortion() []byte {
	return sb.buf[HeaderReserve : HeaderReserve+sb.dataLen]
}

// DataLen returns the length of the data payload.
func (sb *StreamBuffer) DataLen() int {
	return sb.dataLen
}

// Full returns the complete message (headers + data) as a contiguous slice.
// This is useful when the receiver needs a single slice.
func (sb *StreamBuffer) Full() []byte {
	return sb.buf[sb.headerEnd : HeaderReserve+sb.dataLen]
}

// TotalLen returns the total length of headers + data.
func (sb *StreamBuffer) TotalLen() int {
	return sb.HeaderLen() + sb.dataLen
}

// Buffers returns net.Buffers for scatter-gather I/O (writev).
// This allows writing header and data separately without merging them.
func (sb *StreamBuffer) Buffers() net.Buffers {
	return net.Buffers{sb.HeaderSlice(), sb.DataPortion()}
}

// WriteTo implements io.WriterTo interface.
// It uses writev (scatter-gather I/O) when possible for efficiency.
func (sb *StreamBuffer) WriteTo(w io.Writer) (int64, error) {
	bufs := sb.Buffers()
	return bufs.WriteTo(w)
}

// Reset resets the buffer for reuse.
// Call this before returning to pool or reusing the buffer.
func (sb *StreamBuffer) Reset() {
	sb.headerEnd = HeaderReserve
	sb.dataLen = 0
}

// Release returns the buffer to its pool if it came from one.
// After calling Release, the StreamBuffer should not be used.
func (sb *StreamBuffer) Release() {
	if sb.pool != nil {
		sb.Reset()
		sb.pool.Put(sb)
	}
}

// Capacity returns the total buffer capacity.
func (sb *StreamBuffer) Capacity() int {
	return len(sb.buf)
}

// DataCapacity returns the capacity available for data (excluding header reserve).
func (sb *StreamBuffer) DataCapacity() int {
	return len(sb.buf) - HeaderReserve
}

// AvailableHeaderSpace returns the remaining header space available for prepending.
func (sb *StreamBuffer) AvailableHeaderSpace() int {
	return sb.headerEnd
}

// SetData copies data into the buffer's data section.
// Use DataSlice() + SetDataLen() for zero-copy when possible.
func (sb *StreamBuffer) SetData(data []byte) {
	n := copy(sb.buf[HeaderReserve:], data)
	sb.dataLen = n
}

// Clone creates a copy of the StreamBuffer.
// The clone is independent and not associated with any pool.
func (sb *StreamBuffer) Clone() *StreamBuffer {
	clone := NewStreamBuffer(len(sb.buf))
	copy(clone.buf, sb.buf)
	clone.headerEnd = sb.headerEnd
	clone.dataLen = sb.dataLen
	return clone
}
