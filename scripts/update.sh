#!/usr/bin/env sh
# 重建全部列表；服务会在 30s 内自动热加载新 manifest。
#
# 默认离线：只读 data/raw 里的本地副本，不联网。
#   scripts/update.sh            从本地规则源重建 data/dist
#   scripts/update.sh --online   先联网更新 data/raw，再重建
set -e
cd "$(dirname "$0")/.."

MODE=auto
if [ "${1:-}" = "--online" ] || [ "${1:-}" = "--update" ]; then
    MODE=always
    echo "联网更新全部规则源："
else
    echo "离线重建（本地规则源）："
fi

./bin/builder -prepare "$MODE" -config config/sources.json -out data/dist -basedir .

echo "重建完成，服务会自动热加载。"
echo "定时任务示例（每天 04:00 联网更新）："
echo "  0 4 * * * /opt/esp32adlist/scripts/update.sh --online >> /var/log/esp32adlist.log 2>&1"
