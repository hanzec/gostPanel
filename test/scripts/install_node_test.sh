#!/usr/bin/env bash
set -euo pipefail

# Run in the disposable Linux CI container with /run/systemd/system present.
installer="${BASH_SOURCE[0]%/*}/../../scripts/install_node.sh"
test_dir=$(mktemp -d)
trap 'command rm -rf -- "$test_dir"' EXIT
export requests="$test_dir/requests"

systemctl() { printf '%s\n' not-found; }
uname() { printf '%s\n' "$test_arch"; }
curl() {
    printf '%s\n' "${@: -1}" >> "$requests"
    return 22
}
export -f systemctl uname curl

check_download() {
    local expected=$1 output
    shift
    : > "$requests"
    if output=$(env -u YAGP_DAEMON_BIN "$@" bash -s -- --host panel.example.test --token test-token \
        < "$installer" 2>&1); then
        printf 'Expected download failure\n' >&2
        exit 1
    fi
    if [[ $(cat "$requests") != "$expected" || "$output" != *'yagp-daemon 下载失败'* ]]; then
        printf 'Expected one download from %s\nRequests: %s\nInstaller output: %s\n' \
            "$expected" "$(cat "$requests")" "$output" >&2
        exit 1
    fi
}

release_base=https://release.hanzec-overseas.cn/yagp
check_download "$release_base/latest/yagp-daemon-linux-amd64.tar.gz" \
    -u GH_PROXY -u YAGP_DAEMON_VERSION test_arch=x86_64
check_download "$release_base/releases/download/0.1.156-b9ae23e/yagp-daemon-linux-arm64.tar.gz" \
    GH_PROXY=https://proxy.example.test/ YAGP_DAEMON_VERSION=0.1.156-b9ae23e test_arch=aarch64

export nft_requests="$test_dir/nft-requests"
export test_node=900000000000000042
nft() {
    printf '%s\n' "$*" >> "$nft_requests"
    [[ "$*" == "list table inet yagp_redirect_$test_node" || "$*" == "delete table inet yagp_redirect_$test_node" ]]
}
rm() { :; }
rmdir() { :; }
export -f nft rm rmdir
bash "$installer" uninstall --node-id "$test_node"
expected="list table inet yagp_quota_$test_node
list table inet yagp_redirect_$test_node
delete table inet yagp_redirect_$test_node"
if [[ $(cat "$nft_requests") != "$expected" ]]; then
    printf 'Unexpected nftables commands during uninstall:\n%s\n' "$(cat "$nft_requests")" >&2
    exit 1
fi
