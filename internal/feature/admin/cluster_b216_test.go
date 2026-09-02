// v1.5.0+ / B216 — unit tests for the Phase 2.1
// enrichment helpers. The page-side work (online count,
// replicas, DSN host, cluster_audit query) is harder to
// unit-test without a real DB; the pure-shape helpers
// (extractDSNHost, classifyNodeHealth) are the parts
// that can fail at compile or unit-test time, so we
// cover them thoroughly here.
//
// The cluster_audit / cluster_database / cluster_node
// query paths are pinned by scripts/check_b216.sh (which
// grep's for the new query strings + new struct fields)
// and exercised end-to-end by the live-verify script
// scripts/b216_liveverify.sh (which fetches the page
// via HTTP with admin auth and asserts the new sections
// appear in the HTML).

package admin

import (
	"database/sql"
	"testing"
	"time"
)

func TestExtractDSNHost(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		// Standard libpq URL form: scheme://user:pass@host:port/db
		{
			name: "standard",
			in:   "postgres://admin:secret@172.17.0.1:5433/skygate_staging?sslmode=disable",
			want: "172.17.0.1:5433",
		},
		{
			name: "no port",
			in:   "postgres://u:p@db.local/mydb?sslmode=require",
			want: "db.local",
		},
		{
			name: "no query string",
			in:   "postgres://u:p@10.0.0.5:5432/mydb",
			want: "10.0.0.5:5432",
		},
		{
			name: "password with @ char (URL-encoded)",
			in:   "postgres://u:p%40word@db:5432/mydb",
			want: "db:5432",
		},
		// Non-URL form: libpq "host=... port=... user=..."
		// — no @, no host extraction possible. Operator
		// sees the full DSN on /admin/database.
		{
			name: "no at-sign (libpq keyword form)",
			in:   "host=db.local port=5432 user=admin password=secret dbname=mydb sslmode=disable",
			want: "",
		},
		// Unix-socket form: the netloc is the socket
		// path; u.Host is empty after url.Parse. We fall
		// through to the path. The result is "best
		// effort" — includes the dbname too, but the
		// operator can see the full DSN on /admin/database.
		{
			name: "unix socket",
			in:   "postgres://u:p@/var/run/postgresql/mydb?host=/var/run/postgresql",
			want: "/var/run/postgresql/mydb",
		},
		// Edge: user:pass@ followed immediately by /mydb
		// (no host:port, no query). url.Parse returns
		// empty Host + Path=/mydb. Helper returns /mydb.
		{
			name: "no host",
			in:   "postgres://u:p@/mydb",
			want: "/mydb",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := extractDSNHost(c.in)
			if got != c.want {
				t.Errorf("extractDSNHost(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestClassifyNodeHealth(t *testing.T) {
	// nowUnix is a fixed "now" for deterministic results.
	now := int64(1_700_000_000)
	nullTime := sql.NullTime{Valid: false}
	mkTime := func(secs int64) sql.NullTime {
		return sql.NullTime{Time: time.Unix(secs, 0).UTC(), Valid: true}
	}

	cases := []struct {
		name        string
		state       string
		lastSeen    sql.NullTime
		nowUnix     int64
		wantOnline  bool
		wantStale   bool
	}{
		// ready + fresh heartbeat = online
		{"ready + last_seen 0s ago", "ready", mkTime(now), now, true, false},
		{"ready + last_seen 30s ago", "ready", mkTime(now - 30), now, true, false},
		{"ready + last_seen 89s ago (boundary, just inside)", "ready", mkTime(now - 89), now, true, false},
		// ready + stale heartbeat = NOT online, IS stale
		{"ready + last_seen 90s ago (boundary, just outside)", "ready", mkTime(now - 90), now, false, true},
		{"ready + last_seen 5m ago", "ready", mkTime(now - 300), now, false, true},
		{"ready + last_seen 1h ago", "ready", mkTime(now - 3600), now, false, true},
		// ready + never had a heartbeat = NOT online, NOT stale
		// (the "stale" pill is for the in-grace window, not
		// the never-was-online case).
		{"ready + NULL last_seen", "ready", nullTime, now, false, false},
		// Other states: always not online, not stale.
		{"pending", "pending", mkTime(now), now, false, false},
		{"pending + fresh last_seen", "pending", mkTime(now - 10), now, false, false},
		{"draining", "draining", mkTime(now - 10), now, false, false},
		{"failed", "failed", mkTime(now - 10), now, false, false},
		{"failed + old last_seen", "failed", mkTime(now - 86400), now, false, false},
		// Edge: last_seen in the future (clock skew) — treated
		// as "fresh" because age = now - future is negative.
		// Negative is "more online than now" — that node is
		// claiming to have heartbeated from the future, so
		// we treat it as online.
		{"ready + last_seen in future", "ready", mkTime(now + 10), now, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			online, stale := classifyNodeHealth(c.state, c.lastSeen, c.nowUnix)
			if online != c.wantOnline {
				t.Errorf("online = %v, want %v", online, c.wantOnline)
			}
			if stale != c.wantStale {
				t.Errorf("stale = %v, want %v", stale, c.wantStale)
			}
		})
	}
}

func TestOnlineThresholdSec_HAElectorAlignment(t *testing.T) {
	// Sanity check: OnlineThresholdSec must equal 90 — the
	// value the B204 HA elector uses for "3 missed
	// heartbeats (30s × 3) → state=failed". If these two
	// thresholds drift, the "X of Y online" pill on
	// /admin/cluster will disagree with what the HA chain
	// is doing (a node the page says is "online" might
	// already be flipped to state=failed by the chain,
	// or vice versa).
	//
	// If you ever change this value, ALSO update
	// internal/elector/elector.go's "stale" threshold
	// (or accept the visible disagreement).
	if OnlineThresholdSec != 90 {
		t.Errorf("OnlineThresholdSec = %d, want 90 (must match HA elector)", OnlineThresholdSec)
	}
}
