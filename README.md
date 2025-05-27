# AnyProxy

AnyProxy 是一个基于 WebSocket + TLS 的代理系统，允许开发者将本地服务安全地暴露给公网用户。

## 🚀 功能特性

- **安全连接**: 使用 TLS + WebSocket 建立安全的代理通道
- **双代理支持**: 同时支持 HTTP/HTTPS 和 SOCKS5 代理服务
- **HTTP/HTTPS 代理**: 支持标准 HTTP 代理协议，包括 CONNECT 方法用于 HTTPS 隧道
- **SOCKS5 代理**: 支持带认证的 SOCKS5 代理服务
- **透明代理**: 公网用户可以通过代理连接网关，访问内网服务
- **负载均衡**: 支持多客户端连接，自动负载均衡
- **访问控制**: 支持黑名单和白名单机制
- **服务限制**: 可配置允许访问的特定服务
- **独立配置**: 每种代理类型都有独立的监听地址和认证配置

## 📋 系统架构

```
公网用户 → HTTP/SOCKS5代理 → 网关(Gateway) → WebSocket+TLS → 客户端(Client) → 目标服务
```

1. **客户端(Client)**: 主动连接代理网关，建立 WebSocket + TLS 通道
2. **网关(Gateway)**: 接收公网用户的 HTTP/SOCKS5 请求，转发给随机客户端
3. **公网用户**: 通过 HTTP 或 SOCKS5 代理连接网关，访问内网服务

### 双代理架构图

```
┌─────────────────┐    ┌──────────────────┐    ┌─────────────────┐
│   HTTP Client   │───▶│   HTTP Proxy     │    │                 │
└─────────────────┘    │   (Port 8080)    │───▶│   Gateway       │
                       └──────────────────┘    │                 │
┌─────────────────┐    ┌──────────────────┐    │  ┌───────────┐  │
│  SOCKS5 Client  │───▶│  SOCKS5 Proxy    │───▶│  │ WebSocket │  │
└─────────────────┘    │   (Port 1080)    │    │  │ Clients   │  │
                       └──────────────────┘    │  └───────────┘  │
                                               └─────────────────┘
```

## 🛠️ 安装与构建

### 前置要求

- Go 1.21+
- OpenSSL (用于生成证书)

### 构建项目

```bash
# 克隆项目
git clone https://github.com/buhuipao/anyproxy.git
cd anyproxy

# 生成 TLS 证书
make certs

# 构建所有组件
make build
```

### 生成自定义域名证书

```bash
# 为特定域名生成证书
bash generate_certs.sh your-domain.com
```

## ⚙️ 配置

配置文件位于 `configs/config.yaml`，包含以下主要配置：

### 网关配置
```yaml
gateway:
  listen_addr: ":8443"        # 网关监听地址
  tls_cert: "certs/server.crt" # TLS 证书路径
  tls_key: "certs/server.key"  # TLS 私钥路径
  auth_username: "user"        # 认证用户名
  auth_password: "password"    # 认证密码
```

### 客户端配置
```yaml
client:
  gateway_addr: "127.0.0.1:8443"     # 网关地址
  gateway_tls_cert: "certs/server.crt" # 网关 TLS 证书
  client_id: "client"                 # 客户端ID
  replicas: 1                         # 客户端副本数
  max_concurrent_conns: 100           # 最大并发连接数
  auth_username: "user"               # 认证用户名
  auth_password: "password"           # 认证密码
  forbidden_hosts:                    # 禁止访问的主机
    - "internal.example.com"
    - "192.168.1."
  limit:                              # 允许访问的服务列表
    - name: "web-server"
      addr: "localhost:8080"
      protocol: "tcp"
```

### 代理配置

支持同时配置 HTTP 和 SOCKS5 代理：

```yaml
proxy:
  # HTTP 代理配置
  http:
    listen_addr: ":8080"      # HTTP 代理监听地址
    auth_username: "http_user" # HTTP 代理认证用户名（可选）
    auth_password: "http_pass" # HTTP 代理认证密码（可选）
  
  # SOCKS5 代理配置
  socks5:
    listen_addr: ":1080"      # SOCKS5 监听地址
    auth_username: "socks_user" # SOCKS5 认证用户名（可选）
    auth_password: "socks_pass" # SOCKS5 认证密码（可选）
```

**配置选项**:
- 同时配置两个 `listen_addr` 可启动双代理
- 只配置其中一个可启动单一代理
- 如果都不配置会返回错误

## 🚀 使用方法

### 1. 启动网关

```bash
# 使用默认配置启动网关
make run-gateway

# 或者指定配置文件
./bin/anyproxy-gateway --config configs/config.yaml
```

### 2. 启动客户端

```bash
# 使用默认配置启动客户端
make run-client

# 或者指定配置文件
./bin/anyproxy-client --config configs/config.yaml
```

### 3. 使用代理服务

客户端连接成功后，公网用户可以通过 HTTP 或 SOCKS5 代理访问内网服务：

#### HTTP 代理使用

```bash
# 使用 curl 通过 HTTP 代理访问服务
curl -x http://http_user:http_pass@127.0.0.1:8080 https://target-service.com

# 设置环境变量使用 HTTP 代理
export http_proxy=http://http_user:http_pass@127.0.0.1:8080
export https_proxy=http://http_user:http_pass@127.0.0.1:8080

# 配置浏览器使用 HTTP 代理
# 代理地址: 127.0.0.1:8080
```

#### SOCKS5 代理使用

```bash
# 使用 curl 通过 SOCKS5 代理访问服务
curl --socks5 socks_user:socks_pass@127.0.0.1:1080 https://target-service.com

# 设置环境变量使用 SOCKS5 代理
export ALL_PROXY=socks5://socks_user:socks_pass@127.0.0.1:1080

# 配置浏览器使用 SOCKS5 代理
# 代理地址: 127.0.0.1:1080
```

## 📁 项目结构

```
anyproxy/
├── cmd/                    # 应用程序入口
│   ├── gateway/           # 网关程序
│   └── client/            # 客户端程序
├── pkg/                   # 核心包
│   ├── config/           # 配置管理
│   └── proxy/            # 代理核心逻辑
├── configs/              # 配置文件
├── certs/               # TLS 证书
├── design/              # 设计文档
├── docs/                # 项目文档
├── Makefile            # 构建脚本
└── generate_certs.sh   # 证书生成脚本
```

## 🔧 开发

### 运行测试

```bash
# 运行所有测试
go test ./...

# 运行特定包的测试
go test ./pkg/proxy/
```

### 清理构建文件

```bash
make clean
```

## 📖 更多文档

- [需求文档](design/requirement.md)
- [双代理支持](docs/DUAL_PROXY.md)
- [架构设计](docs/ARCHITECTURE.md)
- [部署指南](docs/DEPLOYMENT.md)
- [API 文档](docs/API.md)
- [故障排除](docs/TROUBLESHOOTING.md)
- [HTTP 代理故障排除](docs/HTTP_PROXY_TROUBLESHOOTING.md)

## 🤝 贡献

欢迎提交 Issue 和 Pull Request！

## 📄 许可证

本项目采用 MIT 许可证。 