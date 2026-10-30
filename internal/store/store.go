package store

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"esp32adlist/internal/rules"
)

// List 是加载到内存中的一个列表。
type List struct {
	Info     rules.ListInfo
	Text     []byte
	HashRaw  []byte
	Hashes   []uint32
	domPages []int // 每页起始字节偏移，长度 = 页数 + 1
	hshPages []int

	ManifestVersion string // 所属 manifest 的版本号，写进 hosts 头部注释

	hostsOnce sync.Once
	hostsBuf  []byte // 懒生成的标准 hosts 格式
}

// HostsText 返回标准 hosts 格式（`0.0.0.0 域名`），可直接粘进
// AdGuard Home / Pi-hole / OpenClash / 系统 hosts。首次访问时生成并缓存。
func (l *List) HostsText() []byte {
	l.hostsOnce.Do(func() {
		var b bytes.Buffer
		fmt.Fprintf(&b, "# esp32adlist · %s（%s）\n", l.Info.Title, l.Info.Name)
		fmt.Fprintf(&b, "# %d 个域名 · 版本 %s · SHA256 %s\n", l.Info.Count, l.ManifestVersion, l.Info.SHA256Domains[:12])
		b.WriteByte('\n')

		const prefix = "0.0.0.0 "
		b.Grow(len(l.Text) + len(l.Text)/16)
		start := 0
		for i := 0; i <= len(l.Text); i++ {
			if i == len(l.Text) || l.Text[i] == '\n' {
				if i > start {
					b.WriteString(prefix)
					b.Write(l.Text[start:i])
					b.WriteByte('\n')
				}
				start = i + 1
			}
		}
		l.hostsBuf = b.Bytes()
	})
	return l.hostsBuf
}

// Store 是全部列表的只读快照。
type Store struct {
	Manifest rules.Manifest
	Dir      string
	Lists    map[string]*List
	Order    []string
	ModTime  time.Time
}

// PageBytes 返回文本页内容。
func (l *List) PageBytes(page int) []byte {
	if page < 0 || page+1 >= len(l.domPages) {
		return nil
	}
	return l.Text[l.domPages[page]:l.domPages[page+1]]
}

// HashPage 返回二进制哈希页。
func (l *List) HashPage(page int) []byte {
	if page < 0 || page+1 >= len(l.hshPages) {
		return nil
	}
	return l.HashRaw[l.hshPages[page]:l.hshPages[page+1]]
}

// Lookup 做后缀匹配：a.b.example.com → 依次试 a.b.example.com / b.example.com / example.com。
func (l *List) Lookup(domain string) (bool, string) {
	d := strings.ToLower(strings.Trim(domain, "."))
	for {
		h := rules.HashDomain(d)
		i := sort.Search(len(l.Hashes), func(i int) bool { return l.Hashes[i] >= h })
		if i < len(l.Hashes) && l.Hashes[i] == h {
			return true, d
		}
		idx := strings.IndexByte(d, '.')
		if idx < 0 {
			return false, ""
		}
		d = d[idx+1:]
		if !strings.Contains(d, ".") {
			return false, ""
		}
	}
}

// Load 从目录加载 manifest 与全部列表文件。
func Load(dir string) (*Store, error) {
	mb, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, fmt.Errorf("读取 manifest 失败: %w", err)
	}
	var m rules.Manifest
	if err := json.Unmarshal(mb, &m); err != nil {
		return nil, fmt.Errorf("解析 manifest 失败: %w", err)
	}

	s := &Store{Manifest: m, Dir: dir, Lists: make(map[string]*List, len(m.Lists))}
	for _, info := range m.Lists {
		l, err := loadList(dir, info, m.Version)
		if err != nil {
			return nil, err
		}
		s.Lists[info.Name] = l
		s.Order = append(s.Order, info.Name)
	}
	if st, err := os.Stat(filepath.Join(dir, "manifest.json")); err == nil {
		s.ModTime = st.ModTime()
	}
	return s, nil
}

func loadList(dir string, info rules.ListInfo, version string) (*List, error) {
	text, err := os.ReadFile(filepath.Join(dir, info.Name+".txt"))
	if err != nil {
		return nil, fmt.Errorf("读取 %s.txt 失败: %w", info.Name, err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, info.Name+".u32"))
	if err != nil {
		return nil, fmt.Errorf("读取 %s.u32 失败: %w", info.Name, err)
	}
	hashes := make([]uint32, len(raw)/4)
	for i := range hashes {
		hashes[i] = binary.LittleEndian.Uint32(raw[i*4:])
	}

	l := &List{Info: info, Text: text, HashRaw: raw, Hashes: hashes, ManifestVersion: version}
	l.domPages = pageOffsets(text, rules.DefaultDomainPageSize)
	l.hshPages = hashOffsets(len(raw), rules.DefaultHashPageSize*rules.HashBytes)
	return l, nil
}

func pageOffsets(text []byte, perPage int) []int {
	off := make([]int, 0, len(text)/(perPage*12)+2)
	off = append(off, 0)
	lines := 0
	for i, b := range text {
		if b != '\n' {
			continue
		}
		lines++
		if lines%perPage == 0 && i+1 < len(text) {
			off = append(off, i+1)
		}
	}
	off = append(off, len(text))
	return off
}

func hashOffsets(size, step int) []int {
	off := make([]int, 0, size/step+2)
	for p := 0; p < size; p += step {
		off = append(off, p)
	}
	off = append(off, size)
	return off
}

// Snapshot 提供无锁读取 + 热更新的能力。
type Snapshot struct {
	v atomic.Pointer[Store]
}

func (s *Snapshot) Get() *Store   { return s.v.Load() }
func (s *Snapshot) Set(st *Store) { s.v.Store(st) }

// Watch 每 interval 检查 manifest 修改时间，变化则重新加载。
func (s *Snapshot) Watch(dir string, interval time.Duration) {
	go func() {
		for range time.Tick(interval) {
			st, err := os.Stat(filepath.Join(dir, "manifest.json"))
			if err != nil {
				continue
			}
			cur := s.Get()
			if cur != nil && !st.ModTime().After(cur.ModTime) {
				continue
			}
			if next, err := Load(dir); err == nil {
				s.Set(next)
				fmt.Printf("[reload] 已加载 %s，%d 个列表\n", next.Manifest.Version, len(next.Manifest.Lists))
			}
		}
	}()
}
