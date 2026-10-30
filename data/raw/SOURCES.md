# 规则源本地快照

`data/raw/` 下的文件是上游规则列表的本地副本，已随仓库保存。
构建器默认**只读取这些本地文件**，不联网；只有文件缺失（或显式 `-prepare always`）才会重新下载。

| 名称 | 分类 | 本地文件 | 大小 | 行数 | SHA256(前16) |
|---|---|---|---:|---:|---|
| `cn-neohosts-basic` | cn | `data/raw/neohosts-basic.txt` | 112.6 KiB | 4846 | `18908302ad9ac963` |
| `cn-neohosts-full` | cn | `data/raw/neohosts-full.txt` | 220.5 KiB | 9592 | `85f67ac24f6aedcc` |
| `cn-yhosts` | cn | `data/raw/yhosts.txt` | 180.5 KiB | 7673 | `f34a9e54e7befb24` |
| `cn-goodbyeads` | cn | `data/raw/goodbyeads.txt` | 8.7 MiB | 27.8 万 | `3ece2c35d8cb86f2` |
| `cn-awavenue` | cn | `data/raw/awavenue.txt` | 16.6 KiB | 711 | `b4725e3975fceffe` |
| `cn-adrules` | cn | `data/raw/adrules-dns.txt` | 31.2 KiB | 1780 | `8c93d0207e486a0c` |
| `cn-malicious-lite` | cn | `data/raw/adblockfilters-lite.txt` | 216.8 KiB | 8504 | `b07ecba158815a11` |
| `global-stevenblack` | global | `data/raw/stevenblack.txt` | 2.2 MiB | 8.4 万 | `cb1e84aa87035df1` |
| `global-adguard-dns` | global | `data/raw/adguard-dns.txt` | 4.3 MiB | 18.5 万 | `78ae71fc8ee19dba` |
| `global-hagezi-pro-mini` | global | `data/raw/hagezi-pro-mini.txt` | 1.9 MiB | 8.9 万 | `06b4fbc77d178793` |
| `extra-nocoin` | extra | `data/raw/nocoin.txt` | 8.0 KiB | 323 | `41c0e875b48dc652` |

## 来源地址

- **cn-neohosts-basic**（neoHosts 基础版）轻量无冗余，覆盖国内主流广告与追踪域名，误杀率低，适合日常办公/家用。
  - https://cdn.jsdelivr.net/gh/neoFelhz/neohosts@gh-pages/basic/hosts.txt
- **cn-neohosts-full**（neoHosts 完整版）基础版之上增加恶意站点、网页挖矿、色情赌博域名拦截。
  - https://cdn.jsdelivr.net/gh/neoFelhz/neohosts@gh-pages/full/hosts.txt
- **cn-yhosts**（yhosts）专注国内广告拦截的经典轻量列表，适配国内视频、电商、主流 App 的广告与统计域名。
  - https://cdn.jsdelivr.net/gh/VeleSila/yhosts@master/hosts.txt
- **cn-goodbyeads**（GoodbyeAds）安卓 App 与国内视频平台开屏/贴片广告专项优化，体积大，建议只给带 PSRAM 或大 Flash 的设备用。
  - https://cdn.jsdelivr.net/gh/jerryn70/GoodbyeAds@master/Hosts/GoodbyeAds.txt
- **cn-awavenue**（AWAvenue 秋风广告规则）国内 App 开屏、信息流广告规则，体积小，适合低配设备。
  - https://cdn.jsdelivr.net/gh/haha-tiger/AWAvenue-Ads-Rule@main/Filters/AWAvenue-Ads-Rule-Adguard.txt
- **cn-adrules**（AdRules DNS）Cats-Team/AdRules 的 DNS 规则子集，覆盖国内广告联盟与常见推广域名。
  - https://cdn.jsdelivr.net/gh/Cats-Team/AdRules@main/mod/rules/dns-rules.txt
- **cn-malicious-lite**（国内恶意站点轻量版）仅收录国内高风险广告与恶意域名，体积极小，适合叠加在主列表上。
  - https://gcore.jsdelivr.net/gh/217heidai/adblockfilters@main/rules/adblockhostslite.txt
- **global-stevenblack**（StevenBlack/hosts）全球最主流的开源 hosts 合集，整合 someonewhocares、MVPS、Peter Lowe 等源。
  - https://cdn.jsdelivr.net/gh/StevenBlack/hosts@master/hosts
- **global-adguard-dns**（AdGuard DNS filter）AdGuard 官方 DNS 过滤器（HostlistsRegistry 发布版，AdBlock 语法，构建时自动转换为域名），覆盖全球广告、追踪与社交插件。
  - https://adguardteam.github.io/HostlistsRegistry/assets/filter_1.txt
- **global-hagezi-pro-mini**（HaGeZi Multi PRO mini）面向低内存设备精简过的 PRO 列表，只保留 Top 榜单中出现的域名，性价比最高。
  - https://cdn.jsdelivr.net/gh/hagezi/dns-blocklists@main/adblock/pro.mini.txt
- **extra-nocoin**（NoCoin 网页挖矿拦截）拦截浏览器端加密货币挖矿脚本域名，防止网页偷占 CPU。
  - https://cdn.jsdelivr.net/gh/hoshsadiq/adblock-nocoin-list@master/hosts.txt
