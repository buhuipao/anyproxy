# AnyProxy Web Management Interfaces

AnyProxy 提供了完整的 Web 管理界面，支持实时监控、配置管理和多维度统计分析。

## 🌟 功能特性

### Gateway Web 界面
- **实时监控**: 活跃连接、数据传输统计、成功率监控
- **客户端管理**: 查看所有连接的客户端状态和流量统计
- **多维度统计**: 按客户端、域名、连接等维度分析数据
- **速率限制**: 配置和监控客户端访问限制
- **认证安全**: 基于用户名密码的访问控制
- **国际化支持**: 中英文双语界面

### Client Web 界面
- **本地监控**: 客户端运行状态和连接情况
- **健康检查**: 网关连接和本地服务状态检测
- **连接管理**: 查看活跃连接详情和流量统计
- **系统诊断**: 运行时间、错误统计、网络使用情况
- **配置管理**: 端口转发和主机访问规则配置

## 🚀 快速开始

### 1. 启动服务

使用提供的测试脚本快速启动：

```bash
# 启动 Gateway 和 Client 服务（包含 Web 界面）
./scripts/test-web-interface.sh
```

### 2. 访问 Web 界面

**Gateway 管理界面**
- URL: http://localhost:8090
- 用户名: `admin`
- 密码: `admin123`

**Client 监控界面**
- URL: http://localhost:8091
- 无需认证

## 📱 界面功能

### Gateway 仪表盘
- **统计卡片**: 显示活跃连接、总连接数、数据传输量、成功率
- **客户端状态表**: 实时显示所有客户端的连接状态和流量
- **自动刷新**: 支持10秒自动刷新，可手动开关
- **语言切换**: 点击右上角按钮切换中英文

### Client 监控面板
- **运行状态**: 显示客户端运行时间和基本统计
- **连接列表**: 详细显示所有活跃连接的信息
- **健康检查**: 网关连接状态和本地服务可达性
- **系统信息**: 客户端版本、运行时间、网络统计

## ⚙️ 配置选项

### Gateway Web 配置

```yaml
gateway:
  web:
    enabled: true                    # 启用 Web 界面
    listen_addr: ":8090"            # 监听地址
    static_dir: "web/gateway/static" # 静态文件目录
    auth_enabled: true              # 启用认证
    auth_username: "admin"          # 用户名
    auth_password: "admin123"       # 密码
```

### Client Web 配置

```yaml
client:
  web:
    enabled: true                   # 启用 Web 界面
    listen_addr: ":8091"           # 监听地址
    static_dir: "web/client/static" # 静态文件目录
```

## 🔧 API 接口

### Gateway API

| 接口 | 方法 | 说明 |
|------|------|------|
| `/api/auth/login` | POST | 用户登录 |
| `/api/auth/logout` | POST | 用户登出 |
| `/api/auth/check` | GET | 检查认证状态 |
| `/api/metrics/global` | GET | 全局统计数据 |
| `/api/metrics/clients` | GET | 客户端统计数据 |
| `/api/metrics/domains` | GET | 域名统计数据 |
| `/api/metrics/connections` | GET | 连接统计数据 |
| `/api/ratelimit/config` | GET/POST | 速率限制配置 |

### Client API

| 接口 | 方法 | 说明 |
|------|------|------|
| `/api/status` | GET | 客户端状态 |
| `/api/metrics/local` | GET | 本地统计数据 |
| `/api/metrics/connections` | GET | 连接统计数据 |
| `/api/health` | GET | 健康检查 |
| `/api/diagnostics` | GET | 系统诊断信息 |
| `/api/config/hosts` | GET/PUT | 主机访问配置 |
| `/api/config/ports` | GET/POST | 端口转发配置 |

## 🌍 国际化支持

支持中英文双语界面：
- **English**: 默认语言
- **中文**: 完整本地化支持
- **切换方式**: 点击界面右上角的语言切换按钮
- **持久化**: 语言选择会保存在浏览器本地存储中

## 📊 统计数据

- **内存存储**: 所有统计数据仅存储在内存中，不持久化到磁盘
- **实时性**: 统计数据实时更新，重启后重新开始计算  
- **轻量级**: 无需外部数据库依赖，减少资源占用

## 🔒 安全特性

- **Gateway 认证**: 支持用户名密码认证，保护管理界面
- **会话管理**: 24小时会话超时，支持自动续期
- **CORS 支持**: 允许跨域访问API接口
- **错误处理**: 完善的错误处理和用户友好的错误提示

## 📱 响应式设计

- **移动友好**: 支持手机和平板设备访问
- **自适应布局**: 根据屏幕大小自动调整界面
- **现代UI**: 采用现代化的设计语言和交互体验

## 🚨 故障排除

### 常见问题

1. **无法访问Web界面**
   - 检查端口是否被占用
   - 确认服务是否正常启动
   - 检查防火墙设置

2. **Gateway登录失败**
   - 检查用户名密码是否正确
   - 查看服务日志确认错误信息

3. **数据显示为空**
   - 确认Gateway和Client已建立连接
   - 检查是否有实际的代理流量

4. **自动刷新不工作**
   - 检查浏览器控制台是否有错误
   - 确认API接口是否正常响应

### 日志位置

- Gateway日志: `logs/anyproxy.log`
- Client日志: `logs/anyproxy.log`
- Web访问日志: 控制台输出

## 🔄 开发模式

如需修改Web界面：

1. 修改HTML/CSS/JS文件
2. 重启对应的服务
3. 刷新浏览器页面

静态文件路径：
- Gateway: `web/gateway/static/`
- Client: `web/client/static/` 