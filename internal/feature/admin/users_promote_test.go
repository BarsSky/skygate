// internal/feature/admin/users_promote_test.go — TDD tests for
// v1.5.2 admin-user-sync T6.1: PostAdminUserPromote.
//
// Pre-T6.1, the AdminSyncPromoteToAdmin banner on /admin/users
// (rendered when the portal row exists with the right username
// + linked HS, but is_admin somehow flipped to 0) just explained
// the drift. The operator had to:
//
//   1. Open /admin/users/{id}/edit
//   2. Click "Promote to admin"
//   3. Submit
//
// T6.1 collapses this into a single POST /admin/users/{id}/promote
// button rendered INSIDE the drift banner (mirroring T5's
// "Adopt as Admin" form inside the adopt banner).
//
// Tests verify the dispatch:
//   - happy path: is_admin flips 0 → 1, audit row written
//   - idempotent: is_admin=1 already → no-op, no audit row
//   - non-admin caller: 403 forbidden
//   - missing user id: 400 bad request
//   - username mismatch (defensive — should never happen in
//     practice, but a stale drift banner could refer to a
//     renamed user): refuse with err flash, no DB change

package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"skygate/internal/auth"
)

// promoteStubBackend is the minimal Backend stub for the
// promote-handler tests. Captures audit rows.
type promoteStubBackend struct {
	admin     bool
	auditRows *[]capturedAudit
}

func (s *promoteStubBackend) Render(w http.ResponseWriter, r *http.Request, name string, data any) {}
func (s *promoteStubBackend) RenderWithLayout(w http.ResponseWriter, r *http.Request, name string, c *auth.Claims, data map[string]any) {
}
func (s *promoteStubBackend) CurrentUser(r *http.Request) *auth.Claims {
	return &auth.Claims{UserID: 1, Username: "operator", IsAdmin: s.admin}
}
func (s *promoteStubBackend) Audit(userID int64, username, action, detail string) {
	if s.auditRows != nil {
		*s.auditRows = append(*s.auditRows, capturedAudit{userID, username, action, detail})
	}
}
func (s *promoteStubBackend) InfraAuditIdentity(fallbackUID int64, fallbackUsername string) (int64, string) {
	return fallbackUID, fallbackUsername
}

// setupPromoteTestDB seeds the minimal schema + a single portal user
// (skyadmin, id=1, is_admin=0, hs_id=86) — the drift case the
// banner detects.
func setupPromoteTestDB(t *testing.T) (*dbDBSource, string) {
	t.Helper()
	sqlDB := setupRenameDB(t)
	if _, err := sqlDB.Exec(
		`UPDATE portal_users SET is_admin = 0 WHERE id = 1`,
	); err != nil {
		t.Fatalf("flip is_admin: %v", err)
	}
	return &dbDBSource{db: sqlDB}, "skyadmin"
}

func TestPostAdminUserPromote_HappyPath(t *testing.T) {
	src, username := setupPromoteTestDB(t)
	var rows []capturedAudit
	svc := &Service{
		DB:      src,
		Backend: &promoteStubBackend{admin: true, auditRows: &rows},
	}

	req := httptest.NewRequest("POST", "/admin/users/1/promote", nil)
	rec := httptest.NewRecorder()
	svc.PostAdminUserPromote(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("want 303 redirect; got %d", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Location"), "ok=") {
		t.Errorf("Location = %q; want contains ok= flash", rec.Header().Get("Location"))
	}
	var adminI int
	if err := src.db.QueryRow(`SELECT is_admin FROM portal_users WHERE id = 1`).Scan(&adminI); err != nil {
		t.Fatalf("query: %v", err)
	}
	if adminI != 1 {
		t.Errorf("is_admin = %d, want 1 (drift promoted)", adminI)
	}
	if len(rows) != 1 {
		t.Fatalf("audit rows = %d; want 1", len(rows))
	}
	if rows[0].Action != "admin_promote" {
		t.Errorf("audit.Action = %q; want admin_promote (so operator can grep for the drift fix)",
			rows[0].Action)
	}
	if !strings.Contains(rows[0].Detail, username) {
		t.Errorf("audit.Detail = %q; want contains %q", rows[0].Detail, username)
	}
}

func TestPostAdminUserPromote_Idempotent(t *testing.T) {
	src, _ := setupPromoteTestDB(t)
	// Pre-set is_admin=1 — the user was already an admin (the
	// banner shouldn't even show, but if the operator clicks
	// Promote twice in a race, the second click is a no-op).
	if _, err := src.db.Exec(`UPDATE portal_users SET is_admin = 1 WHERE id = 1`); err != nil {
		t.Fatalf("pre-set is_admin: %v", err)
	}
	var rows []capturedAudit
	svc := &Service{
		DB:      src,
		Backend: &promoteStubBackend{admin: true, auditRows: &rows},
	}

	req := httptest.NewRequest("POST", "/admin/users/1/promote", nil)
	rec := httptest.NewRecorder()
	svc.PostAdminUserPromote(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("want 303; got %d", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Location"), "already_admin=") {
		t.Errorf("Location = %q; want contains already_admin= flash (idempotent UX)", rec.Header().Get("Location"))
	}
	if len(rows) != 0 {
		t.Errorf("audit rows = %d; want 0 (no-op should not audit)", len(rows))
	}
}

func TestPostAdminUserPromote_ForbiddenWhenNonAdmin(t *testing.T) {
	src, _ := setupPromoteTestDB(t)
	svc := &Service{
		DB:      src,
		Backend: &promoteStubBackend{admin: false}, // not admin
	}

	req := httptest.NewRequest("POST", "/admin/users/1/promote", nil)
	rec := httptest.NewRecorder()
	svc.PostAdminUserPromote(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("want 403; got %d", rec.Code)
	}
	var adminI int
	if err := src.db.QueryRow(`SELECT is_admin FROM portal_users WHERE id = 1`).Scan(&adminI); err != nil {
		t.Fatalf("query: %v", err)
	}
	if adminI != 0 {
		t.Errorf("is_admin = %d, want 0 (no change on forbidden)", adminI)
	}
}
