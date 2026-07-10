package headscale

import (
	"testing"
	"time"
)

func TestParseDuration_Formats(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
		err  bool
	}{
		{"30s", 30 * time.Second, false},
		{"5m", 5 * time.Minute, false},
		{"1h", time.Hour, false},
		{"24h", 24 * time.Hour, false},
		{"1h30m", 90 * time.Minute, false},
		// RFC3339 in the future
		{"2030-01-01T00:00:00Z", 0, false}, // exact value depends on test time, just check no err
		// garbage
		{"yesterday", 0, true},
		{"", 0, true},
	}
	for _, c := range cases {
		got, err := parseDuration(c.in)
		if (err != nil) != c.err {
			t.Errorf("parseDuration(%q) err=%v want err=%v", c.in, err, c.err)
			continue
		}
		if c.err {
			continue
		}
		// for RFC3339 input, just assert >= 0 (future relative to test now)
		if c.in == "2030-01-01T00:00:00Z" {
			// it's a fixed future timestamp; check sign/gross size
			if got < 0 || got > 100*365*24*time.Hour {
				t.Errorf("parseDuration(%q) = %v; out of plausible range", c.in, got)
			}
			continue
		}
		if got != c.want {
			t.Errorf("parseDuration(%q) = %v want %v", c.in, got, c.want)
		}
	}
}

func TestDurationFlag_Formats(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{time.Hour, "1h"},
		{24 * time.Hour, "24h"},
		{30 * time.Minute, "30m"},
		{5 * time.Minute, "5m"},
		{time.Minute, "1m"},
		{45 * time.Second, "45s"},
		{0, "0s"},
		// 1h30m falls into the minutes branch (90m). That's the documented
		// behavior of durationFlag — verify it stays stable.
		{90 * time.Minute, "90m"},
	}
	for _, c := range cases {
		got := durationFlag(c.in)
		if got != c.want {
			t.Errorf("durationFlag(%v) = %q want %q", c.in, got, c.want)
		}
	}
}

func TestHasExitNodeTag(t *testing.T) {
	cases := []struct {
		name  string
		tags  []string
		routes []string
		want  bool
	}{
		{"empty node", nil, nil, false},
		// explicit tag
		{"tag:exit-node", []string{"tag:exit-node"}, nil, true},
		{"tag:exit-node uppercase", []string{"TAG:EXIT-NODE"}, nil, true},
		{"other tag", []string{"tag:something-else"}, nil, false},
		// name-based
		{"exit-karolina", nil, nil, true},
		{"exitnode-foo", nil, nil, true},
		{"EXIT-Bar", nil, nil, true},
		{"not-exit-baz", nil, nil, false},
		// route-based (0.29.1 detection)
		{"advertises 0.0.0.0/0", nil, []string{"0.0.0.0/0"}, true},
		{"advertises ::/0", nil, []string{"::/0"}, true},
		{"advertises only 10.0.0.0/8", nil, []string{"10.0.0.0/8"}, false},
	}
	for _, c := range cases {
		got := hasExitNodeTag(c.tags, c.name, c.routes)
		if got != c.want {
			t.Errorf("hasExitNodeTag(name=%q tags=%v routes=%v) = %v want %v",
				c.name, c.tags, c.routes, got, c.want)
		}
	}
}

func TestIsPublicAndPrivate(t *testing.T) {
	pub := NodeView{Tags: []string{"tag:public"}}
	if !pub.IsPublicView() {
		t.Error("tag:public should be IsPublicView")
	}
	if pub.IsPrivateView() {
		t.Error("tag:public should NOT be IsPrivateView")
	}
	priv := NodeView{Tags: []string{"tag:private"}}
	if priv.IsPublicView() {
		t.Error("tag:private should NOT be IsPublicView")
	}
	if !priv.IsPrivateView() {
		t.Error("tag:private should be IsPrivateView")
	}
	none := NodeView{Tags: []string{"tag:other"}}
	if none.IsPublicView() || none.IsPrivateView() {
		t.Error("unrelated tag should fail both")
	}
	// case-insensitive
	upPub := NodeView{Tags: []string{"TAG:Public"}}
	if !upPub.IsPublicView() {
		t.Error("uppercase TAG:Public should still match (EqualFold)")
	}
}

func TestGetenvDefault(t *testing.T) {
	t.Setenv("XHS_TEST_KEY", "real")
	if got := getenvDefault("XHS_TEST_KEY", "default"); got != "real" {
		t.Errorf("got %q want real", got)
	}
	if got := getenvDefault("XHS_MISSING_KEY", "default"); got != "default" {
		t.Errorf("got %q want default", got)
	}
	t.Setenv("XHS_EMPTY_KEY", "")
	if got := getenvDefault("XHS_EMPTY_KEY", "default"); got != "default" {
		t.Errorf("empty env should fall back to default, got %q", got)
	}
}

// HSNode.IsPublic has identical semantics to NodeView.IsPublicView — sanity check.
func TestHSNodeIsPublic(t *testing.T) {
	n := HSNode{Tags: []string{"tag:public"}}
	if !n.IsPublic() {
		t.Error("HSNode with tag:public should be IsPublic")
	}
}
