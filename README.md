# yagp

<div align="center">

**供管理员集中管理多个 Gost v3 实例的内部面板**

[![License](https://img.shields.io/badge/license-MIT-green.svg)](./LICENSE)
[![Docker](https://img.shields.io/badge/docker-ready-blue.svg)](https://www.docker.com/)
[![Go](https://img.shields.io/badge/Go-1.23+-00ADD8.svg)](https://golang.org/)

</div>

yagp 基于原作者 **code-gopher** 开发的 Gost Panel，沿用 MIT 许可证。[上游项目](https://github.com/code-gopher/gostPanel) · [许可证](./LICENSE)

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

安装脚本适用于使用 systemd 且内存大于 128 MB 的 Linux 服务器。脚本默认从当前 GitHub 仓库 `hanzec/gostPanel` 下载最新发布包，也可通过 `YAGP_REPO=owner/repo` 指定发布来源。最新版本须包含对应架构的 `yagp-linux-<arch>.tar.gz`。等待发布包期间可以使用 Docker 或从源码构建。

在本项目根目录以 root 身份运行脚本。默认端口为 `39100`。

```bash
bash scripts/install_panel.sh
```

可指定自定义端口，例如 `8080`。

```bash
bash scripts/install_panel.sh install --port 8080
```

脚本将程序安装到 `/usr/local/bin/yagp`，服务单元为 `gost-panel.service`，配置位于 `/etc/gost-panel`。卸载面板时保留配置和数据库。

```bash
bash scripts/install_panel.sh uninstall
```

### Authentik 正向认证

1. 在 Authentik 创建 Proxy Provider，模式选 **Forward auth (single application)**，External host 设为面板的 HTTPS 地址。将应用授权给管理员组，例如 `yagp-admins`。
2. 在反向代理上对管理页面和 `/api/v1` 启用 forward auth。把 `/outpost.goauthentik.io/` 交给 Authentik outpost 处理登录、回调和退出。代理应使用认证子请求返回的 `X-Authentik-Username` 和 `X-Authentik-Groups` 覆盖客户端同名请求头，保留原始 `Host`，并设置外部协议的 `X-Forwarded-Proto`。[Authentik 正向认证配置](https://docs.goauthentik.io/add-secure-apps/providers/proxy/forward_auth)
3. 在 `config/config.yaml` 中填写面板实际看到的代理对端 IP 或 CIDR，并设定同一个管理员组。宿主机代理访问容器时，使用容器看到的代理源地址。只允许可信代理访问管理端口。

```yaml
authentik:
  trusted_proxies: ["127.0.0.1", "::1"]
  admin_group: "yagp-admins"
```

浏览器访问面板的 HTTPS 地址，由 Authentik 处理登录。面板读取管理员身份并执行组校验。

### 数据库

默认使用 SQLite。使用 MySQL 时，先创建数据库，并给面板账号分配该数据库的读写和建表权限。面板启动时创建表。连接串可通过 `YAGP_DATABASE_DSN` 注入，数据库类型也可通过 `YAGP_DATABASE_TYPE` 指定。

```yaml
database:
  type: mysql
  dsn: "yagp:<password>@tcp(mysql.internal:3306)/yagp?charset=utf8mb4&loc=Local"
```

面板备份功能适用于 SQLite。MySQL 使用数据库服务的备份机制。

### 启用节点注册和流量上报

为面板的节点 gRPC 监听器配置 TLS 证书。`report.address` 是节点注册、上报和获取 nft 模板时访问的 `主机:端口`，证书必须覆盖该地址中的主机名或 IP。将这个端口开放给节点。管理页面通过 Authentik 代理访问。

```yaml
report:
  listen: ":39101"
  address: "panel.internal.example:39101"
  cert_file: "/app/config/panel.crt"
  key_file: "/app/config/panel.key"
```

节点必须先通过一次性 token 注册。面板保存注册时提交的节点证书，并用它验证发往该节点桥的 TLS 连接。

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

1. 在 **节点管理** 添加实例，填写面板用于连接节点桥的地址和 gRPC 端口。多个实例部署到同一台服务器时，为每个实例分配独立端口。
2. 打开该实例的 **安装** 操作，点击 **签发注册令牌**，并在目标 Linux 服务器以 root 执行生成的命令。命令只传 `--panel-address` 和 `--token`。token 有效期为 15 分钟，成功注册后立即失效，重新签发会替换尚未使用的 token。
3. 节点在线后创建并启动转发规则。Gost 的本机观察器把累计统计交给桥接进程，桥接进程调用面板的 gRPC `Panel.Report`。面板计算本次增量，在同一笔事务内更新规则与节点的计数。

首次注册时，`yagp-node` 验证面板 TLS 证书，生成本机证书和私钥，再通过 `Panel.RegisterNode` 提交 token 和公有证书。面板返回节点 ID 和后续通信凭据，节点将其写入 `/etc/yagp-node/<ID>/node.yaml`。替换已有节点时，新节点注册成功后使用新凭据和证书。

Gost 的管理 Web API 绑定在节点本机，面板通过 `yagp-node` 的 TLS gRPC 接口下发配置。节点上报时使用注册获得的 ID 和凭据。[Gost 管理 API](https://v3.gost.run/en/tutorials/api/overview/) · [Gost 观察器](https://www.gost.run/en/concepts/observer/)

如果面板证书由私有 CA 签发，在运行安装命令前将 CA 证书放在节点的 `/etc/yagp-node/panel-ca.pem`，也可通过 `YAGP_PANEL_CA_FILE` 指定路径。安装脚本默认下载 `yagp-node` 发布包。自行构建时，在 systemd 主机使用 `make build-node-systemd`，在 OpenRC 主机使用 `make build-node`，并通过 `YAGP_NODE_BIN=/path/to/yagp-node` 指定对应的二进制。

安装脚本为每个实例启用 `yagp-node-<ID>.service`。节点桥启动前检查对应的 `yagp-gost-<ID>.service`，在服务或程序缺少时尝试补装，并等待本机 Gost API 就绪。Gost 程序从官方 GitHub Releases 下载。

如果注册后服务启动失败，使用 `journalctl -u yagp-node-<ID> -u yagp-gost-<ID>` 查看日志。配置已写入实例目录时，排除故障后执行 `systemctl enable --now yagp-node-<ID>`。如果注册已完成但本地配置未写入，请重新签发令牌。

执行 `bash scripts/install_node.sh uninstall --node-id <ID>` 可卸载实例并保留配置。在同一台服务器重新注册同一节点前，先卸载原实例并移走保留的目录。

### 查看和重启 Gost 服务

在 **节点管理 → 更多 → Gost 服务** 查看 `yagp-gost-<ID>.service` 的状态和最近 100 条日志，也可重启该实例。重启会中断该实例的活动转发连接。

该功能使用节点上的 `libsystemd.so.0`。发布包包含 systemd 和通用构建，安装脚本会按服务管理器选择。在 Linux glibc 主机可用 `make build-node-systemd` 构建 systemd 版本。

### 管理节点的 nftables 配置

在装有 `nftables` 的 Linux 节点上，通过 **节点管理 → 更多 → nft 配置** 编辑并应用原生 nftables JSON。默认模板由面板提供，包含一个无规则的 NAT `prerouting` 链，载入时只填入编辑区。节点也可运行 `/etc/yagp-node/<ID>/yagp-node nft-template -c /etc/yagp-node/<ID>/node.yaml`，经 TLS 从面板获取同一份模板。

配置使用 `{"nftables":[...]}`，只允许为该节点专用的 `inet yagp_node_<ID>` 表添加链、规则、集合等对象。节点先校验配置，再原子替换该表，把成功应用的配置保存到实例目录中的 `nft.json` 并在桥接服务启动时恢复。应用 `{"nftables":[]}` 会删除该表。卸载时安装脚本会清理该表并保留配置文件。[nftables JSON 参考](https://manpages.ubuntu.com/manpages/noble/man5/libnftables-json.5.html)

---

## 📦 预编译下载

发布包以 `yagp-` 开头。面板支持 Linux（amd64、arm64）、Windows（amd64）和 macOS（Apple Silicon、Intel）。节点安装包支持 Linux（amd64、arm64）。[查看当前仓库的 Releases](https://github.com/hanzec/gostPanel/releases)

面板和节点桥需要一起更新到使用 `yagp.node.v1` 的版本。更新后在面板中停止并重新启动已运行的转发规则，以更新 Gost 观察器凭据。未通过令牌注册的节点须重新注册。

### 自动版本号

推送到 `main` 时，GitHub Actions 使用该次推送的最后一个提交生成版本，并发布 GitHub Release 和同名的 GHCR 镜像标签。仓库默认分支为 `master` 时，推送到 `master` 也会触发发布。默认分支的最新提交可以更新 `latest`。

版本格式为 `x.y.z-sha`，例如 `0.1.67-a1b2c3d`。`x.y` 在仓库根目录的 `VERSION` 中维护，`z` 是沿第一父提交统计的主线提交数，`sha` 是提交 SHA 的前 7 位。用 `bash scripts/version.sh` 可查看当前检出提交的版本，需要完整 Git 历史。

面板仪表盘、`/api/v1/health`、`yagp --version` 和 `yagp-node --version` 使用相同的构建版本。

### 开发与编译

使用 Go 1.23+ 和 Node.js 20.19+（20.x）或 22.12+ 构建。Go 编译时会嵌入前端产物，首次编译后端前需先构建前端。

```bash
# 完整构建，产物为 yagp 和 yagp-node
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
./yagp -c config/config.yaml
```

---

## 🤝 声明
本项目仅供学习与科研使用，请在法律范围内使用。
