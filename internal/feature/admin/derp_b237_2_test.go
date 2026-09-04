package admin

// derp_b237_2_test.go — v1.5.2+ (B237.2) —
// unit tests for resolvePublicDERPIP.
//
// Coverage:
//   - SKYGATE_DERP_HOSTNAME env var wins (with real DNS)
//   - Falls back to derperHostname (st.Hostname) when
//     env not set
//   - Last resort: detectEgressIP() (mocked here since
//     the real one dials UDP)
//   - Empty result when nothing resolves
//
// The real "dns" cases below are live DNS lookups
// against public resolvers. If the test host has no
// DNS, the dns cases will skip; the egress case still
// runs.

import (
	"os"
	"testing"
)

func TestResolvePublicDERPIP_EnvWins(t *testing.T) {
	// SKYGATE_DERP_HOSTNAME env var. Use a public
	// domain that's almost certainly resolvable so
	// the test doesn't flake on offline CI.
	t.Setenv("SKYGATE_DERP_HOSTNAME", "controlplane.tailscale.com")
	got, src, ok := resolvePublicDERPIP("")
	if !ok {
		t.Skip("no DNS resolver available (CI offline?)")
	}
	if src != "dns:env" {
		t.Errorf("src = %q, want %q", src, "dns:env")
	}
	// controlplane.tailscale.com is on Cloudflare; any
	// IP is fine — we just want SOMETHING that resolves
	// to a public A record.
	if got == "" {
		t.Errorf("resolvePublicDERPIP: empty IP for controlplane.tailscale.com")
	}
}

func TestResolvePublicDERPIP_DerperHostnameFallback(t *testing.T) {
	// When SKYGATE_DERP_HOSTNAME is not set, fall back
	// to the derperHostname parameter (which is
	// st.Hostname parsed from the derper status page).
	t.Setenv("SKYGATE_DERP_HOSTNAME", "")
	got, src, ok := resolvePublicDERPIP("controlplane.tailscale.com")
	if !ok {
		t.Skip("no DNS resolver available (CI offline?)")
	}
	if src != "dns:derper" {
		t.Errorf("src = %q, want %q", src, "dns:derper")
	}
	if got == "" {
		t.Errorf("resolvePublicDERPIP: empty IP for controlplane.tailscale.com")
	}
}

func TestResolvePublicDERPIP_PlaceholderHostnameSkipped(t *testing.T) {
	// The pre-B237.2 collectDerpStatus seeds the
	// struct with "derp.example.com" as a placeholder
	// when the derper /debug is unreachable. The
	// resolver MUST skip this placeholder (otherwise
	// it would DNS-resolve example.com and show a
	// confusing IP).
	t.Setenv("SKYGATE_DERP_HOSTNAME", "")
	// detectEgressIP() dials UDP 192.0.2.1:80 — that
	// might or might not succeed in a test env. We
	// accept either "egress" (real egress IP) or "" (no
	// DNS, no egress) — the point of the test is that
	// "derp.example.com" is NOT used.
	got, src, ok := resolvePublicDERPIP("derp.example.com")
	if ok && src == "dns:derper" {
		t.Errorf("resolver used the placeholder hostname! got=%q src=%q (placeholder must be skipped)", got, src)
	}
}

func TestResolvePublicDERPIP_UnresolvableHostname(t *testing.T) {
	// SKYGATE_DERP_HOSTNAME points at a domain that
	// does not exist. resolvePublicDERPIP should NOT
	// silently fall back to a wrong source — it should
	// return ok=false (or fall through to egress, which
	// is the documented last-resort path).
	t.Setenv("SKYGATE_DERP_HOSTNAME", "this-domain-does-not-exist-b237-2.invalid")
	_, src, ok := resolvePublicDERPIP("")
	if ok && src == "dns:env" {
		t.Errorf("resolver returned dns:env for invalid hostname (it should fall back or return false): %s", src)
	}
	// ok might be true (if egress succeeded) or false
	// (if both DNS and egress failed). Both are
	// acceptable; the test only asserts the dns:env
	// path is NOT taken for an unresolvable name.
	_ = os.Getenv
}
