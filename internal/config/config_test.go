package config

import (
	"reflect"
	"testing"
	"time"
)

func TestDeriveControlURL(t *testing.T) {
	cases := []struct {
		explicit, hsAPI, want string
	}{
		// Explicit wins
		{"https://head.skynas.ru", "http://foo:50444", "https://head.skynas.ru"},
		{"https://head.skynas.ru/", "http://foo:50444", "https://head.skynas.ru"}, // trailing slash stripped

		// Fallback: strip /api/v1
		{"", "https://hs.example.com/api/v1", "https://hs.example.com"},
		// Fallback: strip /api
		{"", "https://hs.example.com/api", "https://hs.example.com"},
		// Fallback: http → https
		{"", "http://hs.local:50444", "https://hs.local:50444"},
		// Fallback: bare host
		{"", "hs.local:50444", "https://hs.local:50444"},
	}
	for _, c := range cases {
		got := deriveControlURL(c.explicit, c.hsAPI)
		if got != c.want {
			t.Errorf("deriveControlURL(%q, %q) = %q want %q", c.explicit, c.hsAPI, got, c.want)
		}
	}
}

func TestParseUserLimits(t *testing.T) {
	cases := []struct {
		in   string
		want map[string]int
	}{
		{"", map[string]int{}},
		{"alice:100", map[string]int{"alice": 100}},
		{"alice:100,bob:200", map[string]int{"alice": 100, "bob": 200}},
		{"alice : 100 , bob : 200", map[string]int{"alice": 100, "bob": 200}}, // spaces trimmed
		// malformed entries silently dropped
		{"alice:0", map[string]int{}},    // n<=0 dropped
		{"alice:-1", map[string]int{}},   // n<=0 dropped
		{"alice:not-a-number", map[string]int{}},
		{"alice", map[string]int{}},      // no colon
		{",,alice:5,", map[string]int{"alice": 5}},
	}
	for _, c := range cases {
		got := parseUserLimits(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("parseUserLimits(%q) = %v want %v", c.in, got, c.want)
		}
	}
}

func TestGetEnvHelpers(t *testing.T) {
	t.Setenv("XPORT", "1234")
	if getenv("XPORT", "8080") != "1234" {
		t.Error("getenv returned default instead of env value")
	}
	if getenv("NOPE_XYZ", "8080") != "8080" {
		t.Error("getenv did not return default when env unset")
	}

	if getInt("NOPE_ABC", 42) != 42 {
		t.Error("getInt default fail")
	}
	t.Setenv("NUM_X", "10")
	if getInt("NUM_X", 42) != 10 {
		t.Error("getInt env fail")
	}
	t.Setenv("NUM_BOGUS", "not-num")
	if getInt("NUM_BOGUS", 42) != 42 {
		t.Error("getInt should ignore non-numeric env and keep default")
	}

	// getDuration: time.ParseDuration
	if getDuration("NOPE_D", 30*time.Second) != 30*time.Second {
		t.Error("getDuration default fail")
	}
	t.Setenv("D_X", "5m")
	if getDuration("D_X", 30*time.Second) != 5*time.Minute {
		t.Error("getDuration parse fail")
	}
	// getDuration: int fallback to seconds
	t.Setenv("D_INT", "90")
	if getDuration("D_INT", 30*time.Second) != 90*time.Second {
		t.Error("getDuration int fallback fail")
	}
	// getDuration: invalid stays default
	t.Setenv("D_BAD", "garbage")
	if getDuration("D_BAD", 30*time.Second) != 30*time.Second {
		t.Error("getDuration should fall back on garbage input")
	}
}
