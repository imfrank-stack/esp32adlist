# esp32adlist

自托管广告过滤规则服务：Go 单二进制。**复制一条 URL 就能分发**，同时给 ESP32 提供省内存的分片哈希表。

## 订阅链接（复制即用）

服务跑起来后，链接按你的服务器地址替换 `<ip>:8080`：

| 用途 | 链接 |
|---|---|
| **hosts 格式**（AdGuard Home / Pi-hole / 路由器 / 系统 hosts） | `http://<ip>:8080/hosts/esp32-cn.txt` |
| 域名格式（mosdns / SmartDNS / 自写脚本） | `http://<ip>:8080/api/v1/lists/esp32-cn/raw.txt` |
| ESP32 二进制表（uint32 FNV-1a，小端） | `http://<ip>:8080/api/v1/lists/esp32-cn/raw.u32` |

把 `esp32-cn` 换成任意列表名。打开 `http://<ip>:8080/` 有可视化选单，选列表、选格式、一键复制。

## 选哪个列表

| 列表 | 域名 | u32 体积 | 适用 |
|---|---:|---:|---|
| `esp32-tiny` | 1.5 万 | 57 KiB | 4MB Flash，最小 |
| `esp32-cn` | 1.6 万 | 64 KiB | 4MB Flash，国内日常（推荐） |
| `esp32-mix` | 14.5 万 | 566 KiB | 4MB Flash，国内外都要 |
| `esp32-cn-plus` | 29 万 | 1.1 MiB | 8MB Flash / PSRAM |
| `esp32-max` | 50 万 | 1.9 MiB | 8/16MB，全部源 |

11 个上游规则源（neoHosts、yhosts、GoodbyeAds、AWAvenue、AdRules、StevenBlack、AdGuard、HaGeZi 等）已下好放在 `data/raw/` 随仓库走，**构建默认离线**；源清单和 SHA256 见 `data/raw/SOURCES.md`。

## 启动

**Windows**：双击 `start.cmd`。命令行：`scripts\start.cmd -Port 8080 -Background`

**Linux**：`./scripts/start.sh --port 8080 --daemon`

脚本自动找 Go（缺失就下载便携版），编译、构建规则、起服务。`-Stop` / `--stop` 停止。

手动方式：

```bash
go run ./cmd/builder -config config/sources.json -out data/dist -basedir .
go run ./cmd/server -addr :8080 -dir data/dist
```

## 更新规则

| 动作 | 命令 |
|---|---|
| 离线重建（用本地 `data/raw`） | `builder`（默认）/ 脚本 `-Refresh` |
| 联网更新全部源 | `builder -prepare always` / 脚本 `-Update` / `update.sh --online` |

`data/dist/manifest.json` 一变，服务 30 秒内自动热加载，不用重启。定时任务：

```
0 4 * * * /opt/esp32adlist/scripts/update.sh --online >> /var/log/esp32adlist.log 2>&1
```

## 其余 API

- `/api/v1/manifest` — 全部列表的条数、体积、页数、sha256
- `/api/v1/lists/{name}/domains?page=N` — 文本分页 512 条/页
- `/api/v1/lists/{name}/hashes?page=N` — 哈希分页 4096 字节/页
- `/api/v1/lookup/{name}?domain=` — 单域名查询；`/healthz` — 健康检查
- 所有整表端点支持 `Range` 断点续传 + `ETag` 304

ESP32 固件示例：`esp32/AdBlock/AdBlock.ino`（LittleFS 存表 + DNS 53 端口过滤 + 断点续传同步）。部署细节（Docker / systemd / nginx）：看 `Dockerfile`、`deploy/`。
