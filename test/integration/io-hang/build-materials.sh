#!/usr/bin/env bash
# 生成 IOHang 集群验收物料 dist/io-hang/ 并打包为 dist/io-hang.zip，上传到能 kubectl 访问集群的云端测试机运行。
#
# 用法:
#   ./build-materials.sh           # 生成 dist/io-hang/ + dist/io-hang.zip
#   ./build-materials.sh --clean   # 清理 dist/
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DIST_DIR="${SCRIPT_DIR}/dist"
NODE_NAME="io-hang"
MATERIALS=(README.md lib.sh run.sh check-baseline.sh inject-hang.sh recover.sh watch.sh pack-logs.sh)

GREEN='\033[0;32m'
CYAN='\033[0;36m'
YELLOW='\033[1;33m'
NC='\033[0m'

if [[ "${1:-}" == "--clean" ]]; then
    rm -rf "$DIST_DIR"
    echo -e "${GREEN}已清理 ${DIST_DIR}${NC}"
    exit 0
fi

echo -e "${CYAN}=== 1. 生成物料 ===${NC}"
rm -rf "$DIST_DIR"
mkdir -p "${DIST_DIR}/${NODE_NAME}"
for f in "${MATERIALS[@]}"; do
    cp "${SCRIPT_DIR}/${f}" "${DIST_DIR}/${NODE_NAME}/${f}"
done
chmod +x "${DIST_DIR}/${NODE_NAME}"/*.sh
ls -1 "${DIST_DIR}/${NODE_NAME}" | sed 's/^/    /'

echo -e "${CYAN}=== 2. 打包 ===${NC}"
if command -v zip >/dev/null 2>&1; then
    pkg="${DIST_DIR}/${NODE_NAME}.zip"
    (cd "$DIST_DIR" && zip -rq "$pkg" "$NODE_NAME")
    unpack="unzip ${NODE_NAME}.zip"
else
    pkg="${DIST_DIR}/${NODE_NAME}.tar.gz"
    tar -czf "$pkg" -C "$DIST_DIR" "$NODE_NAME"
    unpack="tar xzf ${NODE_NAME}.tar.gz"
    echo -e "  ${YELLOW}zip 命令不可用，已改为 tar.gz${NC}"
fi
echo -e "  ${GREEN}${pkg}${NC} ($(du -h "$pkg" | cut -f1))"

cat <<EOF

上传到云端测试机后:
  ${unpack} && cd ${NODE_NAME}

  # 完整验收（日志写入 ./.logs/<时间>-<pod>/，结束自动打出同名 zip）
  ./run.sh --namespace <ns> --pod <limiter-pod>

  # 汇总打包所有运行日志，按提示 scp 回本地
  ./pack-logs.sh
EOF
