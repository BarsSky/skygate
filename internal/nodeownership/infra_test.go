// infra_test.go — BackfillInfra: the infra-user attribution pass.
//
// BackfillInfra is the second stage of the B77 tick (AutoBackfill → runOneTick
// → Backfill → BackfillInfra). It owns the "system" devices: the skygate VM
// itself (hostname `skygate-host`) and the exit nodes (`tag:exit-node`), which
// must belong to the synthetic 'infra' portal user so the per-infra public
// access grants in the ACL can match them. The original tests used the deleted
// openBackfillTestDB and were replaced by a t.Skip stub in the v1.3.0
// SQLite→PG purge.
//
// These tests run the REAL function against a REAL migrated database (see
// newAutoTestDB in auto_test.go) and pin the current contract:
//
//   - a node matching isInfraNode is RE-ATTRIBUTED to 'infra' even when its row
//     currently belongs to a normal portal user (B111 — the pre-B111
//     INSERT OR IGNORE left the exit nodes stranded in the user buckets, so the
//     per-infra grants missed them),
//   - a matching node with no row yet gets one,
//   - the stored tag is `tag:dev-infra-<lower-case hostname>` (B265 — an
//     upper-case hostname used to produce a tag the ACL could never match),
//   - a database without a LINKED 'infra' user is a silent no-op (nothing to
//     attribute to).

package nodeownership

import (
	"database/sql"
	"testing"

	"skygate/internal/headscale"
)

// TestBackfillInfra_ReattributesExitNodeAndSkygateHost is the operator-visible
// promise: after one pass, both the exit node and the skygate VM belong to
// 'infra' and carry the lower-case infra tag.
func TestBackfillInfra_ReattributesExitNodeAndSkygateHost(t *testing.T) {
	src := newAutoTestDB(t)
	// Migration V054 already inserts portal_users(id=99, username='infra') with
	// headscale_user_id NULL (ensureInfraUser links it at boot), so this test
	// only LINKS it — inserting the row again is a UNIQUE violation.
	if _, err := src.DB.Exec(
		`UPDATE portal_users SET headscale_user_id = 9 WHERE username = 'infra'`); err != nil {
		t.Fatalf("link the infra user: %v", err)
	}
	seedAutoTestUser(t, src.DB, "skyadmin", 1)
	// Pre-B111 state: the exit node is filed under a normal portal user.
	seedAutoTestOwner(t, src.DB, "10", 1, "skyadmin", "tag:dev-skyadmin-Emilia", "Emilia")

	nodes := []headscale.NodeView{
		{ID: "10", Hostname: "Emilia", Tags: []string{"tag:exit-node"}},
		{ID: "11", Hostname: "skygate-host"},
	}
	BackfillInfra(src, nodes)

	// The existing row is re-attributed (not just ignored) — this is what makes
	// the per-infra grants match an exit node that a B69/B89 backfill had put in
	// a user bucket.
	var username, tag string
	var hsID int64
	if err := src.DB.QueryRow(
		`SELECT username, tag, headscale_user_id FROM node_owner_map WHERE node_id = '10'`).
		Scan(&username, &tag, &hsID); err != nil {
		t.Fatalf("node_owner_map has no row for the exit node: %v", err)
	}
	if username != "infra" {
		t.Errorf("exit-node row username = %q, want %q", username, "infra")
	}
	if tag != "tag:dev-infra-emilia" {
		t.Errorf("exit-node row tag = %q, want %q (lower-case hostname)", tag, "tag:dev-infra-emilia")
	}
	if hsID != 9 {
		t.Errorf("exit-node row headscale_user_id = %d, want 9 (the infra headscale user)", hsID)
	}

	// The skygate VM had no row at all: the INSERT half adds it.
	if err := src.DB.QueryRow(
		`SELECT username, tag FROM node_owner_map WHERE node_id = '11'`).
		Scan(&username, &tag); err != nil {
		t.Fatalf("node_owner_map has no row for skygate-host: %v", err)
	}
	if username != "infra" || tag != "tag:dev-infra-skygate-host" {
		t.Errorf("skygate-host row = (%q, %q), want (infra, tag:dev-infra-skygate-host)", username, tag)
	}
}

// TestBackfillInfra_WithoutALinkedInfraUserIsANoOp: the two ways the pass has
// nothing to work with — the V054 'infra' row exists but its
// headscale_user_id is still NULL (migration ran, ensureInfraUser has not
// linked it yet) and no 'infra' row at all (rows deleted by hand / a very old
// schema). Both must return silently and write nothing: a row pointing at
// headscale user 0 would be unusable for the ACL and is worse than no row.
func TestBackfillInfra_WithoutALinkedInfraUserIsANoOp(t *testing.T) {
	nodes := []headscale.NodeView{
		{ID: "10", Hostname: "Emilia", Tags: []string{"tag:exit-node"}},
		{ID: "11", Hostname: "skygate-host"},
	}

	t.Run("v054_infra_row_is_not_linked_yet", func(t *testing.T) {
		src := newAutoTestDB(t)
		seedAutoTestUser(t, src.DB, "skyadmin", 1)
		// Guard the precondition: the migration must have created the row with
		// no headscale id, otherwise this test would silently stop covering the
		// NULL branch.
		var hsID sql.NullInt64
		if err := src.DB.QueryRow(
			`SELECT headscale_user_id FROM portal_users WHERE username = 'infra'`).Scan(&hsID); err != nil {
			t.Fatalf("V054 must create the infra portal user: %v", err)
		}
		if hsID.Valid {
			t.Fatalf("precondition failed: the infra row already has headscale_user_id=%d", hsID.Int64)
		}

		BackfillInfra(src, nodes)

		assertNodeOwnerRows(t, src.DB, 0)
	})

	t.Run("no_infra_row_at_all", func(t *testing.T) {
		src := newAutoTestDB(t)
		if _, err := src.DB.Exec(`DELETE FROM portal_users WHERE username = 'infra'`); err != nil {
			t.Fatalf("delete the infra user: %v", err)
		}

		BackfillInfra(src, nodes)

		assertNodeOwnerRows(t, src.DB, 0)
	})
}

// assertNodeOwnerRows fails unless node_owner_map holds exactly want rows.
func assertNodeOwnerRows(t *testing.T, d *sql.DB, want int) {
	t.Helper()
	var got int
	if err := d.QueryRow(`SELECT COUNT(*) FROM node_owner_map`).Scan(&got); err != nil {
		t.Fatalf("count node_owner_map: %v", err)
	}
	if got != want {
		t.Errorf("node_owner_map rows = %d, want %d (a database with no LINKED infra user must write nothing)", got, want)
	}
}
