#!/usr/bin/env bash
# 将探测文件换成 FIFO，使后续 OpenFile/Sync 阻塞（模拟 IOHang）
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "${SCRIPT_DIR}/lib.sh"

parse_common_args "$@"
if [[ "$SHOW_HELP" -eq 1 ]]; then
    cat <<EOF
用法: $0 --namespace NS [--pod POD]

在容器内:
  mv polaris-limiter-probe.log polaris-limiter-probe.log.bak
  mkfifo polaris-limiter-probe.log

下一次探测的 OpenFile 会阻塞在 FIFO 上，/readiness 在 timeout 后变 503。
只对一个 Pod 操作，不要全量注入。
EOF
    exit 0
fi

resolve_pod
log_step "注入 FIFO hang → $NAMESPACE/$POD $PROBE_PATH"

pod_exec sh -c "
set -e
p='${PROBE_PATH}'
if [ -p \"\$p\" ]; then
  echo 'already-fifo'
  exit 0
fi
dir=\$(dirname \"\$p\")
mkdir -p \"\$dir\"
if [ -f \"\$p\" ]; then
  mv \"\$p\" \"\${p}.bak\"
fi
mkfifo \"\$p\"
ls -l \"\$p\" \"\${p}.bak\" 2>/dev/null || ls -l \"\$p\"
echo injected
"

log_ok "已注入。等待 3～8s 后 /readiness 应变 503。"
log_info "观察: ${SCRIPT_DIR}/watch.sh --namespace $NAMESPACE --pod $POD"
log_info "恢复: ${SCRIPT_DIR}/recover.sh --namespace $NAMESPACE --pod $POD"
exit 0
