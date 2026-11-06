package quic

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/quic-go"

	"github.com/buhuipao/anyproxy/pkg/logger"
	"github.com/buhuipao/anyproxy/pkg/transport"
)

// 🆕 Write request type
type writeRequest struct {
	data      []byte
	errChan   chan error
	buffers   net.Buffers            // For writev support
	streamBuf transport.StreamBuffer // For StreamBuffer ownership transfer
}

// quicConnection implements transport.Connection for QUIC streams
type quicConnection struct {
	stream        quic.Stream     // sagernet/quic-go: Stream is interface type
	conn          quic.Connection // sagernet/quic-go: Connection is interface type
	clientID      string
	groupID       string
	groupPassword string // Client password for group credential management
	// 🆕 Remove mutex, use async writes instead
	writeChan chan *writeRequest // Async write queue; never closed, writers stop on ctx
	closed    atomic.Bool
	ctx       context.Context
	cancel    context.CancelFunc
	readChan  chan []byte
	errorChan chan error // Only used by the auth handshake (tempConn)
	closeOnce sync.Once
	isClient  bool          // Whether this is a client connection
	done      chan struct{} // Closed when writeLoop has flushed and exited
}

var _ transport.Connection = (*quicConnection)(nil)

// newQUICConnection creates a new QUIC connection wrapper
func newQUICConnection(stream quic.Stream, conn quic.Connection, clientID, groupID, groupPassword string) *quicConnection {
	ctx, cancel := context.WithCancel(context.Background())

	c := &quicConnection{
		stream:        stream,
		conn:          conn,
		clientID:      clientID,
		groupID:       groupID,
		groupPassword: groupPassword,
		writeChan:     make(chan *writeRequest, transport.WriteChannelSize),
		ctx:           ctx,
		cancel:        cancel,
		readChan:      make(chan []byte, transport.ReadChannelSize),
		done:          make(chan struct{}),
		isClient:      true, // Default to client
	}

	// 🆕 Start read/write goroutines
	go c.receiveLoop()
	go c.writeLoop()
	return c
}

// newQUICServerConnection creates a new server-side QUIC connection
func newQUICServerConnection(stream quic.Stream, conn quic.Connection, clientID, groupID, groupPassword string) *quicConnection {
	ctx, cancel := context.WithCancel(context.Background())

	c := &quicConnection{
		stream:        stream,
		conn:          conn,
		clientID:      clientID,
		groupID:       groupID,
		groupPassword: groupPassword,
		writeChan:     make(chan *writeRequest, transport.WriteChannelSize),
		ctx:           ctx,
		cancel:        cancel,
		readChan:      make(chan []byte, transport.ReadChannelSize),
		done:          make(chan struct{}),
		isClient:      false, // Server connection
	}

	// 🆕 Start read/write goroutines
	go c.receiveLoop()
	go c.writeLoop()
	return c
}

// 🆕 Async write goroutine, avoiding lock contention
func (c *quicConnection) writeLoop() {
	defer func() {
		// Fix: Ensure all pending requests are cleared to avoid goroutine leaks
		// Process requests already in the queue first
		for {
			select {
			case req := <-c.writeChan:
				if req != nil && req.errChan != nil {
					select {
					case req.errChan <- fmt.Errorf("connection closed"):
						// Successfully sent error
					default:
						// If no one is waiting, skip directly
					}
					close(req.errChan)
				}
			default:
				// Queue is empty, exit
				close(c.done)
				return
			}
		}
	}()

	for {
		select {
		case <-c.ctx.Done():
			// Flush what is already queued (e.g. a final error message)
			// before Close closes the stream, like the websocket drain.
			if len(c.writeChan) == 0 {
				return
			}
		case req := <-c.writeChan:
			var err error
			if req.buffers != nil {
				// 🆕 Use writev for scatter-gather I/O
				_, err = req.buffers.WriteTo(c.stream)
				if req.streamBuf != nil {
					req.streamBuf.Release()
				}
			} else {
				err = c.writeDataDirect(req.data)
			}

			if err != nil && isQUICError(err) {
				c.closed.Store(true)
			}

			if req.errChan != nil {
				req.errChan <- err
				close(req.errChan)
			}
		}
	}
}

// WriteMessage implements transport.Connection
func (c *quicConnection) WriteMessage(data []byte) error {
	return c.writeDataAsync(data)
}

// WriteMessageNoCopy implements transport.NoCopyWriter
// 🔧 PERFORMANCE: Caller guarantees data won't be modified, skip copy
func (c *quicConnection) WriteMessageNoCopy(data []byte) error {
	return c.writeDataAsyncNoCopy(data)
}

// WriteStreamBuffer implements transport.StreamBufferWriter
// 🔧 ZERO-COPY: Uses writev (scatter-gather I/O) for maximum performance
// Takes ownership of the StreamBuffer and releases it after writing.
func (c *quicConnection) WriteStreamBuffer(sb transport.StreamBuffer) error {
	if c.closed.Load() {
		sb.Release()
		return fmt.Errorf("connection closed")
	}

	// Build length prefix for the QUIC stream protocol
	totalLen := sb.TotalLen()
	lenBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(lenBuf, uint32(totalLen)) //nolint:gosec // pooled stream buffers are far below 4 GiB

	// Create net.Buffers for writev: [length][header][data]
	bufs := net.Buffers{lenBuf, sb.HeaderSlice(), sb.DataPortion()}

	req := &writeRequest{
		buffers:   bufs,
		streamBuf: sb,
		errChan:   nil, // Fire-and-forget
	}

	// 🔧 FIX: Block until channel has space or context canceled
	// Don't timeout and drop data - that causes unnecessary packet loss
	select {
	case c.writeChan <- req:
		return nil
	case <-c.ctx.Done():
		sb.Release()
		return c.ctx.Err()
	}
}

// 🆕 Async write method, lock-free design
func (c *quicConnection) writeDataAsync(data []byte) error {
	if c.closed.Load() {
		return fmt.Errorf("connection closed")
	}

	// 🔧 CRITICAL FIX: Must copy data before async processing!
	// The caller may reuse the buffer before our async write completes,
	// causing data corruption (random byte modifications).
	dataCopy := make([]byte, len(data))
	copy(dataCopy, data)

	return c.writeDataAsyncNoCopy(dataCopy)
}

// 🆕 Zero-copy async write - caller guarantees data independence
func (c *quicConnection) writeDataAsyncNoCopy(data []byte) error {
	if c.closed.Load() {
		return fmt.Errorf("connection closed")
	}

	req := &writeRequest{
		data:    data,
		errChan: nil, // No error channel = fire-and-forget
	}

	// 🔧 FIX: Block until channel has space or context canceled
	// Don't timeout and drop data - that causes unnecessary packet loss
	select {
	case c.writeChan <- req:
		return nil
	case <-c.ctx.Done():
		return c.ctx.Err()
	}
}

// 🆕 Direct write data method, only used in writeLoop
func (c *quicConnection) writeDataDirect(data []byte) error {
	// Write length prefix (4 bytes)
	// Check for potential overflow before conversion
	dataLen := len(data)
	if dataLen > 0xFFFFFFFF {
		return fmt.Errorf("data too large: %d bytes", dataLen)
	}
	if dataLen < 0 {
		return fmt.Errorf("invalid data length: %d", dataLen)
	}
	length := uint32(dataLen) // Safe conversion after bounds check
	if err := binary.Write(c.stream, binary.BigEndian, length); err != nil {
		return fmt.Errorf("write length: %v", err)
	}

	// Write data
	if _, err := c.stream.Write(data); err != nil {
		return fmt.Errorf("write data: %v", err)
	}

	return nil
}

// 🆕 Keep writeData method for direct use during authentication (synchronous write)
func (c *quicConnection) writeData(data []byte) error {
	return c.writeDataDirect(data)
}

// ReadMessage implements transport.Connection
func (c *quicConnection) ReadMessage() ([]byte, error) {
	select {
	case data, ok := <-c.readChan:
		if !ok {
			return nil, io.EOF // receiveLoop ended: the stream is closed
		}
		return data, nil
	case <-c.ctx.Done():
		return nil, c.ctx.Err()
	}
}

// Close implements transport.Connection
func (c *quicConnection) Close() error {
	var err error
	c.closeOnce.Do(func() {
		c.closed.Store(true)

		// Cancel context
		if c.cancel != nil {
			c.cancel()
		}

		// Let writeLoop flush queued messages first: Close must not run
		// concurrently with Write. The short deadline bounds the flush, since
		// callers such as Gateway.Stop close clients one by one; it is enough
		// for a final message like the gateway's rejection.
		if c.stream != nil {
			_ = c.stream.SetWriteDeadline(time.Now().Add(time.Second))
		}
		<-c.done

		// Close stream
		if c.stream != nil {
			if err := c.stream.Close(); err != nil {
				logger.Warn("Error closing QUIC stream", "err", err)
			}
		}

		// Only client connections close the entire QUIC connection
		if c.isClient && c.conn != nil {
			err = c.conn.CloseWithError(0, "connection closed")
		}
	})
	return err
}

// RemoteAddr implements transport.Connection
func (c *quicConnection) RemoteAddr() net.Addr {
	if c.conn != nil {
		return c.conn.RemoteAddr()
	}
	return &simpleAddr{network: "quic", address: "quic-remote"}
}

// LocalAddr implements transport.Connection
func (c *quicConnection) LocalAddr() net.Addr {
	if c.conn != nil {
		return c.conn.LocalAddr()
	}
	return &simpleAddr{network: "quic", address: "quic-local"}
}

// GetClientID gets client ID - for upper layer code to extract client information
func (c *quicConnection) GetClientID() string {
	return c.clientID
}

// GetGroupID gets group ID - for upper layer code to extract client information
func (c *quicConnection) GetGroupID() string {
	return c.groupID
}

// GetPassword gets password - for upper layer code to extract client information
func (c *quicConnection) GetPassword() string {
	return c.groupPassword
}

// =============================================================================
// QUIC Datagram Support (for low-latency UDP relay)
// =============================================================================

// SendDatagram sends unreliable data via QUIC Datagram
// Used for TUIC v5 native UDP relay mode - low latency, may lose packets
// Implements transport.DatagramWriter interface
func (c *quicConnection) SendDatagram(data []byte) error {
	if c.conn == nil {
		return fmt.Errorf("QUIC connection is nil")
	}
	return c.conn.SendDatagram(data)
}

// ReceiveDatagram receives unreliable data via QUIC Datagram
// Implements transport.DatagramReader interface
func (c *quicConnection) ReceiveDatagram() ([]byte, error) {
	if c.conn == nil {
		return nil, fmt.Errorf("QUIC connection is nil")
	}
	return c.conn.ReceiveDatagram(c.ctx)
}

// receiveLoop handles incoming messages
func (c *quicConnection) receiveLoop() {
	// Only readChan is closed, so ReadMessage returns queued data before io.EOF.
	defer close(c.readChan)

	for {
		select {
		case <-c.ctx.Done():
			return
		default:
			data, err := c.readData()
			if err != nil {
				// A length-prefixed stream cannot recover from a read error.
				logger.Debug("QUIC stream read ended", "client_id", c.clientID, "err", err)
				return
			}

			select {
			case c.readChan <- data:
			case <-c.ctx.Done():
				return
			}
		}
	}
}

// readData reads data from QUIC stream using simple length-prefix format
func (c *quicConnection) readData() ([]byte, error) {
	// Read length prefix (4 bytes)
	var length uint32
	if err := binary.Read(c.stream, binary.BigEndian, &length); err != nil {
		return nil, fmt.Errorf("read length: %v", err)
	}

	// Check length reasonableness (max 10MB)
	if length > 10*1024*1024 {
		return nil, fmt.Errorf("message too large: %d bytes", length)
	}

	// Read data
	data := make([]byte, length)
	if _, err := io.ReadFull(c.stream, data); err != nil {
		return nil, fmt.Errorf("read data: %v", err)
	}

	return data, nil
}

// isQUICError checks if the error indicates a QUIC connection issue
func isQUICError(err error) bool {
	if err == nil {
		return false
	}

	// Check common QUIC error types
	switch err.(type) {
	case *quic.ApplicationError, *quic.TransportError:
		return true
	}

	return err == io.EOF
}

// simpleAddr simple address implementation
type simpleAddr struct {
	network, address string
}

func (a *simpleAddr) Network() string { return a.network }
func (a *simpleAddr) String() string  { return a.address }
