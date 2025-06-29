package quic

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/buhuipao/anyproxy/pkg/logger"
	"github.com/buhuipao/anyproxy/pkg/transport"
)

// 🆕 Write request type
type writeRequest struct {
	data    []byte
	errChan chan error
}

// quicConnection implements transport.Connection for QUIC streams
type quicConnection struct {
	stream        quic.Stream
	conn          quic.Connection
	clientID      string
	groupID       string
	groupPassword string // Client password for group credential management
	// 🆕 Remove mutex, use async writes instead
	writeChan chan *writeRequest // 🆕 Async write queue
	closed    bool
	ctx       context.Context
	cancel    context.CancelFunc
	readChan  chan []byte
	errorChan chan error
	closeOnce sync.Once
	isClient  bool // Whether this is a client connection
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
		writeChan:     make(chan *writeRequest, 1000), // 🆕 Async write queue
		ctx:           ctx,
		cancel:        cancel,
		readChan:      make(chan []byte, 100),
		errorChan:     make(chan error, 1),
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
		writeChan:     make(chan *writeRequest, 1000), // 🆕 Async write queue
		ctx:           ctx,
		cancel:        cancel,
		readChan:      make(chan []byte, 100),
		errorChan:     make(chan error, 1),
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
			case req, ok := <-c.writeChan:
				if !ok {
					// Channel is closed, exit
					return
				}
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
				return
			}
		}
	}()

	for {
		select {
		case <-c.ctx.Done():
			return
		case req, ok := <-c.writeChan:
			if !ok {
				// writeChan is closed
				return
			}

			if c.closed {
				if req.errChan != nil {
					req.errChan <- fmt.Errorf("connection closed")
					close(req.errChan)
				}
				continue
			}

			err := c.writeDataDirect(req.data)
			if err != nil && isQUICError(err) {
				c.closed = true
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

// 🆕 Async write method, lock-free design
func (c *quicConnection) writeDataAsync(data []byte) error {
	if c.closed {
		return fmt.Errorf("connection closed")
	}

	errChan := make(chan error, 1)
	req := &writeRequest{
		data:    data,
		errChan: errChan,
	}

	select {
	case c.writeChan <- req:
		// Wait for write result
		select {
		case err := <-errChan:
			return err
		case <-c.ctx.Done():
			// Fix: Use timed select to prevent goroutine leaks
			go func() {
				select {
				case <-errChan:
					// Successfully consumed error
				case <-time.After(5 * time.Second):
					// Exit after timeout to prevent permanent blocking
					logger.Warn("Timeout waiting for write error channel", "client_id", c.clientID)
				}
			}()
			return c.ctx.Err()
		}
	case <-c.ctx.Done():
		// Fix: No need to close errChan since no goroutine is waiting for it
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
	case data := <-c.readChan:
		return data, nil
	case err := <-c.errorChan:
		return nil, err
	case <-c.ctx.Done():
		return nil, c.ctx.Err()
	}
}

// Close implements transport.Connection
func (c *quicConnection) Close() error {
	var err error
	c.closeOnce.Do(func() {
		c.closed = true

		// Cancel context
		if c.cancel != nil {
			c.cancel()
		}

		// 🆕 Close write queue
		close(c.writeChan)

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

// receiveLoop handles incoming messages
func (c *quicConnection) receiveLoop() {
	defer func() {
		close(c.readChan)
		close(c.errorChan)
	}()

	for {
		select {
		case <-c.ctx.Done():
			return
		default:
			data, err := c.readData()
			if err != nil {
				if err == io.EOF || isQUICError(err) {
					return
				}
				select {
				case c.errorChan <- err:
				case <-c.ctx.Done():
					return
				}
				continue
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
