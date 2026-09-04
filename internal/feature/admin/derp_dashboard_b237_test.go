package admin

// derp_dashboard_b237_test.go — v1.5.2+ (B237) —
// unit tests for the /admin/derp/relays/derpmap.json
// endpoint helpers.
//
// Coverage:
//   - shortNameFromHostname: 5 cases (with region_code,
//     without, dot, empty, IDN-like)
//   - publicDERPPortFromURL: 6 cases (no port = 443,
//     explicit :443, explicit :8443, malformed URL,
//     empty URL, port is not a number)
//
// The handler itself (GetAdminDerpRelaysDerpmap) is
// covered by an end-to-end smoke test that boots a
// Postgres-free SQL stub via the existing derphealth
// helper — see scripts/b237_verify.sh (live e2e on
// the agent).

import "testing"

func TestShortNameFromHostname(t *testing.T) {
	cases := []struct {
		host, rc, want string
	}{
		{"derp.skynas.ru", "mow", "mow-1"},
		{"derp22b.tailscale.com", "waw", "waw-1"},
		{"controlplane.tailscale.com", "", "controlplane"},
		// No region_code, hostname is the FQDN: take the
		// first label.
		{"derp.skynas.ru", "", "derp"},
		// Empty hostname: returns empty (caller should
		// handle).
		{"", "mow", "mow-1"},
		// Single-label hostname (no dots).
		{"mynode", "xxx", "xxx-1"},
		// Hostname IS the first label.
		{"mynode", "", "mynode"},
	}
	for _, c := range cases {
		got := shortNameFromHostname(c.host, c.rc)
		if got != c.want {
			t.Errorf("shortNameFromHostname(%q, %q) = %q, want %q", c.host, c.rc, got, c.want)
		}
	}
}

func TestPublicDERPPortFromURL(t *testing.T) {
	cases := []struct {
		url  string
		want int
	}{
		// Default = 443 when no explicit port.
		{"https://derp.skynas.ru", 443},
		{"http://derp.skynas.ru", 443},
		// Explicit :443.
		{"https://derp.skynas.ru:443", 443},
		// Explicit non-default port (the B237 case: derper
		// runs on 8443 internally, NPM forwards public :443).
		{"https://derp.skynas.ru:8443", 8443},
		// http with :80 should still be 80 (not 443).
		{"http://example.com:80", 80},
		// Malformed URL: default 443.
		{"not a url", 443},
		// Empty URL: default 443.
		{"", 443},
		// Port is not a number: default 443.
		{"https://derp.skynas.ru:abc", 443},
		// With path.
		{"https://derp.skynas.ru:8443/some/path", 8443},
	}
	for _, c := range cases {
		got := publicDERPPortFromURL(c.url)
		if got != c.want {
			t.Errorf("publicDERPPortFromURL(%q) = %d, want %d", c.url, got, c.want)
		}
	}
}
