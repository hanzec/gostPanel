# 版本信息
ifndef VERSION
VERSION := $(shell bash scripts/version.sh)
endif
ifeq ($(strip $(VERSION)),)
$(error 无法生成构建版本)
endif
LDFLAGS := -s -w -X 'yagp/internal/buildinfo.Version=$(VERSION)'

.PHONY: all build build-web build-server build-node build-node-systemd clean dev help release

# 默认目标
all: build

# 帮助信息
help:
	@echo "Available commands:"
	@echo "  make build          - Build both web and server"
	@echo "  make build-web      - Build web frontend only"
	@echo "  make build-server   - Build server backend only"
	@echo "  make build-node     - Build the Gost node daemon"
	@echo "  make build-node-systemd - Build the Linux glibc daemon with libsystemd support"
	@echo "  make dev            - Run in development mode"
	@echo "  make run            - Build web and run server"
	@echo "  make clean          - Clean build artifacts"
	@echo "  make release        - Build multi-platform release"
	@echo ""
	@echo "💡 Tip for Windows users: Run these commands in Git Bash or WSL for compatibility."

# 完整构建
build: build-web build-server build-node
	@echo "Build complete! Binaries: yagp and yagp-daemon"

# 构建前端
build-web:
	@echo "Building web..."
	cd web && npm install && npm run build
	@echo "Web build complete"
	
# 构建后端（包含嵌入的前端）
build-server:
	@echo "Building server..."
	go build -ldflags="$(LDFLAGS)" -o yagp ./cmd/server
	@echo "Server build complete"

build-node:
	go build -ldflags="$(LDFLAGS)" -o yagp-daemon ./cmd/yagp-daemon

build-node-systemd:
	CGO_ENABLED=0 go build -tags libsystemd -ldflags="$(LDFLAGS)" -o yagp-daemon ./cmd/yagp-daemon

# 运行（构建前端并启动后端）
run: build-web
	@echo "Starting server..."
	go run -ldflags="$(LDFLAGS)" ./cmd/server

# 构建多平台发布版本
release: build-web
	@echo "Building release binaries..."
	GOOS=linux GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o yagp-linux-amd64 ./cmd/server
	GOOS=linux GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o yagp-linux-arm64 ./cmd/server
	GOOS=darwin GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o yagp-darwin-amd64 ./cmd/server
	GOOS=darwin GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o yagp-darwin-arm64 ./cmd/server
	GOOS=windows GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o yagp-windows-amd64.exe ./cmd/server
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o yagp-daemon-linux-amd64 ./cmd/yagp-daemon
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o yagp-daemon-linux-arm64 ./cmd/yagp-daemon
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -tags libsystemd -ldflags="$(LDFLAGS)" -o yagp-daemon-systemd-linux-amd64 ./cmd/yagp-daemon
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -tags libsystemd -ldflags="$(LDFLAGS)" -o yagp-daemon-systemd-linux-arm64 ./cmd/yagp-daemon
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o yagp-depoly-linux-amd64 ./cmd/yagp-depoly
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o yagp-depoly-linux-arm64 ./cmd/yagp-depoly
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o yagp-update-linux-amd64 ./cmd/yagp-update
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o yagp-update-linux-arm64 ./cmd/yagp-update
	@echo "Release build complete"

# 清理构建产物
clean:
	@echo "Cleaning artifacts..."
	rm -f yagp
	rm -f yagp.exe
	rm -f yagp-daemon
	rm -f yagp-daemon.exe
	rm -f main
	rm -f main.exe
	rm -rf internal/router/dist
	rm -rf web/dist
