#!/usr/bin/env bash
# 正常态检查：/、/liveness、/readiness、探测文件、容器 READY
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "${SCRIPT_DIR}/lib.sh"

parse_common_args "$@"
if [[ "$SHOW_HELP" -eq 1 ]]; then
    cat <<EOF
用法: $0 --namespace NS [--pod POD] [--container polaris-limiter]

检查 limiter 正常态探针与探测文件。不注入故障。
EOF
    exit 0
fi

resolve_pod
log_step "基线检查 $NAMESPACE/$POD"
start_port_forward

fail=0

code_body="$(http_get_split /)"
code="$(printf '%s\n' "$code_body" | head -n 1)"
body="$(printf '%s\n' "$code_body" | sed '1d')"
if [[ "$code" == "200" ]] && [[ "$body" == *"polaris limit server"* ]]; then
    log_ok "GET / → 200 polaris limit server"
else
    log_err "GET / 期望 200 固定字符串，实际 code=$code body=$body"
    fail=1
fi

code_body="$(http_get_split /liveness)"
code="$(printf '%s\n' "$code_body" | head -n 1)"
body="$(printf '%s\n' "$code_body" | sed '1d')"
st="$(printf '%s' "$body" | json_field_status || true)"
if [[ "$code" == "200" ]] && [[ "$st" == "UP" ]]; then
    log_ok "GET /liveness → 200 status=UP"
else
    log_err "GET /liveness 期望 200 UP，实际 code=$code status=$st body=$body"
    fail=1
fi

code_body="$(http_get_split /readiness)"
code="$(printf '%s\n' "$code_body" | head -n 1)"
body="$(printf '%s\n' "$code_body" | sed '1d')"
st="$(printf '%s' "$body" | json_field_status || true)"
io="$(printf '%s' "$body" | json_iohang_status || true)"
if [[ "$code" == "200" ]] && [[ "$st" == "UP" ]]; then
    log_ok "GET /readiness → 200 status=UP ioHang=${io:-<none>}"
    if [[ "$io" != "UP" ]]; then
        log_warn "ioHang 不是 UP（enable=false 时仅有 basic，可接受）"
    fi
else
    log_err "GET /readiness 期望 200 UP，实际 code=$code status=$st ioHang=$io body=$body"
    fail=1
fi

if probe_ls="$(pod_exec sh -c "ls -l '${PROBE_PATH}' 2>/dev/null; echo '---'; tail -n 3 '${PROBE_PATH}' 2>/dev/null" 2>/dev/null)"; then
    echo "$probe_ls"
    if printf '%s' "$probe_ls" | grep -q 'iohang-probe'; then
        log_ok "探测文件存在且含 iohang-probe"
    else
        log_warn "探测文件没有 iohang-probe 行（刚启动未满一个 interval？等 5s 再跑）"
    fi
else
    log_warn "无法读取 $PROBE_PATH（路径或权限）"
fi

log_info "容器状态:"
limiter_ready
log_info "restartCount(${CONTAINER})=$(limiter_restart_count)"
pod_phase_line
print_endpoints
query_polaris_instances

if [[ "$fail" -ne 0 ]]; then
    die "基线检查未通过"
fi
log_ok "基线检查通过"
exit 0
