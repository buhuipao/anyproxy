# AnyProxy 优化计划

> 文档创建时间: 2026-01-14
> 状态: **全部优化已完成** ✅

## 一、概述

本文档记录了对 AnyProxy 项目进行代码审查后发现的功能问题和性能瓶颈，并提供分阶段的优化计划。

## 二、问题分类

### 功能问题 (Functional Issues)

| ID | 严重性 | 问题描述 | 影响范围 | 状态 |
|----|--------|----------|----------|------|
| F-001 | 🔴 高 | SOCKS5 UDP Session Manager 未被使用 | UDP 代理稳定性 | ✅ 已修复 |
| F-002 | 🔴 高 | UDP Session 查找存在竞态条件 | 并发安全 | ✅ 已修复 |
| F-003 | 🔴 高 | TUIC Proxy TCP Connect 实现不完整 | TUIC 功能 | ✅ 已修复 |
| F-004 | 🟡 中 | Gateway 锁顺序存在潜在死锁风险 | 系统稳定性 | ✅ 已修复 |
| F-005 | 🟡 中 | HTTP Proxy TLS 握手错误未处理 | HTTPS 代理 | ✅ 已修复 |

### 性能瓶颈 (Performance Bottlenecks)

| ID | 严重性 | 问题描述 | 影响范围 | 状态 |
|----|--------|----------|----------|------|
| P-001 | 🔴 高 | Gateway Client 选择锁竞争严重 | 吞吐量 | ✅ 已优化 |
| P-002 | 🔴 高 | Binary Protocol 频繁内存分配 | GC 压力 | ✅ 已优化 |
| P-003 | 🟡 中 | SOCKS5 Proxy 未使用 Buffer Pool | 内存效率 | ✅ 已优化 |
| P-004 | 🟡 中 | 安全检查中的 DNS 阻塞 | 连接延迟 | ✅ 已优化 |
| P-005 | 🟡 中 | 消息通道容量可能不足 | 高吞吐连接 | ✅ 已优化 |
| P-006 | 🟢 低 | HTTP Proxy 每请求创建新连接 | HTTP 代理性能 | 暂不优化 |

---

## 三、分阶段优化计划

### 阶段一：关键功能修复 (Critical Fixes)

**目标**: 修复影响核心功能的问题
**预计时间**: 1-2 天

#### 1.1 F-001: 使用 UDPSessionManager 替换 sync.Map

**文件**: `pkg/protocols/socks5proxy.go`

**当前实现**:
```go
// UDP support
udpListener *net.UDPConn
udpSessions sync.Map // 简单的 sync.Map，无 LRU 淘汰
```

**修改方案**:
```go
// UDP support with LRU eviction and timeout cleanup
udpListener     *net.UDPConn
udpSessionMgr   *UDPSessionManager // 使用已有的 session manager
```

**修改步骤**:
1. 在 `NewSOCKS5ProxyWithAuth` 中初始化 `UDPSessionManager`
2. 替换所有 `udpSessions.Store/Load/Delete` 调用
3. 在 `Stop()` 中调用 `udpSessionMgr.Stop()`
4. 更新单元测试

---

#### 1.2 F-002: 修复 UDP Session 竞态条件

**文件**: `pkg/protocols/socks5proxy.go`

**当前问题**:
```go
p.udpSessions.Range(func(key, value any) bool {
    // 并发修改 map，存在竞态
    p.udpSessions.Store(sessionKey, session)
    p.udpSessions.Delete(keyStr)
    return false
})
```

**修改方案**: 使用 `UDPSessionManager.LoadOrAdoptTCPSession()` 原子操作

---

#### 1.3 F-004: 统一锁顺序

**文件**: `pkg/gateway/gateway.go`

**修改方案**: 
统一所有地方的锁顺序为 `clientsMu -> groupsMu`

**影响的函数**:
- `getClientByGroup()` - 需要调整
- `addClient()` - 当前顺序正确
- `removeClient()` - 当前顺序正确

---

### 阶段二：性能关键优化 (Performance Critical)

**目标**: 解决高频路径的性能瓶颈
**预计时间**: 2-3 天

#### 2.1 P-001: 优化 Gateway Client 选择

**文件**: `pkg/gateway/gateway.go`

**当前实现**:
```go
func (g *Gateway) getClientByGroup(groupID string) (*ClientConn, error) {
    g.groupsMu.Lock()      // 独占锁
    defer g.groupsMu.Unlock()
    g.clientsMu.RLock()    // 读锁
    defer g.clientsMu.RUnlock()
    // round-robin 选择
}
```

**优化方案 A - 使用 atomic counter**:
```go
type GroupInfo struct {
    Clients []string
    Counter atomic.Int64  // 使用原子计数器
}

func (g *Gateway) getClientByGroup(groupID string) (*ClientConn, error) {
    g.groupsMu.RLock()  // 改为读锁
    groupInfo, exists := g.groups[groupID]
    g.groupsMu.RUnlock()
    
    if !exists || len(groupInfo.Clients) == 0 {
        return nil, fmt.Errorf("no clients available")
    }
    
    // 无锁 round-robin
    idx := groupInfo.Counter.Add(1) % int64(len(groupInfo.Clients))
    clientID := groupInfo.Clients[idx]
    
    g.clientsMu.RLock()
    client := g.clients[clientID]
    g.clientsMu.RUnlock()
    
    return client, nil
}
```

---

#### 2.2 P-002: Binary Protocol 使用 Buffer Pool

**文件**: `pkg/common/protocol/binary_protocol.go`

**添加 Buffer Pool**:
```go
var (
    // 小消息 buffer pool (控制消息)
    smallBufferPool = sync.Pool{
        New: func() interface{} {
            buf := make([]byte, 256)
            return &buf
        },
    }
    
    // 大消息 buffer pool (数据消息)
    largeBufferPool = sync.Pool{
        New: func() interface{} {
            buf := make([]byte, 32*1024)
            return &buf
        },
    }
)
```

---

### 阶段三：功能完善 (Feature Completion)

**目标**: 完善未实现的功能
**预计时间**: 3-5 天

#### 3.1 F-003: 完成 TUIC TCP Relay 实现

**文件**: `pkg/protocols/tuicproxy.go`

**当前状态**: TCP Connect 后立即关闭连接

**修改方案**: 添加 TCP session 管理和双向数据中继

---

#### 3.2 F-005: HTTP Proxy TLS 握手验证

**文件**: `pkg/protocols/httpproxy.go`

**修改方案**: 在 TLS 包装后显式调用 `HandshakeContext()` 进行验证

---

### 阶段四：次要优化 (Secondary Optimizations) ✅

**目标**: 进一步提升性能和稳定性
**预计时间**: 2-3 天
**状态**: 已完成

#### 4.1 P-003: SOCKS5 Proxy UDP Buffer Pool ✅

**文件**: `pkg/protocols/socks5proxy.go`

**实现**:
```go
// P-003: Buffer pool for UDP operations to reduce memory allocation
var udpBufferPool = sync.Pool{
    New: func() interface{} {
        buf := make([]byte, 64*1024)
        return &buf
    },
}
```

**优化点**:
- `runUDPListener()` 使用 buffer pool 读取 UDP 数据包
- `relayUDPResponses()` 使用 buffer pool 构建响应

---

#### 4.2 P-004: 安全检查中的 DNS 查询 ✅

**现状**: 不再使用 DNS 缓存，`rs/dnscache` 已移除。

**实现**: `pkg/client/message.go` 的 `handleConnectMessage()` 先检查不需要 DNS 的名称规则（`passesNameRules()`），被拒绝的名称不会被解析。之后用 `resolveTarget()`（`pkg/client/security.go`）对目标只解析一次，CIDR/IP 规则检查全部解析结果，`dialIPs()` 只拨号这些已检查的 IP。每次连接只有一次 DNS 查询，与拨号时原本需要的查询相同。

**原因**: 缓存解析器只检查第一个地址，拨号时又按名称重新解析，DNS 结果可以在检查和拨号之间变化，CIDR 规则因此可被绕过（审计 H5）。

---

#### 4.3 P-005: 增加消息通道容量 ✅

**文件**: `pkg/common/protocol/protocol.go`

**修改**:
```go
// P-005: Increased from 100 to 500 for better high-throughput support
DefaultMessageChannelSize = 500
```

---

#### 4.4 P-006: HTTP 上游连接池 (暂不实现)

**原因**: 当前 HTTP 代理实现主要用于 CONNECT 隧道场景，连接池收益有限

---

## 四、测试计划

### 单元测试

| 阶段 | 测试文件 | 覆盖内容 |
|------|----------|----------|
| 阶段一 | `socks5proxy_test.go` | UDP session 管理 |
| 阶段二 | `gateway_test.go` | Client 选择性能 |
| 阶段二 | `binary_protocol_test.go` | Buffer pool 正确性 |
| 阶段三 | `tuicproxy_test.go` | TCP relay 功能 |
| 阶段四 | `pkg/client/security_test.go` | 主机规则匹配与目标一次解析 |

### 压力测试

```bash
# SOCKS5 压力测试
go run ./tests/socks5 -proxy 127.0.0.1:11080 -user test-group -pass test-password -test all -concurrent 100

# HTTP 代理压力测试
wrk -t12 -c400 -d30s http://gateway:8080/test
```

### 性能基准

```bash
# Gateway client selection benchmark
go test -bench=BenchmarkGetClientByGroup ./pkg/gateway/...

# Binary protocol benchmark
go test -bench=BenchmarkPackDataMessage ./pkg/common/protocol/...
```

---

## 五、风险评估

| 修改 | 风险级别 | 潜在问题 | 缓解措施 |
|------|----------|----------|----------|
| UDP Session Manager 替换 | 中 | 行为变化 | 充分测试，灰度发布 |
| 锁优化 | 高 | 引入新 bug | 代码审查，压力测试 |
| Buffer Pool | 低 | 内存泄漏 | 监控内存使用 |
| TUIC TCP 实现 | 中 | 功能不完整 | 参考官方规范 |

---

## 六、时间线

```
Week 1:
├── Day 1-2: 阶段一 - 关键功能修复
│   ├── F-001: UDP Session Manager
│   ├── F-002: 竞态条件修复
│   └── F-004: 锁顺序统一

Week 2:
├── Day 3-5: 阶段二 - 性能关键优化
│   ├── P-001: Client 选择优化
│   └── P-002: Buffer Pool

Week 3:
├── Day 6-10: 阶段三 - 功能完善
│   ├── F-003: TUIC TCP Relay
│   └── F-005: TLS 握手验证

Week 4:
├── Day 11-13: 阶段四 - 次要优化
│   ├── P-004: 安全检查中的 DNS 查询
│   ├── P-005: 动态通道
│   └── P-006: 连接池 (可选)
```

---

## 七、详细问题描述

### F-001: SOCKS5 UDP Session Manager 未被使用

**位置**: `pkg/protocols/socks5proxy.go:51-55`

**问题**: 代码中存在完整的 `UDPSessionManager` (带 LRU 淘汰和超时清理)，但 SOCKS5 代理使用简单的 `sync.Map`

**影响**:
- UDP session 没有 LRU 淘汰机制
- 没有超时清理（只依赖 TCP 连接关闭来清理）
- 高负载下可能导致内存泄漏

---

### F-002: UDP Session 查找存在竞态条件

**位置**: `pkg/protocols/socks5proxy.go:316-334`

**问题**: 在查找 TCP 引用的 session 时，使用 `Range` 遍历并修改 map，可能导致并发问题

**影响**: 多个 UDP 包可能同时尝试 "adopt" 同一个 session

---

### F-003: TUIC Proxy TCP Connect 实现不完整

**位置**: `pkg/protocols/tuicproxy.go:388-428`

**问题**: TCP Connect 命令建立连接后立即关闭，没有实现实际的数据中继

**影响**: TUIC 的 TCP 代理功能实际上不工作

---

### F-004: Gateway 锁顺序存在潜在死锁风险

**位置**: `pkg/gateway/gateway.go:528-534` vs `pkg/gateway/gateway.go:436-467`

**问题**: 不同函数中锁的获取顺序不一致

**影响**: 潜在的死锁风险

---

### F-005: HTTP Proxy TLS 握手错误未处理

**位置**: `pkg/protocols/httpproxy.go:435-442`

**问题**: TLS 包装后没有检查握手是否成功

**影响**: TLS 握手失败只会在后续操作中发现，错误信息不明确

---

### P-001: Gateway Client 选择锁竞争严重

**位置**: `pkg/gateway/gateway.go:528-559`

**问题**: 每次代理请求都需要获取两个锁来选择 client

**影响**:
- 高并发场景下严重影响吞吐量
- 单点瓶颈，无法水平扩展

---

### P-002: Binary Protocol 频繁内存分配

**位置**: `pkg/common/protocol/binary_protocol.go`

**问题**: 每条消息都创建新的 byte slice

**影响**:
- GC 压力大
- 高吞吐场景下性能下降明显

---

## 八、变更日志

| 日期 | 版本 | 变更内容 |
|------|------|----------|
| 2026-01-14 | 1.0 | 初始版本，完成问题分析和计划制定 |
| 2026-01-14 | 2.0 | **完成阶段一至三核心优化**：<br>- F-001: 使用 UDPSessionManager 替换 sync.Map<br>- F-002: 使用 LoadOrAdoptTCPSession 修复竞态条件<br>- F-003: 实现 TUIC TCP Relay 数据中继<br>- F-004: 统一锁顺序为 clientsMu -> groupsMu<br>- F-005: 添加显式 TLS HandshakeContext 验证<br>- P-001: 使用 atomic.Int64 实现无锁 round-robin<br>- P-002: 添加 PackDataMessagePooled/PackUDPDataMessagePooled Buffer Pool 函数 |
| 2026-01-14 | 3.0 | **完成阶段四次要优化**：<br>- P-003: SOCKS5 Proxy 添加 UDP Buffer Pool (udpBufferPool) 减少 64KB buffer 频繁分配<br>- P-004: 使用 `github.com/rs/dnscache` 第三方库实现 DNS 缓存，优化 CIDR 模式匹配的 DNS 查询（已在 3.1 移除）<br>- P-005: 消息通道容量从 100 增加到 500，支持高吞吐场景<br>- P-006: HTTP 连接池暂不实现（当前实现已满足需求） |
| 2026-11-03 | 3.1 | P-004: 移除 `rs/dnscache`。客户端每次连接只解析一次目标，CIDR/IP 检查与拨号使用同一组 IP（审计 H5） |