#!/usr/bin/env bash
set -euo pipefail

# Run in the disposable Linux CI container with /run/systemd/system present.
installer="${BASH_SOURCE[0]%/*}/../../scripts/install_node.sh"
test_dir=$(mktemp -d)
trap 'command rm -rf -- "$test_dir"' EXIT
export requests="$test_dir/requests"
export service_requests="$test_dir/service-requests"
export installed_unit_state=not-found

systemctl() {
    printf '%s\n' "$*" >> "$service_requests"
    if [[ "$1" == show ]]; then
        printf 'LoadState=%s\n' "$installed_unit_state"
    fi
}
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

unset -f rm rmdir nft
export flat_root="$test_dir/flat"
export daemon_requests="$test_dir/daemon-requests"
sed "s|^state_dir=.*|state_dir=$flat_root|" "$installer" > "$test_dir/install-flat.sh"
flat_installer="$test_dir/install-flat.sh"
cat > "$test_dir/daemon" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$daemon_requests"
case "$1" in
    --runtime-layout-version) printf '3\n' ;;
    --node-id) sed -n 's/^node_id: //p' "$3" ;;
    prepare-gost) install -m 0700 /dev/null "$3" ;;
    enroll)
        if [[ "$4" == --output ]]; then
            printf 'node_id: %s\n' "$test_node" > "$5/node.yaml"
            printf 'api: {}\n' > "$5/gost.yaml"
            printf 'refresh_token: fixture\n' > "$5/panel-credentials.yaml"
        fi
        printf '%s\n' "$test_node"
        ;;
    update-runtime) printf '3\n' > "${3%/*}/.runtime-layout" ;;
    --upgrade-installed)
        [[ "$PWD" == "$flat_root" ]]
        [[ "$*" == "--upgrade-installed -c $flat_root/node.yaml --node-id $test_node" ]]
        ;;
    *) exit 1 ;;
esac
EOF
chmod +x "$test_dir/daemon"
export YAGP_DAEMON_BIN="$test_dir/daemon" test_arch=x86_64
bash "$flat_installer" --host panel.example.test --token test-token
[[ ! -e "$flat_root/$test_node" && -x "$flat_root/yagp-daemon" && -x "$flat_root/gost" ]]
[[ -f "$flat_root/node.yaml" && -f "$flat_root/gost.yaml" && -f "$flat_root/panel-credentials.yaml" ]]
[[ $(cat "$flat_root/.runtime-layout") == 3 ]]
grep -Fx "update-runtime -c $flat_root/node.yaml" "$daemon_requests"

export installed_unit_state=loaded
if bash "$flat_installer" upgrade --node-id 99; then
    printf 'Expected a mismatched node ID to be rejected\n' >&2
    exit 1
fi
export run_requests="$test_dir/run-requests"
export run_help='--wait --collect'
systemd-run() {
    if [[ "$1" == --help ]]; then printf '%s\n' "$run_help"; return; fi
    printf '%s\n' "$*" >> "$run_requests"
    [[ "$1" == --wait && "$2" == --collect && "$3" == "--unit=yagp-upgrade-$test_node.service" ]]
    [[ "$4" == "--property=WorkingDirectory=$flat_root" && "$5" == -- ]]
    shift 5
    (cd -- "$flat_root" && "$@")
}
export -f systemd-run
bash "$flat_installer" upgrade --node-id "$test_node"
[[ $(wc -l < "$run_requests") -eq 1 ]]
export run_help=
bash "$flat_installer" upgrade --node-id "$test_node"
[[ $(wc -l < "$run_requests") -eq 1 ]]
grep -Fx -- "--upgrade-installed -c $flat_root/node.yaml --node-id $test_node" "$daemon_requests"

: > "$service_requests"
bash "$flat_installer" reenroll --node-id "$test_node" --host panel.example.test --token test-token
grep -Fx "enroll --host panel.example.test --existing $flat_root/node.yaml" "$daemon_requests"
grep -Fx 'stop yagp-daemon.service' "$service_requests"
grep -Fx 'start yagp-daemon.service' "$service_requests"

: > "$service_requests"
sed -i "s|/etc/systemd/system/|$test_dir/systemd-units/|g" "$flat_installer"
bash "$flat_installer" uninstall --node-id "$test_node"
grep -Fx 'disable --now yagp-daemon yagp-gost' "$service_requests"
[[ ! -e "$flat_root/yagp-daemon" && ! -e "$flat_root/gost" && ! -e "$flat_root/.runtime-layout" ]]
[[ -f "$flat_root/node.yaml" && -f "$flat_root/gost.yaml" && -f "$flat_root/panel-credentials.yaml" ]]
: > "$service_requests"
bash "$flat_installer" uninstall --node-id "$test_node"
[[ ! -s "$service_requests" ]]

export openrc_root="$test_dir/openrc"
export openrc_units="$test_dir/init.d"
openrc_installer="$test_dir/install-openrc.sh"
sed -e "s|^state_dir=.*|state_dir=$openrc_root|" \
    -e "s|/run/systemd/system|$test_dir/no-systemd|" \
    -e "s:/sbin/openrc-run:$test_dir/daemon:" \
    -e "s|/etc/init.d/|$openrc_units/|g" "$installer" > "$openrc_installer"

mkdir "$test_dir/openrc-bin"
cat > "$test_dir/openrc-bin/rc-service" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$service_requests"
[[ "$*" != "yagp-daemon-$test_node start" || ! -e "$openrc_root/node.yaml" || "$fail_flat_start" == false ]]
EOF
chmod +x "$test_dir/openrc-bin/rc-service"
export PATH="$test_dir/openrc-bin:$PATH"
export fail_flat_start=true

legacy="$openrc_root/$test_node"
mkdir -p "$legacy" "$openrc_units"
cp "$YAGP_DAEMON_BIN" "$legacy/yagp-daemon"
printf 'node_id: %s\nreport_ca_file: %s/panel.crt\n' "$test_node" "$legacy" > "$legacy/node.yaml"
printf 'certFile: %s/server.crt\n' "$legacy" > "$legacy/gost.yaml"
printf '2\n' > "$legacy/.runtime-layout"
printf 'quota state\n' > "$legacy/.traffic-quota.json"
for unit in "yagp-daemon-$test_node" "yagp-gost-$test_node"; do
    printf 'directory="%s"\n' "$legacy" > "$openrc_units/$unit"
    chmod +x "$openrc_units/$unit"
done

if bash "$openrc_installer" reenroll --node-id "$test_node" --host panel.example.test --token test-token; then
    printf 'Expected OpenRC migration to fail when the daemon cannot start\n' >&2
    exit 1
fi
[[ ! -e "$openrc_root/node.yaml" && $(cat "$legacy/.runtime-layout") == 2 ]]
[[ $(cat "$legacy/.traffic-quota.json") == 'quota state' ]]
grep -Fx "report_ca_file: $legacy/panel.crt" "$legacy/node.yaml"
grep -Fx "directory=\"$legacy\"" "$openrc_units/yagp-gost-$test_node"

export fail_flat_start=false
bash "$openrc_installer" reenroll --node-id "$test_node" --host panel.example.test --token test-token
[[ ! -e "$legacy" && $(cat "$openrc_root/.runtime-layout") == 3 ]]
[[ $(cat "$openrc_root/.traffic-quota.json") == 'quota state' ]]
grep -Fx "report_ca_file: $openrc_root/panel.crt" "$openrc_root/node.yaml"
grep -Fx "certFile: $openrc_root/server.crt" "$openrc_root/gost.yaml"
grep -Fx "directory=\"$openrc_root\"" "$openrc_units/yagp-gost-$test_node"
