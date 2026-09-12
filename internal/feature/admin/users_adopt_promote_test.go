// internal/feature/admin/users_adopt_promote_test.go — TDD tests
// for v1.5.2 admin-user-sync T5: promote_to_admin flag on
// PostAdminHSOrphanAdopt.
//
// Pre-T5, the orphan-adopt button on /admin/users HSOrphans
// list always created the row with is_admin=0. If the operator
// wanted to adopt an orphan that should be the skygate admin
// (e.g. after a fresh headscale install where the headscale
// admin user wasn't carried over to skygate), they had to:
//
//   1. Click "Adopt" (creates is_admin=0 row)
//   2. Run UPDATE portal_users SET is_admin=1 by hand
//
// T5 collapses this into a single POST with the promote_to_admin
// form field. The startup-detection banner (T6) uses this to
// offer "Adopt as Admin" instead of the regular "Adopt" button
// when SKYGATE_ADMIN_USER drift is detected.
//
// Tests verify the dispatch:
//   - promote_to_admin absent → is_admin=0 (B141 behaviour preserved)
//   - promote_to_admin=true   → is_admin=1
//   - promote_to_admin=on|1|yes|empty → is_admin=0 (defensive —
//     only an EXACT "true" promotes, so a future checkbox UI
//     doesn't accidentally promote users)
//   - duplicate-username → no-op (idempotency; same as B141)

package admin

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"skygate/internal/auth"
	"skygate/internal/headscale"
)

// adoptPromoteStubBackend is the variant of renameStubBackend used
// by the adopt-promote tests. The HS client is hard-coded to return
// one headscale user so the orphan-adopt path is exercised end-to-end.
type adoptPromoteStubBackend struct {
	admin    bool
	auditRows *[]capturedAudit
}

func (s *adoptPromoteStubBackend) Render(w http.ResponseWriter, r *http.Request, name string, data any) {}
func (s *adoptPromoteStubBackend) RenderWithLayout(w http.ResponseWriter, r *http.Request, name string, c *auth.Claims, data map[string]any) {
}
func (s *adoptPromoteStubBackend) CurrentUser(r *http.Request) *auth.Claims {
	return &auth.Claims{UserID: 1, Username: "operator", IsAdmin: s.admin}
}
func (s *adoptPromoteStubBackend) Audit(userID int64, username, action, detail string) {
	if s.auditRows != nil {
		*s.auditRows = append(*s.auditRows, capturedAudit{userID, username, action, detail})
	}
}
func (s *adoptPromoteStubBackend) InfraAuditIdentity(fallbackUID int64, fallbackUsername string) (int64, string) {
	return fallbackUID, fallbackUsername
}

func TestPostAdminHSOrphanAdopt_PromoteToAdmin_AbsentFlag(t *testing.T) {
	src := setupRenameDB(t)
	hsClient := headscale.New("http://hs.test", "k")
	hsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"users":[{"id":"42","name":"promoteable","createdAt":"2026-08-25T06:41:11Z"}]}`))
	}))
	defer hsSrv.Close()
	hsClient.BaseURL = hsSrv.URL
	stubHS := func() *headscale.Client { return hsClient }
	svc := &Service{
		DB: &dbDBSource{db: src},
		Backend: &adoptPromoteStubBackend{admin: true},
		HSGlobalFn: stubHS,
	}

	form := url.Values{}
	form.Set("hs_id", "42")
	form.Set("password", "test123")
	// promote_to_admin ABSENT → is_admin=0 (B141 behaviour).
	req := httptest.NewRequest("POST", "/admin/users/HSOrphan/adopt", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	svc.PostAdminHSOrphanAdopt(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("want 303 redirect; got %d", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Location"), "adopted=promoteable") {
		t.Errorf("Location = %q; want contains adopted=promoteable", rec.Header().Get("Location"))
	}
	var adminI int
	if err := src.QueryRow(`SELECT is_admin FROM portal_users WHERE username = 'promoteable'`).Scan(&adminI); err != nil {
		t.Fatalf("query: %v", err)
	}
	if adminI != 0 {
		t.Errorf("promote_to_admin absent: is_admin = %d, want 0 (B141 default)", adminI)
	}
}

func TestPostAdminHSOrphanAdopt_PromoteToAdmin_TrueFlag(t *testing.T) {
	src := setupRenameDB(t)
	hsClient := headscale.New("http://hs.test", "k")
	hsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"users":[{"id":"42","name":"promoteable","createdAt":"2026-08-25T06:41:11Z"}]}`))
	}))
	defer hsSrv.Close()
	hsClient.BaseURL = hsSrv.URL
	stubHS := func() *headscale.Client { return hsClient }
	var rows []capturedAudit
	svc := &Service{
		DB: &dbDBSource{db: src},
		Backend: &adoptPromoteStubBackend{admin: true, auditRows: &rows},
		HSGlobalFn: stubHS,
	}

	form := url.Values{}
	form.Set("hs_id", "42")
	form.Set("password", "test123")
	form.Set("promote_to_admin", "true") // T5: promote path
	req := httptest.NewRequest("POST", "/admin/users/HSOrphan/adopt", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	svc.PostAdminHSOrphanAdopt(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("want 303 redirect; got %d", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Location"), "adopted=promoteable") {
		t.Errorf("Location = %q; want contains adopted=promoteable", rec.Header().Get("Location"))
	}
	var adminI int
	if err := src.QueryRow(`SELECT is_admin FROM portal_users WHERE username = 'promoteable'`).Scan(&adminI); err != nil {
		t.Fatalf("query: %v", err)
	}
	if adminI != 1 {
		t.Errorf("promote_to_admin=true: is_admin = %d, want 1", adminI)
	}
	// Audit row must include promote_admin=true so the operator
	// can tell from the log which path was used.
	if len(rows) != 1 {
		t.Fatalf("audit rows = %d; want 1", len(rows))
	}
	if !strings.Contains(rows[0].Detail, "promote_admin=true") {
		t.Errorf("audit.Detail = %q; want contains promote_admin=true", rows[0].Detail)
	}
}

func TestPostAdminHSOrphanAdopt_PromoteToAdmin_DefensiveValues(t *testing.T) {
	// promote_to_admin set to ANYTHING other than "true" (on,
	// yes, 1, etc.) falls through to is_admin=0. Defensive —
	// a future checkbox-style UI sends "on" or similar and must
	// not accidentally promote users.
	usernames := []string{"u0", "u1", "u2", "u3", "u4", "u5"}
	for i, badFlag := range []string{"on", "yes", "1", "TRUE", " True ", ""} {
		// Per-iteration: fresh DB + fresh headscale stub that
		// returns the matching username (so the orphan list
		// matches what we'll insert).
		username := usernames[i]
		src := setupRenameDB(t)
		hsClient := headscale.New("http://hs.test", "k")
		hsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"users":[{"id":"42","name":"` + username + `","createdAt":"2026-08-25T06:41:11Z"}]}`))
		}))
		stubHS := func() *headscale.Client { return hsClient }
		svc := &Service{
			DB: &dbDBSource{db: src},
			Backend: &adoptPromoteStubBackend{admin: true},
			HSGlobalFn: stubHS,
		}
		hsClient.BaseURL = hsSrv.URL

		form := url.Values{}
		form.Set("hs_id", "42")
		form.Set("password", "test123")
		form.Set("promote_to_admin", badFlag)
		req := httptest.NewRequest("POST", "/admin/users/HSOrphan/adopt", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		svc.PostAdminHSOrphanAdopt(rec, req)

		if rec.Code != http.StatusSeeOther {
			hsSrv.Close()
			t.Errorf("flag=%q: want 303 redirect, got %d", badFlag, rec.Code)
			continue
		}
		var adminI int
		if err := src.QueryRow(`SELECT is_admin FROM portal_users WHERE username = ?`, username).Scan(&adminI); err != nil {
			hsSrv.Close()
			t.Fatalf("flag=%q: query %s: %v", badFlag, username, err)
		}
		if adminI != 0 {
			hsSrv.Close()
			t.Errorf("flag=%q: is_admin = %d, want 0 (only literal 'true' promotes)", badFlag, adminI)
		}
		hsSrv.Close()
	}
}
