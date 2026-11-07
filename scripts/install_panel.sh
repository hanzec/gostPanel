#!/bin/bash

# yagp Binary Installation Script
# Original author: code-gopher
# Upstream project: https://github.com/code-gopher/gostPanel

set -e

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

# Check if running as root
if [ "$EUID" -ne 0 ]; then
  echo -e "${RED}错误: 请以 root 用户运行此脚本${NC}"
  exit 1
fi

# Configuration
APP_NAME="yagp"
INSTALL_DIR="/usr/local/bin"
SERVICE_NAME="gost-panel"
CONFIG_DIR="/etc/gost-panel"
SERVICE_FILE="/etc/systemd/system/$SERVICE_NAME.service"
REPO=${YAGP_REPO:-hanzec/gostPanel}
GH_PROXY=${GH_PROXY:-""}

# Detect Architecture
ARCH=$(uname -m)
case $ARCH in
    x86_64)  PLATFORM="linux-amd64" ;;
    aarch64|arm64) PLATFORM="linux-arm64" ;;
    *) echo -e "${RED}不支持的架构: $ARCH${NC}"; exit 1 ;;
esac

# Functions
function info() { echo -e "${GREEN}[INFO]${NC} $1"; }
function warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
function error() { echo -e "${RED}[ERROR]${NC} $1"; exit 1; }

function check_memory() {
    total_mem=$(free -m | grep Mem | awk '{print $2}')
    if [ "$total_mem" -le 128 ]; then
        error "系统内存不足 ($total_mem MB)。二进制部署模式要求内存必须大于 128MB。"
    fi
    info "内存检查通过: $total_mem MB"
}

function install_panel() {
    check_memory
    TEMP_DIR=$(mktemp -d)
    TAR_FILE="$TEMP_DIR/$APP_NAME.tar.gz"
    DOWNLOAD_URL="${GH_PROXY}https://github.com/$REPO/releases/latest/download/$APP_NAME-$PLATFORM.tar.gz"
    
    info "正在下载发布包: $DOWNLOAD_URL"
    if ! curl -fL "$DOWNLOAD_URL" -o "$TAR_FILE"; then
        rm -rf "$TEMP_DIR"
        error "无法下载 $APP_NAME-$PLATFORM.tar.gz，请确认 $REPO 已发布 yagp 安装包。"
    fi
    
    info "正在解压..."
    if ! tar -xzf "$TAR_FILE" -C "$TEMP_DIR"; then
        rm -rf "$TEMP_DIR"
        error "无法解压发布包"
    fi
    EXTRACTED_BIN="$TEMP_DIR/$APP_NAME-$PLATFORM"
    if [ ! -f "$EXTRACTED_BIN" ] || [ ! -f "$TEMP_DIR/LICENSE" ]; then
        rm -rf "$TEMP_DIR"
        error "发布包缺少二进制文件或许可证"
    fi

    # 处理配置目录
    mkdir -p "$CONFIG_DIR/config" "$CONFIG_DIR/data"
    cp "$TEMP_DIR/LICENSE" "$CONFIG_DIR/LICENSE"
    chmod +x "$EXTRACTED_BIN"
    mv "$EXTRACTED_BIN" "$INSTALL_DIR/$APP_NAME"

    if [ -f "$TEMP_DIR/config/config.yaml" ]; then
        if [ ! -f "$CONFIG_DIR/config/config.yaml" ]; then
            cp "$TEMP_DIR/config/config.yaml" "$CONFIG_DIR/config/config.yaml"
            info "已从发布包初始化配置文件: $CONFIG_DIR/config/config.yaml"
        else
            warn "配置文件已存在，跳过覆盖"
        fi
        
        # 替换端口 (如果有指定)
        local CUSTOM_PORT="$1"
        if [ -n "$CUSTOM_PORT" ]; then
            info "正在根据参数修改端口为: $CUSTOM_PORT"
            # 替换 config.yaml 中的端口配置
            if [ -f "$CONFIG_DIR/config/config.yaml" ]; then
                sed -i "s/port: \":.*\"/port: \":$CUSTOM_PORT\"/g" "$CONFIG_DIR/config/config.yaml"
            fi
        fi
    fi

    # 清理临时目录
    rm -rf "$TEMP_DIR"

    # Create systemd service
    info "创建系统服务..."
    cat > "$SERVICE_FILE" <<EOF
[Unit]
Description=yagp Service
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=$CONFIG_DIR
ExecStart=$INSTALL_DIR/$APP_NAME -c $CONFIG_DIR/config/config.yaml
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload
    systemctl enable "$SERVICE_NAME"
    systemctl restart "$SERVICE_NAME"

    info "yagp 安装完成并已启动！"
    systemctl status "$SERVICE_NAME" --no-pager
    
    # 获取最终端口
    FINAL_PORT=$(grep "port:" "$CONFIG_DIR/config/config.yaml" | awk -F'"' '{print $2}' | sed 's/://')
    [ -z "$FINAL_PORT" ] && FINAL_PORT="39100"
    
    # 获取本机外网IP
    PUBLIC_IP=$(curl -s https://api.ipify.org || curl -s ifconfig.me || echo "127.0.0.1")

    # 获取本机内网IP
    INTERNAL_IP=$(hostname -I 2>/dev/null | awk '{print $1}')
    if [ -z "$INTERNAL_IP" ]; then
        INTERNAL_IP=$(ip -o route get to 8.8.8.8 2>/dev/null | sed -n 's/.*src \([0-9.]\+\).*/\1/p')
    fi
    [ -z "$INTERNAL_IP" ] && INTERNAL_IP="127.0.0.1"
    
    echo -e "\n${GREEN}==============================================${NC}"
    echo -e "   yagp 安装成功！"
    echo -e "   外网访问: http://${PUBLIC_IP}:${FINAL_PORT}"
    echo -e "   内网访问: http://${INTERNAL_IP}:${FINAL_PORT}"
    echo -e "   管理员登录由 Authentik 正向认证代理处理"
    echo -e "${GREEN}==============================================${NC}\n"
}

function uninstall_panel() {
    info "正在卸载 yagp..."
    systemctl stop "$SERVICE_NAME" || true
    systemctl disable "$SERVICE_NAME" || true
    rm -f "$SERVICE_FILE"
    systemctl daemon-reload
    rm -f "$INSTALL_DIR/$APP_NAME"
    warn "二进制文件已移除。配置文件目录 $CONFIG_DIR (包含数据库) 已保留。"
    info "卸载完成。"
}

# Main
ACTION="install"
PORT=""

# Check arguments
if [ -n "$1" ]; then
    # If first arg is a number, assume it's the port and action is install
    if [[ "$1" =~ ^[0-9]+$ ]]; then
        PORT="$1"
    elif [ "$1" == "install" ]; then
        ACTION="install"
        if [ "$2" == "--port" ]; then
            PORT="$3"
            [ -n "$PORT" ] || error "请为 --port 指定端口号"
        else
            PORT="$2"
        fi
    elif [ "$1" == "uninstall" ]; then
        ACTION="uninstall"
    else
        echo "用法: $0 [port] 或 $0 install [--port] [port] 或 $0 uninstall"
        exit 1
    fi
fi
if [ -n "$PORT" ] && ! [[ "$PORT" =~ ^[0-9]+$ ]]; then
    error "无效的端口号: $PORT"
fi

case $ACTION in
    install)
        install_panel "$PORT"
        ;;
    uninstall)
        uninstall_panel
        ;;
esac
