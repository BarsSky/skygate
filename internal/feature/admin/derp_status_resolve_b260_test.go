// v1.5.8+ (B260): pure-function tests for resolveDERPHostname +
// the new ORDER BY id deterministic resolution. These are the
// helpers that drive the URL construction for /admin/derp's
// status probe.
//
// The pre-fix code had a hardcoded `http://192.0.2.1:8443` URL
// (TEST-NET-1, not routable) and used plain HTTP — both bugs
// caused all 6 derper debug probes to silently fail and the page
// to render "DERPER-SERVICE: stopped" regardless of derper's
// actual state. B260 replaces the hardcoded URL with a runtime-
// resolved https://<bundled_hostname>:<bundled_port> and switches
// the httpGet helper to TLS-aware (with InsecureSkipVerify fallback
// only when the URL host is an IP literal — cert CN mismatch is
// unavoidable in that case).
//
// Per the package convention (see derp_status_resolve_test.go
// header note), the DB-backed bundledDERPPortFromDB /
// bundledDERPHostnameFromDB code paths are exercised by the
// live check_b260 script against the operator's VM, not by
// unit tests (the existing test-pg container setup doesn't
// survive into the admin test runner scope).

package admin

import (
	"os"
	"testing"
)

// TestResolveDERPHostname_NilDBEnvFallback pins the env-var
// bootstrap path. When no bundled row is reachable (DB is nil
// OR the bundled-row query returns no rows), the helper falls
// back to SKYGATE_DERP_HOSTNAME env var.
func TestResolveDERPHostname_NilDBEnvFallback(t *testing.T) {
	t.Setenv("SKYGATE_DERP_HOSTNAME", "derp.example.com")
	got := resolveDERPHostname(nil)
	if got != "derp.example.com" {
		t.Errorf("env fallback with nil DB: got %q, want derp.example.com", got)
	}
}

// TestResolveDERPHostname_NilDBEmptyWhenNoEnv pins the
// fully-unconfigured case (no bundled row, no env). Returns ""
// so the caller can fall back to a 127.0.0.1 probe that fails
// cleanly with connect-refused instead of routing to a
// fake IP. Pre-B260 the hardcoded "192.0.2.1" sent probes to
// TEST-NET-1 (RFC 5737) which silently dropped — no error,
// no data, page showed "stopped".
func TestResolveDERPHostname_NilDBEmptyWhenNoEnv(t *testing.T) {
	os.Unsetenv("SKYGATE_DERP_HOSTNAME")
	got := resolveDERPHostname(nil)
	if got != "" {
		t.Errorf("expected empty when nothing configured (nil DB, no env), got %q", got)
	}
}

// TestResolveDERPHostname_EnvWhitespacesTrimming pins the
// whitespace-trimming contract (same as resolveDERPPort).
// Operator-friendliness: copy-pasted env vars with stray
// spaces should still resolve correctly.
func TestResolveDERPHostname_EnvWhitespacesTrimming(t *testing.T) {
	t.Setenv("SKYGATE_DERP_HOSTNAME", "  derp.example.com  ")
	got := resolveDERPHostname(nil)
	if got != "derp.example.com" {
		t.Errorf("whitespace trimming: got %q, want derp.example.com (untrimmed)", got)
	}
}

// bundledDERPHostnameFromDB itself is NOT nil-safe (it calls
// d.QueryRow unconditionally). The caller resolveDERPHostname
// guards with `if d != nil { ... }` — same shape as the
// pre-existing resolveDERPPort → bundledDERPPortFromDB chain.
// So the nil-DB case is implicitly tested by the
// resolveDERPHostname tests above (which all pass nil). A
// direct bundledDERPHostnameFromDB(nil) test would panic and
// is intentionally omitted — the function contract is "caller
// must guard with a nil check".
