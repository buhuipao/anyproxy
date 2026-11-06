# TUIC v5 Latency Test Tool

A comprehensive testing tool for TUIC v5 protocol performance, designed specifically for **Sunshine/Moonlight game streaming** scenarios.

## Overview

This tool helps diagnose latency, throughput, and frame loss issues when using TUIC v5 proxy for game streaming. It simulates realistic gaming traffic patterns and provides detailed metrics for performance analysis.

## Features

- **Latency Testing**: Measure RTT for TCP control commands and UDP video/audio packets
- **Throughput Testing**: Test sustained bandwidth capacity with different bitrates
- **Frame Loss Testing**: Simulate 60fps video streaming and measure packet loss
- **Game Stream Simulation**: Full Sunshine/Moonlight traffic simulation
- **Comparison Mode**: Compare TUIC performance against direct connections

## Quick Start

### One-Command Test (Recommended)

The `run_tests.sh` script handles everything automatically:
1. Kills any existing anyproxy processes
2. Rebuilds binaries (`make build`)
3. Starts `anyproxy-client`
4. Runs tests
5. Cleans up on exit

```bash
cd anyproxy/tests/latency-tuic
chmod +x run_tests.sh

# Run 2K 120Hz test (recommended)
# Script auto-detects certs/server.crt for TLS verification
./run_tests.sh 2k120

# Progressive stress test: 1080p60 → 1080p120 → 2K60 → 2K120 → 4K120
./run_tests.sh progressive

# Run all tests with default params
./run_tests.sh all

# Quick connectivity check
./run_tests.sh quick
```

### Manual Testing

**Option 1: Start client only (for debugging)**
```bash
./run_tests.sh client
```

**Option 2: Direct TUIC test (skip anyproxy-client)**
```bash
./run_tests.sh 2k120 --no-client
```

**Option 3: Skip rebuild**
```bash
./run_tests.sh 2k120 --no-build
```

**Option 4: Custom TLS certificate**
```bash
./run_tests.sh 2k120 --tls-cert /path/to/server.crt
```

### Full Manual Control

```bash
# Terminal 1: Start client manually
cd anyproxy
make build
./bin/anyproxy-client -config tests/latency-tuic/client-config.yaml

# Terminal 2: Run tests
cd anyproxy/tests/latency-tuic
go run . -server 47.107.181.88:9443 -uuid demo-group -password demo-123 \
  -tls-cert ../../certs/server.crt -test all -fps 120 -bitrate 80
```

## Command Line Options

### Connection Options

| Option | Description | Default |
|--------|-------------|---------|
| `-server` | TUIC server address (host:port) | Required |
| `-uuid` | User UUID (can be group_id) | Required |
| `-password` | User password | Required |
| `-mode` | UDP relay mode: `native` or `quic` | `native` |
| `-congestion` | Congestion control: `bbr`, `cubic`, `new_reno` | `bbr` |
| `-0rtt` | Enable 0-RTT handshake | `true` |
| `-tls-cert` | Path to server's TLS certificate | - |
| `-tls-server-name` | Override TLS server name (SNI) | - |

### Test Options

| Option | Description | Default |
|--------|-------------|---------|
| `-test` | Test type: `latency`, `throughput`, `frameloss`, `gamestream`, `all` | `all` |
| `-count` | Number of iterations for latency tests | `20` |
| `-duration` | Duration for streaming tests | `10s` |
| `-fps` | Target FPS for frame loss test | `60` |
| `-bitrate` | Target bitrate in Mbps | `50` |
| `-concurrent` | Concurrent connections for load test | `4` |
| `-skip-direct` | Skip direct baseline tests | `false` |

### Local Service Options

| Option | Description | Default |
|--------|-------------|---------|
| `-http-port` | Local HTTP echo service port | `19080` |
| `-tcp-port` | Local TCP echo service port | `19081` |
| `-udp-port` | Local UDP echo service port | `19053` |

## Test Types

### 1. Latency Test (`-test latency`)

Measures round-trip time for different packet types:

- **Control Commands (TCP, 128B)**: Simulates gamepad input latency
- **Video Packets (UDP, 1.2KB)**: Simulates video frame transmission (tests QUIC Datagram MTU limit)
- **Audio Packets (UDP, 400B)**: Simulates audio stream transmission
- **Concurrent Load**: Tests latency under multiple connections

Output includes: Min, Avg, P50, P90, P99, Max latency, and Jitter.

### 2. Throughput Test (`-test throughput`)

Tests sustained bandwidth at various bitrates:

- 20 Mbps (1080p60 streaming)
- 50 Mbps (4K60 streaming)
- 100 Mbps (high bitrate)
- Custom bitrate via `-bitrate` flag

Output includes: Actual Mbps, packet loss rate, data integrity.

### 3. Frame Loss Test (`-test frameloss`)

Simulates RTSP video streaming:

- Sends packets at target FPS (default 60fps)
- Measures frame loss rate, out-of-order packets, max gap
- Calculates effective FPS and jitter

Critical for identifying stuttering issues in game streaming.

### 4. Game Stream Simulation (`-test gamestream`)

Full Sunshine/Moonlight traffic simulation:

- Video: 60fps × 1.2KB packets (~9.6 Mbps)
- Audio: 100pps × 400B packets (~0.3 Mbps)
- Concurrent streams testing

## UDP Relay Modes

### Native Mode (Recommended for Gaming)

```bash
-mode native
```

- Uses QUIC Datagram (unreliable, low latency)
- Best for real-time gaming where speed > reliability
- Packets ≤1200 bytes use QUIC Datagram; larger packets fallback to reliable stream

### QUIC Mode

```bash
-mode quic
```

- Uses QUIC Stream (reliable, ordered)
- Higher latency due to retransmissions
- Better for applications requiring guaranteed delivery

## Gaming Preset Scenarios

Use the helper script for common gaming scenarios:

```bash
cd tests/latency-tuic

# 1080p 60fps (~20 Mbps)
./run_tests.sh 1080p60 -s your-server:9443 -u your-uuid -p your-password

# 1080p 120fps (~40 Mbps)
./run_tests.sh 1080p120 -s your-server:9443 -u your-uuid -p your-password

# 2K 60fps (~40 Mbps)
./run_tests.sh 2k60 -s your-server:9443 -u your-uuid -p your-password

# 2K 120fps (~80 Mbps) - Recommended for competitive gaming
./run_tests.sh 2k120 -s your-server:9443 -u your-uuid -p your-password

# 4K 60fps (~50 Mbps)
./run_tests.sh 4k60 -s your-server:9443 -u your-uuid -p your-password

# 4K 120fps (~150 Mbps) - Ultra high-end
./run_tests.sh 4k120 -s your-server:9443 -u your-uuid -p your-password
```

### Bandwidth Requirements by Resolution

| Resolution | FPS | Typical Bitrate | Use Case |
|------------|-----|-----------------|----------|
| 1080p | 60 | 15-25 Mbps | Casual gaming |
| 1080p | 120 | 30-50 Mbps | Competitive FPS |
| 2K (1440p) | 60 | 30-50 Mbps | High quality |
| **2K (1440p)** | **120** | **60-100 Mbps** | **Competitive + quality** |
| 4K | 60 | 40-80 Mbps | Ultra quality |
| 4K | 120 | 100-200 Mbps | Ultra high-end |

## Example Scenarios

### 2K 120Hz Competitive Gaming Test

```bash
# Recommended for 2K 120Hz experience
go run ./tests/latency-tuic \
  -server your-server:9443 \
  -uuid your-uuid \
  -password your-pass \
  -test all \
  -fps 120 \
  -bitrate 80 \
  -duration 30s
```

### Quick Connectivity Check

```bash
# Quick test without baseline comparison
go run ./tests/latency-tuic \
  -server your-server:9443 \
  -uuid your-uuid \
  -password your-pass \
  -test latency \
  -count 5 \
  -skip-direct
```

### High-Throughput Stress Test

```bash
# Test maximum throughput (for 4K120 or stress testing)
go run ./tests/latency-tuic \
  -server your-server:9443 \
  -uuid your-uuid \
  -password your-pass \
  -test throughput \
  -bitrate 200 \
  -duration 30s
```

### Compare Native vs QUIC Mode

```bash
# Test native mode (recommended for gaming)
go run ./tests/latency-tuic -server ... -mode native -test all -fps 120

# Test quic mode (for comparison)
go run ./tests/latency-tuic -server ... -mode quic -test all -fps 120
```

## Understanding Results

### Latency Requirements by FPS

| FPS | Frame Time | Max Acceptable RTT | Ideal RTT |
|-----|------------|-------------------|-----------|
| 60 | 16.67ms | <50ms | <30ms |
| **120** | **8.33ms** | **<30ms** | **<16ms** |
| 144 | 6.94ms | <25ms | <14ms |

> For 120Hz gaming, latency is critical. Each frame only has 8.33ms, so RTT should ideally be under 16ms for responsive gameplay.

### Latency Metrics

| Metric | 60Hz Good | 120Hz Good | Poor |
|--------|-----------|------------|------|
| Avg RTT | <30ms | <20ms | >50ms |
| P99 RTT | <50ms | <30ms | >100ms |
| Jitter | <10ms | <5ms | >20ms |

### Frame Loss Metrics

| Metric | 60Hz Good | 120Hz Good | Poor |
|--------|-----------|------------|------|
| Loss Rate | <2% | <1% | >5% |
| Max Gap | <3 frames | <2 frames | >5 frames |
| Out of Order | <1% | <0.5% | >5% |

> At 120fps, even 1% frame loss = 1.2 dropped frames per second, which can be noticeable.

### Throughput Metrics (for 2K 120Hz @ 80Mbps)

| Metric | Excellent | Warning | Failed |
|--------|-----------|---------|--------|
| Actual Bitrate | >95% target | >80% target | <80% target |
| Loss Rate | <0.5% | <2% | >2% |
| Integrity | >99.5% | >98% | <98% |

### 2K 120Hz Qualification Criteria

To achieve smooth 2K 120Hz gaming experience:

| Metric | Requirement |
|--------|-------------|
| Sustained Throughput | >80 Mbps |
| Average Latency | <20ms |
| P99 Latency | <30ms |
| Frame Loss | <1% |
| Jitter | <5ms |
| Max Consecutive Loss | <2 frames |

## Troubleshooting

### High Latency

1. Check network RTT to server: `ping your-server`
2. Try different congestion control: `-congestion cubic`
3. Disable 0-RTT: `-0rtt=false`

### High Frame Loss

1. Switch to QUIC mode: `-mode quic`
2. Reduce bitrate: `-bitrate 20`
3. Check for network congestion

### Connection Failures

1. Verify server address and port
2. Check UUID/password credentials
3. Ensure TLS certificate matches (`-tls-cert`)
4. Check firewall rules (UDP traffic)

## Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│                     latency-tuic Test Architecture                  │
├─────────────────────────────────────────────────────────────────────┤
│                                                                     │
│  Test Client (Go)              Gateway (anyproxy)        Echo Svc   │
│  ┌─────────────────┐         ┌───────────────┐         ┌─────────┐ │
│  │ TUIC v5 Client  │ ──────► │ TUICv5Proxy   │ ──────► │ UDP/TCP │ │
│  │ (sing-quic)     │ QUIC    │ :9443         │ dial    │ Echo    │ │
│  │                 │         │               │         │         │ │
│  │ · native mode   │         │ ┌───────────┐ │         │ Simulates│ │
│  │   (Datagram)    │         │ │ Gateway   │ │         │ Sunshine│ │
│  │ · quic mode     │         │ │ to Client │ │         │ Service │ │
│  │   (Stream)      │         │ └───────────┘ │         │         │ │
│  └─────────────────┘         └───────────────┘         └─────────┘ │
│                                                                     │
│  Measured Metrics:                                                  │
│  · RTT latency distribution (P50/P90/P99)                          │
│  · Packet loss rate (native mode focus)                            │
│  · Out-of-order rate (sequence check)                              │
│  · Effective throughput (Mbps)                                     │
│  · Jitter (latency variance)                                       │
│                                                                     │
└─────────────────────────────────────────────────────────────────────┘
```

## Dependencies

This tool uses the same dependencies as anyproxy:

- `github.com/sagernet/sing-quic/tuic` - TUIC v5 client
- `github.com/sagernet/sing` - sing common library

## License

Part of the anyproxy project.
