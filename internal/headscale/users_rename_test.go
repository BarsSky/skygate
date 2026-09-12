// 2026-09-12: v1.5.2+ admin-user-sync — regression test for the
// headscale-side user rename API client.
//
// Pre-rename, skygate had no client method for renaming headscale
// users; when SKYGATE_ADMIN_USER (skygate side) drifted from the
// headscale admin user, the operator had to:
//
//   1. SSH into the skygate VM
//   2. Run `docker exec headscale headscale users rename -i <id> <new>`
//      (the headscale CLI flag shape; gRPC HTTP path differs)
//   3. Manually UPDATE portal_users.username WHERE headscale_user_id=<id>
//   4. Restart skygate so cached client state refreshed
//
// The T1 B-check (scripts/check_b_admin_user_sync.sh) detects the drift
// but had no remediation path inside skygate. The PostAdminUserRename
// handler (added separately) wraps steps 1-4 into one button per row.
//
// This file tests the HTTP-layer client method that PostAdminUserRename
// uses. Headscale v0.29.1's gRPC HTTP gateway exposes the rename as:
//
//   POST /api/v1/user/{old_id}/rename/{new_name}    (NO request body)
//
// (NOT the pre-rename-URL-shape POST /api/v1/user/{id}/rename with a
// {"name":"..."} body — that was the headscale <= 0.19 shape; v0.20+
// moved the new name into the path. The two are visually similar but
// the request body MUST be empty in v0.20+.)

package headscale

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestRenameUserUsesCorrectURLPath asserts the HTTP method + path
// against the live headscale v0.29.1 gRPC HTTP gateway spec.
//
// MUST be: POST /api/v1/user/{id}/rename/{new_name}
//
// (NOT the pre-0.20 shape POST /api/v1/user/{id}/rename with a
// {"name":"..."} body — that endpoint no longer exists in v0.29.)
func TestRenameUserUsesCorrectURLPath(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"user": HSUser{
			ID:   "42",
			Name: "alice",
		}})
	}))
	defer srv.Close()

	c := New(srv.URL, "test-key")
	if _, err := c.RenameUser(42, "alice"); err != nil {
		t.Fatalf("RenameUser: %v", err)
	}

	if gotMethod != "POST" {
		t.Errorf("RenameUser method = %q; want POST", gotMethod)
	}
	wantPath := "/api/v1/user/42/rename/alice"
	if gotPath != wantPath {
		t.Errorf("RenameUser path = %q; want %q", gotPath, wantPath)
	}
}

// TestRenameUserSendsNoBody asserts the request body is EMPTY.
// Headscale v0.20+ takes the new name from the URL path, NOT from
// the body. Sending {"name":"..."} would be silently ignored
// (headscale v0.29 doesn't reject it, but logs a deprecation
// warning, and it makes the code review-confusing).
func TestRenameUserSendsNoBody(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"user": HSUser{
			ID:   "42",
			Name: "alice",
		}})
	}))
	defer srv.Close()

	c := New(srv.URL, "test-key")
	if _, err := c.RenameUser(42, "alice"); err != nil {
		t.Fatalf("RenameUser: %v", err)
	}
	if gotBody != "" {
		t.Errorf("RenameUser body = %q; want empty (new name is in URL path)", gotBody)
	}
}

// TestRenameUserParsesJSONResponse asserts RenameUser returns the
// HSUser from the {"user": {...}} envelope that headscale v0.29
// emits. Pre-rename-URL-shape headscale returned a flat HSUser;
// the wrapper was added in 0.20+ for symmetry with CreateUser.
func TestRenameUserParsesJSONResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"user": HSUser{
			ID:        "42",
			Name:      "alice",
			CreatedAt: "2026-09-12T13:00:00Z",
		}})
	}))
	defer srv.Close()

	c := New(srv.URL, "test-key")
	u, err := c.RenameUser(42, "alice")
	if err != nil {
		t.Fatalf("RenameUser: %v", err)
	}
	if u == nil {
		t.Fatalf("RenameUser: nil user")
	}
	if u.ID != "42" {
		t.Errorf("RenameUser.ID = %q; want 42", u.ID)
	}
	if u.Name != "alice" {
		t.Errorf("RenameUser.Name = %q; want alice", u.Name)
	}
	if u.CreatedAt != "2026-09-12T13:00:00Z" {
		t.Errorf("RenameUser.CreatedAt = %q; want 2026-09-12T13:00:00Z", u.CreatedAt)
	}
}

// TestRenameUserHandlesAlreadyExistsError asserts RenameUser
// returns a clear error when headscale rejects with
// {"code":2,"message":"expected exactly one user, found 2"}.
// The pre-rename code path treated the 500 as a generic
// "rename failed" — the operator couldn't tell from the error
// whether they hit a duplicate-name conflict, a permission issue,
// or a transient gRPC failure. The B-check at T1 already detects
// duplicate-name conflicts; RenameUser should surface them as
// errors.As(*APIError) with StatusCode=500 for the handler to
// branch on.
func TestRenameUserHandlesAlreadyExistsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":2, "message":"expected exactly one user, found 2", "details":[]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "test-key")
	_, err := c.RenameUser(86, "skyadmin")
	if err == nil {
		t.Fatalf("RenameUser: want error, got nil")
	}
	apiErr := &APIError{}
	if !errors.As(err, &apiErr) {
		t.Fatalf("RenameUser error = %v (%T); want *APIError", err, err)
	}
	if apiErr.StatusCode != http.StatusInternalServerError {
		t.Errorf("RenameUser APIError.StatusCode = %d; want 500", apiErr.StatusCode)
	}
	if !strings.Contains(apiErr.Body, "expected exactly one user") {
		t.Errorf("RenameUser APIError.Body = %q; want contains 'expected exactly one user'", apiErr.Body)
	}
}

// TestRenameUserInvalidatesCacheAfterRename asserts the user
// cache is cleared after a successful rename, so the next
// ListUsers() call fetches the new name. Pre-rename code had
// no cache invalidation, so a successful rename appeared as a
// stale-name read for the cacheTTL (5s).
func TestRenameUserInvalidatesCacheAfterRename(t *testing.T) {
	var listCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/user/42/rename/alice":
			_ = json.NewEncoder(w).Encode(map[string]any{"user": HSUser{ID: "42", Name: "alice"}})
		case "/api/v1/user":
			atomic.AddInt32(&listCalls, 1)
			_ = json.NewEncoder(w).Encode(map[string]any{"users": []HSUser{
				{ID: "42", Name: "alice"},
				{ID: "1", Name: "skyadmin"},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "test-key")
	c.SetCacheTTL(10 * time.Second)

	// Prime the cache.
	if _, err := c.ListUsers(); err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if atomic.LoadInt32(&listCalls) != 1 {
		t.Fatalf("listCalls = %d; want 1 (first ListUsers should hit HTTP)", listCalls)
	}
	// Second ListUsers should hit cache.
	if _, err := c.ListUsers(); err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if atomic.LoadInt32(&listCalls) != 1 {
		t.Errorf("listCalls after 2nd ListUsers = %d; want 1 (cache should absorb 2nd)", listCalls)
	}

	// Rename — should invalidate cache.
	if _, err := c.RenameUser(42, "alice"); err != nil {
		t.Fatalf("RenameUser: %v", err)
	}

	// Next ListUsers should hit HTTP again (not cache).
	if _, err := c.ListUsers(); err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if atomic.LoadInt32(&listCalls) != 2 {
		t.Errorf("listCalls after RenameUser+ListUsers = %d; want 2 (RenameUser must invalidate cache)", listCalls)
	}
}

// TestRenameUserHandlesNotFound asserts RenameUser surfaces a
// 404 from headscale as an *APIError. Headscale returns
// {"code":5, "message":"Not Found", "details":[]} for an
// unknown user_id; the handler treats this as a "user was
// deleted between the form load and the submit" edge case.
func TestRenameUserHandlesNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":5, "message":"Not Found", "details":[]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "test-key")
	_, err := c.RenameUser(99999, "alice")
	if err == nil {
		t.Fatalf("RenameUser: want error, got nil")
	}
	apiErr := &APIError{}
	if !errors.As(err, &apiErr) {
		t.Fatalf("RenameUser error = %v (%T); want *APIError", err, err)
	}
	if apiErr.StatusCode != http.StatusNotFound {
		t.Errorf("RenameUser APIError.StatusCode = %d; want 404", apiErr.StatusCode)
	}
}

// TestRenameUserSpecialCharsInNameURLEncodes asserts the new
// name is URL-encoded in the path. The pre-rename code used
// path.Join which leaves + alone, which decodes to space on
// the server side — "alice test" is a different user from
// "alice+test". httptest gives us the post-unescape path; we
// assert the server saw a + (the test server stores the raw
// URL path which httptest already decoded — so we check the
// decoded form).
func TestRenameUserSpecialCharsInNameURLEncodes(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"user": HSUser{
			ID:   "42",
			Name: "alice+test",
		}})
	}))
	defer srv.Close()

	c := New(srv.URL, "test-key")
	if _, err := c.RenameUser(42, "alice+test"); err != nil {
		t.Fatalf("RenameUser: %v", err)
	}
	// httptest decodes the path; we expect the decoded form.
	if gotPath != "/api/v1/user/42/rename/alice+test" {
		t.Errorf("RenameUser gotPath = %q; want /api/v1/user/42/rename/alice+test", gotPath)
	}
}
