package buffer

import (
	"sync"
)

// Buffer size constants for different use cases
const (
	// SmallBufferSize for control messages (512 bytes + header)
	SmallBufferSize = 512 + HeaderReserve

	// MediumBufferSize for typical UDP packets (2KB + header)
	MediumBufferSize = 2048 + HeaderReserve

	// LargeBufferSize for max UDP datagrams (64KB + header)
	LargeBufferSize = 65536 + HeaderReserve
)

// BufferPool manages a pool of StreamBuffers of a specific size.
// It uses sync.Pool for efficient memory reuse and reduced GC pressure.
type BufferPool struct { //nolint:revive // keep the exported name; renaming would change the package API
	pool     sync.Pool
	capacity int
}

// NewBufferPool creates a new BufferPool for buffers of the specified capacity.
func NewBufferPool(capacity int) *BufferPool {
	bp := &BufferPool{
		capacity: capacity,
	}
	bp.pool = sync.Pool{
		New: func() interface{} {
			sb := NewStreamBuffer(capacity)
			sb.pool = bp
			return sb
		},
	}
	return bp
}

// Get retrieves a StreamBuffer from the pool.
// The buffer is reset and ready for use.
func (bp *BufferPool) Get() *StreamBuffer {
	sb := bp.pool.Get().(*StreamBuffer)
	sb.Reset()
	sb.pool = bp // Ensure pool reference is set
	return sb
}

// Put returns a StreamBuffer to the pool.
// The buffer will be reset automatically when retrieved again.
func (bp *BufferPool) Put(sb *StreamBuffer) {
	if sb == nil {
		return
	}
	// Don't put back buffers from different pools
	if sb.pool != bp {
		return
	}
	bp.pool.Put(sb)
}

// Capacity returns the capacity of buffers in this pool.
func (bp *BufferPool) Capacity() int {
	return bp.capacity
}

// Global buffer pools for different sizes
var (
	// smallPool for control messages
	smallPool = NewBufferPool(SmallBufferSize)

	// mediumPool for typical UDP packets
	mediumPool = NewBufferPool(MediumBufferSize)

	// largePool for max UDP datagrams
	largePool = NewBufferPool(LargeBufferSize)
)

// AcquireSmall gets a small buffer (512 bytes data capacity) from the pool.
// Suitable for control messages.
func AcquireSmall() *StreamBuffer {
	return smallPool.Get()
}

// AcquireMedium gets a medium buffer (2KB data capacity) from the pool.
// Suitable for typical UDP packets.
func AcquireMedium() *StreamBuffer {
	return mediumPool.Get()
}

// AcquireLarge gets a large buffer (64KB data capacity) from the pool.
// Suitable for max UDP datagrams.
func AcquireLarge() *StreamBuffer {
	return largePool.Get()
}

// Acquire gets a buffer with at least the specified data capacity.
// It selects the smallest pool that can accommodate the size.
func Acquire(dataSize int) *StreamBuffer {
	totalSize := dataSize + HeaderReserve
	switch {
	case totalSize <= SmallBufferSize:
		return smallPool.Get()
	case totalSize <= MediumBufferSize:
		return mediumPool.Get()
	case totalSize <= LargeBufferSize:
		return largePool.Get()
	default:
		// For sizes larger than our pools, create a new buffer
		sb := NewStreamBuffer(totalSize)
		return sb
	}
}

// Release returns a buffer to its appropriate pool.
// This is a convenience function that delegates to StreamBuffer.Release().
func Release(sb *StreamBuffer) {
	if sb != nil {
		sb.Release()
	}
}
