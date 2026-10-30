/*
 * ESP32 hosts 广告过滤（配合 esp32adlist 规则 API）
 * ------------------------------------------------------------
 * 1. 规则同步：HEAD 比对 ETag → Range 断点续传下载有序 uint32 哈希表到 LittleFS
 * 2. 查询：FNV-1a32 + 二分查找，直接读 Flash，不把整表放进 RAM
 * 3. DNS：UDP 53 端口，命中返回 0.0.0.0，未命中转发上游
 *
 * 内存占用：表在 Flash，RAM 只占约 2KB 缓冲 + 单个 4 字节读缓存。
 * 依赖：ESP32 Arduino Core 2.x/3.x，LittleFS 默认分区即可。
 */

#include <WiFi.h>
#include <WiFiUdp.h>
#include <HTTPClient.h>
#include <LittleFS.h>

// ---------------- 配置 ----------------
const char *WIFI_SSID = "your-ssid";
const char *WIFI_PASS = "your-password";

const char *API_HOST = "192.168.1.10";   // 你的规则服务器
const uint16_t API_PORT = 8080;
const char *LIST_NAME = "esp32-cn";      // esp32-tiny / esp32-cn / esp32-mix ...

const char *UPSTREAM_DNS = "223.5.5.5";  // 未命中时转发的上游 DNS
const uint32_t RESYNC_MS = 24UL * 3600UL * 1000UL;

// ---------------- 状态 ----------------
const char *RULES_PATH = "/rules.u32";
const char *TMP_PATH = "/rules.tmp";
const char *META_PATH = "/rules.meta";

uint32_t g_count = 0;          // 表内条目数
String g_etag;                 // 服务端内容指纹
File g_rules;                  // 常开的规则文件句柄
uint32_t g_lastSync = 0;

WiFiUDP dnsSock;               // 53 端口，面向客户端
WiFiUDP upSock;                // 临时端口，面向上游
uint8_t packet[600];

// ---------------- 哈希与查询 ----------------
static inline uint32_t fnv1a32(const char *s) {
  uint32_t h = 2166136261u;
  while (*s) {
    h ^= (uint8_t)(*s++);
    h *= 16777619u;
  }
  return h;
}

bool containsHash(uint32_t h) {
  if (g_count == 0) return false;
  int32_t lo = 0, hi = (int32_t)g_count - 1;
  uint8_t b[4];
  while (lo <= hi) {
    int32_t mid = lo + (hi - lo) / 2;
    if (!g_rules.seek((size_t)mid * 4)) return false;
    if (g_rules.read(b, 4) != 4) return false;
    uint32_t v = (uint32_t)b[0] | ((uint32_t)b[1] << 8) |
                 ((uint32_t)b[2] << 16) | ((uint32_t)b[3] << 24);
    if (v == h) return true;
    if (v < h) lo = mid + 1; else hi = mid - 1;
  }
  return false;
}

// 后缀匹配：a.b.ads.com → b.ads.com → ads.com
bool isBlocked(const char *host) {
  char buf[128];
  size_t n = strlen(host);
  if (n == 0 || n >= sizeof(buf)) return false;
  memcpy(buf, host, n + 1);
  if (n && buf[n - 1] == '.') buf[n - 1] = 0;

  for (;;) {
    if (containsHash(fnv1a32(buf))) return true;
    char *dot = strchr(buf, '.');
    if (!dot) return false;
    memmove(buf, dot + 1, strlen(dot + 1) + 1);
    if (!strchr(buf, '.')) return false;   // 只剩顶级域，停止
  }
}

// ---------------- 规则同步 ----------------
void loadMeta() {
  if (!LittleFS.exists(META_PATH)) return;
  File f = LittleFS.open(META_PATH, "r");
  g_etag = f.readStringUntil('\n');
  g_count = f.readStringUntil('\n').toInt();
  f.close();
}

void saveMeta() {
  File f = LittleFS.open(META_PATH, "w");
  f.println(g_etag);
  f.println(g_count);
  f.close();
}

String listURL(const char *suffix) {
  return String("http://") + API_HOST + ":" + API_PORT +
         "/api/v1/lists/" + LIST_NAME + suffix;
}

// 返回 true 表示表已是最新或下载成功
bool syncRules() {
  HTTPClient http;
  http.setConnectTimeout(5000);
  http.setTimeout(15000);
  if (!http.begin(listURL("/raw.u32"))) return false;
  const char *hdr[] = {"ETag", "Content-Length"};
  http.collectHeaders(hdr, 2);
  int code = http.sendRequest("HEAD");
  if (code != 200) {
    Serial.printf("[sync] HEAD 失败 %d\n", code);
    http.end();
    return false;
  }
  String etag = http.header("ETag");
  size_t total = (size_t)http.header("Content-Length").toInt();
  http.end();

  size_t have = 0;
  if (LittleFS.exists(RULES_PATH)) {
    File f0 = LittleFS.open(RULES_PATH, "r");
    if (f0) { have = (size_t)f0.size(); f0.close(); }
  }
  if (etag.length() && etag == g_etag && have == total && g_count == total / 4) {
    Serial.printf("[sync] 已是最新 %s (%u 条)\n", etag.c_str(), g_count);
    if (!g_rules) g_rules = LittleFS.open(RULES_PATH, "r");
    return true;
  }
  if (total == 0 || total % 4 != 0) {
    Serial.println("[sync] 长度异常，放弃");
    return false;
  }

  if (have > total) {           // 服务端缩短了表，重下
    LittleFS.remove(RULES_PATH);
    have = 0;
  }
  Serial.printf("[sync] 下载 %u 字节（已有 %u）\n", (unsigned)total, (unsigned)have);

  if (!http.begin(listURL("/raw.u32"))) return false;
  http.setTimeout(20000);
  http.addHeader("Range", "bytes=" + String((unsigned)have) + "-");
  code = http.GET();
  if (code != 206 && code != 200) {
    Serial.printf("[sync] GET 失败 %d\n", code);
    http.end();
    return false;
  }

  File f = LittleFS.open(TMP_PATH, have ? "a" : "w");
  if (!f) { http.end(); return false; }
  WiFiClient *stream = http.getStreamPtr();
  uint8_t buf[1024];
  size_t got = have;
  uint32_t idle = millis();
  while (got < total && millis() - idle < 20000) {
    size_t n = stream->readBytes(buf, sizeof(buf));
    if (n) {
      f.write(buf, n);
      got += n;
      idle = millis();
    } else {
      delay(2);
    }
  }
  f.close();
  http.end();

  if (got != total) {
    Serial.printf("[sync] 中断 %u/%u，下次续传\n", (unsigned)got, (unsigned)total);
    return false;
  }
  if (LittleFS.exists(RULES_PATH)) LittleFS.remove(RULES_PATH);
  LittleFS.rename(TMP_PATH, RULES_PATH);

  if (g_rules) g_rules.close();
  g_rules = LittleFS.open(RULES_PATH, "r");
  g_count = total / 4;
  g_etag = etag;
  saveMeta();
  Serial.printf("[sync] 完成 %u 条，ETag %s\n", g_count, g_etag.c_str());
  return true;
}

// ---------------- DNS 处理 ----------------
// 返回问题段结束偏移；解析失败返回 0
int parseQuestion(const uint8_t *p, int len, char *host, size_t hostSize) {
  if (len < 12) return 0;
  int i = 12, h = 0;
  while (i < len) {
    uint8_t l = p[i++];
    if (l == 0) break;
    if (l & 0xC0) return 0;                  // 压缩指针，查询里不该出现
    if (i + l > len || h + l + 2 >= (int)hostSize) return 0;
    for (uint8_t k = 0; k < l; k++) {
      char c = (char)p[i + k];
      host[h++] = (c >= 'A' && c <= 'Z') ? c + 32 : c;
    }
    host[h++] = '.';
    i += l;
  }
  if (h == 0) return 0;
  host[h - 1] = 0;                            // 去掉末尾的点
  if (i + 4 > len) return 0;
  return i + 4;                               // 跳过 QTYPE + QCLASS
}

void replyBlocked(const IPAddress &ip, uint16_t port, int qend) {
  uint8_t resp[600];
  memcpy(resp, packet, qend);
  resp[2] = packet[2] | 0x80;   // QR=1，保留 RD
  resp[3] = 0x80;               // RA=1, RCODE=0
  resp[6] = 0; resp[7] = 1;     // ANCOUNT=1
  resp[8] = 0; resp[9] = 0;
  resp[10] = 0; resp[11] = 0;

  int p = qend;
  resp[p++] = 0xC0; resp[p++] = 0x0C;         // 指向问题里的域名
  resp[p++] = 0x00; resp[p++] = 0x01;         // TYPE A
  resp[p++] = 0x00; resp[p++] = 0x01;         // CLASS IN
  resp[p++] = 0; resp[p++] = 0; resp[p++] = 0; resp[p++] = 60;  // TTL 60s
  resp[p++] = 0x00; resp[p++] = 0x04;         // RDLENGTH
  resp[p++] = 0; resp[p++] = 0; resp[p++] = 0; resp[p++] = 0;    // 0.0.0.0

  dnsSock.beginPacket(ip, port);
  dnsSock.write(resp, p);
  dnsSock.endPacket();
}

void forward(const IPAddress &ip, uint16_t port, int len) {
  upSock.beginPacket(UPSTREAM_DNS, 53);
  upSock.write(packet, len);
  upSock.endPacket();

  uint32_t start = millis();
  while (millis() - start < 1200) {
    int n = upSock.parsePacket();
    if (n > 0) {
      uint8_t resp[600];
      int r = upSock.read(resp, sizeof(resp));
      if (r > 12) {
        dnsSock.beginPacket(ip, port);
        dnsSock.write(resp, r);
        dnsSock.endPacket();
      }
      return;
    }
    delay(2);
  }
}

void dnsLoop() {
  int len = dnsSock.parsePacket();
  if (len <= 0 || len > (int)sizeof(packet)) return;
  IPAddress ip = dnsSock.remoteIP();
  uint16_t port = dnsSock.remotePort();
  int n = dnsSock.read(packet, sizeof(packet));
  if (n < 12) return;

  char host[128];
  int qend = parseQuestion(packet, n, host, sizeof(host));
  if (qend > 0 && isBlocked(host)) {
    Serial.printf("[block] %s\n", host);
    replyBlocked(ip, port, qend);
    return;
  }
  forward(ip, port, n);
}

// ---------------- 主流程 ----------------
void setup() {
  Serial.begin(115200);
  delay(300);
  Serial.println("\nESP32 hosts ad-block");

  if (!LittleFS.begin(true)) {
    Serial.println("LittleFS 挂载失败");
    return;
  }
  loadMeta();

  WiFi.mode(WIFI_STA);
  WiFi.begin(WIFI_SSID, WIFI_PASS);
  Serial.print("连接 WiFi");
  while (WiFi.status() != WL_CONNECTED) {
    delay(500);
    Serial.print('.');
  }
  Serial.printf("\nIP: %s\n", WiFi.localIP().toString().c_str());

  syncRules();
  g_lastSync = millis();

  dnsSock.begin(53);
  upSock.begin(0);
  Serial.printf("DNS 已启动，规则 %u 条，上游 %s\n", g_count, UPSTREAM_DNS);
}

void loop() {
  dnsLoop();
  if (millis() - g_lastSync > RESYNC_MS) {
    g_lastSync = millis();
    syncRules();
  }
}
