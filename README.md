# AnyProxy

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Go Version](https://img.shields.io/badge/Go-1.23+-blue.svg)](https://golang.org)
[![Ask DeepWiki](https://deepwiki.com/badge.svg)](https://deepwiki.com/buhuipao/anyproxy)
[![Build Status](https://img.shields.io/badge/Build-Passing-green.svg)]()
[![Release](https://img.shields.io/github/v/release/buhuipao/anyproxy)](https://github.com/buhuipao/anyproxy/releases)

AnyProxy is a modern secure tunneling solution that enables you to safely expose local services to the internet through multiple transport protocols. Built with integrated web management interfaces and intelligent monitoring systems.

## 🚀 30-Second Demo Experience

**Want to quickly experience AnyProxy?** Try our demo client:

```bash
# 1. Enter demo directory
cd demo

# 2. Start demo client (connects to our demo gateway)
# Demo includes pre-generated certificate files
docker run -d \
  --name anyproxy-demo-client \
  --network host \
  -v $(pwd)/configs:/app/configs:ro \
  -v $(pwd)/certs:/app/certs:ro \
  buhuipao/anyproxy:latest \
  ./anyproxy-client --config configs/client.yaml

# 3. Check running status
docker logs anyproxy-demo-client

# 4. Access Web Interface
# http://localhost:8091 (see config file for username/password)
```

**Test proxy connection:**
```bash
# Test with demo proxy (replace group_id with value from config)
curl -x http://your_group_id:your_password@47.107.181.88:8080 http://httpbin.org/ip
```

📖 **Complete Demo Guide**: See [demo/README.md](demo/README.md) for detailed instructions

## ✨ Key Features

### 🔄 Multiple Transport Protocols
- **WebSocket**: Firewall-friendly, HTTP/HTTPS compatible
- **gRPC**: HTTP/2 multiplexing, efficient binary protocol
- **QUIC**: Ultra-low latency, 0-RTT handshake, connection migration

### 🚀 Triple Proxy Support
- **HTTP Proxy**: Standard HTTP CONNECT, full browser compatibility
  - Optional HTTPS proxy mode for encrypted client connections
- **SOCKS5 Proxy**: Universal protocol support, low overhead
- **TUIC Proxy**: UDP-based ultra-low latency proxy, 0-RTT connection

### 🎯 Intelligent Routing & Security
- **Group Routing System**: Multi-environment routing based on `group_id`
- **Dynamic Authentication**: Clients authenticate using `group_id` and `group_password`
- **Host Access Control**: Precise allow/deny lists
- **End-to-End TLS**: Mandatory encryption for all transport protocols

### 🖥️ Web Management Interface
- **Gateway Dashboard**: Real-time monitoring, client management
- **Client Monitoring**: Local connection tracking, performance analytics
- **Multi-Language Support**: Complete English/Chinese bilingual interface

### 🔐 Group-Based Authentication & Load Balancing
- **Single Group ID**: Use group_id directly as proxy username (e.g., `prod-env:password`)
- **Round-Robin**: Automatic load distribution across clients in the same group
- **Zero-Config**: No complex username formats, just group_id and password
- **High Availability**: Seamless failover when clients disconnect
- **Persistent Credentials**: Optional file-based credential storage for production use

## 🏗️ System Architecture

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
     │                                 │             │                       │              │
     │                                 │ Transports: │                       │              │
     │                                 │ • WS:8443   │                       │              │
     │                                 │ • gRPC:9090 │                       │              │
     │                                 │ • QUIC:9091 │                       │              │
     │                                 └─────────────┘                       └──────────────┘
     │                                        │                                     │
SSH, Web, AI ←────────────────── Secure Proxy Connection ──────────────→ Local Services
```

### Group-Based Routing Principle

```
                              Gateway Server
                          ┌─────────────────────┐
  Proxy Auth Requests     │   Route by Group    │           Client Groups
       │                  │                     │                │
       ├─ prod:pass ────► │  ┌─────────────┐    │ ────► ┌─────────────────┐
       │                  │  │  Prod Group │    │       │  Production Env │
       │                  │  │   Router    │    │       │ • prod-api.com  │
       │                  │  └─────────────┘    │       │ • prod-db:5432  │
       │                  │                     │       └─────────────────┘
       ├─ staging:pass ──►│  ┌─────────────┐    │ ────► ┌─────────────────┐
       │                  │  │ Staging     │    │       │  Staging Env    │
       │                  │  │  Router     │    │       │ • staging-api   │
       │                  │  └─────────────┘    │       │ • staging-db    │
       │                  │                     │       └─────────────────┘
       └─ dev:pass ──────►│  ┌─────────────┐    │ ────► ┌─────────────────┐
                          │  │   Dev       │    │       │  Development    │
                          │  │  Router     │    │       │ • localhost:*   │
                          │  └─────────────┘    │       │ • dev-services  │
                          └─────────────────────┘       └─────────────────┘

⚠️ **Critical Authentication Rules**:
• **Proxy Authentication**: Use `group_id` as username, `group_password` as password
• **Routing Mechanism**: Gateway routes traffic to corresponding client group based on authenticated `group_id`
• **Each Client**: Registers with unique `group_id` and `group_password`
• **Password Consistency**: All clients with the same `group_id` must use identical `group_password`, or authentication will fail
```

## 📊 Protocol Comparison

| Protocol | Type | Best For | Port | Authentication |
|----------|------|----------|------|---------------|
| **HTTP** | TCP | Web browsing, API calls | 8080 | group_id/group_password |
| **SOCKS5** | TCP | General purpose | 1080 | group_id/group_password |
| **TUIC** | UDP | Gaming, real-time apps | 9443 | Dynamic Group Auth |

**Important Notes**: 
- Each Gateway/Client instance uses only ONE transport protocol
- TUIC protocol simplified: uses `group_id` as UUID, `group_password` as Token
- All proxy protocols authenticate directly using `group_id` for routing

## 🚀 Quick Start

### Core Concepts

AnyProxy operates on a **group authentication** model:
- **Gateway**: Provides proxy services, accepts client connections
- **Client**: Connects to gateway, provides access to internal services
- **Group Authentication**: Each client belongs to a group (`group_id`) and authenticates using group password (`group_password`)

### Requirements

- **Docker** (recommended) or **Go 1.23+**
- **Public Server** for Gateway deployment
- **TLS Certificates** (required for transport layer security)

### Quick Deployment

⚠️ **Important Notes**:
- **Certificates Required**: All Gateway and Client instances need TLS certificates for secure communication
- **Password Consistency**: All clients with the same `group_id` must use identical `group_password`
- **UDP Ports**: When using QUIC transport or TUIC proxy, Docker ports must be set as UDP type

**1. Start Gateway (Public Server):**
```bash
# Create directories and configuration
mkdir anyproxy-gateway && cd anyproxy-gateway
mkdir -p configs certs logs

# Generate TLS certificates (required step)
# Use your public IP or domain name
./scripts/generate_certs.sh YOUR_GATEWAY_IP
# or for domain: ./scripts/generate_certs.sh gateway.yourdomain.com

# Create gateway configuration
cat > configs/gateway.yaml << 'EOF'
gateway:
  listen_addr: ":8443"
  transport_type: "websocket"
  tls_cert: "certs/server.crt"
  tls_key: "certs/server.key"
  auth_username: "admin"
  auth_password: "secure_password"
  proxy:
    http:
      listen_addr: ":8080"
    socks5:
      listen_addr: ":1080"
    tuic:
      listen_addr: ":9443"
  web:
    enabled: true
    listen_addr: ":8090"
    static_dir: "web/gateway/static"
    auth_enabled: true
    auth_username: "web_admin"
    auth_password: "web_password"
    session_key: "change-this-secret-key"
EOF

# Start gateway (WebSocket transport)
docker run -d --name anyproxy-gateway \
  -p 8080:8080 -p 1080:1080 -p 9443:9443/udp -p 8443:8443 -p 8090:8090 \
  -v $(pwd)/configs:/app/configs:ro \
  -v $(pwd)/certs:/app/certs:ro \
  -v $(pwd)/logs:/app/logs \
  buhuipao/anyproxy:latest ./anyproxy-gateway --config configs/gateway.yaml

# If using QUIC transport, ports need to be set as UDP:
# docker run -d --name anyproxy-gateway \
#   -p 8080:8080 -p 1080:1080 -p 9443:9443/udp -p 9091:9091/udp -p 8090:8090 \
#   ...
```

**2. Start Client (Private Network):**
```bash
# Create directories and configuration
mkdir anyproxy-client && cd anyproxy-client
mkdir -p configs certs logs

# Copy certificate files from gateway server (required)
scp user@YOUR_GATEWAY_IP:/path/to/anyproxy-gateway/certs/server.crt ./certs/

# Create client configuration
cat > configs/client.yaml << 'EOF'
client:
  id: "home-client-001"
  group_id: "homelab"
  group_password: "my_secure_password"  # Ensure all clients in same group use identical password
  replicas: 1
  gateway:
    addr: "YOUR_GATEWAY_IP:8443"  # Replace with your gateway IP
    transport_type: "websocket"
    tls_cert: "certs/server.crt"
    auth_username: "admin"
    auth_password: "secure_password"
  
  # Allow only specific services
  allowed_hosts:
    - "localhost:22"        # SSH
    - "localhost:80"        # Web server
    - "localhost:3000"      # Dev server
    
  # Block dangerous hosts
  forbidden_hosts:
    - "169.254.0.0/16"      # Cloud metadata
    
  web:
    enabled: true
    listen_addr: ":8091"
    static_dir: "web/client/static"
    auth_enabled: true
    auth_username: "client_admin"
    auth_password: "client_password"
    session_key: "change-this-secret-key"
EOF

# Start client (must mount certificate directory)
docker run -d --name anyproxy-client \
  --network host \
  -v $(pwd)/configs:/app/configs:ro \
  -v $(pwd)/certs:/app/certs:ro \
  -v $(pwd)/logs:/app/logs \
  buhuipao/anyproxy:latest ./anyproxy-client --config configs/client.yaml
```

**3. Test Connection:**
```bash
# HTTP proxy example (using group_id authentication)
curl -x http://homelab:my_secure_password@YOUR_GATEWAY_IP:8080 http://localhost:80

# SOCKS5 proxy example
curl --socks5 homelab:my_secure_password@YOUR_GATEWAY_IP:1080 http://localhost:22

# SSH access
ssh -o "ProxyCommand=nc -X 5 -x homelab:my_secure_password@YOUR_GATEWAY_IP:1080 %h %p" user@localhost
```

## 🎯 Common Use Cases

### 1. HTTP Proxy (Web Browsing)

**Browser Setup:**
```
Proxy Server: YOUR_GATEWAY_IP
HTTP Port: 8080
Username: group_id          # e.g., homelab
Password: group_password    # Group password
```

### 2. SOCKS5 Proxy (Universal Protocol)

**SOCKS5 Configuration:**
```
Proxy Type: SOCKS5
Server: YOUR_GATEWAY_IP
Port: 1080
Username: group_id          # e.g., homelab
Password: group_password    # Group password
```

### 3. SSH Server Access

```bash
# Connect via SOCKS5 proxy
ssh -o "ProxyCommand=nc -X 5 -x group_id:group_password@YOUR_GATEWAY_IP:1080 %h %p" user@localhost

# Or configure SSH client
cat >> ~/.ssh/config << 'EOF'
Host tunnel-ssh
  HostName localhost
  User your_username
  Port 22
  ProxyCommand nc -X 5 -x group_id:group_password@YOUR_GATEWAY_IP:1080 %h %p
EOF

ssh tunnel-ssh
```

### 4. Port Forwarding

**Configure Port Forwarding:**
```yaml
client:
  open_ports:
    - remote_port: 2222     # Gateway port
      local_port: 22        # Local SSH port
      local_host: "localhost"
      protocol: "tcp"
      
    - remote_port: 8000     # Gateway port
      local_port: 80        # Local web port
      local_host: "localhost"
      protocol: "tcp"
```

**Use Port Forwarding:**
```bash
# Direct SSH connection
ssh -p 2222 user@YOUR_GATEWAY_IP

# Direct web access
curl http://YOUR_GATEWAY_IP:8000
```

## ⚙️ Configuration

### Transport Selection

```yaml
# WebSocket (Recommended, firewall-friendly)
gateway:
  listen_addr: ":8443"
  transport_type: "websocket"
  
# Docker ports: -p 8443:8443

# gRPC (High performance)
gateway:
  listen_addr: ":9090"
  transport_type: "grpc"
  
# Docker ports: -p 9090:9090

# QUIC (Mobile-optimized) ⚠️ Note: Requires UDP ports
gateway:
  listen_addr: ":9091"
  transport_type: "quic"
  
# Docker ports: -p 9091:9091/udp (note the /udp suffix)
```

### Security Configuration

```yaml
client:
  # Allowed hosts (whitelist)
  allowed_hosts:
    - "localhost:22"
    - "localhost:80"
    - "192.168.1.0/24:*"
    
  # Forbidden hosts (blacklist)
  forbidden_hosts:
    - "169.254.0.0/16"      # Cloud metadata
    - "127.0.0.1"           # Localhost
    - "10.0.0.0/8"          # Private networks
```

### HTTPS Proxy Configuration

To enable HTTPS proxy (where clients connect to the proxy using HTTPS), configure TLS certificates for the HTTP proxy:

```yaml
gateway:
  proxy:
    http:
      listen_addr: ":8080"
      tls_cert: "certs/http-proxy.crt"  # Optional: Enable HTTPS proxy
      tls_key: "certs/http-proxy.key"   # Optional: Enable HTTPS proxy
```

When HTTPS proxy is enabled:
- Clients must use `https://` scheme when connecting to the proxy
- Provides additional encryption between client and proxy server
- Example: `curl -x https://group_id:password@gateway:8080 https://target.com`

### Advanced Gateway Features

#### Credential Management

AnyProxy uses a simple key-value store for managing group credentials:

```yaml
gateway:
  credential:
    type: "file"                        # Options: "memory" (default) or "file"
    file_path: "credentials/groups.json" # Path to credential storage file
```

**Memory Storage (Default)**:
- Fast, in-memory key-value storage
- Credentials lost on restart
- Ideal for development/testing

**File Storage**:
- Persistent JSON storage
- SHA256 password hashing
- Thread-safe operations
- Automatic file management
- Ideal for production

Example credential file structure (simple JSON map):
```json
{
  "prod-group": "5e884898da28047151d0e56f8dc6292773603d0d6aabbdd62a11ef721d1542d8",
  "dev-group": "6b86b273ff34fce19d6b804eff5a3f5747ada4eaa22f1d49c01e52ddb7875b4b"
}
```

### Certificate Generation

```bash
# Use the project's script to generate certificates
./scripts/generate_certs.sh YOUR_GATEWAY_IP

# Or use domain name
./scripts/generate_certs.sh gateway.yourdomain.com

# Certificate files will be generated in certs/ directory:
# - certs/server.crt (certificate file)
# - certs/server.key (private key file)
```

## 🖥️ Web Management Interface

### Gateway Dashboard
- **Access**: `http://YOUR_GATEWAY_IP:8090`
- **Authentication**: Use `gateway.web.auth_username` and `gateway.web.auth_password` from config file
- **Features**: Real-time monitoring, client management, connection statistics

### Client Monitoring Interface
- **Access**: `http://CLIENT_IP:8091`
- **Authentication**: Use `client.web.auth_username` and `client.web.auth_password` from config file
- **Features**: Local connection monitoring, performance analytics

## 🔧 Troubleshooting

### Common Issues

**1. Connection Refused**
- Check if `group_id` and `group_password` match between gateway and client
- Verify ports are open
- Check TLS certificate configuration

**2. Proxy Authentication Failed**
- Ensure you're using `group_id` as username and `group_password` as password
- Verify client is connected to gateway
- **Ensure all clients with the same `group_id` use identical `group_password`**

**3. Cannot Access Services**
- Check `allowed_hosts` configuration
- Ensure target service is not in `forbidden_hosts` list

**4. Certificate Errors**
- Ensure certificate files are properly mounted to containers
- Verify certificate domain/IP matches actual access address
- Check certificate file permissions

**5. QUIC/TUIC Connection Issues**
- Ensure Docker ports are set as UDP type (`-p 9091:9091/udp`)
- Check firewall allows UDP traffic

### View Logs

```bash
# View gateway logs
docker logs anyproxy-gateway

# View client logs
docker logs anyproxy-client

# Or view file logs
tail -f logs/gateway.log
tail -f logs/client.log
```

## 📝 License

MIT License - see [LICENSE](LICENSE) file for details

## 🤝 Contributing

Issues and Pull Requests are welcome!

---

**Quick Links**:
- [30-Second Demo Experience](demo/)
- [Complete Configuration Example](examples/complete-config.yaml)
- [GitHub Issues](https://github.com/buhuipao/anyproxy/issues)
- [Releases](https://github.com/buhuipao/anyproxy/releases)
