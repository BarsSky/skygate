// internal/feature/admin/users_demote_test.go — v0.72 (B264) tests for
// admin role delegation on /admin/users.
//
// B264 lets an administrator grant AND revoke the `admin` role for other
// portal users, while the primary (bootstrap/root) admin account — the
// single portal_users.is_primary=1 row, see
// internal/db/migrations_v0_72_admin_primary.go — stays immutable.
//
// These are pure handler tests (SQLite :memory: through the real migration
// chain, a stub Backend for claims + audit), following the pattern of
// users_promote_test.go and users_rename_test.go. s.I18n is deliberately
// left nil: usersI18n (users.go) then returns the raw key, which is what
// the refusal-flash assertions look for.
//
// Covered guards:
//   - happy path: is_admin flips 1 → 0 + 'admin_demote' audit row
//   - idempotent: already non-admin → ?already_user= flash, no audit
//   - primary: refused with users.err_primary_immutable
//   - self: refused with users.err_cannot_demote_self
//   - last admin: refused with users.err_last_admin
//   - non-admin caller: 403
//   - missing row: 404
//   - Delete / Rename refuse the primary row
//   - Promote works for an arbitrary per-row user (not only the drift banner)

package admin

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"skygate/internal/auth"
)

// demoteStubBackend is the claims/audit stub for the demote tests.
// CurrentUser returns a configurable user id so the self-demotion guard
// can be exercised (the operator is not necessarily row 1).
type demoteStubBackend struct {
	admin     bool
	userID    int64
	auditRows *[]capturedAudit
}

func (s *demoteStubBackend) Render(w http.ResponseWriter, r *http.Request, name string, data any) {}
func (s *demoteStubBackend) RenderWithLayout(w http.ResponseWriter, r *http.Request, name string, c *auth.Claims, data map[string]any) {
}
func (s *demoteStubBackend) CurrentUser(r *http.Request) *auth.Claims {
	id := s.userID
	if id == 0 {
		id = 1
	}
	return &auth.Claims{UserID: id, Username: "operator", IsAdmin: s.admin}
}
func (s *demoteStubBackend) Audit(userID int64, username, action, detail string) {
	if s.auditRows != nil {
		*s.auditRows = append(*s.auditRows, capturedAudit{userID, username, action, detail})
	}
}
func (s *demoteStubBackend) InfraAuditIdentity(fallbackUID int64, fallbackUsername string) (int64, string) {
	return fallbackUID, fallbackUsername
}

// setupDemoteTestDB seeds three portal users on top of setupRenameDB
// (which already inserts id=1 'skyadmin', is_admin=1, hs_id=86):
//
//	id=2 'bob'   is_admin=1 (a delegated admin — the demote target)
//	id=3 'carol' is_admin=0 (the already-a-regular-user case)
//
// is_primary stays 0 for all of them: the primary marker is set
// explicitly by the tests that need it.
func setupDemoteTestDB(t *testing.T) *sql.DB {
	t.Helper()
	sqlDB := setupRenameDB(t)
	if _, err := sqlDB.Exec(
		`INSERT INTO portal_users (id, username, password_hash, is_admin, is_primary, headscale_user_id)
		 VALUES (2, 'bob', 'hash-bob', 1, 0, 87),
		        (3, 'carol', 'hash-carol', 0, 0, 88)`,
	); err != nil {
		t.Fatalf("seed bob/carol: %v", err)
	}
	return sqlDB
}

func markPrimary(t *testing.T, sqlDB *sql.DB, id int64) {
	t.Helper()
	if _, err := sqlDB.Exec(`UPDATE portal_users SET is_primary = 1 WHERE id = ?`, id); err != nil {
		t.Fatalf("mark primary id=%d: %v", id, err)
	}
}

func isAdminOf(t *testing.T, sqlDB *sql.DB, id int64) int {
	t.Helper()
	var n int
	if err := sqlDB.QueryRow(`SELECT is_admin FROM portal_users WHERE id = ?`, id).Scan(&n); err != nil {
		t.Fatalf("read is_admin id=%d: %v", id, err)
	}
	return n
}

func TestPostAdminUserDemote_HappyPath(t *testing.T) {
	src := setupDemoteTestDB(t)
	var rows []capturedAudit
	svc := &Service{
		DB:      &dbDBSource{db: src},
		Backend: &demoteStubBackend{admin: true, userID: 1, auditRows: &rows},
	}

	req := httptest.NewRequest("POST", "/admin/users/2/demote", nil)
	rec := httptest.NewRecorder()
	svc.PostAdminUserDemote(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("want 303 redirect; got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "ok=") {
		t.Errorf("Location = %q; want ok= success flash", loc)
	}
	if got := isAdminOf(t, src, 2); got != 0 {
		t.Errorf("bob is_admin = %d, want 0 (demoted)", got)
	}
	if len(rows) != 1 {
		t.Fatalf("audit rows = %d; want 1", len(rows))
	}
	if rows[0].Action != "admin_demote" {
		t.Errorf("audit.Action = %q; want admin_demote", rows[0].Action)
	}
	if !strings.Contains(rows[0].Detail, "bob") {
		t.Errorf("audit.Detail = %q; want contains bob", rows[0].Detail)
	}
}

func TestPostAdminUserDemote_IdempotentWhenAlreadyUser(t *testing.T) {
	src := setupDemoteTestDB(t)
	var rows []capturedAudit
	svc := &Service{
		DB:      &dbDBSource{db: src},
		Backend: &demoteStubBackend{admin: true, userID: 1, auditRows: &rows},
	}

	// carol (id=3) is already is_admin=0.
	req := httptest.NewRequest("POST", "/admin/users/3/demote", nil)
	rec := httptest.NewRecorder()
	svc.PostAdminUserDemote(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("want 303; got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "already_user=carol") {
		t.Errorf("Location = %q; want already_user=carol (distinct idempotent flash)", loc)
	}
	if len(rows) != 0 {
		t.Errorf("audit rows = %d; want 0 (a no-op must not audit)", len(rows))
	}
}

func TestPostAdminUserDemote_RefusesPrimary(t *testing.T) {
	src := setupDemoteTestDB(t)
	markPrimary(t, src, 1) // skyadmin is the immutable primary
	var rows []capturedAudit
	svc := &Service{
		DB:      &dbDBSource{db: src},
		Backend: &demoteStubBackend{admin: true, userID: 2, auditRows: &rows},
	}

	req := httptest.NewRequest("POST", "/admin/users/1/demote", nil)
	rec := httptest.NewRecorder()
	svc.PostAdminUserDemote(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("want 303; got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "users.err_primary_immutable") {
		t.Errorf("Location = %q; want err=users.err_primary_immutable", loc)
	}
	if got := isAdminOf(t, src, 1); got != 1 {
		t.Errorf("primary is_admin = %d, want 1 (immutable)", got)
	}
	if len(rows) != 0 {
		t.Errorf("audit rows = %d; want 0 (refused)", len(rows))
	}
}

func TestPostAdminUserDemote_RefusesSelf(t *testing.T) {
	src := setupDemoteTestDB(t)
	var rows []capturedAudit
	// The operator IS bob (id=2) and bob is a non-primary admin, so only
	// the self-guard can fire.
	svc := &Service{
		DB:      &dbDBSource{db: src},
		Backend: &demoteStubBackend{admin: true, userID: 2, auditRows: &rows},
	}

	req := httptest.NewRequest("POST", "/admin/users/2/demote", nil)
	rec := httptest.NewRecorder()
	svc.PostAdminUserDemote(rec, req)

	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "users.err_cannot_demote_self") {
		t.Errorf("Location = %q; want err=users.err_cannot_demote_self", loc)
	}
	if got := isAdminOf(t, src, 2); got != 1 {
		t.Errorf("self is_admin = %d, want 1 (refused)", got)
	}
	if len(rows) != 0 {
		t.Errorf("audit rows = %d; want 0 (refused)", len(rows))
	}
}

func TestPostAdminUserDemote_RefusesLastAdmin(t *testing.T) {
	src := setupDemoteTestDB(t)
	// Make bob (id=2) the ONLY admin row, then have a different admin
	// (claims id=1) try to demote him.
	if _, err := src.Exec(`UPDATE portal_users SET is_admin = 0 WHERE id = 1`); err != nil {
		t.Fatalf("clear skyadmin is_admin: %v", err)
	}
	var rows []capturedAudit
	svc := &Service{
		DB:      &dbDBSource{db: src},
		Backend: &demoteStubBackend{admin: true, userID: 1, auditRows: &rows},
	}

	req := httptest.NewRequest("POST", "/admin/users/2/demote", nil)
	rec := httptest.NewRecorder()
	svc.PostAdminUserDemote(rec, req)

	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "users.err_last_admin") {
		t.Errorf("Location = %q; want err=users.err_last_admin", loc)
	}
	if got := isAdminOf(t, src, 2); got != 1 {
		t.Errorf("last admin is_admin = %d, want 1 (refused)", got)
	}
	if len(rows) != 0 {
		t.Errorf("audit rows = %d; want 0 (refused)", len(rows))
	}
}

func TestPostAdminUserDemote_ForbiddenWhenNonAdmin(t *testing.T) {
	src := setupDemoteTestDB(t)
	svc := &Service{
		DB:      &dbDBSource{db: src},
		Backend: &demoteStubBackend{admin: false, userID: 1},
	}

	req := httptest.NewRequest("POST", "/admin/users/2/demote", nil)
	rec := httptest.NewRecorder()
	svc.PostAdminUserDemote(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("want 403; got %d", rec.Code)
	}
	if got := isAdminOf(t, src, 2); got != 1 {
		t.Errorf("bob is_admin = %d, want 1 (no change on forbidden)", got)
	}
}

func TestPostAdminUserDemote_MissingUser(t *testing.T) {
	src := setupDemoteTestDB(t)
	svc := &Service{
		DB:      &dbDBSource{db: src},
		Backend: &demoteStubBackend{admin: true, userID: 1},
	}

	req := httptest.NewRequest("POST", "/admin/users/9999/demote", nil)
	rec := httptest.NewRecorder()
	svc.PostAdminUserDemote(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("want 404; got %d", rec.Code)
	}
}

// TestPostAdminDeleteUser_RefusesPrimary pins the B264 delete guard: the
// primary row must survive a POST even though the template hides the
// button for it (a stale page or a hand-crafted request is the real threat).
func TestPostAdminDeleteUser_RefusesPrimary(t *testing.T) {
	src := setupDemoteTestDB(t)
	markPrimary(t, src, 1)
	svc := &Service{
		DB:      &dbDBSource{db: src},
		Backend: &demoteStubBackend{admin: true, userID: 2},
	}

	req := httptest.NewRequest("POST", "/admin/users/1/delete", nil)
	rec := httptest.NewRecorder()
	svc.PostAdminDeleteUser(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("want 303 redirect; got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "users.err_primary_immutable") {
		t.Errorf("Location = %q; want err=users.err_primary_immutable", loc)
	}
	var n int
	if err := src.QueryRow(`SELECT COUNT(*) FROM portal_users WHERE id = 1`).Scan(&n); err != nil {
		t.Fatalf("count id=1: %v", err)
	}
	if n != 1 {
		t.Error("the primary admin row was deleted — the delete guard did not fire")
	}
}

// TestPostAdminUserRename_RefusesPrimary pins the B264 rename guard:
// renaming the primary would break the SKYGATE_ADMIN_USER match that the
// renegotiated check_b_admin_user_sync.sh contract B asserts.
func TestPostAdminUserRename_RefusesPrimary(t *testing.T) {
	src := setupDemoteTestDB(t)
	markPrimary(t, src, 1)
	svc := &Service{
		DB:      &dbDBSource{db: src},
		Backend: &demoteStubBackend{admin: true, userID: 2},
	}

	body := strings.NewReader("new_username=skyadmin-v2")
	req := httptest.NewRequest("POST", "/admin/users/1/rename", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	svc.PostAdminUserRename(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("want 303 redirect; got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "users.err_primary_immutable") {
		t.Errorf("Location = %q; want err=users.err_primary_immutable", loc)
	}
	var name string
	if err := src.QueryRow(`SELECT username FROM portal_users WHERE id = 1`).Scan(&name); err != nil {
		t.Fatalf("read username: %v", err)
	}
	if name != "skyadmin" {
		t.Errorf("username = %q, want skyadmin (rename must have been refused)", name)
	}
}

// TestPostAdminUserPromote_PerRowArbitraryUser pins the B264 requirement
// that Promote is usable from the per-row menu for ANY user, not only the
// drift-banner case (the target here is an ordinary portal user).
func TestPostAdminUserPromote_PerRowArbitraryUser(t *testing.T) {
	src := setupDemoteTestDB(t)
	var rows []capturedAudit
	svc := &Service{
		DB:      &dbDBSource{db: src},
		Backend: &demoteStubBackend{admin: true, userID: 1, auditRows: &rows},
	}

	// carol (id=3) is a regular user.
	req := httptest.NewRequest("POST", "/admin/users/3/promote", nil)
	rec := httptest.NewRecorder()
	svc.PostAdminUserPromote(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("want 303; got %d", rec.Code)
	}
	if got := isAdminOf(t, src, 3); got != 1 {
		t.Errorf("carol is_admin = %d, want 1 (promoted from the per-row menu)", got)
	}
	if len(rows) != 1 || rows[0].Action != "admin_promote" {
		t.Fatalf("audit rows = %+v; want one admin_promote", rows)
	}
	if !strings.Contains(rows[0].Detail, "carol") {
		t.Errorf("audit.Detail = %q; want contains carol", rows[0].Detail)
	}
}
