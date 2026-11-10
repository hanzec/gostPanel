#!/bin/bash -p
set -euo pipefail

readonly deploy_user=yagp-deploy
readonly deploy_home=/var/lib/yagp-deploy
readonly authorized_keys="$deploy_home/.ssh/authorized_keys"
readonly legacy_dir=/etc/yagp-deploy
readonly legacy_receiver_unit=/etc/systemd/system/yagp-deploy-webhook.service
readonly install_path=/usr/local/sbin/yagp-depoly
readonly managed_dir=/etc/yagp-depoly
readonly managed_project="$managed_dir/project-dir"
readonly managed_override="$managed_dir/compose.yml"
readonly previous_override_name=.yagp-depoly.yml
readonly docker_socket=/var/run/docker.sock
readonly image_repo=ghcr.io/hanzec/yagp
readonly panel_endpoint=/api/depoly
readonly upgrade_endpoint=/api/depoly/upgrade

main() {
    case ${1:-} in
        install-webhook) shift; install_webhook "$@" ;;
        install-upgrade) shift; install_upgrade "$@" ;;
        '') deploy_image ;;
        *) usage ;;
    esac
}

usage() {
    cat >&2 <<'EOF'
用法：
  sudo bash setup_deploy.sh install-webhook Compose部署目录 yagp-depoly二进制 [Traefik容器名]
  sudo bash setup_deploy.sh install-upgrade [Traefik容器名]

install-webhook 适用于已部署的 Linux Docker Compose 面板，要求部署目录内有
root 管理的 docker-compose.yml 和 .env。原有部署变量须保存在 .env。
部署账号使用 nologin。安装时保留旧镜像覆盖配置并撤销旧 SSH 部署入口。
EOF
    exit 2
}

fail() {
    log "错误：$*"
    exit 1
}

log() {
    printf '[%s] %s\n' "$(/bin/date '+%Y-%m-%d %H:%M:%S %z')" "$*" >&2
}

install_webhook() {
    [[ $# -eq 2 || $# -eq 3 ]] || usage
    check_systemd
    local project_dir project binary traefik secret token old_secret old_token
    project_dir=$(resolve_project_dir "$1")
    binary=$(realpath -e -- "$2")
    [[ -f $binary ]] || fail '请提供 yagp-depoly ELF 二进制。'
    project=$(inspect_project "$project_dir")
    traefik=$(inspect_traefik "$project_dir" "$project" "${3:-traefik}")
    prepare_managed_dir
    lock_deploy
    old_secret="$project_dir/.yagp-webhook-secret"
    old_token="$project_dir/.yagp-webhook-token"
    secret=$(prepare_credential "$managed_dir/panel.secret" "$old_secret")
    token=$(prepare_credential "$managed_dir/panel.token" "$old_token")
    check_distinct_credentials "$secret" "$token" "$managed_dir/upgrade.secret" "$managed_dir/upgrade.token"

    setup_account "$project_dir" "$project" "$binary"
    printf '%s\n' "$secret" | write_file "$managed_dir/panel.secret" 0600
    printf '%s\n' "$token" | write_file "$managed_dir/panel.token" 0600
    write_hook_unit panel "$deploy_user" yagp-depoly.service yagp-depoly-panel 39102 "$traefik"
    write_traefik_route "$panel_endpoint" yagp-deploy-webhook traefik-panel.yml 39102 "$traefik"

    systemctl daemon-reload
    systemctl enable yagp-depoly.service
    systemctl restart yagp-depoly.service
    if [[ -f /etc/systemd/system/yagp-depoly-upgrade-webhook.service ]]; then
        systemctl restart yagp-depoly-upgrade-webhook.service
    fi
    retire_legacy_receiver "$project_dir" "$old_secret" "$old_token"
    log '配置完成。部署账号已禁用登录，本次未更新容器。'
    log "请将 $managed_dir/traefik-panel.yml 加入 Traefik 的动态配置目录。"
    log '查看部署日志：journalctl -u yagp-depoly.service -f'
    show_credentials DEPLOY_WEBHOOK panel "$panel_endpoint" "$traefik" "$secret" "$token"
}

install_upgrade() {
    [[ $# -le 1 ]] || usage
    check_systemd
    check_upgrade_cli
    check_managed_file /etc/systemd/system/yagp-depoly.service
    check_managed_file "$install_path"
    require_root_owned "$managed_dir"
    check_managed_file "$managed_project"
    local project_dir project traefik secret token
    project_dir=$(resolve_project_dir "$(<"$managed_project")")
    project=$(inspect_project "$project_dir")
    traefik=$(inspect_traefik "$project_dir" "$project" "${1:-traefik}")
    lock_deploy
    secret=$(prepare_credential "$managed_dir/upgrade.secret")
    token=$(prepare_credential "$managed_dir/upgrade.token")
    check_distinct_credentials "$secret" "$token" "$managed_dir/panel.secret" "$managed_dir/panel.token"

    install_account "$deploy_user" "$deploy_home"
    install_sudo_rule "$deploy_user" yagp-deploy-tool /usr/bin/systemctl --no-block start yagp-depoly-upgrade.service
    printf '%s\n' "$secret" | write_file "$managed_dir/upgrade.secret" 0600
    printf '%s\n' "$token" | write_file "$managed_dir/upgrade.token" 0600
    write_file /etc/systemd/system/yagp-depoly-upgrade.service 0644 <<EOF
[Unit]
Description=yagp deployment tool upgrade
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=$install_path self-upgrade
TimeoutStartSec=10min
EOF
    write_hook_unit upgrade "$deploy_user" yagp-depoly-upgrade-webhook.service yagp-depoly-upgrade-hook 39103 "$traefik"
    write_traefik_route "$upgrade_endpoint" yagp-deploy-upgrade-webhook traefik-upgrade.yml 39103 "$traefik"

    systemctl daemon-reload
    systemctl enable yagp-depoly-upgrade-webhook.service
    systemctl restart yagp-depoly-upgrade-webhook.service
    log "请将 $managed_dir/traefik-upgrade.yml 加入 Traefik 的动态配置目录。"
    log '查看工具升级日志：journalctl -u yagp-depoly-upgrade.service -f'
    show_credentials UPGRADE_WEBHOOK upgrade "$upgrade_endpoint" "$traefik" "$secret" "$token"
}

check_systemd() {
    root_environment
    [[ -d /run/systemd/system ]] || fail 'Webhook 接收器需要 systemd。'
    local systemd_version
    read -r _ systemd_version _ < <(systemctl --version)
    [[ $systemd_version =~ ^[0-9]+$ ]] && (( systemd_version >= 247 )) ||
        fail 'Webhook 接收器需要支持 LoadCredential 的 systemd 247 或更新版本。'
}

check_upgrade_cli() {
    local gh_path version
    local version_pattern='^gh version ([0-9]+)\.([0-9]+)\.[0-9]+([[:space:]]|$)'
    gh_path=$(PATH=/usr/local/bin:/usr/bin:/bin type -P gh) &&
        gh_path=$(realpath -e -- "$gh_path") ||
        fail '工具升级需要 gh 2.102.0 或更新版本。'
    check_managed_file "$gh_path"
    require_root_path "$(dirname -- "$gh_path")"
    version=$("$gh_path" --version) || fail '无法读取 gh 版本。'
    [[ $version =~ $version_pattern ]] &&
        (( 10#${BASH_REMATCH[1]} > 2 || (10#${BASH_REMATCH[1]} == 2 && 10#${BASH_REMATCH[2]} >= 102) )) ||
        fail '工具升级需要 gh 2.102.0 或更新版本。'
}

resolve_project_dir() {
    local dir
    dir=$(realpath -e -- "$1") || fail 'Compose 部署目录不存在。'
    [[ -d $dir && $dir =~ ^(/[a-zA-Z0-9_.-]+)+$ ]] ||
        fail 'Compose 部署目录只能使用字母、数字、点、横线和下划线。'
    check_project_files "$dir"
    printf '%s\n' "$dir"
}

prepare_managed_dir() {
    if [[ ! -e $managed_dir && ! -L $managed_dir ]]; then
        mkdir -m 0700 -- "$managed_dir"
    fi
    require_root_owned "$managed_dir"
    [[ -d $managed_dir ]] || fail "配置路径不是目录：$managed_dir"
    chmod 0700 "$managed_dir"
}

setup_account() {
    local project_dir=$1 project=$2 binary=$3
    local compose_help legacy=false previous_override="$1/$previous_override_name"
    log "检查部署目录与 Compose 配置：$project_dir"
    cd "$project_dir"

    if [[ -e $previous_override || -L $previous_override || -e $project_dir/yagp-depoly ]]; then
        if [[ -e $project_dir/.yagp-depoly.lock || -L $project_dir/.yagp-depoly.lock ]]; then
            check_managed_file "$project_dir/.yagp-depoly.lock"
        fi
        exec 7>>"$project_dir/.yagp-depoly.lock"
        flock 7
    fi
    if [[ -e $legacy_dir/project || -L $legacy_dir/project ]]; then
        require_root_owned "$legacy_dir"
        if [[ -e $legacy_dir/deploy.lock || -L $legacy_dir/deploy.lock ]]; then
            check_managed_file "$legacy_dir/deploy.lock"
        fi
        exec 8>>"$legacy_dir/deploy.lock"
        flock 8
        legacy=true
    fi

    if [[ $legacy == true ]]; then
        check_legacy_install "$project_dir" "$project"
    fi
    log "已识别 Compose 项目 ${project}，检查两个 yagp 服务。"
    check_services "$project_dir" "$project"
    compose_help=$(docker_local compose up --help)
    [[ $compose_help == *--wait-timeout* ]] || fail 'Docker Compose 版本需要支持 up --wait-timeout。'

    if [[ -e $managed_project || -L $managed_project ]]; then
        check_managed_file "$managed_project"
        [[ $(<"$managed_project") == "$project_dir" ]] || fail '已安装的部署工具属于其他 Compose 目录。'
    fi
    if [[ -e $managed_override || -L $managed_override ]]; then
        check_managed_file "$managed_override"
        [[ $(read_project "$managed_override") == "$project" ]] || fail '现有镜像覆盖文件属于其他 Compose 项目。'
    elif [[ -e $previous_override || -L $previous_override ]]; then
        check_managed_file "$previous_override"
        [[ $(read_project "$previous_override") == "$project" ]] || fail '旧镜像覆盖文件属于其他 Compose 项目。'
        write_file "$managed_override" 0600 < "$previous_override"
    elif [[ $legacy == true ]]; then
        import_override "$managed_override" "$project"
    elif getent passwd "$deploy_user" >/dev/null; then
        fail '未找到此项目的部署安装记录。'
    else
        write_override "$managed_override" "$project"
    fi
    printf '%s\n' "$project_dir" | write_file "$managed_project" 0600

    log "检查部署账号 $deploy_user 和 Docker 套接字权限。"
    install_account "$deploy_user" "$deploy_home"
    if [[ -e $authorized_keys || -L $authorized_keys ]]; then
        require_root_owned "$deploy_home/.ssh"
        check_managed_file "$authorized_keys"
    fi
    check_install_parent
    stop_legacy_receiver "$project_dir"
    log "写入部署入口：$install_path"
    write_file "$install_path" 0755 < "$binary"
    log '校验并安装 sudo 规则。'
    install_sudo_rule "$deploy_user" "$deploy_user" "$install_path" deploy
    retire_ssh_key "$project_dir/yagp-depoly"
}

check_install_parent() {
    local parent="/usr/local/sbin"
    if [[ ! -e $parent && ! -L $parent ]]; then
        require_root_path /usr/local
        mkdir -m 0755 -- "$parent"
    fi
    require_root_path "$parent"
}

stop_legacy_receiver() {
    [[ -e $legacy_receiver_unit || -L $legacy_receiver_unit ]] || return 0
    check_managed_file "$legacy_receiver_unit"
    grep -Fxq "User=$deploy_user" "$legacy_receiver_unit" &&
        grep -Fq "ExecStart=$1/yagp-deploy-webhook " "$legacy_receiver_unit" ||
        fail '发现非本安装器管理的旧 Webhook 服务，未修改该服务。'
    log '停止旧 Webhook 接收器。'
    systemctl disable --now yagp-deploy-webhook.service
}

retire_legacy_receiver() {
    local dir=$1 old_secret=$2 old_token=$3 path
    if [[ -f $legacy_receiver_unit ]]; then
        rm -f -- "$legacy_receiver_unit"
        systemctl daemon-reload
    fi
    for path in "$dir/yagp-deploy-webhook" "$dir/yagp-depoly" "$dir/$previous_override_name" "$dir/.yagp-depoly.lock" "$old_secret" "$old_token"; do
        if [[ -e $path || -L $path ]]; then
            check_managed_file "$path"
            rm -f -- "$path"
        fi
    done
    if [[ -f $legacy_dir/project ]]; then
        retire_legacy_install
    fi
}

prepare_credential() {
    local target=$1 previous=${2:-}
    if [[ -e $target || -L $target ]]; then
        read_credential "$target"
    elif [[ -n $previous && ( -e $previous || -L $previous ) ]]; then
        read_credential "$previous"
    else
        openssl rand -hex 32
    fi
}

read_credential() {
    check_managed_file "$1"
    local value
    [[ $(stat -c '%a' -- "$1") == 600 ]] || fail "凭据文件须为 0600：$1"
    value=$(<"$1")
    [[ $value =~ ^[0-9a-f]{64}$ ]] || fail "凭据文件须包含 64 位小写十六进制字符：$1"
    printf '%s\n' "$value"
}

check_distinct_credentials() {
    local secret=$1 token=$2 other_file other_value previous_value=''
    shift 2
    [[ $secret =~ ^[0-9a-f]{64}$ && $token =~ ^[0-9a-f]{64}$ && $secret != "$token" ]] ||
        fail 'Webhook 签名密钥和 Token 须为两份不同的 64 位小写十六进制字符。'
    for other_file in "$@"; do
        if [[ -e $other_file || -L $other_file ]]; then
            other_value=$(read_credential "$other_file")
            [[ $secret != "$other_value" && $token != "$other_value" && $previous_value != "$other_value" ]] ||
                fail '面板部署和工具升级须使用不同的凭据。'
            previous_value=$other_value
        fi
    done
}

show_credentials() {
    local prefix=$1 role=$2 endpoint=$3 hostname=${4%%|*} secret=$5 token=$6
    printf '%s_URL=https://%s%s\n' "$prefix" "$hostname" "$endpoint"
    if [[ -t 1 ]]; then
        printf '%s_SECRET=%s\n%s_TOKEN=%s\n' "$prefix" "$secret" "$prefix" "$token"
    else
        log "凭据保存在 $managed_dir/$role.secret 和 $managed_dir/$role.token，可在可信终端用 sudo cat 读取。"
    fi
}

inspect_traefik() {
    local project_dir=$1 project=$2 proxy_container=$3 hostname metadata entrypoint network gateway proxy_ip
    local host_pattern='^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$'
    hostname=$(compose "$project_dir" "$project" config --environment | awk 'sub(/^YAGP_HOST=/, "")') ||
        fail "无法读取 $project_dir/.env 中的 YAGP_HOST。"
    [[ $hostname =~ $host_pattern && ${#hostname} -le 253 ]] ||
        fail "$project_dir/.env 的 YAGP_HOST 须为有效域名，不能包含协议、端口或路径。"
    metadata=$(docker_local inspect --type container --format \
        '{{index .Config.Labels "traefik.http.routers.yagp.entrypoints"}}|{{index .Config.Labels "traefik.docker.network"}}' yagp) ||
        fail '无法读取 yagp 容器的 Traefik 配置。'
    IFS='|' read -r entrypoint network <<< "$metadata"
    [[ $entrypoint =~ ^[A-Za-z0-9_-]+$ && $network =~ ^[A-Za-z0-9_.-]+$ ]] ||
        fail '无法识别当前 yagp 的 Traefik HTTPS 入口和 Docker 网络。'
    gateway=$(docker_local network inspect --format \
        '{{if eq .Driver "bridge"}}{{range .IPAM.Config}}{{println .Gateway}}{{end}}{{end}}' -- "$network" |
        awk '/^[0-9]+\./ && !found {print; found=1}') ||
        fail "无法读取 Docker 网络 ${network}。"
    case $gateway in
        10.*|172.1[6-9].*|172.2[0-9].*|172.3[01].*|192.168.*) ;;
        *) fail 'Traefik 的 Docker 网络需要有私网 IPv4 bridge 网关。' ;;
    esac
    proxy_ip=$(docker_local inspect --type container --format \
        "{{with index .NetworkSettings.Networks \"$network\"}}{{.IPAddress}}{{end}}" -- "$proxy_container") ||
        fail "找不到 Traefik 容器 ${proxy_container}。"
    [[ $proxy_ip =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ && $proxy_ip != "$gateway" ]] ||
        fail "Traefik 容器 $proxy_container 没有在 $network 上配置 IPv4 地址。"
    log "Webhook 使用 $project_dir/.env 的域名 ${hostname}，仅信任 Traefik 容器 $proxy_container 的内网地址 ${proxy_ip}。"
    printf '%s|%s|%s|%s\n' "$hostname" "$entrypoint" "$gateway" "$proxy_ip"
}

write_hook_unit() {
    local role=$1 user=$2 service=$3 state=$4 port=$5
    local hostname entrypoint gateway proxy_ip
    IFS='|' read -r hostname entrypoint gateway proxy_ip <<< "$6"
    log "安装 ${service}，监听 Docker 内网 $gateway:${port}。"
    write_file "/etc/systemd/system/$service" 0644 <<EOF
[Unit]
Description=yagp $role webhook
After=docker.service
Wants=docker.service

[Service]
Type=notify
User=$user
StateDirectory=$state
StateDirectoryMode=0700
LoadCredential=webhook-secret:$managed_dir/$role.secret
LoadCredential=webhook-token:$managed_dir/$role.token
ExecStart=$install_path serve-$role -listen $gateway:$port -trusted-proxy $proxy_ip -secret-file \${CREDENTIALS_DIRECTORY}/webhook-secret -token-file \${CREDENTIALS_DIRECTORY}/webhook-token -state-file \${STATE_DIRECTORY}/replay
TimeoutStartSec=20s
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF
}

write_traefik_route() {
    local endpoint=$1 name=$2 file=$3 port=$4
    local hostname entrypoint gateway proxy_ip hook_rule
    IFS='|' read -r hostname entrypoint gateway proxy_ip <<< "$5"
    printf -v hook_rule 'Host(`%s`) && Path(`%s`) && Method(`POST`)' "$hostname" "$endpoint"
    write_file "$managed_dir/$file" 0644 <<EOF
http:
  routers:
    $name:
      rule: '$hook_rule'
      entryPoints: ['$entrypoint']
      priority: 90
      tls: {}
      service: $name
      middlewares: [$name-limit]
  middlewares:
    $name-limit:
      rateLimit:
        average: 20
        period: 1m
        burst: 5
        sourceCriterion:
          requestHeaderName: X-Real-IP
  services:
    $name:
      loadBalancer:
        servers:
          - url: 'http://$gateway:$port'
EOF
}

check_legacy_install() {
    check_managed_file "$legacy_dir/project"
    [[ $(<"$legacy_dir/project") == "$1"$'\n'"$2" ]] ||
        fail '旧部署记录与当前 Compose 项目不匹配。'
}

import_override() {
    local target=$1 project=$2 source="$legacy_dir/image.yml" image=''
    if [[ -e $source || -L $source ]]; then
        check_managed_file "$source"
        image=$(sed -n '3s/^    image: //p' "$source")
        [[ $image == "$image_repo@"* && ${image#*@} =~ ^sha256:[0-9a-f]{64}$ ]] ||
            fail '旧镜像覆盖文件中没有有效的 yagp 镜像 digest。'
        log "保留旧镜像：$image"
    else
        log '旧版尚未记录固定镜像，沿用当前 Compose 镜像设置。'
    fi
    write_override "$target" "$project" "$image"
}

retire_legacy_install() {
    log "撤销旧入口，清理 $legacy_dir 中的旧版文件。"
    rm -f -- "$legacy_dir/run" "$legacy_dir/project" "$legacy_dir/image.yml" "$legacy_dir/deploy.lock"
    rmdir -- "$legacy_dir" 2>/dev/null ||
        log "保留 $legacy_dir 中的其他文件。"
}

inspect_project() {
    local project_dir=$1 identity project service compose_files
    identity=$(docker_local inspect --type container --format \
        '{{index .Config.Labels "com.docker.compose.project"}}|{{index .Config.Labels "com.docker.compose.service"}}|{{index .Config.Labels "com.docker.compose.project.config_files"}}' yagp) ||
        fail '无法识别现有 yagp 容器。'
    IFS='|' read -r project service compose_files <<< "$identity"
    [[ $project =~ ^[a-z0-9][a-z0-9_-]*$ && $service == yagp ]] ||
        fail '现有 yagp 容器不属于可识别的 Compose 项目。'
    case $compose_files in
        "$project_dir/docker-compose.yml"|"$project_dir/docker-compose.yml,$managed_override") ;;
        "$project_dir/docker-compose.yml,$project_dir/$previous_override_name")
            [[ -f $project_dir/$previous_override_name || -f $managed_override ]] ||
                fail '现有容器仍引用旧镜像覆盖文件，但该文件已丢失。'
            ;;
        "$project_dir/docker-compose.yml,$legacy_dir/image.yml")
            [[ -f $legacy_dir/image.yml || -f $project_dir/$previous_override_name || -f $managed_override ]] ||
                fail '现有容器仍引用旧镜像覆盖文件，但该文件已丢失。'
            ;;
        *) fail '部署目录与现有 yagp 容器的 Compose 文件不匹配。' ;;
    esac
    printf '%s\n' "$project"
}

deploy_image() {
    [[ $0 == "$install_path" ]] || usage
    root_environment
    local digest project_dir project image running started=$SECONDS
    IFS= read -r digest && [[ $digest =~ ^sha256:[0-9a-f]{64}$ ]] ||
        fail '需要完整的小写 SHA-256 镜像 digest。'
    image="$image_repo@$digest"

    require_root_owned "$managed_dir"
    lock_deploy
    check_managed_file "$managed_project"
    check_managed_file "$managed_override"
    project_dir=$(resolve_project_dir "$(<"$managed_project")")
    project=$(read_project "$managed_override")
    cd "$project_dir"
    check_services "$project_dir" "$project"
    running=$(docker_local inspect --type container --format \
        '{{.Config.Image}}|{{index .Config.Labels "com.docker.compose.project"}}|{{if .State.Health}}{{.State.Health.Status}}{{end}}' yagp 2>/dev/null) || running=''
    if [[ $running == "$image|$project|healthy" ]]; then
        write_override "$managed_override" "$project" "$image"
        log "项目 $project 已运行该镜像且健康，跳过重启：$digest"
        return
    fi
    log "项目 $project 正在拉取 $image"
    docker_local image pull "$image" ||
        fail "镜像拉取失败（退出码 $?），当前容器保持原状。"

    log "写入镜像覆盖文件：$managed_override"
    write_override "$managed_override" "$project" "$image"
    log '启动 yagp-config-init 与 yagp，最多等待健康检查 120 秒。'
    compose "$project_dir" "$project" -f "$managed_override" \
        up --no-build --pull never --wait --wait-timeout 120 yagp yagp-config-init ||
        fail "Compose 更新失败（退出码 $?）。请检查容器状态，覆盖文件已记录本次 digest：$digest"
    log "项目 $project 更新完成，健康检查通过，耗时 $((SECONDS - started)) 秒。"
}

write_override() {
    local target=$1 project=$2 image=${3:-}
    if [[ -z $image ]]; then
        printf 'name: "%s"\nservices: {}\n' "$project" | write_file "$target" 0600
        return
    fi
    write_file "$target" 0600 <<EOF
name: "$project"
services:
  yagp:
    image: $image
  yagp-config-init:
    image: $image
EOF
}

read_project() {
    local line
    local pattern='^name: "([a-z0-9][a-z0-9_-]*)"$'
    IFS= read -r line < "$1" && [[ $line =~ $pattern ]] ||
        fail "镜像覆盖文件缺少有效的 Compose 项目名：$1"
    printf '%s\n' "${BASH_REMATCH[1]}"
}

lock_deploy() {
    local lock="$managed_dir/deploy.lock"
    if [[ -e $lock || -L $lock ]]; then
        check_managed_file "$lock"
    fi
    # 镜像下载、覆盖文件写入和 Compose 启动必须使用同一次部署的数据。
    log "等待部署锁：$lock"
    exec 9>>"$lock"
    flock 9
}

root_environment() {
    export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
    [[ $EUID -eq 0 && $(uname -s) == Linux ]] || fail '请在 Linux 服务器上以 root 执行。'
    umask 077
}

write_file() (
    target=$1 mode=$2
    temporary=$(mktemp -- "$target.XXXXXX")
    trap 'rm -f -- "$temporary"' EXIT
    cat > "$temporary"
    chmod "$mode" "$temporary"
    mv -fT -- "$temporary" "$target"
)

require_root_owned() {
    local owner mode
    [[ ! -L $1 ]] && read -r owner mode < <(stat -c '%u %a' -- "$1") &&
        [[ $owner == 0 ]] && (( (8#$mode & 022) == 0 )) ||
        fail "路径须由 root 所有且仅 root 可写，不可使用符号链接：$1"
}

check_managed_file() {
    [[ -f $1 ]] || fail "缺少所需文件：$1"
    require_root_owned "$1"
}

require_root_path() {
    local path=$1
    while [[ $path != / ]]; do
        require_root_owned "$path"
        path=$(dirname -- "$path")
    done
}

check_project_files() {
    local name
    require_root_path "$1"
    for name in docker-compose.yml .env; do
        [[ -f $1/$name ]] || fail "缺少部署文件：$1/$name"
        require_root_owned "$1/$name"
    done
    for name in config data logs; do
        [[ -d $1/$name ]] || fail "缺少绑定目录：$1/$name"
        require_root_owned "$1/$name"
    done
}

install_account() {
    local user=$1 home=$2 account_home shell nologin
    nologin=$(command -v nologin) || fail '找不到 nologin。'
    if getent passwd "$user" >/dev/null; then
        IFS=: read -r _ _ _ _ _ account_home shell < <(getent passwd "$user")
        [[ $account_home == "$home" &&
            ( $shell == /bin/sh || $shell == /sbin/nologin || $shell == /usr/sbin/nologin || $shell == "$nologin" ) &&
            $(id -nG "$user") == "$user" ]] ||
            fail "现有账号 $user 的主目录、shell 或组已改变。"
        if [[ $shell != "$nologin" ]]; then
            log "将账号 $user 的登录 shell 改为 ${nologin}。"
            usermod --shell "$nologin" "$user"
        fi
    else
        useradd --system --user-group --no-create-home --home-dir "$home" \
            --shell "$nologin" "$user"
    fi
    passwd -l "$user" >/dev/null
    if runuser -u "$user" -- test -w "$docker_socket"; then
        fail "账号 $user 可写 Docker 套接字，请先修正主机权限。"
    fi
    if [[ ! -e $home && ! -L $home ]]; then
        mkdir -m 0755 -- "$home"
    fi
    require_root_owned "$home"
}

retire_ssh_key() {
    [[ -f $authorized_keys ]] || return 0
    local key_line
    key_line=$(<"$authorized_keys")
    if [[ $key_line != *$'\n'* &&
        ( $key_line == "restrict,command=\"$1 ssh\" ssh-ed25519 "* ||
          $key_line == "restrict,command=\"$legacy_dir/run ssh\" ssh-ed25519 "* ) ]]; then
        rm -f -- "$authorized_keys"
        log '已移除旧脚本安装的 SSH 授权公钥。'
    fi
}

install_sudo_rule() (
    user=$1 name=$2
    shift 2
    rule=$(mktemp)
    trap 'rm -f -- "$rule"' EXIT
    {
        printf '%s ALL=(root) NOPASSWD:NOSETENV:' "$user"
        printf ' %s' "$@"
        printf '\n'
    } > "$rule"
    visudo -cf "$rule" >/dev/null || fail 'sudoers 校验失败。'
    write_file "/etc/sudoers.d/$name" 0440 < "$rule"
    sudo -l -U "$user" -- "$@" >/dev/null
)

docker_local() {
    env -i PATH="$PATH" HOME=/root PWD="$PWD" COMPOSE_REMOVE_ORPHANS=false \
        docker --host "unix://$docker_socket" "$@"
}

compose() {
    local project_dir=$1 project=$2
    shift 2
    docker_local compose --project-name "$project" --project-directory "$project_dir" \
        --env-file "$project_dir/.env" -f "$project_dir/docker-compose.yml" "$@"
}

check_services() {
    local services
    services=$(compose "$1" "$2" config --services | LC_ALL=C sort)
    [[ $services == $'yagp\nyagp-config-init' ]] ||
        fail 'Compose 文件须仅包含 yagp 和 yagp-config-init 两个服务。'
}

main "$@"
