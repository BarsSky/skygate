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

// --- UpsertExitServer ---

func TestUpsertExitServer_InsertsNew(t *testing.T) {
	d := openTestDB(t)
	if err := UpsertExitServer(d, "node-1", "alpha", "root@a", "/k", "first", 1); err != nil {
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
	if err := UpsertExitServer(d, "node-1", "alpha-new", "new@b", "/new", "new desc", -1); err != nil {
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
