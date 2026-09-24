#!/usr/bin/env bash
# 解开 FIFO，恢复探测文件，唤醒可能堵在 OpenFile 上的 goroutine
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "${SCRIPT_DIR}/lib.sh"

parse_common_args "$@"
if [[ "$SHOW_HELP" -eq 1 ]]; then
    cat <<EOF
用法: $0 --namespace NS [--pod POD]
EOF
    exit 0
fi

resolve_pod
log_step "恢复探测文件 $NAMESPACE/$POD $PROBE_PATH"

# 先短读 FIFO，让阻塞的 O_WRONLY open 返回；再删 FIFO、还原 .bak
pod_exec sh -c "
p='${PROBE_PATH}'
if [ -p \"\$p\" ]; then
  cat \"\$p\" >/dev/null 2>&1 &
  cpid=\$!
  sleep 1
  kill \$cpid 2>/dev/null || true
  wait \$cpid 2>/dev/null || true
  rm -f \"\$p\"
fi
if [ -f \"\${p}.bak\" ]; then
  mv -f \"\${p}.bak\" \"\$p\"
fi
# 若 bak 不存在，下次探测会新建普通文件
ls -l \"\$p\" 2>/dev/null || echo 'probe file will be recreated on next interval'
echo recovered
"

log_ok "已恢复。等待一个 interval（默认 5s）后 /readiness 应回到 200。"
exit 0
