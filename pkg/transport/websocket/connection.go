package websocket

import (
	"net"
	"sync"

	"github.com/gorilla/websocket"

	"github.com/buhuipao/anyproxy/pkg/transport"
)

const (
	writeBufSize = 1000 // Optimized buffer size
)

// webSocketConnectionWithInfo WebSocket connection implementation with client information and high-performance writing
type webSocketConnectionWithInfo struct {
	conn      *websocket.Conn
	clientID  string
	groupID   string
	password  string           // Client password for group credential management
	writer    *Writer          // 🆕 Integrated high-performance writer
	writeBuf  chan interface{} // 🆕 Async write queue
	closeOnce sync.Once        // Ensure Close() is only executed once
}

var _ transport.Connection = (*webSocketConnectionWithInfo)(nil)

// NewWebSocketConnectionWithInfo creates WebSocket connection wrapper with client information and high-performance writing
func NewWebSocketConnectionWithInfo(conn *websocket.Conn, clientID, groupID, password string) transport.Connection {
	// 🆕 Create write buffer
	writeBuf := make(chan interface{}, writeBufSize)

	// 🆕 Create high-performance writer, using clientID as identifier (transport layer level tracking)
	writer := NewWriterWithID(conn, writeBuf, clientID)
	writer.Start()

	return &webSocketConnectionWithInfo{
		conn:     conn,
		clientID: clientID,
		groupID:  groupID,
		password: password,
		writer:   writer,   // 🆕 High-performance writer
		writeBuf: writeBuf, // 🆕 Async queue
	}
}

// WriteMessage implements transport.Connection
func (c *webSocketConnectionWithInfo) WriteMessage(data []byte) error {
	return c.writer.WriteMessage(data)
}

// ReadMessage implements transport.Connection
func (c *webSocketConnectionWithInfo) ReadMessage() ([]byte, error) {
	_, data, err := c.conn.ReadMessage()
	return data, err
}

// Close gracefully closes connection (🆕 using high-performance writer's graceful stop)
func (c *webSocketConnectionWithInfo) Close() error {
	var err error
	c.closeOnce.Do(func() {
		// 🆕 First stop writer, ensure all messages are sent and connection is closed by writer
		if c.writer != nil {
			c.writer.Stop() // Writer will close the WebSocket connection
		}

		// 🆕 Close write buffer
		if c.writeBuf != nil {
			close(c.writeBuf)
		}

		// Note: WebSocket connection is already closed by writer.Stop(),
		// no need to close it again to avoid potential double-close issues
	})
	return err
}

func (c *webSocketConnectionWithInfo) RemoteAddr() net.Addr {
	return c.conn.RemoteAddr()
}

func (c *webSocketConnectionWithInfo) LocalAddr() net.Addr {
	return c.conn.LocalAddr()
}

// GetClientID gets client ID
func (c *webSocketConnectionWithInfo) GetClientID() string {
	return c.clientID
}

// GetGroupID gets group ID
func (c *webSocketConnectionWithInfo) GetGroupID() string {
	return c.groupID
}

// GetPassword gets client password
func (c *webSocketConnectionWithInfo) GetPassword() string {
	return c.password
}
