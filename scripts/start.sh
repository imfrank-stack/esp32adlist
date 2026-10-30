#!/usr/bin/env bash
# ESP32 广告过滤规则 API —— Linux 一键构建 + 启动
# 自动准备 Go（缺失时下载便携版到 .tools/go）、编译、首次拉规则、启动服务。
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TOOLS="$ROOT/.tools"
LOCAL_GO="$TOOLS/go/bin/go"
BIN="$ROOT/bin"
GO_VERSION="${GO_VERSION:-1.24.0}"
PORT=8080
REFRESH=0
UPDATE=0
DAEMON=0
STOP=0
FORCE_INSTALL=0

if [ -t 1 ]; then
  C_OK=$'\033[32m'; C_WARN=$'\033[33m'; C_ERR=$'\033[31m'; C_STEP=$'\033[36m'; C_DIM=$'\033[90m'; C_END=$'\033[0m'
else
  C_OK=''; C_WARN=''; C_ERR=''; C_STEP=''; C_DIM=''; C_END=''
fi

step() { printf '\n%s==> %s%s\n' "$C_STEP" "$1" "$C_END"; }
ok()   { printf '    %s%s%s\n' "$C_OK" "$1" "$C_END"; }
warn() { printf '    %s%s%s\n' "$C_WARN" "$1" "$C_END"; }
dim()  { printf '    %s%s%s\n' "$C_DIM" "$1" "$C_END"; }
die()  { printf '\n%s[错误] %s%s\n' "$C_ERR" "$1" "$C_END"; exit 1; }

usage() {
  cat <<'EOF'
用法: scripts/start.sh [选项]

  --port N         监听端口（默认 8080）
  --refresh        从本地规则源重建 data/dist（不联网）
  --update         联网更新全部规则源并重建（默认本地优先）
  --daemon, -d     后台运行（日志 logs/server.log，PID run/server.pid）
  --stop           停止后台实例
  --install-go     强制下载便携 Go 到 .tools/go
  --go-version V   指定 Go 版本（默认 1.24.0，也可用环境变量 GO_VERSION）
  -h, --help       显示帮助

示例:
  ./scripts/start.sh                 # 前台运行
  ./scripts/start.sh -d --port 9090  # 后台运行在 9090
  ./scripts/start.sh --refresh -d    # 本地规则源更新后重建
  ./scripts/start.sh --update        # 联网拉取最新规则源
  ./scripts/start.sh --stop
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --port)        PORT="${2:?缺少端口}"; shift 2 ;;
    --refresh)     REFRESH=1; shift ;;
    --update)      UPDATE=1; shift ;;
    --daemon|-d)   DAEMON=1; shift ;;
    --stop)        STOP=1; shift ;;
    --install-go)  FORCE_INSTALL=1; shift ;;
    --go-version)  GO_VERSION="${2:?缺少版本}"; shift 2 ;;
    -h|--help)     usage; exit 0 ;;
    *) echo "未知参数: $1"; usage; exit 1 ;;
  esac
done

PIDFILE="$ROOT/run/server.pid"

if [ "$STOP" = 1 ]; then
  if [ -f "$PIDFILE" ]; then
    pid="$(cat "$PIDFILE")"
    if kill -0 "$pid" 2>/dev/null; then
      kill "$pid" && ok "已停止 server (PID $pid)"
    else
      warn "PID $pid 已不存在"
    fi
    rm -f "$PIDFILE"
  else
    warn "没有 run/server.pid，尝试按进程名结束"
    pkill -f "$BIN/server" 2>/dev/null || true
  fi
  exit 0
fi

printf 'ESP32 规则 API 一键启动\n项目目录: %s\n' "$ROOT"

# ---------- 工具函数 ----------
download() { # download <url> <dest>
  if command -v curl >/dev/null 2>&1; then
    curl -fL --retry 2 --connect-timeout 20 --max-time 600 -o "$2" "$1" 2>/dev/null
  elif command -v wget >/dev/null 2>&1; then
    wget -q -T 30 -O "$2" "$1"
  else
    return 1
  fi
}

go_minor() { # go_minor <go> -> 124 形式
  "$1" version 2>/dev/null | sed -n 's/.*go\([0-9][0-9]*\)\.\([0-9][0-9]*\).*/\1\2/p' | head -1
}

find_go() {
  for c in "$LOCAL_GO" "$(command -v go 2>/dev/null || true)" /usr/local/go/bin/go /usr/lib/go/bin/go; do
    [ -n "$c" ] || continue
    [ -x "$c" ] || continue
    m="$(go_minor "$c" || true)"
    if [ -n "$m" ] && [ "$m" -ge 124 ]; then echo "$c"; return 0; fi
  done
  return 1
}

install_go() {
  ver="${GO_VERSION#go}"; ver="${ver#v}"
  case "$(uname -m)" in
    x86_64|amd64) arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    armv7l|armv6l) arch=armv6l ;;
    *) arch=amd64 ;;
  esac
  file="go${ver}.linux-${arch}.tar.gz"
  mkdir -p "$TOOLS"

  for base in https://mirrors.aliyun.com/golang \
              https://mirrors.ustc.edu.cn/golang \
              https://golang.google.cn/dl \
              https://go.dev/dl; do
    dim "尝试 $base/$file"
    if download "$base/$file" "$TOOLS/$file"; then
      ok "已下载 $file ($(du -h "$TOOLS/$file" | cut -f1))"
      break
    fi
  done
  [ -f "$TOOLS/$file" ] || die "Go 下载失败。可手动下载 $file 解压到 .tools/，或先安装 Go 1.24+ 到 PATH。"

  command -v tar >/dev/null 2>&1 || die "缺少 tar，无法解压"
  rm -rf "$TOOLS/go"
  dim "解压中..."
  tar -xzf "$TOOLS/$file" -C "$TOOLS" || die "解压失败"
  rm -f "$TOOLS/$file"
  [ -x "$LOCAL_GO" ] || die "解压后未找到 .tools/go/bin/go"
  ok "便携 Go 已就绪: $LOCAL_GO"
}

# ---------- Go ----------
step "检查 Go 环境"
GO=""
if [ "$FORCE_INSTALL" = 0 ]; then GO="$(find_go || true)"; fi
if [ -n "$GO" ]; then
  ok "使用已有 Go：$("$GO" version)"
else
  warn "未找到 Go 1.24+，下载便携版（约 80MB）"
  install_go
  GO="$LOCAL_GO"
fi

export GOTOOLCHAIN=local
export GOPROXY="https://goproxy.cn,direct"
export CGO_ENABLED=0

# ---------- 编译 ----------
step "编译"
mkdir -p "$BIN"
(
  cd "$ROOT"
  "$GO" build -trimpath -o "$BIN/builder" ./cmd/builder || exit 1
  "$GO" build -trimpath -o "$BIN/server" ./cmd/server || exit 1
) || die "编译失败"
ok "bin/builder  bin/server"

# ---------- 规则数据 ----------
step "检查规则数据"

# data/raw 里的上游副本随仓库保存，默认只读这些做离线构建，不联网。
RAW_MISSING=""
RAW_TOTAL=0
while IFS= read -r rel; do
  [ -n "$rel" ] || continue
  RAW_TOTAL=$((RAW_TOTAL + 1))
  f="$ROOT/$rel"
  sz=0
  if [ -f "$f" ]; then sz=$(wc -c < "$f" | tr -d ' '); fi
  if [ "$sz" -lt 1024 ]; then RAW_MISSING="$RAW_MISSING $rel"; fi
done < <(sed -n 's/.*"\(data\/raw\/[^"]*\)".*/\1/p' "$ROOT/config/sources.json" | sort -u)

set -- $RAW_MISSING
MISSING_N=$#

if [ "$RAW_TOTAL" -gt 0 ]; then
  if [ "$MISSING_N" -eq 0 ]; then
    ok "本地规则源完整：$RAW_TOTAL/$RAW_TOTAL 个，走离线构建"
  else
    warn "本地规则源缺 $MISSING_N/$RAW_TOTAL 个：$RAW_MISSING"
    warn "将对这些源联网补拉，其余沿用本地副本"
  fi
fi

MODE=auto
[ "$UPDATE" = 1 ] && MODE=always

NEED_BUILD=0
if [ "$UPDATE" = 1 ]; then
  ok "按 --update 重新下载全部规则源"; NEED_BUILD=1
elif [ "$REFRESH" = 1 ]; then
  ok "按 --refresh 从本地规则源重建 data/dist"; NEED_BUILD=1
elif [ ! -f "$ROOT/data/dist/manifest.json" ]; then
  ok "尚无 data/dist/manifest.json，构建一次"; NEED_BUILD=1
elif [ "$MISSING_N" -gt 0 ]; then
  ok "补全缺失规则源后重建"; NEED_BUILD=1
fi

if [ "$NEED_BUILD" = 1 ]; then
  ( cd "$ROOT" && "$BIN/builder" -prepare "$MODE" -config config/sources.json -out data/dist -basedir . ) \
    || die "规则构建失败（检查 config/sources.json 或网络）"
else
  ver="$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$ROOT/data/dist/manifest.json" | head -1)"
  total="$(sed -n 's/.*"total_unique_domains"[[:space:]]*:[[:space:]]*\([0-9]*\).*/\1/p' "$ROOT/data/dist/manifest.json" | head -1)"
  ok "沿用现有 data/dist，版本 ${ver:-unknown}，${total:-0} 个唯一域名（--refresh 重建 / --update 联网更新源）"
fi

# ---------- 启动 ----------
step "启动服务"
URL="http://127.0.0.1:$PORT"

health() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsS --max-time 2 "$URL/healthz" >/dev/null 2>&1
  else
    wget -q -T 2 -O /dev/null "$URL/healthz" 2>/dev/null
  fi
}

if health; then
  ok "服务已在运行：$URL"
  dim "文档页: $URL/"
  exit 0
fi

if [ "$DAEMON" = 1 ]; then
  mkdir -p "$ROOT/logs" "$ROOT/run"
  nohup "$BIN/server" -addr ":$PORT" -dir data/dist >> "$ROOT/logs/server.log" 2>&1 &
  pid=$!
  echo "$pid" > "$PIDFILE"

  for _ in $(seq 1 25); do
    sleep 0.4
    if health; then
      ok "后台启动成功，PID $pid"
      dim "文档页: $URL/"
      dim "日志:   $ROOT/logs/server.log"
      dim "停止:   scripts/start.sh --stop"
      exit 0
    fi
    kill -0 "$pid" 2>/dev/null || break
  done

  warn "启动失败，日志尾部："
  tail -n 15 "$ROOT/logs/server.log" 2>/dev/null | sed 's/^/    /' || true
  rm -f "$PIDFILE"
  exit 1
else
  dim "前台运行，Ctrl+C 结束"
  if [ "$PORT" != "8080" ]; then dim "绑定 0.0.0.0:$PORT，注意放行防火墙"; fi
  cd "$ROOT"
  exec "$BIN/server" -addr ":$PORT" -dir data/dist
fi
