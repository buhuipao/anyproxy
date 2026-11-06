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
	MsgTypeHeartbeat       = "heartbeat"     // 🆕 Heartbeat message (client -> gateway)
	MsgTypeHeartbeatAck    = "heartbeat_ack" // 🆕 Heartbeat acknowledgment (gateway -> client)
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

	// P-005: Message channel size for proxy connections
	// 🔧 GAMING OPTIMIZATION: Tuned for balance between latency and throughput
	// 2000 messages * ~1200 bytes ≈ 2.4MB buffer, ~200ms max latency at 100Mbps
	// Smaller than before (10000) to reduce bufferbloat while handling traffic bursts
	DefaultMessageChannelSize = 2000

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
