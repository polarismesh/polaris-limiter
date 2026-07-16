#!/bin/bash
# =============================================================================
# provider.sh — 云端 provider 节点部署 + 自检脚本
#
# 运行位置: eee2 / eee3（每台机器可起多个不同服务实例：service-1 / service-2，
#   每服务跨 eee2/eee3 各 1 实例构成跨节点共享配额拓扑；不同服务 hashValue 不同，
#   经 SDK Maglev 一致性哈希分散到不同 limiter 节点，从而覆盖云端 2 个 limiter）
#
# 与本地 test/integration/test.sh 的差异:
#   - 云端 polaris-server 已部署在 172.16.0.5，polaris-limiter 已部署并注册为
#     Polaris/polaris.limiter —— 本脚本不再启动 limiter，只启动 provider。
#   - provider 的 polaris.yaml 已固化 172.16.0.5 + limiterService=polaris.limiter，
#     仅 ${POLARIS_TOKEN} 占位由环境变量注入（鉴权开启时设置）。
#   - provider 自身出口 IP 由 SDK 通过 dial polaris-server 自动探测（main.go 的
#     getLocalHost），无需手动指定 host；consumer 通过服务发现拿到该 IP:port。
#
# 链路: curl(任意) → consumer:18201 → provider:18200 → polaris.limiter(gRPC)
#
# 用法:
#   ./provider.sh                                       # start service-1（默认），前台守护 + 自检
#   ./provider.sh start --service GlobalRatelimitEchoServer-2 --port 18200   # 起 service-2
#   ./provider.sh stop                                  # 停止默认 service-1 并 deregister
#   ./provider.sh stop --service GlobalRatelimitEchoServer-2   # 停止 service-2（须带 --service 定位 pidfile）
#   ./provider.sh status                                # 查看运行状态 + 注册情况
#   POLARIS_TOKEN=xxx ./provider.sh                      # polaris-server 开启鉴权时
#
# 退出码: 0=成功, 1=失败
# =============================================================================
set -uo pipefail

# ======================== 颜色 ========================
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

# ======================== 默认配置 ========================
# 云端 polaris-server 固定地址（与 polaris.yaml 中一致）
POLARIS_SERVER="${POLARIS_SERVER:-172.16.0.5}"
POLARIS_TOKEN="${POLARIS_TOKEN:-}"
POLARIS_HTTP_ADDR="http://${POLARIS_SERVER}:8090"

# 业务服务：每台机器可起多个不同服务实例（service-1/service-2），每服务跨 eee2/eee3
# 各 1 实例构成跨节点共享配额拓扑；不同服务 hashValue 不同，分散到不同 limiter 节点。
NAMESPACE="${NAMESPACE:-default}"
SERVICE="${SERVICE:-GlobalRatelimitEchoServer-1}"
PORT=18200
DEBUG=false

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN="${SCRIPT_DIR}/x86-bin"
CONF="${SCRIPT_DIR}/polaris.yaml"

# ======================== 日志 helper ========================
log_info()  { echo -e "${GREEN}[INFO]${NC} $(date '+%H:%M:%S') $*"; }
log_warn()   { echo -e "${YELLOW}[WARN]${NC} $(date '+%H:%M:%S') $*"; }
log_error()  { echo -e "${RED}[ERROR]${NC} $(date '+%H:%M:%S') $*"; }
log_step() {
    echo ""
    echo -e "${CYAN}═══════════════════════════════════════════════════════════════════${NC}"
    echo -e "${CYAN}  $*${NC}"
    echo -e "${CYAN}═══════════════════════════════════════════════════════════════════${NC}"
}

# ======================== 参数解析 ========================
ACTION="start"
while [[ $# -gt 0 ]]; do
    case "$1" in
        start|stop|status|restart) ACTION="$1"; shift ;;
        --port)        PORT="$2";       shift 2 ;;
        --service)     SERVICE="$2";    shift 2 ;;
        --namespace)   NAMESPACE="$2";  shift 2 ;;
        --polaris-server) POLARIS_SERVER="$2"; POLARIS_HTTP_ADDR="http://${POLARIS_SERVER}:8090"; shift 2 ;;
        --polaris-token)  POLARIS_TOKEN="$2";  shift 2 ;;
        --debug)       DEBUG=true;      shift ;;
        -h|--help)
            cat <<EOF
用法: $0 [start|stop|status|restart] [选项]

子命令:
  start     启动 provider（默认），守护运行 + 自检 /echo 与注册状态
  stop      停止 provider（二进制收到 SIGTERM 自行 deregister）
  status    查看运行状态 + polaris 注册情况
  restart   stop + start

选项:
  --port <port>              provider 监听端口 (默认 18200)
  --service <name>           注册的业务服务名 (默认 GlobalRatelimitEchoServer-1)
  --namespace <ns>           命名空间 (默认 default)
  --polaris-server <addr>    polaris-server 地址 (默认 172.16.0.5)
  --polaris-token <token>    polaris-server 鉴权 token (开启鉴权时必填)
  --debug                    开启 SDK DEBUG 日志
  -h, --help                  展示帮助

环境变量:
  POLARIS_TOKEN              同 --polaris-token（注入 polaris.yaml 的 \${POLARIS_TOKEN} 占位）
  POLARIS_SERVER             同 --polaris-server

多服务拓扑（每台机器执行 2 次，覆盖云端 2 个 limiter 节点）:
  # service-1（默认），eee2/eee3 各执行一次 → service-1 跨节点 2 实例
  ./provider.sh start
  # service-2，eee2/eee3 各执行一次 → service-2 跨节点 2 实例
  ./provider.sh start --service GlobalRatelimitEchoServer-2 --port 18202
  # pidfile/logfile 按服务名区分（provider-<service>.pid/.log），同节点可并存
EOF
            exit 0
            ;;
        *) echo -e "${RED}未知参数: $1${NC}"; exit 1 ;;
    esac
done

# pidfile/logfile 按服务名区分，让同节点可并存 service-1 / service-2 两个实例。
# 注意：须在参数解析后定义（--service 可能覆盖 SERVICE 默认值）。
PIDFILE="${SCRIPT_DIR}/provider-${SERVICE}.pid"
LOGFILE="${SCRIPT_DIR}/provider-${SERVICE}.log"

# ======================== helper ========================
# 探测本机出口 IP（与 main.go getLocalHost 一致：dial polaris-server 取 local addr）
detect_local_ip() {
    python3 - "$POLARIS_SERVER" <<'PY' 2>/dev/null
import socket, sys
addr = sys.argv[1]
try:
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.settimeout(3)
    s.connect((addr, 8091))
    print(s.getsockname()[0])
    s.close()
except Exception:
    pass
PY
}

# 查询 polaris 某服务的 healthy 实例列表，stdout 输出 "host port" 每行一条；失败回空
query_healthy_instances() {
    local service="$1" namespace="$2"
    local resp http_code
    http_code=$(curl -s -o /tmp/_prov_$$.tmp -w '%{http_code}' \
        --connect-timeout 3 --max-time 5 \
        "${POLARIS_HTTP_ADDR}/naming/v1/instances?service=${service}&namespace=${namespace}&healthy=true&limit=50" \
        --header "X-Polaris-Token:${POLARIS_TOKEN}" 2>/dev/null)
    resp=$(cat /tmp/_prov_$$.tmp 2>/dev/null || echo "")
    rm -f /tmp/_prov_$$.tmp
    [[ "$http_code" != "200" ]] && return 1
    SVC="$service" NS="$namespace" python3 -c "
import sys, json
try:
    data = json.load(sys.stdin)
    for i in data.get('instances', []):
        if i.get('healthy') and not i.get('isolate'):
            print('%s %s' % (i.get('host',''), i.get('port','')))
except Exception:
    pass
" <<< "$resp"
}

is_running() {
    [[ -f "$PIDFILE" ]] || return 1
    local pid
    pid=$(cat "$PIDFILE" 2>/dev/null)
    [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null
}

# ======================== start ========================
do_start() {
    log_step "[provider] 启动 provider (${NAMESPACE}/${SERVICE}) on :${PORT}"

    if [[ ! -x "$BIN" ]]; then
        log_error "找不到可执行二进制: $BIN"
        log_error "请确认 x86-bin 已与 provider.sh 放在同一目录"
        exit 1
    fi
    if [[ ! -f "$CONF" ]]; then
        log_error "找不到配置文件: $CONF"
        exit 1
    fi
    if is_running; then
        log_warn "provider 已在运行 (PID=$(cat "$PIDFILE"))，跳过启动"
        do_status
        exit 0
    fi

    # 端口冲突检测
    if command -v nc >/dev/null 2>&1; then
        if nc -z 127.0.0.1 "$PORT" 2>/dev/null; then
            log_error "端口 $PORT 已被占用，请用 --port 指定其他端口"
            exit 1
        fi
    elif (echo > "/dev/tcp/127.0.0.1/${PORT}") 2>/dev/null; then
        log_error "端口 $PORT 已被占用，请用 --port 指定其他端口"
        exit 1
    fi

    local debug_args=()
    [[ "$DEBUG" == "true" ]] && debug_args+=(--debug)

    : > "$LOGFILE"
    log_info "启动 provider: bin=${BIN}, service=${NAMESPACE}/${SERVICE}, port=${PORT}"
    log_info "  SDK 配置: ${CONF} (limiterService=polaris.limiter)"
    log_info "  运行日志: ${LOGFILE}"

    # 守护方式启动：nohup + pidfile，脚本退出后 provider 继续运行
    # SDK 从 cwd 加载 ./polaris.yaml，日志写到 run_dir/polaris/log/
    pushd "$SCRIPT_DIR" >/dev/null
    POLARIS_TOKEN="$POLARIS_TOKEN" \
        nohup "$BIN" \
            --namespace "$NAMESPACE" \
            --service "$SERVICE" \
            --port "$PORT" \
            ${POLARIS_TOKEN:+--token "$POLARIS_TOKEN"} \
            ${debug_args[@]+"${debug_args[@]}"} \
        >"$LOGFILE" 2>&1 &
    local pid=$!
    popd >/dev/null
    echo "$pid" > "$PIDFILE"
    log_info "provider PID=${pid}"

    # ---- 就绪探测：等 /echo 可达（200 或 429 都算就绪，429 说明规则已生效）----
    log_info "等待 provider :${PORT} 就绪（最长 30s）..."
    local ready=false i
    for ((i = 0; i < 30; i++)); do
        if ! kill -0 "$pid" 2>/dev/null; then
            log_error "provider 进程已退出，日志末尾："
            tail -30 "$LOGFILE" 2>/dev/null
            rm -f "$PIDFILE"
            exit 1
        fi
        # /echo 命中限流时返回 429，curl -f 会失败，改用 -s 取 http_code
        local code
        code=$(curl -s -o /dev/null -w '%{http_code}' \
            --connect-timeout 1 --max-time 2 \
            "http://127.0.0.1:${PORT}/echo" 2>/dev/null)
        if [[ "$code" == "200" || "$code" == "429" ]]; then
            ready=true
            break
        fi
        # TCP 兜底：端口已开即算就绪
        if (echo > "/dev/tcp/127.0.0.1/${PORT}") 2>/dev/null; then
            ready=true
            log_info "TCP 端口已打开（/echo 尚未响应 HTTP=${code}）"
            break
        fi
        sleep 1
    done
    if [[ "$ready" != "true" ]]; then
        log_error "provider 在 30s 内未就绪，日志末尾："
        tail -30 "$LOGFILE" 2>/dev/null
        do_stop
        exit 1
    fi
    log_info "[OK] provider HTTP :${PORT} 就绪"

    # ---- 注册自检：等实例在 polaris 标记为 healthy（心跳上报需 5-15s）----
    log_info "等待实例注册到 polaris 并 healthy（最长 40s）..."
    local local_ip list registered=false
    local_ip=$(detect_local_ip)
    if [[ -n "$local_ip" ]]; then
        log_info "本机出口 IP（探测自 dial ${POLARIS_SERVER}:8091）: ${local_ip}"
    else
        log_warn "无法探测本机出口 IP（python3 缺失或网络不通），仅校验实例数量"
    fi
    for ((i = 0; i < 40; i++)); do
        list=$(query_healthy_instances "$SERVICE" "$NAMESPACE" 2>/dev/null || true)
        if [[ -n "$list" ]]; then
            # 无本机 IP 时只看实例数；有则确认本机实例在列表中
            if [[ -z "$local_ip" ]] || echo "$list" | grep -q "^${local_ip} ${PORT}\$"; then
                registered=true
                break
            fi
        fi
        sleep 1
    done
    if [[ "$registered" != "true" ]]; then
        log_error "实例在 40s 内未在 polaris 标记为 healthy"
        log_error "  可能原因: 1.鉴权未传 token 2.网络不通 3.provider 注册失败"
        log_error "  provider 日志末尾："
        tail -20 "$LOGFILE" 2>/dev/null
        do_status
        exit 1
    fi
    log_info "[OK] 实例已注册到 polaris: ${NAMESPACE}/${SERVICE} (host=${local_ip:-未知} port=${PORT})"

    log_step "[provider] 部署完成"
    log_info "provider 运行中: PID=$(cat "$PIDFILE"), :${PORT}, ${NAMESPACE}/${SERVICE}"
    log_info "停止: $0 stop --service ${SERVICE} --port ${PORT}    状态: $0 status --service ${SERVICE}    日志: tail -f $LOGFILE"
}

# ======================== stop ========================
do_stop() {
    if ! is_running; then
        log_info "provider 未运行"
        rm -f "$PIDFILE"
        return 0
    fi
    local pid
    pid=$(cat "$PIDFILE")
    log_info "停止 provider PID=${pid}（SIGTERM → 二进制自行 deregister）..."
    kill "$pid" 2>/dev/null || true
    # 等进程退出（最长 15s），超时强杀
    local i
    for ((i = 0; i < 15; i++)); do
        kill -0 "$pid" 2>/dev/null || break
        sleep 1
    done
    if kill -0 "$pid" 2>/dev/null; then
        log_warn "进程未在 15s 内退出，SIGKILL 强杀"
        kill -9 "$pid" 2>/dev/null || true
    fi
    wait "$pid" 2>/dev/null || true
    rm -f "$PIDFILE"
    log_info "provider 已停止"
}

# ======================== status ========================
do_status() {
    if ! is_running; then
        echo -e "${YELLOW}[STATUS]${NC} provider 未运行"
        rm -f "$PIDFILE" 2>/dev/null
        return 1
    fi
    local pid
    pid=$(cat "$PIDFILE")
    echo -e "${GREEN}[STATUS]${NC} provider 运行中: PID=${pid}, :${PORT}, ${NAMESPACE}/${SERVICE}"
    local list amount
    list=$(query_healthy_instances "$SERVICE" "$NAMESPACE" 2>/dev/null || true)
    amount=$(echo "$list" | grep -c .)
    if [[ "$amount" -gt 0 ]]; then
        log_info "polaris healthy 实例数: ${amount}"
        echo "$list" | sed 's/^/    /'
    else
        log_warn "polaris 中无 healthy 实例（可能尚未上报心跳或鉴权失败）"
    fi
    return 0
}

# ======================== 主流程 ========================
case "$ACTION" in
    start)   do_start ;;
    stop)    do_stop ;;
    status)  do_status ;;
    restart) do_stop; do_start ;;
    *)       log_error "未知子命令: $ACTION"; exit 1 ;;
esac
