# ycgp

<div align="center">

**现代化 Gost v3 端口转发管理面板**

[![License](https://img.shields.io/badge/license-MIT-green.svg)](./LICENSE)
[![Docker](https://img.shields.io/badge/docker-ready-blue.svg)](https://www.docker.com/)
[![Go](https://img.shields.io/badge/Go-1.23+-00ADD8.svg)](https://golang.org/)

</div>

ycgp 基于原作者 **code-gopher** 开发的 Gost Panel，沿用 MIT 许可证。[上游项目](https://github.com/code-gopher/gostPanel) · [许可证](./LICENSE)

## 🚀 核心特性

- ⚡ **极简管理** - 统一管理多个 Gost 节点及其转发规则。
- 🔗 **多跳隧道** - 支持复杂的出口/中转隧道配置（入口 -> 出口 -> 目标）。
- 📊 **实时延迟** - 每 5 秒自动检测节点与转发链路的 Ping 值。
- 🛡️ **安全可靠** - 支持 API 认证，内置操作审计日志。
- 🐳 **快速部署** - 全量支持 Docker 一键运行及 GitHub Action 自动构建。

## 🛠️ 快速开始

### 1. 部署面板

**方式 A · Docker（推荐）**

在本项目根目录使用 Docker Compose 从源码构建并启动面板。

```bash
docker compose up -d --build
```

构建环境如需使用 Go 模块代理，可通过 `docker compose build --build-arg GOPROXY=<代理地址>` 指定，再执行 `docker compose up -d`。

面板默认监听 `39100`。从旧版 Compose 升级时，先用旧配置执行 `docker compose down`，再在同一目录替换 Compose 文件并启动，以继续使用现有的 `data` 和 `logs`。如果已经替换配置，可在同一 Compose 项目执行 `docker compose down --remove-orphans` 后再启动。

**方式 B · Linux 二进制部署**

安装脚本适用于使用 systemd 且内存大于 128 MB 的 Linux 服务器。脚本默认从当前 GitHub 仓库 `hanzec/gostPanel` 下载最新发布包，也可通过 `YCGP_REPO=owner/repo` 指定发布来源。最新版本须包含对应架构的 `ycgp-linux-<arch>.tar.gz`。等待发布包期间可以使用 Docker 或从源码构建。

在本项目根目录以 root 身份运行脚本。默认端口为 `39100`。

```bash
bash scripts/install_panel.sh
```

可指定自定义端口，例如 `8080`。

```bash
bash scripts/install_panel.sh install --port 8080
```

脚本将程序安装到 `/usr/local/bin/ycgp`，沿用 `gost-panel.service` 和 `/etc/gost-panel` 以便原地升级。卸载面板时保留配置和数据库。

```bash
bash scripts/install_panel.sh uninstall
```

---

## 📸 上游界面预览

<table>
  <tr>
    <td align="center">
      <h3>仪表板</h3>
      <img src="./docs/screenshots/dash.png" alt="仪表板" width="400"/>
    </td>
    <td align="center">
      <h3>节点管理</h3>
      <img src="./docs/screenshots/node.png" alt="节点管理" width="400"/>
    </td>
  </tr>
  <tr>
    <td align="center">
      <h3>转发管理</h3>
      <img src="./docs/screenshots/forwards.png" alt="转发管理" width="400"/>
    </td>
    <td align="center">
      <h3>隧道管理</h3>
      <img src="./docs/screenshots/tunnels.png" alt="隧道管理" width="400"/>
    </td>
  </tr>
</table>

---

## 📡 添加转发节点

1. 在面板导航至 **节点管理**。
2. 点击已有节点的 **安装** 按钮，或者点击 **添加节点**。
3. 复制生成的安装命令，在目标服务器上运行即可完成自动化部署。

面板生成的命令使用上游 Gost Panel 的脚本安装 Gost 节点。也可将本仓库的 `scripts/install_node.sh` 复制到目标服务器运行。卸载节点时，以 root 身份在脚本所在目录执行 `bash install_node.sh uninstall`。

---

## 📦 预编译下载

新版发布包以 `ycgp-` 开头。发布流程支持 Linux（amd64、arm64）、Windows（amd64）和 macOS（Apple Silicon、Intel）。[查看当前仓库的 Releases](https://github.com/hanzec/gostPanel/releases)

### 开发与编译

使用 Go 1.23+ 和 Node.js 20.19+（20.x）或 22.12+ 构建。Go 编译时会嵌入前端产物，首次编译后端前需先构建前端。

```bash
# 完整构建，产物为 ycgp
make build

# 只构建前端
make build-web

# 只构建后端
make build-server

# 清理构建产物
make clean

# 构建多平台发布版本
make release
```

在项目根目录创建数据库目录，再启动构建好的 Linux 或 macOS 程序。

```bash
mkdir -p data
./ycgp -c config/config.yaml
```

---

## 🤝 声明
本项目仅供学习与科研使用，请在法律范围内使用。
