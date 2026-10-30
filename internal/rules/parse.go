package rules

import (
	"bufio"
	"net"
	"os"
	"sort"
	"strings"
	"unicode/utf8"
)

// Stats 记录单个规则文件的解析统计。
type Stats struct {
	Lines   int `json:"lines"`
	Block   int `json:"block"`
	Allow   int `json:"allow"`
	Invalid int `json:"invalid"`
	Dup     int `json:"dup"`
}

// Set 是一个列表的域名集合。allow 来自 adblock 的 @@ 例外规则，
// 只在当前列表内生效，不向 profile 传播（聚合时"拦"优先于"放"）。
type Set struct {
	block map[string]struct{}
	allow map[string]struct{}
	stats Stats
}

func NewSet() *Set {
	return &Set{
		block: make(map[string]struct{}, 1024),
		allow: make(map[string]struct{}, 64),
	}
}

func (s *Set) Stats() Stats { return s.stats }

func (s *Set) Add(domain string) {
	if _, ok := s.block[domain]; ok {
		s.stats.Dup++
		return
	}
	s.block[domain] = struct{}{}
	s.stats.Block++
}

func (s *Set) AddAllow(domain string) {
	s.allow[domain] = struct{}{}
	s.stats.Allow++
}

// Merge 把 other 的拦截项并入（不并入白名单），用于 profile 聚合。
func (s *Set) Merge(other *Set) {
	for d := range other.block {
		if _, ok := s.block[d]; ok {
			s.stats.Dup++
			continue
		}
		s.block[d] = struct{}{}
		s.stats.Block++
	}
}

// Domains 返回去重并排好序的域名切片。
func (s *Set) Domains() []string {
	out := make([]string, 0, len(s.block))
	for d := range s.block {
		if _, bad := s.allow[d]; bad {
			continue
		}
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

var badSuffixes = []string{
	"localhost", "localdomain", "localhost.localdomain", "local",
	"lan", "home.arpa", "in-addr.arpa", "ip6.arpa", "broadcasthost",
	"onion", "test", "invalid", "example",
}

// LoadFile 解析一个 hosts / adblock / dnsmasq 混合格式的规则文件。
func LoadFile(path string, stripWWW bool) (*Set, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	set := NewSet()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 4<<20)
	first := true
	for sc.Scan() {
		line := sc.Text()
		if first {
			line = strings.TrimPrefix(line, "\ufeff")
			first = false
		}
		set.stats.Lines++
		domains, allow, ok := NormalizeAll(line, stripWWW)
		if !ok {
			if strings.TrimSpace(line) != "" && !isComment(line) {
				set.stats.Invalid++
			}
			continue
		}
		for _, domain := range domains {
			if allow {
				set.AddAllow(domain)
			} else {
				set.Add(domain)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return set, nil
}

func isComment(line string) bool {
	s := strings.TrimSpace(line)
	for _, p := range []string{"#", "!", ";", "[", "//"} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// NormalizeAll 把一行规则归一化成裸域名（一行可能含多个域名）。
// 支持：hosts(IP domain [domain...])、adblock(||domain^ / @@||domain^)、dnsmasq(address=/domain/0.0.0.0)、纯域名。
func NormalizeAll(line string, stripWWW bool) (domains []string, allow bool, ok bool) {
	s := strings.TrimSpace(line)
	if s == "" || isComment(s) {
		return nil, false, false
	}

	switch {
	case strings.HasPrefix(s, "address=/"):
		parts := strings.SplitN(s, "/", 4)
		if len(parts) < 3 {
			return nil, false, false
		}
		s = parts[1]
	case strings.HasPrefix(s, "server=/"):
		return nil, false, false
	}

	if strings.HasPrefix(s, "@@") {
		allow = true
		s = s[2:]
	}

	var candidates []string
	switch {
	case strings.HasPrefix(s, "||"):
		s = s[2:]
		if i := strings.IndexAny(s, "^$/"); i >= 0 {
			s = s[:i]
		}
		s = strings.TrimPrefix(s, "*.")
		s = strings.TrimPrefix(s, ".")
		candidates = []string{s}
	case strings.Contains(s, "##") || strings.Contains(s, "#@#") || strings.Contains(s, "#?#"):
		return nil, false, false
	default:
		// hosts 行 / 纯域名行，先砍行内注释再取字段
		if i := strings.IndexByte(s, '#'); i > 0 {
			s = s[:i]
		}
		fields := strings.Fields(s)
		switch {
		case len(fields) == 0:
			return nil, false, false
		case len(fields) == 1:
			candidates = []string{fields[0]}
		default:
			if net.ParseIP(fields[0]) == nil {
				return nil, false, false
			}
			candidates = fields[1:]
		}
	}

	for _, c := range candidates {
		if stripWWW {
			c = strings.TrimPrefix(c, "www.")
		}
		if d, valid := CleanDomain(c); valid {
			domains = append(domains, d)
		}
	}
	if len(domains) == 0 {
		return nil, false, false
	}
	return domains, allow, true
}

// CleanDomain 校验并归一化域名：小写、去尾点、拒绝非法字符/IP/本地域名。
func CleanDomain(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimSuffix(s, ".")
	if len(s) < 4 || len(s) > 253 {
		return "", false
	}
	if utf8.RuneCountInString(s) != len(s) || !isASCII(s) {
		return "", false // 明确放弃 IDN，避免引入 punycode 依赖
	}
	if strings.ContainsAny(s, "/\\*#$@!^|[]{}()<>?=,;:'\"`~%&+ \t") {
		return "", false
	}
	if strings.Contains(s, "..") || !strings.Contains(s, ".") {
		return "", false
	}
	if net.ParseIP(s) != nil {
		return "", false
	}
	for _, bad := range badSuffixes {
		if s == bad || strings.HasSuffix(s, "."+bad) {
			return "", false
		}
	}
	labels := strings.Split(s, ".")
	for _, l := range labels {
		if l == "" || len(l) > 63 {
			return "", false
		}
		if l[0] == '-' || l[len(l)-1] == '-' {
			return "", false
		}
		for i := 0; i < len(l); i++ {
			c := l[i]
			if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
				continue
			}
			return "", false
		}
	}
	tld := labels[len(labels)-1]
	allDigit := true
	for i := 0; i < len(tld); i++ {
		if tld[i] < '0' || tld[i] > '9' {
			allDigit = false
			break
		}
	}
	if allDigit {
		return "", false
	}
	return s, true
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// HashDomain 计算 FNV-1a 32 位哈希，ESP32 端必须使用完全相同的实现。
func HashDomain(s string) uint32 {
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}
