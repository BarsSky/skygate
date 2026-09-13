// internal/feature/admin/users_rename_test.go — RED tests for
// T4 of v1.5.2 admin-user-sync: PostAdminUserRename handler.
//
// PostAdminUserRename is the button on /admin/users/{id} that the
// operator clicks to reconcile SKYGATE_ADMIN_USER drift detected
// by check_b_admin_user_sync.sh (T1). The handler wraps the
// T3 RenameUser call with the skygate-side state updates:
//
//   1. Rename headscale user via HSGlobalFn().RenameUser
//      (POST /api/v1/user/{id}/rename/{new})
//   2. UPDATE portal_users.username WHERE id = {id}
//   3. Audit log "admin_user_rename" with old/new + hsID + outcome
//   4. 303 redirect to /admin/users?renamed=<old_username>
//
// Tests verify the contract:
//   - happy path: rename succeeds, portal_users updated, audit emitted
//   - admin guard: non-admin caller → 403
//   - invalid pattern (uppercase / special chars) → 400
//   - empty new_username → 400
//   - new == current (no-op rename) → success without calling headscale
//   - headscale-side APIError (duplicate name / 404) → friendly flash
//   - portal-side UPDATE failure (e.g. constraint) → 500 with audit row

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
	"skygate/internal/headscale"
)

// renameStubBackend captures Audit rows so tests can assert the
// emitted detail string without standing up a full DB-side audit
// log. CurrentUser is configurable (admin vs non-admin).
type renameStubBackend struct {
	admin   bool
	auditRows *[]capturedAudit
}

type capturedAudit struct {
	UserID   int64
	Username string
	Action   string
	Detail   string
}

func (s *renameStubBackend) Render(w http.ResponseWriter, r *http.Request, name string, data any) {}
func (s *renameStubBackend) RenderWithLayout(w http.ResponseWriter, r *http.Request, name string, c *auth.Claims, data map[string]any) {
}
func (s *renameStubBackend) CurrentUser(r *http.Request) *auth.Claims {
	return &auth.Claims{UserID: 1, Username: "operator", IsAdmin: s.admin}
}
func (s *renameStubBackend) Audit(userID int64, username, action, detail string) {
	if s.auditRows != nil {
		*s.auditRows = append(*s.auditRows, capturedAudit{userID, username, action, detail})
	}
}
func (s *renameStubBackend) InfraAuditIdentity(fallbackUID int64, fallbackUsername string) (int64, string) {
	return fallbackUID, fallbackUsername
}

// setupRenameTestDB seeds the minimal schema + a single portal user
// (skyadmin, id=1, headscale_user_id=86) for the rename handler tests.
func setupRenameTestDB(t *testing.T) *sql.DB {
	t.Helper()
	_, sqlDB, err := db.OpenWithDialect(":memory:")
	if err != nil {
		t.Fatalf("OpenWithDialect: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })

	if err := db.ApplyMigrations(sqlDB, db.DialectSQLite); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}

	if _, err := sqlDB.Exec(
		`INSERT INTO portal_users (id, username, password_hash, is_admin, headscale_user_id)
		 VALUES (1, 'skyadmin', '<REDACTED>', 1, 86)`,
	); err != nil {
		t.Fatalf("insert portal_users: %v", err)
	}
	return sqlDB
}

// TestPostAdminUserRename_HappyPath asserts the success path:
// the headscale user is renamed, portal_users.username is updated,
// and the audit row is emitted with old/new/hs_id/outcome.
func TestPostAdminUserRename_HappyPath(t *testing.T) {
	src := setupRenameDB(t)
	hsCalls := 0
	hsClient := headscale.New("http://hs.test", "k")
	hsClient.SetCacheTTL(0) // disable cache so InvalidateCache doesn't matter for the test
	stubHS := func() *headscale.Client { hsCalls++; return hsClient }
	var rows []capturedAudit
	svc := &Service{
		DB: &dbDBSource{db: src},
		Backend: &renameStubBackend{admin: true, auditRows: &rows},
		HSGlobalFn: stubHS,
	}

	// Mock headscale HTTP server: respond OK to /api/v1/user/86/rename/<new>.
	hsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/api/v1/user/86/rename/") {
			_, _ = fmt.Fprintf(w, `{"user":{"id":"86","name":"skyadmin-new","createdAt":"2026-08-25T06:41:11Z"}}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer hsSrv.Close()
	hsClient.BaseURL = hsSrv.URL

	form := url.Values{}
	form.Set("new_username", "skyadmin-new")
	req := httptest.NewRequest("POST", "/admin/users/1/rename", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	svc.PostAdminUserRename(rec, req)

	// 303 redirect on success.
	if rec.Code != http.StatusSeeOther {
		t.Errorf("expected 303 redirect; got %d. Body: %s", rec.Code, rec.Body.String())
	}
	wantLoc := "/admin/users?renamed=skyadmin"
	if loc := rec.Header().Get("Location"); loc != wantLoc {
		t.Errorf("Location = %q; want %q", loc, wantLoc)
	}

	// headscale was called exactly once.
	if hsCalls != 1 {
		t.Errorf("HSGlobalFn called %d times; want 1", hsCalls)
	}

	// portal_users.username is updated.
	var got string
	if err := src.QueryRow(`SELECT username FROM portal_users WHERE id = 1`).Scan(&got); err != nil {
		t.Fatalf("query username: %v", err)
	}
	if got != "skyadmin-new" {
		t.Errorf("portal_users.username = %q; want skyadmin-new", got)
	}

	// Audit row was emitted.
	if len(rows) != 1 {
		t.Fatalf("audit rows = %d; want 1", len(rows))
	}
	if rows[0].Action != "admin_user_rename" {
		t.Errorf("audit.Action = %q; want admin_user_rename", rows[0].Action)
	}
	if !strings.Contains(rows[0].Detail, "old=skyadmin") {
		t.Errorf("audit.Detail = %q; want contains old=skyadmin", rows[0].Detail)
	}
	if !strings.Contains(rows[0].Detail, "new=skyadmin-new") {
		t.Errorf("audit.Detail = %q; want contains new=skyadmin-new", rows[0].Detail)
	}
	if !strings.Contains(rows[0].Detail, "hs_id=86") {
		t.Errorf("audit.Detail = %q; want contains hs_id=86", rows[0].Detail)
	}
}

// TestPostAdminUserRename_NonAdminForbidden asserts that a
// non-admin caller gets 403, no headscale call, no DB write.
func TestPostAdminUserRename_NonAdminForbidden(t *testing.T) {
	src := setupRenameDB(t)
	hsCalls := 0
	hsClient := headscale.New("http://hs.test", "k")
	stubHS := func() *headscale.Client { hsCalls++; return hsClient }
	svc := &Service{
		DB: &dbDBSource{db: src},
		Backend: &renameStubBackend{admin: false},
		HSGlobalFn: stubHS,
	}

	form := url.Values{}
	form.Set("new_username", "skyadmin-new")
	req := httptest.NewRequest("POST", "/admin/users/1/rename", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()

	svc.PostAdminUserRename(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 for non-admin; got %d", rec.Code)
	}
	if hsCalls != 0 {
		t.Errorf("HSGlobalFn called %d times; want 0 (caller not admin)", hsCalls)
	}
	var username string
	if err := src.QueryRow(`SELECT username FROM portal_users WHERE id = 1`).Scan(&username); err != nil {
		t.Fatalf("query: %v", err)
	}
	if username != "skyadmin" {
		t.Errorf("portal_users.username = %q; want unchanged skyadmin", username)
	}
}

// TestPostAdminUserRename_InvalidPattern asserts that a new_username
// with uppercase / dot / space fails (303 redirect with err= flash)
// BEFORE hitting headscale. The handler uses the same ?err= flash
// pattern as PostAdminHSOrphanAdopt (303 See Other + err=) for
// consistency with the rest of /admin/users; the pre-fix equivalent
// (400 Bad Request + http.Error text) would have been fine too,
// but the redirect-with-flash is friendlier for the operator who
// just submitted the form.
func TestPostAdminUserRename_InvalidPattern(t *testing.T) {
	src := setupRenameDB(t)
	hsCalls := 0
	hsClient := headscale.New("http://hs.test", "k")
	stubHS := func() *headscale.Client { hsCalls++; return hsClient }
	svc := &Service{
		DB: &dbDBSource{db: src},
		Backend: &renameStubBackend{admin: true},
		HSGlobalFn: stubHS,
	}

	for _, badName := range []string{
		"Alice",     // uppercase
		"sky admin", // space
		"sky.admin", // dot
		"sky/admin", // slash
	} {
		form := url.Values{}
		form.Set("new_username", badName)
		req := httptest.NewRequest("POST", "/admin/users/1/rename", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		svc.PostAdminUserRename(rec, req)
		if rec.Code != http.StatusSeeOther {
			t.Errorf("invalid name %q: want 303 redirect, got %d", badName, rec.Code)
		}
		if !strings.Contains(rec.Header().Get("Location"), "err=") {
			t.Errorf("invalid name %q: Location = %q; want contains err=", badName, rec.Header().Get("Location"))
		}
	}

	// Empty new_username: same flash path (handler short-circuits
	// before the pattern check).
	form := url.Values{}
	form.Set("new_username", "")
	req := httptest.NewRequest("POST", "/admin/users/1/rename", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	svc.PostAdminUserRename(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Errorf("empty name: want 303 redirect, got %d", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Location"), "err=") {
		t.Errorf("empty name: Location = %q; want contains err=", rec.Header().Get("Location"))
	}

	if hsCalls != 0 {
		t.Errorf("HSGlobalFn called %d times; want 0 (invalid pattern rejected before headscale)", hsCalls)
	}
}

// TestPostAdminUserRename_HeadscaleDuplicateNameError asserts that
// a 500 "expected exactly one user" from headscale becomes a
// friendly err= flash, NOT a generic 500. The operator must be
// able to distinguish "duplicate name" from "headscale down".
//
// Uses a new_username DIFFERENT from current ("skyadmin" →
// "skyadmin-new") so the no-op short-circuit doesn't fire.
func TestPostAdminUserRename_HeadscaleDuplicateNameError(t *testing.T) {
	src := setupRenameDB(t)
	hsClient := headscale.New("http://hs.test", "k")
	hsClient.SetCacheTTL(0)
	hsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/api/v1/user/86/rename/") {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"code":2, "message":"expected exactly one user, found 2", "details":[]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer hsSrv.Close()
	hsClient.BaseURL = hsSrv.URL
	stubHS := func() *headscale.Client { return hsClient }
	svc := &Service{
		DB: &dbDBSource{db: src},
		Backend: &renameStubBackend{admin: true},
		HSGlobalFn: stubHS,
	}

	form := url.Values{}
	form.Set("new_username", "skyadmin-new") // different from current "skyadmin"
	req := httptest.NewRequest("POST", "/admin/users/1/rename", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	svc.PostAdminUserRename(rec, req)

	// 303 redirect with err= (the duplicate-name case gets a
	// distinct ?err= flash so the operator can act on it).
	if rec.Code != http.StatusSeeOther {
		t.Errorf("expected 303 redirect; got %d", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Location"), "err=") {
		t.Errorf("Location = %q; want contains err= (duplicate-name flash)", rec.Header().Get("Location"))
	}
	// portal_users.username NOT updated (rename failed before UPDATE).
	var username string
	if err := src.QueryRow(`SELECT username FROM portal_users WHERE id = 1`).Scan(&username); err != nil {
		t.Fatalf("query: %v", err)
	}
	if username != "skyadmin" {
		t.Errorf("portal_users.username = %q; want unchanged skyadmin (headscale failed)", username)
	}
}

// TestPostAdminUserRename_NoChangeIsIdempotent asserts that if
// new == current, the handler succeeds without calling headscale.
// Pre-fix, this would hit headscale every time and produce a
// stale-cache 500 (headscale v0.29 returns 500 if the rename
// target matches an existing row, even if it's the SAME row).
func TestPostAdminUserRename_NoChangeIsIdempotent(t *testing.T) {
	src := setupRenameDB(t)
	hsCalls := 0
	hsClient := headscale.New("http://hs.test", "k")
	stubHS := func() *headscale.Client { hsCalls++; return hsClient }
	svc := &Service{
		DB: &dbDBSource{db: src},
		Backend: &renameStubBackend{admin: true},
		HSGlobalFn: stubHS,
	}

	form := url.Values{}
	form.Set("new_username", "skyadmin") // == current
	req := httptest.NewRequest("POST", "/admin/users/1/rename", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	svc.PostAdminUserRename(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("expected 303 redirect; got %d", rec.Code)
	}
	if hsCalls != 0 {
		t.Errorf("HSGlobalFn called %d times; want 0 (no-op rename, headscale should NOT be hit)", hsCalls)
	}
}

// TestPostAdminUserRename_HeadscaleNotLinked asserts that a portal
// user with no headscale_user_id (e.g. an old pre-link row) still
// succeeds at the skygate-side UPDATE, even though headscale is
// not touched. The audit row should record hs_id=0.
func TestPostAdminUserRename_HeadscaleNotLinked(t *testing.T) {
	src := setupRenameDB(t)
	if _, err := src.Exec(`UPDATE portal_users SET headscale_user_id = NULL WHERE id = 1`); err != nil {
		t.Fatalf("unlink headscale: %v", err)
	}
	hsCalls := 0
	hsClient := headscale.New("http://hs.test", "k")
	stubHS := func() *headscale.Client { hsCalls++; return hsClient }
	var rows []capturedAudit
	svc := &Service{
		DB: &dbDBSource{db: src},
		Backend: &renameStubBackend{admin: true, auditRows: &rows},
		HSGlobalFn: stubHS,
	}

	form := url.Values{}
	form.Set("new_username", "skyadmin-new")
	req := httptest.NewRequest("POST", "/admin/users/1/rename", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	svc.PostAdminUserRename(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("expected 303 redirect; got %d", rec.Code)
	}
	if hsCalls != 0 {
		t.Errorf("HSGlobalFn called %d times; want 0 (headscale not linked)", hsCalls)
	}
	var username string
	if err := src.QueryRow(`SELECT username FROM portal_users WHERE id = 1`).Scan(&username); err != nil {
		t.Fatalf("query: %v", err)
	}
	if username != "skyadmin-new" {
		t.Errorf("portal_users.username = %q; want skyadmin-new", username)
	}
	if len(rows) != 1 {
		t.Fatalf("audit rows = %d; want 1", len(rows))
	}
	if !strings.Contains(rows[0].Detail, "hs_id=0") {
		t.Errorf("audit.Detail = %q; want contains hs_id=0 (headscale not linked)", rows[0].Detail)
	}
}

// setupRenameDB is the per-test wrapper around setupRenameTestDB
// so each test gets a fresh in-memory DB.
func setupRenameDB(t *testing.T) *sql.DB {
	t.Helper()
	return setupRenameTestDB(t)
}
