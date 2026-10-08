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
LOG_DIR="${LOG_DIR:-}"
SHOW_HELP=0
EXTRA_ARGS=()
PF_PID=""
RUN_DIR=""

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
            --log-dir)      LOG_DIR="$2"; shift 2 ;;
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

# ---------- 运行日志与产物 ----------

default_log_base() { echo "${LOG_DIR:-${SCRIPT_DIR}/.logs}"; }

# archive_ext：有 zip 用 zip，否则退回 tar.gz。
archive_ext() {
    if command -v zip >/dev/null 2>&1; then echo zip; else echo tar.gz; fi
}

# pack_archive OUT PARENT ENTRY...：在 PARENT 下把 ENTRY 打包为 OUT（扩展名由 archive_ext 决定）。
pack_archive() {
    local out="$1" parent="$2"
    shift 2
    rm -f "$out"
    if [[ "$out" == *.zip ]]; then
        (cd "$parent" && zip -rq "$out" "$@")
    else
        tar -czf "$out" -C "$parent" "$@"
    fi
}

# init_run_log NAME：创建本次运行目录，此后 stdout/stderr 同时写入 run.log。
# 默认目录为脚本同级 .logs/（已被 .gitignore 忽略），可用 --log-dir / LOG_DIR 覆盖。
init_run_log() {
    local name="$1"
    local base
    base="$(default_log_base)"
    RUN_DIR="${base}/$(date +%Y%m%d-%H%M%S)-${name}"
    mkdir -p "$RUN_DIR"
    RUN_DIR="$(cd "$RUN_DIR" && pwd)"
    exec > >(tee -a "${RUN_DIR}/run.raw.log") 2>&1
    log_info "日志目录: $RUN_DIR"
}

# save_http_snapshot PHASE：保存当前 /readiness、/liveness 响应与容器状态，需已 port-forward。
save_http_snapshot() {
    [[ -n "$RUN_DIR" ]] || return 0
    local phase="$1" path
    for path in readiness liveness; do
        http_get_split "/${path}" > "${RUN_DIR}/${phase}-${path}.txt" || true
    done
    limiter_ready > "${RUN_DIR}/${phase}-containers.txt" 2>&1 || true
}

# pod_tail_file PATH LINES：容器内文件取尾部；FIFO 直接跳过，避免读端阻塞。
pod_tail_file() {
    pod_exec sh -c 'f="$1"; if [ -p "$f" ]; then echo "(fifo, skipped)"; elif [ -f "$f" ]; then tail -n "$2" "$f"; else echo "(missing: $f)"; fi' \
        _ "$1" "$2"
}

collect_artifacts() {
    [[ -n "$RUN_DIR" && -n "$POD" && -n "$NAMESPACE" ]] || return 0
    local d="$RUN_DIR" log_dir restarts
    log_dir="$(dirname "$PROBE_PATH")"
    kubectl get pod -n "$NAMESPACE" "$POD" -o yaml > "${d}/pod.yaml" 2>&1 || true
    kubectl describe pod -n "$NAMESPACE" "$POD" > "${d}/pod-describe.txt" 2>&1 || true
    kubectl get events -n "$NAMESPACE" --field-selector "involvedObject.name=${POD}" \
        --sort-by=.lastTimestamp > "${d}/events.txt" 2>&1 || true
    kubectl logs -n "$NAMESPACE" "$POD" -c "$CONTAINER" --tail=5000 > "${d}/container-stdout.log" 2>&1 || true
    restarts="$(limiter_restart_count 2>/dev/null || echo 0)"
    if [[ "${restarts:-0}" != "0" ]]; then
        kubectl logs -n "$NAMESPACE" "$POD" -c "$CONTAINER" --previous --tail=5000 \
            > "${d}/container-stdout-previous.log" 2>&1 || true
    fi
    pod_exec ls -la "$log_dir" > "${d}/pod-log-dir.txt" 2>&1 || true
    pod_tail_file "${log_dir}/polaris-limiter.log" 5000 > "${d}/polaris-limiter.log" 2>&1 || true
    pod_tail_file "$PROBE_PATH" 200 > "${d}/polaris-limiter-probe.log" 2>&1 || true
}

# finalize_run_log RC：收集产物、去除颜色码、打包为 ${RUN_DIR}.zip（无 zip 时为 .tar.gz）。
finalize_run_log() {
    local rc="$1" archive
    archive="${RUN_DIR}.$(archive_ext)"
    collect_artifacts
    if [[ "$rc" -eq 0 ]]; then log_ok "结果: PASS"; else log_err "结果: FAIL (exit $rc)"; fi
    log_info "日志目录: $RUN_DIR"
    log_info "打包文件: $archive"
    # 关闭 stdout/stderr 让 tee 读到 EOF，再处理 run.log
    exec 1>&- 2>&-
    sleep 0.5
    if command -v perl >/dev/null 2>&1; then
        perl -pe 's/\e\[[0-9;]*m//g' "${RUN_DIR}/run.raw.log" > "${RUN_DIR}/run.log" \
            && rm -f "${RUN_DIR}/run.raw.log"
    else
        mv "${RUN_DIR}/run.raw.log" "${RUN_DIR}/run.log"
    fi
    pack_archive "$archive" "$(dirname "$RUN_DIR")" "$(basename "$RUN_DIR")" >/dev/null 2>&1 || true
}

on_exit() {
    local rc=$?
    stop_port_forward
    if [[ -n "$RUN_DIR" ]]; then
        finalize_run_log "$rc"
    fi
}
trap on_exit EXIT
