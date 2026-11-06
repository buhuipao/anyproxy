// Package transport provides transport layer abstractions for AnyProxy.
package transport

import (
	"crypto/tls"
	"net"
)

// =============================================================================
// Unified Channel Buffer Sizes for Transport Layers
// =============================================================================
// These constants ensure consistent behavior across QUIC, gRPC, and WebSocket.
// Sized for 1Gbps+ throughput with ~93K packets/sec.
//
// Note: BufferedConn buffer sizes are defined in pkg/common/connection/buffered_conn.go

const (
	// WriteChannelSize is the buffer size for write channels (outgoing data)
	// Used by: QUIC writeChan, gRPC writeChan, WebSocket ch.
	// At 500000 the channel itself became the OOM source: each queued
	// writeRequest retains a 64KB StreamBuffer, so a stalled reader could
	// pin hundreds of MB of packet data (observed: 1.6GB client RSS during
	// 4K120 Moonlight streaming). 2000 caps worst-case channel memory
	// while still absorbing normal bursts.
	WriteChannelSize = 2000

	// ReadChannelSize is the buffer size for read channels (incoming data)
	// Used by: QUIC readChan, gRPC readChan. Same rationale as WriteChannelSize.
	ReadChannelSize = 2000
)

// ClientAuthValidator is a callback function for dynamic client authentication
// It validates client_id and client_key against pre-registered credentials
// Returns true if the credentials are valid
type ClientAuthValidator func(clientID, clientKey string) bool

// AuthConfig authentication configuration
type AuthConfig struct {
	Username        string              // Fixed gateway auth username
	Password        string              // Fixed gateway auth password
	ClientValidator ClientAuthValidator // Dynamic client auth validator callback
}

// Transport interface - minimalist design to support multiple transport protocols
type Transport interface {
	// Server side: listen and handle connections (🆕 supports TLS configuration)
	ListenAndServe(addr string, handler func(Connection)) error
	ListenAndServeWithTLS(addr string, handler func(Connection), tlsConfig *tls.Config) error
	// Client side: connect to server (supports configuration)
	DialWithConfig(addr string, config *ClientConfig) (Connection, error)
	// Close transport layer
	Close() error
}

// Connection interface - simplified connection abstraction
type Connection interface {
	// Write message (binary data)
	WriteMessage(data []byte) error
	// Read message
	ReadMessage() ([]byte, error)
	// Connection management
	Close() error
	RemoteAddr() net.Addr
	LocalAddr() net.Addr
	// Client information - must be implemented by all transport layers
	GetClientID() string
	GetGroupID() string
	GetPassword() string // Get the client password for group credential management
}

// =============================================================================
// QUIC Datagram Interfaces (for low-latency UDP relay)
// =============================================================================

// DatagramWriter interface for unreliable datagram sending
// Used for TUIC v5 native UDP relay mode (QUIC Datagram)
// Provides low latency at the cost of potential packet loss
type DatagramWriter interface {
	SendDatagram(data []byte) error
}

// DatagramReader interface for unreliable datagram receiving
type DatagramReader interface {
	ReceiveDatagram() ([]byte, error)
}

// DatagramConnection combines both for bidirectional datagrams
type DatagramConnection interface {
	DatagramWriter
	DatagramReader
}

// =============================================================================
// Zero-Copy Write Interfaces (Unified definitions)
// =============================================================================

// MessageWriter is the basic interface for writing messages.
// All transport connections implement this via Connection.WriteMessage.
type MessageWriter interface {
	WriteMessage(data []byte) error
}

// NoCopyWriter is an optional interface for connections that support zero-copy writes.
// Caller guarantees that data won't be modified after the call.
// 🔧 PERFORMANCE: Avoids extra copy when caller guarantees data independence.
type NoCopyWriter interface {
	WriteMessageNoCopy(data []byte) error
}

// StreamBufferWriter is an optional interface for connections that support
// zero-copy StreamBuffer writes with writev (scatter-gather I/O).
// This provides the best performance by avoiding all user-space copies.
// The implementation takes ownership of the buffer and will release it after writing.
type StreamBufferWriter interface {
	WriteStreamBuffer(sb StreamBuffer) error
}

// StreamBuffer interface for zero-copy buffer operations.
// Implemented by buffer.StreamBuffer to avoid circular imports.
type StreamBuffer interface {
	// Header operations
	HeaderSlice() []byte
	HeaderLen() int
	// Data operations
	DataPortion() []byte
	DataLen() int
	// Combined operations
	Full() []byte
	TotalLen() int
	// Lifecycle
	Release()
}

// =============================================================================
// Client Configuration
// =============================================================================

// ClientConfig client configuration
type ClientConfig struct {
	ClientID      string
	Username      string
	Password      string // Gateway authentication password
	GroupID       string
	GroupPassword string // Client group password for proxy authentication
	TLSCert       string
	TLSConfig     *tls.Config
	SkipVerify    bool
}

// ConnectionHandler connection handler function type
type ConnectionHandler func(Connection)
