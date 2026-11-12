# 前端构建阶段
FROM node:26.13.0-alpine AS web-builder
WORKDIR /app/web
COPY web/package*.json ./
RUN npm ci
COPY web/index.html web/vite.config.js ./
COPY web/src/ ./src/
COPY web/public/ ./public/
COPY pkg/gost/relay_options.json /app/pkg/gost/relay_options.json
RUN npm run build

# 后端构建阶段
FROM golang:1.27.3-alpine AS backend-builder
WORKDIR /app

# 版本号参数（通过 --build-arg VERSION=xxx 传入）
ARG VERSION=dev

# 复制依赖文件
ARG GOPROXY
COPY go.mod go.sum ./
RUN go mod download

# 复制后端源码
COPY cmd/ cmd/
COPY internal/ internal/
COPY pkg/ pkg/

# 从前端构建阶段复制 dist 到后端 embedding 路径
# 注意：前端构建输出到了 ../internal/router/dist，即 /app/internal/router/dist
COPY --from=web-builder /app/internal/router/dist ./internal/router/dist

# 编译（关闭 CGO，使用纯 Go SQLite 驱动，注入版本号）
RUN CGO_ENABLED=0 GOOS=linux \
    go build -ldflags="-s -w -X 'yagp/internal/buildinfo.Version=${VERSION}'" -o yagp ./cmd/server

# 运行阶段
FROM alpine:latest
LABEL org.opencontainers.image.title="yagp" \
      org.opencontainers.image.description="Yet Another Gost Panel"
WORKDIR /app
RUN apk --no-cache add ca-certificates tzdata
ENV TZ=Asia/Shanghai

# 复制程序和配置
COPY --from=backend-builder /app/yagp .
COPY config/config.yaml ./config/config.yaml
COPY LICENSE ./LICENSE

# 创建目录
RUN mkdir -p /app/data /app/logs

EXPOSE 39100 39101

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -q -O /dev/null http://127.0.0.1:39100/api/v1/health

CMD ["./yagp"]
