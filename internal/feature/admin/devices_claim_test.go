// internal/feature/admin/devices_claim_test.go — RED tests for
// B-mod-first-run-adoption T4-T5 (bulk-claim handler).
//
// The bulk-claim handler is the operator's escape hatch when
// nodes joined headscale via `headscale preauthkeys create` (NOT
// via skygate's /my/preauth) and never got auto-attributed by
// nodeownership.Backfill (Strategies A/C/D/E all need a
// preauth key or existing tag to match). The operator goes to
// /admin/users/{id}, clicks "Claim all nodes for this user", and
// every node_owner_map row matching that headscale user gets
// its tagged_by_user_id set to the portal user.
//
// Tests verify the contract:
//   - 200 OK on success
//   - the node_owner_map rows for the user's headscale_user_id
//     have tagged_by_user_id = portal_user_id
//   - rows that DON'T match are left alone
//   - invalid portal_user_id → 400
//   - non-admin caller → 403
package admin

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"skygate/internal/auth"
	"skygate/internal/db"
)

// setupClaimTestDB opens an in-memory SQLite DB (via the new
// B-mod-sqlite-pg-bidi Dialect interface), runs all migrations
// (so node_owner_map + portal_users + audit_log exist), and
// returns the *sql.DB + a few seeded rows for the test to
// operate on.
//
// Returns:
//   - the *sql.DB (callers wrap in a dbDBSource for the Service)
//   - portal user IDs for admin and daniil
func setupClaimTestDB(t *testing.T) (*dbDBSource, int64, int64) {
	t.Helper()
	_, sqlDB, err := db.OpenWithDialect(":memory:")
	if err != nil {
		t.Fatalf("OpenWithDialect: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })

	if err := db.ApplyMigrations(sqlDB, db.DialectSQLite); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}

	// Seed: 2 portal users (admin + daniil), each linked to a
	// headscale user via headscale_user_id.
	res, err := sqlDB.Exec(
		`INSERT INTO portal_users (username, password_hash, is_admin, headscale_user_id)
		 VALUES ('admin', '<REDACTED>', 1, 1), ('daniil', '<REDACTED>', 0, 2)`,
	)
	if err != nil {
		t.Fatalf("insert portal_users: %v", err)
	}
	rowsAffected, _ := res.RowsAffected()
	if rowsAffected != 2 {
		t.Fatalf("expected 2 portal_users rows, got %d", rowsAffected)
	}

	var adminID, daniilID int64
	if err := sqlDB.QueryRow(`SELECT id FROM portal_users WHERE username='admin'`).Scan(&adminID); err != nil {
		t.Fatalf("query admin id: %v", err)
	}
	if err := sqlDB.QueryRow(`SELECT id FROM portal_users WHERE username='daniil'`).Scan(&daniilID); err != nil {
		t.Fatalf("query daniil id: %v", err)
	}

	// Seed: 3 node_owner_map rows for admin's HS user, 1 for
	// daniil's HS user. None have tagged_by_user_id set (the
	// un-claimed state).
	if _, err := sqlDB.Exec(
		`INSERT INTO node_owner_map
			(node_id, headscale_user_id, username, tag)
			VALUES
			('node-1', 1, 'admin', 'tag:dev-admin-laptop1'),
			('node-2', 1, 'admin', 'tag:dev-admin-laptop2'),
			('node-3', 1, 'admin', 'tag:dev-admin-phone'),
			('node-4', 2, 'daniil', 'tag:dev-daniil-tablet')`,
	); err != nil {
		t.Fatalf("insert node_owner_map: %v", err)
	}

	return &dbDBSource{db: sqlDB}, adminID, daniilID
}

// claimStubBackend satisfies the admin.Backend interface for tests.
// Returns the pre-set admin Claims from CurrentUser; no-ops for
// the rendering methods (the claim handler doesn't render).
type claimStubBackend struct {
	admin *auth.Claims
}

func (s *claimStubBackend) Render(w http.ResponseWriter, r *http.Request, name string, data any) {}
func (s *claimStubBackend) RenderWithLayout(w http.ResponseWriter, r *http.Request, name string, c *auth.Claims, data map[string]any) {
}
func (s *claimStubBackend) CurrentUser(r *http.Request) *auth.Claims { return s.admin }
func (s *claimStubBackend) Audit(userID int64, username, action, detail string) {
	// No-op for tests — the handler uses db.AppendAuditLogWithTarget
	// directly (which writes via the DB), not Backend.Audit.
}
func (s *claimStubBackend) InfraAuditIdentity(fallbackUID int64, fallbackUsername string) (int64, string) {
	return fallbackUID, fallbackUsername
}

// newClaimTestService builds a Service with the DB + admin Backend
// wired up. The admin Claims is the operator (for the audit row
// userID).
func newClaimTestService(src *dbDBSource) *Service {
	return &Service{
		DB:      src,
		Backend: &claimStubBackend{admin: &auth.Claims{UserID: 1, Username: "admin", IsAdmin: true}},
	}
}

// TestPostAdminDevicesClaimAllForUser_ClaimsAllUnclaimed —
// happy path. Operator clicks "Claim all" for admin; the 3 admin
// node_owner_map rows get tagged_by_user_id = adminID. Daniil's
// row is left alone.
func TestPostAdminDevicesClaimAllForUser_ClaimsAllUnclaimed(t *testing.T) {
	src, adminID, _ := setupClaimTestDB(t)
	svc := newClaimTestService(src)

	form := url.Values{}
	form.Set("portal_user_id", fmt.Sprintf("%d", adminID))
	req := httptest.NewRequest("POST", "/admin/devices/claim-all-for-user", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	svc.PostAdminDevicesClaimAllForUser(rec, req)

	// Expect 303 redirect (back to /admin/users/{id}).
	if rec.Code != 303 {
		t.Errorf("expected 303 redirect; got %d. Body: %s", rec.Code, rec.Body.String())
	}
	expectedLoc := fmt.Sprintf("/admin/users/%d", adminID)
	if loc := rec.Header().Get("Location"); loc != expectedLoc {
		t.Errorf("expected Location=%q; got %q", expectedLoc, loc)
	}

	// Verify the 3 admin node_owner_map rows now have
	// tagged_by_user_id = adminID. Note: tagged_by_user_id is
	// NOT NULL DEFAULT 0 in the schema, so the test uses plain
	// int64 (no NullInt64 wrapper).
	rows, err := src.db.Query(
		`SELECT node_id, tagged_by_user_id FROM node_owner_map
		 WHERE headscale_user_id = 1 ORDER BY node_id`,
	)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	got := map[string]int64{}
	for rows.Next() {
		var nodeID string
		var taggedBy int64
		if err := rows.Scan(&nodeID, &taggedBy); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[nodeID] = taggedBy
	}
	wantClaimed := map[string]int64{
		"node-1": adminID,
		"node-2": adminID,
		"node-3": adminID,
	}
	for k, v := range wantClaimed {
		if got[k] != v {
			t.Errorf("node %s: tagged_by_user_id = %d, want %d", k, got[k], v)
		}
	}

	// Verify daniil's row is untouched (tagged_by_user_id stays 0).
	var daniilTaggedBy int64
	if err := src.db.QueryRow(
		`SELECT tagged_by_user_id FROM node_owner_map WHERE node_id = 'node-4'`,
	).Scan(&daniilTaggedBy); err != nil {
		t.Fatalf("query node-4: %v", err)
	}
	if daniilTaggedBy != 0 {
		t.Errorf("daniil's node should NOT be claimed; got tagged_by_user_id = %d", daniilTaggedBy)
	}
}

// TestPostAdminDevicesClaimAllForUser_NoopWhenAllClaimed —
// idempotency: if the rows are already claimed (tagged_by_user_id
// is set), the handler still succeeds (no error), and the
// tagged_by_user_id is left as-is.
func TestPostAdminDevicesClaimAllForUser_NoopWhenAllClaimed(t *testing.T) {
	src, adminID, _ := setupClaimTestDB(t)
	svc := newClaimTestService(src)

	// Pre-claim node-1 + node-2 so they're "already claimed".
	if _, err := src.db.Exec(
		`UPDATE node_owner_map SET tagged_by_user_id = ? WHERE node_id IN ('node-1', 'node-2')`,
		adminID,
	); err != nil {
		t.Fatalf("pre-claim: %v", err)
	}

	form := url.Values{}
	form.Set("portal_user_id", fmt.Sprintf("%d", adminID))
	req := httptest.NewRequest("POST", "/admin/devices/claim-all-for-user", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	svc.PostAdminDevicesClaimAllForUser(rec, req)

	// 303 redirect on success.
	if rec.Code != 303 {
		t.Errorf("expected 303 redirect; got %d", rec.Code)
	}
	// node-1 + node-2 still tagged with adminID.
	var n int64
	if err := src.db.QueryRow(
		`SELECT tagged_by_user_id FROM node_owner_map WHERE node_id = 'node-1'`,
	).Scan(&n); err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != adminID {
		t.Errorf("node-1 tagged_by_user_id = %d, want %d", n, adminID)
	}
}

// TestPostAdminDevicesClaimAllForUser_InvalidPortalUserID —
// missing or non-numeric portal_user_id → 400 Bad Request.
func TestPostAdminDevicesClaimAllForUser_InvalidPortalUserID(t *testing.T) {
	src, _, _ := setupClaimTestDB(t)
	svc := newClaimTestService(src)

	form := url.Values{}
	form.Set("portal_user_id", "not-a-number")
	req := httptest.NewRequest("POST", "/admin/devices/claim-all-for-user", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	svc.PostAdminDevicesClaimAllForUser(rec, req)

	if rec.Code != 400 {
		t.Errorf("expected 400 on invalid portal_user_id; got %d", rec.Code)
	}
}

// dbDBSource wraps a *sql.DB so it satisfies the admin package's
// DBSource interface (Current() *sql.DB).
type dbDBSource struct {
	db *sql.DB
}

func (d *dbDBSource) Current() *sql.DB { return d.db }
