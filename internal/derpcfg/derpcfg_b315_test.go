package derpcfg

import (
	"database/sql"
	"errors"
	"testing"

	"skygate/internal/db"
)

// newB315DB opens a real in-memory SQLite DB through the production open path and
// applies the real migration chain, so the global_settings helpers this package
// dispatches on are the ones production uses.
func newB315DB(t *testing.T) *sql.DB {
	t.Helper()
	_, d, err := db.OpenWithDialect(":memory:")
	if err != nil {
		t.Fatalf("OpenWithDialect(:memory:): %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := db.MigrateSQLite(d); err != nil {
		t.Fatalf("MigrateSQLite: %v", err)
	}
	return d
}

// TestB315_ValidateDebugURL pins the accepted shapes and, more importantly, that
// every refusal carries its OWN code: the page translates the code, so a single
// "invalid" for all of them would give the operator a message that does not say
// what to change.
func TestB315_ValidateDebugURL(t *testing.T) {
	ok := map[string]string{
		"http://172.18.0.1:8767":               "http://172.18.0.1:8767",
		"http://172.18.0.1:8767/":              "http://172.18.0.1:8767",
		"172.18.0.1:8767":                      "http://172.18.0.1:8767",
		"192.0.2.10:8767/metrics":              "http://192.0.2.10:8767/metrics",
		"https://metrics.example.com":          "https://metrics.example.com",
		"HTTPS://Metrics.Example.COM":          "https://metrics.example.com",
		"  http://host.docker.internal:8767  ": "http://host.docker.internal:8767",
		"":                                     "",
		"   ":                                  "",
	}
	for raw, want := range ok {
		got, err := ValidateDebugURL(raw)
		if err != nil {
			t.Fatalf("ValidateDebugURL(%q): unexpected error %v", raw, err)
		}
		if got != want {
			t.Fatalf("ValidateDebugURL(%q) = %q, want %q", raw, got, want)
		}
	}

	bad := map[string]string{
		"ftp://relay.example.com":  "scheme",
		"http://a b":               "spaces",
		"http://host:99999":        "port",
		"http://host:notaport":     "port",
		"http://":                  "invalid",
		"http://user:pw@host:8767": "invalid",
		"http://host:8767/?x=1":    "invalid",
		"http://host:8767/#frag":   "invalid",
	}
	for raw, wantCode := range bad {
		_, err := ValidateDebugURL(raw)
		if err == nil {
			t.Fatalf("ValidateDebugURL(%q): expected a refusal", raw)
		}
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Fatalf("ValidateDebugURL(%q): error is not a *ValidationError: %v", raw, err)
		}
		if ve.Code != wantCode {
			t.Fatalf("ValidateDebugURL(%q): code=%q, want %q", raw, ve.Code, wantCode)
		}
	}
}

// TestB315_DebugEndpointPrecedence is the operator-facing contract of the knob:
// the value saved on the page wins while it is set, clearing it hands control
// back to .env, and with neither the state is "no endpoint" — never a stale
// value and never an error.
func TestB315_DebugEndpointPrecedence(t *testing.T) {
	d := newB315DB(t)
	t.Setenv(DebugEnvKey, "http://192.0.2.20:8767")

	if got := ResolveDebug(d); got.Host != "http://192.0.2.20:8767" || got.Source != SourceEnv {
		t.Fatalf("env layer: got %+v", got)
	}
	if err := SaveDebug(d, "172.18.0.1:8767"); err != nil {
		t.Fatalf("SaveDebug: %v", err)
	}
	if got := ResolveDebug(d); got.Host != "http://172.18.0.1:8767" || got.Source != SourceDB {
		t.Fatalf("db layer must win: got %+v", got)
	}
	if got := DebugBaseURL(d); got != "http://172.18.0.1:8767" {
		t.Fatalf("DebugBaseURL: got %q", got)
	}
	if err := SaveDebug(d, "   "); err != nil {
		t.Fatalf("SaveDebug(clear): %v", err)
	}
	if got := ResolveDebug(d); got.Host != "http://192.0.2.20:8767" || got.Source != SourceEnv {
		t.Fatalf("after clearing the row the env layer must take over: got %+v", got)
	}
	t.Setenv(DebugEnvKey, "")
	if got := ResolveDebug(d); got.Host != "" || got.Source != SourceDefault {
		t.Fatalf("with no row and no env: got %+v, want empty/default", got)
	}
	// A nil handle is legal (cron / tests) and must fall through to env, not panic.
	if got := ResolveDebug(nil); got.Source != SourceDefault {
		t.Fatalf("ResolveDebug(nil): got %+v", got)
	}
}

// TestB315_SaveDebugRefusesGarbageWithoutWriting pins that a refused value never
// reaches global_settings — a bad endpoint must not silently become the value
// every later render reads.
func TestB315_SaveDebugRefusesGarbageWithoutWriting(t *testing.T) {
	d := newB315DB(t)
	t.Setenv(DebugEnvKey, "")
	if err := SaveDebug(d, "ftp://relay.example.com"); err == nil {
		t.Fatal("SaveDebug(ftp://): expected a refusal")
	}
	if got := ResolveDebug(d); got.Host != "" || got.Source != SourceDefault {
		t.Fatalf("a refused value was stored: %+v", got)
	}
}
