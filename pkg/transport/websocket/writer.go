package websocket

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/buhuipao/anyproxy/pkg/logger"
	"github.com/buhuipao/anyproxy/pkg/transport"
	"github.com/gorilla/websocket"
)

const (
	// Time allowed to write a message to the peer.
	writeWait = 10 * time.Second

	// Time allowed to read the next pong message from the peer.
	pongWait = 60 * time.Second

	// Send pings to peer with this period. Must be less than pongWait.
	pingPeriod = (pongWait * 9) / 10
)

var (
	// ErrStopped is returned when trying to write to a stopped writer
	ErrStopped = errors.New("writer stopped")

	// ErrQueueFull is returned when the write queue is full
	ErrQueueFull = errors.New("write queue full")

	// ErrWriteTimeout is returned when a write operation times out
	ErrWriteTimeout = errors.New("write timeout")
)

// writeMsg represents a message to be written
type writeMsg struct {
	msgType   int
	data      []byte
	callback  chan error
	streamBuf streamBuffer // For StreamBuffer ownership transfer
}

// streamBuffer interface matches transport.StreamBuffer
type streamBuffer interface {
	Full() []byte
	Release()
}

// Writer manages WebSocket write operations in a single goroutine
type Writer struct {
	conn         *websocket.Conn
	once         sync.Once
	wg           sync.WaitGroup
	messageCount int64
	bytesWritten int64
	connectionID string
	stopped      atomic.Bool
	stopOnce     sync.Once
	stopCh       chan struct{}
	ch           chan *writeMsg
	queueTimeout time.Duration
	writeTimeout time.Duration
}

// NewWriterWithID creates a new WebSocket writer with specific connection ID
func NewWriterWithID(conn *websocket.Conn, _ chan interface{}, connID string) *Writer {
	logger.Debug("Creating new WebSocket writer", "conn_id", connID, "remote_addr", conn.RemoteAddr(), "local_addr", conn.LocalAddr())

	writer := &Writer{
		conn:         conn,
		connectionID: connID,
		stopCh:       make(chan struct{}),
		ch:           make(chan *writeMsg, transport.WriteChannelSize),
		queueTimeout: 5 * time.Second,
		writeTimeout: 10 * time.Second,
	}

	logger.Debug("WebSocket writer created successfully", "conn_id", connID)

	return writer
}

// Start starts the writer goroutine
func (w *Writer) Start() {
	logger.Info("Starting WebSocket writer", "conn_id", w.connectionID, "remote_addr", w.conn.RemoteAddr())

	w.wg.Add(1)
	go w.run()

	logger.Debug("WebSocket writer goroutine started", "conn_id", w.connectionID)
}

// Stop stops the writer and waits for completion
func (w *Writer) Stop() {
	logger.Info("Stopping WebSocket writer", "conn_id", w.connectionID, "messages_written", atomic.LoadInt64(&w.messageCount))

	w.once.Do(func() {
		w.stopped.Store(true)
		w.stopOnce.Do(func() { close(w.stopCh) })

		// wait for writer to finish
		logger.Debug("Waiting for WebSocket writer goroutine to finish", "conn_id", w.connectionID)
		w.wg.Wait()

		// Close the WebSocket connection
		logger.Debug("Closing WebSocket connection", "conn_id", w.connectionID)
		if err := w.conn.Close(); err != nil {
			logger.Debug("Error closing WebSocket connection (expected during shutdown)", "conn_id", w.connectionID, "err", err)
		}

		logger.Info("WebSocket writer stopped", "conn_id", w.connectionID, "total_messages", w.messageCount, "total_bytes", w.bytesWritten)
	})
}

// WriteMessage queues a binary message for writing
func (w *Writer) WriteMessage(data []byte) error {
	if w.stopped.Load() {
		return ErrStopped
	}

	msg := &writeMsg{
		msgType:  websocket.BinaryMessage,
		data:     data,
		callback: nil, // No callback = fire-and-forget
	}

	// 🔧 FIX: Block until channel has space or writer stopped
	// Don't timeout and drop data - that causes unnecessary packet loss
	select {
	case w.ch <- msg:
		return nil
	case <-w.stopCh:
		return ErrStopped
	}
}

// run is the main writer loop
func (w *Writer) run() {
	defer func() {
		logger.Debug("WebSocket writer stopped", "client_id", w.connectionID)
		w.wg.Done()
	}()

	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-w.stopCh:
			// Graceful shutdown
			w.handleShutdown()
			return

		case msg := <-w.ch:
			if err := w.handleWrite(msg); err != nil {
				w.fail("Write error", err)
				return
			}

		case <-ticker.C:
			// Send ping
			if err := w.conn.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
				logger.Warn("Failed to set write deadline for ping", "conn_id", w.connectionID, "err", err)
			}
			if err := w.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				w.fail("Ping error", err)
				return
			}
		}
	}
}

// fail stops the writer after a write error. Gorilla write errors are sticky,
// so the connection is closed to make the reader and the peer see it is gone.
func (w *Writer) fail(msg string, err error) {
	logger.Warn(msg, "conn_id", w.connectionID, "err", err)
	w.stopped.Store(true)
	w.stopOnce.Do(func() { close(w.stopCh) })
	if err := w.conn.Close(); err != nil {
		logger.Debug("Error closing WebSocket connection after write error", "conn_id", w.connectionID, "err", err)
	}
}

// handleWrite writes a message and sends result to callback
func (w *Writer) handleWrite(msg *writeMsg) error {
	if err := w.conn.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
		logger.Error("Failed to set write deadline", "conn_id", w.connectionID, "err", err)
	}

	var data []byte
	if msg.streamBuf != nil {
		// 🆕 Use StreamBuffer's Full() - no copy, direct reference
		data = msg.streamBuf.Full()
	} else {
		data = msg.data
	}

	err := w.conn.WriteMessage(msg.msgType, data)

	// Release StreamBuffer after write
	if msg.streamBuf != nil {
		msg.streamBuf.Release()
	}

	// Update statistics
	if err == nil {
		atomic.AddInt64(&w.messageCount, 1)
		atomic.AddInt64(&w.bytesWritten, int64(len(data)))
	}

	// Send result to callback if present
	if msg.callback != nil {
		select {
		case msg.callback <- err:
		default:
			// Callback channel might be closed if timed out
		}
	}

	return err
}

// WriteStreamBuffer queues a StreamBuffer for writing
// 🔧 ZERO-COPY: Takes ownership of the buffer and releases it after writing.
func (w *Writer) WriteStreamBuffer(sb streamBuffer) error {
	if w.stopped.Load() {
		sb.Release()
		return ErrStopped
	}

	msg := &writeMsg{
		msgType:   websocket.BinaryMessage,
		streamBuf: sb,
		callback:  nil, // Fire-and-forget
	}

	// 🔧 FIX: Block until channel has space or writer stopped
	// Don't timeout and drop data - that causes unnecessary packet loss
	select {
	case w.ch <- msg:
		return nil
	case <-w.stopCh:
		sb.Release()
		return ErrStopped
	}
}

// handleShutdown drains remaining messages
func (w *Writer) handleShutdown() {
	logger.Debug("Draining remaining WebSocket messages", "conn_id", w.connectionID)

	drainedCount := 0

	// Drain main channel
	for {
		select {
		case msg := <-w.ch:
			if err := w.handleWrite(msg); err != nil {
				logger.Warn("Error writing message during drain", "err", err)
			} else {
				drainedCount++
			}
		default:
			logger.Debug("WebSocket message drain completed", "conn_id", w.connectionID, "drained_messages", drainedCount)
			return
		}
	}
}
