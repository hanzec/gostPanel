# ycgp 仓库指南

## 项目来源与 AI 使用

ycgp 基于原作者 **code-gopher** 开发的 Gost Panel，沿用 MIT 许可证。修改或分发时保留原始版权声明和许可文本。[上游项目](https://github.com/code-gopher/gostPanel) · [许可证](./LICENSE)

本次项目改名、移除推广、更新文档和验证使用了 OpenAI Codex 辅助。

## 开发与验证

- `cmd/server` 是 Go 服务入口，`internal` 包含 API、业务逻辑和持久化代码。`web` 包含 Vue 前端。
- 项目名使用 `ycgp`，转发节点继续使用 Gost 名称。涉及构建产物命名时，同步核对 Makefile、Dockerfile、发布工作流和面板安装脚本。
- 使用 Go 1.23+ 和 Node.js 20.19+（20.x）或 22.12+。前端产物输出到 `internal/router/dist`，由 Go 编译时嵌入。验证后端前先构建前端。

```bash
npm --prefix web ci
npm --prefix web run build
go test ./...
```

使用 `make build` 构建完整面板，或用 `docker compose up -d --build` 从源码启动容器。

## 升级兼容性

- 保留 SQLite 默认路径 `./data/gost-panel.db`、备份前缀 `gost_panel_`、JWT 签发者 `gost-panel`，以及原生安装使用的 `/etc/gost-panel` 和 `gost-panel.service`。
- 启动时将仍采用原始默认标题和版权设置的站点标题更新为 `ycgp`，保留自定义站点标题。
