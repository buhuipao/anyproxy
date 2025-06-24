# Changelog

All notable changes to AnyProxy will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Planned Features
- Prometheus metrics integration
- Kubernetes deployment manifests
- Configuration hot reload
- Advanced load balancing algorithms
- Plugin system for custom protocols

## [v1.0.0] - 2025-06-15

### 🚀 Initial Production Release - AnyProxy

A secure tunneling solution with modern architecture, multiple transport protocols, comprehensive web management interface, and enterprise-grade features.

#### ✨ Core Features

**Multi-Transport Support**
- **WebSocket**: Firewall-friendly transport with HTTP/HTTPS compatibility and high-performance async writing
- **gRPC**: High-performance transport with HTTP/2 multiplexing and bidirectional streaming
- **QUIC**: Ultra-low latency transport with 0-RTT handshake for mobile and unreliable networks

**Triple Proxy Protocol Support**
- **HTTP Proxy**: Standard HTTP CONNECT proxy for web browsing and API access
- **SOCKS5 Proxy**: Universal protocol support with low overhead for general-purpose use
- **TUIC Proxy**: Ultra-low latency UDP-based proxy with 0-RTT handshake and connection migration

**Modern Architecture** 
- Group-based routing for multi-environment support (production/staging/development)
- Port forwarding with direct service access and protocol support (TCP/UDP)
- Modular transport layer design with pluggable protocols
- Comprehensive security with per-service access control

**Production-Ready Deployment**
- Docker containers with multi-architecture support (linux/amd64, linux/arm64)
- Clean YAML configuration format with comprehensive validation
- Optimized Gateway (public) and Client (private) separation
- Automated certificate generation and management

#### 🖥️ Web Management Interface

**Gateway Dashboard**
- Real-time metrics monitoring (connections, bandwidth, success rates)
- Client management with online/offline status detection and traffic analytics
- Session-based authentication with 24-hour automatic renewal
- Complete bilingual support (English/Chinese) with persistent localStorage preferences
- Auto-refresh capabilities with 10-second configurable intervals
- RESTful JSON API with CORS support for integration
- Memory-based metrics with automatic cleanup and inconsistency detection

**Client Dashboard**
- Local connection monitoring and performance metrics
- Multi-client instance support with runtime information tracking
- Optional authentication with configurable session management
- Real-time connection analytics with detailed traffic breakdown
- Client status monitoring with uptime and connection summaries

#### ⚡ Advanced Features

**Intelligent Metrics System**
- Memory-based real-time metrics collection with atomic operations
- Automatic client offline detection (2-minute inactivity timeout)
- Periodic cleanup process with 10-second intervals for responsive updates
- Connection count validation and automatic inconsistency correction
- Stale connection cleanup for offline clients

**Security Enhancements**
- Transport-level TLS encryption for all protocols
- Host-based access control with regex pattern support
- Group isolation for multi-tenant environments
- Certificate management with automated rotation support
- Comprehensive audit logging with structured output

**Monitoring & Analytics**
- Real-time connection tracking with individual lifecycle management
- Thread-safe metrics collection using atomic operations
- Client online/offline status with automatic state management
- Connection validation with automatic count correction
- Memory-efficient storage with intelligent cleanup (3-minute offline retention)

#### ⚙️ Configuration

**Enhanced Configuration Features**
- Pure YAML configuration format with schema validation
- Transport selection with automatic compatibility checking
- Environment-specific configuration with group routing
- Clean binary naming: `anyproxy-gateway` and `anyproxy-client`

**Configuration Options**
```yaml
transport:
  type: "websocket"  # websocket, grpc, or quic

proxy:
  http:
    listen_addr: ":8080"
    auth_username: "user"
    auth_password: "pass"
  socks5:
    listen_addr: ":1080"
    auth_username: "user"
    auth_password: "pass"
  tuic:
    listen_addr: ":9443"
    token: "secure-token"
    uuid: "client-uuid"
    cert_file: "certs/server.crt"
    key_file: "certs/server.key"

gateway:
  listen_addr: ":8443"
  tls_cert: "certs/server.crt"
  tls_key: "certs/server.key"
  web:
    enabled: true
    listen_addr: ":8090"
    auth_enabled: true
    auth_username: "admin"
    auth_password: "admin123"

client:
  group_id: "production"     # For group-based routing
  open_ports: []             # For port forwarding
  allowed_hosts: []          # Explicit allow list with regex support
  forbidden_hosts: []        # Security blacklist with CIDR support
  web:
    enabled: true
    listen_addr: ":8091"
```

#### 🐳 Docker Support

- Multi-architecture Docker images (linux/amd64, linux/arm64)
- Optimized Alpine-based runtime images with minimal attack surface
- Health check integration with container orchestration
- Non-root user execution for enhanced security
- Comprehensive port exposure for all services
- Volume mounting for configuration and certificate management

#### 📊 Performance Improvements

- **QUIC Transport**: 0-RTT handshake for faster connections with connection migration
- **gRPC Transport**: HTTP/2 multiplexing for better throughput and reduced latency
- **WebSocket Transport**: High-performance async writing with connection pooling
- **Connection Pooling**: Efficient resource utilization with automatic cleanup
- **Memory Optimization**: Reduced memory footprint with intelligent garbage collection

### 🔧 Technical Implementation

#### Build System
- Go 1.23+ requirement with modern toolchain support
- Cross-platform builds (Linux, macOS, Windows) with automated CI/CD
- GitHub Actions with comprehensive test coverage and security scanning
- Docker multi-arch builds with BuildKit optimization
- Automated release management with checksums and signatures

#### Code Structure
- Modern Go module organization with clean dependency management
- Clean package structure under `pkg/` with clear separation of concerns
- Optimized import paths and module organization
- Well-structured dependency management with vulnerability scanning
- Comprehensive test coverage with race condition detection

#### Protocol Implementation
- **WebSocket**: Gorilla WebSocket with custom high-performance writer
- **gRPC**: Protocol Buffers with bidirectional streaming and keepalive
- **QUIC**: quic-go library with custom message framing and authentication
- **TUIC**: Full TUIC v0.05 implementation with fragmentation and reassembly

### 🛡️ Reliability Features

- Robust connection management for long-running sessions
- Comprehensive error handling for network failures and edge cases
- Thread-safe concurrent connection handling with proper synchronization
- Enhanced logging system with structured output and log rotation
- Graceful shutdown handling with connection draining
- Memory leak prevention with automatic resource cleanup
- Heartbeat mechanisms for connection health monitoring

### 📚 Documentation

- Comprehensive README with practical examples and use cases
- Clear architecture documentation with detailed diagrams
- Complete Docker deployment guides with production recommendations
- Detailed configuration reference for all transport types and features
- API documentation for web interface integration
- Multi-language support documentation
- Troubleshooting guide with common issues and solutions

### 🌍 Internationalization

- Complete English and Chinese language support in web interface
- Persistent language preferences with browser storage
- Consistent terminology across all interfaces
- Cultural adaptation for date/time formatting and number display

---

## Version Support

### Current Version
- **v1.x**: Active development and support (Current stable)

### Usage
- **Docker**: Use `buhuipao/anyproxy:latest` or `buhuipao/anyproxy:v1.0.0`
- **Binaries**: Use `anyproxy-gateway` and `anyproxy-client`
- **Source**: Build from source with Go 1.23+

### System Requirements
- **Go**: 1.23+ (for building from source)
- **OS**: Linux (primary), macOS, Windows
- **Memory**: 50MB+ per instance (100MB+ recommended for web interface)
- **Network**: Internet connectivity for tunneling
- **Storage**: 100MB+ for logs and temporary files

### Port Usage
- **Proxy Ports**: 8080 (HTTP), 1080 (SOCKS5), 9443 (TUIC/UDP)
- **Transport Ports**: 8443 (WebSocket), 9090 (gRPC), 9091 (QUIC)
- **Management Ports**: 8090 (Gateway Web), 8091 (Client Web)

## Migration Guide

### From Earlier Versions
This is the first stable release. For beta users:
1. Update configuration format to YAML
2. Enable web interface if desired
3. Review security settings (allowed/forbidden hosts)
4. Update Docker images to use new tag

## Contributing

We welcome contributions! Please:
1. Read our [Contributing Guidelines](CONTRIBUTING.md)
2. Check existing [issues](https://github.com/buhuipao/anyproxy/issues)
3. Follow our code style and testing requirements
4. Submit pull requests with clear descriptions
5. Include tests for new features
6. Update documentation as needed

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

## Support

- 📖 **Documentation**: [README.md](README.md)
- 🐛 **Issues**: [GitHub Issues](https://github.com/buhuipao/anyproxy/issues)
- 💬 **Discussions**: [GitHub Discussions](https://github.com/buhuipao/anyproxy/discussions)
- 📧 **Contact**: Create an issue for support requests
- 🌐 **Web Demo**: Try the web interface with our examples 