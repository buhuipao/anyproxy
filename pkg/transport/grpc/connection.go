package grpc

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/buhuipao/anyproxy/pkg/transport"
)

// 🆕 Write message type
type writeRequest struct {
	msgType   StreamMessage_MessageType
	data      []byte
	errChan   chan error
	streamBuf transport.StreamBuffer // For StreamBuffer ownership transfer
}

// grpcStream unified stream interface
type grpcStream interface {
	Send(*StreamMessage) error
	Recv() (*StreamMessage, error)
	Context() context.Context
}

// grpcConnection unified gRPC connection implementation
type grpcConnection struct {
	stream        grpcStream
	conn          *grpc.ClientConn // Only client connections have this
	clientID      string
	groupID       string
	groupPassword string // Client password for group credential management
	// 🆕 Remove mutex, use async writes instead
	writeChan chan *writeRequest // Async write queue; never closed, writers stop on ctx
	closed    atomic.Bool
	ctx       context.Context
	cancel    context.CancelFunc
	readChan  chan []byte
	errorChan chan error
	closeOnce sync.Once
	done      chan struct{} // Closed when writeLoop has flushed and exited
}

var _ transport.Connection = (*grpcConnection)(nil)

// newGRPCConnection creates a client gRPC connection
func newGRPCConnection(stream TransportService_BiStreamClient, conn *grpc.ClientConn, clientID, groupID, groupPassword string) *grpcConnection {
	ctx, cancel := context.WithCancel(context.Background())

	c := &grpcConnection{
		stream:        stream,
		conn:          conn,
		clientID:      clientID,
		groupID:       groupID,
		groupPassword: groupPassword,
		writeChan:     make(chan *writeRequest, transport.WriteChannelSize),
		ctx:           ctx,
		cancel:        cancel,
		readChan:      make(chan []byte, transport.ReadChannelSize),
		errorChan:     make(chan error, 1),
		done:          make(chan struct{}),
	}

	// 🆕 Start read/write goroutines
	go c.receiveLoop()
	go c.writeLoop()
	return c
}

// newGRPCServerConnection creates a server gRPC connection
func newGRPCServerConnection(stream TransportService_BiStreamServer, clientID, groupID, groupPassword string) *grpcConnection {
	ctx, cancel := context.WithCancel(stream.Context())

	c := &grpcConnection{
		stream:        stream,
		conn:          nil, // Server connections don't have client connections
		clientID:      clientID,
		groupID:       groupID,
		groupPassword: groupPassword,
		writeChan:     make(chan *writeRequest, transport.WriteChannelSize),
		ctx:           ctx,
		cancel:        cancel,
		readChan:      make(chan []byte, transport.ReadChannelSize),
		errorChan:     make(chan error, 1),
		done:          make(chan struct{}),
	}

	// 🆕 Start read/write goroutines
	go c.receiveLoop()
	go c.writeLoop()
	return c
}

// 🆕 Async write goroutine, avoiding lock contention
func (c *grpcConnection) writeLoop() {
	defer close(c.done)

	for {
		select {
		case <-c.ctx.Done():
			// Flush what is already queued (e.g. a final error message)
			// before exiting; a server's BiStream ends the RPC only after
			// this, like the websocket drain.
			if len(c.writeChan) == 0 {
				return
			}
		case req := <-c.writeChan:
			var data []byte
			if req.streamBuf != nil {
				// 🆕 Use StreamBuffer's Full() - no copy, direct reference
				data = req.streamBuf.Full()
			} else {
				data = req.data
			}

			msg := &StreamMessage{
				Type:     req.msgType,
				Data:     data,
				ClientId: c.clientID,
				GroupId:  c.groupID,
			}

			err := c.stream.Send(msg)

			// Release StreamBuffer after send (protobuf may have copied internally)
			if req.streamBuf != nil {
				req.streamBuf.Release()
			}

			if err != nil && isGRPCError(err) {
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
func (c *grpcConnection) WriteMessage(data []byte) error {
	return c.writeMessageAsync(StreamMessage_DATA, data)
}

// WriteMessageNoCopy implements transport.NoCopyWriter
// 🔧 PERFORMANCE: Caller guarantees data won't be modified, skip copy
func (c *grpcConnection) WriteMessageNoCopy(data []byte) error {
	return c.writeMessageAsyncNoCopy(StreamMessage_DATA, data)
}

// WriteStreamBuffer implements transport.StreamBufferWriter
// 🔧 ZERO-COPY: Avoids user-space copies by using StreamBuffer directly.
// Note: gRPC/protobuf may still copy internally, but we avoid our copies.
// Takes ownership of the StreamBuffer and releases it after sending.
func (c *grpcConnection) WriteStreamBuffer(sb transport.StreamBuffer) error {
	if c.closed.Load() {
		sb.Release()
		return fmt.Errorf("connection closed")
	}

	req := &writeRequest{
		msgType:   StreamMessage_DATA,
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
func (c *grpcConnection) writeMessageAsync(msgType StreamMessage_MessageType, data []byte) error {
	if c.closed.Load() {
		return fmt.Errorf("connection closed")
	}

	// 🔧 CRITICAL FIX: Must copy data before async processing!
	// The caller may reuse the buffer before our async write completes,
	// causing data corruption (random byte modifications).
	dataCopy := make([]byte, len(data))
	copy(dataCopy, data)

	return c.writeMessageAsyncNoCopy(msgType, dataCopy)
}

// 🆕 Zero-copy async write - caller guarantees data independence
func (c *grpcConnection) writeMessageAsyncNoCopy(msgType StreamMessage_MessageType, data []byte) error {
	if c.closed.Load() {
		return fmt.Errorf("connection closed")
	}

	req := &writeRequest{
		msgType: msgType,
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

// ReadMessage implements transport.Connection
func (c *grpcConnection) ReadMessage() ([]byte, error) {
	select {
	case data, ok := <-c.readChan:
		if !ok {
			return nil, io.EOF // receiveLoop ended: the stream is closed
		}
		return data, nil
	case err := <-c.errorChan:
		return nil, err
	case <-c.ctx.Done():
		return nil, c.ctx.Err()
	}
}

// Close implements transport.Connection
func (c *grpcConnection) Close() error {
	var err error
	c.closeOnce.Do(func() {
		c.closed.Store(true)

		// Cancel context
		if c.cancel != nil {
			c.cancel()
		}

		// Only client connections close the gRPC connection
		if c.conn != nil {
			err = c.conn.Close()
		}
	})
	return err
}

// RemoteAddr implements transport.Connection
func (c *grpcConnection) RemoteAddr() net.Addr {
	if c.conn != nil {
		return &simpleAddr{network: grpcNetwork, address: c.conn.Target()}
	}
	return &simpleAddr{network: grpcNetwork, address: "grpc-client"}
}

// LocalAddr implements transport.Connection
func (c *grpcConnection) LocalAddr() net.Addr {
	return &simpleAddr{network: grpcNetwork, address: "grpc-server"}
}

// GetClientID gets client ID - for upper layer code to extract client information
func (c *grpcConnection) GetClientID() string {
	return c.clientID
}

// GetGroupID gets group ID - for upper layer code to extract client information
func (c *grpcConnection) GetGroupID() string {
	return c.groupID
}

// GetPassword gets password - for upper layer code to extract client password
func (c *grpcConnection) GetPassword() string {
	return c.groupPassword
}

// receiveLoop handles receiving messages
func (c *grpcConnection) receiveLoop() {
	// Only readChan is closed, so ReadMessage returns queued data before io.EOF.
	defer close(c.readChan)

	for {
		select {
		case <-c.ctx.Done():
			return
		default:
			msg, err := c.stream.Recv()
			if err != nil {
				if err == io.EOF || isGRPCError(err) {
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
			case c.readChan <- msg.Data:
			case <-c.ctx.Done():
				return
			}
		}
	}
}

// isGRPCError checks if the error is a connection error
func isGRPCError(err error) bool {
	if err == nil {
		return false
	}

	if st, ok := status.FromError(err); ok {
		switch st.Code() {
		case codes.Unavailable, codes.Canceled, codes.DeadlineExceeded:
			return true
		}
	}
	return false
}

// grpcNetwork is the network name of gRPC connection addresses
const grpcNetwork = "grpc"

// simpleAddr simple address implementation
type simpleAddr struct {
	network, address string
}

func (a *simpleAddr) Network() string { return a.network }
func (a *simpleAddr) String() string  { return a.address }
