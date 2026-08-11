// Tests for the exit_servers helpers in internal/db/exit_servers.go.
//
// Этап 10 part 5 (2026-07-12). Same strategy as
// node_owner_map_test.go: openTestDB gives a fresh sqlite with the
// production migration chain applied (so all of v0.20 + v0.24 + v0.26
// columns are present), then seed helpers centralise the fixtures.
//
// Each helper has at least one populated-case test plus, where
// relevant, an empty / no-match / idempotency test. The
// InsertIgnore + Upsert pair is tested against each other to
// verify the "discovery adds a row, admin re-upserts with the same
// node_id" sequence (Upsert wins because it's an explicit
// ON CONFLICT DO UPDATE).

package db

import (
	"database/sql"
	"testing"
)

// seedExitServer inserts one row into exit_servers with the columns
// the helpers care about. enabled=1 means yes, 0 means no. The full
// set of columns matches the v0.20 CREATE plus v0.24 ALTERs
// (ssh_target / ssh_key_path) plus v0.26 ALTER (accept_routes).
func seedExitServer(t *testing.T, d *sql.DB, nodeID, hostname, tailscaleIP, sshTarget, sshKeyPath, description string, enabled, acceptRoutes int) {
	t.Helper()
	if _, err := d.Exec(
		`INSERT INTO exit_servers
			(node_id, hostname, tailscale_ip, ssh_target, ssh_key_path, description, enabled, accept_routes)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		nodeID, hostname, tailscaleIP, sshTarget, sshKeyPath, description, enabled, acceptRoutes,
	); err != nil {
		t.Fatalf("seedExitServer(%q): %v", nodeID, err)
	}
}

// --- ListExitServers ---

func TestListExitServers_Empty(t *testing.T) {
	d := openTestDB(t)
	got, err := ListExitServers(d)
	if err != nil {
		t.Fatalf("ListExitServers: %v", err)
	}
	if got == nil {
		t.Errorf("got nil slice, want []ExitServer{} (non-nil)")
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %d rows", len(got))
	}
}

func TestListExitServers_PopulatedOrderedByHostname(t *testing.T) {
	d := openTestDB(t)
	// Insert in non-alphabetical order to verify the ORDER BY hostname.
	seedExitServer(t, d, "node-zeta", "zeta", "100.0.0.3", "", "", "", 1, 0)
	seedExitServer(t, d, "node-alpha", "alpha", "100.0.0.1", "root@alpha", "/keys/a", "first", 1, 1)
	seedExitServer(t, d, "node-mu", "mu", "100.0.0.2", "", "", "", 0, -1)

	got, err := ListExitServers(d)
	if err != nil {
		t.Fatalf("ListExitServers: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(got))
	}
	// Verify order: alpha, mu, zeta.
	want := []string{"alpha", "mu", "zeta"}
	for i, e := range got {
		if e.Hostname != want[i] {
			t.Errorf("row %d: hostname=%q, want %q", i, e.Hostname, want[i])
		}
	}
	// Verify the full row shape on the second row (mu): enabled=false, accept_routes=-1.
	if got[1].Enabled {
		t.Errorf("row 1: expected Enabled=false, got true")
	}
	if got[1].AcceptRoutes != -1 {
		t.Errorf("row 1: AcceptRoutes=%d, want -1", got[1].AcceptRoutes)
	}
	// Verify all fields on the row with non-empty values (alpha).
	if got[0].SSHTarget != "root@alpha" || got[0].SSHKeyPath != "/keys/a" || got[0].Description != "first" {
		t.Errorf("row 0: ssh/description not preserved: %+v", got[0])
	}
	if got[0].TailscaleIP != "100.0.0.1" {
		t.Errorf("row 0: tailscale_ip=%q, want 100.0.0.1", got[0].TailscaleIP)
	}
	if !got[0].Enabled || got[0].AcceptRoutes != 1 {
		t.Errorf("row 0: enabled/accept_routes wrong: %+v", got[0])
	}
}

// --- ListEnabledExitServerHostnames ---

func TestListEnabledExitServerHostnames_FiltersDisabled(t *testing.T) {
	d := openTestDB(t)
	// enabled=0 row must be filtered out.
	seedExitServer(t, d, "n1", "alpha", "", "", "", "", 1, 0)
	seedExitServer(t, d, "n2", "beta", "", "", "", "", 0, 0) // disabled
	seedExitServer(t, d, "n3", "gamma", "", "", "", "", 1, 0)

	got, err := ListEnabledExitServerHostnames(d)
	if err != nil {
		t.Fatalf("ListEnabledExitServerHostnames: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 enabled hostnames, got %d (%v)", len(got), got)
	}
	want := []string{"alpha", "gamma"}
	for i, h := range got {
		if h != want[i] {
			t.Errorf("hostname[%d]=%q, want %q", i, h, want[i])
		}
	}
}

func TestListEnabledExitServerHostnames_Empty(t *testing.T) {
	d := openTestDB(t)
	got, err := ListEnabledExitServerHostnames(d)
	if err != nil {
		t.Fatalf("ListEnabledExitServerHostnames: %v", err)
	}
	if got == nil {
		t.Errorf("got nil slice, want []string{} (non-nil)")
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %d rows", len(got))
	}
}

// --- LookupExitServerAcceptRoutes ---

func TestLookupExitServerAcceptRoutes_Found(t *testing.T) {
	d := openTestDB(t)
	seedExitServer(t, d, "n1", "alpha", "", "", "", "", 1, 1)
	seedExitServer(t, d, "n2", "beta", "", "", "", "", 1, -1)
	seedExitServer(t, d, "n3", "gamma", "", "", "", "", 1, 0)

	cases := []struct {
		host string
		want int
	}{
		{"alpha", 1},
		{"beta", -1},
		{"gamma", 0},
	}
	for _, c := range cases {
		got, err := LookupExitServerAcceptRoutes(d, c.host)
		if err != nil {
			t.Errorf("Lookup(%q): %v", c.host, err)
			continue
		}
		if got != c.want {
			t.Errorf("Lookup(%q)=%d, want %d", c.host, got, c.want)
		}
	}
}

func TestLookupExitServerAcceptRoutes_NotFoundReturnsZero(t *testing.T) {
	d := openTestDB(t)
	// The whole point of the helper: a missing row falls back to 0
	// ("unset, do not change AcceptRoutes on the node") without
	// bubbling an error to the caller.
	got, err := LookupExitServerAcceptRoutes(d, "does-not-exist")
	if err != nil {
		t.Errorf("expected nil err on no-match, got %v", err)
	}
	if got != 0 {
		t.Errorf("expected fallback 0, got %d", got)
	}
}

// --- LookupExitServerSSH (v0.33.1) ---

// TestLookupExitServerSSH_Found pins the happy path: a row with
// both ssh_target and ssh_key_path populated returns both values
// verbatim. karolina is the real-world case (port 18022, custom
// key path); if the SQL doesn't read these columns the operator's
// `ssh karolina` call from the dockerised skygate would fail with
// "port 22: connection refused" (the legacy hard-coded fallback).
func TestLookupExitServerSSH_Found(t *testing.T) {
	d := openTestDB(t)
	seedExitServer(t, d, "n1", "karolina", "100.64.0.2", "root@karolina.example.com:18022", "/ssh-sync/id_ed25519", "v0.33.1", 1, -1)
	seedExitServer(t, d, "n2", "emilia", "100.64.0.3", "", "", "no SSH override", 1, 0)

	got, err := LookupExitServerSSH(d, "karolina")
	if err != nil {
		t.Fatalf("LookupExitServerSSH(karolina): %v", err)
	}
	if got.Target != "root@karolina.example.com:18022" {
		t.Errorf("Target: got %q want %q", got.Target, "root@karolina.example.com:18022")
	}
	if got.KeyPath != "/ssh-sync/id_ed25519" {
		t.Errorf("KeyPath: got %q want %q", got.KeyPath, "/ssh-sync/id_ed25519")
	}

	// emilia has no per-row override — both should be empty,
	// letting the caller fall back to Config.SSHKeyPath.
	got, err = LookupExitServerSSH(d, "emilia")
	if err != nil {
		t.Fatalf("LookupExitServerSSH(emilia): %v", err)
	}
	if got.Target != "" || got.KeyPath != "" {
		t.Errorf("emilia should have no overrides, got (%q, %q)", got.Target, got.KeyPath)
	}
}

// TestLookupExitServerSSH_NotFoundReturnsEmpty pins the
// ("", "")-on-miss contract. SyncAdvertisedRoutes relies on
// this to fall back to the per-Cfg SSHKeyPath / SKYGATE_EXIT_SSH_KEY
// default without special-casing sql.ErrNoRows at every call
// site. A future refactor that returns the sql error here
// would force every caller to add a `if errors.Is(err, sql.ErrNoRows)`
// branch — that's the same shape bug as the v0.32.x
// LookupExitServerAcceptRoutes had pre-fix.
func TestLookupExitServerSSH_NotFoundReturnsEmpty(t *testing.T) {
	d := openTestDB(t)
	got, err := LookupExitServerSSH(d, "no-such-host")
	if err != nil {
		t.Errorf("expected nil err on no-match, got %v", err)
	}
	if got.Target != "" || got.KeyPath != "" {
		t.Errorf("expected ('', '') fallback, got (%q, %q)", got.Target, got.KeyPath)
	}
}

// --- LookupExitServerSSHTarget (v0.33.1.29 B81) ---
//
// B81 pins the SSH-target fallback chain:
//   1. exit_servers.ssh_target (operator override) — non-default port
//      or custom user. The most common case is karolina's
//      "root@karolina.example.com:18022".
//   2. "root@<tailscale_ip>" (auto-fallback) — populated by
//      ensureExitServers from headscale's IPAddresses. The Tailscale
//      IP is always reachable from the skygate host (same headscale
//      network), so this works without DNS / firewall holes.
//   3. "" — neither override nor auto available. Caller must
//      surface a clear "set ssh_target or wait for discovery"
//      error instead of silently falling back to nodeHostname
//      (which doesn't resolve for typical exit-nodes).
//
// The legacy fallback to nodeHostname stays in
// SetAdvertisedRoutes (the v0.33.1 path) but only for the
// "no exit_servers row at all" case — it never runs when the
// row exists with empty ssh_target. That separation is the
// v0.33.1.29 fix.

// TestLookupExitServerSSHTarget_OperatorOverrideWins pins case 1:
// when ssh_target is set, the helper returns it verbatim — even
// when the Tailscale IP is ALSO set. The operator's override
// (non-default port, custom user, public IP for a relay
// behind a NAT) always wins over the auto-fallback. Without
// this guarantee an operator with a custom karolina:18022
// override would silently lose it on the next sync.
func TestLookupExitServerSSHTarget_OperatorOverrideWins(t *testing.T) {
	d := openTestDB(t)
	seedExitServer(t, d, "n1", "karolina", "100.64.0.2", "root@karolina.example.com:18022", "/ssh-sync/id_ed25519", "v0.33.1.29 B81", 1, -1)
	got, err := LookupExitServerSSHTarget(d, "karolina")
	if err != nil {
		t.Fatalf("LookupExitServerSSHTarget: %v", err)
	}
	if got != "root@karolina.example.com:18022" {
		t.Errorf("ssh_target should win over tailscale_ip, got %q", got)
	}
}

// TestLookupExitServerSSHTarget_FallsBackToTailscaleIP pins case 2:
// when ssh_target is empty but tailscale_ip is set, the helper
// returns "root@<tailscale_ip>". This is the B81 fix for the
// "ssh root@<public-ip>:22: Operation timed out" failure mode
// — the operator's public-IP override is bypassed in favour of
// the always-reachable Tailscale IP. The empty ssh_target can
// be the result of a fresh /admin/exit-nodes/add (the form
// leaves it blank) OR a pre-B81 row that the operator has
// since cleared.
func TestLookupExitServerSSHTarget_FallsBackToTailscaleIP(t *testing.T) {
	d := openTestDB(t)
	seedExitServer(t, d, "n2", "emilia", "100.64.0.3", "", "", "auto via Tailscale IP", 1, 0)
	got, err := LookupExitServerSSHTarget(d, "emilia")
	if err != nil {
		t.Fatalf("LookupExitServerSSHTarget: %v", err)
	}
	if got != "root@100.64.0.3" {
		t.Errorf("expected root@<tailscale_ip>, got %q", got)
	}
}

// TestLookupExitServerSSHTarget_BothEmptyReturnsEmpty pins case 3:
// when BOTH ssh_target and tailscale_ip are empty (ensureExitServers
// hasn't run yet, or the node was just added manually and headscale
// hasn't returned its IP), the helper returns "". The caller must
// surface a clear "no SSH target" error instead of silently
// falling back to nodeHostname — that's the v0.33.1-era
// "Could not resolve hostname relay-N" trap.
func TestLookupExitServerSSHTarget_BothEmptyReturnsEmpty(t *testing.T) {
	d := openTestDB(t)
	seedExitServer(t, d, "n3", "fresh", "", "", "", "no IP yet", 1, 0)
	got, err := LookupExitServerSSHTarget(d, "fresh")
	if err != nil {
		t.Fatalf("LookupExitServerSSHTarget: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty string when both columns are empty, got %q", got)
	}
}

// TestLookupExitServerSSHTarget_NotFoundReturnsEmpty pins the
// "row missing" case: returns ("", nil), same as the row-exists-
// but-both-empty case above. The caller can treat both cases
// identically (no row OR row with no SSH info → "" → hard error
// in the SSH path).
func TestLookupExitServerSSHTarget_NotFoundReturnsEmpty(t *testing.T) {
	d := openTestDB(t)
	got, err := LookupExitServerSSHTarget(d, "no-such-host")
	if err != nil {
		t.Errorf("expected nil err on no-match, got %v", err)
	}
	if got != "" {
		t.Errorf("expected empty fallback, got %q", got)
	}
}

// TestLookupExitServerSSHTarget_PicksFirstIPFromList pins the
// v0.33.1.30 B82 follow-up: ensureExitServers stores the
// headscale IPAddresses array as a comma-joined list in
// tailscale_ip (e.g. "100.64.0.3,fd7a:115c:a1e0::3"). The
// B81 helper returned this list verbatim, which made the
// `ssh` CLI barf (it doesn't parse a comma in the target).
// The fix: take the first IP from the list (typically the
// IPv4, which headscale's API returns first). The raw
// tailscale_ip column stays untouched for the
// /admin/exit-nodes table render (which can show the full
// list for diagnostic purposes).
func TestLookupExitServerSSHTarget_PicksFirstIPFromList(t *testing.T) {
	d := openTestDB(t)
	// Real-world case: emilia on the live VM has BOTH an
	// IPv4 and an IPv6 address; the headscale API returns
	// IPv4 first. The B82 follow-up should pick the IPv4
	// and use it as the SSH target.
	seedExitServer(t, d, "n3", "emilia", "100.64.0.3,fd7a:115c:a1e0::3", "", "", "v0.33.1.30 B82 follow-up: dual-stack", 1, 0)
	got, err := LookupExitServerSSHTarget(d, "emilia")
	if err != nil {
		t.Fatalf("LookupExitServerSSHTarget: %v", err)
	}
	if got != "root@100.64.0.3" {
		t.Errorf("expected first IP (IPv4) from the comma-joined list, got %q", got)
	}
	// Also: trailing whitespace in the list (the headscale
	// API doesn't add it, but a future refactor might).
	seedExitServer(t, d, "n4", "sharlotta", "100.64.0.4, fd7a:115c:a1e0::4", "", "", "with space", 1, 0)
	got, err = LookupExitServerSSHTarget(d, "sharlotta")
	if err != nil {
		t.Fatalf("LookupExitServerSSHTarget: %v", err)
	}
	if got != "root@100.64.0.4" {
		t.Errorf("expected first IP (with whitespace stripped), got %q", got)
	}
}

// TestLookupExitServerSSHTarget_B85SSHPortSufix pins the
// v0.33.1.33 B85 contract: when ssh_target is empty AND
// tailscale_ip is set AND ssh_port is set, the helper returns
// "root@<tailscale_ip>:<ssh_port>" — the per-row non-default
// port suffix for the B81 auto-fallback.
//
// Background. The operator observed (2026-08-10) that the
// B81 auto-fallback hard-codes port 22, but exit-nodes may
// have sshd on a non-standard port (2222 / 8022 / etc. — the
// design intent is "use Tailscale for SSH because the standard
// public path may be blocked, AND remember the exit-node may
// have other ports open"). The B85 fix adds the per-row
// exit_servers.ssh_port column; when set, the auto-fallback
// appends ":<port>". Empty ssh_port = no suffix = port 22
// (preserves the v0.33.1.29/v0.33.1.32 behaviour, so the
// migration is a no-op for operators who don't need a
// non-default port).
//
// The SetAdvertisedRoutes helper at
// internal/headscale/routes.go:222-230 already parses
// "user@host:port" syntax (target + -p <port> for ssh), so
// the B85 value just slots into the existing string. No
// headscale-side changes.
func TestLookupExitServerSSHTarget_B85SSHPortSuffix(t *testing.T) {
	d := openTestDB(t)
	// Direct INSERT (the seedExitServer helper doesn't take
	// ssh_port — we add the column in V053 and don't want to
	// break the existing helper's signature for tests that
	// pre-date B85).
	if _, err := d.Exec(
		`INSERT INTO exit_servers
			(node_id, hostname, tailscale_ip, ssh_target, ssh_key_path, description, enabled, accept_routes, ssh_port)
			VALUES (?, ?, ?, ?, ?, ?, 1, 0, ?)`,
		"n-b85", "karolina", "100.64.0.2", "", "", "B85 non-default port test", "18022",
	); err != nil {
		t.Fatalf("seed with ssh_port: %v", err)
	}
	got, err := LookupExitServerSSHTarget(d, "karolina")
	if err != nil {
		t.Fatalf("LookupExitServerSSHTarget: %v", err)
	}
	if got != "root@100.64.0.2:18022" {
		t.Errorf("B85: expected root@<tailscale_ip>:<ssh_port>, got %q", got)
	}
}

// TestLookupExitServerSSHTarget_B85EmptyPortNoSuffix pins the
// backward-compat half of the B85 contract: an empty
// ssh_port (the default for existing rows) produces the
// v0.33.1.29 B81 string "root@<tailscale_ip>" with NO port
// suffix. This is what the live VM sees today (emilia /
// karolina / sharlotta were inserted pre-B85, so their
// ssh_port is the empty-string default). The migration is
// a no-op for them — the auto-fallback still uses port 22.
func TestLookupExitServerSSHTarget_B85EmptyPortNoSuffix(t *testing.T) {
	d := openTestDB(t)
	seedExitServer(t, d, "n-b85b", "emilia", "100.64.0.3", "", "", "B85 backward compat: empty port", 1, 0)
	// Explicitly verify the column exists and is empty (the
	// v0.33.1.29 B81 string in pre-B85 rows).
	var port string
	if err := d.QueryRow(`SELECT ssh_port FROM exit_servers WHERE hostname='emilia'`).Scan(&port); err != nil {
		t.Fatalf("read ssh_port: %v", err)
	}
	if port != "" {
		t.Fatalf("precondition: ssh_port should be empty for pre-B85 row, got %q", port)
	}
	got, err := LookupExitServerSSHTarget(d, "emilia")
	if err != nil {
		t.Fatalf("LookupExitServerSSHTarget: %v", err)
	}
	if got != "root@100.64.0.3" {
		t.Errorf("B85: empty ssh_port must produce no port suffix, got %q", got)
	}
}

// TestLookupExitServerSSHTarget_B85OperatorOverrideIgnoresPort
// pins the priority: when ssh_target is set, the operator's
// override wins — even when ssh_port is ALSO set. The
// operator's "user@host:port" string already includes the
// port, so appending ssh_port would double-append. The
// B85 fix only affects the AUTO-FALLBACK (case 2), not
// the operator override (case 1).
func TestLookupExitServerSSHTarget_B85OperatorOverrideIgnoresPort(t *testing.T) {
	d := openTestDB(t)
	if _, err := d.Exec(
		`INSERT INTO exit_servers
			(node_id, hostname, tailscale_ip, ssh_target, ssh_key_path, description, enabled, accept_routes, ssh_port)
			VALUES (?, ?, ?, ?, ?, ?, 1, 0, ?)`,
		"n-b85c", "emilia", "100.64.0.3", "root@emilia.example.com:18022", "", "B85 operator override + ssh_port", "9999",
	); err != nil {
		t.Fatalf("seed: %v", err)
	}
	got, err := LookupExitServerSSHTarget(d, "emilia")
	if err != nil {
		t.Fatalf("LookupExitServerSSHTarget: %v", err)
	}
	// Operator override wins verbatim — ssh_port (9999) is
	// NOT appended. The override's :18022 is the canonical
	// port, not the column's value.
	if got != "root@emilia.example.com:18022" {
		t.Errorf("B85: operator override must win verbatim (no ssh_port append), got %q", got)
	}
}

// TestMigrateV053_AddsSSHPortColumn pins the v0.33.1.33
// migration: V053 adds the exit_servers.ssh_port column.
//
// openTestDB already runs the full Open() chain (which
// includes migrateV053), so by the time this test starts
// the column is already there. We verify (a) the column
// exists, (b) it has the empty-string default, (c) the
// DEFAULT applies to new rows, and (d) UPDATE round-trips.
// Re-running migrateV053 against the post-Open schema is
// intentionally NOT tested (it's the production-time path,
// not the test path; the ALTER TABLE ADD COLUMN would
// fail with "duplicate column name", which is why the
// applied_migrations table check upstream of V053 in db.go
// short-circuits on a re-run).
func TestMigrateV053_AddsSSHPortColumn(t *testing.T) {
	d := openTestDB(t)

	// Verify the column exists + has the empty-string default.
	var defaultValue string
	if err := d.QueryRow(
		`SELECT COALESCE(dflt_value, '') FROM pragma_table_info('exit_servers') WHERE name='ssh_port'`,
	).Scan(&defaultValue); err != nil {
		t.Fatalf("read column info: %v", err)
	}
	// SQLite returns the default as "''" (with the single
	// quotes from the DDL), or as "" for the empty default.
	if defaultValue != "''" && defaultValue != "" {
		t.Errorf("ssh_port default should be '' (empty), got %q", defaultValue)
	}

	// Insert + read back: empty default preserved.
	if _, err := d.Exec(
		`INSERT INTO exit_servers(node_id, hostname, tailscale_ip) VALUES ('mig1', 'emilia', '100.64.0.3')`,
	); err != nil {
		t.Fatalf("insert pre-B85-style row: %v", err)
	}
	var port string
	if err := d.QueryRow(`SELECT ssh_port FROM exit_servers WHERE hostname='emilia'`).Scan(&port); err != nil {
		t.Fatalf("read ssh_port: %v", err)
	}
	if port != "" {
		t.Errorf("ssh_port should be '' for new row (DEFAULT), got %q", port)
	}

	// Update the port + read it back.
	if _, err := d.Exec(`UPDATE exit_servers SET ssh_port = '18022' WHERE hostname='emilia'`); err != nil {
		t.Fatalf("update ssh_port: %v", err)
	}
	if err := d.QueryRow(`SELECT ssh_port FROM exit_servers WHERE hostname='emilia'`).Scan(&port); err != nil {
		t.Fatalf("read updated ssh_port: %v", err)
	}
	if port != "18022" {
		t.Errorf("ssh_port update should round-trip, got %q", port)
	}
}

// --- UpsertExitServer ---

func TestUpsertExitServer_InsertsNew(t *testing.T) {
	d := openTestDB(t)
	if err := UpsertExitServer(d, "node-1", "alpha", "root@a", "/k", "first", "", 1); err != nil {
		t.Fatalf("UpsertExitServer: %v", err)
	}
	got, err := ListExitServers(d)
	if err != nil {
		t.Fatalf("ListExitServers: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 row, got %d", len(got))
	}
	e := got[0]
	if e.NodeID != "node-1" || e.Hostname != "alpha" || e.SSHTarget != "root@a" ||
		e.SSHKeyPath != "/k" || e.Description != "first" || e.AcceptRoutes != 1 || !e.Enabled {
		t.Errorf("unexpected row: %+v", e)
	}
}

func TestUpsertExitServer_ReplacesOnConflict(t *testing.T) {
	d := openTestDB(t)
	// First insert via the seed (so we have known state, including enabled).
	seedExitServer(t, d, "node-1", "alpha", "10.0.0.1", "old@a", "/old", "old desc", 1, 0)
	// Re-upsert with a different hostname, ssh, description, accept_routes.
	if err := UpsertExitServer(d, "node-1", "alpha-new", "new@b", "/new", "new desc", "", -1); err != nil {
		t.Fatalf("UpsertExitServer: %v", err)
	}
	got, err := ListExitServers(d)
	if err != nil {
		t.Fatalf("ListExitServers: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 row (replace, not duplicate), got %d", len(got))
	}
	e := got[0]
	if e.Hostname != "alpha-new" || e.SSHTarget != "new@b" || e.SSHKeyPath != "/new" ||
		e.Description != "new desc" || e.AcceptRoutes != -1 {
		t.Errorf("re-upsert did not replace: %+v", e)
	}
}

// --- DeleteExitServerByNodeID ---

func TestDeleteExitServerByNodeID(t *testing.T) {
	d := openTestDB(t)
	seedExitServer(t, d, "node-1", "alpha", "", "", "", "", 1, 0)
	seedExitServer(t, d, "node-2", "beta", "", "", "", "", 1, 0)
	if err := DeleteExitServerByNodeID(d, "node-1"); err != nil {
		t.Fatalf("DeleteExitServerByNodeID: %v", err)
	}
	got, err := ListExitServers(d)
	if err != nil {
		t.Fatalf("ListExitServers: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 row after delete, got %d", len(got))
	}
	if got[0].NodeID != "node-2" {
		t.Errorf("wrong row remaining: %+v", got[0])
	}
}

func TestDeleteExitServerByNodeID_Idempotent(t *testing.T) {
	d := openTestDB(t)
	// Deleting a non-existent node_id must be a no-op (no error).
	if err := DeleteExitServerByNodeID(d, "does-not-exist"); err != nil {
		t.Errorf("delete of missing row returned error: %v", err)
	}
}

// --- InsertIgnoreExitServerOnDiscovery ---

func TestInsertIgnoreExitServerOnDiscovery_InsertsWhenMissing(t *testing.T) {
	d := openTestDB(t)
	if err := InsertIgnoreExitServerOnDiscovery(d, "node-1", "alpha", "100.0.0.1"); err != nil {
		t.Fatalf("InsertIgnoreExitServerOnDiscovery: %v", err)
	}
	got, err := ListExitServers(d)
	if err != nil {
		t.Fatalf("ListExitServers: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 row, got %d", len(got))
	}
	e := got[0]
	if e.NodeID != "node-1" || e.Hostname != "alpha" || e.TailscaleIP != "100.0.0.1" {
		t.Errorf("unexpected row: %+v", e)
	}
	// Default values from schema: enabled=1, accept_routes=0.
	if !e.Enabled {
		t.Errorf("expected default Enabled=true, got false")
	}
	if e.AcceptRoutes != 0 {
		t.Errorf("expected default AcceptRoutes=0, got %d", e.AcceptRoutes)
	}
	// Admin-curated fields must remain default (empty strings).
	if e.SSHTarget != "" || e.SSHKeyPath != "" || e.Description != "" {
		t.Errorf("admin fields should be default, got: %+v", e)
	}
}

func TestInsertIgnoreExitServerOnDiscovery_RespectsAdminRow(t *testing.T) {
	d := openTestDB(t)
	// Admin previously added this node with enabled=0 (they want it
	// disabled). Discovery should NOT clobber that.
	seedExitServer(t, d, "node-1", "alpha", "10.0.0.1", "root@a", "/k", "admin desc", 0, -1)
	if err := InsertIgnoreExitServerOnDiscovery(d, "node-1", "alpha-DIFFERENT", "100.0.0.99"); err != nil {
		t.Fatalf("InsertIgnoreExitServerOnDiscovery: %v", err)
	}
	got, err := ListExitServers(d)
	if err != nil {
		t.Fatalf("ListExitServers: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 row, got %d", len(got))
	}
	e := got[0]
	// The admin's hostname (alpha) must be preserved — discovery's
	// "alpha-DIFFERENT" must be ignored.
	if e.Hostname != "alpha" {
		t.Errorf("discovery overwrote admin hostname: %+v", e)
	}
	if e.TailscaleIP != "10.0.0.1" {
		t.Errorf("discovery overwrote admin tailscale_ip: %+v", e)
	}
	if e.Enabled {
		t.Errorf("discovery flipped admin's enabled=false: %+v", e)
	}
	if e.AcceptRoutes != -1 {
		t.Errorf("discovery overwrote admin's accept_routes: %+v", e)
	}
	if e.SSHTarget != "root@a" || e.SSHKeyPath != "/k" || e.Description != "admin desc" {
		t.Errorf("discovery clobbered admin ssh/description: %+v", e)
	}
}
