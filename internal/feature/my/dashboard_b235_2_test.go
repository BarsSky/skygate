package my

// dashboard_b235_2_test.go — v1.5.2 (B235.2) — unit tests
// for the region_code-empty fallback in bestHealthyDERP.
//
// Pre-B235.2: the bundled controlplane DERP
// (region_id=901, is_own=1) had region_code='' in
// derp_relays. bestHealthyDERP returned an empty
// string, and the template's
// `{{if .TailnetMetrics.ActiveDERP}}` evaluated to
// false, so the hero rendered "—" even though the
// row had a healthy latency (e.g. 108 ms).
//
// B235.2 fix: shortHostLabel(host) maps
// "controlplane.tailscale.com" → "cdn" and other
// hosts → their first label.

import "testing"

func TestShortHostLabel(t *testing.T) {
	cases := []struct {
		host string
		want string
	}{
		{"controlplane.tailscale.com", "cdn"},
		{"derp22b.tailscale.com", "derp22b"},
		{"derp1f.tailscale.com", "derp1f"},
		{"", ""},
		{"nodot", "nodot"},
		{"a.b.c", "a"},
	}
	for _, c := range cases {
		got := shortHostLabel(c.host)
		if got != c.want {
			t.Errorf("shortHostLabel(%q) = %q, want %q", c.host, got, c.want)
		}
	}
}
