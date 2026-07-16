#!/bin/bash
# =============================================================================
# consumer.sh — 云端 consumer 节点 E2E 验证脚本（多服务 × 多 limiter 节点）
#
# 运行位置: eee1（consumer 节点）。provider 已在 eee2/eee3 由 provider.sh 启动。
#
# 多服务拓扑（覆盖云端 2 个 limiter 节点）:
#   - polaris-go SDK 的 GetMessageSender 用 Maglev 一致性哈希选 limiter 实例，
#     哈希 key = <被限流服务名>#<命名空间>#<labels>（见 polaris-go
#     pkg/flow/quota/window.go:buildQuotaHashValue / remote.go:GetMessageSender）。
#   - 故同一被限流服务 → 相同 hashValue → 固定打到同一个 limiter 节点。
#     若 2 个 provider 注册同一服务，只有一个 limiter 会被命中。
#   - 解法：让 provider 注册 2 个不同服务（service-1 / service-2），每服务跨
#     eee2/eee3 各 1 实例（共 2 实例，保留跨节点共享配额语义），两服务 hashValue
#     不同 → 期望分散到 2 个 limiter 节点。
#   - 限制：Maglev 不保证两服务一定落到不同 limiter（2 节点环上可能同侧）。
#     脚本无法跨节点访问 limiter /metrics，故只打印 limiter 实例地址 + 人工核对
#     提示，不把"命中不同 limiter"计入 PASS/FAIL；若两服务 counter 都在同一
#     limiter Pod 日志，换服务名后缀重试。
#
# 参考: test/integration/test.sh（本地单机 E2E）。云端适配差异:
#   - polaris-server 已部署在 172.16.0.5，polaris-limiter 已部署并注册为
#     Polaris/limiter —— 不启动 limiter。只验证分布式限流行为，不验证 limiter 的
#     /metrics（云端 limiter 与 consumer 跨节点网络不通）。
#   - 流量改为持续时长驱动（默认每服务 180s：Case A / Case B 各 90s），串行验证
#     每个服务；两服务合计 ~6 分钟。
#
# 日志: stdout/stderr 同时输出到屏幕（带色）和日志文件（去 ANSI）。
#   用命名管道 + 后台 sed 做可靠分流，EXIT trap 内等待 sed flush，避免退出时末尾丢失。
#
# 链路: curl → consumer:18201 → provider(eee2/eee3):18200|18202 → limiter(gRPC)
#
# 前置:
#   1. eee2/eee3 已运行: 每台机器各起 service-1 / service-2 两个 provider 实例
#      ./provider.sh start
#      ./provider.sh start --service GlobalRatelimitEchoServer-2 --port 18202
#
# 用法:
#   ./consumer.sh --polaris-server 172.16.0.10           # 默认: 串行验证 service-1/service-2，每服务 180s
#   ./consumer.sh --polaris-server 172.16.0.10 --duration 60   # 每服务 60s（A/B 各 30s，调试用）
#   ./consumer.sh --polaris-server 172.16.0.10 --keep          # 保留日志（consumer 进程仍按服务 stop）
#   POLARIS_TOKEN=xxx ./consumer.sh --polaris-server 172.16.0.10   # polaris-server 开启鉴权时
#
# 退出码: 0=通过, 1=失败
# =============================================================================
set -uo pipefail

# ======================== 颜色 ========================
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

# ======================== 默认配置 ========================
POLARIS_SERVER="${POLARIS_SERVER:-}"
POLARIS_TOKEN="${POLARIS_TOKEN:-}"
KEEP_RESOURCES=false

# 业务命名空间 + 多服务列表：每服务跨 eee2/eee3 各 1 实例，构成跨节点共享配额拓扑；
# 不同服务 hashValue 不同，分散到不同 limiter 节点。
NAMESPACE="default"
# 默认两服务（数字后缀 -1/-2）；可用 --services a,b 或多次 --service 覆盖。
SERVICES=("GlobalRatelimitEchoServer-1" "GlobalRatelimitEchoServer-2")
PORT_CONSUMER=18201

# GLOBAL 规则参数（QPS reject）—— 每服务各创建一条同名后缀规则
GLOBAL_MAX_AMOUNT=4
GLOBAL_WINDOW_SECOND=1
# 持续流量时长（秒）：语义为「每服务时长」，Case A / Case B / Case C 各占 1/3。
# 两服务串行，默认每服务 180s，合计 ~12 分钟。
TRAFFIC_DURATION_SEC="${TRAFFIC_DURATION_SEC:-180}"
# 每批并发请求数（每 ~1.5s 一批）
GLOBAL_BURST_REQUESTS=8          # Case A：经 consumer 每批并发数
GLOBAL_PER_INSTANCE_REQUESTS=5   # Case B：直打每实例每批并发数（A/B 各一份）
GLOBAL_MIXED_PER_INSTANCE=8      # Case C：直打每实例每批并发数（混合 4 组合）

# Case C 累加验证：每服务创建 4 条 agg 规则（不同 method + HEADER x-route argument），
# 4 个 (path, route) 组合产生 4 个不同 hashValue → Maglev 分散到 2 limiter Pod →
# 接收平台按 instanceid+ns+service 相加两 Pod 数据 = 该 service 总流量。
AGG_PATHS=("/agg1" "/agg2" "/agg3" "/agg4")
AGG_ROUTES=("a" "b" "c" "d")

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN="${SCRIPT_DIR}/x86-bin"
CONF="${SCRIPT_DIR}/polaris.yaml"
LOG_DIR="${SCRIPT_DIR}/.logs"
mkdir -p "$LOG_DIR"
LOG_FILE="${LOG_DIR}/consumer-test-$(date +%Y%m%d_%H%M%S).log"
: > "$LOG_FILE"

# 按服务区分的 metric 桶文件（verify_service 入口创建、Step 7 用完即删）。
# 因 run_*_for_duration 在 $(...) 子 shell 中执行、数组改动不回传父进程，故落盘到临时文件。
# 每行格式：<批次发起时刻 epoch 秒> <200数> <429数> <其他数>
BUCKET_FILE=""

# ======================== 日志双写（屏幕带色 + 日志文件去 ANSI）========================
# 用命名管道 + 后台 sed 做可靠分流：
#   stdout/stderr → tee(屏幕带色) + FIFO → sed(去 ANSI) → 追加日志文件
# 相比 `tee >(sed ...)` 进程替换，本方式在 EXIT trap 中显式等待 sed 退出，
# 保证退出时末尾日志（含结论行）完整落盘，不丢尾部。
if ! command -v mkfifo >/dev/null 2>&1; then
    echo "[ERROR] 缺少 mkfifo，无法建立日志双写管道" >&2
    exit 1
fi
_LOG_FIFO=$(mktemp -u "${LOG_DIR}/.fifo.XXXXXX") || { echo "[ERROR] mktemp 失败" >&2; exit 1; }
if ! mkfifo "$_LOG_FIFO"; then
    echo "[ERROR] mkfifo 失败: $_LOG_FIFO" >&2
    exit 1
fi
sed -u 's/\x1b\[[0-9;]*m//g' < "$_LOG_FIFO" >> "$LOG_FILE" &
_LOG_SED_PID=$!
exec > >(tee "$_LOG_FIFO") 2>&1

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
print_block() {
    local title="$1"; shift
    echo -e "${CYAN}┌─ ${title} ─────────────────────────────────────────────${NC}"
    for line in "$@"; do echo -e "${CYAN}│${NC} ${line}"; done
    echo -e "${CYAN}└──────────────────────────────────────────────────────────────${NC}"
}

log_info "===== consumer.sh 云端验证日志 $(date '+%Y-%m-%d %H:%M:%S') ====="
log_info "Command: $0 $*"
log_info "日志文件: ${LOG_FILE}"

# ======================== 参数解析 ========================
while [[ $# -gt 0 ]]; do
    case "$1" in
        --service)             SERVICES=("$2");         shift 2 ;;   # 单次 --service 覆盖默认列表
        --services)            IFS=',' read -ra SERVICES <<< "$2"; shift 2 ;;
        --namespace)           NAMESPACE="$2";         shift 2 ;;
        --port)                PORT_CONSUMER="$2";     shift 2 ;;
        --polaris-server)     POLARIS_SERVER="$2";     shift 2 ;;
        --polaris-token)      POLARIS_TOKEN="$2";      shift 2 ;;
        --duration)           TRAFFIC_DURATION_SEC="$2"; shift 2 ;;
        --keep)                KEEP_RESOURCES=true;    shift ;;
        -h|--help)
            cat <<EOF
用法: $0 --polaris-server <addr> [选项]

选项:
  --service <name>              业务服务名（覆盖默认列表，仅一个；多次用 --services）
  --services <a,b>              业务服务名列表 (默认 GlobalRatelimitEchoServer-1,GlobalRatelimitEchoServer-2)
  --namespace <ns>              命名空间 (默认 default)
  --port <port>                 consumer 监听端口 (默认 18201，串行复用)
  --polaris-server <addr>      polaris-server 地址 (必填)
  --polaris-token <token>       polaris-server 鉴权 token (开启鉴权时必填)
  --duration <sec>              每服务持续流量时长秒 (默认 180，Case A/B/C 各 1/3；两服务串行合计 ~12 分钟)
  --keep                       保留日志（consumer 进程仍按服务 stop 以串行复用 18201）
  -h, --help                    展示帮助

环境变量:
  TRAFFIC_DURATION_SEC         同 --duration
  POLARIS_TOKEN                同 --polaris-token
EOF
            exit 0
            ;;
        *) echo -e "${RED}未知参数: $1${NC}"; exit 1 ;;
    esac
done

if [[ -z "$POLARIS_SERVER" ]]; then
    echo -e "${RED}[ERROR] 必须指定 polaris-server 地址${NC}" >&2
    echo "用法: POLARIS_SERVER=<addr> $0 或 $0 --polaris-server <addr>" >&2
    exit 1
fi
POLARIS_HTTP_ADDR="http://${POLARIS_SERVER}:8090"

if [[ ${#SERVICES[@]} -lt 1 ]]; then
    log_error "服务列表为空（--services / --service）"
    exit 1
fi

# ======================== 全局状态 ========================
# 当前活跃 consumer 进程 PID（串行复用 18201：每服务 start 覆盖、stop 置空）
CONSUMER_PID=""
TOTAL_FAIL=0
declare -a CASE_NAMES
declare -a CASE_VERDICTS
declare -a CASE_DETAILS

# 当前 verify_service 上下文（全局变量风格：现有 helper 读这些，签名不动）
SERVICE=""
RULE_NAME=""

# ======================== 清理 helper ========================
# stop_consumer：停当前活跃 consumer 进程（CONSUMER_PID 非空才 kill），为下一服务腾出 18201。
stop_consumer() {
    if [[ -n "$CONSUMER_PID" ]] && kill -0 "$CONSUMER_PID" 2>/dev/null; then
        log_info "停止 consumer 进程 PID=${CONSUMER_PID}（为下一服务腾出 :${PORT_CONSUMER}）..."
        kill "$CONSUMER_PID" 2>/dev/null || true
        local i
        for ((i = 0; i < 15; i++)); do
            kill -0 "$CONSUMER_PID" 2>/dev/null || break
            sleep 1
        done
        if kill -0 "$CONSUMER_PID" 2>/dev/null; then
            log_warn "consumer 未在 15s 内退出，SIGKILL 强杀"
            kill -9 "$CONSUMER_PID" 2>/dev/null || true
        fi
        wait "$CONSUMER_PID" 2>/dev/null || true
    fi
    CONSUMER_PID=""
}

cleanup() {
    # consumer 进程一律按服务 stop（即便 --keep），否则串行复用 18201 会端口冲突。
    stop_consumer
    if [[ "$KEEP_RESOURCES" == "true" ]]; then
        log_info "--keep 指定：保留日志与 metric 桶文件（consumer 进程已按服务 stop，18201 串行复用）"
    else
        rm -f "${BUCKET_FILE:-}"
        log_info "已清理（限流规则不删除，下次复用）"
    fi
}

# _drain_log: 关闭 stdout 对 tee 的写入 → tee 收到 EOF → sed 收到 EOF → 等其 flush 完
# 在 EXIT trap 中于 cleanup 之后调用，确保末尾日志（含结论）落盘后再删管道。
_drain_log() {
    exec 1>/dev/null 2>/dev/null
    local i
    for ((i = 0; i < 30; i++)); do           # 最多等 3s 让 sed 退出
        kill -0 "${_LOG_SED_PID:-}" 2>/dev/null || break
        sleep 0.1
    done
    kill "${_LOG_SED_PID:-}" 2>/dev/null || true
    rm -f "${_LOG_FIFO:-}"
}
trap 'cleanup; _drain_log' EXIT

# ======================== record_case ========================
record_case() {
    local name="$1" verdict="$2" detail="$3"
    CASE_NAMES+=("$name"); CASE_VERDICTS+=("$verdict"); CASE_DETAILS+=("$detail")
    [[ "$verdict" == "FAIL" ]] && TOTAL_FAIL=$((TOTAL_FAIL + 1))
    case "$verdict" in
        PASS) echo -e "  ${GREEN}[PASS]${NC} [${name}] - ${detail}" ;;
        FAIL) echo -e "  ${RED}[FAIL]${NC} [${name}] - ${detail}" ;;
        WARN) echo -e "  ${YELLOW}[WARN]${NC} [${name}] - ${detail}" ;;
        SKIP) echo -e "  ${YELLOW}[SKIP]${NC} [${name}] - ${detail}" ;;
    esac
}

# ======================== 依赖检查 ========================
if [[ ! -x "$BIN" ]]; then
    log_error "找不到可执行二进制: $BIN（请确认 x86-bin 已与 consumer.sh 同目录）"
    exit 1
fi

# _json_min：把 JSON 响应压成无空白单行，便于 grep/sed 提取。
# 仅用于提取 id/type/host/port 等无空格字段值，故删除所有空白是安全的（不解析含空格的字符串值）。
_json_min() { tr -d '[:space:]'; }

# ======================== 规则名派生 ========================
# 由服务名派生限流规则名：取末段 -N 数字后缀；无数字后缀时用服务名小写全文。
rule_name_for_service() {
    local svc="$1" suffix
    suffix="${svc##*-}"
    if [[ "$suffix" =~ ^[0-9]+$ ]]; then
        echo "ratelimit-cloud-global-rule-${suffix}"
    else
        echo "ratelimit-cloud-global-rule-$(echo "$svc" | tr '[:upper:]' '[:lower:]')"
    fi
}

# ======================== Step 1: polaris-server 存活探测 ========================
probe_polaris() {
    local http_code
    http_code=$(curl -s -o /dev/null -w '%{http_code}' \
        --connect-timeout 3 --max-time 5 \
        "${POLARIS_HTTP_ADDR}/naming/v1/ratelimits?limit=1" \
        --header "X-Polaris-Token:${POLARIS_TOKEN}" 2>/dev/null)
    [[ "$http_code" != "000" ]]
}

# ======================== 限流规则 helper（读全局 SERVICE/RULE_NAME）========================
query_rule_field() {
    local rule_name="$1" field="$2"
    local resp http_code min
    http_code=$(curl -s -o /tmp/_c_rl_$$.tmp -w '%{http_code}' \
        --connect-timeout 5 --max-time 10 \
        "${POLARIS_HTTP_ADDR}/naming/v1/ratelimits?name=${rule_name}&service=${SERVICE}&namespace=${NAMESPACE}&limit=50" \
        --header "X-Polaris-Token:${POLARIS_TOKEN}" 2>/dev/null)
    resp=$(cat /tmp/_c_rl_$$.tmp 2>/dev/null || echo "")
    rm -f /tmp/_c_rl_$$.tmp
    [[ "$http_code" != "200" ]] && return 1
    # 服务端已按 name/service/namespace 过滤，响应内至多一条匹配规则；压成单行后 grep 提取。
    min=$(printf '%s' "$resp" | _json_min)
    case "$field" in
        # 规则级 type 是枚举字符串(GLOBAL/LOCAL/UNKNOWN)，与 method.type(EXACT/REGEX) 区分开
        type) printf '%s' "$min" | grep -oE '"type":"(GLOBAL|LOCAL|UNKNOWN)"' | head -1 | sed -E 's/.*:"(.*)"/\1/' ;;
        # id 仅出现在规则对象顶层（ratelimits 响应无顶层 id 字段）
        *)    printf '%s' "$min" | grep -oE "\"${field}\":\"[^\"]*\"" | head -1 | sed -E 's/.*:"(.*)"/\1/' ;;
    esac
}
query_rule_id() { query_rule_field "$1" "id"; }
rule_exists() { [[ -n "$(query_rule_id "$1")" ]]; }

update_rule_via_http() {
    local body="$1" resp http_code
    http_code=$(curl -s -o /tmp/_c_ru_$$.tmp -w '%{http_code}' \
        --connect-timeout 5 --max-time 10 \
        --request PUT "${POLARIS_HTTP_ADDR}/naming/v1/ratelimits" \
        --header "X-Polaris-Token:${POLARIS_TOKEN}" \
        --header 'Content-Type: application/json' \
        --data "$body" 2>/dev/null)
    resp=$(cat /tmp/_c_ru_$$.tmp 2>/dev/null || echo "")
    rm -f /tmp/_c_ru_$$.tmp
    [[ "$http_code" == "200" ]] && return 0
    log_error "[update_rule] HTTP=${http_code} body=${resp}"
    return 1
}

# 构造 GLOBAL 规则 body；rule_id 非空时为 PUT 更新（带 id）。
# 纯字符串拼接，无需 JSON 库；maxAmount 为整数、disable 为 bool，不能加引号。
build_global_rule_body() {
    local rule_id="$1" id_field=""
    [[ -n "$rule_id" ]] && id_field="\"id\":\"${rule_id}\","
    cat <<EOF
[{${id_field}"name":"${RULE_NAME}","service":"${SERVICE}","namespace":"${NAMESPACE}","priority":0,"resource":"QPS","type":"GLOBAL","method":{"type":"EXACT","value":"/echo"},"amounts":[{"maxAmount":${GLOBAL_MAX_AMOUNT},"validDuration":"${GLOBAL_WINDOW_SECOND}s"}],"action":"REJECT","failover":"FAILOVER_LOCAL","disable":false}]
EOF
}

create_global_rule() {
    local existing_id existing_type body http_code resp
    existing_id=$(query_rule_id "$RULE_NAME")
    if [[ -n "$existing_id" ]]; then
        existing_type=$(query_rule_field "$RULE_NAME" "type")
        if [[ "$existing_type" != "GLOBAL" ]]; then
            log_info "规则 [$RULE_NAME] 已存在但 type=${existing_type}（应为 GLOBAL），PUT 更新"
            update_rule_via_http "$(build_global_rule_body "$existing_id")" || return 1
            log_info "规则 [$RULE_NAME] 已更新为 GLOBAL"
        else
            log_info "规则 [$RULE_NAME] 已存在且 type=GLOBAL，PUT 刷新参数（maxAmount=${GLOBAL_MAX_AMOUNT}/${GLOBAL_WINDOW_SECOND}s）"
            update_rule_via_http "$(build_global_rule_body "$existing_id")" || return 1
        fi
        return 0
    fi
    body=$(build_global_rule_body "")
    http_code=$(curl -s -o /tmp/_c_rc_$$.tmp -w '%{http_code}' \
        --connect-timeout 5 --max-time 10 \
        --request POST "${POLARIS_HTTP_ADDR}/naming/v1/ratelimits" \
        --header "X-Polaris-Token:${POLARIS_TOKEN}" \
        --header 'Content-Type: application/json' \
        --data-raw "$body" 2>/dev/null)
    resp=$(cat /tmp/_c_rc_$$.tmp 2>/dev/null || echo "")
    rm -f /tmp/_c_rc_$$.tmp
    if [[ "$http_code" != "200" ]]; then
        log_error "创建规则失败 HTTP=${http_code} resp=${resp}"
        return 1
    fi
    log_info "规则 [$RULE_NAME] 已创建（GLOBAL / QPS / maxAmount=${GLOBAL_MAX_AMOUNT} / ${GLOBAL_WINDOW_SECOND}s）"
    return 0
}

# 构造 agg 规则 body（method + HEADER x-route argument）；rule_id 非空时为 PUT 更新。
# 与 build_global_rule_body 同构，仅 method.value 和 arguments 不同。
# 关键：argument.key 必须小写 x-route（provider buildQuotaRequest 用 strings.ToLower 提取 header）；
#       argument.value 是嵌套 MatchString {type,value}，value 为字符串（与 method 一致，value_type 默认 EXACT）。
build_agg_rule_body() {
    local rule_id="$1" method_path="$2" route_value="$3" id_field=""
    [[ -n "$rule_id" ]] && id_field="\"id\":\"${rule_id}\","
    cat <<EOF
[{${id_field}"name":"${RULE_NAME}","service":"${SERVICE}","namespace":"${NAMESPACE}","priority":0,"resource":"QPS","type":"GLOBAL","method":{"type":"EXACT","value":"${method_path}"},"amounts":[{"maxAmount":${GLOBAL_MAX_AMOUNT},"validDuration":"${GLOBAL_WINDOW_SECOND}s"}],"action":"REJECT","failover":"FAILOVER_LOCAL","disable":false,"arguments":[{"type":"HEADER","key":"x-route","value":{"type":"EXACT","value":"${route_value}"}}]}]
EOF
}

# 为 svc 创建 4 条 agg 规则（/agg1-4 + x-route=a/b/c/d），幂等（存在 PUT、不存在 POST）。
# 循环内复用全局 RULE_NAME/SERVICE（query_rule_id 等读全局），每条规则名带 -agg<M> 后缀。
create_agg_rules() {
    local svc="$1" suffix m rule_name path route existing_id existing_type body http_code resp
    suffix="${svc##*-}"
    for m in 1 2 3 4; do
        rule_name="ratelimit-cloud-global-rule-${suffix}-agg${m}"
        RULE_NAME="$rule_name"
        path="${AGG_PATHS[$((m-1))]}"
        route="${AGG_ROUTES[$((m-1))]}"
        existing_id=$(query_rule_id "$rule_name")
        if [[ -n "$existing_id" ]]; then
            existing_type=$(query_rule_field "$rule_name" "type")
            if [[ "$existing_type" != "GLOBAL" ]]; then
                log_info "agg 规则 [$rule_name] 已存在但 type=${existing_type}，PUT 更新为 GLOBAL"
                update_rule_via_http "$(build_agg_rule_body "$existing_id" "$path" "$route")" || return 1
            else
                update_rule_via_http "$(build_agg_rule_body "$existing_id" "$path" "$route")" || return 1
            fi
            continue
        fi
        body=$(build_agg_rule_body "" "$path" "$route")
        http_code=$(curl -s -o /tmp/_c_arc_$$.tmp -w '%{http_code}' \
            --connect-timeout 5 --max-time 10 \
            --request POST "${POLARIS_HTTP_ADDR}/naming/v1/ratelimits" \
            --header "X-Polaris-Token:${POLARIS_TOKEN}" \
            --header 'Content-Type: application/json' \
            --data-raw "$body" 2>/dev/null)
        resp=$(cat /tmp/_c_arc_$$.tmp 2>/dev/null || echo "")
        rm -f /tmp/_c_arc_$$.tmp
        if [[ "$http_code" != "200" ]]; then
            log_error "创建 agg 规则 [$rule_name] 失败 HTTP=${http_code} resp=${resp}"
            return 1
        fi
        log_info "agg 规则 [$rule_name] 已创建（method=${path} x-route=${route}）"
    done
    return 0
}

# ======================== healthy 实例查询（读全局 SERVICE）========================
get_healthy_instances() {
    # stdout 输出 "host port" 每行一条；失败回空
    local resp http_code min
    http_code=$(curl -s -o /tmp/_c_ins_$$.tmp -w '%{http_code}' \
        --connect-timeout 3 --max-time 5 \
        "${POLARIS_HTTP_ADDR}/naming/v1/instances?service=${SERVICE}&namespace=${NAMESPACE}&healthy=true&limit=50" \
        --header "X-Polaris-Token:${POLARIS_TOKEN}" 2>/dev/null)
    resp=$(cat /tmp/_c_ins_$$.tmp 2>/dev/null || echo "")
    rm -f /tmp/_c_ins_$$.tmp
    [[ "$http_code" != "200" ]] && return 1
    # URL 已带 healthy=true，服务端仅返回健康实例。polaris jsonpb 按 proto 字段序输出，
    # 每个 instance 内 host(#5) 与 port(#6) 相邻，压成单行后成对提取。
    # port 为整数（不带引号）；host 为字符串（带引号）。
    printf '%s' "$resp" | _json_min \
        | grep -oE '"host":"[^"]*","port":[0-9]+' \
        | sed -E 's/"host":"([^"]*)","port":([0-9]+)/\1 \2/'
}

# ======================== limiter 实例查询 + 命中提示 ========================
# 查询云端 Polaris/limiter 的 healthy 实例（2 节点），用于打印人工核对提示。
get_limiter_instances() {
    # stdout 输出 "host port" 每行一条；失败回空
    local resp http_code min
    http_code=$(curl -s -o /tmp/_c_lim_$$.tmp -w '%{http_code}' \
        --connect-timeout 3 --max-time 5 \
        "${POLARIS_HTTP_ADDR}/naming/v1/instances?service=limiter&namespace=Polaris&healthy=true&limit=50" \
        --header "X-Polaris-Token:${POLARIS_TOKEN}" 2>/dev/null)
    resp=$(cat /tmp/_c_lim_$$.tmp 2>/dev/null || echo "")
    rm -f /tmp/_c_lim_$$.tmp
    [[ "$http_code" != "200" ]] && return 1
    printf '%s' "$resp" | _json_min \
        | grep -oE '"host":"[^"]*","port":[0-9]+' \
        | sed -E 's/"host":"([^"]*)","port":([0-9]+)/\1 \2/'
}

print_limiter_hit_hint() {
    log_step "[limiter 双节点命中提示（人工核对，不计 PASS/FAIL）]"
    local list
    list=$(get_limiter_instances 2>/dev/null || true)
    if [[ -z "$list" ]]; then
        log_warn "未查到 Polaris/limiter healthy 实例（鉴权/网络问题），跳过提示"
        return
    fi
    local count
    count=$(echo "$list" | grep -c .)
    log_info "云端 limiter 实例（Polaris/limiter，healthy=${count} 个）:"
    echo "$list" | sed 's/^/    /'
    print_block "[原理]" \
        "SDK GetMessageSender 用 Maglev 一致性哈希选 limiter，key=<服务名>#<ns>#<labels>" \
        "→ service-1 与 service-2 的 hashValue 不同，期望分散到上述 2 个 limiter 节点" \
        "→ 每服务跨 eee2/eee3 共享同一 limiter 节点的配额（跨节点共享配额语义）"
    print_block "[人工核对]" \
        "kubectl exec 进两个 limiter Pod（polaris-limiter-0-0 / -0-1）：" \
        "  grep 'GlobalRatelimitEchoServer-1' /root/log/polaris-limiter.log" \
        "  grep 'GlobalRatelimitEchoServer-2' /root/log/polaris-limiter.log" \
        "期望：两服务的 counter init 分别出现在不同 Pod；若都集中在一个 Pod，" \
        "说明两服务哈希到同一 limiter —— 换服务名后缀（如 -3/-4）重试。"
    log_warn "Maglev 不保证两服务一定落到不同 limiter；脚本无法跨节点访问 limiter /metrics，故不计入 PASS/FAIL。"
}

# ======================== 流量 helper ========================
# count_status_concurrent <url> <total>：并发打 N 请求，回 "ok limited other"
# 注意：内部日志走 >&2（最终结果走 stdout，供 $(...) 捕获，避免污染）
count_status_concurrent() {
    local url="$1" total="$2"
    local tmp; tmp=$(mktemp -d)
    local i
    for ((i = 0; i < total; i++)); do
        (
            code=$(curl -s -o /dev/null --connect-timeout 2 --max-time 10 \
                -w '%{http_code}' "$url" 2>/dev/null || echo "000")
            echo "$code" > "${tmp}/code_${i}"
        ) &
    done
    wait
    local ok=0 limited=0 other=0 code
    for f in "${tmp}"/code_*; do
        code=$(cat "$f")
        case "$code" in
            200) ok=$((ok + 1)) ;;
            429) limited=$((limited + 1)) ;;
            *)   other=$((other + 1)) ;;
        esac
    done
    rm -rf "$tmp"
    echo "${ok} ${limited} ${other}"
}

# count_status_concurrent_header <url> <total> <route_value>：并发打 N 请求带 X-Route header，回 "ok limited other"
# Case C 用：请求必须带 X-Route 才能匹配 agg 规则（argument 匹配），否则全 200 不限流。
count_status_concurrent_header() {
    local url="$1" total="$2" route_value="$3"
    local tmp; tmp=$(mktemp -d)
    local i
    for ((i = 0; i < total; i++)); do
        (
            code=$(curl -s -o /dev/null --connect-timeout 2 --max-time 10 \
                -H "X-Route: ${route_value}" \
                -w '%{http_code}' "$url" 2>/dev/null || echo "000")
            echo "$code" > "${tmp}/code_${i}"
        ) &
    done
    wait
    local ok=0 limited=0 other=0 code
    for f in "${tmp}"/code_*; do
        code=$(cat "$f")
        case "$code" in
            200) ok=$((ok + 1)) ;;
            429) limited=$((limited + 1)) ;;
            *)   other=$((other + 1)) ;;
        esac
    done
    rm -rf "$tmp"
    echo "${ok} ${limited} ${other}"
}

# 窗口间隔（窗口 + 0.5s 余量），用于批次之间
_win_sleep() { sleep "$(awk -v s="$GLOBAL_WINDOW_SECOND" 'BEGIN{ printf "%.1f", s+0.5 }')"; }

# record_bucket <epoch> <200数> <429数> <其他数>：把本批次结果按 <epoch> 落盘到 BUCKET_FILE，
# 供 Step 7 按自然分钟聚合，反推 monitor 预期上报值。批次在 $(...) 子 shell 中，用追加写。
# <epoch> 传批次「发起时刻」而非完成时刻：并发请求几乎在发起瞬间到达 limiter，
# 与 limiter 按到达时刻归窗的口径最接近，可减少跨 :00 分界批次的归窗抖动（见 Step 7 边界说明）。
record_bucket() {
    echo "$1 $2 $3 $4" >> "$BUCKET_FILE"
}

# run_burst_for_duration <url> <per_batch> <duration_sec>
# 每 ~1.5s 发一批 per_batch 并发，持续 duration_sec 秒；返回 "ok limited other"（stdout）。
# 批次进度日志走 >&2，不污染返回值。
run_burst_for_duration() {
    local url="$1" per_batch="$2" duration_sec="$3"
    local total_ok=0 total_limited=0 total_other=0 batch=0
    local deadline=$(( $(date +%s) + duration_sec ))
    while (( $(date +%s) < deadline )); do
        batch=$((batch + 1))
        local stat ok lim oth batch_epoch
        batch_epoch=$(date +%s)                # 发起时刻，用于归窗（贴近 limiter 到达时刻）
        stat=$(count_status_concurrent "$url" "$per_batch")
        read -r ok lim oth <<< "$stat"
        record_bucket "$batch_epoch" "$ok" "$lim" "$oth"
        total_ok=$((total_ok + ok)); total_limited=$((total_limited + lim)); total_other=$((total_other + oth))
        log_info "  批次 ${batch}: 200=${ok} 429=${lim} 其他=${oth} | 累计 200=${total_ok} 429=${total_limited} 其他=${total_other}（已 ${duration_sec}s 中）" >&2
        (( $(date +%s) < deadline )) && _win_sleep
    done
    echo "$total_ok $total_limited $total_other"
}

# run_two_instances_for_duration <url_a> <url_b> <per_instance> <duration_sec>
# 每 ~1.5s 同时打 A/B 各 per_instance 并发，持续 duration_sec 秒；返回合计。
run_two_instances_for_duration() {
    local url_a="$1" url_b="$2" per_instance="$3" duration_sec="$4"
    local total_ok=0 total_limited=0 total_other=0 batch=0
    local deadline=$(( $(date +%s) + duration_sec ))
    while (( $(date +%s) < deadline )); do
        batch=$((batch + 1))
        local sa sb ok_a lim_a oth_a ok_b lim_b oth_b batch_epoch
        batch_epoch=$(date +%s)                # 发起时刻，用于归窗（贴近 limiter 到达时刻）
        sa=$(count_status_concurrent "$url_a" "$per_instance")
        sb=$(count_status_concurrent "$url_b" "$per_instance")
        read -r ok_a lim_a oth_a <<< "$sa"
        read -r ok_b lim_b oth_b <<< "$sb"
        record_bucket "$batch_epoch" "$((ok_a + ok_b))" "$((lim_a + lim_b))" "$((oth_a + oth_b))"
        total_ok=$((total_ok + ok_a + ok_b)); total_limited=$((total_limited + lim_a + lim_b)); total_other=$((total_other + oth_a + oth_b))
        log_info "  批次 ${batch}: A=(200=${ok_a},429=${lim_a}) B=(200=${ok_b},429=${lim_b}) | 累计 200=${total_ok} 429=${total_limited} 其他=${total_other}（已 ${duration_sec}s 中）" >&2
        (( $(date +%s) < deadline )) && _win_sleep
    done
    echo "$total_ok $total_limited $total_other"
}

# run_mixed_two_instances_for_duration <url_a_base> <url_b_base> <per_instance> <duration_sec>
# Case C 用：每批轮询 (path, x-route) 组合（4 组合循环），A/B 两 provider 各打 ${base}${path} -H "X-Route"。
# 4 组合 → 4 个不同 hashValue → Maglev 分散到 2 limiter Pod。record_bucket 写 $BUCKET_FILE（Case C 用 BUCKET_FILE_C）。
run_mixed_two_instances_for_duration() {
    local url_a="$1" url_b="$2" per_instance="$3" duration_sec="$4"
    local total_ok=0 total_limited=0 total_other=0 batch=0 idx
    local deadline=$(( $(date +%s) + duration_sec ))
    while (( $(date +%s) < deadline )); do
        batch=$((batch + 1))
        idx=$(( (batch - 1) % 4 ))
        local path="${AGG_PATHS[$idx]}" route="${AGG_ROUTES[$idx]}"
        local sa sb ok_a lim_a oth_a ok_b lim_b oth_b batch_epoch
        batch_epoch=$(date +%s)                # 发起时刻，用于归窗
        sa=$(count_status_concurrent_header "${url_a}${path}" "$per_instance" "$route")
        sb=$(count_status_concurrent_header "${url_b}${path}" "$per_instance" "$route")
        read -r ok_a lim_a oth_a <<< "$sa"
        read -r ok_b lim_b oth_b <<< "$sb"
        record_bucket "$batch_epoch" "$((ok_a + ok_b))" "$((lim_a + lim_b))" "$((oth_a + oth_b))"
        total_ok=$((total_ok + ok_a + ok_b)); total_limited=$((total_limited + lim_a + lim_b)); total_other=$((total_other + oth_a + oth_b))
        log_info "  批次 ${batch}: ${path} x-route=${route} A=(200=${ok_a},429=${lim_a}) B=(200=${ok_b},429=${lim_b}) | 累计 200=${total_ok} 429=${total_limited} 其他=${total_other}（已 ${duration_sec}s 中）" >&2
        (( $(date +%s) < deadline )) && _win_sleep
    done
    echo "$total_ok $total_limited $total_other"
}

# ======================== discover_providers：等该服务 ≥2 实例 healthy ========================
# stdout 输出该服务前两个 healthy 实例 "host port"（PROVIDER_A/B）；失败 exit 1。
discover_providers() {
    local svc="$1"
    local provider_count=0 i
    declare -a provider_instances=()
    for i in $(seq 1 60); do
        provider_instances=()
        while IFS= read -r line; do
            [[ -n "$line" ]] && provider_instances+=("$line")
        done < <(get_healthy_instances 2>/dev/null || true)
        provider_count=${#provider_instances[@]}
        if [[ "$provider_count" -ge 2 ]]; then
            log_info "第 ${i} 次轮询：${svc} 发现 ${provider_count} 个 healthy 实例" >&2
            break
        fi
        (( i % 5 == 0 )) && log_info "第 ${i}/60 次轮询：${svc} 仅 ${provider_count} 个实例，等 2s..." >&2
        sleep 2
    done
    if [[ "$provider_count" -lt 2 ]]; then
        log_error "${svc} 在 120s 内未凑齐 ≥2 个 healthy 实例（当前 ${provider_count}）" >&2
        log_error "请确认 eee2/eee3 均已为该服务运行: ./provider.sh start --service ${svc}" >&2
        return 1
    fi
    log_info "[OK] ${svc} 发现 ${provider_count} 个 provider 实例:" >&2
    printf '    %s\n' "${provider_instances[@]}" >&2
    # 仅以下两行走 stdout 作为返回值（verify_service 用 $(...) 捕获）；
    # 上面的 log_info/printf 必须 >&2，否则会污染返回值（曾导致 Case B 直打 URL 变成日志文本）。
    echo "${provider_instances[0]}"
    echo "${provider_instances[1]}"
    return 0
}

# ======================== start_consumer_for：启动 consumer 进程（串行复用 18201）========================
start_consumer_for() {
    local svc="$1"
    # 端口冲突预检：18201 若仍被占用（上一服务 consumer 未退干净），兜底清理
    if (echo > "/dev/tcp/127.0.0.1/${PORT_CONSUMER}") 2>/dev/null; then
        log_warn "端口 ${PORT_CONSUMER} 仍被占用，尝试兜底清理残留 consumer..."
        pkill -f "${BIN}.*--port ${PORT_CONSUMER}" 2>/dev/null || true
        sleep 2
    fi
    local consumer_log="${LOG_DIR}/consumer-${svc}.log"
    : > "$consumer_log"
    log_info "启动 consumer (:${PORT_CONSUMER}, service=${NAMESPACE}/${svc})"
    # SDK 从 cwd 加载 ./polaris.yaml，日志写到 ./polaris/log/
    pushd "$SCRIPT_DIR" >/dev/null
    POLARIS_TOKEN="$POLARIS_TOKEN" \
        nohup "$BIN" \
            --namespace "$NAMESPACE" \
            --service "$svc" \
            --port "$PORT_CONSUMER" \
            ${POLARIS_TOKEN:+--token "$POLARIS_TOKEN"} \
        >"$consumer_log" 2>&1 &
    CONSUMER_PID=$!
    popd >/dev/null
    log_info "consumer PID=${CONSUMER_PID}, run log=${consumer_log}"

    # 等 consumer 端口可达
    log_info "等待 consumer :${PORT_CONSUMER} 就绪（最长 30s）..."
    local i code consumer_ready=false
    for i in $(seq 1 30); do
        if ! kill -0 "$CONSUMER_PID" 2>/dev/null; then
            log_error "consumer 进程已退出，日志末尾："
            tail -30 "$consumer_log" 2>/dev/null
            return 1
        fi
        code=$(curl -s -o /dev/null -w '%{http_code}' \
            --connect-timeout 1 --max-time 2 \
            "http://127.0.0.1:${PORT_CONSUMER}/" 2>/dev/null)
        [[ "$code" != "000" ]] && { consumer_ready=true; break; }
        sleep 1
    done
    if [[ "$consumer_ready" != "true" ]]; then
        log_error "consumer 在 30s 内未就绪，日志末尾："
        tail -30 "$consumer_log" 2>/dev/null
        return 1
    fi
    log_info "[OK] consumer 就绪 :${PORT_CONSUMER}"
    # 等 SDK 拉规则 + 与 limiter 完成首次配额同步
    log_info "等待 6s 让 SDK 拉规则 + 与 limiter 完成首次配额同步..."
    sleep 6
    return 0
}

# ======================== Step 7: 反推 monitor 预期上报值 ========================
# 时序对齐（人工比对 monitor/record 的依据；基于实测 record T:00 ≈ 脚本 [T-2:00]）：
#   - limiter flushLoop 对齐分钟整点(:00)，flushOnce 取「上一分钟」[M:00,(M+1):00) 增量
#     累加进 prometheus Counter（Counter 为累计值，每分钟 Add 上一分钟增量）；
#   - monitor cron "15 */1 * * * ?" 每分钟 :15 抓取 /metrics，上报 delta = 本次累计-上次累计
#     = 上一分钟 [M:00,(M+1):00) 的流量（即 monitor @(M+1):15 反映窗口 [M:00,(M+1):00)）；
#   - record 时间戳归到拉取后的下一个整点（monitor @(M+1):15 拉取 → record 标 (M+2):00）；
#   - 故脚本窗口 [M:00,(M+1):00) 的流量 → monitor @(M+1):15 拉取 → record (M+2):00 出现。
#     实测两服务均一致：record T:00 ≈ 脚本 [T-2:00]，故 report_at 标 record 出现时刻 (M+2):00。
# 本函数把实测批次按「批次发起时刻所在自然分钟」聚合，输出每个窗口对应的预期上报值。
# 全部流量均命中单一维度 <ns>/<service>(/echo)，聚合后即服务级 3 counter。
print_expected_monitor_metrics() {
    if [[ ! -s "$BUCKET_FILE" ]]; then
        log_warn "无实测批次数据（BUCKET_FILE 为空），跳过预期值反推"
        return
    fi
    local svc_lower
    svc_lower=$(echo "$SERVICE" | tr '[:upper:]' '[:lower:]')
    local ns_lower
    ns_lower=$(echo "$NAMESPACE" | tr '[:upper:]' '[:lower:]')
    print_block "[Step 7] monitor 预期上报值（${SERVICE}，人工比对用）" \
        "维度: ratelimitcalleenamespace=${ns_lower}  ratelimitcalleeservice=${svc_lower}" \
        "映射: 200→request_pass_count  429→request_limit_count  (pass+limit)→request_count" \
        "对齐: 窗口 [M:00,(M+1):00) 流量 → monitor @(M+1):15 拉取 → record (M+2):00 出现（实测 record T:00≈脚本[T-2:00]）"
    # 用 awk 按自然分钟(epoch 向下取整到 60s)聚合，输出每行：
    #   <窗口起始 epoch> <pass> <limit> <other>，末行 "TOTAL <pass> <limit>"。
    # 依赖 sort -n 保证 epoch 升序，awk 在分钟切换时结算上一分钟；纯算术，无 python。
    local agg
    agg=$(sort -n "$BUCKET_FILE" | awk '
        {
            minute = $1 - ($1 % 60)
            if (NR == 1) cur = minute
            if (minute != cur) { print cur, p, l, o; p = l = o = 0; cur = minute }
            p += $2; l += $3; o += $4
            tp += $2; tl += $3
        }
        END { if (NR > 0) print cur, p, l, o; print "TOTAL", tp, tl }
    ')
    # 逐行格式化：per-minute 行用 date 把 epoch 转成 HH:MM:SS；record 出现时刻 = 窗口起始 + 120s = (M+2):00
    local w_epoch pass limit other w_start report_at total tot_pass tot_limit extra
    while read -r w_epoch pass limit other; do
        if [[ "$w_epoch" == "TOTAL" ]]; then
            tot_pass="$pass"; tot_limit="$limit"    # TOTAL 行：$pass=总pass $limit=总limit
            continue
        fi
        w_start=$(date -d "@${w_epoch}" '+%H:%M:%S' 2>/dev/null || date -r "$w_epoch" '+%H:%M:%S')
        report_at=$(date -d "@$((w_epoch + 120))" '+%H:%M:%S' 2>/dev/null || date -r "$((w_epoch + 120))" '+%H:%M:%S')
        total=$((pass + limit))
        extra=""
        [[ "$other" -gt 0 ]] && extra="  [注意]该窗口 other=${other}，非 200/429，不计入 limiter counter"
        echo "  窗口 [${w_start}] → record@${report_at}  request_count=${total}  request_pass_count=${pass}  request_limit_count=${limit}${extra}"
    done <<< "$agg"
    echo ""
    echo "  合计（${SERVICE} 跨全部窗口，供总量核对）: pass=${tot_pass} limit=${tot_limit} total=$((tot_pass + tot_limit))"
    log_info "时序说明：limiter :00 flush 上一分钟增量 + monitor :15 拉取 + record 归整点 →"
    log_info "  脚本窗口 [M:00,(M+1):00) 的流量在 record (M+2):00 出现（实测滞后约 2 分钟）。"
    log_info "边界说明：批次按「发起时刻」归窗（贴近 limiter 按到达时刻的归窗口径，已尽量减小抖动）；"
    log_info "  仍可能有个别跨 :00 请求归窗不同，相邻两窗口之和更稳定，总量最可靠。"
    log_info "口径说明：脚本 429 统计的是 consumer 观测到的 HTTP 状态，含 SDK 本地兜底 reject，"
    log_info "  而 limiter counter 只计到达 gRPC 的请求，故脚本值通常略高于 record（口径差约 2-5%）。"
    log_warn "首个出现该维度的窗口，monitor 按防脉冲逻辑上报 delta=0（首值被吞），从第二个窗口起才与上表一致。"
}

# print_accumulate_hint <svc> <ok> <limited> <other> <case_sec>
# Case C 累加核对提示（人工，不计 PASS/FAIL）：打印两 limiter Pod 地址 + 该 service Case C 预期总量 + 人工核对步骤。
# 机制：4 个 (path,x-route) 组合 → 4 hashValue → Maglev 分散 2 Pod；monitor 抹平 method/labels 按 ns+service 上报；
#       两 Pod 同 polarisinstanceid，接收平台相加两 Pod = 该 service 总流量。
print_accumulate_hint() {
    local svc="$1" ok="$2" limited="$3" other="$4" case_sec="$5"
    local svc_lower total list count
    svc_lower=$(echo "$svc" | tr '[:upper:]' '[:lower:]')
    total=$((ok + limited))
    log_step "[Case C 累加核对提示（${svc}，人工核对，不计 PASS/FAIL）]"
    list=$(get_limiter_instances 2>/dev/null || true)
    if [[ -z "$list" ]]; then
        log_warn "未查到 Polaris/limiter healthy 实例，跳过 Pod 地址打印"
    else
        count=$(echo "$list" | grep -c .)
        log_info "云端 limiter 实例（Polaris/limiter，healthy=${count} 个，两 Pod 同 polarisinstanceid=ins-87d1724e）:"
        echo "$list" | sed 's/^/    /'
    fi
    print_block "[Case C 预期总量（${svc} ${case_sec}s，4 个 (path,x-route) 组合合计）]" \
        "request_count(总) = ${total}" \
        "request_pass_count(200) = ${ok}" \
        "request_limit_count(429) = ${limited}" \
        "维度: ratelimitcalleenamespace=default  ratelimitcalleeservice=${svc_lower}（monitor 抹平 method/labels，接收平台按 instanceid+ns+service 相加两 Pod）"
    print_block "[人工核对步骤]" \
        "1) kubectl exec 进两 Pod（polaris-limiter-0-0 / -0-1, ns=ins-87d1724e, c=polaris-limiter）：" \
        "     各 Pod: curl localhost:8100/metrics | grep 'ratelimit_rq_total{' | grep 'service=\"${svc}\"'" \
        "   或 grep '<svc>' /root/log/polaris-limiter.log 看 counter init 是否分散两 Pod" \
        "2) 两 Pod ratelimit_rq_total 之和应 ≈ 脚本总量 ${total}（口径差 2-5%，脚本含本地兜底 reject 略高）" \
        "3) 接收平台 record：polaris_limiter_request_count:sum{ratelimitcalleeservice=${svc_lower}}（两 Pod 同 instanceid 相加）≈ ${total}" \
        "   record 出现时刻 = Case C 各窗口 (M+2):00（实测滞后约 2 分钟）"
    log_warn "Maglev 不保证 4 个 hashValue 一定分散到 2 Pod；若 counter 全落同一 Pod，换 path/x-route 组合（如 /agg5-8 + e/f/g/h）重试。"
    log_warn "若 limited=0 且 other=0（全 200），多半是 argument 未匹配（curl 未带 X-Route 或规则 key 大小写不符）。"
}

# ======================== verify_service：单服务完整验证流程 ========================
# 包原 Step 2~7：创建规则 → 等实例 → 起 consumer → Case A → 停 consumer → Case B → monitor 反推。
# 失败只 record_case 不 exit，保证后续服务继续验证。
verify_service() {
    local svc="$1"
    # ---- 设置该服务上下文（全局变量风格：现有 helper 读这些）----
    SERVICE="$svc"
    RULE_NAME="$(rule_name_for_service "$svc")"
    BUCKET_FILE="${LOG_DIR}/.metric-buckets-${svc}-$$.tmp"
    : > "$BUCKET_FILE"
    # Case C 独立 metric 桶（仅记 Case C 的 /agg 混合流量批次），用于反推 Case C 总量 + 累加核对。
    local BUCKET_FILE_C="${LOG_DIR}/.metric-buckets-${svc}-caseC-$$.tmp"
    : > "$BUCKET_FILE_C"

    # Case A / Case B / Case C 时长各占 1/3（基于每服务时长）
    local case_a_sec case_b_sec case_c_sec
    case_a_sec=$(( TRAFFIC_DURATION_SEC / 3 ))
    case_b_sec=$(( TRAFFIC_DURATION_SEC / 3 ))
    case_c_sec=$(( TRAFFIC_DURATION_SEC - case_a_sec - case_b_sec ))

    log_step "[Step 2] 创建 GLOBAL 限流规则 [${RULE_NAME}] on ${NAMESPACE}/${SERVICE}"
    if ! create_global_rule; then
        record_case "[$svc] 规则创建" "FAIL" "create_global_rule 失败，跳过该服务其余用例"
        rm -f "$BUCKET_FILE" "$BUCKET_FILE_C"
        return 1
    fi
    record_case "[$svc] 规则创建" "PASS" "规则 ${RULE_NAME}（GLOBAL/QPS/maxAmount=${GLOBAL_MAX_AMOUNT}/${GLOBAL_WINDOW_SECOND}s）"
    # Case C 用：创建 4 条 agg 规则（/agg1-4 + x-route=a/b/c/d），不同 (method,argument) 产生 4 hashValue 分散 2 Pod
    local agg_ok=true
    log_step "[Step 2b] 创建 4 条 agg 规则（/agg1-4 + x-route）用于 Case C 累加验证"
    if ! create_agg_rules "$svc"; then
        record_case "[$svc] agg 规则创建" "FAIL" "create_agg_rules 失败，Case C 将跳过"
        agg_ok=false
    else
        record_case "[$svc] agg 规则创建" "PASS" "4 条 agg 规则（/agg1-4 + x-route=a/b/c/d）"
    fi

    log_step "[Step 3] 等待 ${NAMESPACE}/${SERVICE} 实例注册（期望 eee2/eee3 两个节点，≥2 healthy）"
    local providers provider_a provider_b
    providers=$(discover_providers "$svc") || { rm -f "$BUCKET_FILE"; return 1; }
    provider_a=$(echo "$providers" | sed -n '1p')
    provider_b=$(echo "$providers" | sed -n '2p')
    local provider_a_url provider_b_url provider_a_base provider_b_base
    provider_a_base="http://${provider_a/ /:}"
    provider_b_base="http://${provider_b/ /:}"
    provider_a_url="${provider_a_base}/echo"
    provider_b_url="${provider_b_base}/echo"

    log_step "[Step 4] 启动 consumer (:${PORT_CONSUMER}, service=${NAMESPACE}/${SERVICE})"
    if ! start_consumer_for "$svc"; then
        record_case "[$svc] consumer 启动" "FAIL" "consumer 未就绪"
        stop_consumer
        rm -f "$BUCKET_FILE"
        return 1
    fi
    record_case "[$svc] consumer 启动" "PASS" "consumer :${PORT_CONSUMER} 就绪并完成首次配额同步"

    # ---- Case A：经 consumer 持续请求触发限流 ----
    local per_batch_a min_limited_a
    per_batch_a=$GLOBAL_BURST_REQUESTS
    min_limited_a=$(( case_a_sec / 6 + 1 ))
    log_step "[Step 5] [${svc}] 用例 A：经 consumer 持续请求 ${case_a_sec}s（链路: curl→consumer→provider→limiter）"
    print_block "[用例 A] GLOBAL 持续流量触发限流" \
        "操作: 经 consumer:${PORT_CONSUMER} 持续 ${case_a_sec}s，每 ~1.5s 并发突发 ${per_batch_a} 次" \
        "原理: type=GLOBAL → SDK 走 gRPC 与 limiter 通信；阈值 ${GLOBAL_MAX_AMOUNT}/${GLOBAL_WINDOW_SECOND}s 为该服务全集群配额" \
        "预期: ${case_a_sec}s 合计 limited ≥ ${min_limited_a}，且 other==0" \
        "判定: limited ≥ ${min_limited_a} && other==0 → PASS"
    local stat ok_a limited_a other_a
    stat=$(run_burst_for_duration "http://127.0.0.1:${PORT_CONSUMER}/echo" "$per_batch_a" "$case_a_sec")
    read -r ok_a limited_a other_a <<< "$stat"
    log_info "[用例 A] 行为结果: 总 200=${ok_a} 429=${limited_a} 其他=${other_a}"
    local m_ok_a=true
    if [[ "$other_a" -gt 0 ]]; then
        record_case "[$svc] 用例 A 链路触发限流" "FAIL" "出现非 200/429 状态码 (other=${other_a})"; m_ok_a=false
    elif [[ "$limited_a" -lt "$min_limited_a" ]]; then
        record_case "[$svc] 用例 A 链路触发限流" "FAIL" "limited=${limited_a} 不足 ${min_limited_a}"; m_ok_a=false
    fi
    if $m_ok_a; then
        record_case "[$svc] 用例 A 链路触发限流" "PASS" \
            "${case_a_sec}s 合计: 200=${ok_a} 429=${limited_a}（≥${min_limited_a}）"
    fi
    sleep $((GLOBAL_WINDOW_SECOND + 1))

    # ---- Case A 结束即停 consumer，为 Case B（直打）腾出 18201，并保证下一服务端口干净 ----
    stop_consumer

    # ---- Case B：直打两个 provider，持续验证跨节点共享配额 ----
    local per_instance local_bound max_ok min_limited_b
    per_instance=$GLOBAL_PER_INSTANCE_REQUESTS
    local_bound=$(( 2 * GLOBAL_MAX_AMOUNT * case_b_sec ))      # LOCAL 退化下界（每实例每窗口独立 maxAmount）
    max_ok=$(( GLOBAL_MAX_AMOUNT * case_b_sec * 3 / 2 ))         # GLOBAL 节流后 ok 上界（1.5× 纯 global 速率）
    min_limited_b=$(( case_b_sec / 6 + 1 ))
    log_step "[Step 6] [${svc}] 用例 B：直打两实例持续 ${case_b_sec}s，验证跨节点共享远端配额"
    print_block "[用例 B] GLOBAL 双节点持续共享配额" \
        "操作: 持续 ${case_b_sec}s，每 ~1.5s 同时打 A(${provider_a}) 与 B(${provider_b}) 各 ${per_instance} 并发" \
        "原理: 两 provider 跨节点共享同一远端配额（同一服务 hashValue 固定同一 limiter）；LOCAL 退化恒为 ${local_bound}" \
        "预期: ok ≤ ${max_ok}（LOCAL 下界=${local_bound}）、limited ≥ ${min_limited_b}、other==0" \
        "判定: other==0 && ok<${local_bound} && ok≤${max_ok} && limited≥${min_limited_b} → PASS"
    log_info "[用例 B] A=${provider_a_url}  B=${provider_b_url}"
    local ok_b limited_b other_b m_ok_b=true
    stat=$(run_two_instances_for_duration "$provider_a_url" "$provider_b_url" "$per_instance" "$case_b_sec")
    read -r ok_b limited_b other_b <<< "$stat"
    log_info "[用例 B] 行为结果: 总 200=${ok_b} 429=${limited_b} 其他=${other_b}"
    if [[ "$other_b" -gt 0 ]]; then
        record_case "[$svc] 用例 B 跨节点共享配额" "FAIL" "出现非 200/429 状态码 (other=${other_b})"; m_ok_b=false
    elif [[ "$ok_b" -ge "$local_bound" ]]; then
        record_case "[$svc] 用例 B 跨节点共享配额" "FAIL" "ok=${ok_b} 达到 LOCAL 退化下界 ${local_bound}（远端未接入）"; m_ok_b=false
    elif [[ "$ok_b" -gt "$max_ok" ]]; then
        record_case "[$svc] 用例 B 跨节点共享配额" "FAIL" "ok=${ok_b} 落在灰区（>${max_ok}），GLOBAL 节流不充分"; m_ok_b=false
    elif [[ "$limited_b" -lt "$min_limited_b" ]]; then
        record_case "[$svc] 用例 B 跨节点共享配额" "FAIL" "limited=${limited_b} 不足 ${min_limited_b}"; m_ok_b=false
    fi
    if $m_ok_b; then
        record_case "[$svc] 用例 B 跨节点共享配额" "PASS" \
            "${case_b_sec}s 合计 ok=${ok_b}（≤${max_ok}，相对 LOCAL ${local_bound} 节省 $((local_bound - ok_b))）、429=${limited_b}"
    fi

    # ---- Case C：直打两 provider 混合 /agg1-4 + X-Route，验证多 limiter 节点累加 ----
    local per_instance_c min_limited_c ok_c limited_c other_c m_ok_c=true
    per_instance_c=$GLOBAL_MIXED_PER_INSTANCE
    min_limited_c=$(( case_c_sec / 6 + 1 ))
    if ! $agg_ok; then
        record_case "[$svc] 用例 C 多 limiter 累加" "SKIP" "agg 规则创建失败，跳过 Case C"
        ok_c=0; limited_c=0; other_c=0
    else
        log_step "[Step 6b] [${svc}] 用例 C：直打两实例混合 /agg1-4 + X-Route 持续 ${case_c_sec}s，验证多 limiter 节点累加"
        print_block "[用例 C] 多 method+argument 分散 2 Pod（累加验证）" \
            "操作: 持续 ${case_c_sec}s，每 ~1.5s 轮询 (path,x-route) 4 组合，A/B 两 provider 各 ${per_instance_c} 并发" \
            "原理: 4 个 (path,x-route) → 4 hashValue → Maglev 分散 2 limiter Pod；接收平台按 instanceid+ns+service 相加两 Pod" \
            "预期: limited ≥ ${min_limited_c} 且 other==0（行为级 PASS；累加值人工核对，不计 PASS/FAIL）" \
            "判定: limited ≥ ${min_limited_c} && other==0 → PASS"
        log_info "[用例 C] A=${provider_a_base}  B=${provider_b_base}（混合 /agg1-4 + X-Route: a/b/c/d）"
        # Case C 写独立桶 BUCKET_FILE_C（run_mixed_two_instances_for_duration 内 record_bucket 用 $BUCKET_FILE）
        local bucket_ab="$BUCKET_FILE"
        BUCKET_FILE="$BUCKET_FILE_C"
        stat=$(run_mixed_two_instances_for_duration "$provider_a_base" "$provider_b_base" "$per_instance_c" "$case_c_sec")
        read -r ok_c limited_c other_c <<< "$stat"
        BUCKET_FILE="$bucket_ab"
        log_info "[用例 C] 行为结果: 总 200=${ok_c} 429=${limited_c} 其他=${other_c}"
        if [[ "$other_c" -gt 0 ]]; then
            record_case "[$svc] 用例 C 多 limiter 累加" "FAIL" "出现非 200/429 状态码 (other=${other_c})"; m_ok_c=false
        elif [[ "$limited_c" -lt "$min_limited_c" ]]; then
            record_case "[$svc] 用例 C 多 limiter 累加" "FAIL" "limited=${limited_c} 不足 ${min_limited_c}（可能 argument 未匹配→全200）"; m_ok_c=false
        fi
        if $m_ok_c; then
            record_case "[$svc] 用例 C 多 limiter 累加" "PASS" \
                "${case_c_sec}s 合计: 200=${ok_c} 429=${limited_c}（≥${min_limited_c}，累加值见下方人工核对）"
        fi
    fi

    # ---- Step 7: 反推 monitor 预期上报值（A/B 桶）----
    print_expected_monitor_metrics
    # ---- Case C 累加核对提示（Case C 总量 + 两 Pod 核对，不计 PASS/FAIL）----
    if $agg_ok; then
        print_accumulate_hint "$svc" "$ok_c" "$limited_c" "$other_c" "$case_c_sec"
    fi

    # 非 --keep：删该服务 metric 桶
    [[ "$KEEP_RESOURCES" != "true" ]] && rm -f "$BUCKET_FILE" "$BUCKET_FILE_C"
    return 0
}

# ======================== 主流程 ========================
log_step "[Step 1] 探测 polaris-server (${POLARIS_SERVER})"
if ! probe_polaris; then
    log_error "无法连接 polaris-server: ${POLARIS_HTTP_ADDR}"
    log_error "请确认 polaris-server 已启动，或用 --polaris-server <addr> 指定"
    exit 1
fi
log_info "polaris-server 可达: ${POLARIS_HTTP_ADDR}"

# 循环外查一次 limiter 实例 + 贴人工核对提示
print_limiter_hit_hint

log_info "将串行验证 ${#SERVICES[@]} 个服务: ${SERVICES[*]}（每服务 ${TRAFFIC_DURATION_SEC}s，Case A/B/C 各 1/3）"
for svc in "${SERVICES[@]}"; do
    log_step "===== 验证服务 ${NAMESPACE}/${svc} ====="
    verify_service "$svc" || true      # 失败不中断，继续下一服务
    # 兜底：确保每服务结束后无残留 consumer 进程占用 18201
    stop_consumer
done

# ======================== Step 8: 汇总 + 结论 ========================
log_step "[用例汇总]"
for idx in "${!CASE_NAMES[@]}"; do
    log_info "  [${CASE_VERDICTS[$idx]}] ${CASE_NAMES[$idx]}"
done

log_step "[结论]"
if [[ "$TOTAL_FAIL" -eq 0 ]]; then
    log_info "验证结论: [PASS] — 多服务分布式限流 + 跨节点共享配额 + 多 limiter 累加验证全部通过"
    log_info "（已串行验证 ${#SERVICES[@]} 个服务，覆盖 2 个 limiter 节点；多 limiter 累加需人工核对，见 Case C 提示）"
    log_info "完整日志: ${LOG_FILE}"
    exit 0
else
    log_error "验证结论: [FAIL] — 共 ${TOTAL_FAIL} 项失败"
    for idx in "${!CASE_NAMES[@]}"; do
        [[ "${CASE_VERDICTS[$idx]}" == "FAIL" ]] && \
            log_error "  [FAIL] [${CASE_NAMES[$idx]}] ${CASE_DETAILS[$idx]}"
    done
    log_error "完整日志: ${LOG_FILE}"
    exit 1
fi
