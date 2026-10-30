package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"esp32adlist/internal/rules"
)

// 本地规则源的最小体积阈值，低于此值视为下载失败的残骸。
const minSourceBytes = 1024

func main() {
	var (
		cfgPath  = flag.String("config", "config/sources.json", "规则源配置")
		outDir   = flag.String("out", "data/dist", "输出目录")
		stripWWW = flag.Bool("strip-www", true, "统一去掉 www. 前缀（配合后缀匹配可减小体积）")
		baseDir  = flag.String("basedir", ".", "相对路径基准目录")
		prepare  = flag.String("prepare", "auto", "规则源获取策略：auto=本地优先、缺失才下载；always=无条件重新下载；never=纯本地、缺档即报错")
		fetch    = flag.Bool("fetch", false, "等价于 -prepare always（保留兼容）")
	)
	flag.Parse()

	mode := strings.ToLower(*prepare)
	if *fetch {
		mode = "always"
	}
	switch mode {
	case "auto", "always", "never":
	default:
		must(fmt.Errorf("-prepare 只支持 auto/always/never，收到 %q", *prepare))
	}

	cfg, err := rules.LoadConfig(*cfgPath)
	must(err)

	must(os.MkdirAll(*outDir, 0o755))

	// 先看本地有没有货：默认不全量联网，只在缺档时才补下载。
	missing := missingSources(cfg, *baseDir)
	switch {
	case mode == "never":
		if len(missing) > 0 {
			reportMissing(missing)
			must(fmt.Errorf("%d 个规则源本地缺失，且当前为纯本地模式（去掉 -prepare never 允许联网补拉）", len(missing)))
		}
		fmt.Println("规则源：全部命中本地缓存，不联网")
	case mode == "always":
		fmt.Println("更新规则源（-prepare always，强制重新下载）：")
		fetchSources(cfg, *baseDir, cfg.Sources)
	case len(missing) > 0:
		fmt.Printf("更新规则源（本地缺 %d 个，其余沿用缓存）：\n", len(missing))
		fetchSources(cfg, *baseDir, missing)
	default:
		fmt.Println("规则源：全部命中本地缓存，不联网")
	}
	must(writeInventory(cfg, *baseDir))

	names := make([]string, 0, len(cfg.Sources))
	sets := make(map[string]*rules.Set, len(cfg.Sources))
	infos := make([]rules.ListInfo, 0, len(cfg.Sources)+len(cfg.Profiles))

	fmt.Println("解析规则源：")
	for _, src := range cfg.Sources {
		strip := *stripWWW
		if src.StripWWW != nil {
			strip = *src.StripWWW
		}
		merged := rules.NewSet()
		var st rules.Stats
		for _, rel := range src.Files {
			p := rel
			if !filepath.IsAbs(p) {
				p = filepath.Join(*baseDir, rel)
			}
			set, err := rules.LoadFile(p, strip)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  ! %-22s 读取失败 %v\n", src.Name, err)
				continue
			}
			merged.Merge(set)
			s := set.Stats()
			st.Lines += s.Lines
			st.Allow += s.Allow
			st.Invalid += s.Invalid
			st.Dup += s.Dup
		}
		st.Block = merged.Stats().Block
		info, err := emit(*outDir, src.Name, merged, st)
		must(err)
		info.Title = src.Title
		info.Kind = "source"
		info.Category = src.Category
		info.Description = src.Description
		info.Sources = src.URLs
		infos = append(infos, info)
		sets[src.Name] = merged
		names = append(names, src.Name)
		fmt.Printf("  %-22s %7d 域名  (行 %d, 例外 %d, 非法 %d)\n",
			src.Name, info.Count, st.Lines, st.Allow, st.Invalid)
	}

	fmt.Println("生成聚合 profile：")
	all := rules.NewSet()
	for _, n := range names {
		all.Merge(sets[n])
	}
	for _, p := range cfg.Profiles {
		acc := rules.NewSet()
		var st rules.Stats
		for _, m := range p.Members {
			s, ok := sets[m]
			if !ok {
				fmt.Fprintf(os.Stderr, "  ! profile %s 成员 %s 缺失\n", p.Name, m)
				continue
			}
			acc.Merge(s)
			st.Dup += s.Stats().Dup
		}
		st.Block = acc.Stats().Block
		st.Lines = acc.Stats().Block + st.Dup
		info, err := emit(*outDir, p.Name, acc, st)
		must(err)
		info.Title = p.Title
		info.Kind = "profile"
		info.Category = "profile"
		info.Description = p.Description
		info.Members = p.Members
		infos = append(infos, info)
		fmt.Printf("  %-22s %7d 域名  %6.1f KiB(u32)  %d 个哈希页\n",
			p.Name, info.Count, float64(info.HashBytes)/1024, info.HashPages)
	}

	sort.SliceStable(infos, func(i, j int) bool {
		if infos[i].Kind != infos[j].Kind {
			return infos[i].Kind == "source"
		}
		return infos[i].Count > infos[j].Count
	})

	m := rules.Manifest{
		Schema:         rules.SchemaVersion,
		Version:        time.Now().Format("20060102-1504"),
		Generated:      time.Now().UTC().Format(time.RFC3339),
		Hash:           rules.HashSpec{Algo: rules.HashAlgo, Endian: rules.HashEndian, Bytes: rules.HashBytes},
		DomainPageSize: rules.DefaultDomainPageSize,
		HashPageSize:   rules.DefaultHashPageSize,
		TotalUnique:    len(all.Domains()),
		Lists:          infos,
	}
	prune(*outDir, infos)

	b, err := json.MarshalIndent(m, "", "  ")
	must(err)
	tmp := filepath.Join(*outDir, "manifest.json.tmp")
	must(os.WriteFile(tmp, b, 0o644))
	must(os.Rename(tmp, filepath.Join(*outDir, "manifest.json")))

	fmt.Printf("\n总计：%d 个列表，全部源并集 %d 个唯一域名，版本 %s\n", len(infos), m.TotalUnique, m.Version)
	fmt.Printf("输出目录：%s\n", *outDir)
}

func emit(dir, name string, set *rules.Set, st rules.Stats) (rules.ListInfo, error) {
	domains := set.Domains()

	var sb strings.Builder
	sb.Grow(len(domains) * 16)
	for _, d := range domains {
		sb.WriteString(d)
		sb.WriteByte('\n')
	}
	text := []byte(sb.String())

	hashes := make([]uint32, 0, len(domains))
	for _, d := range domains {
		hashes = append(hashes, rules.HashDomain(d))
	}
	sort.Slice(hashes, func(i, j int) bool { return hashes[i] < hashes[j] })
	uniq := hashes[:0]
	for i, h := range hashes {
		if i == 0 || h != hashes[i-1] {
			uniq = append(uniq, h)
		}
	}
	hashes = uniq

	bin := make([]byte, 4*len(hashes))
	for i, h := range hashes {
		binary.LittleEndian.PutUint32(bin[i*4:], h)
	}

	if err := writeFile(filepath.Join(dir, name+".txt"), text); err != nil {
		return rules.ListInfo{}, err
	}
	if err := writeFile(filepath.Join(dir, name+".u32"), bin); err != nil {
		return rules.ListInfo{}, err
	}

	ds := sha256.Sum256(text)
	hs := sha256.Sum256(bin)
	info := rules.ListInfo{
		Name:          name,
		Count:         len(domains),
		DomainBytes:   int64(len(text)),
		HashBytes:     int64(len(bin)),
		DomainPages:   pageCount(len(domains), rules.DefaultDomainPageSize),
		HashPages:     pageCount(len(hashes), rules.DefaultHashPageSize),
		SHA256Domains: hex.EncodeToString(ds[:16]),
		SHA256Hashes:  hex.EncodeToString(hs[:16]),
		Stats:         &st,
	}
	return info, nil
}

// sourcePath 返回规则源在本地的绝对路径。
func sourcePath(src rules.Source, base string) string {
	if len(src.Files) == 0 {
		return ""
	}
	p := src.Files[0]
	if !filepath.IsAbs(p) {
		p = filepath.Join(base, p)
	}
	return p
}

// localOK 判断本地副本是否可用（存在且不像是下载失败的残骸）。
func localOK(path string) bool {
	if path == "" {
		return false
	}
	st, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !st.IsDir() && st.Size() >= minSourceBytes
}

// missingSources 找出本地还没有（或残缺）的来源。
func missingSources(cfg *rules.Config, base string) []rules.Source {
	out := make([]rules.Source, 0, len(cfg.Sources))
	for _, src := range cfg.Sources {
		if len(src.URLs) == 0 {
			continue // 纯本地来源，没有联网入口
		}
		if !localOK(sourcePath(src, base)) {
			out = append(out, src)
		}
	}
	return out
}

func reportMissing(missing []rules.Source) {
	fmt.Fprintln(os.Stderr, "以下规则源本地缺失：")
	for _, s := range missing {
		fmt.Fprintf(os.Stderr, "  - %-22s %s\n", s.Name, sourcePath(s, "."))
	}
}

// fetchSources 下载给定的规则源，逐个原子落盘，失败不影响其他源。
func fetchSources(cfg *rules.Config, base string, list []rules.Source) {
	client := &http.Client{Timeout: 180 * time.Second}
	for _, src := range list {
		if len(src.URLs) == 0 || len(src.Files) == 0 {
			continue
		}
		dst := sourcePath(src, base)
		must(os.MkdirAll(filepath.Dir(dst), 0o755))

		var lastErr error
		var size int64
		for _, u := range src.URLs {
			size, lastErr = download(client, u, dst)
			if lastErr == nil {
				break
			}
		}
		if lastErr != nil {
			fmt.Fprintf(os.Stderr, "  ! %-22s 下载失败: %v（保留旧副本或跳过）\n", src.Name, lastErr)
			continue
		}
		fmt.Printf("  %-22s %9d 字节\n", src.Name, size)
	}
}

func download(client *http.Client, url, dst string) (int64, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "esp32adlist/1.0 (+https://github.com/)")
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	tmp := dst + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, resp.Body)
	f.Close()
	if err != nil {
		os.Remove(tmp)
		return 0, err
	}
	if n < minSourceBytes {
		os.Remove(tmp)
		return 0, fmt.Errorf("响应过小（%d 字节），疑似错误页", n)
	}
	if err := os.Rename(tmp, dst); err != nil {
		return 0, err
	}
	return n, nil
}

// writeInventory 把规则源清单落盘，方便核对本地副本的来源与校验值。
func writeInventory(cfg *rules.Config, base string) error {
	var b strings.Builder
	b.WriteString("# 规则源本地快照\n\n")
	b.WriteString("`data/raw/` 下的文件是上游规则列表的本地副本，已随仓库保存。\n")
	b.WriteString("构建器默认**只读取这些本地文件**，不联网；只有文件缺失（或显式 `-prepare always`）才会重新下载。\n\n")
	b.WriteString("| 名称 | 分类 | 本地文件 | 大小 | 行数 | SHA256(前16) |\n|---|---|---|---:|---:|---|\n")
	for _, src := range cfg.Sources {
		p := sourcePath(src, base)
		rel := p
		if r, err := filepath.Rel(base, p); err == nil {
			rel = filepath.ToSlash(r)
		}
		size, lines, sum := int64(0), 0, "-"
		if localOK(p) {
			data, err := os.ReadFile(p)
			if err == nil {
				size = int64(len(data))
				lines = bytes.Count(data, []byte{'\n'})
				if data[len(data)-1] != '\n' {
					lines++
				}
				h := sha256.Sum256(data)
				sum = hex.EncodeToString(h[:8])
			}
		} else {
			sum = "(缺失)"
		}
		fmt.Fprintf(&b, "| `%s` | %s | `%s` | %s | %s | `%s` |\n",
			src.Name, src.Category, rel, humanBytes(size), humanCount(lines), sum)
	}
	b.WriteString("\n## 来源地址\n\n")
	for _, src := range cfg.Sources {
		fmt.Fprintf(&b, "- **%s**（%s）%s\n", src.Name, src.Title, src.Description)
		for _, u := range src.URLs {
			fmt.Fprintf(&b, "  - %s\n", u)
		}
	}
	out := filepath.Join(base, "data", "raw", "SOURCES.md")
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	return writeFile(out, []byte(b.String()))
}

func humanBytes(n int64) string {
	switch {
	case n >= 1024*1024:
		return fmt.Sprintf("%.1f MiB", float64(n)/1024/1024)
	case n >= 1024:
		return fmt.Sprintf("%.1f KiB", float64(n)/1024)
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func humanCount(n int) string {
	if n >= 10000 {
		return fmt.Sprintf("%.1f 万", float64(n)/10000)
	}
	return fmt.Sprintf("%d", n)
}

// prune 删除本次没有生成的旧表，避免改名后残留文件占空间。
func prune(dir string, keep []rules.ListInfo) {
	valid := make(map[string]bool, len(keep)*2)
	for _, l := range keep {
		valid[l.Name+".txt"] = true
		valid[l.Name+".u32"] = true
	}
	valid["manifest.json"] = true
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || valid[e.Name()] {
			continue
		}
		if strings.HasSuffix(e.Name(), ".txt") || strings.HasSuffix(e.Name(), ".u32") ||
			strings.HasSuffix(e.Name(), ".tmp") {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

func pageCount(n, size int) int {
	if n == 0 {
		return 0
	}
	return (n + size - 1) / size
}

func writeFile(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
}
