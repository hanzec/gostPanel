# ycgp

<div align="center">

**供管理员集中管理多个 Gost v3 实例的内部面板**

[![License](https://img.shields.io/badge/license-MIT-green.svg)](./LICENSE)
[![Docker](https://img.shields.io/badge/docker-ready-blue.svg)](https://www.docker.com/)
[![Go](https://img.shields.io/badge/Go-1.23+-00ADD8.svg)](https://golang.org/)

</div>

ycgp 基于原作者 **code-gopher** 开发的 Gost Panel，沿用 MIT 许可证。[上游项目](https://github.com/code-gopher/gostPanel) · [许可证](./LICENSE)

改名、管理员认证、数据库和节点通信改造使用了 OpenAI Codex 辅助。[AGENTS.md](./AGENTS.md)

## 🚀 核心特性

- ⚡ **极简管理** - 统一管理多个 Gost 节点及其转发规则。
- 🔗 **多跳隧道** - 支持复杂的出口/中转隧道配置（入口 -> 出口 -> 目标）。
- 📊 **实时延迟** - 每 5 秒自动检测节点与转发链路的 Ping 值。
- 🛡️ **管理员访问** - 使用 Authentik 正向认证和管理员组，操作日志按管理员用户名记录。
- 📈 **流量统计** - 节点通过 TLS gRPC 上报流量，面板累计每条规则和每个入口节点的流量。
- 🐳 **快速部署** - 全量支持 Docker 一键运行及 GitHub Action 自动构建。

## 🛠️ 快速开始

### 部署面板

**方式 A · Docker（推荐）**

在本项目根目录使用 Docker Compose 从源码构建并启动面板。

```bash
docker compose up -d --build
```

构建环境如需使用 Go 模块代理，可通过 `docker compose build --build-arg GOPROXY=<代理地址>` 指定，再执行 `docker compose up -d`。

Compose 将管理端口 `39100` 发布到宿主机的 `127.0.0.1`，将节点流量上报端口 `39101` 发布到宿主机。`config/` 以只读方式挂载到 `/app/config`。部署前配置下面的 Authentik、数据库和节点上报参数。

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

脚本将程序安装到 `/usr/local/bin/ycgp`，服务单元为 `gost-panel.service`，配置位于 `/etc/gost-panel`。卸载面板时保留配置和数据库。

```bash
bash scripts/install_panel.sh uninstall
```

### Authentik 正向认证

1. 在 Authentik 创建 Proxy Provider，模式选 **Forward auth (single application)**，External host 设为面板的 HTTPS 地址。将应用授权给管理员组，例如 `ycgp-admins`。
2. 在反向代理上对管理页面和 `/api/v1` 启用 forward auth。把 `/outpost.goauthentik.io/` 交给 Authentik outpost 处理登录、回调和退出。代理应使用认证子请求返回的 `X-Authentik-Username` 和 `X-Authentik-Groups` 覆盖客户端同名请求头，保留原始 `Host`，并设置外部协议的 `X-Forwarded-Proto`。[Authentik 正向认证配置](https://docs.goauthentik.io/add-secure-apps/providers/proxy/forward_auth)
3. 在 `config/config.yaml` 中填写面板实际看到的代理对端 IP 或 CIDR，并设定同一个管理员组。宿主机代理访问容器时，使用容器看到的代理源地址。只允许可信代理访问管理端口。

```yaml
authentik:
  trusted_proxies: ["127.0.0.1", "::1"]
  admin_group: "ycgp-admins"
```

浏览器访问面板的 HTTPS 地址，由 Authentik 处理登录。面板读取管理员身份并执行组校验。

### 数据库

默认使用 SQLite。使用 MySQL 时，先创建数据库，并给面板账号分配该数据库的读写和建表权限。面板启动时创建表。连接串可通过 `YCGP_DATABASE_DSN` 注入，数据库类型也可通过 `YCGP_DATABASE_TYPE` 指定。

```yaml
database:
  type: mysql
  dsn: "ycgp:<password>@tcp(mysql.internal:3306)/ycgp?charset=utf8mb4&loc=Local"
```

面板备份功能适用于 SQLite。MySQL 使用数据库服务的备份机制。

### 启用节点流量上报

为面板上报监听器配置 TLS 证书。`report.address` 是节点使用的地址，证书必须覆盖其中的主机名。将端口开放给 Gost 节点。管理页面仍通过 Authentik 代理访问。

```yaml
report:
  listen: ":39101"
  address: "panel.internal.example:39101"
  cert_file: "/app/config/panel.crt"
  key_file: "/app/config/panel.key"
node_tls:
  ca_file: "/app/config/node-ca.crt"
```

`node_tls.ca_file` 用于面板验证节点桥的 TLS 证书。使用系统信任的签发机构时可留空。

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

## 📡 添加 Gost 实例

1. 在 **节点管理** 添加实例，填写节点桥的地址、gRPC 端口和认证凭据。
2. 打开该实例的 **安装** 操作，填写证书和私钥路径，并在目标服务器执行生成的命令。多个实例部署到同一台服务器时，为每个实例分配独立端口。
3. 节点在线后创建并启动转发规则。Gost 的本机观察器把累计统计交给桥接进程，桥接进程调用面板的 gRPC `Panel.Report`。面板计算本次增量，在同一笔事务内更新规则与节点的计数。

Gost 的管理 Web API 绑定在节点本机，面板通过 `ycgp-node` 的 TLS gRPC 接口下发配置。同一节点的上报使用其面板登记的 ID 和凭据。[Gost 管理 API](https://v3.gost.run/en/tutorials/api/overview/) · [Gost 观察器](https://www.gost.run/en/concepts/observer/)

使用私有 CA 签发面板证书时，在安装窗口填写节点上的上报 CA 文件路径。安装脚本会下载 `ycgp-node` 发布包，也可在目标节点用 `YCGP_NODE_BIN=/path/to/ycgp-node` 指定已构建的二进制。卸载某个实例时，执行 `bash scripts/install_node.sh uninstall --node-id <ID>`。

---

## 📦 预编译下载

新版发布包以 `ycgp-` 开头。发布流程支持 Linux（amd64、arm64）、Windows（amd64）和 macOS（Apple Silicon、Intel）。[查看当前仓库的 Releases](https://github.com/hanzec/gostPanel/releases)

### 开发与编译

使用 Go 1.23+ 和 Node.js 20.19+（20.x）或 22.12+ 构建。Go 编译时会嵌入前端产物，首次编译后端前需先构建前端。

```bash
# 完整构建，产物为 ycgp 和 ycgp-node
make build

# 只构建前端
make build-web

# 只构建后端
make build-server

# 只构建节点桥
make build-node

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
