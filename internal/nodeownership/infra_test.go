package nodeownership

// infra_test.go — unit tests for the v0.33.1.41 BackfillInfra
// helper (Issue 4, B93). The helper attributes skygate-host-*
// nodes (and any node tagged tag:dev-infra-*) to the
// 'infra' portal user. Tests pin the contract:
//
//   - Skips when no 'infra' row exists (V054 hasn't run).
//   - Skips when 'infra' row has NULL headscale_user_id
//     (ensureInfraUser hasn't linked yet).
//   - Inserts a node_owner_map row for skygate-host-* nodes.
//   - Inserts a row for any node with tag:dev-infra-*.
//   - Idempotent: re-running is a no-op (INSERT OR IGNORE
//     on the node_id PK).
//   - Doesn't steal nodes that already have an owner
//     (the existing row in node_owner_map is preserved).

import (
	"testing"

	"skygate/internal/headscale"
)

// TestBackfillInfra_NoInfraUser_NoInsert — when V054
// hasn't run (or the 'infra' row was deleted), the
// helper is a silent no-op. This is the "fresh DB" path
// for tests that exercise BackfillInfra before V054.
func TestBackfillInfra_NoInfraUser_NoInsert(t *testing.T) {
	d := openBackfillTestDB(t)
	nodes := []headscale.NodeView{
		{ID: "1", Hostname: "skygate-host-1", Tags: []string{"tag:private"}},
	}
	BackfillInfra(d, nodes)
	// No node_owner_map row should be created — the
	// helper short-circuited on the missing 'infra' row.
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM node_owner_map WHERE node_id = '1'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 rows (no infra user), got %d", n)
	}
}

// TestBackfillInfra_UnlinkedInfraUser_NoInsert — the
// 'infra' row exists but headscale_user_id is NULL
// (V054 ran, ensureInfraUser hasn't completed). The
// helper is silent — adding a row with headscale_user_id=0
// would be useless because the per-infra ACL grant
// can't match without a real hs id.
func TestBackfillInfra_UnlinkedInfraUser_NoInsert(t *testing.T) {
	d := openBackfillTestDB(t)
	// Seed the 'infra' row at the reserved id=99 (V054
	// shape) but with NULL headscale_user_id.
	if _, err := d.Exec(
		`INSERT INTO portal_users (id, username, headscale_user_id) VALUES (99, 'infra', 0)`,
	); err != nil {
		t.Fatalf("seed infra: %v", err)
	}
	nodes := []headscale.NodeView{
		{ID: "1", Hostname: "skygate-host-1", Tags: []string{"tag:private"}},
	}
	BackfillInfra(d, nodes)
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM node_owner_map WHERE node_id = '1'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 rows (unlinked infra user), got %d", n)
	}
}

// TestBackfillInfra_SkygateHostPrefix_InsertsRow — the
// main case: a node whose hostname starts with
// 'skygate-host-' is attributed to 'infra'. The row
// uses tag='tag:private' (so the existing per-device
// loose grant in the ACL builder — which matches
// tagsByUser[uname] — picks it up) and
// headscale_user_id=infra's id from headscale.
func TestBackfillInfra_SkygateHostPrefix_InsertsRow(t *testing.T) {
	d := openBackfillTestDB(t)
	// Seed 'infra' at id=99 with headscale_user_id=42
	// (simulating a successful ensureInfraUser link).
	if _, err := d.Exec(
		`INSERT INTO portal_users (id, username, headscale_user_id) VALUES (99, 'infra', 42)`,
	); err != nil {
		t.Fatalf("seed infra: %v", err)
	}
	nodes := []headscale.NodeView{
		{ID: "33", Hostname: "skygate-host-1", Tags: []string{"tag:dev-skyadmin-skygate-vm", "tag:private"}},
	}
	BackfillInfra(d, nodes)
	// Read back the row.
	var username, tag, hostname string
	var hsUID int64
	if err := d.QueryRow(
		`SELECT username, headscale_user_id, tag, hostname FROM node_owner_map WHERE node_id = '33'`,
	).Scan(&username, &hsUID, &tag, &hostname); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if username != "infra" {
		t.Errorf("username = %q, want 'infra'", username)
	}
	if hsUID != 42 {
		t.Errorf("headscale_user_id = %d, want 42", hsUID)
	}
	if tag != "tag:private" {
		t.Errorf("tag = %q, want 'tag:private'", tag)
	}
	if hostname != "skygate-host-1" {
		t.Errorf("hostname = %q, want 'skygate-host-1'", hostname)
	}
}

// TestBackfillInfra_InfraDevTag_InsertsRow — a node
// with tag:dev-infra-<device> is attributed to 'infra'
// (matches the B77 autoupdater's Strategy D shape —
// future nodes the operator marks with the infra tag
// get auto-attributed).
func TestBackfillInfra_InfraDevTag_InsertsRow(t *testing.T) {
	d := openBackfillTestDB(t)
	if _, err := d.Exec(
		`INSERT INTO portal_users (id, username, headscale_user_id) VALUES (99, 'infra', 7)`,
	); err != nil {
		t.Fatalf("seed infra: %v", err)
	}
	nodes := []headscale.NodeView{
		{ID: "5", Hostname: "some-other-vm", Tags: []string{"tag:dev-infra-baz"}},
	}
	BackfillInfra(d, nodes)
	var username string
	if err := d.QueryRow(
		`SELECT username FROM node_owner_map WHERE node_id = '5'`,
	).Scan(&username); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if username != "infra" {
		t.Errorf("username = %q, want 'infra' (matched by tag:dev-infra-*)", username)
	}
}

// TestBackfillInfra_RegularNode_NoInsert — a node that
// doesn't match any of the infra rules (no skygate-host-
// prefix, no tag:dev-infra-*) is left alone. The
// per-portal-user Backfill handles these.
func TestBackfillInfra_RegularNode_NoInsert(t *testing.T) {
	d := openBackfillTestDB(t)
	if _, err := d.Exec(
		`INSERT INTO portal_users (id, username, headscale_user_id) VALUES (99, 'infra', 7)`,
	); err != nil {
		t.Fatalf("seed infra: %v", err)
	}
	nodes := []headscale.NodeView{
		{ID: "10", Hostname: "alice-laptop", Tags: []string{"tag:dev-alice-alice-laptop"}},
		{ID: "11", Hostname: "bob-desktop", Tags: []string{"tag:dev-bob-bob-desktop", "tag:exit-node"}},
	}
	BackfillInfra(d, nodes)
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM node_owner_map`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 rows (regular nodes are not infra), got %d", n)
	}
}

// TestBackfillInfra_Idempotent — running the helper
// twice on the same node list is a no-op the second
// time (INSERT OR IGNORE on the node_id PK).
func TestBackfillInfra_Idempotent(t *testing.T) {
	d := openBackfillTestDB(t)
	if _, err := d.Exec(
		`INSERT INTO portal_users (id, username, headscale_user_id) VALUES (99, 'infra', 7)`,
	); err != nil {
		t.Fatalf("seed infra: %v", err)
	}
	nodes := []headscale.NodeView{
		{ID: "33", Hostname: "skygate-host-1", Tags: []string{"tag:private"}},
	}
	BackfillInfra(d, nodes)
	BackfillInfra(d, nodes) // 2nd run — should be a no-op
	var n int
	if err := d.QueryRow(
		`SELECT count(*) FROM node_owner_map WHERE node_id = '33' AND username = 'infra'`,
	).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 row (idempotent), got %d", n)
	}
}

// TestBackfillInfra_PreservesExistingOwner — the
// INSERT OR IGNORE is on the node_id PK, so a node
// that's already owned by 'skyadmin' (from a prior
// backfill, e.g. B69/B89) is left alone. The helper
// doesn't move ownership — that's an operator decision
// via /admin/devices or a manual node_owner_map UPDATE.
func TestBackfillInfra_PreservesExistingOwner(t *testing.T) {
	d := openBackfillTestDB(t)
	if _, err := d.Exec(
		`INSERT INTO portal_users (id, username, headscale_user_id) VALUES (99, 'infra', 7)`,
	); err != nil {
		t.Fatalf("seed infra: %v", err)
	}
	// Pre-existing row: skygate-host-1 already owned by
	// 'skyadmin' from the B89 backfill.
	if _, err := d.Exec(
		`INSERT INTO node_owner_map (node_id, username, headscale_user_id, tag, hostname) VALUES ('33', 'skyadmin', 1, 'tag:dev-skyadmin-skygate-vm', 'skygate-host-1')`,
	); err != nil {
		t.Fatalf("seed pre-existing row: %v", err)
	}
	nodes := []headscale.NodeView{
		{ID: "33", Hostname: "skygate-host-1", Tags: []string{"tag:dev-skyadmin-skygate-vm", "tag:private"}},
	}
	BackfillInfra(d, nodes)
	// The pre-existing row should still be there.
	var username, tag string
	if err := d.QueryRow(
		`SELECT username, tag FROM node_owner_map WHERE node_id = '33'`,
	).Scan(&username, &tag); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if username != "skyadmin" {
		t.Errorf("username = %q, want 'skyadmin' (existing owner preserved)", username)
	}
	if tag != "tag:dev-skyadmin-skygate-vm" {
		t.Errorf("tag = %q, want pre-existing tag preserved", tag)
	}
}

// TestIsInfraNode — the predicate. Pure logic, no DB.
func TestIsInfraNode(t *testing.T) {
	cases := []struct {
		name string
		n    headscale.NodeView
		want bool
	}{
		{
			name: "skygate-host- prefix",
			n:    headscale.NodeView{Hostname: "skygate-host-1"},
			want: true,
		},
		{
			name: "skygate-host-vm-test (longer name)",
			n:    headscale.NodeView{Hostname: "skygate-host-vm-test"},
			want: true,
		},
		{
			name: "tag:dev-infra- tag",
			n:    headscale.NodeView{Hostname: "anything", Tags: []string{"tag:dev-infra-foo"}},
			want: true,
		},
		{
			name: "tag:dev-skyadmin- (not infra)",
			n:    headscale.NodeView{Hostname: "alice-laptop", Tags: []string{"tag:dev-skyadmin-skygate-vm"}},
			want: false,
		},
		{
			name: "regular user device",
			n:    headscale.NodeView{Hostname: "alice-laptop", Tags: []string{"tag:dev-alice-alice-laptop"}},
			want: false,
		},
		{
			name: "relay-1 (exit node, not infra)",
			n:    headscale.NodeView{Hostname: "relay-1", Tags: []string{"tag:exit-node"}},
			want: false,
		},
		{
			name: "skygate- prefix only is NOT infra (catches skygate-subnet-alice)",
			n:    headscale.NodeView{Hostname: "skygate-subnet-alice"},
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isInfraNode(c.n); got != c.want {
				t.Errorf("isInfraNode(%+v) = %v, want %v", c.n, got, c.want)
			}
		})
	}
}
