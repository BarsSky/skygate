// internal/db/exit_servers_b292_test.go — B292 (2026-09-23).
//
// FirstTailscaleIP is the one rule that turns headscale's address list into the
// string that can be spliced into an `ssh root@<ip>` argument, and
// SetExitServerTailscaleIPIfEmpty is what lets the sync repair a row the
// discovery pass created before headscale reported an address (INSERT OR IGNORE
// never updates the column, so it stayed empty forever and the B81 fallback
// chain resolved to "" — the live `aro` "Could not resolve hostname" bug).
package db

import (
	"database/sql"
	"strings"
	"testing"
)

// newB292SQLiteDB opens a migrated, PRIVATE in-memory SQLite database. Private
// (a named DB keyed by the test name) because `file::memory:?cache=shared` is
// shared by every connection in the process and two tests in this package would
// otherwise see each other's exit_servers rows.
func newB292SQLiteDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := "file:b292_" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()) + "?mode=memory&cache=shared"
	_, d, err := OpenWithDialect(dsn)
	if err != nil {
		t.Skipf("sqlite dialect unavailable: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := ApplyMigrations(d, DialectSQLite); err != nil {
		t.Fatalf("ApplyMigrations(sqlite): %v", err)
	}
	return d
}

func TestFirstTailscaleIP_B292(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"blank", "   ", ""},
		{"ipv4 only", "100.64.0.1", "100.64.0.1"},
		{"ipv4 first", "100.64.0.1,fd7a:115c:a1e0::1", "100.64.0.1"},
		{"ipv6 first still prefers the v4", "fd7a:115c:a1e0::1,100.64.0.1", "100.64.0.1"},
		{"ipv6 only falls back to it", "fd7a:115c:a1e0::1", "fd7a:115c:a1e0::1"},
		{"spaces around the separator", " 100.64.0.2 , fd7a::2 ", "100.64.0.2"},
		{"empty first entry", ",100.64.0.3", "100.64.0.3"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := FirstTailscaleIP(c.in); got != c.want {
				t.Errorf("FirstTailscaleIP(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestLookupExitServerSSHTarget_B292 pins the B81 fallback chain through the new
// shared helper: an operator override wins, otherwise root@<tailscale_ip>, and
// an empty column yields "" (the caller must then name the missing target
// rather than handing ssh a hostname).
func TestLookupExitServerSSHTarget_B292(t *testing.T) {
	d := newB292SQLiteDB(t) // migrated, private in-memory SQLite

	seed := func(hostname, sshTarget, tsIP, port string) {
		t.Helper()
		if _, err := d.Exec(`
			INSERT INTO exit_servers (node_id, hostname, tailscale_ip, ssh_target, ssh_key_path, ssh_port, description, enabled, accept_routes)
			VALUES ($1, $2, $3, $4, '', $5, '', 1, 0)`,
			hostname+"-id", hostname, tsIP, sshTarget, port); err != nil {
			t.Fatalf("seed %s: %v", hostname, err)
		}
	}
	seed("override", "root@relay.example.com:18022", "100.64.0.9", "")
	seed("auto", "", "100.64.0.1,fd7a:115c:a1e0::1", "")
	seed("auto-port", "", "100.64.0.2", "2222")
	seed("nothing", "", "", "")

	cases := []struct{ host, want string }{
		{"override", "root@relay.example.com:18022"},
		{"auto", "root@100.64.0.1"},
		{"auto-port", "root@100.64.0.2:2222"},
		{"nothing", ""},
		{"absent-row", ""},
	}
	for _, c := range cases {
		got, err := LookupExitServerSSHTarget(d, c.host)
		if err != nil {
			t.Fatalf("LookupExitServerSSHTarget(%s): %v", c.host, err)
		}
		if got != c.want {
			t.Errorf("LookupExitServerSSHTarget(%s) = %q, want %q", c.host, got, c.want)
		}
	}
}

// TestSetExitServerTailscaleIPIfEmpty_B292 is the repair half: it fills a blank
// column, and never overwrites an address that is already there (a relay may
// legitimately be reached on a different address than the one headscale lists).
func TestSetExitServerTailscaleIPIfEmpty_B292(t *testing.T) {
	d := newB292SQLiteDB(t)

	if _, err := d.Exec(`
		INSERT INTO exit_servers (node_id, hostname, tailscale_ip, ssh_target, ssh_key_path, ssh_port, description, enabled, accept_routes)
		VALUES ('n1', 'blank', '', '', '', '', '', 1, 0),
		       ('n2', 'pinned', '100.64.0.5', '', '', '', '', 1, 0),
		       ('n3', 'spaces', '   ', '', '', '', '', 1, 0)`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := SetExitServerTailscaleIPIfEmpty(d, "blank", "100.64.0.1"); err != nil {
		t.Fatalf("fill blank: %v", err)
	}
	if err := SetExitServerTailscaleIPIfEmpty(d, "spaces", "100.64.0.2"); err != nil {
		t.Fatalf("fill whitespace: %v", err)
	}
	if err := SetExitServerTailscaleIPIfEmpty(d, "pinned", "100.64.0.42"); err != nil {
		t.Fatalf("pinned: %v", err)
	}
	// A missing row is a no-op, not an error — the sync may see a relay that has
	// no exit_servers row at all.
	if err := SetExitServerTailscaleIPIfEmpty(d, "no-such-relay", "100.64.0.3"); err != nil {
		t.Fatalf("absent row: %v", err)
	}
	if err := SetExitServerTailscaleIPIfEmpty(d, "blank", ""); err != nil {
		t.Fatalf("empty ip: %v", err)
	}

	want := map[string]string{"blank": "100.64.0.1", "spaces": "100.64.0.2", "pinned": "100.64.0.5"}
	for host, w := range want {
		var got string
		if err := d.QueryRow(`SELECT tailscale_ip FROM exit_servers WHERE hostname = $1`, host).Scan(&got); err != nil {
			t.Fatalf("read %s: %v", host, err)
		}
		if got != w {
			t.Errorf("tailscale_ip[%s] = %q, want %q", host, got, w)
		}
	}

	// And the repaired row now resolves to a usable SSH target — the whole point.
	got, err := LookupExitServerSSHTarget(d, "blank")
	if err != nil {
		t.Fatalf("resolve after repair: %v", err)
	}
	if got != "root@100.64.0.1" {
		t.Errorf("resolved target after repair = %q, want root@100.64.0.1 (the live `aro` row had an "+
			"empty tailscale_ip, so the sync fell back to the node name and ssh said 'Could not resolve hostname')", got)
	}
}
