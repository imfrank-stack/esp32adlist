package rules

import "testing"

func TestNormalizeAll(t *testing.T) {
	cases := []struct {
		in    string
		want  []string
		allow bool
		ok    bool
	}{
		{"0.0.0.0 ads.example.com", []string{"ads.example.com"}, false, true},
		{"127.0.0.1 a.example.com b.example.com", []string{"a.example.com", "b.example.com"}, false, true},
		{"||tracker.example.com^", []string{"tracker.example.com"}, false, true},
		{"||tracker.example.com^$third-party", []string{"tracker.example.com"}, false, true},
		{"@@||good.example.com^", []string{"good.example.com"}, true, true},
		{"address=/ads.example.com/0.0.0.0", []string{"ads.example.com"}, false, true},
		{"www.example.com", []string{"example.com"}, false, true},
		{"||*.example.com^", []string{"example.com"}, false, true},
		{"0.0.0.0 example.com # inline", []string{"example.com"}, false, true},
		{"# comment", nil, false, false},
		{"! adblock comment", nil, false, false},
		{"||example.com^$replace=/x/y/", []string{"example.com"}, false, true},
		{"||example.com/path", []string{"example.com"}, false, true},
		{"example.com##.banner", nil, false, false},
		{"127.0.0.1 localhost", nil, false, false},
		{"0.0.0.0 127.0.0.1", nil, false, false},
		{"0.0.0.0 bad_domain..com", nil, false, false},
		{"0.0.0.0 -bad.example.com", nil, false, false},
	}
	for _, c := range cases {
		got, allow, ok := NormalizeAll(c.in, true)
		if ok != c.ok {
			t.Fatalf("%q: ok=%v want %v", c.in, ok, c.ok)
		}
		if allow != c.allow {
			t.Fatalf("%q: allow=%v want %v", c.in, allow, c.allow)
		}
		if len(got) != len(c.want) {
			t.Fatalf("%q: got %v want %v", c.in, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("%q: got %v want %v", c.in, got, c.want)
			}
		}
	}
}

func TestHashDomainStable(t *testing.T) {
	// 官方 FNV-1a 32 位测试向量；ESP32 固件必须与之完全一致
	cases := map[string]uint32{
		"":       2166136261, // offset basis
		"a":      0xe40c292c,
		"foobar": 0xbf9cf968,
	}
	for d, want := range cases {
		if got := HashDomain(d); got != want {
			t.Fatalf("%q: got %d (0x%08x) want %d (0x%08x)", d, got, got, want, want)
		}
	}
}
