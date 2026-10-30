package rules

const (
	SchemaVersion         = 1
	DefaultDomainPageSize = 512
	DefaultHashPageSize   = 1024
	HashAlgo              = "fnv1a32"
	HashEndian            = "le"
	HashBytes             = 4
)

// HashSpec 描述二进制哈希表的格式，ESP32 端按此解析。
type HashSpec struct {
	Algo   string `json:"algo"`
	Endian string `json:"endian"`
	Bytes  int    `json:"bytes"`
}

// ListInfo 是一个可请求的列表（原始源或聚合 profile）。
type ListInfo struct {
	Name          string   `json:"name"`
	Title         string   `json:"title"`
	Kind          string   `json:"kind"` // source | profile
	Category      string   `json:"category"`
	Description   string   `json:"description"`
	Sources       []string `json:"sources,omitempty"`
	Members       []string `json:"members,omitempty"`
	Count         int      `json:"count"`
	DomainBytes   int64    `json:"domain_bytes"`
	HashBytes     int64    `json:"hash_bytes"`
	DomainPages   int      `json:"domain_pages"`
	HashPages     int      `json:"hash_pages"`
	SHA256Domains string   `json:"sha256_domains"`
	SHA256Hashes  string   `json:"sha256_hashes"`
	Stats         *Stats   `json:"stats,omitempty"`
}

// Manifest 是 /api/v1/manifest 的返回体，同时落盘为 data/dist/manifest.json。
type Manifest struct {
	Schema         int        `json:"schema"`
	Version        string     `json:"version"`
	Generated      string     `json:"generated"`
	Hash           HashSpec   `json:"hash"`
	DomainPageSize int        `json:"domain_page_size"`
	HashPageSize   int        `json:"hash_page_size"`
	TotalUnique    int        `json:"total_unique_domains"`
	Lists          []ListInfo `json:"lists"`
}
