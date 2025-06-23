.PHONY: all build clean run-gateway run-client certs test lint docker-build docker-run help

# Build variables
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS = -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildTime=$(BUILD_TIME)

# Go build settings
GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)
CGO_ENABLED ?= 0

# Binary names
GATEWAY_BINARY = anyproxy-gateway
CLIENT_BINARY = anyproxy-client

# Build directory
BUILD_DIR = bin

all: certs build ## Build everything

help: ## Show this help message
	@echo 'Usage: make [target]'
	@echo ''
	@echo 'Build Commands:'
	@awk 'BEGIN {FS = " ## "} /^(build|run-).*##/ {gsub(/:.*/, "", $$1); printf "    %-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)
	@echo ''
	@echo 'Development Commands:'
	@awk 'BEGIN {FS = " ## "} /^(test|lint|fmt|vet|deps).*##/ {gsub(/:.*/, "", $$1); printf "    %-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)
	@echo ''
	@echo 'Docker Commands:'
	@awk 'BEGIN {FS = " ## "} /^docker.*##/ {gsub(/:.*/, "", $$1); printf "    %-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)
	@echo ''
	@echo 'Other Commands:'
	@awk 'BEGIN {FS = " ## "} !/^(build|run-|test|lint|fmt|vet|deps|docker).*##/ && /.*##/ {gsub(/:.*/, "", $$1); printf "    %-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build binaries for current platform
	@echo "Building binaries for $(GOOS)/$(GOARCH)..."
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) go build -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(GATEWAY_BINARY) cmd/gateway/main.go
	CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) go build -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(CLIENT_BINARY) cmd/client/main.go
	@echo "Build completed: $(BUILD_DIR)/$(GATEWAY_BINARY), $(BUILD_DIR)/$(CLIENT_BINARY)"

build-all: ## Build binaries for all platforms
	@echo "Building for all platforms..."
	@mkdir -p build
	
	# Linux AMD64
	@echo "Building for linux/amd64..."
	@CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o build/$(GATEWAY_BINARY)-linux-amd64 cmd/gateway/main.go
	@CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o build/$(CLIENT_BINARY)-linux-amd64 cmd/client/main.go
	
	# Linux ARM64
	@echo "Building for linux/arm64..."
	@CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o build/$(GATEWAY_BINARY)-linux-arm64 cmd/gateway/main.go
	@CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o build/$(CLIENT_BINARY)-linux-arm64 cmd/client/main.go
	
	# Windows AMD64
	@echo "Building for windows/amd64..."
	@CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o build/$(GATEWAY_BINARY)-windows-amd64.exe cmd/gateway/main.go
	@CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o build/$(CLIENT_BINARY)-windows-amd64.exe cmd/client/main.go
	
	# macOS AMD64
	@echo "Building for darwin/amd64..."
	@CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o build/$(GATEWAY_BINARY)-darwin-amd64 cmd/gateway/main.go
	@CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o build/$(CLIENT_BINARY)-darwin-amd64 cmd/client/main.go
	
	# macOS ARM64
	@echo "Building for darwin/arm64..."
	@CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o build/$(GATEWAY_BINARY)-darwin-arm64 cmd/gateway/main.go
	@CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o build/$(CLIENT_BINARY)-darwin-arm64 cmd/client/main.go
	
	@echo "Cross-compilation completed. Binaries are in build/"

certs: ## Generate TLS certificates
	@echo "Generating TLS certificates..."
	@bash scripts/generate_certs.sh
	@echo "Certificates generated in certs/ directory"

test: ## Run tests
	@echo "Running tests..."
	@go test -v -race -coverprofile=coverage.out ./...
	@echo "Tests completed"

test-coverage: test ## Run tests with coverage report
	@echo "Generating coverage report..."
	@go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report generated: coverage.html"

lint: ## Run linter
	@echo "Running linter..."
	@which golangci-lint > /dev/null || (echo "golangci-lint not found. Install it from https://golangci-lint.run/usage/install/" && exit 1)
	@golangci-lint run --timeout=5m
	@echo "Linting completed"

run-gateway: build ## Run gateway
	@echo "Starting gateway..."
	@./$(BUILD_DIR)/$(GATEWAY_BINARY) --config configs/config.yaml

run-client: build ## Run client
	@echo "Starting client..."
	@./$(BUILD_DIR)/$(CLIENT_BINARY) --config configs/config.yaml

docker-build: ## Build Docker image
	@echo "Building Docker image..."
	@docker build \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg BUILD_TIME=$(BUILD_TIME) \
		-t anyproxy:$(VERSION) \
		-t anyproxy:latest .
	@echo "Docker image built: anyproxy:$(VERSION)"

docker-build-cn: ## Build Docker image with China mirrors (faster in China)
	@echo "Building Docker image with China mirrors..."
	@docker build \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg BUILD_TIME=$(BUILD_TIME) \
		-f Dockerfile.cn \
		-t anyproxy:$(VERSION) \
		-t anyproxy:latest .
	@echo "Docker image built with China mirrors: anyproxy:$(VERSION)"

docker-run: docker-run-gateway docker-run-client ## Run with Docker

docker-run-gateway: docker-build ## Run with Docker
	@echo "Starting services with Docker..."
	@docker run -d --name anyproxy-gateway \
		-p 8080:8080 -p 1080:1080 -p 8443:8443 -p 8090:8090 \
		-v $(shell pwd)/configs:/app/configs:ro \
		-v $(shell pwd)/logs:/app/logs \
		-v $(shell pwd)/certs:/app/certs:ro \
		anyproxy:$(VERSION) ./anyproxy-gateway --config configs/config.yaml
	@echo "Gateway started. Use 'docker logs anyproxy-gateway' to view logs"

docker-run-client: docker-build ## Run with Docker
	@echo "Starting services with Docker..."
	@docker run -d --name anyproxy-client \
		-p 8091:8091 \
		--network host \
		-v $(shell pwd)/configs:/app/configs:ro \
		-v $(shell pwd)/logs:/app/logs \
		-v $(shell pwd)/certs:/app/certs:ro \
		anyproxy:$(VERSION) ./anyproxy-client --config configs/config.yaml
	@echo "Client started. Use 'docker logs anyproxy-client' to view logs"

docker-stop: ## Stop Docker containers
	@echo "Stopping Docker containers..."
	@docker stop anyproxy-gateway anyproxy-client 2>/dev/null || true
	@docker rm anyproxy-gateway anyproxy-client 2>/dev/null || true
	@echo "Containers stopped"

docker-logs: ## View Docker logs
	@docker logs -f anyproxy-gateway

clean: ## Clean build artifacts
	@echo "Cleaning build artifacts..."
	@rm -rf $(BUILD_DIR) build coverage.out coverage.html
	@docker stop anyproxy-gateway 2>/dev/null || true
	@docker rm anyproxy-gateway 2>/dev/null || true
	@echo "Clean completed"

deps: ## Download dependencies
	@echo "Downloading dependencies..."
	@go mod download
	@go mod verify
	@echo "Dependencies downloaded"

fmt: ## Format code
	@echo "Formatting code..."
	@go fmt ./...
	@echo "Code formatted"

vet: ## Run go vet
	@echo "Running go vet..."
	@go vet ./...
	@echo "go vet completed"

check: fmt vet lint test ## Run all checks (format, vet, lint, test)
