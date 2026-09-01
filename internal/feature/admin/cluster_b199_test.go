// v1.5.0+ / B199 — unit tests for the /admin/cluster
// helpers. Phase 2.1 is read-only; the helper tests cover
// the 3 pure-shape parsers (parsePGTextArray,
// parseClusterChain, abbreviateClusterTime) so any drift
// from the expected shape gets caught at unit-test time
// rather than via a blank page in the browser.

package admin

import (
	"testing"
)

func TestParsePGTextArray(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"empty braces", "{}", nil},
		{"null literal", "NULL", nil},
		{"single value", "{a}", []string{"a"}},
		{"multiple values", "{a,b,c}", []string{"a", "b", "c"}},
		{"with spaces", "{a, b, c}", []string{"a", "b", "c"}},
		{"quoted", `{"a","b"}`, []string{"a", "b"}},
		{"empty inner", "{}", nil},
		{"comma-only", "{,}", nil},
		{"trailing comma", "{a,}", []string{"a"}},
		{"non-literal (raw string)", "skyadmin", []string{"skyadmin"}},
		// Real-world cluster_node.roles values (post-B195 insert):
		{"roles example", "{skygate,patroni-primary}", []string{"skygate", "patroni-primary"}},
		{"single role", "{skygate}", []string{"skygate"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parsePGTextArray(c.in)
			if !equalSlices(got, c.want) {
				t.Errorf("parsePGTextArray(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestParseClusterChain(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		// wantCount is the expected number of member lines.
		// 0 = the page renders the "(no chain)" empty state.
		wantCount int
	}{
		{"nil", nil, 0},
		{"empty array", []byte("[]"), 0},
		{"null literal", []byte("null"), 0},
		{"one member", []byte(`[{"hostname":"a","priority":1}]`), 1},
		{"two members", []byte(`[{"hostname":"a","priority":1},{"hostname":"b","priority":2}]`), 2},
		{"malformed", []byte(`not json`), 1}, // returns raw as one line
		{"empty object array", []byte(`[{}]`), 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseClusterChain(c.in)
			if len(got) != c.wantCount {
				t.Errorf("parseClusterChain(%q) = %d lines, want %d", c.in, len(got), c.wantCount)
			}
		})
	}
}

func TestAbbreviateClusterTime(t *testing.T) {
	cases := []struct {
		delta int64
		want  string
	}{
		{0, "0s ago"},
		{30, "30s ago"},
		{59, "59s ago"},
		{60, "1m ago"},
		{90, "1m ago"},
		{3600, "1h ago"},
		{7200, "2h ago"},
		{86400, "1d ago"},
		{172800, "2d ago"},
		{-30, "in 30s"},  // future — clock skew
		{-3600, "in 3600s"},
	}
	for _, c := range cases {
		got := abbreviateClusterTime(c.delta)
		if got != c.want {
			t.Errorf("abbreviateClusterTime(%d) = %q, want %q", c.delta, got, c.want)
		}
	}
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
