#!/usr/bin/env bash

# yagp Gost 节点安装脚本
# 原作者: code-gopher
set -euo pipefail
umask 077

die() { printf '%s\n' "$*" >&2; exit 1; }

usage() {
    cat <<'EOF'
用法:
  install_node.sh --panel-address HOST:PORT --token TOKEN
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
panel_address=
token=
while (($#)); do
    [[ $# -ge 2 ]] || die "缺少参数值: $1"
    case "$action:$1" in
        uninstall:--node-id) node_id=$2 ;;
        install:--panel-address) panel_address=$2 ;;
        install:--token) token=$2 ;;
        *) usage; die "未知参数: $1" ;;
    esac
    shift 2
done

state_dir=/etc/yagp-node

if command -v systemctl >/dev/null 2>&1 && [[ -d /run/systemd/system ]]; then
    manager=systemd
elif command -v rc-service >/dev/null 2>&1 && [[ -x /sbin/openrc-run ]]; then
    manager=openrc
else
    die "节点安装需要 systemd 或 OpenRC"
fi

if [[ "$action" == uninstall ]]; then
    [[ "$node_id" =~ ^[1-9][0-9]{0,17}$ ]] || die "请填写面板中的节点 ID"
    base_path="$state_dir/$node_id"
    gost_unit="yagp-gost-$node_id"
    bridge_unit="yagp-node-$node_id"
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
    if [[ -f "$base_path/nft.json" ]] && command -v nft >/dev/null 2>&1 &&
        nft list table inet "yagp_node_$node_id" >/dev/null 2>&1; then
        nft delete table inet "yagp_node_$node_id"
    fi
    rm -f -- "$base_path/yagp-node" "$base_path/gost"
    printf '节点 %s 服务已卸载，配置位于 %s\n' "$node_id" "$base_path"
    exit 0
fi

[[ -n "$panel_address" && -n "$token" ]] || { usage; exit 1; }

case "$(uname -m)" in
    x86_64) arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    *) die "当前发布支持 Linux amd64 和 arm64" ;;
esac

repo="${YAGP_REPO:-hanzec/gostPanel}"
release_path=latest/download
node_version="${YAGP_NODE_VERSION:-}"
if [[ -n "$node_version" ]]; then
    release_path="download/$node_version"
fi

mkdir -p -- "$state_dir"
tmp_dir=$(mktemp -d -- "$state_dir/.install.XXXXXXXX")
stage="$tmp_dir/node"
cleanup() {
    local result=$?
    if (( result != 0 )) && [[ -n "$node_id" ]]; then
        printf '节点已完成注册，token 已失效\n' >&2
        if [[ -d "$stage" ]]; then
            printf '注册文件已保留在 %s\n' "$stage" >&2
            return
        fi
        printf '节点配置已保留在 %s，请检查服务后重新启动\n' "$base_path" >&2
        if [[ "$manager" == systemd ]]; then
            printf '查看日志: journalctl -u %s -u %s\n' "$bridge_unit" "$gost_unit" >&2
        fi
    fi
    rm -rf -- "$tmp_dir"
}
trap cleanup EXIT
mkdir -p -- "$tmp_dir/bridge" "$stage"

bridge_bin="${YAGP_NODE_BIN:-}"
if [[ -z "$bridge_bin" ]]; then
    curl -fsSL "${GH_PROXY:-}https://github.com/$repo/releases/$release_path/yagp-node-linux-$arch.tar.gz" \
        -o "$tmp_dir/bridge.tar.gz"
    tar -xzf "$tmp_dir/bridge.tar.gz" -C "$tmp_dir/bridge"
    if [[ "$manager" == systemd ]]; then
        bridge_bin="$tmp_dir/bridge/yagp-node-systemd-linux-$arch"
    else
        bridge_bin="$tmp_dir/bridge/yagp-node-linux-$arch"
    fi
fi
[[ -f "$bridge_bin" ]] || die "找不到适用于 $manager 的 yagp-node 程序: $bridge_bin"

install -m 0700 -- "$bridge_bin" "$stage/yagp-node"
if [[ -f "$tmp_dir/bridge/LICENSE" ]]; then
    install -m 0600 -- "$tmp_dir/bridge/LICENSE" "$stage/LICENSE"
fi
"$stage/yagp-node" prepare-gost --output "$stage/gost"

if ! node_id=$(printf '%s' "$token" | "$stage/yagp-node" enroll --panel-address "$panel_address" --output "$stage"); then
    die "节点未安装。若 token 已失效，请在面板生成新的 token 后重试。"
fi
unset token
[[ "$node_id" =~ ^[1-9][0-9]{0,17}$ ]] || die "yagp-node 未返回有效的节点 ID"
base_path="$state_dir/$node_id"
gost_unit="yagp-gost-$node_id"
bridge_unit="yagp-node-$node_id"
mv -T -n -- "$stage" "$base_path"
[[ ! -e "$stage" ]] || die "节点安装目录已存在: $base_path"

write_service() {
    local name=$1 description=$2 executable=$3 config_flag=$4 config_file=$5
    local unit_path
    if [[ "$manager" == systemd ]]; then
        unit_path="/etc/systemd/system/$name.service"
        (set -o noclobber; cat > "$unit_path") <<EOF
[Unit]
Description=$description
After=network.target nftables.service

[Service]
WorkingDirectory=$base_path
ExecStartPre=$base_path/yagp-node ensure-gost -c $base_path/node.yaml
ExecStart=$base_path/$executable $config_flag $base_path/$config_file
Restart=on-failure
RestartSec=2

[Install]
WantedBy=multi-user.target
EOF
        chmod 0644 "$unit_path"
    else
        unit_path="/etc/init.d/$name"
        (set -o noclobber; cat > "$unit_path") <<EOF
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
        chmod 0755 "$unit_path"
    fi
}

write_service "$bridge_unit" "yagp node $node_id bridge" yagp-node -c node.yaml
if [[ "$manager" == systemd ]]; then
    gost_unit_path="/etc/systemd/system/$gost_unit.service"
    gost_unit_existed=false
    if [[ -e "$gost_unit_path" || -L "$gost_unit_path" ]]; then
        gost_unit_existed=true
    fi
    systemctl daemon-reload
    if ! systemctl enable --now "$bridge_unit" ||
        ! systemctl is-active --quiet "$bridge_unit" ||
        ! systemctl is-active --quiet "$gost_unit"; then
        systemctl disable --now "$bridge_unit" || true
        if [[ "$gost_unit_existed" == false && -f "$gost_unit_path" ]]; then
            systemctl disable --now "$gost_unit" || true
        fi
        die "服务启动失败。修复后运行: systemctl enable --now $bridge_unit"
    fi
else
    write_service "$gost_unit" "yagp node $node_id Gost" gost -C gost.yaml
    for unit in "$gost_unit" "$bridge_unit"; do
        rc-update add "$unit" default
        rc-service "$unit" restart
    done
fi

printf '节点 %s 已启动，上报地址 %s\n' "$node_id" "$panel_address"
