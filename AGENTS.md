# yagp（Yet Another Gost Panel）仓库指南

## 项目来源与 AI 使用

yagp 基于原作者 **code-gopher** 开发的 Gost Panel，沿用 MIT 许可证。修改或分发时保留原始版权声明和许可文本。[上游项目](https://github.com/code-gopher/gostPanel) · [许可证](./LICENSE)

项目改名、移除推广、管理员认证、数据库与节点通信改造使用了 OpenAI Codex 辅助。

## 分支与合并

- `master` 是本仓库的远端默认分支，也是日常改动的目标。从最新的 `origin/master` 创建功能分支，推送功能分支后创建 PR，并显式指定 `--base master`。
- `main` 是历史分支，推送到它仍会触发发布流程。如果请求仅写了「推送到 main」或把 `main` 当作默认分支，先指出本仓库使用 `master`，等用户确认本次确实要操作历史 `main` 后再执行。创建 `main`、更改默认分支或以其他分支为 PR 合并目标也须明确授权。
- 推送前用 `git remote -v`、`git status --short --branch` 和 `git ls-remote --symref origin HEAD` 核对远端、当前分支和远端默认分支，并检查推送命令的目标引用。合并前确认 PR 的 `baseRefName` 为 `master`，且必需的 CI 检查已通过。

## 开发与验证

- `cmd/server` 是面板入口，`cmd/yagp-daemon` 是节点守护程序入口，`internal` 包含 API、业务逻辑和持久化代码。`web` 包含 Vue 前端。
- 项目名使用 `yagp`，转发节点继续使用 Gost 名称。涉及构建产物命名时，同步核对 Makefile、Dockerfile、发布工作流和面板安装脚本。
- 使用 `go.mod` 和 `.node-version` 指定的 Go 1.27.3 与 Node.js 26.13.0 LTS。前端产物输出到 `internal/router/dist`，由 Go 编译时嵌入。验证后端前先构建前端。

```bash
npm --prefix web ci
npm --prefix web run build
go test ./...
```

使用 `make build` 从源码构建完整面板，或用 `docker compose up -d` 启动已发布的镜像。

## 实现原则

- 按明确需求控制改动范围。不要为猜测的低概率竞态或安全边界增加复杂度。认为有必要时，先说明具体失败方式、发生概率和新增代码量，取得同意后再做。
- 外部数据只在入口校验一次，内部传递已校验的类型和值。
- 只在逻辑可复用、确实复杂或能代表清晰概念时提取函数。主函数只写正常流程，把校验和细节放在下方的小函数中。
- 注释说明不明显的约束或反直觉的行为。
- 修改生成文件的源头，然后重新生成。
- 新增功能或修复缺陷时，必须在同一变更中添加对应的测试用例。功能测试覆盖新增行为，缺陷修复补上能复现原问题的回归测试。
- 测试真实实现，避免在测试中重写业务逻辑，尽量少用 mock。
- 沿用仓库现有工具和流程。
