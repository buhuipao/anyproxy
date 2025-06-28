package protocol

import "time"

// Message type constants
const (
	MsgTypeConnect         = "connect"
	MsgTypeConnectResponse = "connect_response"
	MsgTypeData            = "data"
	MsgTypeClose           = "close"
	MsgTypePortForwardReq  = "port_forward_request"
	MsgTypePortForwardResp = "port_forward_response"
	MsgTypeError           = "error"
)

// Protocol constants
const (
	ProtocolTCP = "tcp"
	ProtocolUDP = "udp"
)

// Scheme constants
const (
	SchemeHTTPS = "https"
	SchemeHTTP  = "http"
)

// Transport type constants
const (
	TransportTypeGRPC      = "grpc"
	TransportTypeWebSocket = "websocket"
	TransportTypeQUIC      = "quic"
	TransportTypeDefault   = TransportTypeGRPC
)

// Timeout configuration
var (
	// DefaultConnectTimeout default connection timeout
	DefaultConnectTimeout = 30 * time.Second

	// DefaultReadTimeout default read timeout
	DefaultReadTimeout = 30 * time.Second

	// DefaultWriteTimeout default write timeout
	DefaultWriteTimeout = 30 * time.Second

	// DefaultShutdownTimeout default shutdown timeout
	DefaultShutdownTimeout = 3 * time.Second

	// DefaultMessageChannelSize default message channel size
	DefaultMessageChannelSize = 100

	// DefaultBufferSize default buffer size
	DefaultBufferSize = 32 * 1024 // 32KB
)

// SetConnectTimeout sets connection timeout (for testing or dynamic configuration)
func SetConnectTimeout(timeout time.Duration) {
	DefaultConnectTimeout = timeout
}

// SetReadTimeout sets read timeout
func SetReadTimeout(timeout time.Duration) {
	DefaultReadTimeout = timeout
}

// SetWriteTimeout sets write timeout
func SetWriteTimeout(timeout time.Duration) {
	DefaultWriteTimeout = timeout
}
