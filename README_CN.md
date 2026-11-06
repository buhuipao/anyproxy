# AnyProxy

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Go Version](https://img.shields.io/badge/Go-1.25+-blue.svg)](https://golang.org)
[![Ask DeepWiki](https://deepwiki.com/badge.svg)](https://deepwiki.com/buhuipao/anyproxy)
[![Build Status](https://img.shields.io/badge/Build-Passing-green.svg)]()
[![Release](https://img.shields.io/github/v/release/buhuipao/anyproxy)](https://github.com/buhuipao/anyproxy/releases)

AnyProxy 是一个现代化的安全隧道解决方案，让你能够安全地将内网服务暴露到互联网。支持多种传输协议和代理类型，提供完整的 Web 管理界面。

## 🚀 30秒体验 Demo

**想快速体验 AnyProxy？** 使用我们的演示客户端：

```bash
# 1. 进入demo目录
cd demo

# 2. 启动演示客户端（连接到我们的演示网关）
# Demo 已包含预生成的证书文件
docker run -d \
  --name anyproxy-demo-client \
  --network host \
  -v $(pwd)/configs:/app/configs:ro \
  -v $(pwd)/certs:/app/certs:ro \
  buhuipao/anyproxy:latest \
  ./anyproxy-client --config configs/client.yaml

# 3. 查看运行状态
docker logs anyproxy-demo-client

# 4. 访问 Web 界面
# http://localhost:8091 （用户名密码见配置文件）
```

**测试代理连接：**
```bash
# 使用演示代理测试（替换 group_id 为配置中的值）
curl -x http://your_group_id:your_password@47.107.181.88:8080 http://httpbin.org/ip
```

📖 **完整 Demo 说明**: 查看 [demo/README.md](demo/README.md) 获取详细指南

## ✨ 核心特性

### 🔄 多种传输协议
- **WebSocket**: 防火墙友好，HTTP/HTTPS 兼容
- **gRPC**: HTTP/2 多路复用，高效二进制协议  
- **QUIC**: 超低延迟，0-RTT 握手，连接迁移

### 🚀 三种代理协议
- **HTTP 代理**: 标准 HTTP CONNECT，浏览器完全兼容
- **SOCKS5 代理**: 通用协议支持，低开销
- **TUIC 代理**: 基于 UDP 的超低延迟代理，0-RTT 连接

### 🎯 智能路由与安全
- **组路由系统**: 基于 `group_id` 的多环境路由支持
- **动态认证**: 客户端注册时使用 `group_id` 和 `group_password` 进行认证
- **主机访问控制**: 精确的允许/禁止列表
- **端到端 TLS**: 所有传输协议强制加密

### 🖥️ Web 管理界面
- **Gateway 仪表盘**: 实时监控，客户端管理
- **Client 监控**: 本地连接跟踪，性能分析
- **多语言支持**: 完整中英文双语界面

### 🔐 基于组的认证与负载均衡
- **单一组ID**：直接使用 group_id 作为代理用户名（如：`prod-env:password`）
- **轮询调度**：自动在同组客户端间分配负载
- **零配置**：无需复杂的用户名格式，仅需 group_id 和密码
- **高可用性**：客户端断开时无缝故障转移
- **持久化凭证**：可选的基于文件或数据库的凭证存储，适用于生产环境

## 🏗️ 系统架构

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

### 分组路由原理

```
                              网关服务器
                          ┌─────────────────────┐
  代理认证请求             │   按组路由            │           客户端分组
       │                  │                     │                │
       ├─ prod:pass ────► │  ┌─────────────┐    │ ────► ┌─────────────────┐
       │                  │  │   生产组     │    │       │  生产环境        │
       │                  │  │   路由器     │    │       │ • prod-api.com  │
       │                  │  └─────────────┘    │       │ • prod-db:5432  │
       │                  │                     │       └─────────────────┘
       ├─ staging:pass ──►│  ┌─────────────┐    │ ────► ┌─────────────────┐
       │                  │  │  测试组      │    │       │  测试环境        │
       │                  │  │  路由器      │    │       │ • staging-api   │
       │                  │  └─────────────┘    │       │ • staging-db    │
       │                  │                     │       └─────────────────┘
       └─ dev:pass ──────►│  ┌─────────────┐    │ ────► ┌─────────────────┐
                          │  │   开发组     │    │       │  开发环境        │
                          │  │   路由器     │    │       │ • localhost:*   │
                          │  └─────────────┘    │       │ • dev-services  │
                          └─────────────────────┘       └─────────────────┘

⚠️ **关键认证规则**:
• **代理认证**: 使用 `group_id` 作为用户名，`group_password` 作为密码
• **路由机制**: 网关根据认证的 `group_id` 路由流量到对应客户端组
• **每个客户端**: 注册时指定唯一的 `group_id` 和 `group_password`
• **密码一致性**: 同一个 `group_id` 的所有客户端必须使用相同的 `group_password`，密码与已注册密码不同的客户端会被网关拒绝
```

## 📊 协议对比

| 协议 | 类型 | 最适场景 | 端口 | 认证方式 |
|------|------|----------|------|---------|
| **HTTP** | TCP | Web 浏览、API 调用 | 8080 | group_id/group_password |
| **SOCKS5** | TCP | 通用代理 | 1080 | group_id/group_password |
| **TUIC** | UDP | 游戏、实时应用 | 9443 | 动态组认证 |

**重要说明**: 
- 每个 Gateway/Client 实例只使用一种传输协议
- TUIC 协议简化：使用 `group_id` 作为 UUID，`group_password` 作为 Token
- 所有代理协议都直接使用 `group_id` 进行认证和路由
- SOCKS5 UDP ASSOCIATE：UDP 数据报必须来自与 SOCKS5 TCP 控制连接相同的 IP（RFC 1928）。如果 TCP 和 UDP 经 NAT 后的 IP 不同（例如使用不同的负载均衡器，或 Kubernetes `externalTrafficPolicy: Cluster`），数据报会被丢弃
- HTTP CONNECT 和 SOCKS5 CONNECT 隧道在任一端关闭时立即关闭，隧道不支持半关闭。先半关闭再等待响应的客户端（例如 `nc -N` 或 `socat`）收不到响应

## 🚀 快速开始

### 基本概念

AnyProxy 基于**组认证**模式工作：
- **Gateway（网关）**: 提供代理服务，接受客户端连接
- **Client（客户端）**: 连接到网关，提供内网服务访问
- **组认证**: 每个客户端属于一个组（`group_id`），使用组密码（`group_password`）认证

### 环境要求

- **Docker** (推荐) 或 **Go 1.25+**
- **公网服务器** 用于部署 Gateway
- **TLS 证书** (必需，为了传输层安全)

### 快速部署

⚠️ **重要提醒**:
- **证书必需**: 所有 Gateway 和 Client 都需要 TLS 证书进行安全通信
- **密码一致**: 同一个 `group_id` 的所有客户端必须使用相同的 `group_password`
- **UDP 端口**: 使用 QUIC 传输或 TUIC 代理时，Docker 端口必须设置为 UDP 类型

**1. 启动网关（公网服务器）:**
```bash
# 创建目录和配置
mkdir anyproxy-gateway && cd anyproxy-gateway
mkdir -p configs certs logs

# 生成 TLS 证书（必需步骤）
# 使用你的公网 IP 或域名
./scripts/generate_certs.sh YOUR_GATEWAY_IP
# 或使用域名: ./scripts/generate_certs.sh gateway.yourdomain.com

# 创建网关配置
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

# 启动网关（WebSocket 传输）
docker run -d --name anyproxy-gateway \
  -p 8080:8080 -p 1080:1080 -p 9443:9443/udp -p 8443:8443 -p 8090:8090 \
  -v $(pwd)/configs:/app/configs:ro \
  -v $(pwd)/certs:/app/certs:ro \
  -v $(pwd)/logs:/app/logs \
  buhuipao/anyproxy:latest ./anyproxy-gateway --config configs/gateway.yaml

# 如果使用 QUIC 传输，端口需要设置为 UDP:
# docker run -d --name anyproxy-gateway \
#   -p 8080:8080 -p 1080:1080 -p 9443:9443/udp -p 9091:9091/udp -p 8090:8090 \
#   ...
```

**2. 启动客户端（内网服务器）:**
```bash
# 创建目录和配置
mkdir anyproxy-client && cd anyproxy-client
mkdir -p configs certs logs

# 从网关服务器复制证书文件（必需）
scp user@YOUR_GATEWAY_IP:/path/to/anyproxy-gateway/certs/server.crt ./certs/

# 创建客户端配置
cat > configs/client.yaml << 'EOF'
client:
  id: "home-client-001"
  group_id: "homelab"
  group_password: "my_secure_password"  # 确保同组所有客户端密码相同
  replicas: 1
  gateway:
    addr: "YOUR_GATEWAY_IP:8443"  # 替换为你的网关 IP
    transport_type: "websocket"
    tls_cert: "certs/server.crt"
    auth_username: "admin"
    auth_password: "secure_password"
  
  # 只允许特定服务
  allowed_hosts:
    - "localhost:22"        # SSH
    - "localhost:80"        # Web 服务器
    - "localhost:3000"      # 开发服务器
    
  # 阻止危险主机
  forbidden_hosts:
    - "169.254.0.0/16"      # 云元数据
    - "fd00:ec2::254"       # IPv6 云元数据
      
  web:
    enabled: true
    listen_addr: ":8091"
    static_dir: "web/client/static"
    auth_enabled: true
    auth_username: "client_admin"
    auth_password: "client_password"
    session_key: "change-this-secret-key"
EOF

# 启动客户端（必须挂载证书目录）
docker run -d --name anyproxy-client \
  --network host \
  -v $(pwd)/configs:/app/configs:ro \
  -v $(pwd)/certs:/app/certs:ro \
  -v $(pwd)/logs:/app/logs \
  buhuipao/anyproxy:latest ./anyproxy-client --config configs/client.yaml
```

**3. 测试连接:**
```bash
# HTTP 代理示例（使用 group_id 认证）
curl -x http://homelab:my_secure_password@YOUR_GATEWAY_IP:8080 http://localhost:80

# SOCKS5 代理示例
curl --socks5 homelab:my_secure_password@YOUR_GATEWAY_IP:1080 http://localhost:22

# SSH 访问
ssh -o "ProxyCommand=nc -X 5 -x homelab:my_secure_password@YOUR_GATEWAY_IP:1080 %h %p" user@localhost
```

## 🎯 常见用法

### 1. HTTP 代理（Web 浏览）

**浏览器设置:**
```
代理服务器: YOUR_GATEWAY_IP
HTTP 端口: 8080
用户名: group_id          # 例如：homelab
密码: group_password      # 组密码
```

### 2. SOCKS5 代理（通用协议）

**SOCKS5 配置:**
```
代理类型: SOCKS5
服务器: YOUR_GATEWAY_IP
端口: 1080
用户名: group_id          # 例如：homelab  
密码: group_password      # 组密码
```

### 3. SSH 服务器访问

```bash
# 通过 SOCKS5 代理连接 SSH
ssh -o "ProxyCommand=nc -X 5 -x group_id:group_password@YOUR_GATEWAY_IP:1080 %h %p" user@localhost

# 或者配置 SSH 客户端
cat >> ~/.ssh/config << 'EOF'
Host tunnel-ssh
  HostName localhost
  User your_username
  Port 22
  ProxyCommand nc -X 5 -x group_id:group_password@YOUR_GATEWAY_IP:1080 %h %p
EOF

ssh tunnel-ssh
```

### 4. 端口转发

**配置端口转发:**
```yaml
client:
  open_ports:
    - remote_port: 2222     # 网关端口
      local_port: 22        # 本地 SSH 端口
      local_host: "localhost"
      protocol: "tcp"

    - remote_port: 8000     # 网关端口
      local_port: 80        # 本地 Web 端口
      local_host: "localhost"
      protocol: "tcp"
```

**使用端口转发:**
```bash
# 直接 SSH 连接
ssh -p 2222 user@YOUR_GATEWAY_IP

# 直接访问 Web 服务
curl http://YOUR_GATEWAY_IP:8000
```

## ⚙️ 配置说明

### 传输协议选择

```yaml
# WebSocket（推荐，防火墙友好）
gateway:
  listen_addr: ":8443"
  transport_type: "websocket"
  
# Docker 端口: -p 8443:8443

# gRPC（高性能）
gateway:
  listen_addr: ":9090"
  transport_type: "grpc"

# Docker 端口: -p 9090:9090

# QUIC（移动网络优化）⚠️ 注意：需要 UDP 端口
gateway:
  listen_addr: ":9091"
  transport_type: "quic"
  
# Docker 端口: -p 9091:9091/udp （注意 /udp 后缀）
```

### 安全配置

```yaml
client:
  # 允许的主机（白名单）；为空时允许所有未被禁止的目标
  allowed_hosts:
    - "localhost:22"
    - "localhost:80"
    - "192.168.1.0/24:*"
    
  # 同时拨号 IPv6 目标（默认 false）；开启前请先阅读下面的 IPv6 说明
  allow_ipv6: false
    
  # 禁止的主机（黑名单）；禁止规则始终优先
  forbidden_hosts:
    - "169.254.0.0/16"      # 云元数据服务
    - "10.0.0.0/8"          # 私有网络
    - "fe80::/10"           # IPv6 链路本地地址，用于 allow_ipv6: true
    - "fc00::/7"            # IPv6 私有网络，用于 allow_ipv6: true
```

规则形式：
- `example.com` 匹配该主机及其子域名（例如 `api.example.com`，但不包括 `evilexample.com`），任意端口
- `example.com:443` 和 `example.com:*` 只匹配该主机本身，不包括子域名：禁止 `internal.corp:*` 不会拦截 `db.internal.corp`，需要同时列出 `*.internal.corp:*` 才能覆盖子域名
- `*.example.com` 匹配子域名，任意端口；`*.example.com:443` 同时检查端口
- `127.0.0.1`、`127.0.0.1:22`、`10.0.0.0/8` 和 `10.0.0.0/8:22` 是 IP 规则；IPv6 的 IP 规则写作 `::1`（或 `[::1]`）、`[::1]:22`、`fc00::/7` 和 `fc00::/7:22`
- 含有 `\.`、`^`、`(` 等正则字符的规则是正则表达式，匹配 `host:port`

客户端对每个目标只解析一次，用所有解析出的地址检查 IP 规则，并且只拨号这些地址。禁止规则优先于允许规则：只要名称或任一解析地址命中禁止规则，目标就会被拒绝。例如禁止 `127.0.0.0/8` 也会拒绝 `localhost:22`，即使上面允许了它。名称和通配符规则只匹配请求中的名称，要屏蔽地址段请使用 IP 或 CIDR 规则。`::ffff:127.0.0.1` 这类 IPv4 映射地址按 `127.0.0.1` 检查。当 `allowed_hosts` 中有 IP 或 CIDR 规则时，没有被名称规则匹配的名称会先通过 DNS 解析，然后才能判断是否允许。

IPv6：默认（`allow_ipv6: false`）客户端只解析和拨号 IPv4，因此 `[2001:db8::1]:443` 这样的 IPv6 目标会失败，名称只拨号其 IPv4 地址。设置 `allow_ipv6: true` 后才会拨号 IPv6 目标。IPv4 规则不匹配 IPv6 地址：禁止 `127.0.0.0/8` 不会拦截 `[::1]:22`。开启前请同时禁止 `::1`、`::`、`fe80::/10`、`fc00::/7` 和 `fd00:ec2::254` 等 IPv6 地址段。开启后，`allowed_hosts` 中的 IP 规则必须匹配每个解析出的地址（包括 IPv6），因此同时有 IPv4 和 IPv6 地址的名称需要为两者各写一条允许规则。

### 证书生成

```bash
# 使用项目提供的脚本生成证书
./scripts/generate_certs.sh YOUR_GATEWAY_IP

# 或使用域名
./scripts/generate_certs.sh gateway.yourdomain.com

# 证书文件会生成在 certs/ 目录下：
# - certs/server.crt （证书文件）
# - certs/server.key （私钥文件）
```

### 高级网关功能

#### 凭证管理

AnyProxy 使用简单的键值存储来管理组凭证，支持多种存储后端：

```yaml
gateway:
  credential:
    type: "file"                        # 选项："memory"（默认）、"file" 或 "db"
    file_path: "credentials/groups.json" # 凭证存储文件路径（file 类型）
    
    # 数据库配置（db 类型）
    db:
      driver: "mysql"                   # 数据库驱动：mysql、postgres、sqlite
      data_source: "user:pass@tcp(localhost:3306)/anyproxy"
      table_name: "credentials"         # 可选，默认为 "credentials"
```

**内存存储（默认）**：
- 快速的内存键值存储
- 重启时凭证丢失
- 客户端必须提供 group_password，未提供的客户端会被拒绝
- 组内最后一个客户端断开时删除该组凭证
- 修改组密码时，先停止该组所有客户端，再用新密码启动
- 适合开发/测试

**文件存储**：
- 持久化 JSON 存储
- 存储原始组密码（不做哈希），注意保护文件权限
- 线程安全操作
- 自动文件管理
- 客户端必须提供 group_password（预先配置的组也一样），未提供的客户端会被拒绝
- 客户端的 group_password 与已存储的密码不同时会被拒绝
- 修改组密码时编辑该文件，再用新密码重启该组的客户端。每次校验都会重新读取文件，网关无需重启
- 组内客户端全部断开后凭证仍然保留
- 适合简单部署

**数据库存储**：
- 高可用、可扩展
- 支持 MySQL、PostgreSQL、SQLite
- 自动建表
- 使用预编译语句提升性能
- 适合生产集群
- 客户端必须提供 group_password（预先配置的组也一样），未提供的客户端会被拒绝
- 客户端的 group_password 与已存储的密码不同时会被拒绝
- 修改组密码时更新对应的行，再用新密码重启该组的客户端。每次校验都会读取该行，网关无需重启
- 组内客户端全部断开后凭证仍然保留

凭证文件结构示例（group_id 到原始 group_password 的简单 JSON 映射）：
```json
{
  "prod-group": "prod-secret",
  "dev-group": "dev-secret"
}
```

数据库凭证表结构（`password_hash` 列保存原始组密码而不是哈希，最长 64 个字符）：
```sql
CREATE TABLE IF NOT EXISTS credentials (
    group_id VARCHAR(255) PRIMARY KEY,
    password_hash VARCHAR(64) NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
```

#### 使用预配置凭证

使用文件或数据库存储时，可以预先配置凭证。这些组的客户端仍然必须把已存储的密码作为 group_password 发送。

> **升级说明**：旧版本允许文件/数据库存储中预先配置的组的客户端省略 group_password。现在这些客户端必须把 group_password 设置为已存储的密码，否则网关会拒绝连接。

**1. 预配置凭证（以 SQLite 为例）：**
```sql
-- 插入原始组密码；password_hash 列按原样保存
INSERT INTO credentials (group_id, password_hash) VALUES 
('prod-env', 'secret123');
```

**2. 客户端配置（与已存储的密码相同）：**
```yaml
client:
  id: "prod-client"
  group_id: "prod-env"
  group_password: "secret123"  # 必须与已存储的密码一致
```

**3. 代理认证使用同一个密码：**
```bash
curl -x http://prod-env:secret123@gateway:8080 http://example.com
```

## 🖥️ Web 管理界面

### Gateway 仪表盘
- **访问地址**: `http://YOUR_GATEWAY_IP:8090`
- **登录认证**: 使用配置文件中 `gateway.web.auth_username` 和 `gateway.web.auth_password`
- **平台 API**: `POST /api/credentials/register` 需要使用同一组用户名和密码进行 HTTP Basic Auth，即使 `auth_enabled` 为 false 也是如此。未配置时始终返回 401。其中的 `bandwidth_mbps` 字段会被保存，但暂未生效
- **功能**: 实时监控、客户端管理、连接统计

### Client 监控界面  
- **访问地址**: `http://CLIENT_IP:8091`
- **登录认证**: 使用配置文件中 `client.web.auth_username` 和 `client.web.auth_password`
- **功能**: 本地连接监控、性能分析

## 🔧 故障排除

### 常见问题

**1. 连接被拒绝**
- 检查网关和客户端的 `group_id` 和 `group_password` 是否匹配
- 确认端口是否开放
- 检查 TLS 证书配置

**2. 代理认证失败**
- 确保使用 `group_id` 作为用户名，`group_password` 作为密码
- 检查客户端是否已连接到网关
- **确保同一个 `group_id` 的所有客户端使用相同的 `group_password`**

**3. 无法访问某些服务**
- 检查 `allowed_hosts` 配置
- 确认目标服务在 `forbidden_hosts` 列表中

**4. 证书错误**
- 确保证书文件正确挂载到容器中
- 验证证书的域名/IP 与实际访问地址匹配
- 检查证书文件权限

**5. QUIC/TUIC 连接问题**
- 确保 Docker 端口设置为 UDP 类型（`-p 9091:9091/udp`）
- 检查防火墙是否允许 UDP 流量

**6. 用户停止读取后连接提前结束**
- 网关为每个连接最多排队约 7000 条用户尚未读取的消息。队列满且用户在 100ms 内没有读取任何数据时（例如播放器暂停或终端被挂起），网关会关闭这个连接，避免它拖住同一客户端的其他连接，并记录日志 `Message channel full, closing connection`
- 如果用户一直在读取，但速度比目标发送得慢，连接不会被关闭。队列满后，它的每条消息都可能让同一客户端的其他连接最多等待 100ms，这些连接只能跟随它的速度
- 用户会先收到部分已排队的数据，然后看到正常的连接结束。没有自带长度信息的协议（例如以关闭连接表示结束的 HTTP 响应）无法分辨数据被截断

### 日志查看

```bash
# 查看网关日志
docker logs anyproxy-gateway

# 查看客户端日志
docker logs anyproxy-client

# 或查看文件日志
tail -f logs/gateway.log
tail -f logs/client.log
```

## 📝 许可证

MIT License - 详见 [LICENSE](LICENSE) 文件

## 🤝 贡献

欢迎提交 Issue 和 Pull Request！

---

**快速链接**:
- [30秒体验 Demo](demo/)
- [完整配置示例](examples/complete-config.yaml)
- [GitHub Issues](https://github.com/buhuipao/anyproxy/issues)
- [版本发布](https://github.com/buhuipao/anyproxy/releases)