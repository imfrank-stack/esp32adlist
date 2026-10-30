package rules

import (
	"encoding/json"
	"fmt"
	"os"
)

// Source 配置中的一个原始规则源。
type Source struct {
	Name        string   `json:"name"`
	Title       string   `json:"title"`
	Category    string   `json:"category"`
	Description string   `json:"description"`
	Files       []string `json:"files"`
	URLs        []string `json:"urls"`
	StripWWW    *bool    `json:"strip_www,omitempty"`
}

// Profile 是给终端（ESP32）直接用的聚合列表。
type Profile struct {
	Name        string   `json:"name"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Members     []string `json:"members"`
}

type Config struct {
	Sources  []Source  `json:"sources"`
	Profiles []Profile `json:"profiles"`
}

func LoadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w", path, err)
	}
	seen := map[string]bool{}
	for _, s := range c.Sources {
		if s.Name == "" || len(s.Files) == 0 {
			return nil, fmt.Errorf("source %q 缺少 name/files", s.Name)
		}
		if seen[s.Name] {
			return nil, fmt.Errorf("重名列表 %q", s.Name)
		}
		seen[s.Name] = true
	}
	for _, p := range c.Profiles {
		if seen[p.Name] {
			return nil, fmt.Errorf("profile 与 source 重名: %q", p.Name)
		}
		seen[p.Name] = true
		for _, m := range p.Members {
			if !seen[m] {
				return nil, fmt.Errorf("profile %q 引用了不存在的列表 %q", p.Name, m)
			}
		}
	}
	return &c, nil
}
