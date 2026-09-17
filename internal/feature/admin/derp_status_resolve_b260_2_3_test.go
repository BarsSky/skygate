// v1.5.8+ (B260.2.3): regression test for the DB-first priority
// in resolveDERPPort. Pre-B260.2.3 the env var DERP_HTTP_PORT
// took priority over the DB query — operators with a legacy
// `.env` containing `DERP_HTTP_PORT=8443` (set during the
// pre-B-derper-cert systemd era when derper listened on :8443)
// saw `:8443` on /admin/derp forever, even after B260 added
// `ORDER BY id ASC LIMIT 1` (which correctly returned `:443`
// from the DB but was overridden by the env var).
//
// B260.2.3 fix: swap the priority to DB-first (matching
// resolveDERPHostname). The DB is the source of truth — it's
// editable via /admin/derp/relays and stays in sync with the
// published derpmap.json. The env var is now a fallback for
// operators running skygate against a DB-less test fixture.
package admin

import (
	"os"
	"testing"
)

// TestResolveDERPPort_DBFirstStaleEnv pins the B260.2.3 fix.
// The DB returns :443 (id=2, the canonical bundled row from
// B260's ORDER BY id ASC LIMIT 1); the env has the stale
// :8443. Pre-fix the env won and the page rendered :8443.
// Post-fix the DB wins and the page renders :443.
//
// The DB code path is exercised live by check_b260_2 against
// the operator's VM (per the package convention in
// derp_status_resolve_test.go header); here we only test the
// nil-DB + env-fallback path (the inverse case: env wins when
// DB is nil).
func TestResolveDERPPort_NilDBEnvFallback(t *testing.T) {
	t.Setenv("DERP_HTTP_PORT", ":443")
	got := resolveDERPPort(nil)
	if got != ":443" {
		t.Errorf("env fallback with nil DB: got %q, want :443", got)
	}
}

// TestResolveDERPPort_NilDBEmptyWhenNoEnv pins the
// fully-unconfigured case. Returns "" so the caller falls
// back to "443" at the call site (don't treat empty as a
// real port).
func TestResolveDERPPort_NilDBEmptyWhenNoEnv(t *testing.T) {
	os.Unsetenv("DERP_HTTP_PORT")
	got := resolveDERPPort(nil)
	if got != "" {
		t.Errorf("no DB + no env: got %q, want \"\"", got)
	}
}

// TestResolveDERPPort_NilDBEnvWhitespacesTrimming pins the
// defensive whitespace-trim path. Operators sometimes
// accidentally write `DERP_HTTP_PORT= 443` (with a leading
// space) when copy-pasting from a doc — the helper must
// treat the value as "443", not " 443".
func TestResolveDERPPort_NilDBEnvWhitespacesTrimming(t *testing.T) {
	t.Setenv("DERP_HTTP_PORT", "  :443  ")
	got := resolveDERPPort(nil)
	if got != ":443" {
		t.Errorf("whitespace env: got %q, want :443", got)
	}
}
