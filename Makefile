# 版本信息
VERSION ?= $(shell git describe --tags --always)
LDFLAGS := -s -w -X 'ycgp/internal/config.Version=$(VERSION)'

.PHONY: all build build-web build-server build-node clean dev help release

# 默认目标
all: build

# 帮助信息
help:
	@echo "Available commands:"
	@echo "  make build          - Build both web and server"
	@echo "  make build-web      - Build web frontend only"
	@echo "  make build-server   - Build server backend only"
	@echo "  make build-node     - Build the Gost node bridge"
	@echo "  make dev            - Run in development mode"
	@echo "  make run            - Build web and run server"
	@echo "  make clean          - Clean build artifacts"
	@echo "  make release        - Build multi-platform release"
	@echo ""
	@echo "💡 Tip for Windows users: Run these commands in Git Bash or WSL for compatibility."

# 完整构建
build: build-web build-server build-node
	@echo "Build complete! Binaries: ycgp and ycgp-node"

# 构建前端
build-web:
	@echo "Building web..."
	cd web && npm install && npm run build
	@echo "Web build complete"
	
# 构建后端（包含嵌入的前端）
build-server:
	@echo "Building server..."
	go build -ldflags="$(LDFLAGS)" -o ycgp cmd/server/main.go
	@echo "Server build complete"

build-node:
	go build -ldflags="-s -w" -o ycgp-node ./cmd/ycgp-node

# 运行（构建前端并启动后端）
run: build-web
	@echo "Starting server..."
	go run cmd/server/main.go

# 构建多平台发布版本
release: build-web
	@echo "Building release binaries..."
	GOOS=linux GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o ycgp-linux-amd64 cmd/server/main.go
	GOOS=linux GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o ycgp-linux-arm64 cmd/server/main.go
	GOOS=darwin GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o ycgp-darwin-amd64 cmd/server/main.go
	GOOS=darwin GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o ycgp-darwin-arm64 cmd/server/main.go
	GOOS=windows GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o ycgp-windows-amd64.exe cmd/server/main.go
	GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o ycgp-node-linux-amd64 ./cmd/ycgp-node
	GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o ycgp-node-linux-arm64 ./cmd/ycgp-node
	@echo "Release build complete"

# 清理构建产物
clean:
	@echo "Cleaning artifacts..."
	rm -f ycgp
	rm -f ycgp.exe
	rm -f ycgp-node
	rm -f ycgp-node.exe
	rm -f main
	rm -f main.exe
	rm -rf internal/router/dist
	rm -rf web/dist
