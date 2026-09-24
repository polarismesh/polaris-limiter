#!/usr/bin/env bash
# 把 run.sh 产生的所有运行日志（默认 .logs/）打成一个包，便于从云端测试机拷回本地。
# 每次 run.sh 结束已自动打出单次运行包；本脚本用于一次性带走多次运行结果。
#
# 用法:
#   ./pack-logs.sh                          # 生成 io-hang-logs-<时间戳>.zip（无 zip 时为 .tar.gz）
#   ./pack-logs.sh /root/my-logs.zip        # 指定输出路径
#   ./pack-logs.sh --log-dir /data/io-hang  # 与 run.sh --log-dir 保持一致
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "${SCRIPT_DIR}/lib.sh"

parse_common_args "$@"
if [[ "$SHOW_HELP" -eq 1 ]]; then
    sed -n '2,8p' "$0" | sed 's/^# \{0,1\}//'
    exit 0
fi

base="$(default_log_base)"
[[ -d "$base" ]] || die "日志目录不存在: $base（尚未执行 run.sh，或 --log-dir 不一致）"
base="$(cd "$base" && pwd)"

entries=()
for d in "$base"/*/; do
    [[ -d "$d" ]] && entries+=("$(basename "$d")")
done
[[ ${#entries[@]} -gt 0 ]] || die "日志目录为空: $base"

ext="$(archive_ext)"
out="${EXTRA_ARGS[0]:-${SCRIPT_DIR}/io-hang-logs-$(date +%Y%m%d_%H%M%S).${ext}}"
out="${out%.zip}"
out="${out%.tar.gz}.${ext}"
mkdir -p "$(dirname "$out")"
out="$(cd "$(dirname "$out")" && pwd)/$(basename "$out")"

pack_archive "$out" "$base" "${entries[@]}"
log_ok "日志包: ${out} ($(du -h "$out" | cut -f1))，共 ${#entries[@]} 次运行"
echo "传回本地: scp root@<测试机地址>:${out} ."
