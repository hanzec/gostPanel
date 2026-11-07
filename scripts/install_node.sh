#!/usr/bin/env bash

# ycgp Gost 节点安装脚本
# 原作者: code-gopher
set -euo pipefail
umask 077

die() { printf '%s\n' "$*" >&2; exit 1; }

usage() {
    cat <<'EOF'
用法:
  install_node.sh --node-id ID --username USER --password PASSWORD \
    --report-address HOST:PORT --tls-cert CERT_FILE --tls-key KEY_FILE \
    [--control-port 39000] [--admin-port 39001] [--observer-port 39002] \
    [--report-ca CA_FILE]
  install_node.sh uninstall --node-id ID
EOF
}

[[ "${1:-}" != "--help" ]] || { usage; exit 0; }
[[ $EUID -eq 0 ]] || die "请使用 root 用户运行此脚本"

action=install
if [[ "${1:-}" == uninstall ]]; then
    action=uninstall
    shift
fi

node_id=
control_port=39000
admin_port=39001
observer_port=39002
username=
password=
report_address=
report_ca=
tls_cert=
tls_key=
while (($#)); do
    [[ $# -ge 2 ]] || die "缺少参数值: $1"
    case "$1" in
        --node-id) node_id=$2 ;;
        --control-port) control_port=$2 ;;
        --admin-port) admin_port=$2 ;;
        --observer-port) observer_port=$2 ;;
        --username) username=$2 ;;
        --password) password=$2 ;;
        --report-address) report_address=$2 ;;
        --report-ca) report_ca=$2 ;;
        --tls-cert) tls_cert=$2 ;;
        --tls-key) tls_key=$2 ;;
        *) usage; die "未知参数: $1" ;;
    esac
    shift 2
done

[[ "$node_id" =~ ^[1-9][0-9]{0,17}$ ]] || die "请填写面板中的节点 ID"
base_path="/etc/ycgp-node/$node_id"
gost_unit="ycgp-gost-$node_id"
bridge_unit="ycgp-node-$node_id"

if command -v systemctl >/dev/null 2>&1 && [[ -d /run/systemd/system ]]; then
    manager=systemd
elif command -v rc-service >/dev/null 2>&1 && [[ -x /sbin/openrc-run ]]; then
    manager=openrc
else
    die "节点安装需要 systemd 或 OpenRC"
fi

if [[ "$action" == uninstall ]]; then
    if [[ "$manager" == systemd ]]; then
        systemctl disable --now "$bridge_unit" "$gost_unit" || true
        rm -f -- "/etc/systemd/system/$bridge_unit.service" "/etc/systemd/system/$gost_unit.service"
        systemctl daemon-reload
    else
        for unit in "$bridge_unit" "$gost_unit"; do
            rc-service "$unit" stop || true
            rc-update del "$unit" default || true
            rm -f -- "/etc/init.d/$unit"
        done
    fi
    rm -f -- "$base_path/ycgp-node" "$base_path/gost"
    printf '节点 %s 服务已卸载，配置位于 %s\n' "$node_id" "$base_path"
    exit 0
fi

for port in "$control_port" "$admin_port" "$observer_port"; do
    [[ "$port" =~ ^[1-9][0-9]{0,4}$ ]] && ((port <= 65535)) || die "无效端口: $port"
done
[[ "$control_port" != "$admin_port" && "$control_port" != "$observer_port" && "$admin_port" != "$observer_port" ]] ||
    die "控制、Gost 管理和观察器端口必须各不相同"
if [[ ! -f "$base_path/node.yaml" ]] && command -v ss >/dev/null 2>&1; then
    for port in "$control_port" "$admin_port" "$observer_port"; do
        if [[ -n "$(ss -H -ltn "sport = :$port")" ]]; then
            die "端口 $port 已被占用"
        fi
    done
fi
[[ -n "$username" && "$username" != *:* && -n "$password" ]] || die "请填写节点认证信息，用户名不能包含冒号"
[[ "$report_address" == *:* ]] || die "请填写面板流量上报地址 HOST:PORT"
[[ -f "$tls_cert" && -f "$tls_key" ]] || die "请先准备节点 TLS 证书和私钥"
tls_cert=$(readlink -f -- "$tls_cert")
tls_key=$(readlink -f -- "$tls_key")
if [[ -n "$report_ca" ]]; then
    [[ -f "$report_ca" ]] || die "上报 CA 文件不存在"
    report_ca=$(readlink -f -- "$report_ca")
fi

yaml_quote() {
    [[ "$1" != *$'\n'* && "$1" != *$'\r'* ]] || die "参数不能包含换行"
    local value=${1//\'/\'\'}
    printf "'%s'" "$value"
}

case "$(uname -m)" in
    x86_64) arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    *) die "当前发布支持 Linux amd64 和 arm64" ;;
esac

gost_version=3.2.6
repo="${YCGP_REPO:-hanzec/gostPanel}"
release_path=latest/download
if [[ -n "${YCGP_NODE_VERSION:-}" ]]; then
    release_path="download/$YCGP_NODE_VERSION"
fi

tmp_dir=$(mktemp -d)
trap 'rm -rf -- "$tmp_dir"' EXIT
mkdir -p -- "$tmp_dir/gost" "$tmp_dir/bridge"

# 先验证配置和下载内容，再写入系统目录。
for value in "$username" "$password" "$report_address" "$report_ca" "$tls_cert" "$tls_key"; do
    yaml_quote "$value" >/dev/null
done
cat > "$tmp_dir/node.yaml" <<EOF
node_id: $node_id
username: $(yaml_quote "$username")
password: $(yaml_quote "$password")
control_listen: ":$control_port"
admin_address: "127.0.0.1:$admin_port"
observer_listen: "127.0.0.1:$observer_port"
report_address: $(yaml_quote "$report_address")
report_ca_file: $(yaml_quote "$report_ca")
tls_cert_file: $(yaml_quote "$tls_cert")
tls_key_file: $(yaml_quote "$tls_key")
EOF
if [[ -f "$base_path/node.yaml" ]] && ! cmp -s "$tmp_dir/node.yaml" "$base_path/node.yaml"; then
    die "此节点已有不同的设置。请核对 $base_path/node.yaml 和 $base_path/gost.yaml"
fi
cat > "$tmp_dir/gost.yaml" <<EOF
api:
  addr: "127.0.0.1:$admin_port"
  pathPrefix: /api
  auth:
    username: $(yaml_quote "$username")
    password: $(yaml_quote "$password")
EOF

curl -fsSL "${GH_PROXY:-}https://github.com/go-gost/gost/releases/download/v$gost_version/gost_${gost_version}_linux_$arch.tar.gz" \
    -o "$tmp_dir/gost.tar.gz"
tar -xzf "$tmp_dir/gost.tar.gz" -C "$tmp_dir/gost"
if [[ -n "${YCGP_NODE_BIN:-}" ]]; then
    bridge_bin="$YCGP_NODE_BIN"
else
    curl -fsSL "${GH_PROXY:-}https://github.com/$repo/releases/$release_path/ycgp-node-linux-$arch.tar.gz" \
        -o "$tmp_dir/bridge.tar.gz"
    tar -xzf "$tmp_dir/bridge.tar.gz" -C "$tmp_dir/bridge"
    bridge_bin="$tmp_dir/bridge/ycgp-node-linux-$arch"
fi
[[ -f "$tmp_dir/gost/gost" && -f "$bridge_bin" ]] || die "安装包缺少 Gost 或 ycgp-node 程序"

install -d -m 0700 -- "$base_path"
install -m 0700 -- "$tmp_dir/gost/gost" "$base_path/gost"
install -m 0700 -- "$bridge_bin" "$base_path/ycgp-node"
if [[ -f "$tmp_dir/bridge/LICENSE" ]]; then
    install -m 0600 -- "$tmp_dir/bridge/LICENSE" "$base_path/LICENSE"
fi
if [[ ! -f "$base_path/node.yaml" ]]; then
    install -m 0600 -- "$tmp_dir/node.yaml" "$base_path/node.yaml"
fi
if [[ ! -f "$base_path/gost.yaml" ]]; then
    install -m 0600 -- "$tmp_dir/gost.yaml" "$base_path/gost.yaml"
fi

write_service() {
    local name=$1 description=$2 executable=$3 config_flag=$4 config_file=$5
    if [[ "$manager" == systemd ]]; then
        cat > "/etc/systemd/system/$name.service" <<EOF
[Unit]
Description=$description
After=network.target

[Service]
WorkingDirectory=$base_path
ExecStart=$base_path/$executable $config_flag $base_path/$config_file
Restart=on-failure
RestartSec=2

[Install]
WantedBy=multi-user.target
EOF
        chmod 0644 "/etc/systemd/system/$name.service"
    else
        cat > "/etc/init.d/$name" <<EOF
#!/sbin/openrc-run
description="$description"
command="$base_path/$executable"
command_args="$config_flag $base_path/$config_file"
directory="$base_path"
command_background=true
pidfile="/run/$name.pid"
depend() {
    need net
}
EOF
        chmod 0755 "/etc/init.d/$name"
    fi
}

write_service "$bridge_unit" "ycgp node $node_id bridge" ycgp-node -c node.yaml
write_service "$gost_unit" "ycgp node $node_id Gost" gost -C gost.yaml
if [[ "$manager" == systemd ]]; then
    systemctl daemon-reload
    systemctl enable "$bridge_unit" "$gost_unit"
    systemctl restart "$bridge_unit" "$gost_unit"
else
    for unit in "$bridge_unit" "$gost_unit"; do
        rc-update add "$unit" default
        rc-service "$unit" restart
    done
fi

printf '节点 %s 已启动。gRPC 控制端口 %s，上报地址 %s\n' "$node_id" "$control_port" "$report_address"
