package main

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"esp32adlist/internal/rules"
	"esp32adlist/internal/store"
)

//go:embed web/index.html
var docsHTML []byte

func main() {
	var (
		addr    = flag.String("addr", ":8080", "监听地址")
		dir     = flag.String("dir", "data/dist", "构建产物目录")
		reload  = flag.Duration("reload", 30*time.Second, "manifest 变更检查间隔，0 关闭")
		logPath = flag.String("log", "", "同时把访问日志追加写入该文件")
		token   = flag.String("token", "", "访问令牌；非空时所有请求必须带 ?token= 或有效 cookie，否则一律 404")
	)
	flag.Parse()

	if *logPath != "" {
		f, err := os.OpenFile(*logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			log.Fatalf("打开日志文件失败: %v", err)
		}
		log.SetOutput(io.MultiWriter(os.Stderr, f))
	}

	st, err := store.Load(*dir)
	if err != nil {
		log.Fatalf("加载失败: %v", err)
	}
	snapshot = &store.Snapshot{}
	snapshot.Set(st)
	if *reload > 0 {
		snapshot.Watch(*dir, *reload)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		cur := snapshot.Get()
		writeJSON(w, r, http.StatusOK, map[string]any{
			"ok": true, "version": cur.Manifest.Version, "lists": len(cur.Manifest.Lists),
		})
	})
	mux.HandleFunc("GET /api/v1/manifest", func(w http.ResponseWriter, r *http.Request) {
		cur := snapshot.Get()
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.Header().Set("ETag", `"`+cur.Manifest.Version+`"`)
		writeJSON(w, r, http.StatusOK, cur.Manifest)
	})
	mux.HandleFunc("GET /api/v1/lists", func(w http.ResponseWriter, r *http.Request) {
		cur := snapshot.Get()
		w.Header().Set("Cache-Control", "public, max-age=60")
		writeJSON(w, r, http.StatusOK, map[string]any{"version": cur.Manifest.Version, "lists": cur.Manifest.Lists})
	})
	mux.HandleFunc("GET /api/v1/lists/{name}", listMeta)
	mux.HandleFunc("GET /api/v1/lists/{name}/domains", domainPage)
	mux.HandleFunc("GET /api/v1/lists/{name}/hashes", hashPage)
	mux.HandleFunc("GET /api/v1/lists/{name}/raw.txt", rawFile("txt"))
	mux.HandleFunc("GET /api/v1/lists/{name}/raw.u32", rawFile("u32"))
	mux.HandleFunc("GET /hosts/{name}", hostsFile)                  // /hosts/esp32-cn 或 /hosts/esp32-cn.txt
	mux.HandleFunc("GET /api/v1/lists/{name}/hosts.txt", hostsFile) // 长路径别名
	mux.HandleFunc("GET /api/v1/lookup/{name}", lookup)
	mux.HandleFunc("GET /api/v1/check", check)
	mux.HandleFunc("GET /api/v1/hash", func(w http.ResponseWriter, r *http.Request) {
		d := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("domain")))
		if d == "" {
			writeErr(w, http.StatusBadRequest, "缺少 domain 参数")
			return
		}
		writeJSON(w, r, http.StatusOK, map[string]any{
			"domain": d, "algo": rules.HashAlgo, "hash": rules.HashDomain(d),
			"hash_hex": fmt.Sprintf("%08x", rules.HashDomain(d)),
		})
	})
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(docsHTML)
	})

	// 统一附加 CORS 与请求日志
	// tokenOK 校验访问令牌：查询参数或 cookie 命中即可。
	// 校验失败统一回 404，让未授权者以为服务不存在，也不给爆破反馈。
	tokenOK := func(r *http.Request) bool {
		if *token == "" {
			return true
		}
		if r.URL.Query().Get("token") == *token {
			return true
		}
		if ck, err := r.Cookie("esp32adlist_token"); err == nil && ck.Value == *token {
			return true
		}
		return false
	}

	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !tokenOK(r) {
			time.Sleep(50 * time.Millisecond) // 轻微延迟，抬高字典爆破成本
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, code: 200}
		mux.ServeHTTP(sw, r)
		log.Printf("%s %s %d %s", r.Method, r.URL.RequestURI(), sw.code, time.Since(start).Round(time.Millisecond))
	})

	srv := &http.Server{
		Addr:              *addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      300 * time.Second, // 2Mbps 峰值带宽下 3MB 名单要 25s+，30s 会掐断传输
		IdleTimeout:       60 * time.Second,
	}
	log.Printf("规则版本 %s，%d 个列表，监听 %s", st.Manifest.Version, len(st.Manifest.Lists), *addr)
	log.Fatal(srv.ListenAndServe())
}

// 单实例服务，用包级快照即可。
var snapshot *store.Snapshot

func current(*http.Request) *store.Store { return snapshot.Get() }

func getList(w http.ResponseWriter, r *http.Request) (*store.Store, *store.List, bool) {
	cur := current(r)
	name := r.PathValue("name")
	l, ok := cur.Lists[name]
	if !ok {
		writeErr(w, http.StatusNotFound, "列表不存在: "+name)
		return nil, nil, false
	}
	return cur, l, true
}

func listMeta(w http.ResponseWriter, r *http.Request) {
	_, l, ok := getList(w, r)
	if !ok {
		return
	}
	base := "/api/v1/lists/" + l.Info.Name
	writeJSON(w, r, http.StatusOK, map[string]any{
		"list": l.Info,
		"endpoints": map[string]string{
			"domains_page": fmt.Sprintf("%s/domains?page=1", base),
			"hashes_page":  fmt.Sprintf("%s/hashes?page=1", base),
			"raw_txt":      base + "/raw.txt",
			"raw_u32":      base + "/raw.u32",
			"lookup":       "/api/v1/lookup/" + l.Info.Name + "?domain=example.com",
		},
		"page_size":      rules.DefaultDomainPageSize,
		"hash_page_size": rules.DefaultHashPageSize,
	})
}

func domainPage(w http.ResponseWriter, r *http.Request) {
	_, l, ok := getList(w, r)
	if !ok {
		return
	}
	page, okPage := pageOr400(w, r, 1)
	if !okPage {
		return
	}
	body := l.PageBytes(page - 1)
	if body == nil {
		writeErr(w, http.StatusNotFound, fmt.Sprintf("页码越界（共 %d 页）", l.Info.DomainPages))
		return
	}
	etag := fmt.Sprintf(`"%s-p%d"`, l.Info.SHA256Domains[:12], page)
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("X-List", l.Info.Name)
	w.Header().Set("X-Page", strconv.Itoa(page))
	w.Header().Set("X-Total-Pages", strconv.Itoa(l.Info.DomainPages))
	w.Header().Set("X-Total-Count", strconv.Itoa(l.Info.Count))
	w.Header().Set("X-Page-Size", strconv.Itoa(rules.DefaultDomainPageSize))
	if page < l.Info.DomainPages {
		w.Header().Set("X-Next-Page", strconv.Itoa(page+1))
		w.Header().Set("Link", fmt.Sprintf(`<%s?page=%d>; rel="next"`, r.URL.Path, page+1))
	}
	writeBody(w, r, etag, "text/plain; charset=utf-8", body)
}

func hashPage(w http.ResponseWriter, r *http.Request) {
	_, l, ok := getList(w, r)
	if !ok {
		return
	}
	page, okPage := pageOr400(w, r, 1)
	if !okPage {
		return
	}
	body := l.HashPage(page - 1)
	if body == nil {
		writeErr(w, http.StatusNotFound, fmt.Sprintf("页码越界（共 %d 页）", l.Info.HashPages))
		return
	}
	etag := fmt.Sprintf(`"%s-h%d"`, l.Info.SHA256Hashes[:12], page)
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("X-List", l.Info.Name)
	w.Header().Set("X-Page", strconv.Itoa(page))
	w.Header().Set("X-Total-Pages", strconv.Itoa(l.Info.HashPages))
	w.Header().Set("X-Total-Count", strconv.Itoa(l.Info.Count))
	w.Header().Set("X-Page-Size", strconv.Itoa(rules.DefaultHashPageSize))
	if page < l.Info.HashPages {
		w.Header().Set("X-Next-Page", strconv.Itoa(page+1))
		w.Header().Set("Link", fmt.Sprintf(`<%s?page=%d>; rel="next"`, r.URL.Path, page+1))
	}
	writeBody(w, r, etag, "application/octet-stream", body)
}

func rawFile(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, l, ok := getList(w, r)
		if !ok {
			return
		}
		var data []byte
		var name, ctype string
		if kind == "txt" {
			data, name, ctype = l.Text, l.Info.Name+".txt", "text/plain; charset=utf-8"
		} else {
			data, name, ctype = l.HashRaw, l.Info.Name+".u32", "application/octet-stream"
		}
		etag := shaFor(l, kind)
		if kind == "txt" && acceptsGzip(r) {
			data = l.GzipText()
			w.Header().Set("Content-Encoding", "gzip")
			etag += "-gz"
		}
		w.Header().Set("Content-Type", ctype)
		w.Header().Set("Cache-Control", "public, max-age=300")
		w.Header().Set("Content-Disposition", "inline; filename="+name)
		w.Header().Set("ETag", `"`+etag+`"`)
		http.ServeContent(w, r, name, time.Now().Add(-time.Hour), bytes.NewReader(data))
	}
}

// acceptsGzip 判断响应是否可以走 gzip：客户端声明支持且未请求 Range
// （Range 语义与压缩冲突；u32 二进制也压不动多少，跳过）。
func acceptsGzip(r *http.Request) bool {
	if r.Header.Get("Range") != "" {
		return false
	}
	return strings.Contains(r.Header.Get("Accept-Encoding"), "gzip")
}

// hostsFile 输出标准 hosts 格式（`0.0.0.0 域名`），
// 可直接粘贴分发到 AdGuard Home / Pi-hole / OpenClash / 系统 hosts。
func hostsFile(w http.ResponseWriter, r *http.Request) {
	// /hosts/esp32-cn 和 /hosts/esp32-cn.txt 都指向同一个列表
	r.SetPathValue("name", strings.TrimSuffix(r.PathValue("name"), ".txt"))
	_, l, ok := getList(w, r)
	if !ok {
		return
	}
	data := l.HostsText()
	etag := l.Info.SHA256Domains[:12] + "-hosts"
	if acceptsGzip(r) {
		data = l.GzipHosts()
		w.Header().Set("Content-Encoding", "gzip")
		etag += "-gz"
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("Content-Disposition", "inline; filename="+l.Info.Name+".txt")
	w.Header().Set("ETag", `"`+etag+`"`)
	http.ServeContent(w, r, l.Info.Name+".txt", time.Now().Add(-time.Hour), bytes.NewReader(data))
}

func shaFor(l *store.List, kind string) string {
	if kind == "txt" {
		return l.Info.SHA256Domains
	}
	return l.Info.SHA256Hashes
}

func lookup(w http.ResponseWriter, r *http.Request) {
	cur, l, ok := getList(w, r)
	if !ok {
		return
	}
	d := r.URL.Query().Get("domain")
	if d == "" {
		writeErr(w, http.StatusBadRequest, "缺少 domain 参数")
		return
	}
	norm, valid := rules.CleanDomain(d)
	if !valid {
		writeJSON(w, r, http.StatusOK, map[string]any{
			"domain": d, "valid": false, "blocked": false, "list": l.Info.Name,
		})
		return
	}
	blocked, matched := l.Lookup(norm)
	writeJSON(w, r, http.StatusOK, map[string]any{
		"domain": norm, "valid": true, "blocked": blocked, "matched": matched,
		"list": l.Info.Name, "version": cur.Manifest.Version,
	})
}

func check(w http.ResponseWriter, r *http.Request) {
	cur := current(r)
	d := r.URL.Query().Get("domain")
	if d == "" {
		writeErr(w, http.StatusBadRequest, "缺少 domain 参数")
		return
	}
	norm, valid := rules.CleanDomain(d)
	if !valid {
		writeJSON(w, r, http.StatusOK, map[string]any{"domain": d, "valid": false, "blocked": false, "results": []any{}})
		return
	}
	names := strings.Split(r.URL.Query().Get("lists"), ",")
	if len(names) == 1 && names[0] == "" {
		names = []string{"esp32-cn"}
	}
	results := make([]map[string]any, 0, len(names))
	anyBlocked := false
	for _, n := range names {
		n = strings.TrimSpace(n)
		l, ok := cur.Lists[n]
		if !ok {
			results = append(results, map[string]any{"list": n, "error": "列表不存在"})
			continue
		}
		blocked, matched := l.Lookup(norm)
		anyBlocked = anyBlocked || blocked
		results = append(results, map[string]any{"list": n, "blocked": blocked, "matched": matched})
	}
	writeJSON(w, r, http.StatusOK, map[string]any{
		"domain": norm, "valid": true, "blocked": anyBlocked,
		"hash": rules.HashDomain(norm), "version": cur.Manifest.Version, "results": results,
	})
}

func parsePage(r *http.Request, def int) int {
	q := r.URL.Query().Get("page")
	if q == "" {
		return def // 未传参数：用默认页
	}
	p, err := strconv.Atoi(q)
	if err != nil || p < 1 {
		return 0 // 显式传了非法值：让调用方回 400，不做静默回落
	}
	return p
}

// pageOr400 统一处理非法页码参数。
func pageOr400(w http.ResponseWriter, r *http.Request, def int) (int, bool) {
	p := parsePage(r, def)
	if p < 1 {
		writeErr(w, http.StatusBadRequest, "page 必须是 >= 1 的整数")
		return 0, false
	}
	return p, true
}

func writeJSON(w http.ResponseWriter, r *http.Request, code int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "编码失败", http.StatusInternalServerError)
		return
	}
	b = append(b, '\n')
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if code == http.StatusOK && len(b) > 1024 && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(code)
		gz := gzip.NewWriter(w)
		gz.Write(b)
		gz.Close()
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(code)
	w.Write(b)
}

func writeBody(w http.ResponseWriter, r *http.Request, etag, ctype string, body []byte) {
	if etag != "" {
		w.Header().Set("ETag", etag)
		if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Vary", "Accept-Encoding")
	if len(body) > 1024 && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		gz.Write(body)
		gz.Close()
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Write(body)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{"error": msg})
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (s *statusWriter) WriteHeader(c int) { s.code = c; s.ResponseWriter.WriteHeader(c) }

func (s *statusWriter) Write(b []byte) (int, error) {
	return s.ResponseWriter.Write(b)
}
