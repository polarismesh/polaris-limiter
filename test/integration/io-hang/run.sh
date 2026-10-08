#!/usr/bin/env bash
# 串行验收：基线 → 注入 hang → readiness 503 且不重启 → 恢复 → readiness 200
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "${SCRIPT_DIR}/lib.sh"

SKIP_INJECT=false
WAIT_DOWN=25
WAIT_UP=40
WAIT_NOT_READY=15
orig_args=("$@")
parse_common_args "$@"
if [[ "$SHOW_HELP" -eq 1 ]]; then
    cat <<EOF
用法: $0 --namespace NS [--pod POD] [--skip-inject] [--wait-down 25] [--wait-up 40] [--wait-not-ready 15] [--log-dir DIR]

完整 IOHang 探针验收。默认会注入 FIFO，结束后自动 recover。
日志与产物写入 DIR/<时间>-<pod>/ 并打包为同名 .zip，无 zip 命令时为 .tar.gz（默认 DIR=脚本同级 .logs/）。
EOF
    exit 0
fi
set -- "${EXTRA_ARGS[@]+"${EXTRA_ARGS[@]}"}"
while [[ $# -gt 0 ]]; do
    case "$1" in
        --skip-inject) SKIP_INJECT=true; shift ;;
        --wait-down)   WAIT_DOWN="$2"; shift 2 ;;
        --wait-up)     WAIT_UP="$2"; shift 2 ;;
        --wait-not-ready) WAIT_NOT_READY="$2"; shift 2 ;;
        *) shift ;;
    esac
done

wait_readiness() {
    local expect_code="$1"
    local expect_status="$2"
    local timeout_s="$3"
    local deadline=$((SECONDS + timeout_s))
    while (( SECONDS < deadline )); do
        local code_body code body st
        code_body="$(http_get_split /readiness)"
        code="$(printf '%s\n' "$code_body" | head -n 1)"
        body="$(printf '%s\n' "$code_body" | sed '1d')"
        st="$(printf '%s' "$body" | json_field_status 2>/dev/null || echo '')"
        if [[ "$code" == "$expect_code" ]] && [[ "$st" == "$expect_status" ]]; then
            log_ok "/readiness code=$code status=$st"
            printf '%s\n' "$body"
            return 0
        fi
        sleep 1
    done
    log_err "等待 /readiness code=$expect_code status=$expect_status 超时 (${timeout_s}s)，最后 code=${code:-?} status=${st:-?}"
    printf '%s\n' "${body:-}"
    return 1
}

resolve_pod
init_run_log "$POD"
log_step "IOHang E2E  $NAMESPACE/$POD"

# 基线（独立进程，避免 port-forward 冲突）
"${SCRIPT_DIR}/check-baseline.sh" "${orig_args[@]}" --namespace "$NAMESPACE" --pod "$POD"
if [[ "$SKIP_INJECT" == true ]]; then
    log_ok "--skip-inject，结束"
    exit 0
fi

start_port_forward
save_http_snapshot 1-baseline
base_restarts="$(limiter_restart_count)"
log_info "注入前 restartCount=${base_restarts}"

live_code="$(http_get_split /liveness | head -n 1)"
[[ "$live_code" == "200" ]] || die "注入前 /liveness 不是 200"

log_step "注入 hang"
"${SCRIPT_DIR}/inject-hang.sh" --namespace "$NAMESPACE" --pod "$POD" --container "$CONTAINER" --probe-path "$PROBE_PATH"

log_step "等待 /readiness 503 DOWN（最多 ${WAIT_DOWN}s）"
wait_readiness 503 DOWN "$WAIT_DOWN" || {
    "${SCRIPT_DIR}/recover.sh" --namespace "$NAMESPACE" --pod "$POD" --container "$CONTAINER" --probe-path "$PROBE_PATH" || true
    die "注入后 readiness 未转 DOWN"
}
save_http_snapshot 2-hang

live_body="$(http_get_split /liveness)"
live_code="$(printf '%s\n' "$live_body" | head -n 1)"
if [[ "$live_code" != "200" ]]; then
    "${SCRIPT_DIR}/recover.sh" --namespace "$NAMESPACE" --pod "$POD" --container "$CONTAINER" --probe-path "$PROBE_PATH" || true
    die "IOHang 期间 /liveness 应为 200，实际 $live_code（探针误引入了 IO）"
fi
log_ok "/liveness 仍为 200"

cur_restarts="$(limiter_restart_count)"
if [[ "$cur_restarts" != "$base_restarts" ]]; then
    "${SCRIPT_DIR}/recover.sh" --namespace "$NAMESPACE" --pod "$POD" --container "$CONTAINER" --probe-path "$PROBE_PATH" || true
    die "restartCount 从 $base_restarts 变为 $cur_restarts，IOHang 触发了重启"
fi
log_ok "restartCount 未增加 ($cur_restarts)"

log_step "等待 limiter 容器 ready=false（最多 ${WAIT_NOT_READY}s，readinessProbe 5s × 2）"
ready_deadline=$((SECONDS + WAIT_NOT_READY))
ready_now="$(limiter_container_ready)"
while [[ "$ready_now" != "false" ]] && (( SECONDS < ready_deadline )); do
    sleep 1
    ready_now="$(limiter_container_ready)"
done
if [[ "$ready_now" == "false" ]]; then
    log_ok "limiter 容器已摘流 (ready=false)"
else
    log_warn "limiter 容器仍 ready=${ready_now}：确认探针已切到 /readiness 且 periodSeconds/failureThreshold 为 5/2"
fi
print_endpoints
query_polaris_instances

log_step "恢复"
"${SCRIPT_DIR}/recover.sh" --namespace "$NAMESPACE" --pod "$POD" --container "$CONTAINER" --probe-path "$PROBE_PATH"

log_step "等待 /readiness 200 UP（最多 ${WAIT_UP}s）"
wait_readiness 200 UP "$WAIT_UP" || die "恢复后 readiness 未回到 UP"
save_http_snapshot 3-recovered

cur_restarts="$(limiter_restart_count)"
[[ "$cur_restarts" == "$base_restarts" ]] || die "恢复阶段 restartCount 变化: $base_restarts -> $cur_restarts"
log_ok "全程未重启"

log_ok "IOHang E2E 通过"
exit 0
