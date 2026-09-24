#!/usr/bin/env bash
# test/integration/io-hang/lib.sh — kubectl 访问 limiter Pod 的公共函数
# shellcheck disable=SC2034

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

NAMESPACE="${NAMESPACE:-}"
POD="${POD:-}"
CONTAINER="${CONTAINER:-polaris-limiter}"
HTTP_PORT="${HTTP_PORT:-8100}"
LOCAL_PORT="${LOCAL_PORT:-18100}"
PROBE_PATH="${PROBE_PATH:-/root/log/polaris-limiter-probe.log}"
SERVICE="${SERVICE:-}"
POLARIS_HTTP="${POLARIS_HTTP:-}"
POLARIS_NS="${POLARIS_NS:-Polaris}"
POLARIS_SVC="${POLARIS_SVC:-polaris.limiter}"
POLARIS_TOKEN="${POLARIS_TOKEN:-}"
SHOW_HELP=0
EXTRA_ARGS=()
PF_PID=""

log_info()  { echo -e "${CYAN}[INFO]${NC}  $*"; }
log_ok()    { echo -e "${GREEN}[OK]${NC}    $*"; }
log_warn()  { echo -e "${YELLOW}[WARN]${NC}  $*"; }
log_err()   { echo -e "${RED}[ERROR]${NC} $*"; }
log_step()  { echo -e "\n${CYAN}======== $* ========${NC}"; }

die() { log_err "$*"; exit 1; }

parse_common_args() {
    EXTRA_ARGS=()
    SHOW_HELP=0
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --namespace|-n) NAMESPACE="$2"; shift 2 ;;
            --pod)          POD="$2"; shift 2 ;;
            --container)    CONTAINER="$2"; shift 2 ;;
            --http-port)    HTTP_PORT="$2"; shift 2 ;;
            --local-port)   LOCAL_PORT="$2"; shift 2 ;;
            --probe-path)   PROBE_PATH="$2"; shift 2 ;;
            --service)      SERVICE="$2"; shift 2 ;;
            --polaris-http) POLARIS_HTTP="$2"; shift 2 ;;
            --polaris-ns)   POLARIS_NS="$2"; shift 2 ;;
            --polaris-svc)  POLARIS_SVC="$2"; shift 2 ;;
            --polaris-token) POLARIS_TOKEN="$2"; shift 2 ;;
            --help|-h)      SHOW_HELP=1; return 0 ;;
            *)              EXTRA_ARGS+=("$1"); shift ;;
        esac
    done
}

require_bin() {
    command -v "$1" >/dev/null 2>&1 || die "缺少命令: $1"
}

resolve_pod() {
    require_bin kubectl
    require_bin curl
    require_bin python3
    [[ -n "$NAMESPACE" ]] || die "请指定 --namespace / NAMESPACE"

    if [[ -z "$POD" ]]; then
        POD="$(kubectl get pods -n "$NAMESPACE" --no-headers 2>/dev/null \
            | awk '/polaris-limiter/ {print $1; exit}')"
        [[ -n "$POD" ]] || die "namespace $NAMESPACE 中未找到 polaris-limiter Pod，请 --pod 指定"
        log_info "自动选择 Pod: $POD"
    fi
    kubectl get pod -n "$NAMESPACE" "$POD" >/dev/null || die "Pod 不存在: $NAMESPACE/$POD"
}

pod_exec() {
    kubectl exec -n "$NAMESPACE" "$POD" -c "$CONTAINER" -- "$@"
}

limiter_ready() {
    kubectl get pod -n "$NAMESPACE" "$POD" \
        -o 'jsonpath={range .status.containerStatuses[*]}{.name}={.ready} restart={.restartCount}{"\n"}{end}'
}

limiter_restart_count() {
    kubectl get pod -n "$NAMESPACE" "$POD" \
        -o "jsonpath={.status.containerStatuses[?(@.name==\"${CONTAINER}\")].restartCount}"
}

limiter_container_ready() {
    kubectl get pod -n "$NAMESPACE" "$POD" \
        -o "jsonpath={.status.containerStatuses[?(@.name==\"${CONTAINER}\")].ready}"
}

pod_phase_line() {
    kubectl get pod -n "$NAMESPACE" "$POD" --no-headers
}

start_port_forward() {
    stop_port_forward
    kubectl port-forward -n "$NAMESPACE" "pod/${POD}" "${LOCAL_PORT}:${HTTP_PORT}" >/dev/null 2>&1 &
    PF_PID=$!
    local i
    for i in 1 2 3 4 5 6 7 8 9 10; do
        if curl -sf -o /dev/null --max-time 1 "http://127.0.0.1:${LOCAL_PORT}/liveness" \
            || curl -sf -o /dev/null --max-time 1 "http://127.0.0.1:${LOCAL_PORT}/"; then
            return 0
        fi
        sleep 0.3
    done
    stop_port_forward
    die "port-forward ${LOCAL_PORT}->${HTTP_PORT} 失败，换 --local-port 或检查网络策略"
}

stop_port_forward() {
    if [[ -n "${PF_PID}" ]] && kill -0 "$PF_PID" 2>/dev/null; then
        kill "$PF_PID" 2>/dev/null || true
        wait "$PF_PID" 2>/dev/null || true
    fi
    PF_PID=""
}

# 输出: 第一行 HTTP 状态码，其余为 body
http_get_split() {
    local path="$1"
    local raw code body
    raw="$(curl -sS -m 3 -w "\n%{http_code}" "http://127.0.0.1:${LOCAL_PORT}${path}" || true)"
    code="$(printf '%s' "$raw" | tail -n 1)"
    body="$(printf '%s' "$raw" | sed '$d')"
    printf '%s\n%s' "$code" "$body"
}

json_field_status() {
    python3 -c 'import json,sys
raw=sys.stdin.read()
try:
    d=json.loads(raw)
except Exception:
    sys.exit(2)
print(d.get("status",""))
'
}

json_iohang_status() {
    python3 -c 'import json,sys
raw=sys.stdin.read()
try:
    d=json.loads(raw)
except Exception:
    sys.exit(2)
comp=(d.get("components") or {}).get("ioHang") or {}
print(comp.get("status",""))
'
}

print_endpoints() {
    [[ -n "$SERVICE" ]] || return 0
    log_info "Endpoints $NAMESPACE/$SERVICE:"
    kubectl get endpoints -n "$NAMESPACE" "$SERVICE" -o wide 2>/dev/null || log_warn "未找到 Endpoints $SERVICE"
}

query_polaris_instances() {
    [[ -n "$POLARIS_HTTP" ]] || return 0
    local url="${POLARIS_HTTP}/naming/v1/instances?namespace=${POLARIS_NS}&service=${POLARIS_SVC}&limit=100"
    local auth=()
    [[ -n "$POLARIS_TOKEN" ]] && auth=(-H "X-Polaris-Token: ${POLARIS_TOKEN}")
    log_info "Polaris 实例: $url"
    curl -sS -m 5 "${auth[@]+"${auth[@]}"}" "$url" | python3 -c '
import json,sys
try:
    d=json.loads(sys.stdin.read())
except Exception as e:
    print("parse polaris response failed:", e)
    sys.exit(0)
inst=d.get("instances") or []
print("count", len(inst))
for i in inst:
    print(" ", i.get("host"), i.get("port"), i.get("protocol"),
          "healthy="+str(i.get("healthy")), "isolate="+str(i.get("isolate")))
' || log_warn "查询北极星失败（可忽略，改控制台核对）"
}

cleanup_pf() { stop_port_forward; }
trap cleanup_pf EXIT
