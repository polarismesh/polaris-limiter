#!/usr/bin/env bash
# 轮询 /readiness、容器 READY、restartCount
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "${SCRIPT_DIR}/lib.sh"

INTERVAL=2
parse_common_args "$@"
if [[ "$SHOW_HELP" -eq 1 ]]; then
    cat <<EOF
用法: $0 --namespace NS [--pod POD] [--interval 2]
EOF
    exit 0
fi
set -- "${EXTRA_ARGS[@]+"${EXTRA_ARGS[@]}"}"
while [[ $# -gt 0 ]]; do
    case "$1" in
        --interval) INTERVAL="$2"; shift 2 ;;
        *) shift ;;
    esac
done

resolve_pod
log_step "watch $NAMESPACE/$POD  interval=${INTERVAL}s  Ctrl+C 退出"
start_port_forward
printf '%-8s %-6s %-8s %-8s %-10s %s\n' "time" "http" "overall" "ioHang" "ready" "pod"
while true; do
    code_body="$(http_get_split /readiness)"
    code="$(printf '%s\n' "$code_body" | head -n 1)"
    body="$(printf '%s\n' "$code_body" | sed '1d')"
    st="$(printf '%s' "$body" | json_field_status 2>/dev/null || echo '?')"
    io="$(printf '%s' "$body" | json_iohang_status 2>/dev/null || echo '?')"
    ready="$(limiter_container_ready)"
    now="$(date +%H:%M:%S)"
    printf '%-8s %-6s %-8s %-8s %-10s %s\n' "$now" "$code" "$st" "$io" "$ready" "$(pod_phase_line | awk '{print $2,$3,$4,$5}')"
    sleep "$INTERVAL"
done
