# yagp

<div align="center">

**供管理员集中管理多个 Gost v3 实例的内部面板**

[![License](https://img.shields.io/badge/license-MIT-green.svg)](./LICENSE)
[![Docker](https://img.shields.io/badge/docker-ready-blue.svg)](https://www.docker.com/)
[![Go](https://img.shields.io/badge/Go-1.27.3-00ADD8.svg)](https://go.dev/)

</div>

yagp 基于原作者 **code-gopher** 开发的 Gost Panel，沿用 MIT 许可证。[上游项目](https://github.com/code-gopher/gostPanel) · [许可证](./LICENSE)

改名、管理员认证、数据库和节点通信改造使用了 OpenAI Codex 辅助。[AGENTS.md](./AGENTS.md)

## 节点一键安装与升级

### 安装新节点

先在面板配置 [TLS 注册和上报地址](#启用节点注册和流量上报)。在 **节点管理 → 添加节点** 填写 gRPC 端口。同一台服务器上的多个节点使用不同端口。配置了 `report.address` 后，添加节点会打开安装窗口并生成一次性令牌。点击 **复制命令**，在目标 Linux 主机以 root 身份执行。

也可用下面的模板，把地址和令牌替换为面板提供的值。令牌有效期为 15 分钟，注册成功后立即失效。重新签发会撤销上一枚未使用的令牌。[节点安装脚本](./scripts/install_node.sh)

```bash
curl -fsSL https://raw.githubusercontent.com/hanzec/gostPanel/HEAD/scripts/install_node.sh \
  | bash -s -- --panel-address 'panel.example.com:443' --token 'TOKEN_FROM_PANEL'
```

### 升级已安装节点

在使用 systemd 且系统 D-Bus 可用的节点上，以 root 身份执行以下命令，将 `42` 换成面板中的节点 ID。升级只替换 `yagp-daemon`，原配置、凭据和证书可继续使用，无需重新注册。

```bash
curl -fsSL https://raw.githubusercontent.com/hanzec/gostPanel/HEAD/scripts/install_node.sh \
  | bash -s -- upgrade --node-id 42
```

## 🚀 核心特性

- ⚡ **极简管理** - 统一管理多个 Gost 节点及其转发规则。
- 🔗 **多跳隧道** - 支持复杂的出口/中转隧道配置（入口 -> 出口 -> 目标）。
- 📊 **实时延迟** - 每 5 秒自动检测节点与转发链路的 Ping 值。
- 🛡️ **管理员访问** - 使用 Authentik 正向认证和管理员组，操作日志按管理员用户名记录。
- 📈 **流量统计与配额** - 节点通过 TLS gRPC 上报流量，可为每个节点单独设置转发流量上限和重置周期。
- 🐳 **快速部署** - 全量支持 Docker 一键运行及 GitHub Action 自动构建。

## 🛠️ 快速开始

### 部署面板

**方式 A · Docker（推荐）**

此方式接入已有的 Traefik（v3.4 或更高版本，启用 Docker provider）和 Authentik Outpost。将面板域名指向 Traefik，并为其配置 HTTPS 证书。Traefik 和面板加入同一个 Docker 网络，Traefik 还须能访问 Outpost 的内部地址。

在本项目根目录创建 `.env`，将域名、网络名和 Outpost 地址换成实际值。Outpost 地址包含协议和端口，末尾不加 `/`；`websecure` 是 Traefik 的 HTTPS 入口名。

```dotenv
YAGP_HOST=panel.example.com
TRAEFIK_NETWORK=traefik
TRAEFIK_HTTPS_ENTRYPOINT=websecure
AUTHENTIK_OUTPOST_URL=http://authentik-server:9000
```

按下文配置 [Authentik 正向认证](#authentik-正向认证) 和 [节点注册与上报](#启用节点注册和流量上报)，再从当前目录构建并启动：

```bash
docker compose up -d --build
```

构建环境如需使用 Go 模块代理，可通过 `docker compose build --build-arg GOPROXY=<代理地址>` 指定，再执行 `docker compose up -d`。

Compose 将当前目录的 `config/`、`data/` 和 `logs/` 分别绑定到容器中的 `/app/config`、`/app/data` 和 `/app/logs`。`config/` 只读，数据库和日志可写。从其他目录使用 `-f` 指定 Compose 文件时，确保 `PWD` 环境变量指向所需目录，并在其中准备 `config/config.yaml`（PowerShell 可用 `$env:PWD = (Get-Location).Path`）。如果未设置 `PWD`，则使用 Compose 项目目录。

面板网页、API 和节点 gRPC 共用 Traefik 的 HTTPS 域名和 `443` 端口。Compose 不向宿主机发布面板的 `39100` 或 `39101` 端口。

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

1. 在 Authentik 创建 Proxy Provider，模式选 **Forward auth (single application)**，External host 设为面板的 HTTPS 地址。将应用授权给管理员组，例如 `yagp-admins`，并将其分配给所用的 Outpost。
2. Compose 已配置 Traefik 对面板启用 ForwardAuth，并把 `/outpost.goauthentik.io/` 直接交给 Outpost 处理登录、回调和退出。Traefik 应只信任实际在它前面的代理发来的转发头，不要开启入口的 `forwardedHeaders.insecure`。如使用二进制部署，需在自己的反向代理上配置这两条路由，保留原始 `Host` 和外部协议 `X-Forwarded-Proto`，并用认证响应覆盖 `X-Authentik-Username` 和 `X-Authentik-Groups`。[Authentik 的 Traefik 配置](https://docs.goauthentik.io/add-secure-apps/providers/proxy/server_traefik/)
3. 在 `config/config.yaml` 中填入面板实际看到的代理源 IP，并设定同一个管理员组。使用 Compose 时填写 Traefik 在共享 Docker 网络上的 IP（建议固定该地址），不要将整个共享网络设为可信代理。以下为示例；代理确实在本机访问面板时才填 `127.0.0.1` 或 `::1`。

```yaml
authentik:
  trusted_proxies: ["172.30.0.2"]
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

`report.address` 是节点注册、上报和获取 nft 模板时使用的 `主机:端口`，无需添加 `https://` 或路径。

**Docker Compose：** Traefik 在 `443` 终止 TLS，把 `/yagp.node.v1.Panel/` 下的原生 gRPC 请求原样转发到 Docker 网络内的 `39101`（HTTP/2，即 h2c）。该路由使用节点认证，网页和 API 仍使用 Authentik。面板无需单独的上报证书。把 `proxy_ip` 设为 Traefik 在共享网络上的固定 IP，面板只接受该 IP 发来的 gRPC 回源请求。[Traefik 的 gRPC 配置](https://doc.traefik.io/traefik/expose/overview/#exposing-grpc-services)

```yaml
report:
  listen: ":39101"
  address: "panel.example.com:443"
  proxy_ip: "172.30.0.2"
```

迁移已有节点时，将 `/etc/yagp-daemon/<ID>/node.yaml` 的 `report_address` 改为新地址，再重启该节点的 daemon 服务。若改用公有 CA 签发的证书，清除原来的 `report_ca_file`，让节点使用系统 CA。节点 ID、凭据和节点证书可以继续使用，无需重新注册。

**二进制直连部署：** 不设置 `proxy_ip`，给 gRPC 监听器配置证书和私钥。证书须覆盖 `report.address` 中的主机名或 IP，并将监听端口开放给节点。

```yaml
report:
  listen: ":39101"
  address: "panel.internal.example:39101"
  cert_file: "/etc/gost-panel/panel.crt"
  key_file: "/etc/gost-panel/panel.key"
```

节点先通过一次性 token 注册，之后使用面板发放的节点凭据。面板保存注册时提交的节点证书，并用它验证发往该节点桥的独立 TLS 连接。

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

按 [一键安装步骤](#安装新节点) 添加实例。需要重新生成命令时，可在该实例的 **安装** 窗口点击 **签发注册令牌**。

节点在线后即可创建并启动转发规则。Gost 的本机观察器把累计统计交给桥接进程，桥接进程调用面板的 gRPC `Panel.Report`。面板计算本次增量，在同一笔事务内更新规则与节点的计数。

首次注册时，`yagp-daemon` 验证面板 TLS 证书，生成本机证书和私钥，再通过 `Panel.RegisterNode` 提交 token 和公有证书。面板返回节点 ID 和后续通信凭据，节点将其写入 `/etc/yagp-daemon/<ID>/node.yaml`。替换已有节点时，新节点注册成功后使用新凭据和证书。

参照 Komari，`yagp-daemon` 在注册前通过 ipify 的 HTTPS 接口分别探测公网 IPv4 和 IPv6，并随注册请求上报。探测最多等待 3 秒。两种地址均未获取时，面板使用可信 Traefik 转发的节点 IP，或在直连部署中使用注册连接的来源 IP。面板优先使用记录到的 IPv4，其次使用 IPv6。节点的 gRPC 端口须能通过该地址从面板访问。[Komari 节点探测](https://github.com/komari-monitor/komari-agent/blob/ddbdbdcdaeb12abc0e68ba5bc2f0a6b914dad3a7/monitoring/unit/ip.go#L14-L152) · [ipify 接口](https://www.ipify.org/#api-usage)

`yagp-daemon` 启动时立即探测一次，随后每 5 分钟探测。节点用现有凭据通过 TLS 上报地址变化，面板自动更新连接地址。某一地址族探测失败时保留其上次记录；上报失败则在下一轮重试。[Komari 上报周期](https://komari-monitor.github.io/komari-document/dev/agent.html)

Gost 的管理 Web API 绑定在节点本机，面板通过 `yagp-daemon` 的 TLS gRPC 接口下发配置。节点上报时使用注册获得的 ID 和凭据。[Gost 管理 API](https://v3.gost.run/en/tutorials/api/overview/) · [Gost 观察器](https://www.gost.run/en/concepts/observer/)

如果面板证书由私有 CA 签发，在运行安装命令前将 CA 证书放在节点的 `/etc/yagp-daemon/panel-ca.pem`，也可通过 `YAGP_PANEL_CA_FILE` 指定路径。安装脚本默认下载最新版 `yagp-daemon-linux-<架构>.tar.gz`，可用 `YAGP_DAEMON_VERSION` 指定发布版本。自行构建时，在 systemd 主机使用 `make build-node-systemd`，在 OpenRC 主机使用 `make build-node`，并通过 `YAGP_DAEMON_BIN=/path/to/yagp-daemon` 指定对应的二进制。

安装脚本为每个实例启用 `yagp-daemon-<ID>.service`。守护程序启动前检查对应的 `yagp-gost-<ID>.service`，在服务或程序缺少时尝试补装，并等待本机 Gost API 就绪。Gost 程序从官方 GitHub Releases 下载。

如果注册后服务启动失败，使用 `journalctl -u yagp-daemon-<ID> -u yagp-gost-<ID>` 查看日志。配置已写入实例目录时，排除故障后执行 `systemctl enable --now yagp-daemon-<ID>`。如果注册已完成但本地配置未写入，请重新签发令牌。

执行 `bash scripts/install_node.sh uninstall --node-id <ID>` 可卸载实例并保留配置。在同一台服务器重新注册同一节点前，先卸载原实例并移走保留的目录。

### 升级已注册的节点

在 **节点管理 → 更多 → 版本与升级** 查看 Gost 和 `yagp-daemon` 的当前版本、最新稳定版本和升级结果。每个节点可分别开启两个组件的自动升级，开关默认关闭。面板每小时检查一次已开启自动升级的在线节点，并依次处理。也可以在对话框中手动升级任一组件。

在线升级适用于 systemd 节点。面板和节点需要能通过 HTTPS 访问 GitHub。节点校验发布包的 SHA-256，再替换对应的二进制并重启服务。配置、凭据、证书和节点 ID 都会保留。Gost 重启会中断该实例正在转发的连接，新版服务启动检查失败时会尝试恢复旧版。

如果已安装的节点尚不支持在线升级，请先执行 [一键升级命令](#升级已安装节点)。`upgrade` 适用于安装在 `/etc/yagp-daemon/<ID>` 的 systemd 实例，默认使用最新发布包。使用 `YAGP_DAEMON_VERSION` 或 `YAGP_DAEMON_BIN` 指定版本时，请选择 `0.1.77` 或更新版本。

如果节点仍使用改名前的 `yagp-node-<ID>` 服务，迁移时先停止该服务和 `yagp-gost-<ID>`，并取消旧守护服务的开机启动。将 `/etc/yagp-node/<ID>` 移到 `/etc/yagp-daemon/<ID>`，放入新版 `yagp-daemon`。systemd 选用包内的 `yagp-daemon-systemd-linux-<架构>`，OpenRC 选用 `yagp-daemon-linux-<架构>`。更新 `node.yaml` 的 `tls_cert_file` 和 `tls_key_file`，如 `report_ca_file` 指向已移动的文件，也一并更新。原 ID、凭据和证书可继续使用。

将守护服务定义改名为 `yagp-daemon-<ID>`，更新守护服务和 `yagp-gost-<ID>` 的工作目录、程序和配置路径。systemd 下执行 `systemctl daemon-reload`，再用 `systemctl enable --now yagp-daemon-<ID>` 启动新版，由它启动 Gost。OpenRC 下将新服务加入默认运行级别，再依次启动 Gost 和 `yagp-daemon-<ID>`。

### 查看和重启 Gost 服务

在 **节点管理 → 更多 → Gost 服务** 查看 `yagp-gost-<ID>.service` 的状态和最近 100 条日志，也可重启该实例。重启会中断该实例的活动转发连接。

该功能使用节点上的 `libsystemd.so.0`。发布包包含 systemd 和通用构建，安装脚本会按服务管理器选择。在 Linux glibc 主机可用 `make build-node-systemd` 构建 systemd 版本。

### 设置节点流量配额

在 **节点管理 → 添加节点** 或 **更多 → 编辑节点** 中填写流量上限，单位为 GiB，支持小数。默认不设上限，留空或填 `0` 即可解除限制。可选择不重置、每日、每周或每月重置。重置按北京时间（UTC+8）计算，每天、每周一或每月 1 日的零点开启新周期。

配额按面板管理的 TCP 和 UDP 转发端口收发的数据包字节计量，包含 IP 首部和重传。隧道入口和出口 Relay 的流量分别计入各自节点的额度。首次启用不重置配额时，使用已有的节点累计流量作为起始用量。首次启用周期配额时，从启用开始计量。节点清单分别显示内核配额用量和 Gost 应用层累计流量，两者统计口径不同。周期重置时，历史累计流量继续保留。

启用配额需要支持该功能的 `yagp-daemon` 和 `nftables`。节点使用独立的 nftables 规则，达额时由内核立即拦截数据包，包括已经建立的 TCP 连接上的数据。Gost 进程持续运行。在线节点约每 5 秒同步用量和周期策略。面板与节点断开期间继续执行现有配额，周期重置和设置变更在恢复连接后生效。卸载节点会清理该节点的配额规则。

### 管理节点的 nftables 配置

在装有 `nftables` 的 Linux 节点上，通过 **节点管理 → 更多 → nft 配置** 编辑并应用原生 nftables JSON。默认模板由面板提供，包含一个无规则的 NAT `prerouting` 链，载入时只填入编辑区。节点也可运行 `/etc/yagp-daemon/<ID>/yagp-daemon nft-template -c /etc/yagp-daemon/<ID>/node.yaml`，经 TLS 从面板获取同一份模板。

配置使用 `{"nftables":[...]}`，只允许为该节点专用的 `inet yagp_node_<ID>` 表添加链、规则、集合等对象。节点先校验配置，再原子替换该表，把成功应用的配置保存到实例目录中的 `nft.json` 并在桥接服务启动时恢复。应用 `{"nftables":[]}` 会删除该表。卸载时安装脚本会清理该表并保留配置文件。[nftables JSON 参考](https://manpages.ubuntu.com/manpages/noble/man5/libnftables-json.5.html)

### Gost Relay 来源 IP 白名单

在 **系统设置 → Gost IP 白名单** 勾选允许连接的节点。面板将所选节点最新上报的 IPv4 和 IPv6 应用到面板管理的隧道出口 Relay。地址变化后，下一轮同步会把新名单热更新到在线 Relay，无需重新勾选节点。Relay 匹配实际连接的来源 IP，多出口节点的探测地址可能与连接 Relay 时使用的地址不同。清空选择并保存可解除该白名单限制。首次为已有 Relay 绑定准入配置时会重载该服务。

运行中的隧道会自动跟随出口 IP。出口节点上报新地址后，面板在下一轮同步中更新入口 Gost 转发链的出口地址，保留 Relay 端口和其他链配置。新连接会使用新地址，无需手动重启隧道或 Gost。已经建立的连接若因旧地址失效而断开，需要重新连接。启用此功能需升级面板和入口节点的 `yagp-daemon`。

### Clash 规则集订阅

在 **系统设置 → Clash 规则集** 填写 HTTPS 规则集链接并选择要同步的边缘节点。面板保存转换结果，之后每小时刷新，并将 Gost 的 `yagp-transparent-proxy` 白名单同步到所选在线节点。下载或转换失败时保留最近一次成功的规则。取消选择某个节点会停止给该节点自动更新，并保留最近下发的白名单。

支持 `payload:` YAML 和文本规则集，包含域名、域名后缀、可等价转换的关键词、IP 和 CIDR 条目。在既有 Gost 透明代理转发链的第一跳设置 `bypass: yagp-transparent-proxy`，匹配目标走该转发链，其余目标直连。域名匹配依赖现有透明代理从 HTTP Host 或 TLS SNI 识别目标，纯 IP 流量使用 IP/CIDR 条目。[Gost 分流配置](https://v3.gost.run/en/concepts/bypass/) · [Gost 透明代理](https://v3.gost.run/en/tutorials/redirect/)

---

## 📦 预编译下载

发布包以 `yagp-` 开头。面板支持 Linux（amd64、arm64）、Windows（amd64）和 macOS（Apple Silicon、Intel）。节点安装包支持 Linux（amd64、arm64）。[查看当前仓库的 Releases](https://github.com/hanzec/gostPanel/releases)

面板和节点桥需要一起更新到使用 `yagp.node.v1` 的版本。更新后在面板中停止并重新启动已运行的转发规则，以更新 Gost 观察器凭据。未通过令牌注册的节点须重新注册。

### 自动版本号

推送到 `main` 时，GitHub Actions 使用该次推送的最后一个提交生成版本，并发布 GitHub Release 和同名的 GHCR 镜像标签。仓库默认分支为 `master` 时，推送到 `master` 也会触发发布。默认分支的最新提交可以更新 `latest`。

版本格式为 `x.y.z-sha`，例如 `0.1.67-a1b2c3d`。`x.y` 在仓库根目录的 `VERSION` 中维护，`z` 是沿第一父提交统计的主线提交数，`sha` 是提交 SHA 的前 7 位。用 `bash scripts/version.sh` 可查看当前检出提交的版本，需要完整 Git 历史。

GitHub Release 正文由 `cmd/release-notes` 生成。程序沿当前分支的第一父提交查找最近的项目发布标签，按提交类型整理此后进入该分支的提交，未分类的提交也会列出。首次发布会列出完整历史。用 `git fetch --tags` 获取标签后，可在完整检出的仓库中运行 `go run ./cmd/release-notes` 预览正文。

面板仪表盘、`/api/v1/health`、`yagp --version` 和 `yagp-daemon --version` 使用相同的构建版本。

### 开发与编译

使用 Go 1.27.3 和 Node.js 26.13.0 LTS 构建。版本分别记录在 [go.mod](./go.mod) 和 [.node-version](./.node-version)。Go 编译时会嵌入前端产物，首次编译后端前需先构建前端。

推送到 `main` 或 `master`，以及以这些分支为目标的 PR，都会运行 **Vue and Go tests** 检查。测试任务先检查 Vue，再构建 Go 需要嵌入的前端产物，并在隔离的 Linux 容器中运行 Go 测试，包括真实 nftables 配额和使用隔离 systemd 命令的升级场景。发布 GitHub Release 和推送 Docker 镜像都要等这个检查通过。

运行摘要分别显示 Vue 和 Go 的语句覆盖率。通过检查需要 Vue 达到 90%，Go 超过 90%。本地可运行 `npm --prefix web run test:coverage` 生成前端报告 `web/coverage/index.html`。构建前端后，运行 `go test ./... -cover` 可查看各个 Go 包的覆盖率。Windows 常规测试会跳过 Linux 专用场景。

```bash
# 完整构建，产物为 yagp 和 yagp-daemon
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
