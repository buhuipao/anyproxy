# AnyProxy

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Go Version](https://img.shields.io/badge/Go-1.23+-blue.svg)](https://golang.org)
[![Ask DeepWiki](https://deepwiki.com/badge.svg)](https://deepwiki.com/buhuipao/anyproxy)
[![Build Status](https://img.shields.io/badge/Build-Passing-green.svg)]()
[![Release](https://img.shields.io/github/v/release/buhuipao/anyproxy)](https://github.com/buhuipao/anyproxy/releases)

AnyProxy 是一个安全的隧道解决方案，支持通过多种传输协议将本地服务暴露到互联网。采用模块化架构，支持 WebSocket、gRPC 和 QUIC 传输协议，并提供端到端 TLS 加密。

## 🚀 体验 Demo

**想在 30 秒内测试 AnyProxy？** 试试我们的演示客户端：

```bash
cd demo && docker run -d \
  --name anyproxy-demo-client \
  --network host \
  -v $(pwd)/configs:/app/configs:ro \
  -v $(pwd)/certs:/app/certs:ro \
  buhuipao/anyproxy:latest \
  ./anyproxy-client --config configs/client.yaml
```

🌐 **Web 界面**: http://localhost:8091 (admin / admin123)

📖 **完整 Demo 指南**: 查看 [demo/README.md](demo/README.md) 获取详细说明。

## 📑 目录

- [体验 Demo](#-体验-demo)
- [核心特性](#-核心特性)
- [架构概览](#️-架构概览)
- [快速开始](#-快速开始)
- [常见用例](#-常见用例)
- [配置说明](#️-配置说明)
- [Web 管理界面](#-web-管理界面)
- [Docker 部署](#-docker-部署)
- [安全特性](#-安全特性)
- [故障排除](#-故障排除)
- [集成示例](#-集成示例)
- [贡献指南](#-贡献指南)
- [开源许可](#-开源许可)

## ✨ 核心特性

- 🔄 **多种传输协议**: 支持 WebSocket、gRPC 或 QUIC 协议
- 🔐 **端到端 TLS 加密**: 所有协议都提供安全通信保障  
- 🚀 **三重代理支持**: 支持 HTTP/HTTPS、SOCKS5 和 TUIC 代理
  - **HTTP 代理**: 标准 Web 浏览和 API 访问
  - **SOCKS5 代理**: 通用协议支持，低开销  
  - **TUIC 代理**: 超低延迟 UDP 代理，支持 0-RTT 握手
- 🎯 **分组路由**: 将流量路由到特定的客户端组
- ⚡ **端口转发**: 服务的直接端口映射
- 🌐 **跨平台支持**: 支持 Linux、macOS、Windows
- 🐳 **容器就绪**: 提供官方 Docker 镜像
- 🖥️ **Web 管理界面**: 实时监控和配置管理
- ⚙️ **速率限制**: 客户端和全局流量控制
- 🌍 **多语言支持**: 中英文 Web 界面
- 📊 **实时监控**: 连接指标和性能分析

## 🏗️ 架构概览

### 系统架构

```
Internet 用户                            公网网关服务器                             私有网络
     │                                       │                                     │
     │ ◄─── HTTP/SOCKS5/TUIC 代理 ──►   ┌─────────────┐ ◄─── TLS 隧道  ───►   ┌──────────────┐
     │                                 │   Gateway   │                       │   Clients    │
     │                                 │             │                       │              │
     │                                 │ • HTTP:8080 │                       │ • SSH 服务器 │
     │                                 │ • SOCKS:1080│                       │ • Web 应用   │
     │                                 │ • TUIC:9443 │                       │ • 数据库     │
     │                                 │ • Web:8090  │                       │ • AI 模型    │
     │                                 │             │                       │ • Web:8091   │
     │                                 │ 传输层:      │                       │              │
     │                                 │ • WS:8443   │                       │              │
     │                                 │ • gRPC:9090 │                       │              │
     │                                 │ • QUIC:9091 │                       │              │
     │                                 └─────────────┘                       └──────────────┘
     │                                        │                                     │
SSH, Web, AI     ←──────────────────    安全代理连接        ──────────────→       本地服务
```

### 传输协议对比

| 传输协议 | 最适场景 | 核心优势 | 端口 |
|-----------|----------|--------------|------|
| **WebSocket** | 防火墙兼容性 | • 可穿越大多数防火墙<br>• 兼容 HTTP/HTTPS<br>• 广泛的浏览器支持 | 8443 |
| **gRPC** | 高性能 | • HTTP/2 多路复用<br>• 高效二进制协议<br>• 内置负载均衡 | 9090 |
| **QUIC** | 移动/不稳定网络 | • 超低延迟<br>• 0-RTT 握手<br>• 连接迁移 | 9091 |

### 代理协议对比

| 协议 | 类型 | 最适场景 | 核心特性 | 端口 |
|----------|------|----------|--------------|------|
| **HTTP** | TCP | Web 浏览、API 调用 | • 标准 HTTP CONNECT<br>• 兼容所有浏览器<br>• 简单认证 | 8080 |
| **SOCKS5** | TCP | 通用代理 | • 协议无关<br>• 低开销<br>• 广泛的客户端支持 | 1080 |
| **TUIC** | UDP | 游戏、实时应用 | • 0-RTT 连接建立<br>• 内置多路复用<br>• 连接迁移<br>• 需要 TLS 1.3 | 9443 |

**注意**: 每个 Gateway/Client 实例只使用一种传输协议。

### 分组路由架构

```
                              网关服务器
                          ┌─────────────────────┐
  用户请求                 │   按分组路由          │           客户端分组
       │                  │                     │                │
       ├─ user.prod ────► │  ┌─────────────┐    │ ────► ┌─────────────────┐
       │                  │  │   生产组     │    │       │  生产环境        │
       │                  │  │   路由器     │    │       │ • prod-api.com  │
       │                  │  └─────────────┘    │       │ • prod-db:5432  │
       │                  │                     │       └─────────────────┘
       ├─ user.staging ──►│  ┌─────────────┐    │ ────► ┌─────────────────┐
       │                  │  │  测试组      │    │       │  测试环境        │
       │                  │  │  路由器      │    │       │ • staging-api   │
       │                  │  └─────────────┘    │       │ • staging-db    │
       │                  │                     │       └─────────────────┘
       └─ user.dev ──────►│  ┌─────────────┐    │ ────► ┌──────────── ────┐
                          │  │   开发组     │    │       │  开发环境        │
                          │  │   路由器     │    │       │ • localhost:*   │
                          │  └─────────────┘    │       │ • dev-services  │
                          └─────────────────────┘       └─────────────────┘

使用示例:
• curl -x http://user.prod:pass@gateway:8080 https://prod-api.com
• curl -x http://user.staging:pass@gateway:8080 https://staging-api.com  
• curl -x http://user.dev:pass@gateway:8080 http://localhost:3000

⚠️ **关键分组路由规则**:
• **使用 group_id**: 代理认证中使用 `username.group_id` 格式
• **不使用 group_id**: 仅使用 `username` - 路由到默认分组
• **错误 group_id**: 无效的 group_id 也会路由到默认分组
• **缺少 group_id**: 没有 group_id 的客户端加入默认分组
```

### 端口转发架构

```
Internet 访问                网关服务器               目标服务
      │                    ┌──────────────────┐                   | 
      │                    │  端口映射         │                   │
      ├─ SSH :2222 ──────► │ 2222 → 客户端 A   │─────────────► SSH:22
      │                    │                  │                   │
      ├─ HTTP :8000 ─────► │ 8000 → 客户端 B   │─────────────► Web:80
      │                    │                  │                   │ 
      ├─ DB :5432 ───────► │ 5432 → 客户端 C   │─────────────► PostgreSQL :5432
      │                    │                  │                   │
      └─ API :3000 ──────► │ 3000 → 客户端 D   │─────────────► API 服务器 :3000
                           └──────────────────┘

配置示例:
open_ports:
  - remote_port: 2222    # 网关监听端口 :2222
    local_port: 22       # 转发到客户端 SSH :22
    local_host: "localhost"
    protocol: "tcp"
    
  - remote_port: 5432    # 网关监听端口 :5432  
    local_port: 5432     # 转发到内部数据库
    local_host: "database.internal"
    protocol: "tcp"

访问方式: ssh -p 2222 user@gateway.example.com
         psql -h gateway.example.com -p 5432 mydb
```

## 🚀 快速开始

### 前置要求

- **公网服务器** 用于部署 Gateway（需要公网 IP）
- **内网环境** 包含要暴露的服务
- 两个环境都安装 Docker

### 步骤 1: 部署 Gateway（公网服务器）

```bash
# 在你的公网服务器上（如 VPS、云实例）
mkdir anyproxy-gateway && cd anyproxy-gateway
mkdir -p configs certs logs
```

**Gateway 配置:**
```bash
cat > configs/gateway.yaml << 'EOF'
log:
  level: "info"
  format: "json"
  output: "file"
  file: "logs/gateway.log"

transport:
  type: "websocket"  # 选择: websocket, grpc, 或 quic

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
  listen_addr: ":8443"  # WebSocket 端口（gRPC 使用 :9090，QUIC 使用 :9091）
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

**生成证书并启动:**

> ⚠️ **证书重要提醒**: Docker 镜像包含的预生成测试证书**仅适用于 localhost 本地测试**。如果在远程服务器部署网关，**必须**使用提供的脚本生成包含正确 IP 地址或域名的证书。

```bash
# 远程网关部署: 使用公网 IP/域名生成证书（必需）
./scripts/generate_certs.sh YOUR_PUBLIC_IP
# 或使用域名:
./scripts/generate_certs.sh gateway.yourdomain.com

# 使用生成的证书启动网关
docker run -d --name anyproxy-gateway \
  --restart unless-stopped \
  -p 8080:8080 -p 1080:1080 -p 9443:9443/udp -p 8443:8443 -p 8090:8090 \
  -v $(pwd)/configs:/app/configs:ro \
  -v $(pwd)/certs:/app/certs:ro \
  -v $(pwd)/logs:/app/logs \
  buhuipao/anyproxy:latest ./anyproxy-gateway --config configs/gateway.yaml

# 仅限本地测试: 使用内置测试证书（仅适用于 localhost）
docker run -d --name anyproxy-gateway \
  --restart unless-stopped \
  -p 8080:8080 -p 1080:1080 -p 9443:9443/udp -p 8443:8443 -p 8090:8090 \
  -v $(pwd)/configs:/app/configs:ro \
  -v $(pwd)/logs:/app/logs \
  buhuipao/anyproxy:latest ./anyproxy-gateway --config configs/gateway.yaml
```

### 步骤 2: 部署 Client（内网环境）

```bash
# 在你的内网机器上
mkdir anyproxy-client && cd anyproxy-client
mkdir -p configs certs logs

# 从网关服务器复制服务器证书（远程网关必需）
# 远程网关: scp user@gateway-server:/path/to/anyproxy-gateway/certs/server.crt ./certs/
# 本地测试: 可以使用内置证书（跳过复制）
```

**Client 配置:**
```bash
cat > configs/client.yaml << 'EOF'
log:
  level: "info"
  format: "json"
  output: "file"
  file: "logs/client.log"

transport:
  type: "websocket"  # 必须与网关传输协议匹配

client:
  gateway_addr: "YOUR_PUBLIC_SERVER_IP:8443"  # 替换为你的网关 IP
  gateway_tls_cert: "certs/server.crt"       # 使用内置证书或自己的证书
  client_id: "home-client-001"
  group_id: "homelab"
  replicas: 1
  auth_username: "gateway_admin"
  auth_password: "very_secure_gateway_password"
  
  # 安全: 只允许特定的本地服务
  allowed_hosts:
    - "localhost:22"        # SSH
    - "localhost:80"        # Web 服务器
    - "localhost:3000"      # 开发服务器
    
  # 阻止危险主机
  forbidden_hosts:
    - "169.254.0.0/16"      # 云元数据
    - "127.0.0.1"           # 本地主机
    - "0.0.0.0"
    
  # 可选: 端口转发
  open_ports:
    - remote_port: 2222     # 网关端口
      local_port: 22        # 本地 SSH 端口
      local_host: "localhost"
      protocol: "tcp"
      
  web:
    enabled: true
    listen_addr: ":8091"
EOF
```

**启动 Client:**
```bash
# 远程网关: 使用网关服务器的证书（必需）
docker run -d --name anyproxy-client \
  --restart unless-stopped \
  --network host \
  -v $(pwd)/configs:/app/configs:ro \
  -v $(pwd)/certs:/app/certs:ro \
  -v $(pwd)/logs:/app/logs \
  buhuipao/anyproxy:latest ./anyproxy-client --config configs/client.yaml

# 仅限本地测试: 使用内置测试证书（仅适用于 localhost）
docker run -d --name anyproxy-client \
  --restart unless-stopped \
  --network host \
  -v $(pwd)/configs:/app/configs:ro \
  -v $(pwd)/logs:/app/logs \
  buhuipao/anyproxy:latest ./anyproxy-client --config configs/client.yaml
```

### 步骤 3: 测试连接

⚠️ **分组路由重要提醒**: 如果在客户端配置中设置了 `group_id`，你**必须**在代理认证中使用 `username.group_id` 格式。否则，流量将路由到默认分组。

```bash
# 测试 HTTP 代理（使用 group_id - 必需格式）
curl -x http://proxy_user.homelab:secure_proxy_password@YOUR_PUBLIC_SERVER_IP:8080 \
  http://localhost:80

# 测试 SOCKS5 代理（使用 group_id - 必需格式）
curl --socks5 socks_user.homelab:secure_socks_password@YOUR_PUBLIC_SERVER_IP:1080 \
  http://localhost:22

# 不使用 group_id（走默认分组）
curl -x http://proxy_user:secure_proxy_password@YOUR_PUBLIC_SERVER_IP:8080 \
  http://localhost:80

# 测试 TUIC 代理（需要兼容 TUIC 的客户端）
# 使用 TUIC 客户端: tuic://your-tuic-token@YOUR_PUBLIC_SERVER_IP:9443?uuid=12345678-1234-5678-9abc-123456789abc

# 测试端口转发（如果已配置）
ssh -p 2222 user@YOUR_PUBLIC_SERVER_IP
```

## 🎯 常见用例

### 1. SSH 服务器访问

**快速 SSH 设置:**
```bash
# Gateway: 使用 SOCKS5 的标准设置
# Client: 只允许 SSH
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

# 通过 SSH 连接（使用 group_id）
ssh -o "ProxyCommand=nc -X 5 -x socks_user.ssh:secure_socks_password@YOUR_GATEWAY_IP:1080 %h %p" user@localhost
```

### 2. Web 开发

**开发服务器访问:**
```bash
# 使用 QUIC 获得更好性能
transport:
  type: "quic"

gateway:
  listen_addr: ":9091"  # QUIC 端口

client:
  gateway_addr: "YOUR_GATEWAY_IP:9091"
  allowed_hosts:
    - "localhost:*"
    - "127.0.0.1:*"

# 访问本地开发服务器（将 'dev' 替换为你的 group_id）
curl -x http://proxy_user.dev:secure_proxy_password@YOUR_GATEWAY_IP:8080 http://localhost:3000
```

### 3. 数据库访问

**数据库隧道设置:**
```bash
# 使用端口转发进行直接数据库访问
open_ports:
  - remote_port: 5432
    local_port: 5432
    local_host: "database.internal"
    protocol: "tcp"

# 直接连接
psql -h YOUR_GATEWAY_IP -p 5432 -U postgres mydb
```

### 4. TUIC 代理（超低延迟）

**TUIC 0-RTT 性能设置:**
```bash
# Gateway: 在配置中启用 TUIC 代理
proxy:
  tuic:
    listen_addr: ":9443"
    token: "your-secure-token"
    uuid: "12345678-1234-5678-9abc-123456789abc"
    cert_file: "certs/server.crt"
    key_file: "certs/server.key"

# TUIC 提供:
# - 0-RTT 连接建立
# - 内置 UDP 和 TCP 多路复用
# - 移动网络优化
# - 增强的连接迁移

# 客户端使用（使用兼容 TUIC 的客户端）:
# tuic://your-secure-token@YOUR_GATEWAY_IP:9443?uuid=12345678-1234-5678-9abc-123456789abc
```

**TUIC 理想应用场景:**
- **游戏应用**: 实时游戏的最小延迟
- **视频流**: 支持连接迁移的流畅流媒体
- **移动网络**: 无缝处理网络切换
- **IoT 设备**: 频繁短连接的高效处理
- **实时通信**: VoIP、视频通话、实时聊天

**性能优势:**
- **0-RTT 握手**: 无需往返延迟即可连接
- **连接迁移**: 网络切换时保持连接
- **多路复用**: 单个 UDP 连接上的多个数据流
- **TLS 1.3**: 现代加密与完美前向保密

## ⚙️ 配置说明

### 传输协议选择

**每个 Gateway/Client 对选择一种传输协议:**

```yaml
# WebSocket（推荐用于大多数情况）
transport:
  type: "websocket"
gateway:
  listen_addr: ":8443"

# gRPC（高性能）
transport:
  type: "grpc"
gateway:
  listen_addr: ":9090"

# QUIC（移动/不稳定网络）
transport:
  type: "quic"
gateway:
  listen_addr: ":9091"
```

### TUIC 代理配置

```yaml
proxy:
  tuic:
    listen_addr: ":9443"               # TUIC 的 UDP 端口
    token: "your-tuic-token"           # TUIC 协议令牌
    uuid: "12345678-1234-5678-9abc-123456789abc"  # TUIC 客户端 UUID
    cert_file: "certs/server.crt"      # TLS 证书（必需）
    key_file: "certs/server.key"       # TLS 私钥（必需）
```

**TUIC 协议特性:**
- **0-RTT 握手**: 超快连接建立
- **基于 UDP**: 建立在 QUIC 之上以获得最佳性能
- **需要 TLS 1.3**: 强制加密
- **多路复用**: 单连接上的多个流
- **连接迁移**: 无缝网络切换

### 安全配置

```yaml
client:
  # 阻止危险主机
  forbidden_hosts:
    - "169.254.0.0/16"      # 云元数据
    - "127.0.0.1"           # 本地主机
    - "10.0.0.0/8"          # 私有网络
    - "172.16.0.0/12"
    - "192.168.0.0/16"
  
  # 只允许特定服务
  allowed_hosts:
    - "localhost:22"        # 仅 SSH
    - "localhost:80"        # 仅 HTTP
    - "localhost:443"       # 仅 HTTPS
```

### 速率限制配置

```yaml
# 全局速率限制
rate_limiting:
  enabled: true
  global_limit: 1000      # 每分钟请求数
  per_client_limit: 100   # 每客户端每分钟请求数
  bandwidth_limit: 10     # 每客户端 MB/s
```

## 🖥️ Web 管理界面

AnyProxy 提供全面的基于 Web 的管理界面，具有会话认证、实时监控和智能指标收集功能。

### Gateway 仪表盘

**访问地址**: `http://YOUR_GATEWAY_IP:8090`
**登录凭据**: admin / admin123

**功能特性:**
- 📊 **实时指标**: 活跃连接、数据传输、成功率，支持自动清理
- 👥 **客户端管理**: 查看所有连接的客户端及在线/离线检测
- 🌍 **多语言**: 完整的中英文双语界面，支持持久化偏好设置
- 🔄 **自动刷新**: 10 秒可配置实时数据更新
- 🔐 **会话认证**: 24 小时安全会话，支持自动续期
- 📈 **内存分析**: 轻量级指标，支持自动不一致检测

### Client 仪表盘

**访问地址**: `http://YOUR_CLIENT_IP:8091`
**认证**: 可选（可配置）

**功能特性:**
- 🔍 **连接监控**: 实时查看所有活跃代理连接
- 📊 **性能指标**: 数据传输统计和运行时间跟踪
- 🎯 **多客户端支持**: 从单一界面跟踪多个客户端实例
- ⚙️ **运行时信息**: 客户端状态、连接摘要和系统指标

### API 接口

**Gateway API:**
- `POST /api/auth/login` - 创建 24 小时认证会话
- `POST /api/auth/logout` - 销毁当前会话
- `GET /api/auth/check` - 验证认证状态
- `GET /api/metrics/global` - 全局系统指标（连接、数据传输、成功率）
- `GET /api/metrics/clients` - 所有客户端统计信息及在线/离线状态
- `GET /api/metrics/connections` - 活跃连接详情和流量指标

**Client API:**
- `POST /api/auth/login` - 用户登录（如果启用认证）
- `POST /api/auth/logout` - 用户登出（如果启用认证）
- `GET /api/auth/check` - 检查认证状态
- `GET /api/status` - 客户端运行状态及连接摘要
- `GET /api/metrics/connections` - 所有跟踪客户端实例的连接指标

## 🐳 Docker 部署

> 💡 **即用即试**: Docker 镜像包含测试证书和 Web 界面文件。你只需提供配置文件即可立即开始测试。

### Gateway（公网服务器）
```yaml
# docker-compose.gateway.yml
version: '3.8'
services:
  anyproxy-gateway:
    image: buhuipao/anyproxy:latest
    container_name: anyproxy-gateway
    command: ./anyproxy-gateway --config configs/gateway.yaml
    ports:
      - "8080:8080"     # HTTP 代理
      - "1080:1080"     # SOCKS5 代理
      - "9443:9443/udp" # TUIC 代理（UDP）
      - "8443:8443"     # WebSocket（gRPC 使用 9090，QUIC 使用 9091）
      - "8090:8090"     # Web 管理界面
    volumes:
      - ./configs:/app/configs:ro
      # 可选: 使用自己的证书覆盖内置测试证书
      # - ./certs:/app/certs:ro
      - ./logs:/app/logs
    restart: unless-stopped
```

### Client（内网环境）
```yaml
# docker-compose.client.yml
version: '3.8'
services:
  anyproxy-client:
    image: buhuipao/anyproxy:latest
    container_name: anyproxy-client
    command: ./anyproxy-client --config configs/client.yaml
    ports:
      - "8091:8091"     # Web 管理界面
    volumes:
      - ./configs:/app/configs:ro
      # 可选: 使用自己的证书覆盖内置测试证书
      # - ./certs:/app/certs:ro
      - ./logs:/app/logs
    restart: unless-stopped
    network_mode: host
```

## 🔐 安全特性

### 证书管理

> ⚠️ **关键证书信息**: Docker 镜像包含的预生成测试证书**仅适用于 localhost, 127.0.0.1, 和 anyproxy**。如果在远程服务器部署网关，**必须**生成包含正确 IP/域名的证书。

```bash
# 远程网关 - 使用提供的脚本（推荐）
./scripts/generate_certs.sh YOUR_GATEWAY_IP
# 或使用域名:
./scripts/generate_certs.sh gateway.yourdomain.com

# 手动生成证书（替代方法）
openssl req -x509 -newkey rsa:2048 -keyout certs/server.key -out certs/server.crt \
    -days 365 -nodes -subj "/CN=YOUR_DOMAIN" \
    -addext "subjectAltName = IP:YOUR_IP,DNS:YOUR_DOMAIN"

# 或使用 Let's Encrypt 为生产域名生成证书
certbot certonly --standalone -d gateway.yourdomain.com

# 内置测试证书限制:
# ❌ 不适用于远程 IP 地址
# ❌ 不适用于自定义域名
# ✅ 仅适用于: localhost, 127.0.0.1, anyproxy
# ✅ 仅用于本地开发/测试
```

### 安全最佳实践
- ✅ 对所有认证使用强密码
- ✅ 将允许的主机限制为特定服务
- ✅ 为所有传输协议启用 TLS
- ✅ 定期轮换证书
- ✅ 监控连接日志以发现可疑活动
- ✅ 使用防火墙规则限制对管理端口的访问

## 📊 故障排除

### 基本健康检查
```bash
# 检查网关连接性（使用 group_id）
curl -x http://user.mygroup:pass@gateway:8080 https://httpbin.org/ip

# 检查 TUIC 代理端口（UDP）
nc -u -v gateway 9443

# 检查 Web 界面
curl http://gateway:8090/api/metrics/global
curl http://client:8091/api/status

# 检查日志
docker logs anyproxy-gateway
docker logs anyproxy-client

# 测试特定服务（使用 group_id）
curl -x http://user.mygroup:pass@gateway:8080 http://localhost:22
```

### 常见问题
- **连接被拒绝**: 检查防火墙和端口配置
- **认证失败**: 验证配置中的用户名和密码
- **证书错误**: 确保证书与域名/IP 匹配
- **传输协议不匹配**: 确保 Gateway 和 Client 使用相同的传输协议
- **Web 界面 404**: 验证 web.enabled 为 true 且端口可访问

## 🔗 集成示例

### Python 示例
```python
import requests

# 使用 group_id（将 'mygroup' 替换为你的 group_id）
proxies = {
    'http': 'http://user.mygroup:pass@gateway.com:8080',
    'https': 'http://user.mygroup:pass@gateway.com:8080'
}

response = requests.get('http://localhost:8000/api', proxies=proxies)
print(response.json())

# 不使用 group_id（路由到默认分组）
proxies_default = {
    'http': 'http://user:pass@gateway.com:8080',
    'https': 'http://user:pass@gateway.com:8080'
}
```

### cURL 示例
```bash
# HTTP 代理（使用 group_id - 将 'mygroup' 替换为你的 group_id）
curl -x http://user.mygroup:pass@gateway:8080 http://localhost:3000

# SOCKS5 代理（使用 group_id）
curl --socks5 user.mygroup:pass@gateway:1080 http://localhost:22

# 不使用 group_id（路由到默认分组）
curl -x http://user:pass@gateway:8080 http://localhost:3000
```

### Clash 配置
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

## 📚 快速参考

**默认端口:**
- HTTP 代理: `8080`
- SOCKS5 代理: `1080`
- TUIC 代理: `9443` (UDP)
- WebSocket: `8443`, gRPC: `9090`, QUIC: `9091`
- Gateway Web: `8090`, Client Web: `8091`

**关键命令:**
```bash
# 启动网关
./anyproxy-gateway --config gateway.yaml

# 启动客户端
./anyproxy-client --config client.yaml

# 测试连接（使用 group_id - 将 'mygroup' 替换为你的 group_id）
curl -x http://user.mygroup:pass@gateway:8080 https://httpbin.org/ip

# 访问 Web 界面
open http://gateway:8090  # Gateway 仪表盘
open http://client:8091   # Client 监控
```

## 🤝 贡献指南

1. Fork 仓库
2. 创建你的功能分支 (`git checkout -b feature/AmazingFeature`)
3. 提交你的更改 (`git commit -m 'Add some AmazingFeature'`)
4. 推送到分支 (`git push origin feature/AmazingFeature`)
5. 开启 Pull Request

## 📄 开源许可

本项目基于 MIT 许可证 - 查看 [LICENSE](LICENSE) 文件了解详情。

---

**由 AnyProxy 团队用 ❤️ 构建**

### 支持与社区

- 🐛 **问题反馈**: [GitHub Issues](https://github.com/buhuipao/anyproxy/issues)
- 💬 **讨论交流**: [GitHub Discussions](https://github.com/buhuipao/anyproxy/discussions)
- 📧 **邮件联系**: chenhua22@outlook.com
- 🌟 如果 AnyProxy 对你有帮助，请在 GitHub 上 **Star** 支持我们！

---

*获取最新更新和发布，请访问我们的 [GitHub 仓库](https://github.com/buhuipao/anyproxy)。*