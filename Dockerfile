# Build stage
FROM golang:1.23-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git openssl ca-certificates

# Set working directory
WORKDIR /app

# Copy go mod files first for better caching
COPY go.mod go.sum ./

# Set Go proxy for faster downloads (especially useful in China)
ENV GOPROXY=https://goproxy.cn,https://goproxy.io,https://proxy.golang.org,direct
ENV GOSUMDB=sum.golang.google.cn
ENV GO111MODULE=on

# Download dependencies with retry mechanism
RUN go mod download && go mod verify

# Copy source code
COPY . .

# Generate test certificates (for immediate testing - users can override with their own)
RUN mkdir -p certs && \
    openssl req -x509 -newkey rsa:2048 -keyout certs/server.key -out certs/server.crt \
    -days 365 -nodes -subj "/CN=localhost" \
    -addext "subjectAltName = DNS:localhost,DNS:anyproxy,IP:127.0.0.1,IP:0.0.0.0"

# Build binaries
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildTime=${BUILD_TIME}" \
    -o anyproxy-gateway cmd/gateway/main.go && \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildTime=${BUILD_TIME}" \
    -o anyproxy-client cmd/client/main.go

# Verify binaries
RUN chmod +x anyproxy-gateway anyproxy-client

# Runtime stage
FROM alpine:latest

# Install runtime dependencies
RUN apk --no-cache add ca-certificates tzdata curl

# Create non-root user
RUN addgroup -g 1001 anyproxy && \
    adduser -D -u 1001 -G anyproxy anyproxy

# Set working directory
WORKDIR /app

# Copy binaries from builder stage
COPY --from=builder /app/anyproxy-gateway /app/anyproxy-client ./

# Copy certificates (includes test certificate for immediate use)
COPY --from=builder /app/certs ./certs/

# Copy configuration files
COPY --from=builder /app/configs ./configs/

# Copy web interface static files only (dashboard, login, monitoring pages)
COPY --from=builder /app/web/gateway/static ./web/gateway/static/
COPY --from=builder /app/web/client/static ./web/client/static/

# Create necessary directories and set permissions
RUN mkdir -p logs && \
    chown -R anyproxy:anyproxy /app && \
    chmod -R 755 /app/web/gateway/static /app/web/client/static

# Switch to non-root user
USER anyproxy

# Expose ports
# HTTP Proxy, SOCKS5 Proxy, TUIC Proxy (UDP), WebSocket Transport, gRPC Transport, QUIC Transport (UDP)
# Web management interfaces (Gateway: 8090, Client: 8091)
EXPOSE 8080 1080 9443/udp 8443 9090 9091/udp 8090 8091

# Health check - works for both gateway and client
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD pgrep -f "anyproxy-" > /dev/null || exit 1

# Default command (can be overridden)
# Note: Built-in test certificate allows immediate testing without custom certs
CMD ["./anyproxy-gateway", "--config", "configs/config.yaml"] 