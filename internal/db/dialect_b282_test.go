// internal/db/dialect_b282_test.go — B282 (2026-09-22).
//
// Live case: the native `aro` host (SQLite at /var/lib/skygate/skygate.db)
// answered
//
//	/admin/audit            → 500 SQL logic error: unrecognized token: ":"
//	exit_rules.preferred_mismatch → fail: query rules: SQL logic error: unrecognized token: ":"
//	/admin/headscale/acl    → 500 list acl: unmarshal policy: json: cannot unmarshal
//	                          string into Go value of type admin.ACLView
//
// Two of those three were a hardcoded PostgreSQL `::` cast running on
// SQLite, and the decoded timestamp was assumed to be a time.Time while
// SQLite hands back an INTEGER (or the TEXT CURRENT_TIMESTAMP form).
// These tests pin the two helpers that replace both assumptions.
package db

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestCastTextB282 pins the dialect-native text cast.
//
// The SQLite form is the one that matters: `::` is not a token in
// SQLite at all, so the PG shorthand is a parse error for the WHOLE
// statement — which is how a cast on one join column took down a page.
func TestCastTextB282(t *testing.T) {
	cases := []struct {
		name string
		kind DialectKind
		expr string
		want string
	}{
		{"postgres uses the shorthand", DialectPostgres, "r.device_id", "r.device_id::text"},
		{"sqlite uses CAST", DialectSQLite, "r.device_id", "CAST(r.device_id AS TEXT)"},
		{"unknown leaves the expression alone", DialectUnknown, "r.device_id", "r.device_id"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.kind.CastText(c.expr); got != c.want {
				t.Errorf("CastText(%q) = %q, want %q", c.expr, got, c.want)
			}
		})
	}

	// The anti-regression that matters: a SQLite cast must never
	// contain the PG shorthand, or the statement does not parse.
	sqlite := DialectSQLite.CastText("r.device_id")
	if strings.Contains(sqlite, "::") {
		t.Fatalf("SQLite cast %q contains \"::\" — SQLite has no such token and the whole "+
			"statement fails with `unrecognized token: \":\"` (the live B282 failure)", sqlite)
	}
	// PG must keep it — without a cast, `text = integer` is SQLSTATE
	// 42883 on PostgreSQL.
	if !strings.Contains(DialectPostgres.CastText("r.device_id"), "::text") {
		t.Error("PostgreSQL cast lost its ::text — the join then fails with " +
			"`operator does not exist: text = integer` (SQLSTATE 42883)")
	}
}

// TestParseDBTimeB282 pins the tolerant timestamp decoder: the same
// logical column arrives as time.Time on PG, as an INTEGER on SQLite
// (audit_log.created_at is Unix seconds) and as the TEXT
// CURRENT_TIMESTAMP form wherever the older DDL is still in force
// (cluster_audit.created_at).
func TestParseDBTimeB282(t *testing.T) {
	wantUnix := time.Date(2026, 9, 22, 15, 0, 0, 0, time.UTC)
	unixSecs := wantUnix.Unix()
	cases := []struct {
		name string
		in   any
		want time.Time
		ok   bool
	}{
		{"nil is not a time", nil, time.Time{}, false},
		{"time.Time passes through", wantUnix, wantUnix, true},
		{"int64 is unix seconds (audit_log)", unixSecs, wantUnix, true},
		{"int behaves like int64", int(unixSecs), wantUnix, true},
		{"float64 keeps sub-second precision", float64(unixSecs) + 0.5, wantUnix.Add(500 * time.Millisecond), true},
		{"TEXT CURRENT_TIMESTAMP form (cluster_audit)", "2026-09-22 15:00:00", wantUnix, true},
		{"TEXT with sub-seconds", "2026-09-22 15:00:00.250", wantUnix.Add(250 * time.Millisecond), true},
		{"RFC3339", "2026-09-22T15:00:00Z", wantUnix, true},
		{"all-digit text is unix seconds", strconv.FormatInt(unixSecs, 10), wantUnix, true},
		{"[]byte from the driver", []byte("2026-09-22 15:00:00"), wantUnix, true},
		{"empty TEXT is not a time", "", time.Time{}, false},
		{"garbage is not a time", "not-a-timestamp", time.Time{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := ParseDBTime(c.in)
			if ok != c.ok {
				t.Fatalf("ParseDBTime(%#v) ok = %v, want %v", c.in, ok, c.ok)
			}
			if !ok {
				return
			}
			if !got.Equal(c.want) {
				t.Errorf("ParseDBTime(%#v) = %s, want %s", c.in, got, c.want)
			}
		})
	}

	// Unix seconds must NOT be read as a year-shaped date: the all-digit
	// fast path exists precisely so a 10-digit epoch is not handed to the
	// "2006-01-02" layout (which would fail anyway) nor to any layout
	// that could match a prefix of it.
	if ts, ok := ParseDBTime(int64(0)); !ok || !ts.Equal(time.Unix(0, 0).UTC()) {
		t.Errorf("ParseDBTime(int64(0)) = %v/%v, want the unix epoch", ts, ok)
	}
}
