# -*- coding: utf-8 -*-
"""无头浏览器验证：文档页渲染 + 页面内 API 调用 + 规则文件体积审计。

用法:
    python scripts/verify_headless.py [-port 18084] [-base http://127.0.0.1:18084]

驱动系统已装的 Chrome（channel="chrome"），不需要下载 Chromium。
"""
import argparse
import json
import struct
import sys
import time
from pathlib import Path

from playwright.sync_api import sync_playwright

ROOT = Path(__file__).resolve().parent.parent
DIST = ROOT / "data" / "dist"

# ESP32 资源预算（字节）。u32 表会整体存进 Flash 的一个分区，超过预算就放不下。
FLASH_4MB = 3 * 1024 * 1024   # 4MB Flash 扣掉 bootloader/分区表/系统分区后给数据的典型余量
FLASH_8MB = 6 * 1024 * 1024

passed, failed = [], []


def check(name, ok, detail=""):
    (passed if ok else failed).append((name, detail))
    mark = "PASS" if ok else "FAIL"
    print(f"  [{mark}] {name}" + (f"  —— {detail}" if detail else ""))


def human(n):
    for unit in ("B", "KiB", "MiB"):
        if n < 1024 or unit == "MiB":
            return f"{n:.1f} {unit}" if unit != "B" else f"{n} B"
        n /= 1024


def audit_sizes(manifest):
    """规则文件体积审计：对照 ESP32 Flash 预算逐个检查。"""
    print("\n== 规则文件体积审计 ==")
    lists = {l["name"]: l for l in manifest["lists"]}

    for name in ("esp32-tiny", "esp32-cn", "esp32-mix"):
        l = lists.get(name)
        check(f"{name} 适合 4MB Flash", l["hash_bytes"] <= FLASH_4MB,
              f"u32 表 {human(l['hash_bytes'])} / 预算 {human(FLASH_4MB)}")

    for name in ("esp32-cn-plus", "esp32-global-plus", "esp32-max"):
        l = lists.get(name)
        check(f"{name} 适合 8MB Flash（不应推荐给 4MB）", l["hash_bytes"] <= FLASH_8MB,
              f"u32 表 {human(l['hash_bytes'])}")

    # 单页大小：ESP32 一次读入 RAM 的量，必须小
    for name in ("esp32-cn", "esp32-mix"):
        page = DIST / f"{name}.u32"
        if page.exists():
            first_page = page.read_bytes()[: 1024 * 4]
            check(f"{name} 单哈希页 = 4096 字节", len(first_page) == 4096,
                  f"实际 {len(first_page)}")

    # 文本表 vs 二进制表的压缩比：u32 应远小于 txt
    txt = (DIST / "esp32-mix.txt").stat().st_size
    u32 = (DIST / "esp32-mix.u32").stat().st_size
    check("esp32-mix u32 表显著小于文本表", u32 < txt * 0.25,
          f"u32 {human(u32)} vs txt {human(txt)}（{u32 / txt * 100:.0f}%）")

    # 哈希碰撞检查：u32 去重后条数应接近域名数
    for name in ("esp32-cn", "esp32-mix", "esp32-max"):
        l = lists.get(name)
        ratio = l["count"] / max(1, l["hash_bytes"] // 4)
        check(f"{name} 32 位哈希碰撞率 < 1%", ratio >= 0.99,
              f"域名 {l['count']} vs 哈希 {l['hash_bytes'] // 4}")

    # 最大的单个源也不该失控
    big = max((l for l in manifest["lists"] if l["kind"] == "source"),
              key=lambda l: l["hash_bytes"])
    print(f"  (info) 最大单源 {big['name']}: u32 {human(big['hash_bytes'])}, "
          f"{big['count']:,} 域名 —— 仅建议分片按需下载")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("-base", default="http://127.0.0.1:18084")
    ap.add_argument("-port", type=int, default=18084)
    args = ap.parse_args()
    base = args.base
    shots = ROOT / "data" / "shots"
    shots.mkdir(parents=True, exist_ok=True)

    with sync_playwright() as pw:
        browser = pw.chromium.launch(channel="chrome", headless=True)
        page = browser.new_page(viewport={"width": 1280, "height": 900})

        js_errors = []
        page.on("pageerror", lambda e: js_errors.append(str(e)))
        failed_reqs = []
        page.on("requestfailed",
                lambda r: failed_reqs.append(f"{r.url} {r.failure}"))

        # ---------- 1. 文档页 ----------
        print("== 文档页 / ==")
        # 先空跑一次导航，把 Chrome 冷启动（进程拉起/渲染器初始化）从计时里剔掉
        page.goto(base + "/healthz", wait_until="domcontentloaded", timeout=15000)

        t0 = time.time()
        resp = page.goto(base + "/", wait_until="domcontentloaded", timeout=15000)
        ttfb_ms = (time.time() - t0) * 1000  # 导航响应到达
        page.wait_for_load_state("networkidle", timeout=15000)
        check("HTTP 200", resp.status == 200, f"status={resp.status}")
        check("页面响应 < 2s（排除浏览器冷启动）", ttfb_ms < 2000, f"{ttfb_ms:.0f} ms")
        title = page.title()
        check("标题正确（ESP32 广告过滤规则 API）",
              "ESP32" in title and "规则" in title, title)
        check("无 JS 运行时错误", not js_errors, "; ".join(js_errors[:3]))
        check("无失败的网络请求", not failed_reqs, "; ".join(failed_reqs[:3]))

        # 核心区块渲染
        body_text = page.inner_text("body")
        check("页面包含 manifest 概览", "502" in body_text or "唯一域名" in body_text)
        check("页面包含接口路径", "/api/v1/manifest" in body_text)
        page.screenshot(path=str(shots / "doc_page.png"), full_page=False)
        print(f"  (info) 截图 data/shots/doc_page.png")

        # ---------- 2. 页面内 fetch API ----------
        print("\n== 页面内 fetch API（真实浏览器上下文）==")
        m = page.evaluate(
            "fetch('/api/v1/manifest').then(r => r.json())")
        check("manifest JSON 可解析", isinstance(m, dict) and "lists" in m)
        check("schema=1 且有 18 个列表",
              m.get("schema") == 1 and len(m.get("lists", [])) == 18,
              f"schema={m.get('schema')} lists={len(m.get('lists', []))}")
        check("总唯一域名 502,861", m.get("total_unique_domains") == 502861,
              str(m.get("total_unique_domains")))

        # lookup：拦 / 不拦
        r1 = page.evaluate(
            "fetch('/api/v1/lookup/esp32-cn?domain=ads.google.com').then(r=>r.json())")
        check("lookup 命中广告域名", r1.get("blocked") is True, json.dumps(r1))
        r2 = page.evaluate(
            "fetch('/api/v1/lookup/esp32-cn?domain=www.qq.com').then(r=>r.json())")
        check("lookup 放行正常站点", r2.get("blocked") is False, json.dumps(r2))

        # 分页：文本页 512 行有序
        txt_page = page.evaluate(
            "fetch('/api/v1/lists/esp32-cn/domains?page=2').then(r=>r.text())")
        lines = [x for x in txt_page.splitlines() if x.strip()]
        check("文本页 = 512 行且有序",
              len(lines) == 512 and lines == sorted(lines),
              f"{len(lines)} 行")

        # 分页：二进制页 4096 字节升序
        import base64
        b64 = page.evaluate(
            "fetch('/api/v1/lists/esp32-cn/hashes?page=1').then(r=>r.arrayBuffer())"
            ".then(b => { const u=new Uint8Array(b); let s='';"
            "for(let i=0;i<u.length;i++) s+=String.fromCharCode(u[i]);"
            "return btoa(s); })")
        raw = base64.b64decode(b64)
        vals = list(struct.unpack(f"<{len(raw)//4}I", raw))
        check("哈希页 = 4096 字节且 uint32 升序",
              len(raw) == 4096 and vals == sorted(vals),
              f"{len(raw)} 字节 {len(vals)} 条")

        # 404 与参数校验
        code = page.evaluate(
            "fetch('/api/v1/lists/no-such-list/domains?page=1').then(r=>r.status)")
        check("未知列表返回 404", code == 404, f"status={code}")
        code = page.evaluate(
            "fetch('/api/v1/lists/esp32-cn/domains?page=0').then(r=>r.status)")
        check("page=0 返回 4xx", 400 <= code < 500, f"status={code}")

        # ---------- 3. Range 断点续传（浏览器层面）----------
        print("\n== Range / ETag ==")
        rng = page.evaluate("""
            fetch('/api/v1/lists/esp32-mix/raw.u32',
                  {headers:{'Range':'bytes=0-4095'}})
            .then(async r => ({status:r.status, len:(await r.arrayBuffer()).byteLength}))
        """)
        check("Range 请求返回 206 + 4096 字节",
              rng["status"] == 206 and rng["len"] == 4096, json.dumps(rng))

        etag = page.evaluate(
            "fetch('/api/v1/lists/esp32-cn/domains?page=1')"
            ".then(r => r.headers.get('etag'))")
        check("响应携带 ETag", bool(etag), etag or "无")
        code304 = page.evaluate(
            """fetch('/api/v1/lists/esp32-cn/domains?page=1',
                    {headers:{'If-None-Match': %s}})
               .then(r => r.status)""" % json.dumps(etag))
        check("If-None-Match 命中返回 304", code304 == 304, f"status={code304}")

        browser.close()

    # ---------- 4. 文件体积审计 ----------
    audit_sizes(m)

    # ---------- 汇总 ----------
    print(f"\n===== 结果：{len(passed)} 项通过，{len(failed)} 项失败 =====")
    for name, detail in failed:
        print(f"  FAIL {name}  {detail}")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
