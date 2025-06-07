package websocket

import (
	"net"

	"github.com/gorilla/websocket"

	"github.com/buhuipao/anyproxy/pkg/proxy_v2/transport"
)

const (
	writeBufSize = 1000 // 与 v1 相同的缓冲区大小
)

// webSocketConnectionWithInfo 带有客户端信息和高性能写入的 WebSocket 连接实现 (🆕 集成 v1 的性能优化)
type webSocketConnectionWithInfo struct {
	conn     *websocket.Conn
	clientID string
	groupID  string
	writer   *Writer          // 🆕 集成高性能 writer
	writeBuf chan interface{} // 🆕 异步写入队列
}

var _ transport.Connection = (*webSocketConnectionWithInfo)(nil)

// NewWebSocketConnectionWithInfo 创建带有客户端信息和高性能写入的 WebSocket 连接包装器 (🆕 集成 v1 性能优化)
func NewWebSocketConnectionWithInfo(conn *websocket.Conn, clientID, groupID string) transport.Connection {
	// 🆕 创建写入缓冲区 (与 v1 相同)
	writeBuf := make(chan interface{}, writeBufSize)

	// 🆕 创建高性能 writer，使用 clientID 作为标识符（传输层级别的追踪）
	writer := NewWriterWithID(conn, writeBuf, clientID)
	writer.Start()

	return &webSocketConnectionWithInfo{
		conn:     conn,
		clientID: clientID,
		groupID:  groupID,
		writer:   writer,   // 🆕 高性能 writer
		writeBuf: writeBuf, // 🆕 异步队列
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

// Close 优雅关闭连接 (🆕 使用高性能 writer 的优雅停止)
func (c *webSocketConnectionWithInfo) Close() error {
	// 🆕 首先停止 writer，确保所有消息都被发送
	if c.writer != nil {
		c.writer.Stop()
	}

	// 🆕 关闭写入缓冲区
	if c.writeBuf != nil {
		close(c.writeBuf)
	}

	// 然后关闭底层连接 (writer.Stop() 已经关闭了，但为了安全再次调用)
	return c.conn.Close()
}

func (c *webSocketConnectionWithInfo) RemoteAddr() net.Addr {
	return c.conn.RemoteAddr()
}

func (c *webSocketConnectionWithInfo) LocalAddr() net.Addr {
	return c.conn.LocalAddr()
}

// GetClientID 获取客户端ID
func (c *webSocketConnectionWithInfo) GetClientID() string {
	return c.clientID
}

// GetGroupID 获取组ID
func (c *webSocketConnectionWithInfo) GetGroupID() string {
	return c.groupID
}
