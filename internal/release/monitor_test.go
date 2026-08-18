// 2026-07-14: Этап 14 v8 — tests for the release-monitor.

package release

import (
	"strings"
	"testing"
)

func TestCompareSemver(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v0.10.7", "v0.10.7", 0},
		{"v0.10.7", "v0.10.8", -1},
		{"v0.10.8", "v0.10.7", +1},
		{"v0.10.7", "0.10.7", 0},  // v prefix is optional
		{"0.10.7", "v0.10.8", -1},
		{"v1.0.0", "v0.99.99", +1},
		{"v0.10.8-dev.1", "v0.10.8", -1}, // pre-release < release
		{"v0.10.8", "v0.10.8-dev.1", +1},
		{"v0.10.8-dev.1", "v0.10.8-dev.2", -1},
		{"v0.10.8-dev.2", "v0.10.8-dev.1", +1},
		{"v0.10.8-dev.5", "v0.10.8-dev.10", -1}, // lexical
		{"v1.0", "v1.0.0", 0},                    // missing patch == 0
		{"v1", "v1.0.0", 0},                       // missing minor.patch == 0
		{"v2", "v1.99.99", +1},
		{"v0.10.8-rc.1", "v0.10.7", +1},
		// 2026-08-18: B128 — 4-component versions (skygate
		// v1.3.12+ convention: x.y.z.w). The pre-B128 code
		// ignored the 4th component, so v1.3.19.2 == v1.3.19.4
		// and the layout banner on every admin page would show
		// the wrong "latest" version. After B128 the 4th
		// component is included in the compare.
		{"v1.3.19.2", "v1.3.19.2", 0},
		{"v1.3.19.2", "v1.3.19.1", +1},    // sub-patch bump
		{"v1.3.19.1", "v1.3.19.2", -1},
		{"v1.3.19.4", "v1.3.19.2", +1},    // the live B128 trigger
		{"v1.3.19.2", "v1.3.19.4", -1},
		{"v1.3.19", "v1.3.19.0", 0},       // implicit .0 padding
	}
	for _, c := range cases {
		got := CompareSemver(c.a, c.b)
		if got != c.want {
			t.Errorf("CompareSemver(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestFormatAlert(t *testing.T) {
	cur := &Release{TagName: "v0.10.7"}
	latest := &Release{
		TagName:     "v0.10.8",
		Name:        "Skygate v0.10.8",
		HTMLURL:     "https://github.com/BarsSky/skygate/releases/tag/v0.10.8",
		PublishedAt: "2026-07-15T10:00:00Z",
		Body:        "## What's new\n\nButler voice v2...",
	}
	out := FormatAlert(cur, latest)
	if out == "" {
		t.Fatal("FormatAlert returned empty")
	}
	for _, want := range []string{"v0.10.7", "v0.10.8", "2026-07-15", "github.com/BarsSky/skygate/releases/tag/v0.10.8"} {
		if !contains(out, want) {
			t.Errorf("FormatAlert missing %q in:\n%s", want, out)
		}
	}
}

func TestFormatAlertTruncatesLongBody(t *testing.T) {
	cur := &Release{TagName: "v0.10.7"}
	longBody := strings.Repeat("X", 2000)
	latest := &Release{
		TagName:     "v0.10.8",
		HTMLURL:     "https://github.com/BarsSky/skygate/releases/tag/v0.10.8",
		PublishedAt: "2026-07-15T10:00:00Z",
		Body:        longBody,
	}
	out := FormatAlert(cur, latest)
	if len(out) > 1500 {
		t.Errorf("FormatAlert too long (%d bytes); expected truncation at ~800", len(out))
	}
	if !contains(out, "…") {
		t.Errorf("FormatAlert should have truncation ellipsis when body > 800")
	}
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}
