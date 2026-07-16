#!/bin/bash
# =============================================================================
# clean-c.sh — consumer 节点清理脚本（在 eee1 运行）
#
# 清理内容（默认）:
#   1. 终止本机 consumer 进程（ps 兜底匹配 x86-bin；consumer.sh 用 nohup 启动，
#      --keep 模式下进程会残留）
#   2. 删除本地产物: .logs/(验证日志 + metrics_snapshot) / consumer.log / polaris/(SDK 日志)
#
# 可选清理（需显式开启，默认不动 polaris）:
#   --rule   删除 polaris-server 上的测试限流规则（默认遍历 service-1/service-2，每服务删
#            /echo + agg1-4 共 5 条；--rule-name 指定时只删一条）
#            （consumer.sh 创建的 GLOBAL 规则；默认复用，需彻底重置时开启）
#
# 参考: test/integration/cleanup.sh 的交互风格（-f/--force, --dry-run, 确认提示,
#        SIGTERM→SIGKILL）。与上游 cleanup.sh 一致：默认不删限流规则（规则复用）。
#
# 用法:
#   ./clean-c.sh                # 默认: 展示后确认再清理进程+目录
#   ./clean-c.sh -f             # 强制: 直接清理，不需确认
#   ./clean-c.sh --dry-run      # 仅展示，不执行
#   ./clean-c.sh --rule         # 额外删除 polaris 限流规则
#   POLARIS_TOKEN=xxx ./clean-c.sh --rule   # polaris 开启鉴权时
# =============================================================================
set -uo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

FORCE=false
DRY_RUN=false
CLEAN_RULE=false

# 与 consumer.sh 保持一致的默认值
POLARIS_SERVER="${POLARIS_SERVER:-172.16.0.5}"
POLARIS_TOKEN="${POLARIS_TOKEN:-}"
NAMESPACE="${NAMESPACE:-default}"
# 多服务：--rule 默认遍历两服务派生规则名删除；--service / --rule-name 可退化为单条。
SERVICES=("GlobalRatelimitEchoServer-1" "GlobalRatelimitEchoServer-2")
# 单值兼容（--service 覆盖时退化为单服务模式）
SERVICE=""
RULE_NAME=""

while [[ $# -gt 0 ]]; do
    case "$1" in
        -f|--force)     FORCE=true;        shift ;;
        --dry-run)      DRY_RUN=true;       shift ;;
        --rule)         CLEAN_RULE=true;    shift ;;
        --service)      SERVICE="$2"; SERVICES=("$2"); shift 2 ;;   # 退化为单服务
        --services)     IFS=',' read -ra SERVICES <<< "$2"; shift 2 ;;
        --namespace)    NAMESPACE="$2";     shift 2 ;;
        --rule-name)    RULE_NAME="$2";     shift 2 ;;              # 显式指定时只删这一条
        --polaris-server) POLARIS_SERVER="$2"; shift 2 ;;
        --polaris-token)  POLARIS_TOKEN="$2";  shift 2 ;;
        -h|--help)
            cat <<EOF
用法: $0 [选项]

清理本机 consumer 进程 + 本地产物（.logs/ / consumer.log / polaris/）。

选项:
  -f, --force            直接清理，不需确认
  --dry-run              仅展示匹配的进程和目录，不执行清理
  --rule                 额外删除 polaris 限流规则（默认遍历 service-1/service-2，每服务删 /echo+agg1-4 共 5 条）
  --service <name>       业务服务名（退化为单服务模式，只删对应规则）
  --services <a,b>       业务服务名列表 (默认 GlobalRatelimitEchoServer-1,GlobalRatelimitEchoServer-2)
  --namespace <ns>       命名空间 (默认 default)
  --rule-name <name>     显式规则名（指定时只删这一条，不遍历服务）
  --polaris-server <addr> polaris-server 地址 (默认 172.16.0.5)
  --polaris-token <token>  polaris-server 鉴权 token
  -h, --help             展示帮助

不清理:
  - provider 实例（由 eee2/eee3 的 clean-p.sh --instances 管理）
EOF
            exit 0
            ;;
        *) echo -e "${RED}未知参数: $1${NC}"; exit 1 ;;
    esac
done

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
POLARIS_HTTP_ADDR="http://${POLARIS_SERVER}:8090"

log_info()  { echo -e "${GREEN}[INFO]${NC} $*"; }
log_warn()   { echo -e "${YELLOW}[WARN]${NC} $*"; }
log_error()  { echo -e "${RED}[ERROR]${NC} $*"; }

# 由服务名派生限流规则名（与 consumer.sh rule_name_for_service 一致）。
rule_name_for_service() {
    local svc="$1" suffix
    suffix="${svc##*-}"
    if [[ "$suffix" =~ ^[0-9]+$ ]]; then
        echo "ratelimit-cloud-global-rule-${suffix}"
    else
        echo "ratelimit-cloud-global-rule-$(echo "$svc" | tr '[:upper:]' '[:lower:]')"
    fi
}

# 输出某服务的所有规则名：/echo 规则 + 4 条 agg 规则（与 consumer.sh create_agg_rules 一致）。
# --rule 默认遍历每服务删这 5 条。
agg_rule_names_for_service() {
    local svc="$1" suffix m
    suffix="${svc##*-}"
    rule_name_for_service "$svc"                       # /echo 规则
    for m in 1 2 3 4; do
        echo "ratelimit-cloud-global-rule-${suffix}-agg${m}"
    done
}

# delete_one_rule <rule_name> <service>：查规则 id 后 DELETE（带 body 兜底）。
delete_one_rule() {
    local rule_name="$1" service="$2"
    local resp rule_id http_code http_code2
    resp=$(curl -s --connect-timeout 5 --max-time 10 \
        "${POLARIS_HTTP_ADDR}/naming/v1/ratelimits?name=${rule_name}&service=${service}&namespace=${NAMESPACE}&limit=50" \
        --header "X-Polaris-Token:${POLARIS_TOKEN}" 2>/dev/null || echo "")
    rule_id=$(echo "$resp" | SVC="$service" RULE="$rule_name" python3 -c "
import sys, json, os
try:
    data = json.load(sys.stdin)
    for r in data.get('rateLimits', []):
        if r.get('name','')==os.environ['RULE'] and r.get('service','')==os.environ['SVC']:
            print(r.get('id','')); break
except Exception:
    pass
" 2>/dev/null || true)
    if [[ -z "$rule_id" ]]; then
        log_info "polaris 中无规则 [${rule_name}] (service=${service})，无需删除"
        return 0
    fi
    log_info "规则 [${rule_name}] id=${rule_id}，执行 DELETE..."
    http_code=$(curl -s -o /dev/null -w '%{http_code}' \
        --connect-timeout 5 --max-time 10 \
        --request DELETE \
        "${POLARIS_HTTP_ADDR}/naming/v1/ratelimits?id=${rule_id}" \
        --header "X-Polaris-Token:${POLARIS_TOKEN}" 2>/dev/null)
    if [[ "$http_code" == "200" ]]; then
        echo -e "  ${GREEN}[OK]${NC} 已删除规则 [${rule_name}] (HTTP 200)"
        return 0
    fi
    # 兜底：部分 polaris 版本 DELETE 需要 JSON body
    http_code2=$(curl -s -o /dev/null -w '%{http_code}' \
        --connect-timeout 5 --max-time 10 \
        --request DELETE "${POLARIS_HTTP_ADDR}/naming/v1/ratelimits" \
        --header "X-Polaris-Token:${POLARIS_TOKEN}" \
        --header 'Content-Type: application/json' \
        --data "[{\"id\":\"${rule_id}\",\"service\":\"${service}\",\"namespace\":\"${NAMESPACE}\"}]" 2>/dev/null)
    if [[ "$http_code2" == "200" ]]; then
        echo -e "  ${GREEN}[OK]${NC} 已删除规则 [${rule_name}] (HTTP 200, via body)"
        return 0
    fi
    echo -e "  ${RED}[X]${NC} 删除规则 [${rule_name}] 失败 (HTTP ${http_code} / body ${http_code2})；可去 polaris 控制台手动删除"
    return 1
}

echo ""
echo -e "${CYAN}========================================${NC}"
echo -e "${CYAN}  consumer 节点清理工具 (clean-c.sh)${NC}"
echo -e "${CYAN}========================================${NC}"
echo ""

# ======================== 收集 consumer 进程 ========================
declare -a PIDS=()
declare -a DESCS=()

add_pid() {
    local pid="$1" desc="$2"
    [[ -z "$pid" ]] && return
    for p in "${PIDS[@]+"${PIDS[@]}"}"; do [[ "$p" == "$pid" ]] && return; done
    PIDS+=("$pid"); DESCS+=("$desc")
}

# consumer.sh 用 nohup 启动且无 pidfile；用 ps 匹配本目录 x86-bin（grep -F 固定串）
while IFS= read -r line; do
    [[ -z "$line" ]] && continue
    pid=$(echo "$line" | awk '{print $1}')
    [[ -n "$pid" ]] && add_pid "$pid" "via ps (x86-bin)"
done < <(ps -eo pid,args | grep -F "${SCRIPT_DIR}/x86-bin" | grep -v grep)

# ======================== 展示并清理进程 ========================
kill_pids() {
    local killed=0 force_killed=0
    for pid in "${PIDS[@]+"${PIDS[@]}"}"; do
        if kill -0 "$pid" 2>/dev/null; then
            kill "$pid" 2>/dev/null && { echo -e "  ${GREEN}[OK]${NC} SIGTERM PID $pid"; killed=$((killed + 1)); }
        else
            echo -e "  ${YELLOW}-${NC} PID $pid 已不存在，跳过"
        fi
    done
    sleep 1
    for pid in "${PIDS[@]+"${PIDS[@]}"}"; do
        if kill -0 "$pid" 2>/dev/null; then
            echo -e "  ${YELLOW}!${NC} PID $pid 未响应 SIGTERM，SIGKILL..."
            kill -9 "$pid" 2>/dev/null || true
            force_killed=$((force_killed + 1))
        fi
    done
    echo -e "  ${GREEN}进程清理完成:${NC} 终止 ${killed}"\
        "$( [[ $force_killed -gt 0 ]] && echo ", 强杀 ${force_killed}" )"
}

if [[ ${#PIDS[@]} -eq 0 ]]; then
    echo -e "${GREEN}未发现 consumer 残留进程，无需清理。${NC}"
else
    echo -e "${YELLOW}发现 ${#PIDS[@]} 个 consumer 进程:${NC}"
    for i in "${!PIDS[@]}"; do
        echo -e "  PID ${PIDS[$i]}  ${CYAN}(${DESCS[$i]})${NC}"
    done
    echo ""
    if [[ "$DRY_RUN" == true ]]; then
        echo -e "${YELLOW}[dry-run] 仅展示，未清理进程。${NC}"
    elif [[ "$FORCE" == true ]]; then
        kill_pids
    else
        read -r -p "确认清理以上 consumer 进程? [y/N] " response
        case "$response" in
            [yY]|[yY][eE][sS]) kill_pids ;;
            *) echo -e "${YELLOW}跳过进程清理。${NC}" ;;
        esac
    fi
fi

# ======================== 清理本地目录/文件 ========================
echo ""
declare -a FILES=()
[[ -d "${SCRIPT_DIR}/.logs" ]] && FILES+=(".logs/")
[[ -f "${SCRIPT_DIR}/consumer.log" ]] && FILES+=("consumer.log")
[[ -d "${SCRIPT_DIR}/polaris" ]] && FILES+=("polaris/")

if [[ ${#FILES[@]} -eq 0 ]]; then
    echo -e "${GREEN}未发现需要清理的本地文件/目录。${NC}"
else
    echo -e "${YELLOW}发现本地产物:${NC}"
    for f in "${FILES[@]}"; do
        sz=""
        if [[ -d "${SCRIPT_DIR}/${f}" ]]; then
            sz=$(du -sh "${SCRIPT_DIR}/${f}" 2>/dev/null | awk '{print $1}')
        fi
        echo -e "  ${f}  ${sz}"
    done
    if [[ "$DRY_RUN" == true ]]; then
        echo -e "${YELLOW}[dry-run] 仅展示，未清理文件。${NC}"
    elif [[ "$FORCE" == true ]]; then
        for f in "${FILES[@]}"; do rm -rf "${SCRIPT_DIR}/${f}"; echo -e "  ${GREEN}[OK]${NC} 已删除 ${f}"; done
    else
        read -r -p "确认清理以上文件/目录? [y/N] " response
        case "$response" in
            [yY]|[yY][eE][sS])
                for f in "${FILES[@]}"; do rm -rf "${SCRIPT_DIR}/${f}"; echo -e "  ${GREEN}[OK]${NC} 已删除 ${f}"; done
                ;;
            *) echo -e "${YELLOW}跳过文件清理。${NC}" ;;
        esac
    fi
fi

# ======================== 可选: 删除 polaris 限流规则 ========================
if [[ "$CLEAN_RULE" == true ]]; then
    echo ""
    echo -e "${CYAN}--- 删除 polaris 限流规则 ---${NC}"
    if [[ "$DRY_RUN" == true ]]; then
        echo -e "${YELLOW}[dry-run] 仅展示，未执行删除。${NC}"
    else
        if [[ -n "$RULE_NAME" ]]; then
            # 显式规则名：只删这一条（service 取 --service 或服务列表首项）
            delete_one_rule "$RULE_NAME" "${SERVICE:-${SERVICES[0]:-}}"
        else
            # 遍历 SERVICES，每服务删 /echo + agg1-4 共 5 条规则
            for svc in "${SERVICES[@]}"; do
                while IFS= read -r rn; do
                    [[ -n "$rn" ]] && delete_one_rule "$rn" "$svc"
                done < <(agg_rule_names_for_service "$svc")
            done
        fi
    fi
fi

echo ""
if [[ "$DRY_RUN" == true ]]; then
    echo -e "${YELLOW}[dry-run] 全部为展示结果，未执行任何清理。${NC}"
else
    echo -e "${GREEN}consumer 节点清理完成。${NC}"
fi
