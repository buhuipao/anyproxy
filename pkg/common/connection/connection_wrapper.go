package connection

import (
	"net"
	"strconv"
	"sync"

	"github.com/buhuipao/anyproxy/pkg/common/protocol"
)

// ConnWrapper wraps a net.Conn and provides custom LocalAddr and RemoteAddr
type ConnWrapper struct {
	net.Conn
	localAddr     net.Addr
	remoteAddr    net.Addr
	connID        string
	mu            sync.RWMutex // Add read-write lock to protect connID
	network       string
	remoteAddress string
}

var _ net.Conn = (*ConnWrapper)(nil)

// NewConnWrapper creates a new connection wrapper with custom addresses
func NewConnWrapper(conn net.Conn, network, address string) *ConnWrapper {
	if network == "" {
		network = protocol.ProtocolTCP
	}

	wrapper := &ConnWrapper{
		Conn:          conn,
		network:       network,
		remoteAddress: address,
	}

	// Create a fake local address (SOCKS5 server binding address)
	localAddr := &net.TCPAddr{
		IP:   net.IPv4(127, 0, 0, 1),
		Port: 0, // Let the system assign a port
	}

	wrapper.localAddr = localAddr

	// Parse and set the remote address
	wrapper.remoteAddr = parseAddress(network, address)

	return wrapper
}

// parseAddress parses an address string and returns appropriate net.Addr
func parseAddress(network, address string) net.Addr {
	// Handle empty address
	if address == "" {
		if network == protocol.ProtocolTCP {
			return &net.TCPAddr{IP: net.IPv4(0, 0, 0, 0), Port: 80}
		}
		return &net.UDPAddr{IP: net.IPv4(0, 0, 0, 0), Port: 0}
	}

	// Try to parse as host:port
	host, portStr, err := net.SplitHostPort(address)
	if err != nil {
		// If parsing fails, assume it's just a host without port
		host = address
		if network == protocol.ProtocolTCP {
			portStr = "80" // Default HTTP port for TCP
		} else {
			portStr = "0"
		}
	}

	port, _ := strconv.Atoi(portStr)

	// Parse IP address. Domain names are not resolved here: the client at
	// the far end of the tunnel does the lookup, so use 0.0.0.0.
	ip := net.ParseIP(host)
	if ip == nil {
		ip = net.IPv4(0, 0, 0, 0)
	}

	// Return appropriate address type based on network protocol
	switch network {
	case protocol.ProtocolTCP:
		return &net.TCPAddr{IP: ip, Port: port}
	case protocol.ProtocolUDP:
		return &net.UDPAddr{IP: ip, Port: port}
	default:
		// For other protocols, return virtualAddr
		return &virtualAddr{
			network: network,
			address: address,
		}
	}
}

// LocalAddr returns the local network address
func (cw *ConnWrapper) LocalAddr() net.Addr {
	return cw.localAddr
}

// RemoteAddr returns the remote network address
func (cw *ConnWrapper) RemoteAddr() net.Addr {
	return cw.remoteAddr
}

// CloseWrite closes the whole connection. The tunnel has no half-close, so a
// relay that half-closes when one direction ends (such as go-socks5) must end
// the stream, or the other direction blocks on Read forever.
func (cw *ConnWrapper) CloseWrite() error {
	return cw.Close()
}

// GetConnID returns the connection ID
func (cw *ConnWrapper) GetConnID() string {
	cw.mu.RLock()
	defer cw.mu.RUnlock()
	return cw.connID
}

// SetConnID sets the connection ID
func (cw *ConnWrapper) SetConnID(connID string) {
	cw.mu.Lock()
	defer cw.mu.Unlock()
	cw.connID = connID
}

// virtualAddr implements net.Addr interface
type virtualAddr struct {
	network string
	address string
}

func (v *virtualAddr) Network() string {
	return v.network
}

func (v *virtualAddr) String() string {
	return v.address
}
