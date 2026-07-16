#!/bin/bash
# =============================================================================
# clean-p.sh — provider 节点清理脚本（在 eee2 / eee3 各自运行）
#
# 清理内容（默认）:
#   1. 终止本机 provider 进程（枚举 provider-*.pid，再 ps 兜底匹配 x86-bin）
#   2. 删除本地产物: provider-*.pid / provider-*.log / polaris/(SDK 日志目录)
#
# 可选清理（需显式开启，默认不动 polaris）:
#   --instances   注销本节点在 polaris 上的残留实例（默认遍历 service-1/service-2，
#                 仅删 host=本机出口IP 的实例，不影响 eee2/eee3 另一节点）。
#                 用于进程被 -9 杀掉、实例未及时下线的场景。
#
# 参考: test/integration/cleanup.sh 的交互风格（-f/--force, --dry-run, 确认提示,
#        SIGTERM→SIGKILL）。与上游 cleanup.sh 一致：不删限流规则（规则复用）。
#
# 用法:
#   ./clean-p.sh                 # 默认: 展示后确认再清理进程+目录
#   ./clean-p.sh -f              # 强制: 直接清理，不需确认
#   ./clean-p.sh --dry-run       # 仅展示，不执行
#   ./clean-p.sh --instances     # 额外注销本节点 polaris 残留实例
#   POLARIS_TOKEN=xxx ./clean-p.sh --instances   # polaris 开启鉴权时
# =============================================================================
set -uo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

FORCE=false
DRY_RUN=false
CLEAN_INSTANCES=false

# 与 provider.sh 保持一致的默认值
POLARIS_SERVER="${POLARIS_SERVER:-172.16.0.5}"
POLARIS_TOKEN="${POLARIS_TOKEN:-}"
NAMESPACE="${NAMESPACE:-default}"
# 多服务：--instances 默认遍历两服务注销；--service 退化为单服务。
SERVICES=("GlobalRatelimitEchoServer-1" "GlobalRatelimitEchoServer-2")
SERVICE=""
PORT=18200

while [[ $# -gt 0 ]]; do
    case "$1" in
        -f|--force)     FORCE=true;          shift ;;
        --dry-run)      DRY_RUN=true;         shift ;;
        --instances)    CLEAN_INSTANCES=true; shift ;;
        --service)      SERVICE="$2"; SERVICES=("$2"); shift 2 ;;   # 退化为单服务
        --services)     IFS=',' read -ra SERVICES <<< "$2"; shift 2 ;;
        --namespace)    NAMESPACE="$2";       shift 2 ;;
        --port)         PORT="$2";            shift 2 ;;
        --polaris-server) POLARIS_SERVER="$2"; shift 2 ;;
        --polaris-token)  POLARIS_TOKEN="$2";  shift 2 ;;
        -h|--help)
            cat <<EOF
用法: $0 [选项]

清理本机 provider 进程 + 本地产物（provider-*.pid / provider-*.log / polaris/）。

选项:
  -f, --force            直接清理，不需确认
  --dry-run              仅展示匹配的进程和目录，不执行清理
  --instances            额外注销本节点 polaris 残留实例（默认遍历 service-1/service-2）
  --service <name>       业务服务名（退化为单服务模式）
  --services <a,b>       业务服务名列表 (默认 GlobalRatelimitEchoServer-1,GlobalRatelimitEchoServer-2)
  --namespace <ns>       命名空间 (默认 default)
  --port <port>          provider 端口 (默认 18200)
  --polaris-server <addr> polaris-server 地址 (默认 172.16.0.5)
  --polaris-token <token>  polaris-server 鉴权 token
  -h, --help             展示帮助

不清理:
  - polaris-server 上的限流规则（规则复用，由 consumer 侧 clean-c.sh --rule 管理）
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

echo ""
echo -e "${CYAN}========================================${NC}"
echo -e "${CYAN}  provider 节点清理工具 (clean-p.sh)${NC}"
echo -e "${CYAN}========================================${NC}"
echo ""

# ======================== 收集 provider 进程 ========================
# 先读 pidfile；再用 ps 兜底匹配本目录的 x86-bin（pidfile 可能已被删 / 进程被 -9）
declare -a PIDS=()
declare -a DESCS=()

add_pid() {
    local pid="$1" desc="$2"
    [[ -z "$pid" ]] && return
    for p in "${PIDS[@]+"${PIDS[@]}"}"; do [[ "$p" == "$pid" ]] && return; done
    PIDS+=("$pid"); DESCS+=("$desc")
}

# 枚举所有按服务区分的 pidfile（provider-<service>.pid）+ 兼容旧 provider.pid
shopt -s nullglob
for pf in "${SCRIPT_DIR}"/provider-*.pid "${SCRIPT_DIR}"/provider.pid; do
    [[ -f "$pf" ]] || continue
    pidfile_pid=$(cat "$pf" 2>/dev/null || true)
    if [[ -n "$pidfile_pid" ]] && kill -0 "$pidfile_pid" 2>/dev/null; then
        add_pid "$pidfile_pid" "via pidfile ${pf##*/}"
    fi
done
shopt -u nullglob
# ps 兜底：grep -F 固定串匹配本目录 x86-bin，grep -v grep 排除自身
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
    echo -e "${GREEN}未发现 provider 残留进程，无需清理。${NC}"
else
    echo -e "${YELLOW}发现 ${#PIDS[@]} 个 provider 进程:${NC}"
    for i in "${!PIDS[@]}"; do
        echo -e "  PID ${PIDS[$i]}  ${CYAN}(${DESCS[$i]})${NC}"
    done
    echo ""
    if [[ "$DRY_RUN" == true ]]; then
        echo -e "${YELLOW}[dry-run] 仅展示，未清理进程。${NC}"
    elif [[ "$FORCE" == true ]]; then
        kill_pids
    else
        read -r -p "确认清理以上 provider 进程? [y/N] " response
        case "$response" in
            [yY]|[yY][eE][sS]) kill_pids ;;
            *) echo -e "${YELLOW}跳过进程清理。${NC}" ;;
        esac
    fi
fi

# ======================== 清理本地目录/文件 ========================
echo ""
declare -a FILES=()
# 枚举所有按服务区分的 pidfile/logfile（provider-<service>.*）+ 兼容旧 provider.pid/provider.log
shopt -s nullglob
for f in "${SCRIPT_DIR}"/provider-*.pid "${SCRIPT_DIR}"/provider.pid; do
    [[ -f "$f" ]] && FILES+=("${f##*/}")
done
for f in "${SCRIPT_DIR}"/provider-*.log "${SCRIPT_DIR}"/provider.log; do
    [[ -f "$f" ]] && FILES+=("${f##*/}")
done
shopt -u nullglob
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

# ======================== 可选: 注销本节点 polaris 残留实例 ========================
if [[ "$CLEAN_INSTANCES" == true ]]; then
    echo ""
    echo -e "${CYAN}--- 注销本节点 polaris 残留实例（遍历 ${SERVICES[*]}）---${NC}"
    # 探测本机出口 IP（与 provider.sh 一致：dial polaris-server）
    local_ip=$(python3 - "$POLARIS_SERVER" <<'PY' 2>/dev/null
import socket, sys
try:
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM); s.settimeout(3)
    s.connect((sys.argv[1], 8091)); print(s.getsockname()[0]); s.close()
except Exception:
    pass
PY
)
    if [[ -z "$local_ip" ]]; then
        log_error "无法探测本机出口 IP（python3 缺失或网络不通），跳过实例注销"
    else
        log_info "本机出口 IP: ${local_ip}"
        if [[ "$DRY_RUN" == true ]]; then
            echo -e "${YELLOW}[dry-run] 仅展示，未执行注销。${NC}"
        else
            for svc in "${SERVICES[@]}"; do
                echo -e "${CYAN}-- ${NAMESPACE}/${svc} 下 host=${local_ip} --${NC}"
                resp=$(curl -s --connect-timeout 5 --max-time 10 \
                    "${POLARIS_HTTP_ADDR}/naming/v1/instances?service=${svc}&namespace=${NAMESPACE}&limit=200" \
                    --header "X-Polaris-Token:${POLARIS_TOKEN}" 2>/dev/null || echo "")
                host_port_list=$(echo "$resp" | HOST="$local_ip" python3 -c "
import sys, json, os
host = os.environ['HOST']
try:
    data = json.load(sys.stdin)
    for i in data.get('instances', []):
        if i.get('host','') == host:
            print('%s %s' % (i.get('host',''), i.get('port','')))
except Exception:
    pass
" 2>/dev/null || true)
                if [[ -z "$host_port_list" ]]; then
                    log_info "polaris 中无 ${svc} 下 host=${local_ip} 的实例，无需注销"
                    continue
                fi
                deleted=0 failed=0
                while IFS= read -r line; do
                    [[ -z "$line" ]] && continue
                    h="${line% *}"; p="${line#* }"
                    http_code=$(curl -s -o /dev/null -w '%{http_code}' \
                        --connect-timeout 5 --max-time 10 \
                        --request DELETE \
                        "${POLARIS_HTTP_ADDR}/naming/v1/instances?service=${svc}&namespace=${NAMESPACE}&host=${h}&port=${p}" \
                        --header "X-Polaris-Token:${POLARIS_TOKEN}" 2>/dev/null)
                    if [[ "$http_code" == "200" ]]; then
                        echo -e "  ${GREEN}[OK]${NC} 注销实例 ${svc} ${h}:${p} (HTTP 200)"; deleted=$((deleted + 1))
                    else
                        echo -e "  ${RED}[X]${NC} 注销实例 ${svc} ${h}:${p} 失败 (HTTP ${http_code})"; failed=$((failed + 1))
                    fi
                done <<< "$host_port_list"
                echo -e "  ${GREEN}${svc} 注销完成:${NC} 成功 ${deleted}"\
                    "$( [[ $failed -gt 0 ]] && echo -e ", ${RED}失败 ${failed}${NC}" )"
            done
        fi
    fi
fi

echo ""
if [[ "$DRY_RUN" == true ]]; then
    echo -e "${YELLOW}[dry-run] 全部为展示结果，未执行任何清理。${NC}"
else
    echo -e "${GREEN}provider 节点清理完成。${NC}"
fi
