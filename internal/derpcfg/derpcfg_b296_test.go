package derpcfg

import (
	"database/sql"
	"errors"
	"testing"

	"skygate/internal/db"
)

// newB296DB opens a real in-memory SQLite database through the SAME open path
// production uses (so the dialect classification the global_settings helpers
// dispatch on is the real one) and applies the real migration chain, so the
// precedence test cannot pass against a hand-made table that production lacks.
func newB296DB(t *testing.T) *sql.DB {
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

// TestB296_OverrideBeatsEnvAndClearingFallsBack is the whole point of the block:
// what the operator types on the page wins over .env while it is set, and
// clearing it hands control back to .env (which is what deploy.sh writes).
func TestB296_OverrideBeatsEnvAndClearingFallsBack(t *testing.T) {
	d := newB296DB(t)
	t.Setenv(EnvKey, "192.0.2.11")

	// env layer only
	if got := Resolve(d); got.Host != "192.0.2.11" || got.Source != SourceEnv {
		t.Fatalf("with only %s set: got %+v, want 192.0.2.11/env", EnvKey, got)
	}

	// db layer wins
	if err := Save(d, "192.0.2.10"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got := Resolve(d); got.Host != "192.0.2.10" || got.Source != SourceDB {
		t.Fatalf("after Save: got %+v, want 192.0.2.10/db", got)
	}
	if got := DialHost(d); got != "192.0.2.10" {
		t.Fatalf("DialHost after Save: got %q", got)
	}

	// clearing the override -> env again (not "")
	if err := Save(d, "   "); err != nil {
		t.Fatalf("Save(clear): %v", err)
	}
	if got := Resolve(d); got.Host != "192.0.2.11" || got.Source != SourceEnv {
		t.Fatalf("after clearing: got %+v, want 192.0.2.11/env", got)
	}

	// and with no env at all the default layer is "no hint", never a stale value
	t.Setenv(EnvKey, "")
	if got := Resolve(d); got.Host != "" || got.Source != SourceDefault {
		t.Fatalf("with no env and no override: got %+v, want empty/default", got)
	}
}

// TestB296_ResolveToleratesANilHandle pins that the cron/test callers without a
// *sql.DB fall through to the env layer instead of panicking.
func TestB296_ResolveToleratesANilHandle(t *testing.T) {
	t.Setenv(EnvKey, " 192.0.2.12 ")
	if got := Resolve(nil); got.Host != "192.0.2.12" || got.Source != SourceEnv {
		t.Fatalf("Resolve(nil): got %+v, want 192.0.2.12/env", got)
	}
	if err := Save(nil, "192.0.2.10"); err == nil {
		t.Fatal("Save(nil) must report the missing handle, not pretend to store it")
	}
}

// TestB296_SaveRejectsGarbageWithoutStoringIt makes sure a refused value never
// reaches the DB (an invalid override would break every probe silently).
func TestB296_SaveRejectsGarbageWithoutStoringIt(t *testing.T) {
	d := newB296DB(t)
	t.Setenv(EnvKey, "192.0.2.11")
	var ve *ValidationError
	if err := Save(d, "192.0.2.10:443"); !errors.As(err, &ve) || ve.Code != "port" {
		t.Fatalf("Save(host:port): err=%v, want ValidationError{port}", err)
	}
	if got := Resolve(d); got.Host != "192.0.2.11" || got.Source != SourceEnv {
		t.Fatalf("a refused value must not be stored: got %+v", got)
	}
	if v, err := db.GetGlobalSetting(d, SettingKey, ""); err != nil || v != "" {
		t.Fatalf("global_settings row: v=%q err=%v, want empty", v, err)
	}
}

func TestB296_Validate(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		code    string // "" = no error expected
		comment string
	}{
		{"192.0.2.10", "192.0.2.10", "", "IPv4 literal"},
		{" 192.0.2.10 ", "192.0.2.10", "", "trimmed"},
		{"2001:db8::1", "2001:db8::1", "", "bare IPv6"},
		{"[2001:db8::1]", "2001:db8::1", "", "bracketed IPv6 (JoinHostPort form)"},
		{"relay.example.com", "relay.example.com", "", "hostname"},
		{"Relay.Example.COM", "relay.example.com", "", "hostnames are case-insensitive"},
		{"", "", "", "empty = clear the override"},
		{"192.0.2.10:443", "", "port", "the port comes from the relay row URL"},
		{"https://relay.example.com", "", "scheme", "no scheme"},
		{"//relay.example.com", "", "scheme", "protocol-relative form"},
		{"relay.example.com/derp", "", "path", "not a dial address"},
		{"192.0.2.10 192.0.2.11", "", "spaces", "one address, not a list"},
		{"not a host", "", "spaces", "spaces first"},
		{"-bad.example.com", "", "invalid", "label must start alphanumeric"},
		{"bad_.example.com", "", "invalid", "underscores are not DNS"},
		{"999.0.2.10", "", "invalid", "not an IP, not a valid label set? (all-numeric label)"},
	}
	for _, tc := range cases {
		got, err := Validate(tc.in)
		var ve *ValidationError
		switch {
		case tc.code == "" && err != nil:
			t.Errorf("Validate(%q) [%s]: unexpected error %v", tc.in, tc.comment, err)
		case tc.code == "" && got != tc.want:
			t.Errorf("Validate(%q) [%s]: got %q, want %q", tc.in, tc.comment, got, tc.want)
		case tc.code != "":
			if !errors.As(err, &ve) {
				t.Errorf("Validate(%q) [%s]: err=%v, want ValidationError{%s}", tc.in, tc.comment, err, tc.code)
			} else if ve.Code != tc.code {
				t.Errorf("Validate(%q) [%s]: code=%s, want %s", tc.in, tc.comment, ve.Code, tc.code)
			}
		}
	}
}
