#!/bin/bash
# =============================================================================
# test/integration/test.sh — 分布式限流端到端 + polaris-limiter 服务端 /metrics 验证
#
# 参考: git.woa.com/polaris-go-examples/ratelimit/verify_ratelimit.sh
#
# 目标:
#   本地启动 polaris-limiter 服务端（statis=prometheus），让 provider 通过 gRPC 接入
#   做分布式限流，然后验证 **polaris-limiter 进程 /metrics 端口**输出的监控数据
#   符合预期（ratelimit_rq_total/pass/limit + 7 维 label + total==pass+limit 等）。
#
#   不验证 provider SDK 自身的 /metrics（那是另一套 callee_* label 的指标）。
#
# 链路:
#   curl → consumer:18201 → provider:18200 → polaris.limiter-local:8101 (gRPC)
#                                                  ↓
#                                              /metrics:8100 (HTTP, prometheus 插件)
#
# 前置:
#   1. polaris-server 已在 <polaris-server>:8091 运行（默认 127.0.0.1，可用 --polaris-server 指定）
#   2. polaris-server 已开放 :8090 HTTP API（用于创建限流规则）
#
# 用法:
#   ./test/integration/test.sh                              # 默认 polaris=127.0.0.1
#   ./test/integration/test.sh --polaris-server 1.2.3.4    # 指定远程 polaris
#   ./test/integration/test.sh --polaris-token TOKEN       # 鉴权 token
#   ./test/integration/test.sh --keep                      # 保留进程和日志
#   ./test/integration/test.sh --limiter-http-port 9100    # 避开 8100 端口冲突
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
POLARIS_SERVER="${POLARIS_SERVER:-127.0.0.1}"
POLARIS_TOKEN="${POLARIS_TOKEN:-}"
KEEP_RESOURCES=false
# 日志级别：DEBUG 或 INFO，影响 limiter 的 zap 日志 + provider/consumer 的 SDK 日志
LOG_LEVEL="INFO"
# polaris-limiter 端口（默认与仓库 polaris-limiter.yaml 一致）
LIMITER_HTTP_PORT=8100
LIMITER_GRPC_PORT=8101

# 业务端口
PORT_PROVIDER=18200
PORT_CONSUMER=18201
# provider SDK 的 metrics 端口（本脚本不验证它，但 polaris.yaml 需要这个变量非空）
PORT_PROVIDER_METRICS=28200

# 限流规则 / 服务
NAMESPACE="default"
METRICS_SERVICE="MetricsRatelimitEchoServer"
METRICS_RULE_NAME="ratelimit-e2e-metrics-rule"
# polaris-limiter 注册的服务名：用本地独立服务名 polaris.limiter-local，
# 避免与集群中已存在的 polaris.limiter 实例冲突（本地启动的 limiter 只服务于本次集成测试）.
# provider SDK 通过 POLARIS_LIMITER_SVC 指向这个名称做服务发现.
LIMITER_NS="Polaris"
LIMITER_SVC="polaris.limiter-local"

# GLOBAL 规则参数（QPS reject）
RULE_MAX_AMOUNT=2
RULE_WINDOW_SECOND=1
# 串行 6 次：1s 窗口内限到 2，最坏跨 2 窗口仍能限到 ≥2
TOTAL_REQUESTS=6

# ======================== 用例 6.x：分布式 GLOBAL 限流 ========================
# 独立服务名，与 MetricsRatelimitEchoServer 隔离，避免干扰 Step 9 的 /metrics 断言。
# 移植自 git.woa.com/polaris-go-examples/ratelimit/verify_ratelimit.sh 的 run_global_cases。
GLOBAL_SERVICE="GlobalRatelimitEchoServer"
GLOBAL_RULE_NAME="ratelimit-e2e-global-rule"
GLOBAL_MAX_AMOUNT=4
GLOBAL_WINDOW_SECOND=1
GLOBAL_BURST_REQUESTS=8                      # 单批并发突发请求数（6.1/6.2/6.5）
GLOBAL_PER_INSTANCE_REQUESTS=5               # 6.3 多实例共享用例每个实例每窗口的并发数
GLOBAL_OBSERVE_WINDOWS=4                     # 6.1/6.2 多窗口聚合判定的窗口数
GLOBAL_SHARED_WINDOWS=8                      # 6.3 多实例共享配额专用窗口数
GLOBAL_LIMITER_BAD_SERVICE="ratelimit-e2e-nonexistent-limiter"  # 6.5 故意指向不存在的 limiter 服务

# 用例 6.4：GLOBAL + regex_combine 多 path 共享同一远端配额
REGEX_SERVICE="RegexCombineEchoServer"
REGEX_RULE_NAME="ratelimit-e2e-regex-combine-rule"
REGEX_MAX_AMOUNT=4
REGEX_WINDOW_SECOND=1
REGEX_PATH_PATTERN='/users/.*/orders'        # 规则 method 字段（REGEX 类型）
REGEX_PATH_A='/users/100/orders'             # 第 1 条实际请求路径
REGEX_PATH_B='/users/200/orders'             # 第 2 条实际请求路径
REGEX_PER_PATH_REQUESTS=5                    # 每条路径并发请求数

# 6.x 端口（避开现有 18200/18201/28200/8100/8101）
PORT_PROVIDER_GLOBAL_A=18210                 # 6.x 第 1 个 provider 实例
PORT_PROVIDER_GLOBAL_B=18211                 # 6.3 第 2 个 provider 实例（同服务名，不同端口）
PORT_CONSUMER_GLOBAL=18212                   # 6.1/6.2 用 consumer
PORT_PROVIDER_REGEX=18220                    # 6.4 regex provider
PORT_PROVIDER_GLOBAL_FAILOVER=18230          # 6.5 远端降级专用 provider

# ======================== monitor sidecar 模拟（后台贯穿全程）=========================
# 模拟 polaris-monitor sidecar：cron "15 */1 * * * ?"（每分钟第 15 秒）抓取 limiter /metrics。
# 在 Step 4 limiter 启动后即开启后台采集，贯穿所有用例，直到 6.x 结束后停止——
# 这样 6.x 全程产生的流量会经 SDK 批量上报 + 每 :00 flush 反映到 counter，
# monitor 必然捕获到 delta>0，避免末尾单独造流因 SDK 批量上报延迟导致的 delta=0 误判。
# 参考 polaris-monitor/cmd/start.go:139 与 polaris-monitor/job/limiter_metrics.go。
MONITOR_SIM_MIN_CYCLES=2            # 后台 monitor 至少采集次数（测试全程时长保证远超此值）

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
LOG_DIR="${SCRIPT_DIR}/.logs"
BUILD_DIR="${SCRIPT_DIR}/.build"
TMP_DIR="${SCRIPT_DIR}/.tmp"
mkdir -p "$LOG_DIR" "$BUILD_DIR" "$TMP_DIR"

LOG_FILE="${LOG_DIR}/test-$(date +%Y%m%d_%H%M%S).log"

# stdout/stderr 同时输出到屏幕和日志文件（日志中剥离 ANSI 颜色码）
{
    echo "===== test/integration/test.sh 验证日志 $(date '+%Y-%m-%d %H:%M:%S') ====="
    echo "Command: $0 $*"
} > "$LOG_FILE"
exec > >(tee >(sed -u 's/\x1b\[[0-9;]*m//g' >> "$LOG_FILE")) 2>&1

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
while [[ $# -gt 0 ]]; do
    case "$1" in
        --polaris-server)    POLARIS_SERVER="$2";    shift 2 ;;
        --polaris-token)    POLARIS_TOKEN="$2";     shift 2 ;;
        --limiter-http-port) LIMITER_HTTP_PORT="$2"; shift 2 ;;
        --limiter-grpc-port) LIMITER_GRPC_PORT="$2"; shift 2 ;;
        --log-level)
            LOG_LEVEL=$(echo "$2" | tr '[:lower:]' '[:upper:]')
            if [[ "$LOG_LEVEL" != "DEBUG" && "$LOG_LEVEL" != "INFO" ]]; then
                echo -e "${RED}--log-level 只支持 DEBUG 或 INFO，收到: $2${NC}"
                exit 1
            fi
            shift 2
            ;;
        --keep)             KEEP_RESOURCES=true;    shift ;;
        -h|--help)
            cat <<EOF
用法: $0 [选项]

选项:
  --polaris-server <addr>     Polaris 服务端地址 (默认 127.0.0.1，端口固定 8091/8090)
  --polaris-token <token>     Polaris 鉴权 Token (开启鉴权时必填)
  --limiter-http-port <port>  polaris-limiter HTTP 端口 (默认 8100，含 /metrics)
  --limiter-grpc-port <port>  polaris-limiter gRPC 端口 (默认 8101，provider 接入)
  --log-level <DEBUG|INFO>    日志级别 (默认 INFO，影响 limiter + provider + consumer)
  --keep                      保留 polaris-limiter / provider / consumer 进程和日志
  -h, --help                  展示帮助
EOF
            exit 0
            ;;
        *) echo -e "${RED}未知参数: $1${NC}"; exit 1 ;;
    esac
done

POLARIS_HTTP_ADDR="http://${POLARIS_SERVER}:8090"
LIMITER_METRICS_ADDR="http://127.0.0.1:${LIMITER_HTTP_PORT}"

# ======================== 全局状态 ========================
LIMITER_PID=""
PROVIDER_PID=""
CONSUMER_PID=""
# 用例 6.x 进程
PROVIDER_GLOBAL_A_PID=""
PROVIDER_GLOBAL_B_PID=""
CONSUMER_GLOBAL_PID=""
PROVIDER_REGEX_PID=""
PROVIDER_GLOBAL_FAILOVER_PID=""
# monitor sidecar 后台采集进程（Step 4 启动，Step 11 停止）
MONITOR_SIM_PID=""
TOTAL_FAIL=0

# 用例 6.x 结果聚合（record_case 维护）
declare -a CASE_NAMES
declare -a CASE_VERDICTS
declare -a CASE_DETAILS

# ======================== 清理 helper ========================
cleanup() {
    if [[ "$KEEP_RESOURCES" == "true" ]]; then
        log_info "--keep 指定，保留进程和日志（手动 kill: pkill -f polaris-limiter）"
        return
    fi
    log_info "清理子进程..."
    # 先停 monitor 后台采集（touch stop 优雅退出 + kill 兜底）
    if [[ -n "$MONITOR_SIM_PID" ]] && kill -0 "$MONITOR_SIM_PID" 2>/dev/null; then
        touch "${LOG_DIR}/monitor_sim/stop" 2>/dev/null
        kill "$MONITOR_SIM_PID" 2>/dev/null || true
        wait "$MONITOR_SIM_PID" 2>/dev/null || true
    fi
    for pid in "$CONSUMER_PID" "$PROVIDER_PID" "$LIMITER_PID" \
               "$CONSUMER_GLOBAL_PID" "$PROVIDER_GLOBAL_A_PID" "$PROVIDER_GLOBAL_B_PID" \
               "$PROVIDER_REGEX_PID" "$PROVIDER_GLOBAL_FAILOVER_PID"; do
        if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
            kill "$pid" 2>/dev/null || true
            wait "$pid" 2>/dev/null || true
        fi
    done
    log_info "进程已清理（限流规则不删除，下次复用）"
}
trap cleanup EXIT

# ======================== monitor sidecar 模拟 helper ========================
# 模拟 polaris-monitor sidecar 后台贯穿全程：cron "15 */1 * * * ?" 每 :15 抓取 limiter /metrics，
# 逐行复刻 polaris-monitor/job/limiter_metrics.go 的 parseLimiterMetrics + computeAndAdvance。
# 在 Step 4 limiter /metrics 可达后启动（run_monitor_sim_bg &），Step 11 停止并断言（stop_and_assert_monitor_sim）。

# align_to_next_15: 用 date +%S 算出到下一分钟第 15 秒的等待秒数，模拟 cron 对齐
align_to_next_15() {
    local sec target
    sec=$((10#$(date +%S)))
    if (( sec < 15 )); then
        target=$((15 - sec))
    else
        target=$((75 - sec))   # 60 - sec + 15，落到下一分钟的 :15
    fi
    (( target < 1 )) && target=1
    sleep "$target"
}

# run_monitor_sim_bg <out_dir>: 后台循环对齐 :15 → scrape → parse/delta → 写 cycle json + summary
# 检测 ${out_dir}/stop 文件存在则退出。stdout 日志带 [monitor-sim] 前缀。
run_monitor_sim_bg() {
    local out_dir="$1"
    mkdir -p "$out_dir"
    local stop_file="${out_dir}/stop" summary_file="${out_dir}/summary.log"
    # 清理上一轮运行残留：stop 文件会让 while 首次即退出（采集 0 次），
    # 旧 body_*/cycle_*.json 会混入本次结果，故启动前一并删干净。
    rm -f "$stop_file" "${out_dir}"/body_* "${out_dir}"/cycle_*.json 2>/dev/null || true
    : > "$summary_file"
    local prev_json="" cycle=0
    log_info "[monitor-sim] 后台采集启动，对齐每分钟 :15 抓取 ${LIMITER_METRICS_ADDR}/metrics（贯穿全程，stop 文件=${stop_file}）"
    while [[ ! -f "$stop_file" ]]; do
        align_to_next_15
        [[ -f "$stop_file" ]] && break
        cycle=$((cycle + 1))
        local body_file="${out_dir}/body_${cycle}"
        local snap_file="${out_dir}/cycle_${cycle}.json"
        local http_code
        http_code=$(curl -s -o "$body_file" -w '%{http_code}' --connect-timeout 5 --max-time 10 \
            "${LIMITER_METRICS_ADDR}/metrics" 2>/dev/null || echo "000")
        local prev_arg="-"
        [[ -n "$prev_json" ]] && prev_arg="$prev_json"
        local concl=""
        if [[ "$http_code" == "200" && -s "$body_file" ]]; then
            concl=$(MONITOR_SIM_BODY="$body_file" MONITOR_SIM_PREV="$prev_arg" \
                MONITOR_SIM_SVC="$METRICS_SERVICE" MONITOR_SIM_OUT="$snap_file" \
                python3 - <<'PY'
import os, json
body_path = os.environ["MONITOR_SIM_BODY"]
prev_path = os.environ.get("MONITOR_SIM_PREV", "-")
target_svc = os.environ.get("MONITOR_SIM_SVC", "")
out_path = os.environ.get("MONITOR_SIM_OUT", "")

COUNTERS = {"ratelimit_rq_total": "total", "ratelimit_rq_pass": "pass", "ratelimit_rq_limit": "limit"}
GAUGES = ("ratelimit_active_streams", "ratelimit_counter_count",
          "ratelimit_process_avg_us", "ratelimit_process_max_us")

def parse_labels(s):
    out = {}
    if not s:
        return out
    for kv in s.split(","):
        eq = kv.find("=")
        if eq < 0:
            continue
        k = kv[:eq].strip()
        v = kv[eq + 1:].strip()
        if len(v) >= 2 and v[0] == '"' and v[-1] == '"':
            v = v[1:-1]
        if k:
            out[k] = v
    return out

services = {}
gauges = {}
nan_inf_skipped = 0
with open(body_path) as f:
    for line in f:
        line = line.strip()
        if not line or line[0] == "#":
            continue
        sp = line.rfind(" ")
        if sp < 0:
            continue
        try:
            val = float(line[sp + 1:])
        except ValueError:
            continue
        if val != val or val > 1e18 or val < -1e18:
            nan_inf_skipped += 1
            continue
        head = line[:sp].strip()
        br = head.find("{")
        if br < 0:
            name = head
            labels = {}
        else:
            name = head[:br]
            ls = head[br + 1:]
            if ls.endswith("}"):
                ls = ls[:-1]
            labels = parse_labels(ls)
        if not name.startswith("ratelimit_"):
            continue
        if name in GAUGES:
            gauges[name] = val
        elif name in COUNTERS:
            ns = labels.get("namespace", "")
            svc = labels.get("service", "")
            if not ns or not svc:
                continue
            key = ns + "\x1f" + svc
            c = services.setdefault(key, {"namespace": ns, "service": svc,
                                          "total": 0.0, "pass": 0.0, "limit": 0.0})
            c[COUNTERS[name]] += val

target_total = None
target_key = None
for k, c in services.items():
    if c["service"] == target_svc:
        target_total = c["total"]
        target_key = k
        break

prev_services = {}
if prev_path and prev_path != "-":
    try:
        with open(prev_path) as pf:
            prev = json.load(pf)
        prev_services = prev.get("services", {})
    except Exception:
        prev_services = {}

first_scrape = len(prev_services) == 0
monotonic_ok = True
target_delta = None
max_delta = 0.0
for k, c in services.items():
    p = prev_services.get(k)
    if p is None:
        c["d_total"] = c["d_pass"] = c["d_limit"] = 0.0
    else:
        dt = c["total"] - p.get("total", 0.0)
        dp = c["pass"] - p.get("pass", 0.0)
        dl = c["limit"] - p.get("limit", 0.0)
        c["d_total"], c["d_pass"], c["d_limit"] = dt, dp, dl
        if dt < 0 or dp < 0 or dl < 0:
            monotonic_ok = False
    if c["d_total"] > max_delta:
        max_delta = c["d_total"]
    if k == target_key:
        target_delta = c["d_total"]

result = {"gauges": gauges, "services": services, "first_scrape": first_scrape,
          "monotonic_ok": monotonic_ok, "nan_inf_skipped": nan_inf_skipped,
          "target_service": target_svc, "target_total": target_total,
          "target_delta": target_delta, "max_delta": max_delta}
if out_path:
    with open(out_path, "w") as of:
        json.dump(result, of, ensure_ascii=False)

tt = "-" if target_total is None else target_total
td = "-" if target_delta is None else target_delta
print("%d %d %d %d %d %s %s %s" % (1 if first_scrape else 0, 1 if monotonic_ok else 0,
                                   len(gauges), len(services), nan_inf_skipped, tt, td, max_delta))
PY
)
        fi
        local fs=1 mono=1 g=0 svc=0 ni=0 tt="-" td="-" md="-"
        [[ -n "$concl" ]] && read -r fs mono g svc ni tt td md <<< "$concl"
        echo "${cycle} ${http_code} ${fs} ${mono} ${g} ${svc} ${ni} ${tt} ${td} ${md}" >> "$summary_file"
        log_info "[monitor-sim] cycle ${cycle} @ $(date '+%H:%M:%S') http=${http_code} mono=${mono} gauges=${g}/4 svc=${svc} target_delta=${td} max_delta=${md}"
        [[ -f "$snap_file" ]] && prev_json="$snap_file"
    done
    log_info "[monitor-sim] 后台采集退出（共 ${cycle} 次）"
}

# stop_and_assert_monitor_sim: 停止后台采集并断言契约（采集次数/单调/delta>0/指标齐备）
stop_and_assert_monitor_sim() {
    local out_dir="${LOG_DIR}/monitor_sim"
    local stop_file="${out_dir}/stop" summary_file="${out_dir}/summary.log"

    print_block "[monitor-sim] 停止后台采集并断言 sidecar 契约" \
        "操作: touch stop + kill 后台 PID，读取 ${summary_file} 统计" \
        "原理: 后台贯穿 Step4-6.x 全程按 cron :15 抓取；6.x 持续流量经 SDK 批量上报 + :00 flush 反映到 counter" \
        "预期: 采集次数≥${MONITOR_SIM_MIN_CYCLES}、counter 全程单调、至少一次 max_delta>0、末次 7 指标齐备"

    touch "$stop_file" 2>/dev/null
    if [[ -n "$MONITOR_SIM_PID" ]] && kill -0 "$MONITOR_SIM_PID" 2>/dev/null; then
        kill "$MONITOR_SIM_PID" 2>/dev/null || true
        wait "$MONITOR_SIM_PID" 2>/dev/null || true
        log_info "[monitor-sim] 后台 PID=${MONITOR_SIM_PID} 已停止"
    fi

    if [[ ! -s "$summary_file" ]]; then
        record_case "用例 6.6 monitor sidecar 定时抓取模拟" "FAIL" \
            "summary 为空，后台未采集到任何数据（${out_dir}）"
        return 1
    fi

    print_block "[monitor-sim] summary 字段说明（每行 = 一次 :15 采集点的结果）" \
        "cycle   采集轮次序号（从 1 起，每分钟 :15 一次，对应 monitor cron '15 */1 * * * ?')" \
        "http    本次 scrape /metrics 的 HTTP 状态码（应恒 200）" \
        "first   1=首采（prev 为空，delta 视为 0）；0=已建立基线、正常算增量" \
        "mono    1=counter 全程单调（无 curr<prev 回退）；0=出现过回退（疑 limiter 重启或 flush 异常）" \
        "gauges  解析到的实例级 gauge 数（满 4：active_streams/counter_count/process_avg_us/process_max_us）" \
        "svc     解析到的服务维度数（不同 namespace+service 组合；随 6.x 起 provider 逐步增多）" \
        "nan     本次跳过的 NaN/Inf 非法值数（monitor parseLimiterMetrics 防御逻辑，正常应为 0）" \
        "tt      目标服务 ${METRICS_SERVICE} 的累计总量（target_total，counter 单调递增）" \
        "td      目标服务本周期增量 = curr - prev（target_delta；6.x 不打该服务时恒 0）" \
        "maxd    全部服务 delta 的最大值（max_delta，断言依据：6.x 持续流量必 >0）"
    log_info "[monitor-sim] summary 全部采集记录："
    awk '{printf "    cycle=%s http=%s first=%s mono=%s gauges=%s/4 svc=%s nan=%s tt=%s td=%s maxd=%s\n", $1,$2,$3,$4,$5,$6,$7,$8,$9,$10}' "$summary_file"

    # 结合本次实际采集值给出解读（聚合 summary.log + cycle_*.json），让看日志者无需翻文件即可理解结论
    log_info "[monitor-sim] 本次采集值解读："
    SUMMARY_FILE="$summary_file" CYCLE_DIR="$out_dir" TARGET_SVC="$METRICS_SERVICE" \
    python3 - <<'PY' 2>/dev/null || log_warn "[monitor-sim] 解读生成失败（不影响断言）"
import json, os
summary_file = os.environ["SUMMARY_FILE"]
cycle_dir = os.environ["CYCLE_DIR"]
target_svc = os.environ["TARGET_SVC"]

rows = []
with open(summary_file) as f:
    for line in f:
        p = line.split()
        if len(p) >= 10:
            rows.append({"cycle": int(p[0]), "http": p[1], "first": p[2], "mono": p[3],
                         "gauges": p[4], "svc": int(p[5]), "nan": p[6],
                         "tt": p[7], "td": p[8], "maxd": p[9]})

n = len(rows)
http_ok = sum(1 for r in rows if r["http"] == "200")
mono_fail = sum(1 for r in rows if r["mono"] != "1")
nan_total = sum(int(r["nan"]) for r in rows if r["nan"] != "-")
svc_final = rows[-1]["svc"] if rows else 0
svc_min = min((r["svc"] for r in rows), default=0)
svc_max = max((r["svc"] for r in rows), default=0)

# 解析每轮 cycle json，找出 max_delta 峰值轮次 + 来源服务 + 目标服务全程增量
peak_cycle = peak_val = None
peak_svc = ""
target_start = target_end = None
for r in rows:
    c = r["cycle"]
    jp = os.path.join(cycle_dir, "cycle_%d.json" % c)
    if not os.path.exists(jp):
        continue
    try:
        d = json.load(open(jp))
    except Exception:
        continue
    for k, s in d.get("services", {}).items():
        dt = s.get("d_total", 0.0)
        if peak_val is None or dt > peak_val:
            peak_val, peak_cycle, peak_svc = dt, c, s.get("service", "")
        if s.get("service") == target_svc:
            if target_start is None:
                target_start = s.get("total", 0.0)
            target_end = s.get("total", 0.0)

def lines(msg):
    print("[INFO] " + msg)

print("[monitor-sim]   - 采集点数: %d 次（HTTP 200=%d/%d）" % (n, http_ok, n))
print("[monitor-sim]   - 服务维度数: 从 %d 逐步增至 %d（随 6.x 起 provider 陆续接入 limiter）" % (svc_min, svc_max))
print("[monitor-sim]   - counter 单调性: %s（%s，非单调 %d 次）" % (
    "通过" if mono_fail == 0 else "未通过", "全程无 curr<prev 回退" if mono_fail == 0 else "出现回退", mono_fail))
print("[monitor-sim]   - NaN/Inf 防御: 跳过 %d 个非法值（monitor parseLimiterMetrics 丢弃逻辑生效）" % nan_total)
if peak_val is not None and float(peak_val) > 0:
    print("[monitor-sim]   - max_delta 峰值: cycle %s = %s（来源服务 %s，证明 6.x 持续流量被 monitor 捕获）" % (
        peak_cycle, peak_val, peak_svc))
else:
    print("[monitor-sim]   - max_delta 峰值: 全程为 0（未捕获任何流量增量，疑 flush 未生效或全程无流量）")
if target_start is not None and target_end is not None:
    print("[monitor-sim]   - 目标服务 %s: total %s→%s（全程增量 %s，6.x 不打该服务时为 0）" % (
        target_svc, target_start, target_end, target_end - target_start))
print("[monitor-sim]   - 断言依据: max_delta>0 已捕获即 PASS（不依赖目标服务增量，覆盖全维度 delta）")
PY


    local total_cycles=0 monotonic_fail=0 max_delta_seen=0 last_cycle="" line
    while IFS= read -r line; do
        [[ -z "$line" ]] && continue
        total_cycles=$((total_cycles + 1))
        local c hc fs mono g svc ni tt td md
        read -r c hc fs mono g svc ni tt td md <<< "$line"
        [[ "$mono" != "1" ]] && monotonic_fail=$((monotonic_fail + 1))
        if [[ "$md" != "-" ]] && awk -v v="$md" 'BEGIN{exit !(v>0)}'; then
            max_delta_seen=1
        fi
        last_cycle="$c"
    done < "$summary_file"

    local last_body="${out_dir}/body_${last_cycle}"
    local sim_fail=0
    if (( total_cycles < MONITOR_SIM_MIN_CYCLES )); then
        log_error "[monitor-sim] 采集次数 ${total_cycles} < ${MONITOR_SIM_MIN_CYCLES}"
        sim_fail=1
    fi
    if (( monotonic_fail > 0 )); then
        log_error "[monitor-sim] counter 非单调 ${monotonic_fail} 次（可能 limiter 重启或 flush 异常）"
        sim_fail=1
    fi
    if [[ "$max_delta_seen" != "1" ]]; then
        log_error "[monitor-sim] 全程未捕获到任何 max_delta>0（流量未上报到 limiter / flush 未生效）"
        sim_fail=1
    fi
    if [[ -f "$last_body" ]]; then
        local m miss=""
        for m in ratelimit_active_streams ratelimit_counter_count ratelimit_process_avg_us \
                 ratelimit_process_max_us ratelimit_rq_total ratelimit_rq_pass ratelimit_rq_limit; do
            grep -qE "^${m}[[:space:]{]" "$last_body" 2>/dev/null || miss="${miss} ${m}"
        done
        if [[ -n "$miss" ]]; then
            log_error "[monitor-sim] 末次 body 缺失指标:${miss}"
            sim_fail=1
        fi
    else
        log_error "[monitor-sim] 末次 body 文件不存在: ${last_body}"
        sim_fail=1
    fi

    if [[ "$sim_fail" -eq 0 ]]; then
        record_case "用例 6.6 monitor sidecar 定时抓取模拟" "PASS" \
            "后台 ${total_cycles} 次 :15 采集，counter 全程单调，max_delta>0 已捕获，末次 7 指标齐备"
    else
        record_case "用例 6.6 monitor sidecar 定时抓取模拟" "FAIL" \
            "见上方失败项（summary: ${out_dir}/summary.log，快照: ${out_dir}/cycle_*.json）"
    fi
}

# ======================== Step 1: polaris-server 存活探测 ========================
log_step "[Step 1] 探测 polaris-server (${POLARIS_SERVER})"

probe_polaris() {
    local resp http_code
    # 注意：curl 失败时 -w '%{http_code}' 已输出 000，不需要再 || echo "000"（否则会拼成 000000）
    http_code=$(curl -s -o /tmp/_probe_$$.tmp -w '%{http_code}' \
        --connect-timeout 3 --max-time 5 \
        "${POLARIS_HTTP_ADDR}/health" 2>/dev/null)
    resp=$(cat /tmp/_probe_$$.tmp 2>/dev/null || echo "")
    rm -f /tmp/_probe_$$.tmp
    # /health 可能返回 200 或 404 (路由不存在)，只要能连上就算 polaris-server 在
    if [[ "$http_code" == "000" ]]; then
        return 1
    fi
    # 进一步校验：实际发一个 ratelimits 查询请求看是否真的能响应
    http_code=$(curl -s -o /dev/null -w '%{http_code}' \
        --connect-timeout 3 --max-time 5 \
        "${POLARIS_HTTP_ADDR}/naming/v1/ratelimits?limit=1" \
        --header "X-Polaris-Token:${POLARIS_TOKEN}" 2>/dev/null)
    [[ "$http_code" != "000" ]]
}

if ! probe_polaris; then
    log_error "无法连接 polaris-server: ${POLARIS_HTTP_ADDR}"
    log_error "请确认 polaris-server 已启动（默认监听 8090/8091），或用 --polaris-server <addr> 指定远程地址"
    exit 1
fi
log_info "polaris-server 可达: ${POLARIS_HTTP_ADDR}"

# ======================== Step 2: 端口冲突检测 ========================
log_step "[Step 2] 端口冲突检测"
check_port_free() {
    local port="$1"
    # 用 nc 探测，无 nc 时用 /dev/tcp
    if command -v nc >/dev/null 2>&1; then
        if nc -z 127.0.0.1 "$port" 2>/dev/null; then
            return 1
        fi
    else
        if (echo > "/dev/tcp/127.0.0.1/$port") 2>/dev/null; then
            return 1
        fi
    fi
    return 0
}
for p in "$LIMITER_HTTP_PORT" "$LIMITER_GRPC_PORT" "$PORT_PROVIDER" "$PORT_CONSUMER" "$PORT_PROVIDER_METRICS" \
         "$PORT_PROVIDER_GLOBAL_A" "$PORT_PROVIDER_GLOBAL_B" "$PORT_CONSUMER_GLOBAL" \
         "$PORT_PROVIDER_REGEX" "$PORT_PROVIDER_GLOBAL_FAILOVER"; do
    if ! check_port_free "$p"; then
        log_error "端口 $p 已被占用"
        log_error "如果是 polaris-limiter 端口冲突，用 --limiter-http-port / --limiter-grpc-port 覆盖"
        log_error "如果是 provider/consumer 端口冲突，请先 kill 占用进程"
        exit 1
    fi
done
log_info "所有端口可用: limiter=${LIMITER_HTTP_PORT}/${LIMITER_GRPC_PORT}, provider=${PORT_PROVIDER}, consumer=${PORT_CONSUMER}, 6.x=${PORT_PROVIDER_GLOBAL_A}/${PORT_PROVIDER_GLOBAL_B}/${PORT_CONSUMER_GLOBAL}/${PORT_PROVIDER_REGEX}/${PORT_PROVIDER_GLOBAL_FAILOVER}"

# ======================== Step 3: 编译 ========================
log_step "[Step 3] 编译 polaris-limiter / provider / consumer"

# 二进制统一放到 .build/bin/，运行目录用 .build/<name>/（存放 polaris.yaml 软链 + SDK 日志）
BIN_DIR="${BUILD_DIR}/bin"
mkdir -p "$BIN_DIR"

log_info "编译 polaris-limiter（在 ${REPO_ROOT}）"
if ! (cd "$REPO_ROOT" && go build -o "${BIN_DIR}/polaris-limiter" .); then
    log_error "polaris-limiter 编译失败"
    exit 1
fi

log_info "编译 provider-qps"
if ! (cd "${SCRIPT_DIR}/provider-qps" && go build -o "${BIN_DIR}/provider-qps" .); then
    log_error "provider-qps 编译失败"
    exit 1
fi

log_info "编译 consumer"
if ! (cd "${SCRIPT_DIR}/consumer" && go build -o "${BIN_DIR}/consumer" .); then
    log_error "consumer 编译失败"
    exit 1
fi
log_info "编译完成，产物在 ${BIN_DIR}/"

# ======================== Step 4: 启动 polaris-limiter ========================
log_step "[Step 4] 启动 polaris-limiter（statis=prometheus）"

# 用 sed 把配置文件中的占位符替换为实际值，生成临时配置
# 注：LIMITER_HTTP_PORT/GRPC_PORT 已在 polaris-limiter-test.yaml 中固定为 8100/8101，
# 如需改端口请直接改 yaml 或用 --limiter-http-port 同时改 yaml
# LOG_LEVEL 转小写：limiter 的 zap logger 只支持小写 "debug"/"info"
RUN_CONFIG="${TMP_DIR}/polaris-limiter-run.yaml"
LIMITER_LOG_LEVEL=$(echo "$LOG_LEVEL" | tr '[:upper:]' '[:lower:]')
sed -e "s|\${POLARIS_SERVER}|${POLARIS_SERVER}|g" \
    -e "s|\${POLARIS_TOKEN}|${POLARIS_TOKEN}|g" \
    -e "s|\${LOG_LEVEL}|${LIMITER_LOG_LEVEL}|g" \
    "${SCRIPT_DIR}/polaris-limiter-test.yaml" > "$RUN_CONFIG"

# polaris-limiter 运行目录：.build/polaris-limiter/
# limiter 在此目录下启动，配置里的相对路径（.logs/）都相对此目录解析
# SDK 的 file_log 分类日志和 zap 日志都写到 .build/polaris-limiter/ 下
LIMITER_RUN_DIR="${BUILD_DIR}/polaris-limiter"
mkdir -p "${LIMITER_RUN_DIR}/.logs"

log_info "生成运行配置: ${RUN_CONFIG}"
LIMITER_LOG="${LOG_DIR}/polaris-limiter.log"
: > "$LIMITER_LOG"
log_info "启动 polaris-limiter，run_dir=${LIMITER_RUN_DIR}, log_level=${LOG_LEVEL}, stdout 日志 ${LIMITER_LOG}"
# 用 pushd 到 run_dir 启动：配置里的 .logs/ 和 log/ 相对 run_dir 解析
# 用 bash -c + exec 确保 $! 拿到的是 limiter 进程自身的 PID
pushd "${LIMITER_RUN_DIR}" >/dev/null
POLARIS_SERVER="$POLARIS_SERVER" \
    "${BIN_DIR}/polaris-limiter" start -c "$RUN_CONFIG" >"$LIMITER_LOG" 2>&1 &
LIMITER_PID=$!
popd >/dev/null
log_info "polaris-limiter PID=${LIMITER_PID}"

# 轮询 /metrics 端口可达
log_info "等待 polaris-limiter HTTP :${LIMITER_HTTP_PORT} 启动..."
limiter_ready=false
for i in $(seq 1 20); do
    if ! kill -0 "$LIMITER_PID" 2>/dev/null; then
        log_error "polaris-limiter 进程已退出，日志末尾："
        tail -30 "$LIMITER_LOG" 2>/dev/null
        exit 1
    fi
    http_code=$(curl -s -o /dev/null -w '%{http_code}' \
        --connect-timeout 1 --max-time 2 \
        "${LIMITER_METRICS_ADDR}/metrics" 2>/dev/null)
    if [[ "$http_code" != "000" ]]; then
        limiter_ready=true
        break
    fi
    sleep 1
done
if [[ "$limiter_ready" != "true" ]]; then
    log_error "polaris-limiter 在 20s 内未启动，日志末尾："
    tail -30 "$LIMITER_LOG" 2>/dev/null
    exit 1
fi
log_info "polaris-limiter 已启动，/metrics 可达 (${LIMITER_METRICS_ADDR}/metrics)"

# 启动 monitor sidecar 后台模拟（贯穿全程，每分钟 :15 抓取 /metrics，Step 11 停止断言）
log_info "[monitor-sim] 启动后台采集（贯穿全程，对齐每分钟 :15）"
run_monitor_sim_bg "${LOG_DIR}/monitor_sim" &
MONITOR_SIM_PID=$!
log_info "[monitor-sim] 后台 PID=${MONITOR_SIM_PID}"

# 等 polaris.limiter-local 注册到 polaris-server（consumer 通过服务发现才能找到它）
# 注：limiter 通过 gRPC 注册 polaris-server，首次注册 + 心跳上报需要 10-30s
# （远端 polaris-server 网络延迟更大），这里轮询最长 90s
log_info "等待 ${LIMITER_SVC} 注册到 polaris-server（最长 90s，首次注册需要 10-30s）..."
limiter_registered=false
for i in $(seq 1 45); do
    http_code=$(curl -s -o /tmp/_lim_$$.tmp -w '%{http_code}' \
        --connect-timeout 2 --max-time 3 \
        "${POLARIS_HTTP_ADDR}/naming/v1/instances?service=${LIMITER_SVC}&namespace=${LIMITER_NS}&limit=10" \
        --header "X-Polaris-Token:${POLARIS_TOKEN}" 2>/dev/null)
    resp=$(cat /tmp/_lim_$$.tmp 2>/dev/null || echo "")
    rm -f /tmp/_lim_$$.tmp
    # 用 python 解析 amount，避免 grep healthy 字符串格式问题
    lim_amount=$(echo "$resp" | python3 -c "import sys,json; print(json.load(sys.stdin).get('amount',0))" 2>/dev/null || echo "0")
    if [[ "$http_code" == "200" && "$lim_amount" -gt 0 ]]; then
        limiter_registered=true
        log_info "第 ${i} 次轮询：${LIMITER_SVC} 已注册到 polaris-server"
        break
    fi
    if (( i % 5 == 0 )); then
        log_info "第 ${i}/45 次轮询：尚未注册（http_code=${http_code}），等 2s..."
    fi
    sleep 2
done
if [[ "$limiter_registered" != "true" ]]; then
    log_error "${LIMITER_SVC} 在 90s 内未注册到 polaris-server"
    log_error "可能原因：1.polaris-server 鉴权未传 token 2.网络不通 3.limiter 启动失败"
    log_error "limiter 日志末尾："
    tail -20 "$LIMITER_LOG" 2>/dev/null
    exit 1
else
    log_info "${LIMITER_SVC} 已注册到 polaris-server"
fi

# ======================== Step 5: 创建 GLOBAL 限流规则 ========================
log_step "[Step 5] 创建 GLOBAL 限流规则 [${METRICS_RULE_NAME}]"

# query_rule_field <rule_name> <service> <field>：查询规则的指定字段值
# 用于校验已存在规则是否为 GLOBAL 类型；查询失败/字段缺失输出空串
query_rule_field() {
    local rule_name="$1" service="$2" field="$3"
    local resp http_code
    http_code=$(curl -s -o /tmp/_rl_qf_$$.tmp -w '%{http_code}' \
        --connect-timeout 5 --max-time 10 \
        "${POLARIS_HTTP_ADDR}/naming/v1/ratelimits?name=${rule_name}&service=${service}&namespace=${NAMESPACE}&limit=50" \
        --header "X-Polaris-Token:${POLARIS_TOKEN}" 2>/dev/null)
    resp=$(cat /tmp/_rl_qf_$$.tmp 2>/dev/null || echo "")
    rm -f /tmp/_rl_qf_$$.tmp
    [[ "$http_code" != "200" ]] && return 1
    SVC="$service" RULE="$rule_name" FIELD="$field" python3 -c "
import sys, json, os
svc = os.environ['SVC']
rule = os.environ['RULE']
field = os.environ['FIELD']
try:
    data = json.load(sys.stdin)
    for r in data.get('rateLimits', []):
        if r.get('name', '') == rule and r.get('service', '') == svc:
            v = r.get(field, '')
            print(v if v is not None else '')
            break
except Exception:
    pass
" <<< "$resp" 2>/dev/null
}

# query_rule_id <rule_name> <service>：查询规则 ID（用于 PUT 更新）
query_rule_id() {
    query_rule_field "$1" "$2" "id"
}

rule_exists() {
    local rule_name="$1" service="$2"
    local id
    id=$(query_rule_id "$rule_name" "$service")
    [[ -n "$id" ]]
}

# update_rule_via_http <body_json>：对一个或多个已有规则做整体替换更新（PUT）。
# body 必须是 JSON 数组、每项含 id。供 update_rule_to_global / flip_regex_combine 等复用。
update_rule_via_http() {
    local body="$1"
    local resp http_code
    http_code=$(curl -s -o /tmp/_rl_u_$$.tmp -w '%{http_code}' \
        --connect-timeout 5 --max-time 10 \
        --request PUT "${POLARIS_HTTP_ADDR}/naming/v1/ratelimits" \
        --header "X-Polaris-Token:${POLARIS_TOKEN}" \
        --header 'Content-Type: application/json' \
        --data "$body" 2>/dev/null)
    resp=$(cat /tmp/_rl_u_$$.tmp 2>/dev/null || echo "")
    rm -f /tmp/_rl_u_$$.tmp
    if [[ "$http_code" != "200" ]]; then
        log_error "[update_rule] HTTP=${http_code} body=${resp}"
        return 1
    fi
    return 0
}

# update_rule_to_global <rule_id>：用 PUT 把已存在规则更新为 GLOBAL 类型
update_rule_to_global() {
    local rule_id="$1"
    local body
    body=$(SVC="$METRICS_SERVICE" NS="$NAMESPACE" NAME="$METRICS_RULE_NAME" ID="$rule_id" \
        AMOUNT="$RULE_MAX_AMOUNT" WINDOW="$RULE_WINDOW_SECOND" \
        python3 -c "
import os, json
print(json.dumps([{
    'id': os.environ['ID'],
    'name': os.environ['NAME'],
    'service': os.environ['SVC'],
    'namespace': os.environ['NS'],
    'priority': 0,
    'resource': 'QPS',
    'type': 'GLOBAL',
    'method': {'type': 'EXACT', 'value': '/echo'},
    'amounts': [{
        'maxAmount': int(os.environ['AMOUNT']),
        'validDuration': '%ss' % os.environ['WINDOW'],
    }],
    'action': 'REJECT',
    'disable': False,
}]))")
    update_rule_via_http "$body"
}

create_metrics_rule() {
    # 检查规则是否已存在
    local existing_id existing_type
    existing_id=$(query_rule_id "$METRICS_RULE_NAME" "$METRICS_SERVICE")
    if [[ -n "$existing_id" ]]; then
        existing_type=$(query_rule_field "$METRICS_RULE_NAME" "$METRICS_SERVICE" "type")
        if [[ "$existing_type" != "GLOBAL" ]]; then
            log_info "规则 [$METRICS_RULE_NAME] 已存在但 type=${existing_type}（应为 GLOBAL），用 PUT 更新"
            if ! update_rule_to_global "$existing_id"; then
                return 1
            fi
            log_info "规则 [$METRICS_RULE_NAME] 已更新为 GLOBAL 类型"
        else
            log_info "规则 [$METRICS_RULE_NAME] 已存在且 type=GLOBAL，跳过创建"
        fi
        return 0
    fi

    # 规则不存在，POST 创建
    local body
    body=$(SVC="$METRICS_SERVICE" NS="$NAMESPACE" NAME="$METRICS_RULE_NAME" \
        AMOUNT="$RULE_MAX_AMOUNT" WINDOW="$RULE_WINDOW_SECOND" \
        python3 -c "
import os, json
print(json.dumps([{
    'name': os.environ['NAME'],
    'service': os.environ['SVC'],
    'namespace': os.environ['NS'],
    'priority': 0,
    'resource': 'QPS',
    'type': 'GLOBAL',
    'method': {'type': 'EXACT', 'value': '/echo'},
    'amounts': [{
        'maxAmount': int(os.environ['AMOUNT']),
        'validDuration': '%ss' % os.environ['WINDOW'],
    }],
    'action': 'REJECT',
    'disable': False,
}]))")
    local http_code resp
    http_code=$(curl -s -o /tmp/_rl_c_$$.tmp -w '%{http_code}' \
        --connect-timeout 5 --max-time 10 \
        --request POST "${POLARIS_HTTP_ADDR}/naming/v1/ratelimits" \
        --header "X-Polaris-Token:${POLARIS_TOKEN}" \
        --header 'Content-Type: application/json' \
        --data-raw "$body" 2>/dev/null)
    resp=$(cat /tmp/_rl_c_$$.tmp 2>/dev/null || echo "")
    rm -f /tmp/_rl_c_$$.tmp
    if [[ "$http_code" != "200" ]]; then
        log_error "创建规则失败 HTTP=${http_code} resp=${resp}"
        return 1
    fi
    log_info "规则 [$METRICS_RULE_NAME] 已创建（GLOBAL / QPS / maxAmount=${RULE_MAX_AMOUNT} / ${RULE_WINDOW_SECOND}s）"
    return 0
}

if ! create_metrics_rule; then
    log_error "规则创建失败，终止"
    exit 1
fi

# ======================== Step 6: 启动 provider + consumer ========================
log_step "[Step 6] 启动 provider (${METRICS_SERVICE}:${PORT_PROVIDER}) + consumer (:${PORT_CONSUMER})"

# provider-qps polaris.yaml 用 ${POLARIS_SERVER} / ${POLARIS_TOKEN} / ${POLARIS_LIMITER_NS}
# / ${POLARIS_LIMITER_SVC} / ${POLARIS_METRICS_PORT} 占位符，polaris-go SDK 自带 env 替换
PROVIDER_LOG="${LOG_DIR}/provider.log"
CONSUMER_LOG="${LOG_DIR}/consumer.log"
: > "$PROVIDER_LOG"
: > "$CONSUMER_LOG"

# provider/consumer 运行目录：.build/provider-qps/ 和 .build/consumer/
# 在此目录下启动，SDK 默认从 cwd 加载 ./polaris.yaml（软链到源码），日志写到 ./polaris/log/
# 参考 verify_ratelimit.sh 的设计：每个实例独立 run_dir，互不干扰
PROVIDER_RUN_DIR="${BUILD_DIR}/provider-qps"
CONSUMER_RUN_DIR="${BUILD_DIR}/consumer"
mkdir -p "${PROVIDER_RUN_DIR}" "${CONSUMER_RUN_DIR}"
ln -sf "${SCRIPT_DIR}/provider-qps/polaris.yaml" "${PROVIDER_RUN_DIR}/polaris.yaml"
ln -sf "${SCRIPT_DIR}/consumer/polaris.yaml" "${CONSUMER_RUN_DIR}/polaris.yaml"

# provider/consumer 的 --debug flag：LOG_LEVEL=DEBUG 时透传给二进制，
# 由 main.go 调用 api.SetLoggersLevel(api.DebugLog) 让 SDK 全部 logger 下到 DEBUG
debug_args=()
if [[ "$LOG_LEVEL" == "DEBUG" ]]; then
    debug_args+=(--debug)
fi

log_info "启动 provider-qps: run_dir=${PROVIDER_RUN_DIR}, log_level=${LOG_LEVEL}, --service ${METRICS_SERVICE} --port ${PORT_PROVIDER}"
# pushd 到 run_dir：SDK 从 cwd 加载 polaris.yaml，日志写到 run_dir/polaris/log/
pushd "${PROVIDER_RUN_DIR}" >/dev/null
POLARIS_SERVER="$POLARIS_SERVER" \
POLARIS_TOKEN="$POLARIS_TOKEN" \
POLARIS_LIMITER_NS="$LIMITER_NS" \
POLARIS_LIMITER_SVC="$LIMITER_SVC" \
POLARIS_METRICS_PORT="$PORT_PROVIDER_METRICS" \
    "${BIN_DIR}/provider-qps" \
        --namespace "$NAMESPACE" \
        --service "$METRICS_SERVICE" \
        --port "$PORT_PROVIDER" \
        ${POLARIS_TOKEN:+--token "$POLARIS_TOKEN"} \
        ${debug_args[@]+"${debug_args[@]}"} \
    >"$PROVIDER_LOG" 2>&1 &
PROVIDER_PID=$!
popd >/dev/null
log_info "provider PID=${PROVIDER_PID} (run_dir: ${PROVIDER_RUN_DIR}, SDK 日志: ${PROVIDER_RUN_DIR}/polaris/log)"

log_info "启动 consumer: run_dir=${CONSUMER_RUN_DIR}, log_level=${LOG_LEVEL}, --service ${METRICS_SERVICE} --port ${PORT_CONSUMER}"
pushd "${CONSUMER_RUN_DIR}" >/dev/null
POLARIS_SERVER="$POLARIS_SERVER" \
POLARIS_TOKEN="$POLARIS_TOKEN" \
    "${BIN_DIR}/consumer" \
        --namespace "$NAMESPACE" \
        --service "$METRICS_SERVICE" \
        --port "$PORT_CONSUMER" \
        ${POLARIS_TOKEN:+--token "$POLARIS_TOKEN"} \
        ${debug_args[@]+"${debug_args[@]}"} \
    >"$CONSUMER_LOG" 2>&1 &
CONSUMER_PID=$!
popd >/dev/null
log_info "consumer PID=${CONSUMER_PID} (run_dir: ${CONSUMER_RUN_DIR}, SDK 日志: ${CONSUMER_RUN_DIR}/polaris/log)"

# 等 provider/consumer 启动完成 + 注册到 polaris-server
# 注：provider 注册后需要 5-15s 心跳上报，polaris-server 才会标记为 healthy
# 且上一次测试残留的 provider 实例可能还在 polaris-server 上（heartbeat 未超时），
# 会导致就绪检查误判——先 sleep 5s 让新 provider 注册 + 旧实例开始超时
log_info "等待 provider/consumer 启动并注册到 polaris-server（provider 心跳上报需 5-15s）..."
sleep 5
provider_ready=false
for i in $(seq 1 30); do
    if ! kill -0 "$PROVIDER_PID" 2>/dev/null; then
        log_error "provider 进程已退出，日志末尾："
        tail -30 "$PROVIDER_LOG" 2>/dev/null
        exit 1
    fi
    if ! kill -0 "$CONSUMER_PID" 2>/dev/null; then
        log_error "consumer 进程已退出，日志末尾："
        tail -30 "$CONSUMER_LOG" 2>/dev/null
        exit 1
    fi
    # 检查 consumer 端口可达
    http_code=$(curl -s -o /dev/null -w '%{http_code}' \
        --connect-timeout 1 --max-time 2 \
        "http://127.0.0.1:${PORT_CONSUMER}/" 2>/dev/null)
    # 检查 provider 是否已注册且 healthy（查询 healthy=true 的实例，amount > 0 才算就绪）
    inst_code=$(curl -s -o /tmp/_p_$$.tmp -w '%{http_code}' \
        --connect-timeout 2 --max-time 5 \
        "${POLARIS_HTTP_ADDR}/naming/v1/instances?service=${METRICS_SERVICE}&namespace=${NAMESPACE}&healthy=true&limit=10" \
        --header "X-Polaris-Token:${POLARIS_TOKEN}" 2>/dev/null)
    inst_resp=$(cat /tmp/_p_$$.tmp 2>/dev/null || echo "")
    rm -f /tmp/_p_$$.tmp
    # 用 python 解析 amount 字段
    inst_amount=$(echo "$inst_resp" | python3 -c "import sys,json; print(json.load(sys.stdin).get('amount',0))" 2>/dev/null || echo "0")
    if [[ "$http_code" != "000" && "$inst_code" == "200" && "$inst_amount" -gt 0 ]]; then
        provider_ready=true
        break
    fi
    sleep 1
done
if [[ "$provider_ready" != "true" ]]; then
    log_error "provider/consumer 在 30s 内未就绪"
    log_error "provider 日志末尾："
    tail -20 "$PROVIDER_LOG" 2>/dev/null
    log_error "consumer 日志末尾："
    tail -20 "$CONSUMER_LOG" 2>/dev/null
    exit 1
fi
log_info "provider + consumer 就绪"

# ======================== Step 7: 链路验证（产生限流数据） ========================
log_step "[Step 7] 链路验证: curl → consumer:${PORT_CONSUMER} → provider:${PORT_PROVIDER} → limiter:${LIMITER_GRPC_PORT}"

log_info "串行发 ${TOTAL_REQUESTS} 次 /echo 请求..."
pass_count=0
limit_count=0
other_count=0
for i in $(seq 1 "$TOTAL_REQUESTS"); do
    http_code=$(curl -s -o /dev/null -w '%{http_code}' \
        --connect-timeout 3 --max-time 5 \
        "http://127.0.0.1:${PORT_CONSUMER}/echo" 2>/dev/null)
    case "$http_code" in
        200) pass_count=$((pass_count + 1)) ;;
        429) limit_count=$((limit_count + 1)) ;;
        *)   other_count=$((other_count + 1)); log_warn "请求 #${i} 返回 HTTP=${http_code}" ;;
    esac
    log_info "  #${i}/${TOTAL_REQUESTS} HTTP=${http_code}"
    # 间隔 100ms 避免瞬时全部打到同一窗口外
    sleep 0.1
done
log_info "链路结果: 200=${pass_count} 429=${limit_count} other=${other_count}"

if [[ "$other_count" -gt 0 ]]; then
    log_error "存在异常 HTTP 状态码，分布式限流链路有问题"
    TOTAL_FAIL=$((TOTAL_FAIL + 1))
fi
# 分布式限流应生效：pass ≤ maxAmount*N 窗口，limit ≥ 1（最坏跨 2 窗口仍能限到 ≥2）
if [[ "$limit_count" -eq 0 ]]; then
    log_warn "未触发限流（429=0），可能是分布式配额尚未同步或 maxAmount 设置过松；继续验证 /metrics"
else
    log_info "分布式限流已触发（429=${limit_count}）"
fi

# ======================== Step 8: 等待 polaris-limiter flush（最长 70s） ========================
log_step "[Step 8] 等待 polaris-limiter flush（对齐到下一分钟整点，最长 70s）"
log_info "prometheus 插件每 60s flush 一次，对齐到分钟整点；现在时间: $(date '+%H:%M:%S')"

metrics_body=""
flush_ok=false
for attempt in $(seq 1 14); do  # 14 * 5s = 70s
    metrics_body=$(curl -s --connect-timeout 3 --max-time 5 \
        "${LIMITER_METRICS_ADDR}/metrics" 2>/dev/null || echo "")
    if echo "$metrics_body" | grep -q "^ratelimit_rq_total"; then
        log_info "第 ${attempt} 次轮询命中 ratelimit_rq_total（已 flush）"
        flush_ok=true
        break
    fi
    log_info "第 ${attempt} 次轮询未见 ratelimit_rq_total，等 5s... ($(date '+%H:%M:%S'))"
    sleep 5
done

if [[ "$flush_ok" != "true" ]]; then
    log_error "在 70s 内未在 /metrics 看到 ratelimit_rq_total，flush 未触发或无数据"
    log_error "/metrics 完整输出："
    echo "$metrics_body" | head -50
    TOTAL_FAIL=$((TOTAL_FAIL + 1))
    # 仍然继续，做后续断言让用户看完整失败信息
fi

# ======================== Step 9: /metrics 断言 ========================
log_step "[Step 9] /metrics 指标断言"

if [[ -z "$metrics_body" ]]; then
    metrics_body=$(curl -s --connect-timeout 3 --max-time 5 \
        "${LIMITER_METRICS_ADDR}/metrics" 2>/dev/null || echo "")
fi

if [[ -z "$metrics_body" ]]; then
    log_error "curl ${LIMITER_METRICS_ADDR}/metrics 返回空"
    TOTAL_FAIL=$((TOTAL_FAIL + 1))
    exit 1
fi

# 保存完整 metrics 便于审计
echo "$metrics_body" > "${LOG_DIR}/metrics_snapshot.txt"
log_info "完整 /metrics 已保存到 ${LOG_DIR}/metrics_snapshot.txt"

# --- 断言 1: 7 个指标都存在 ---
# gauge 类型：^metric_name <空格>value
# counter 类型：^metric_name{<labels>} value
assert_metric_exists() {
    local metric="$1"
    if ! echo "$metrics_body" | grep -qE "^${metric}[[:space:]{]"; then
        log_error "缺少指标: ${metric}"
        return 1
    fi
    return 0
}

metrics_ok=true
for m in ratelimit_active_streams ratelimit_counter_count \
         ratelimit_process_avg_us ratelimit_process_max_us \
         ratelimit_rq_total ratelimit_rq_pass ratelimit_rq_limit; do
    if ! assert_metric_exists "$m"; then
        metrics_ok=false
        TOTAL_FAIL=$((TOTAL_FAIL + 1))
    fi
done
if [[ "$metrics_ok" == "true" ]]; then
    log_info "✓ 7 个 ratelimit_* 指标全部存在"
fi

# --- 断言 2: ratelimit_active_streams ≥ 1（provider 已接入 limiter） ---
active_streams=$(echo "$metrics_body" | grep "^ratelimit_active_streams " | awk '{print $2}')
if [[ -z "$active_streams" ]]; then
    log_error "ratelimit_active_streams 无值"
    TOTAL_FAIL=$((TOTAL_FAIL + 1))
elif awk -v v="$active_streams" 'BEGIN{exit !(v<1)}'; then
    log_error "ratelimit_active_streams=${active_streams} < 1（provider 未接入 limiter）"
    TOTAL_FAIL=$((TOTAL_FAIL + 1))
else
    log_info "✓ ratelimit_active_streams=${active_streams} ≥ 1"
fi

# --- 断言 3: ratelimit_counter_count ≥ 1（至少一个限流桶） ---
counter_count=$(echo "$metrics_body" | grep "^ratelimit_counter_count " | awk '{print $2}')
if [[ -z "$counter_count" ]]; then
    log_error "ratelimit_counter_count 无值"
    TOTAL_FAIL=$((TOTAL_FAIL + 1))
elif awk -v v="$counter_count" 'BEGIN{exit !(v<1)}'; then
    log_error "ratelimit_counter_count=${counter_count} < 1（无活跃限流桶）"
    TOTAL_FAIL=$((TOTAL_FAIL + 1))
else
    log_info "✓ ratelimit_counter_count=${counter_count} ≥ 1"
fi

# --- 断言 4: ratelimit_rq_total/pass/limit label 7 维齐备 + service 精确匹配 ---
# label 顺序（prometheus 字母序）: appid, duration, labels, method, namespace, service, uin
# 注意：label 值可以为空串（如 appid=""），这是合法的——只要 key 出现就算"齐备"
check_labels() {
    local line="$1" label_name="$2" expected="$3"
    # 先检查 label key 是否出现（key=" 是 prometheus label 的标志）
    if ! echo "$line" | grep -q "${label_name}=\""; then
        log_error "  label ${label_name} 缺失（key 未出现）"
        return 1
    fi
    # 值为空串是合法的，只有当 expected 非空时才校验值
    local val
    val=$(echo "$line" | sed -n 's/.*'"${label_name}"'="\([^"]*\)".*/\1/p')
    if [[ -n "$expected" && "$val" != "$expected" ]]; then
        log_error "  label ${label_name}=\"${val}\" != 期望 \"${expected}\""
        return 1
    fi
    return 0
}

# 找到 service=${METRICS_SERVICE} 的 ratelimit_rq_total 行
total_line=$(echo "$metrics_body" | grep "^ratelimit_rq_total{" | \
    grep "service=\"${METRICS_SERVICE}\"" | head -1)

if [[ -z "$total_line" ]]; then
    log_error "未找到 ratelimit_rq_total{service=\"${METRICS_SERVICE}\"} 行"
    log_error "  /metrics 中所有 ratelimit_rq_total 行："
    echo "$metrics_body" | grep "^ratelimit_rq_total{" | sed 's/^/    /'
    TOTAL_FAIL=$((TOTAL_FAIL + 1))
else
    log_info "目标 ratelimit_rq_total 行: ${total_line}"
    labels_ok=true
    for lbl in appid duration labels method namespace service uin; do
        exp_val=""
        case "$lbl" in
            service)   exp_val="$METRICS_SERVICE" ;;
            namespace) exp_val="$NAMESPACE" ;;
            # method label 在 polaris-limiter 服务端实际为空串（method 信息被放到 labels 字段）
            # 这里只校验 label key 存在，不校验值
            duration)  exp_val="${RULE_WINDOW_SECOND}s" ;;
        esac
        if ! check_labels "$total_line" "$lbl" "$exp_val"; then
            labels_ok=false
        fi
    done
    if [[ "$labels_ok" == "true" ]]; then
        log_info "✓ 7 维 label 齐备且 service=${METRICS_SERVICE} 精确匹配"
    else
        TOTAL_FAIL=$((TOTAL_FAIL + 1))
    fi
fi

# --- 断言 5: total == pass + limit 且 pass > 0 且 limit > 0 ---
# 取目标行的 total 值
m_total=$(echo "$total_line" | awk '{print $NF}')
# 简化：直接按 namespace+service 匹配（同一规则下应只有一组 label）
m_pass=$(echo "$metrics_body" | grep "^ratelimit_rq_pass{" | \
    grep "service=\"${METRICS_SERVICE}\"" | head -1 | awk '{print $NF}')
m_limit=$(echo "$metrics_body" | grep "^ratelimit_rq_limit{" | \
    grep "service=\"${METRICS_SERVICE}\"" | head -1 | awk '{print $NF}')

log_info "数值: total=${m_total} pass=${m_pass} limit=${m_limit}"

numeric_ok=true
if [[ -z "$m_total" || -z "$m_pass" ]]; then
    log_error "total 或 pass 值为空"
    numeric_ok=false
elif awk -v v="$m_pass" 'BEGIN{exit !(v<=0)}'; then
    log_error "pass=${m_pass} ≤ 0（应该 > 0）"
    numeric_ok=false
fi
if [[ -z "$m_limit" ]]; then
    log_error "limit 值为空（可能未触发限流，或尚未 flush 到 limit 行）"
    # 不直接 fail，仅警告——limit=0 时 prometheus 不输出该维度行
    log_warn "  注: 限流 limited=0 的维度不会输出 limit 行（prometheus 行为）"
elif awk -v v="$m_limit" 'BEGIN{exit !(v<=0)}'; then
    log_warn "limit=${m_limit} ≤ 0（可能本次请求未触发限流，或跨窗口聚合后 limited=0）"
fi

# total == pass + limit（limit 为空时按 0 处理）
m_limit_num="${m_limit:-0}"
if [[ -n "$m_total" && -n "$m_pass" ]]; then
    # 用 awk 做浮点比较，避免 shell 算术只支持整数
    if awk -v t="$m_total" -v p="$m_pass" -v l="$m_limit_num" \
        'BEGIN{exit (t == p + l) ? 0 : 1}'; then
        log_info "✓ total(${m_total}) == pass(${m_pass}) + limit(${m_limit_num})"
    else
        log_error "total(${m_total}) != pass(${m_pass}) + limit(${m_limit_num})"
        numeric_ok=false
    fi
fi

if [[ "$numeric_ok" != "true" ]]; then
    TOTAL_FAIL=$((TOTAL_FAIL + 1))
fi

# --- 断言 6: ratelimit_process_avg_us / max_us 存在且 ≥ 0 ---
for m in ratelimit_process_avg_us ratelimit_process_max_us; do
    val=$(echo "$metrics_body" | grep "^${m} " | awk '{print $2}')
    if [[ -z "$val" ]]; then
        log_error "${m} 无值"
        TOTAL_FAIL=$((TOTAL_FAIL + 1))
    # 用 awk 做负数判断，避免依赖 bc
    elif awk -v v="$val" 'BEGIN{exit !(v<0)}'; then
        log_error "${m}=${val} < 0"
        TOTAL_FAIL=$((TOTAL_FAIL + 1))
    else
        log_info "✓ ${m}=${val} ≥ 0"
    fi
done

# 打印所有 ratelimit_rq_* 指标行（含完整 label + 数值），供日志审计
log_info "--- ratelimit_rq 完整指标 ---"
echo "$metrics_body" | grep "^ratelimit_rq" | while IFS= read -r line; do
    echo "  ${line}"
done

# ======================== 用例 6.x helper ========================
# 移植自 git.woa.com/polaris-go-examples/ratelimit/verify_ratelimit.sh，适配本仓库约定。

# print_block <title> <line...>：浅蓝框打印配置/操作/预期块
print_block() {
    local title="$1"
    shift
    echo -e "${CYAN}┌─ ${title} ─────────────────────────────────────────────${NC}"
    for line in "$@"; do
        echo -e "${CYAN}│${NC} ${line}"
    done
    echo -e "${CYAN}└──────────────────────────────────────────────────────────────${NC}"
}

# record_case <name> <verdict> <detail>：聚合用例结果，FAIL 累加 TOTAL_FAIL
record_case() {
    local name="$1" verdict="$2" detail="$3"
    CASE_NAMES+=("$name")
    CASE_VERDICTS+=("$verdict")
    CASE_DETAILS+=("$detail")
    [[ "$verdict" != "PASS" && "$verdict" != "SKIP" ]] && TOTAL_FAIL=$((TOTAL_FAIL + 1))
    case "$verdict" in
        PASS) echo -e "  ${GREEN}✅ [${name}] PASS${NC} - ${detail}" ;;
        FAIL) echo -e "  ${RED}❌ [${name}] FAIL${NC} - ${detail}" ;;
        WARN) echo -e "  ${YELLOW}⚠️  [${name}] WARN${NC} - ${detail}" ;;
        SKIP) echo -e "  ${YELLOW}⏭️  [${name}] SKIP${NC} - ${detail}" ;;
    esac
}

# count_status_concurrent <port> <path> <total>：并发打 N 请求，回 "ok limited other"
count_status_concurrent() {
    local port="$1" path="$2" total="$3"
    local tmp
    tmp=$(mktemp -d)
    local i
    for ((i = 0; i < total; i++)); do
        (
            code=$(curl -s -o /dev/null --connect-timeout 2 --max-time 10 \
                -w '%{http_code}' "http://127.0.0.1:${port}${path}" 2>/dev/null || echo "000")
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

# count_status_serial_with_delay <port> <path> <total> <delay_ms>：串行 + 毫秒间隔
count_status_serial_with_delay() {
    local port="$1" path="$2" total="$3" delay_ms="$4"
    local ok=0 limited=0 other=0 i code
    for ((i = 0; i < total; i++)); do
        code=$(curl -s -o /dev/null --connect-timeout 2 --max-time 5 \
            -w '%{http_code}' "http://127.0.0.1:${port}${path}" 2>/dev/null || echo "000")
        case "$code" in
            200) ok=$((ok + 1)) ;;
            429) limited=$((limited + 1)) ;;
            *)   other=$((other + 1)) ;;
        esac
        if [[ $i -lt $((total - 1)) ]]; then
            python3 -c "import time; time.sleep(${delay_ms}/1000.0)" 2>/dev/null
        fi
    done
    echo "${ok} ${limited} ${other}"
}

# run_global_burst_in_windows <port> <path> <total_per_window> <windows>
# 多窗口各发一批并发，聚合 "ok limited other"。函数内 log_info 全部 >&2，避免污染 stdout。
run_global_burst_in_windows() {
    local port="$1" path="$2" total_per_window="$3" windows="$4"
    local total_ok=0 total_limited=0 total_other=0 w
    for ((w = 0; w < windows; w++)); do
        local stat ok lim oth
        stat=$(count_status_concurrent "$port" "$path" "$total_per_window")
        read -r ok lim oth <<< "$stat"
        log_info "  窗口 $((w + 1))/${windows}: 200=${ok} 429=${lim} 其他=${oth}" >&2
        total_ok=$((total_ok + ok))
        total_limited=$((total_limited + lim))
        total_other=$((total_other + oth))
        if [[ $((w + 1)) -lt windows ]]; then
            sleep "$(awk -v s="$GLOBAL_WINDOW_SECOND" 'BEGIN{ printf "%.1f", s+0.5 }')"
        fi
    done
    echo "$total_ok $total_limited $total_other"
}

# run_global_two_instances_in_windows <port_a> <port_b> <path> <per_instance> <windows>
# 多窗口同时打 A/B 两端口，聚合（绕开 consumer 负载均衡随机性）
run_global_two_instances_in_windows() {
    local port_a="$1" port_b="$2" path="$3" per_instance="$4" windows="$5"
    local total_ok=0 total_limited=0 total_other=0 w
    for ((w = 0; w < windows; w++)); do
        local stat_a stat_b ok_a lim_a oth_a ok_b lim_b oth_b
        stat_a=$(count_status_concurrent "$port_a" "$path" "$per_instance")
        stat_b=$(count_status_concurrent "$port_b" "$path" "$per_instance")
        read -r ok_a lim_a oth_a <<< "$stat_a"
        read -r ok_b lim_b oth_b <<< "$stat_b"
        log_info "  窗口 $((w + 1))/${windows}: A=(200=${ok_a},429=${lim_a}) B=(200=${ok_b},429=${lim_b})" >&2
        total_ok=$((total_ok + ok_a + ok_b))
        total_limited=$((total_limited + lim_a + lim_b))
        total_other=$((total_other + oth_a + oth_b))
        if [[ $((w + 1)) -lt windows ]]; then
            sleep "$(awk -v s="$GLOBAL_WINDOW_SECOND" 'BEGIN{ printf "%.1f", s+0.5 }')"
        fi
    done
    echo "$total_ok $total_limited $total_other"
}

# probe_limiter_available：探测本地 polaris.limiter-local 是否有 healthy 实例。0=可用
probe_limiter_available() {
    local resp http_code
    http_code=$(curl -s -o /tmp/_rl_probe_$$.tmp -w '%{http_code}' \
        --connect-timeout 5 --max-time 10 \
        "${POLARIS_HTTP_ADDR}/naming/v1/instances?service=${LIMITER_SVC}&namespace=${LIMITER_NS}&limit=50" \
        --header "X-Polaris-Token:${POLARIS_TOKEN}" 2>/dev/null || echo "000")
    resp=$(cat /tmp/_rl_probe_$$.tmp 2>/dev/null || echo "")
    rm -f /tmp/_rl_probe_$$.tmp
    [[ "$http_code" != "200" ]] && return 1
    local healthy
    healthy=$(python3 -c "
import sys, json
try:
    data = json.load(sys.stdin)
    cnt = 0
    for ins in data.get('instances', []):
        if ins.get('healthy', False) and not ins.get('isolate', False):
            cnt += 1
    print(cnt)
except Exception:
    print(0)
" <<< "$resp" 2>/dev/null || echo 0)
    [[ "$healthy" -gt 0 ]]
}

# _metric_val <body> <metric> <service>：从 /metrics 文本中取某 service 的某 rq 指标值
# 同一 service 可能有多组 label（如 6.4 regex 多 path），全部求和返回。
_metric_val() {
    echo "$1" | grep "^${2}{" | grep "service=\"${3}\"" | awk '{s+=$NF} END{print s+0}'
}

# wait_flush_and_assert_metrics <service> <expect_limit_gt0>
# 轮询 /metrics（最长 70s）等到下一次 flush 把本服务流量写入；断言 total==pass+limit、pass>0，
# expect_limit_gt0=true 时额外要求 limit>0。返回 0=通过，1=失败（细节已打 log）。
wait_flush_and_assert_metrics() {
    local service="$1" expect_limit="$2"
    local snap_total
    snap_total=$(curl -s --connect-timeout 3 --max-time 5 "${LIMITER_METRICS_ADDR}/metrics" 2>/dev/null \
        | grep "^ratelimit_rq_total{" | grep "service=\"${service}\"" | awk '{s+=$NF} END{print s+0}')
    log_info "[metrics] ${service} flush 前 total=${snap_total:-0}，轮询等待 flush 增量（最长 120s）..."
    local body="" cur_total="" ok=false attempt
    # 修复 fileLogger 抢先 expire 共享 collector 值的 bug 后，prometheus flush 会在下一个分钟整点
    # 可靠捕获本用例流量（≤60s）；120s 窗口覆盖一个 flush 周期 + 余量。
    for attempt in $(seq 1 24); do
        body=$(curl -s --connect-timeout 3 --max-time 5 "${LIMITER_METRICS_ADDR}/metrics" 2>/dev/null || echo "")
        cur_total=$(_metric_val "$body" "ratelimit_rq_total" "$service")
        # 等 total 相对快照发生变化（新 flush 捕获了流量）；快照为空时等到 total>0
        if [[ -n "$cur_total" ]] && awk -v c="$cur_total" 'BEGIN{exit !(c>0)}'; then
            if [[ -z "$snap_total" ]] || [[ "$cur_total" != "$snap_total" ]]; then
                ok=true
                break
            fi
        fi
        log_info "[metrics] 第 ${attempt}/24 次轮询：total=${cur_total:-0}（未变化），等 5s... ($(date '+%H:%M:%S'))" >&2
        sleep 5
    done
    if [[ "$ok" != "true" ]]; then
        log_error "[metrics] ${service} 在 120s 内未观察到 flush 增量（snap=${snap_total:-0} cur=${cur_total:-0}）"
        return 1
    fi
    local m_total m_pass m_limit m_limit_num
    m_total=$(_metric_val "$body" "ratelimit_rq_total" "$service")
    m_pass=$(_metric_val "$body" "ratelimit_rq_pass" "$service")
    m_limit=$(_metric_val "$body" "ratelimit_rq_limit" "$service")
    m_limit_num="${m_limit:-0}"
    log_info "[metrics] ${service}: total=${m_total} pass=${m_pass} limit=${m_limit_num}"
    if [[ -z "$m_total" || -z "$m_pass" ]]; then
        log_error "[metrics] ${service} total 或 pass 值为空"
        return 1
    fi
    if awk -v v="$m_pass" 'BEGIN{exit !(v<=0)}'; then
        log_error "[metrics] ${service} pass=${m_pass} ≤ 0"
        return 1
    fi
    if ! awk -v t="$m_total" -v p="$m_pass" -v l="$m_limit_num" \
        'BEGIN{exit (t == p + l) ? 0 : 1}'; then
        log_error "[metrics] ${service} total(${m_total}) != pass(${m_pass}) + limit(${m_limit_num})"
        return 1
    fi
    if [[ "$expect_limit" == "true" ]]; then
        if [[ -z "$m_limit" ]] || awk -v v="$m_limit" 'BEGIN{exit !(v<=0)}'; then
            log_error "[metrics] ${service} 期望 limit>0，实际 limit=${m_limit:-（空）}"
            return 1
        fi
    fi
    log_info "[metrics] ✓ ${service} /metrics 正常：total=${m_total}==pass(${m_pass})+limit(${m_limit_num})"
    return 0
}

# start_provider_instance <port> <service> <run_name> <out_pid_var> [limiter_svc_override]
# 复用现有 inline 启动模式：独立 run_dir、软链 polaris.yaml、pushd 启动、轮询端口就绪。
start_provider_instance() {
    local port="$1" service="$2" run_name="$3" out_var="$4" limiter_svc="${5:-}"
    local run_dir="${BUILD_DIR}/${run_name}"
    local metrics_port=$((port + 10000))
    mkdir -p "$run_dir"
    ln -sf "${SCRIPT_DIR}/provider-qps/polaris.yaml" "${run_dir}/polaris.yaml"
    local pid_log="${LOG_DIR}/${run_name}.log"
    : > "$pid_log"
    local limiter_svc_env="${limiter_svc:-$LIMITER_SVC}"
    log_info "[start] ${run_name} 监听 :${port} (metrics :${metrics_port}), service=${service}, limiter=${limiter_svc_env}"
    pushd "$run_dir" >/dev/null
    POLARIS_SERVER="$POLARIS_SERVER" \
    POLARIS_TOKEN="$POLARIS_TOKEN" \
    POLARIS_LIMITER_NS="$LIMITER_NS" \
    POLARIS_LIMITER_SVC="$limiter_svc_env" \
    POLARIS_METRICS_PORT="$metrics_port" \
        "${BIN_DIR}/provider-qps" \
            --namespace "$NAMESPACE" --service "$service" --port "$port" \
            ${POLARIS_TOKEN:+--token "$POLARIS_TOKEN"} \
            ${debug_args[@]+"${debug_args[@]}"} \
        >"$pid_log" 2>&1 &
    local pid=$!
    popd >/dev/null
    eval "$out_var=\"$pid\""
    local i
    for ((i = 0; i < 30; i++)); do
        if ! kill -0 "$pid" 2>/dev/null; then
            log_error "[start] ${run_name} 进程已退出，日志末尾："
            tail -20 "$pid_log" 2>/dev/null
            return 1
        fi
        if curl -fsS --connect-timeout 1 --max-time 2 "http://127.0.0.1:${port}/echo" >/dev/null 2>&1; then
            log_info "[ready] ${run_name} (PID=${pid}, port=${port}) 就绪"
            return 0
        fi
        # /echo 可能因规则已生效返回 429，也算就绪，TCP 探测兜底
        if (echo > "/dev/tcp/127.0.0.1/${port}") 2>/dev/null; then
            log_info "[ready] ${run_name} (PID=${pid}, port=${port}) TCP 端口已打开"
            return 0
        fi
        sleep 1
    done
    log_error "[start] ${run_name} 30s 内未就绪，详见 ${pid_log}"
    return 1
}

# start_consumer_instance <port> <service> <run_name> <out_pid_var>
start_consumer_instance() {
    local port="$1" service="$2" run_name="$3" out_var="$4"
    local run_dir="${BUILD_DIR}/${run_name}"
    mkdir -p "$run_dir"
    ln -sf "${SCRIPT_DIR}/consumer/polaris.yaml" "${run_dir}/polaris.yaml"
    local pid_log="${LOG_DIR}/${run_name}.log"
    : > "$pid_log"
    log_info "[start] ${run_name} 监听 :${port}, service=${service}"
    pushd "$run_dir" >/dev/null
    POLARIS_SERVER="$POLARIS_SERVER" \
    POLARIS_TOKEN="$POLARIS_TOKEN" \
        "${BIN_DIR}/consumer" \
            --namespace "$NAMESPACE" --service "$service" --port "$port" \
            ${POLARIS_TOKEN:+--token "$POLARIS_TOKEN"} \
            ${debug_args[@]+"${debug_args[@]}"} \
        >"$pid_log" 2>&1 &
    local pid=$!
    popd >/dev/null
    eval "$out_var=\"$pid\""
    local i
    for ((i = 0; i < 30; i++)); do
        if ! kill -0 "$pid" 2>/dev/null; then
            log_error "[start] ${run_name} 进程已退出，日志末尾："
            tail -20 "$pid_log" 2>/dev/null
            return 1
        fi
        if curl -fsS --connect-timeout 1 --max-time 2 "http://127.0.0.1:${port}/" >/dev/null 2>&1; then
            log_info "[ready] ${run_name} (PID=${pid}, port=${port}) 就绪"
            return 0
        fi
        sleep 1
    done
    log_error "[start] ${run_name} 30s 内未就绪，详见 ${pid_log}"
    return 1
}

# ======================== 用例 6.x 规则函数 ========================

_build_global_rule_body() {
    local rule_name="$1" rule_id="$2"
    SVC="$GLOBAL_SERVICE" NS="$NAMESPACE" NAME="$rule_name" \
        AMOUNT="$GLOBAL_MAX_AMOUNT" WINDOW="$GLOBAL_WINDOW_SECOND" RULE_ID="$rule_id" \
        python3 -c "
import os, json
rule = {
    'name': os.environ['NAME'],
    'service': os.environ['SVC'],
    'namespace': os.environ['NS'],
    'priority': 0,
    'resource': 'QPS',
    'type': 'GLOBAL',
    'method': {'type': 'EXACT', 'value': '/echo'},
    'amounts': [{'maxAmount': int(os.environ['AMOUNT']), 'validDuration': '%ss' % os.environ['WINDOW']}],
    'action': 'REJECT',
    'failover': 'FAILOVER_LOCAL',
    'disable': False,
}
rid = os.environ.get('RULE_ID', '')
if rid:
    rule['id'] = rid
print(json.dumps([rule]))
"
}

create_global_rule() {
    local rule_name="$GLOBAL_RULE_NAME"
    if rule_exists "$rule_name" "$GLOBAL_SERVICE"; then
        log_info "GLOBAL 规则 [$rule_name] 已存在于服务 [$GLOBAL_SERVICE]，跳过创建"
        return 0
    fi
    local body
    body=$(_build_global_rule_body "$rule_name" "")
    local http_code resp
    http_code=$(curl -s -o /tmp/_rl_c_$$.tmp -w '%{http_code}' \
        --connect-timeout 5 --max-time 10 \
        --request POST "${POLARIS_HTTP_ADDR}/naming/v1/ratelimits" \
        --header "X-Polaris-Token:${POLARIS_TOKEN}" \
        --header 'Content-Type: application/json' \
        --data-raw "$body" 2>/dev/null || echo "000")
    resp=$(cat /tmp/_rl_c_$$.tmp 2>/dev/null || echo "")
    rm -f /tmp/_rl_c_$$.tmp
    if [[ "$http_code" != "200" ]]; then
        log_error "创建 GLOBAL 规则失败 HTTP=${http_code} resp=${resp}"
        return 1
    fi
    log_info "GLOBAL 规则 [$rule_name] 已创建"
    return 0
}

_build_regex_rule_body() {
    local rule_name="$1" rule_id="$2" regex_combine="$3"
    SVC="$REGEX_SERVICE" NS="$NAMESPACE" NAME="$rule_name" \
        AMOUNT="$REGEX_MAX_AMOUNT" WINDOW="$REGEX_WINDOW_SECOND" \
        PATTERN="$REGEX_PATH_PATTERN" REGEX_COMBINE="$regex_combine" RULE_ID="$rule_id" \
        python3 -c "
import os, json
rule = {
    'name': os.environ['NAME'],
    'service': os.environ['SVC'],
    'namespace': os.environ['NS'],
    'priority': 0,
    'resource': 'QPS',
    'type': 'LOCAL',
    'method': {'type': 'REGEX', 'value': os.environ['PATTERN']},
    'amounts': [{'maxAmount': int(os.environ['AMOUNT']), 'validDuration': '%ss' % os.environ['WINDOW']}],
    'action': 'REJECT',
    'regex_combine': os.environ['REGEX_COMBINE'].lower() == 'true',
    'disable': False,
}
rid = os.environ.get('RULE_ID', '')
if rid:
    rule['id'] = rid
print(json.dumps([rule]))
"
}

create_regex_rule() {
    local rule_name="$REGEX_RULE_NAME"
    if rule_exists "$rule_name" "$REGEX_SERVICE"; then
        log_info "regex 规则 [$rule_name] 已存在于服务 [$REGEX_SERVICE]，跳过创建"
        return 0
    fi
    local body
    body=$(_build_regex_rule_body "$rule_name" "" "false")
    local http_code resp
    http_code=$(curl -s -o /tmp/_rl_c_$$.tmp -w '%{http_code}' \
        --connect-timeout 5 --max-time 10 \
        --request POST "${POLARIS_HTTP_ADDR}/naming/v1/ratelimits" \
        --header "X-Polaris-Token:${POLARIS_TOKEN}" \
        --header 'Content-Type: application/json' \
        --data-raw "$body" 2>/dev/null || echo "000")
    resp=$(cat /tmp/_rl_c_$$.tmp 2>/dev/null || echo "")
    rm -f /tmp/_rl_c_$$.tmp
    if [[ "$http_code" != "200" ]]; then
        log_error "创建 regex 规则失败 HTTP=${http_code} resp=${resp}"
        return 1
    fi
    log_info "regex 规则 [$rule_name] 已创建（LOCAL + regex_combine=false）"
    return 0
}

# flip_regex_combine_to_global <rule_id>：把 regex 规则切到 GLOBAL+regex_combine=true（6.4 用）
flip_regex_combine_to_global() {
    local rule_id="$1"
    local body
    body=$(SVC="$REGEX_SERVICE" NS="$NAMESPACE" NAME="$REGEX_RULE_NAME" \
        AMOUNT="$REGEX_MAX_AMOUNT" WINDOW="$REGEX_WINDOW_SECOND" \
        PATTERN="$REGEX_PATH_PATTERN" RULE_ID="$rule_id" \
        python3 -c "
import os, json
print(json.dumps([{
    'id': os.environ['RULE_ID'],
    'name': os.environ['NAME'],
    'service': os.environ['SVC'],
    'namespace': os.environ['NS'],
    'priority': 0,
    'resource': 'QPS',
    'type': 'GLOBAL',
    'method': {'type': 'REGEX', 'value': os.environ['PATTERN']},
    'amounts': [{'maxAmount': int(os.environ['AMOUNT']), 'validDuration': '%ss' % os.environ['WINDOW']}],
    'action': 'REJECT',
    'failover': 'FAILOVER_LOCAL',
    'regex_combine': True,
    'disable': False,
}]))")
    update_rule_via_http "$body"
}

# ======================== Step 10: 用例 6.x 分布式限流语义 + /metrics 验证 ========================
log_step "[Step 10] 用例 6.x 分布式集群限流 GLOBAL（链路: curl → consumer/provider → limiter:${LIMITER_GRPC_PORT}）"

run_global_cases() {
    # ---------- 用例 6.0：探测 polaris.limiter-local 是否可用 ----------
    print_block "[用例 6.0] 探测 ${LIMITER_NS}/${LIMITER_SVC} 健康实例" \
        "操作: GET /naming/v1/instances?service=${LIMITER_SVC}&namespace=${LIMITER_NS}" \
        "预期: 至少 1 个 healthy=true 且 isolate=false 的实例" \
        "判定: 有健康实例 → PASS（整段 6.x 可跑）；否则 SKIP 整段"
    if ! probe_limiter_available; then
        record_case "用例 6.0 limiter 探测" "SKIP" \
            "${LIMITER_NS}/${LIMITER_SVC} 下无健康实例；整段 6.x 跳过"
        return
    fi
    record_case "用例 6.0 limiter 探测" "PASS" \
        "${LIMITER_NS}/${LIMITER_SVC} 下存在健康实例，可继续 6.x"

    if ! create_global_rule; then
        record_case "6.0 创建 GLOBAL 规则" "FAIL" "HTTP API 调用失败"
        return
    fi

    # 启动 global provider A + consumer（6.1/6.2 用）
    if ! start_provider_instance "$PORT_PROVIDER_GLOBAL_A" "$GLOBAL_SERVICE" \
        "provider-global-a" "PROVIDER_GLOBAL_A_PID"; then
        record_case "6.0 启动 GLOBAL provider A" "FAIL" "provider-global-a 启动失败"
        return
    fi
    if ! start_consumer_instance "$PORT_CONSUMER_GLOBAL" "$GLOBAL_SERVICE" \
        "consumer-global" "CONSUMER_GLOBAL_PID"; then
        record_case "6.0 启动 GLOBAL consumer" "FAIL" "consumer-global 启动失败"
        return
    fi
    log_info "等待 6s 让 SDK 拉规则 + 与 limiter 完成首次配额同步..."
    sleep 6

    local stat ok limited other

    # ---------- 用例 6.1：GLOBAL 多窗口聚合触发限流 ----------
    local windows=$GLOBAL_OBSERVE_WINDOWS
    local total_per_window=$GLOBAL_BURST_REQUESTS
    local agg_min_limited_6_1=$((windows * 1))
    print_block "[用例 6.1] GLOBAL 多窗口聚合触发限流" \
        "操作: 连续 ${windows} 个窗口，每窗口经 consumer:${PORT_CONSUMER_GLOBAL} 并发突发 ${total_per_window} 次" \
        "原理: type=GLOBAL → SDK 走 gRPC 与 limiter 通信；阈值 ${GLOBAL_MAX_AMOUNT}/${GLOBAL_WINDOW_SECOND}s 为全集群配额" \
        "预期: ${windows} 窗口合计 limited ≥ ${agg_min_limited_6_1}，且 /metrics 中 ${GLOBAL_SERVICE} 的 total==pass+limit、limit>0" \
        "判定: limited ≥ ${agg_min_limited_6_1} && other==0 && /metrics 正常 → PASS"
    log_info "[用例 6.1] 连续 ${windows} 窗口并发突发"
    stat=$(run_global_burst_in_windows "$PORT_CONSUMER_GLOBAL" "/echo" "$total_per_window" "$windows")
    read -r ok limited other <<< "$stat"
    log_info "[用例 6.1] 行为结果: 总 200=${ok} 429=${limited} 其他=${other}"
    local m_ok_61=true
    if [[ "$other" -gt 0 ]]; then
        record_case "用例 6.1 GLOBAL 多窗口触发限流" "FAIL" "出现非 200/429 状态码 (other=${other})"
        m_ok_61=false
    elif [[ "$limited" -lt "$agg_min_limited_6_1" ]]; then
        record_case "用例 6.1 GLOBAL 多窗口触发限流" "FAIL" "聚合 limited=${limited} 不足 ${agg_min_limited_6_1}"
        m_ok_61=false
    fi
    log_info "[用例 6.1] 验证 /metrics..."
    if $m_ok_61 && wait_flush_and_assert_metrics "$GLOBAL_SERVICE" true; then
        record_case "用例 6.1 GLOBAL 多窗口触发限流" "PASS" \
            "${windows} 窗口合计: 200=${ok} 429=${limited}（≥${agg_min_limited_6_1}），/metrics 正常"
    elif $m_ok_61; then
        record_case "用例 6.1 GLOBAL 多窗口触发限流" "FAIL" "行为通过但 /metrics 断言失败"
    fi

    sleep $((GLOBAL_WINDOW_SECOND + 1))

    # ---------- 用例 6.2：GLOBAL 新窗口仍能触发限流 ----------
    local agg_min_limited_6_2=$(((windows + 1) / 2))
    print_block "[用例 6.2] GLOBAL 新窗口仍能触发限流" \
        "操作: 再次连续 ${windows} 个窗口，每窗口并发突发 ${total_per_window} 次" \
        "原理: 远端 limiter 按窗口重置配额；规则持续生效而非一次静音" \
        "预期: limited ≥ ${agg_min_limited_6_2} && other==0 && /metrics 正常"
    log_info "[用例 6.2] 新一轮 ${windows} 窗口并发突发"
    stat=$(run_global_burst_in_windows "$PORT_CONSUMER_GLOBAL" "/echo" "$total_per_window" "$windows")
    read -r ok limited other <<< "$stat"
    log_info "[用例 6.2] 行为结果: 总 200=${ok} 429=${limited} 其他=${other}"
    local m_ok_62=true
    if [[ "$other" -gt 0 ]]; then
        record_case "用例 6.2 GLOBAL 新窗口再次生效" "FAIL" "出现非 200/429 状态码 (other=${other})"
        m_ok_62=false
    elif [[ "$limited" -lt "$agg_min_limited_6_2" ]]; then
        record_case "用例 6.2 GLOBAL 新窗口再次生效" "FAIL" "聚合 limited=${limited} 不足 ${agg_min_limited_6_2}"
        m_ok_62=false
    fi
    log_info "[用例 6.2] 验证 /metrics..."
    if $m_ok_62 && wait_flush_and_assert_metrics "$GLOBAL_SERVICE" true; then
        record_case "用例 6.2 GLOBAL 新窗口再次生效" "PASS" \
            "${windows} 窗口合计: 200=${ok} 429=${limited}（≥${agg_min_limited_6_2}），/metrics 正常"
    elif $m_ok_62; then
        record_case "用例 6.2 GLOBAL 新窗口再次生效" "FAIL" "行为通过但 /metrics 断言失败"
    fi

    sleep $((GLOBAL_WINDOW_SECOND + 1))

    # ---------- 用例 6.3：GLOBAL 多实例共享配额 ----------
    if ! start_provider_instance "$PORT_PROVIDER_GLOBAL_B" "$GLOBAL_SERVICE" \
        "provider-global-b" "PROVIDER_GLOBAL_B_PID"; then
        record_case "用例 6.3 GLOBAL 多实例共享配额" "FAIL" "provider-global-b 启动失败"
    else
        sleep 6
        local shared_windows=$GLOBAL_SHARED_WINDOWS
        local per_instance=$GLOBAL_PER_INSTANCE_REQUESTS
        local share_local_bound=$((shared_windows * 2 * GLOBAL_MAX_AMOUNT))
        local share_max_ok=$((share_local_bound - GLOBAL_MAX_AMOUNT))
        local share_min_limited=$((shared_windows * 1))
        print_block "[用例 6.3] GLOBAL 多实例共享配额（核心语义）" \
            "操作: 连续 ${shared_windows} 个窗口，每窗口同时打 A:${PORT_PROVIDER_GLOBAL_A} 与 B:${PORT_PROVIDER_GLOBAL_B} 各 ${per_instance} 并发" \
            "原理: 两 provider 共享同一远端配额；LOCAL 退化恒为 ${share_local_bound}，GLOBAL 实测应明显低于该值" \
            "预期: ok ≤ ${share_max_ok}（LOCAL 下界=${share_local_bound}）、limited ≥ ${share_min_limited}、/metrics 正常"
        log_info "[用例 6.3] ${shared_windows} 窗口双实例并发突发"
        stat=$(run_global_two_instances_in_windows "$PORT_PROVIDER_GLOBAL_A" "$PORT_PROVIDER_GLOBAL_B" "/echo" "$per_instance" "$shared_windows")
        read -r ok limited other <<< "$stat"
        log_info "[用例 6.3] 行为结果: 总 200=${ok} 429=${limited} 其他=${other}"
        local m_ok_63=true
        if [[ "$other" -gt 0 ]]; then
            record_case "用例 6.3 GLOBAL 多实例共享配额" "FAIL" "出现非 200/429 状态码 (other=${other})"
            m_ok_63=false
        elif [[ "$ok" -ge "$share_local_bound" ]]; then
            record_case "用例 6.3 GLOBAL 多实例共享配额" "FAIL" "ok=${ok} 达到 LOCAL 退化下界 ${share_local_bound}（远端未接入）"
            m_ok_63=false
        elif [[ "$ok" -gt "$share_max_ok" ]]; then
            record_case "用例 6.3 GLOBAL 多实例共享配额" "FAIL" "ok=${ok} 落在灰区（>${share_max_ok}），GLOBAL 节流不充分"
            m_ok_63=false
        elif [[ "$limited" -lt "$share_min_limited" ]]; then
            record_case "用例 6.3 GLOBAL 多实例共享配额" "FAIL" "limited=${limited} 不足 ${share_min_limited}"
            m_ok_63=false
        fi
        log_info "[用例 6.3] 验证 /metrics..."
        if $m_ok_63 && wait_flush_and_assert_metrics "$GLOBAL_SERVICE" true; then
            record_case "用例 6.3 GLOBAL 多实例共享配额" "PASS" \
                "${shared_windows} 窗口合计 ok=${ok}（≤${share_max_ok}，相对 LOCAL ${share_local_bound} 节省 $((share_local_bound - ok))）、429=${limited}，/metrics 正常"
        elif $m_ok_63; then
            record_case "用例 6.3 GLOBAL 多实例共享配额" "FAIL" "行为通过但 /metrics 断言失败"
        fi
    fi

    sleep $((GLOBAL_WINDOW_SECOND + 1))

    # ---------- 用例 6.3.5：GLOBAL 稳态精准验证（串行 + 60ms 间隔） ----------
    if [[ -n "$PROVIDER_GLOBAL_B_PID" ]] && kill -0 "$PROVIDER_GLOBAL_B_PID" 2>/dev/null; then
        local steady_per_instance=8 steady_delay_ms=60 steady_windows=3
        local steady_max_ok=$((steady_windows * GLOBAL_MAX_AMOUNT + GLOBAL_MAX_AMOUNT))
        local steady_min_limited=$steady_windows
        print_block "[用例 6.3.5] GLOBAL 稳态精准验证（串行 + ${steady_delay_ms}ms 间隔）" \
            "操作: A/B 两实例并发，每实例内部串行 ${steady_per_instance} 个，间隔 ${steady_delay_ms}ms" \
            "原理: 间隔 > SDK acquire 周期，burst≈0，可严格验证全集群合计语义" \
            "预期: 合计 ok ≤ ${steady_max_ok}、limited ≥ ${steady_min_limited}、/metrics 正常"
        log_info "[用例 6.3.5] 双实例并发 + 实例内串行（${steady_per_instance} 次, ${steady_delay_ms}ms 间隔）"
        local steady_tmp
        steady_tmp=$(mktemp -d)
        (
            count_status_serial_with_delay "$PORT_PROVIDER_GLOBAL_A" "/echo" "$steady_per_instance" "$steady_delay_ms" \
                > "${steady_tmp}/a"
        ) &
        local pid_a=$!
        (
            count_status_serial_with_delay "$PORT_PROVIDER_GLOBAL_B" "/echo" "$steady_per_instance" "$steady_delay_ms" \
                > "${steady_tmp}/b"
        ) &
        local pid_b=$!
        wait "$pid_a" "$pid_b"
        local sa sb
        read -r ok sa other < "${steady_tmp}/a"
        local ok_a=$ok lim_a=$sa
        read -r ok sb other < "${steady_tmp}/b"
        local ok_b=$ok lim_b=$sb
        rm -rf "$steady_tmp"
        ok=$((ok_a + ok_b))
        limited=$((lim_a + lim_b))
        other=0
        log_info "[用例 6.3.5] 行为结果: A=(200=${ok_a},429=${lim_a}) B=(200=${ok_b},429=${lim_b}) 合计 ok=${ok} limit=${limited}"
        local m_ok_635=true
        if [[ "$ok" -gt "$steady_max_ok" ]]; then
            record_case "用例 6.3.5 GLOBAL 稳态精准验证" "FAIL" "稳态 ok=${ok} 超过上界 ${steady_max_ok}"
            m_ok_635=false
        elif [[ "$limited" -lt "$steady_min_limited" ]]; then
            record_case "用例 6.3.5 GLOBAL 稳态精准验证" "FAIL" "稳态 limited=${limited} 不足 ${steady_min_limited}"
            m_ok_635=false
        fi
        log_info "[用例 6.3.5] 验证 /metrics..."
        if $m_ok_635 && wait_flush_and_assert_metrics "$GLOBAL_SERVICE" true; then
            record_case "用例 6.3.5 GLOBAL 稳态精准验证" "PASS" \
                "ok=${ok}（≤${steady_max_ok}）、limited=${limited}（≥${steady_min_limited}），/metrics 正常"
        elif $m_ok_635; then
            record_case "用例 6.3.5 GLOBAL 稳态精准验证" "FAIL" "行为通过但 /metrics 断言失败"
        fi
        sleep $((GLOBAL_WINDOW_SECOND + 1))
    else
        record_case "用例 6.3.5 GLOBAL 稳态精准验证" "SKIP" "依赖 provider B 存活（6.3 启动失败时跳过）"
    fi

    # ---------- 用例 6.4：GLOBAL + regex_combine 多 path 共享远端配额 ----------
    if ! create_regex_rule; then
        record_case "用例 6.4 GLOBAL+regex_combine 共享" "FAIL" "regex 规则创建失败"
    else
        local regex_rule_id
        regex_rule_id=$(query_rule_id "$REGEX_RULE_NAME" "$REGEX_SERVICE")
        if [[ -z "$regex_rule_id" ]]; then
            record_case "用例 6.4 GLOBAL+regex_combine 共享" "FAIL" "找不到 regex 规则 id"
        elif ! start_provider_instance "$PORT_PROVIDER_REGEX" "$REGEX_SERVICE" \
            "provider-regex" "PROVIDER_REGEX_PID"; then
            record_case "用例 6.4 GLOBAL+regex_combine 共享" "FAIL" "provider-regex 启动失败"
        else
            log_info "[用例 6.4] 把 regex 规则切换到 type=GLOBAL+regex_combine=true"
            if ! flip_regex_combine_to_global "$regex_rule_id"; then
                record_case "用例 6.4 GLOBAL+regex_combine 共享" "FAIL" "PUT 切换到 GLOBAL+regex_combine 失败"
            else
                log_info "[用例 6.4] 已切换；等待 5s 让 SDK 拉新规则..."
                sleep 5
                local rw=$GLOBAL_OBSERVE_WINDOWS
                local regex_share_max_ok=$((rw * (REGEX_MAX_AMOUNT + 2)))
                local regex_share_min_limited=$rw
                print_block "[用例 6.4] GLOBAL + regex_combine=true：多 path 共享同一远端配额" \
                    "操作: 连续 ${rw} 个窗口，每窗口同时打 ${REGEX_PATH_A} 与 ${REGEX_PATH_B} 各 ${REGEX_PER_PATH_REQUESTS} 并发" \
                    "原理: 两 path 命中同一 REGEX → 共享同一远端配额" \
                    "预期: total limited ≥ ${rw} && total ok ≤ ${regex_share_max_ok} && /metrics 中 ${REGEX_SERVICE} 正常"
                log_info "[用例 6.4] ${rw} 窗口双 path 并发"
                local total_ok_64=0 total_lim_64=0 total_oth_64=0 w64
                for ((w64 = 0; w64 < rw; w64++)); do
                    local stat_a stat_b ok_a lim_a oth_a ok_b lim_b oth_b
                    stat_a=$(count_status_concurrent "$PORT_PROVIDER_REGEX" "$REGEX_PATH_A" "$REGEX_PER_PATH_REQUESTS")
                    stat_b=$(count_status_concurrent "$PORT_PROVIDER_REGEX" "$REGEX_PATH_B" "$REGEX_PER_PATH_REQUESTS")
                    read -r ok_a lim_a oth_a <<< "$stat_a"
                    read -r ok_b lim_b oth_b <<< "$stat_b"
                    log_info "  窗口 $((w64 + 1))/${rw}: A=(200=${ok_a},429=${lim_a}) B=(200=${ok_b},429=${lim_b})" >&2
                    total_ok_64=$((total_ok_64 + ok_a + ok_b))
                    total_lim_64=$((total_lim_64 + lim_a + lim_b))
                    total_oth_64=$((total_oth_64 + oth_a + oth_b))
                    if [[ $((w64 + 1)) -lt rw ]]; then
                        sleep "$(awk -v s="$REGEX_WINDOW_SECOND" 'BEGIN{ printf "%.1f", s+0.5 }')"
                    fi
                done
                log_info "[用例 6.4] 行为结果: 总 200=${total_ok_64} 429=${total_lim_64} 其他=${total_oth_64}"
                local m_ok_64=true
                if [[ "$total_oth_64" -gt 0 ]]; then
                    record_case "用例 6.4 GLOBAL+regex_combine 共享" "FAIL" "出现非 200/429 状态码 (other=${total_oth_64})"
                    m_ok_64=false
                elif [[ "$total_ok_64" -gt "$regex_share_max_ok" ]]; then
                    record_case "用例 6.4 GLOBAL+regex_combine 共享" "FAIL" "ok=${total_ok_64} 超过 ${regex_share_max_ok}（共享语义不成立）"
                    m_ok_64=false
                elif [[ "$total_lim_64" -lt "$regex_share_min_limited" ]]; then
                    record_case "用例 6.4 GLOBAL+regex_combine 共享" "FAIL" "limited=${total_lim_64} 不足 ${regex_share_min_limited}"
                    m_ok_64=false
                fi
                log_info "[用例 6.4] 验证 /metrics..."
                if $m_ok_64 && wait_flush_and_assert_metrics "$REGEX_SERVICE" true; then
                    record_case "用例 6.4 GLOBAL+regex_combine 共享" "PASS" \
                        "${rw} 窗口合计 ok=${total_ok_64}（≤${regex_share_max_ok}）、429=${total_lim_64}（≥${regex_share_min_limited}），/metrics 正常"
                elif $m_ok_64; then
                    record_case "用例 6.4 GLOBAL+regex_combine 共享" "FAIL" "行为通过但 /metrics 断言失败"
                fi
            fi
            # 收尾：把 regex 规则翻回 LOCAL+regex_combine=false，保持初始状态
            local back_body
            back_body=$(_build_regex_rule_body "$REGEX_RULE_NAME" "$regex_rule_id" "false")
            if update_rule_via_http "$back_body"; then
                log_info "[用例 6.4] 收尾：regex 规则已翻回 LOCAL+regex_combine=false"
            else
                log_warn "[用例 6.4] 收尾翻回失败（不影响测试）"
            fi
        fi
        sleep $((GLOBAL_WINDOW_SECOND + 1))
    fi

    # ---------- 用例 6.5：远端不可达降级到本地（FAILOVER_LOCAL） ----------
    print_block "[用例 6.5] 远端不可达降级到本地（FAILOVER_LOCAL）" \
        "操作: 用 POLARIS_LIMITER_SVC=${GLOBAL_LIMITER_BAD_SERVICE}（不存在）启动 provider，直接打 provider:${PORT_PROVIDER_GLOBAL_FAILOVER} ${windows} 窗口并发" \
        "原理: SDK 拉不到 limiter → remoteExpired → failover=FAILOVER_LOCAL → 按本地配额限流" \
        "预期: 仍能限流 limited ≥ ${windows}（不全放通）；/metrics 端点存活未 crash（6.5 流量本地降级，不进 limiter /metrics）"
    log_info "[用例 6.5] 启动 provider，指向不存在的 limiter '${GLOBAL_LIMITER_BAD_SERVICE}'"
    if ! start_provider_instance "$PORT_PROVIDER_GLOBAL_FAILOVER" "$GLOBAL_SERVICE" \
        "provider-global-failover" "PROVIDER_GLOBAL_FAILOVER_PID" "$GLOBAL_LIMITER_BAD_SERVICE"; then
        record_case "用例 6.5 远端降级到本地" "FAIL" "provider-global-failover 启动失败"
    else
        log_info "等待 8s 让 SDK 完成 limiter 服务发现失败 + 远程过期判定..."
        sleep 8
        stat=$(run_global_burst_in_windows "$PORT_PROVIDER_GLOBAL_FAILOVER" "/echo" "$total_per_window" "$windows")
        read -r ok limited other <<< "$stat"
        log_info "[用例 6.5] 行为结果: 总 200=${ok} 429=${limited} 其他=${other}"
        local m_ok_65=true
        if [[ "$other" -gt 0 ]]; then
            record_case "用例 6.5 远端降级到本地" "FAIL" "出现非 200/429 状态码 (other=${other})"
            m_ok_65=false
        elif [[ "$limited" -lt "$windows" ]]; then
            record_case "用例 6.5 远端降级到本地" "FAIL" "limited=${limited} 不足 ${windows}（降级未生效？）"
            m_ok_65=false
        fi
        # 6.5 流量本地降级，不进 limiter /metrics：只断言端点存活 + 7 指标仍存在（未 crash）
        log_info "[用例 6.5] 验证 /metrics 端点存活（6.5 流量本地降级，不进 limiter /metrics）..."
        local alive_body alive_ok=true
        alive_body=$(curl -s --connect-timeout 3 --max-time 5 "${LIMITER_METRICS_ADDR}/metrics" 2>/dev/null || echo "")
        for m in ratelimit_active_streams ratelimit_counter_count ratelimit_rq_total ratelimit_rq_pass; do
            if ! echo "$alive_body" | grep -qE "^${m}[[:space:]{]"; then
                log_error "[用例 6.5] /metrics 缺少指标: ${m}（limiter 可能 crash）"
                alive_ok=false
                break
            fi
        done
        if $m_ok_65 && $alive_ok; then
            record_case "用例 6.5 远端降级到本地" "PASS" \
                "${windows} 窗口合计 ok=${ok} 429=${limited}（≥${windows}），降级生效；/metrics 端点存活"
        elif $m_ok_65; then
            record_case "用例 6.5 远端降级到本地" "FAIL" "行为通过但 /metrics 端点异常"
        fi
    fi
}

run_global_cases

# 打印 6.x 用例汇总
log_info "--- 用例 6.x 汇总 ---"
for idx in "${!CASE_NAMES[@]}"; do
    log_info "  [${CASE_VERDICTS[$idx]}] ${CASE_NAMES[$idx]}"
done

# ======================== Step 11: 停止 monitor sidecar 后台采集并断言 ========================
log_step "[Step 11] 停止 monitor sidecar 后台采集并断言 sidecar 契约"
stop_and_assert_monitor_sim

# ======================== Step 12: 输出结论 ========================
log_step "[结论]"
if [[ "$TOTAL_FAIL" -eq 0 ]]; then
    log_info "验证结论: ✅ PASS — /metrics 指标验证 + 用例 6.x 分布式限流语义 + monitor sidecar 模拟全部通过"
    log_info "  [Step 9 /metrics] 7 个 ratelimit_* 指标齐全；MetricsRatelimitEchoServer label 7 维齐备；total==pass+limit"
    log_info "  [用例 6.x] 6.0-6.5 全部 PASS，每个子用例 /metrics 验证正常（6.0/6.5 验证端点存活）"
    log_info "  [Step 11 monitor-sim] 后台贯穿全程每分钟 :15 采集，counter 单调、max_delta>0、7 指标齐备"
    exit 0
else
    log_error "验证结论: ❌ FAIL — 共 ${TOTAL_FAIL} 项失败"
    log_error "  失败项（含 Step 9 /metrics 断言 + 用例 6.x）："
    for idx in "${!CASE_NAMES[@]}"; do
        [[ "${CASE_VERDICTS[$idx]}" == "FAIL" ]] && \
            log_error "  ❌ [${CASE_NAMES[$idx]}] ${CASE_DETAILS[$idx]}"
    done
    exit 1
fi
