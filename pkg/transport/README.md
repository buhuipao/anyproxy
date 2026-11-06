# Transport Layer Implementation

This package provides multiple transport layer implementations for the AnyProxy system. Each transport layer implements the `Transport` interface and provides bidirectional communication between clients and gateways.

## Available Transport Layers

### 1. WebSocket Transport (`websocket`)

**Features:**
- HTTP/HTTPS upgrade to WebSocket
- TLS support (WSS)
- High-performance asynchronous writing
- Basic authentication support
- Cross-origin support

**Usage:**
```go
// Create WebSocket transport
transport := transport.CreateTransport("websocket", authConfig)

// Server side
err := transport.ListenAndServe(":8080", connectionHandler)

// Client side
conn, err := transport.DialWithConfig("localhost:8080", clientConfig)
```

### 2. gRPC Transport (`grpc`)

**Features:**
- HTTP/2 based bidirectional streaming
- Built-in TLS support
- Protocol buffer message framing
- Metadata-based authentication
- Connection multiplexing

**Usage:**
```go
// Create gRPC transport
transport := transport.CreateTransport("grpc", authConfig)

// Server side
err := transport.ListenAndServeWithTLS(":9090", connectionHandler, tlsConfig)

// Client side
conn, err := transport.DialWithConfig("localhost:9090", clientConfig)
```

**Protocol Definition:**
The gRPC transport uses a custom protocol defined in `transport.proto`:
- `BiStream` RPC for bidirectional streaming
- `StreamMessage` for data/JSON/control messages
- Metadata for client authentication

### 3. QUIC Transport (`quic`)

**Features:**
- UDP-based with built-in TLS 1.3
- Low latency connection establishment
- Connection migration support
- Stream multiplexing
- Custom message framing

**Usage:**
```go
// Create QUIC transport
transport := transport.CreateTransport("quic", authConfig)

// Server side (TLS required)
err := transport.ListenAndServeWithTLS(":9091", connectionHandler, tlsConfig)

// Client side
conn, err := transport.DialWithConfig("localhost:9091", clientConfig)
```

**Message Format:**
QUIC transport uses JSON-based message framing:
```json
{
  "type": 0,           // 0=DATA, 1=JSON, 2=CONTROL
  "data": "...",       // Base64 encoded data
  "client_id": "...",  // Client identifier
  "group_id": "..."    // Group identifier
}
```

## Transport Interface

All transport implementations follow the same interface:

```go
type Transport interface {
    // Server side
    ListenAndServe(addr string, handler func(Connection)) error
    ListenAndServeWithTLS(addr string, handler func(Connection), tlsConfig *tls.Config) error
    
    // Client side
    DialWithConfig(addr string, config *ClientConfig) (Connection, error)
    
    // Lifecycle
    Close() error
}

type Connection interface {
    WriteMessage(data []byte) error
    WriteJSON(v interface{}) error
    ReadMessage() ([]byte, error)
    Close() error
    RemoteAddr() net.Addr
    LocalAddr() net.Addr
}
```

## Configuration

### Client Configuration
```go
type ClientConfig struct {
    ClientID   string
    GroupID    string
    Username   string
    Password   string
    TLSConfig  *tls.Config
    SkipVerify bool
}
```

### Authentication Configuration
```go
type AuthConfig struct {
    Username string
    Password string
}
```

## Examples

### Gateway Example
```go
// Create gateway with gRPC transport
gw, err := gateway.NewGateway(cfg, "grpc")
if err != nil {
    log.Fatal(err)
}

// Start gateway
if err := gw.Start(); err != nil {
    log.Fatal(err)
}
```

### Client Example
```go
// Create client with QUIC transport
client, err := client.NewClient(cfg, "quic")
if err != nil {
    log.Fatal(err)
}

// Connect to gateway
if err := client.Connect(); err != nil {
    log.Fatal(err)
}
```

## Performance Characteristics

| Transport | Latency | Throughput | CPU Usage | Memory Usage |
|-----------|---------|------------|-----------|--------------|
| WebSocket | Medium  | High       | Low       | Low          |
| gRPC      | Low     | High       | Medium    | Medium       |
| QUIC      | Lowest  | Medium     | High      | Medium       |

## Security Features

- **WebSocket**: TLS 1.2/1.3 support, Basic authentication
- **gRPC**: TLS 1.2/1.3 support, Metadata-based auth, HTTP/2 security
- **QUIC**: Built-in TLS 1.3, Connection migration, Forward secrecy

## Transport Selection Guidelines

- **WebSocket**: Best for web-based clients, firewall-friendly
- **gRPC**: Best for service-to-service communication, strong typing
- **QUIC**: Best for mobile clients, unstable networks, lowest latency

## Implementation Notes

### gRPC Transport
- Uses protocol buffers for message serialization
- Supports bidirectional streaming
- Requires protobuf code generation
- Metadata used for authentication; the server sends response headers once it accepts the stream, and `DialWithConfig` waits for them, so a rejected client gets an `Unauthenticated` dial error. Gateways without this send headers only with their first message, so against them the dial waits 10s and then proceeds
- Closing the server-side connection flushes queued messages and then ends the RPC

### QUIC Transport
- Always requires TLS (no plaintext mode)
- Uses JSON for message serialization
- Custom framing with length prefixes
- First message used for authentication

### WebSocket Transport
- Supports both WS and WSS
- High-performance async writer
- HTTP upgrade mechanism
- Header-based authentication 