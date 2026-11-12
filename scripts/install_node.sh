#!/usr/bin/env bash

# yagp Gost 节点安装脚本
# 原作者: code-gopher
set -euo pipefail
umask 077

die() { printf '%s\n' "$*" >&2; exit 1; }

usage() {
    cat <<'EOF'
用法:
  install_node.sh --host HOST --token TOKEN
  YAGP_DAEMON_VERSION=TAG install_node.sh reenroll --node-id ID --host HOST --token TOKEN
  YAGP_DAEMON_VERSION=TAG install_node.sh upgrade --node-id ID
  install_node.sh uninstall --node-id ID
EOF
}

action=install
case "${1:-}" in
    reenroll|upgrade|uninstall) action=$1; shift ;;
esac
[[ "${1:-}" != "--help" ]] || { usage; exit 0; }
[[ $EUID -eq 0 ]] || die "请使用 root 用户运行此脚本"

node_id=
panel_host=
token=
while (($#)); do
    [[ $# -ge 2 ]] || die "缺少参数值: $1"
    case "$action:$1" in
        reenroll:--node-id|upgrade:--node-id|uninstall:--node-id) node_id=$2 ;;
        install:--host|reenroll:--host) panel_host=$2 ;;
        install:--token|reenroll:--token) token=$2 ;;
        *) usage; die "未知参数: $1" ;;
    esac
    shift 2
done

state_dir=/etc/yagp-daemon

if command -v systemctl >/dev/null 2>&1 && [[ -d /run/systemd/system ]]; then
    manager=systemd
elif command -v rc-service >/dev/null 2>&1 && [[ -x /sbin/openrc-run ]]; then
    manager=openrc
else
    die "节点安装需要 systemd 或 OpenRC"
fi

if [[ "$action" != install ]]; then
    [[ "$node_id" =~ ^[1-9][0-9]{0,17}$ ]] || die "请填写面板中的节点 ID"
    base_path="$state_dir/$node_id"
    bridge_unit="yagp-daemon-$node_id"
    gost_unit="yagp-gost-$node_id"
    nft_table="yagp_node_$node_id"
    quota_table="yagp_quota_$node_id"
    if [[ -x "$base_path/yagp-daemon" && -f "$base_path/.runtime-layout" ]] &&
        [[ $(cat "$base_path/.runtime-layout") =~ ^[1-9][0-9]*$ ]]; then
        nft_table=yagp_node
        quota_table=yagp_quota
        if [[ "$manager" == systemd ]]; then
            bridge_unit=yagp-daemon
            gost_unit=yagp-gost
        fi
    fi
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
    if [[ -f "$base_path/nft.json" ]] && command -v nft >/dev/null 2>&1 &&
        nft list table inet "$nft_table" >/dev/null 2>&1; then
        nft delete table inet "$nft_table"
    fi
    if command -v nft >/dev/null 2>&1 &&
        nft list table inet "$quota_table" >/dev/null 2>&1; then
        nft delete table inet "$quota_table"
    fi
    rm -f -- "$base_path/yagp-daemon" "$base_path/gost" "$base_path/.runtime-layout"
    printf '节点 %s 服务已卸载，配置位于 %s\n' "$node_id" "$base_path"
    exit 0
fi

for installed in "$state_dir"/*/yagp-daemon; do
    [[ -f "$installed" ]] || continue
    installed_dir=${installed%/*}
    [[ ${installed_dir##*/} =~ ^[1-9][0-9]{0,17}$ ]] || continue
    if [[ "$action" == install || "$installed_dir" != "$base_path" ]]; then
        die "每台服务器只允许安装一个节点。已安装节点位于 $installed_dir。"
    fi
done

if [[ "$action" == install && "$manager" == systemd ]]; then
    for unit in yagp-daemon yagp-gost; do
        [[ $(systemctl show --property=LoadState --value "$unit.service" 2>/dev/null || true) == not-found ]] ||
            die "已有服务 $unit.service，请先检查该服务。"
    done
fi

if [[ "$action" == upgrade || "$action" == reenroll ]]; then
    [[ -n "${YAGP_DAEMON_VERSION:-}" || -n "${YAGP_DAEMON_BIN:-}" ]] ||
        die '请通过 YAGP_DAEMON_VERSION 指定当前面板的版本标签，或通过 YAGP_DAEMON_BIN 指定对应的二进制。'
    [[ -f "$base_path/node.yaml" && -x "$base_path/yagp-daemon" ]] || die "未找到已安装的节点 $node_id"
    if [[ "$manager" == systemd ]]; then
        [[ $(systemctl show --property=LoadState --value "$bridge_unit.service") == loaded ]] || die "未找到服务 $bridge_unit.service"
    else
        [[ "$action" == reenroll ]] || die "在线升级功能需要 systemd 节点"
        [[ -x "/etc/init.d/$bridge_unit" ]] || die "未找到服务 $bridge_unit"
    fi
fi
if [[ "$action" == install || "$action" == reenroll ]]; then
    [[ -n "$panel_host" && -n "$token" ]] || { usage; exit 1; }
fi

case "$(uname -m)" in
    x86_64) arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    *) die "当前发布支持 Linux amd64 和 arm64" ;;
esac

release_base=https://release.hanzec-overseas.cn/yagp
release_path=latest
node_version="${YAGP_DAEMON_VERSION:-}"
if [[ -n "$node_version" ]]; then
    release_path="releases/download/$node_version"
fi

mkdir -p -- "$state_dir"
tmp_dir=$(mktemp -d -- "$state_dir/.install.XXXXXXXX")
stage="$tmp_dir/node"
restore_bridge=false
bridge_service() {
    if [[ "$manager" == systemd ]]; then
        systemctl "$1" "$bridge_unit.service"
    else
        rc-service "$bridge_unit" "$1"
    fi
}
cleanup() {
    local result=$?
    if (( result != 0 )) && [[ "$restore_bridge" == true ]] && ! bridge_service start; then
        printf '原守护进程启动失败，请检查 %s\n' "$bridge_unit" >&2
    fi
    if (( result != 0 )) && [[ "$action" == install && -n "$node_id" ]]; then
        printf '节点已完成注册，token 已失效\n' >&2
        if [[ -d "$stage" ]]; then
            printf '注册文件已保留在 %s\n' "$stage" >&2
            return
        fi
        printf '节点配置已保留在 %s，请检查服务后重新启动\n' "$base_path" >&2
        if [[ "$manager" == systemd ]]; then
            printf '查看日志: journalctl -u %s -u %s\n' "$bridge_unit" "$gost_unit" >&2
            if [[ "$bridge_unit" == yagp-daemon ]]; then
                printf '修复后运行: %s/yagp-daemon update-runtime -c %s/node.yaml\n' "$base_path" "$base_path" >&2
            fi
        fi
    fi
    rm -rf -- "$tmp_dir"
}
trap cleanup EXIT
mkdir -p -- "$tmp_dir/bridge"

bridge_bin="${YAGP_DAEMON_BIN:-}"
if [[ -z "$bridge_bin" ]]; then
    bridge_url="$release_base/$release_path/yagp-daemon-linux-$arch.tar.gz"
    printf '正在下载 yagp-daemon: %s\n' "$bridge_url" >&2
    curl -fSL --retry 2 --connect-timeout 10 --speed-limit 1 --speed-time 20 \
        --write-out 'HTTP %{http_code}, %{size_download} bytes in %{time_total}s\n' \
        -o "$tmp_dir/bridge.tar.gz" -- "$bridge_url" >&2 ||
        die 'yagp-daemon 下载失败，请检查上面的 curl 日志。'
    tar -xzf "$tmp_dir/bridge.tar.gz" -C "$tmp_dir/bridge"
    if [[ "$manager" == systemd ]]; then
        bridge_bin="$tmp_dir/bridge/yagp-daemon-systemd-linux-$arch"
    else
        bridge_bin="$tmp_dir/bridge/yagp-daemon-linux-$arch"
    fi
fi
[[ -f "$bridge_bin" ]] || die "找不到适用于 $manager 的 yagp-daemon 程序: $bridge_bin"

if [[ "$action" == upgrade ]]; then
    candidate="$tmp_dir/yagp-daemon"
    install -m 0700 -- "$bridge_bin" "$candidate"
    upgrade_unit="yagp-upgrade-$node_id.service"
    if ! systemd-run --wait --collect --unit="$upgrade_unit" --property=Type=exec \
        --working-directory="$base_path" -- "$candidate" --upgrade-installed -c "$base_path/node.yaml" --node-id "$node_id"; then
        die "升级失败，请查看日志: journalctl -u $upgrade_unit"
    fi
    printf '节点 %s 的 yagp-daemon 已就绪，可在面板管理后续升级。\n' "$node_id"
    exit 0
fi

if [[ "$action" == reenroll ]]; then
    candidate="$tmp_dir/yagp-daemon"
    install -m 0700 -- "$bridge_bin" "$candidate"
    runtime_layout=$("$candidate" --runtime-layout-version 2>/dev/null || true)
    if [[ -f "$base_path/.runtime-layout" && ! "$runtime_layout" =~ ^[1-9][0-9]*$ ]]; then
        die '该节点使用新版运行布局，请选择支持当前布局的 yagp-daemon。'
    fi
    if [[ "$manager" == systemd ]]; then
        if systemctl is-active --quiet "$bridge_unit.service"; then restore_bridge=true; fi
    elif rc-service "$bridge_unit" status >/dev/null 2>&1; then
        restore_bridge=true
    fi
    # A running daemon may still be writing refreshed credentials for the old registration.
    bridge_service stop || die "无法停止节点守护服务 $bridge_unit"
    if ! registered_id=$(printf '%s' "$token" | "$candidate" enroll --host "$panel_host" --existing "$base_path/node.yaml"); then
        die "节点未完成重新注册。若 token 已失效，请在面板生成新的 token 后重试。"
    fi
    unset token
    [[ "$registered_id" == "$node_id" ]] || die "yagp-daemon 返回了不同的节点 ID"
    mv -fT -- "$candidate" "$base_path/yagp-daemon" || die "节点已重新注册，但未能替换守护进程"
    if [[ "$manager" == systemd && "$bridge_unit" != yagp-daemon && "$runtime_layout" =~ ^[1-9][0-9]*$ ]]; then
        "$base_path/yagp-daemon" update-runtime -c "$base_path/node.yaml" || die '节点已重新注册。服务迁移失败，请检查上面的日志。'
    else
        if [[ "$manager" == openrc && "$runtime_layout" =~ ^[1-9][0-9]*$ && ! -f "$base_path/.runtime-layout" ]]; then
            printf '%s\n' "$runtime_layout" > "$base_path/.runtime-layout"
        fi
        if ! bridge_service start; then
            if [[ "$manager" == systemd ]]; then
                die "节点已重新注册。服务启动失败，请查看日志: journalctl -u $bridge_unit"
            fi
            die "节点已重新注册。服务启动失败，请检查 $bridge_unit"
        fi
    fi
    restore_bridge=false
    printf '节点 %s 已重新注册，面板地址 %s\n' "$node_id" "$panel_host"
    exit 0
fi

mkdir -- "$stage"
install -m 0700 -- "$bridge_bin" "$stage/yagp-daemon"
runtime_layout=$("$stage/yagp-daemon" --runtime-layout-version 2>/dev/null || true)
if [[ -f "$tmp_dir/bridge/LICENSE" ]]; then
    install -m 0600 -- "$tmp_dir/bridge/LICENSE" "$stage/LICENSE"
fi
printf '正在准备 Gost。\n' >&2
"$stage/yagp-daemon" prepare-gost --output "$stage/gost" ||
    die 'Gost 准备失败，尚未提交注册令牌。请检查上面的日志。'

if ! node_id=$(printf '%s' "$token" | "$stage/yagp-daemon" enroll --host "$panel_host" --output "$stage"); then
    die "节点未安装。若 token 已失效，请在面板生成新的 token 后重试。"
fi
unset token
[[ "$node_id" =~ ^[1-9][0-9]{0,17}$ ]] || die "yagp-daemon 未返回有效的节点 ID"
base_path="$state_dir/$node_id"
gost_unit="yagp-gost-$node_id"
bridge_unit="yagp-daemon-$node_id"
mv -T -n -- "$stage" "$base_path"
[[ ! -e "$stage" ]] || die "节点安装目录已存在: $base_path"

if [[ "$runtime_layout" =~ ^[1-9][0-9]*$ ]]; then
    if [[ "$manager" == systemd ]]; then
        bridge_unit=yagp-daemon
        gost_unit=yagp-gost
        "$base_path/yagp-daemon" update-runtime -c "$base_path/node.yaml" || die '服务启动失败，请检查上面的日志。'
        printf '节点 %s 已启动，面板地址 %s\n' "$node_id" "$panel_host"
        exit 0
    fi
    printf '%s\n' "$runtime_layout" > "$base_path/.runtime-layout"
fi

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
ExecStartPre=$base_path/yagp-daemon ensure-gost -c $base_path/node.yaml
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

write_service "$bridge_unit" "yagp daemon $node_id" yagp-daemon -c node.yaml
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

printf '节点 %s 已启动，面板地址 %s\n' "$node_id" "$panel_host"
