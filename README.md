# AnyProxy

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Go Version](https://img.shields.io/badge/Go-1.23+-blue.svg)](https://golang.org)
[![Ask DeepWiki](https://deepwiki.com/badge.svg)](https://deepwiki.com/buhuipao/anyproxy)
[![Build Status](https://img.shields.io/badge/Build-Passing-green.svg)]()
[![Release](https://img.shields.io/github/v/release/buhuipao/anyproxy)](https://github.com/buhuipao/anyproxy/releases)

AnyProxy is a secure tunneling solution that enables you to expose local services to the internet through multiple transport protocols. Built with a modular architecture supporting WebSocket, gRPC, and QUIC transports with end-to-end TLS encryption.

## 📑 Table of Contents

- [Key Features](#-key-features)
- [Architecture Overview](#️-architecture-overview)
- [Quick Start](#-quick-start)
- [Common Use Cases](#-common-use-cases)
- [Configuration](#️-configuration)
- [Web Management Interface](#-web-management-interface)
- [Docker Deployment](#-docker-deployment)
- [Security](#-security)
- [Troubleshooting](#-troubleshooting)
- [Integration](#-integration)
- [Contributing](#-contributing)
- [License](#-license)

## ✨ Key Features

- 🔄 **Multiple Transport Protocols**: Choose between WebSocket, gRPC, or QUIC
- 🔐 **End-to-End TLS Encryption**: Secure communication for all protocols  
- 🚀 **Triple Proxy Support**: HTTP/HTTPS, SOCKS5, and TUIC proxies
  - **HTTP Proxy**: Standard web browsing and API access
  - **SOCKS5 Proxy**: Universal protocol support with low overhead  
  - **TUIC Proxy**: Ultra-low latency UDP-based proxy with 0-RTT handshake
- 🎯 **Group-Based Routing**: Route traffic to specific client groups
- ⚡ **Port Forwarding**: Direct port mapping for services
- 🌐 **Cross-Platform**: Linux, macOS, Windows support
- 🐳 **Container Ready**: Official Docker images available
- 🖥️ **Web Management Interface**: Real-time monitoring and configuration
- ⚙️ **Rate Limiting**: Per-client and global traffic control
- 🌍 **Multi-Language Support**: English and Chinese web interface
- 📊 **Real-time Monitoring**: Connection metrics and performance analytics

## 🏗️ Architecture Overview

### System Architecture

```
Internet Users                       Public Gateway Server                   Private Networks
     │                                       │                                     │
     │ ◄─── HTTP/SOCKS5/TUIC Proxy ──► ┌─────────────┐ ◄─── TLS Tunnels ───► ┌──────────────┐
     │                                 │   Gateway   │                       │   Clients    │
     │                                 │             │                       │              │
     │                                 │ • HTTP:8080 │                       │ • SSH Server │
     │                                 │ • SOCKS:1080│                       │ • Web Apps   │
     │                                 │ • TUIC:9443 │                       │ • Databases  │
     │                                 │ • Web:8090  │                       │ • AI Models  │
     │                                 │             │                       │ • Web:8091   │
     │                                 │ Transports: │                       │              │
     │                                 │ • WS:8443   │                       │              │
     │                                 │ • gRPC:9090 │                       │              │
     │                                 │ • QUIC:9091 │                       │              │
     │                                 └─────────────┘                       └──────────────┘
     │                                        │                                     │
SSH, Web, AI ←────────────────── Secure Proxy Connection ──────────────→ Local Services
```

### Transport Protocols Comparison

| Transport | Best For | Key Benefits | Port |
|-----------|----------|--------------|------|
| **WebSocket** | Firewall compatibility | • Works through most firewalls<br>• HTTP/HTTPS compatible<br>• Wide browser support | 8443 |
| **gRPC** | High performance | • HTTP/2 multiplexing<br>• Efficient binary protocol<br>• Built-in load balancing | 9090 |
| **QUIC** | Mobile/unreliable networks | • Ultra-low latency<br>• 0-RTT handshake<br>• Connection migration | 9091 |

### Proxy Protocols Comparison

| Protocol | Type | Best For | Key Features | Port |
|----------|------|----------|--------------|------|
| **HTTP** | TCP | Web browsing, API calls | • Standard HTTP CONNECT<br>• Compatible with all browsers<br>• Simple authentication | 8080 |
| **SOCKS5** | TCP | General purpose | • Protocol agnostic<br>• Low overhead<br>• Wide client support | 1080 |
| **TUIC** | UDP | Gaming, real-time apps | • 0-RTT connection setup<br>• Built-in multiplexing<br>• Connection migration<br>• TLS 1.3 required | 9443 |

**Note**: Each Gateway/Client instance uses only ONE transport protocol.

### Group-Based Routing Architecture

```
                              Gateway Server
                          ┌─────────────────────┐
  User Requests           │   Route by Group    │           Client Groups
       │                  │                     │                │
       ├─ user.prod ────► │  ┌─────────────┐    │ ────► ┌─────────────────┐
       │                  │  │  Prod Group │    │       │  Production Env │
       │                  │  │   Router    │    │       │ • prod-api.com  │
       │                  │  └─────────────┘    │       │ • prod-db:5432  │
       │                  │                     │       └─────────────────┘
       ├─ user.staging ──►│  ┌─────────────┐    │ ────► ┌─────────────────┐
       │                  │  │ Staging     │    │       │  Staging Env    │
       │                  │  │  Router     │    │       │ • staging-api   │
       │                  │  └─────────────┘    │       │ • staging-db    │
       │                  │                     │       └─────────────────┘
       └─ user.dev ──────►│  ┌─────────────┐    │ ────► ┌─────────────────┐
                          │  │   Dev       │    │       │  Development    │
                          │  │  Router     │    │       │ • localhost:*   │
                          │  └─────────────┘    │       │ • dev-services  │
                          └─────────────────────┘       └─────────────────┘

Usage Examples:
• curl -x http://user.prod:pass@gateway:8080 https://prod-api.com
• curl -x http://user.staging:pass@gateway:8080 https://staging-api.com  
• curl -x http://user.dev:pass@gateway:8080 http://localhost:3000
```

### Port Forward Architecture

```
Internet Access                Gateway Server               Target Services
      │                    ┌──────────────────┐                   │
      │                    │  Port Mappings   │                   │
      ├─ SSH :2222 ──────► │ 2222 → Client A  │─────────────► SSH:22
      │                    │                  │                   │
      ├─ HTTP :8000 ─────► │ 8000 → Client B  │─────────────► Web:80
      │                    │                  │                   │ 
      ├─ DB :5432 ───────► │ 5432 → Client C  │─────────────► PostgreSQL :5432
      │                    │                  │                   │
      └─ API :3000 ──────► │ 3000 → Client D  │─────────────► API Server :3000
                           └──────────────────┘

Configuration Example:
open_ports:
  - remote_port: 2222    # Gateway listens on :2222
    local_port: 22       # Forward to client's SSH :22
    local_host: "localhost"
    protocol: "tcp"
    
  - remote_port: 5432    # Gateway listens on :5432  
    local_port: 5432     # Forward to internal database
    local_host: "database.internal"
    protocol: "tcp"

Access: ssh -p 2222 user@gateway.example.com
       psql -h gateway.example.com -p 5432 mydb
```

## 🚀 Quick Start

### Prerequisites

- **Public Server** for Gateway deployment (with public IP)
- **Private Network** with services you want to expose
- Docker installed on both environments

### Step 1: Deploy Gateway (Public Server)

```bash
# On your public server (e.g., VPS, Cloud instance)
mkdir anyproxy-gateway && cd anyproxy-gateway
mkdir -p configs certs logs
```

**Gateway Configuration:**
```bash
cat > configs/gateway.yaml << 'EOF'
log:
  level: "info"
  format: "json"
  output: "file"
  file: "logs/gateway.log"

transport:
  type: "websocket"  # Choose: websocket, grpc, or quic

proxy:
  http:
    listen_addr: ":8080"
    auth_username: "proxy_user"
    auth_password: "secure_proxy_password"
  socks5:
    listen_addr: ":1080"
    auth_username: "socks_user"
    auth_password: "secure_socks_password"
  tuic:
    listen_addr: ":9443"
    token: "your-tuic-token-here"
    uuid: "12345678-1234-5678-9abc-123456789abc"
    cert_file: "certs/server.crt"
    key_file: "certs/server.key"

gateway:
  listen_addr: ":8443"  # WebSocket port (use :9090 for gRPC, :9091 for QUIC)
  tls_cert: "certs/server.crt"
  tls_key: "certs/server.key"
  auth_username: "gateway_admin"
  auth_password: "very_secure_gateway_password"
  web:
    enabled: true
    listen_addr: ":8090"
    auth_enabled: true
    auth_username: "admin"
    auth_password: "admin123"
EOF
```

**Generate Certificates & Start:**

> 💡 **Quick Start Tip**: The Docker image includes a pre-generated test certificate for immediate testing. You can skip certificate generation and use the built-in certificate for initial setup.

```bash
# Option 1: Use built-in test certificate (for quick testing)
docker run -d --name anyproxy-gateway \
  --restart unless-stopped \
  -p 8080:8080 -p 1080:1080 -p 9443:9443/udp -p 8443:8443 -p 8090:8090 \
  -v $(pwd)/configs:/app/configs:ro \
  -v $(pwd)/logs:/app/logs \
  buhuipao/anyproxy:latest ./anyproxy-gateway --config configs/gateway.yaml

# Option 2: Generate your own certificate (for production)
openssl req -x509 -newkey rsa:2048 -keyout certs/server.key -out certs/server.crt \
    -days 365 -nodes -subj "/CN=YOUR_PUBLIC_DOMAIN_OR_IP"

# Start with custom certificate
docker run -d --name anyproxy-gateway \
  --restart unless-stopped \
  -p 8080:8080 -p 1080:1080 -p 9443:9443/udp -p 8443:8443 -p 8090:8090 \
  -v $(pwd)/configs:/app/configs:ro \
  -v $(pwd)/certs:/app/certs:ro \
  -v $(pwd)/logs:/app/logs \
  buhuipao/anyproxy:latest ./anyproxy-gateway --config configs/gateway.yaml
```

### Step 2: Deploy Client (Private Network)

```bash
# On your private network machine
mkdir anyproxy-client && cd anyproxy-client
mkdir -p configs logs

# Copy server certificate from gateway (if using custom certificate)
# For testing with built-in certificate, you can skip this step
```

**Client Configuration:**
```bash
cat > configs/client.yaml << 'EOF'
log:
  level: "info"
  format: "json"
  output: "file"
  file: "logs/client.log"

transport:
  type: "websocket"  # Must match gateway transport

client:
  gateway_addr: "YOUR_PUBLIC_SERVER_IP:8443"  # Replace with your gateway IP
  gateway_tls_cert: "certs/server.crt"       # Use built-in cert or your own
  client_id: "home-client-001"
  group_id: "homelab"
  replicas: 1
  auth_username: "gateway_admin"
  auth_password: "very_secure_gateway_password"
  
  # Security: Only allow specific local services
  allowed_hosts:
    - "localhost:22"        # SSH
    - "localhost:80"        # Web server
    - "localhost:3000"      # Dev server
    
  # Block dangerous hosts
  forbidden_hosts:
    - "169.254.0.0/16"      # Cloud metadata
    - "127.0.0.1"           # Localhost
    - "0.0.0.0"
    
  # Optional: Port forwarding
  open_ports:
    - remote_port: 2222     # Gateway port
      local_port: 22        # Local SSH port
      local_host: "localhost"
      protocol: "tcp"
      
  web:
    enabled: true
    listen_addr: ":8091"
EOF
```

**Start Client:**
```bash
# Option 1: Use built-in test certificate (for quick testing)
docker run -d --name anyproxy-client \
  --restart unless-stopped \
  --network host \
  -v $(pwd)/configs:/app/configs:ro \
  -v $(pwd)/logs:/app/logs \
  buhuipao/anyproxy:latest ./anyproxy-client --config configs/client.yaml

# Option 2: Use custom certificate (for production)
docker run -d --name anyproxy-client \
  --restart unless-stopped \
  --network host \
  -v $(pwd)/configs:/app/configs:ro \
  -v $(pwd)/certs:/app/certs:ro \
  -v $(pwd)/logs:/app/logs \
  buhuipao/anyproxy:latest ./anyproxy-client --config configs/client.yaml
```

### Step 3: Test Connection

```bash
# Test HTTP proxy
curl -x http://proxy_user:secure_proxy_password@YOUR_PUBLIC_SERVER_IP:8080 \
  http://localhost:80

# Test SOCKS5 proxy
curl --socks5 socks_user:secure_socks_password@YOUR_PUBLIC_SERVER_IP:1080 \
  http://localhost:22

# Test TUIC proxy (requires TUIC-compatible client)
# Use TUIC client with: tuic://your-tuic-token@YOUR_PUBLIC_SERVER_IP:9443?uuid=12345678-1234-5678-9abc-123456789abc

# Test port forwarding (if configured)
ssh -p 2222 user@YOUR_PUBLIC_SERVER_IP
```

## 🎯 Common Use Cases

### 1. SSH Server Access

**Quick SSH Setup:**
```bash
# Gateway: Standard setup with SOCKS5
# Client: Allow SSH only
cat > configs/ssh-client.yaml << 'EOF'
transport:
  type: "websocket"

client:
  gateway_addr: "YOUR_GATEWAY_IP:8443"
  gateway_tls_cert: "certs/server.crt"
  client_id: "ssh-client"
  group_id: "ssh"
  replicas: 1
  auth_username: "gateway_admin"
  auth_password: "very_secure_gateway_password"
  allowed_hosts:
    - "localhost:22"
  forbidden_hosts:
    - "169.254.0.0/16"
EOF

# Connect via SSH
ssh -o "ProxyCommand=nc -X 5 -x socks_user:secure_socks_password@YOUR_GATEWAY_IP:1080 %h %p" user@localhost
```

### 2. Web Development

**Development Server Access:**
```bash
# Use QUIC for better performance
transport:
  type: "quic"

gateway:
  listen_addr: ":9091"  # QUIC port

client:
  gateway_addr: "YOUR_GATEWAY_IP:9091"
  allowed_hosts:
    - "localhost:*"
    - "127.0.0.1:*"

# Access local dev servers
curl -x http://proxy_user:secure_proxy_password@YOUR_GATEWAY_IP:8080 http://localhost:3000
```

### 3. Database Access

**Database Tunnel Setup:**
```bash
# Use port forwarding for direct database access
open_ports:
  - remote_port: 5432
    local_port: 5432
    local_host: "database.internal"
    protocol: "tcp"

# Connect directly
psql -h YOUR_GATEWAY_IP -p 5432 -U postgres mydb
```

### 4. TUIC Proxy (Ultra-Low Latency)

**TUIC Setup for 0-RTT Performance:**
```bash
# Gateway: Enable TUIC proxy in configuration
proxy:
  tuic:
    listen_addr: ":9443"
    token: "your-secure-token"
    uuid: "12345678-1234-5678-9abc-123456789abc"
    cert_file: "certs/server.crt"
    key_file: "certs/server.key"

# TUIC provides:
# - 0-RTT connection establishment
# - Built-in UDP and TCP multiplexing
# - Optimal for mobile networks
# - Enhanced connection migration

# Client usage (with TUIC-compatible clients):
# tuic://your-secure-token@YOUR_GATEWAY_IP:9443?uuid=12345678-1234-5678-9abc-123456789abc
```

**TUIC is ideal for:**
- **Gaming Applications**: Minimal latency for real-time gaming
- **Video Streaming**: Smooth streaming with connection migration
- **Mobile Networks**: Handles network switching seamlessly
- **IoT Devices**: Efficient for frequent short-lived connections
- **Real-time Communications**: VoIP, video calls, live chat

**Performance Benefits:**
- **0-RTT Handshake**: Connect instantly without round-trip delays
- **Connection Migration**: Maintain connections when switching networks
- **Multiplexing**: Multiple data streams over single UDP connection
- **TLS 1.3**: Modern encryption with perfect forward secrecy

## ⚙️ Configuration

### Transport Selection

**Choose ONE transport per Gateway/Client pair:**

```yaml
# WebSocket (Recommended for most cases)
transport:
  type: "websocket"
gateway:
  listen_addr: ":8443"

# gRPC (High performance)
transport:
  type: "grpc"
gateway:
  listen_addr: ":9090"

# QUIC (Mobile/unstable networks)
transport:
  type: "quic"
gateway:
  listen_addr: ":9091"
```

### TUIC Proxy Configuration

```yaml
proxy:
  tuic:
    listen_addr: ":9443"               # UDP port for TUIC
    token: "your-tuic-token"           # TUIC protocol token
    uuid: "12345678-1234-5678-9abc-123456789abc"  # TUIC client UUID
    cert_file: "certs/server.crt"      # TLS certificate (required)
    key_file: "certs/server.key"       # TLS private key (required)
```

**TUIC Protocol Features:**
- **0-RTT Handshake**: Ultra-fast connection establishment
- **UDP-Based**: Built on QUIC for optimal performance
- **TLS 1.3 Required**: Mandatory encryption
- **Multiplexing**: Multiple streams over single connection
- **Connection Migration**: Seamless network switching

### Security Configuration

```yaml
client:
  # Block dangerous hosts
  forbidden_hosts:
    - "169.254.0.0/16"      # Cloud metadata
    - "127.0.0.1"           # Localhost
    - "10.0.0.0/8"          # Private networks
    - "172.16.0.0/12"
    - "192.168.0.0/16"
  
  # Only allow specific services
  allowed_hosts:
    - "localhost:22"        # SSH only
    - "localhost:80"        # HTTP only
    - "localhost:443"       # HTTPS only
```

### Rate Limiting Configuration

```yaml
# Global rate limiting
rate_limiting:
  enabled: true
  global_limit: 1000      # requests per minute
  per_client_limit: 100   # requests per minute per client
  bandwidth_limit: 10     # MB/s per client
```

## 🖥️ Web Management Interface

AnyProxy provides comprehensive web-based management interfaces for both Gateway and Client components.

### Gateway Dashboard

**Access**: `http://YOUR_GATEWAY_IP:8090`
**Credentials**: admin / admin123

**Features:**
- 📊 **Real-time Metrics**: Active connections, data transfer, success rates
- 👥 **Client Management**: View all connected clients and their status
- 🌍 **Multi-language**: English/Chinese interface with one-click switching
- 🔄 **Auto-refresh**: Configurable real-time data updates
- 🔐 **Secure Authentication**: Session-based login system
- 📈 **Traffic Analytics**: Detailed connection and performance metrics

### Client Dashboard

**Access**: `http://YOUR_CLIENT_IP:8091`
**No authentication required**

**Features:**
- 🔍 **Connection Monitoring**: View all active proxy connections
- 📊 **Performance Metrics**: Data transfer and uptime statistics
- 🏥 **Health Status**: Gateway connectivity and local service checks
- ⚙️ **Configuration View**: Current client settings and port forwards

### API Endpoints

**Gateway API:**
- `GET /api/metrics/global` - Global system metrics
- `GET /api/metrics/clients` - All client statistics  
- `GET /api/metrics/connections` - Active connections
- `POST /api/auth/login` - Authentication
- `GET /api/ratelimit/config` - Rate limiting settings

**Client API:**
- `GET /api/status` - Client runtime status
- `GET /api/metrics/local` - Local performance metrics
- `GET /api/metrics/connections` - Client connections

## 🐳 Docker Deployment

> 💡 **Ready to Use**: The Docker image includes test certificates and web interface files. You can start testing immediately by only providing configuration files.

### Gateway (Public Server)
```yaml
# docker-compose.gateway.yml
version: '3.8'
services:
  anyproxy-gateway:
    image: buhuipao/anyproxy:latest
    container_name: anyproxy-gateway
    command: ./anyproxy-gateway --config configs/gateway.yaml
    ports:
      - "8080:8080"     # HTTP proxy
      - "1080:1080"     # SOCKS5 proxy
      - "9443:9443/udp" # TUIC proxy (UDP)
      - "8443:8443"     # WebSocket (or 9090 for gRPC, 9091 for QUIC)
      - "8090:8090"     # Web management interface
    volumes:
      - ./configs:/app/configs:ro
      # Optional: Override built-in test certificate with your own
      # - ./certs:/app/certs:ro
      - ./logs:/app/logs
    restart: unless-stopped
```

### Client (Private Network)
```yaml
# docker-compose.client.yml
version: '3.8'
services:
  anyproxy-client:
    image: buhuipao/anyproxy:latest
    container_name: anyproxy-client
    command: ./anyproxy-client --config configs/client.yaml
    ports:
      - "8091:8091"     # Web management interface
    volumes:
      - ./configs:/app/configs:ro
      # Optional: Override built-in test certificate with your own
      # - ./certs:/app/certs:ro
      - ./logs:/app/logs
    restart: unless-stopped
    network_mode: host
```

## 🔐 Security

### Certificate Management

> 💡 **Quick Testing**: The Docker image includes a pre-generated test certificate that works for localhost and common test scenarios. You can start testing immediately without generating your own certificates.

```bash
# For production - Generate your own certificate
openssl req -x509 -newkey rsa:2048 -keyout certs/server.key -out certs/server.crt \
    -days 365 -nodes -subj "/CN=YOUR_DOMAIN"

# Or use Let's Encrypt for production domains
certbot certonly --standalone -d gateway.yourdomain.com

# For testing - The Docker image includes:
# - Test certificate valid for localhost, 127.0.0.1, anyproxy
# - Allows immediate testing without certificate setup
# - Located at /app/certs/ inside the container
```

### Security Best Practices
- ✅ Use strong passwords for all authentication
- ✅ Restrict allowed hosts to specific services only
- ✅ Enable TLS for all transport protocols
- ✅ Regularly rotate certificates
- ✅ Monitor connection logs for suspicious activity
- ✅ Use firewall rules to restrict access to management ports

## 📊 Troubleshooting

### Basic Health Checks
```bash
# Check gateway connectivity
curl -x http://user:pass@gateway:8080 https://httpbin.org/ip

# Check TUIC proxy port (UDP)
nc -u -v gateway 9443

# Check web interfaces
curl http://gateway:8090/api/metrics/global
curl http://client:8091/api/status

# Check logs
docker logs anyproxy-gateway
docker logs anyproxy-client

# Test specific service
curl -x http://user:pass@gateway:8080 http://localhost:22
```

### Common Issues
- **Connection refused**: Check firewall and port configuration
- **Authentication failed**: Verify usernames and passwords in configs
- **Certificate errors**: Ensure certificate matches domain/IP
- **Transport mismatch**: Ensure Gateway and Client use the same transport type
- **Web interface 404**: Verify web.enabled is true and ports are accessible

## 🔗 Integration

### Python Example
```python
import requests

proxies = {
    'http': 'http://user:pass@gateway.com:8080',
    'https': 'http://user:pass@gateway.com:8080'
}

response = requests.get('http://localhost:8000/api', proxies=proxies)
print(response.json())
```

### cURL Example
```bash
# HTTP proxy
curl -x http://user:pass@gateway:8080 http://localhost:3000

# SOCKS5 proxy
curl --socks5 user:pass@gateway:1080 http://localhost:22
```

### Clash Configuration
```yaml
proxies:
  - name: "AnyProxy-HTTP"
    type: http
    server: YOUR_GATEWAY_IP
    port: 8080
    username: proxy_user
    password: secure_proxy_password

  - name: "AnyProxy-SOCKS5"
    type: socks5
    server: YOUR_GATEWAY_IP
    port: 1080
    username: socks_user
    password: secure_socks_password
```

## 📚 Quick Reference

**Default Ports:**
- HTTP Proxy: `8080`
- SOCKS5 Proxy: `1080`
- TUIC Proxy: `9443` (UDP)
- WebSocket: `8443`, gRPC: `9090`, QUIC: `9091`
- Gateway Web: `8090`, Client Web: `8091`

**Key Commands:**
```bash
# Start gateway
./anyproxy-gateway --config gateway.yaml

# Start client  
./anyproxy-client --config client.yaml

# Test connection
curl -x http://user:pass@gateway:8080 https://httpbin.org/ip

# Access web interface
open http://gateway:8090  # Gateway dashboard
open http://client:8091   # Client monitoring
```

## 🤝 Contributing

1. Fork the repository
2. Create your feature branch (`git checkout -b feature/AmazingFeature`)
3. Commit your changes (`git commit -m 'Add some AmazingFeature'`)
4. Push to the branch (`git push origin feature/AmazingFeature`)
5. Open a Pull Request

## 📄 License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

---

**Built with ❤️ by the AnyProxy team**

### Support & Community

- 🐛 **Issues**: [GitHub Issues](https://github.com/buhuipao/anyproxy/issues)
- 💬 **Discussions**: [GitHub Discussions](https://github.com/buhuipao/anyproxy/discussions)
- 📧 **Email**: chenhua22@outlook.com
- 🌟 **Star us** on GitHub if AnyProxy helps you!

---

*For the latest updates and releases, visit our [GitHub repository](https://github.com/buhuipao/anyproxy).*
