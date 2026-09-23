// B303 (v1.5.68) — the ownerless-device deadlock.
//
// Operator report (live, /admin/devices): pressing "Transfer" on a device that
// "turned out to belong to nobody" opened a separate raw text page
//
//	node not in node_owner_map: db: node_owner_map: no row
//
// and a device with no tag could not be tagged by the admin at all ("словно
// проигнорировано для тех скриптов что должны проверять и добавлять
// устройства").
//
// Root cause: node_owner_map had no row for the node.
//   * PostAdminDeviceTransfer required the row it exists to create.
//   * PostAdminNodeTag recorded ownership only when headscale named a user AND
//     the row already existed (the `tagged-devices` branch used an UPDATE), so
//     the tag landed in headscale while skygate stayed blind to the device.
//
// The tests below drive the REAL handlers against an in-memory SQLite DB and a
// fake headscale REST server, and assert the operator-visible contract:
//   * a missing row is treated as "ownerless" — the transfer/adoption succeeds
//   * the node_owner_map row (with hostname) is written
//   * every refusal is a 303 flash on /admin/devices, never a raw error page
package admin

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"skygate/internal/auth"
	"skygate/internal/db"
	"skygate/internal/headscale"
)

// b303TestDB opens an in-memory SQLite DB with all migrations applied and two
// portal users (admin → headscale user 1, daniil → headscale user 2). The
// node_owner_map table is deliberately EMPTY: that is the reported state.
func b303TestDB(t *testing.T) *dbDBSource {
	t.Helper()
	_, sqlDB, err := db.OpenWithDialect(":memory:")
	if err != nil {
		t.Fatalf("OpenWithDialect: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.ApplyMigrations(sqlDB, db.DialectSQLite); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	if _, err := sqlDB.Exec(
		`INSERT INTO portal_users (username, password_hash, is_admin, headscale_user_id)
		 VALUES ('admin', 'x', 1, 1), ('daniil', 'x', 0, 2)`); err != nil {
		t.Fatalf("insert portal_users: %v", err)
	}
	if _, err := sqlDB.Exec(`DELETE FROM node_owner_map`); err != nil {
		t.Fatalf("clear node_owner_map: %v", err)
	}
	return &dbDBSource{db: sqlDB}
}

// b303FakeHeadscale serves the two REST endpoints the handlers touch:
// GET /api/v1/node (the live node list) and POST /api/v1/node/{id}/tags.
// `nodesJSON` is the raw body of the list endpoint, so each test can model the
// exact headscale answer it needs (empty user, tagged-devices, failure).
func b303FakeHeadscale(t *testing.T, nodesJSON string, listStatus int) (*headscale.Client, *[]string) {
	t.Helper()
	var tagCalls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/node":
			if listStatus != 0 && listStatus != http.StatusOK {
				w.WriteHeader(listStatus)
				_, _ = w.Write([]byte(`{"message":"boom"}`))
				return
			}
			_, _ = w.Write([]byte(nodesJSON))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/tags"):
			tagCalls = append(tagCalls, r.URL.Path)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	// No docker / no headscale binary: a CLI fallback would fail, so a
	// successful tag proves the REST path answered.
	t.Setenv("PATH", t.TempDir())
	c := headscale.New(srv.URL, "test-key")
	c.SetCacheTTL(0)
	return c, &tagCalls
}

func b303Service(src *dbDBSource, hs *headscale.Client) *Service {
	return &Service{
		DB:         src,
		Backend:    &claimStubBackend{admin: &auth.Claims{UserID: 1, Username: "admin", IsAdmin: true}},
		HSGlobalFn: func() *headscale.Client { return hs },
	}
}

func b303PostForm(t *testing.T, h http.HandlerFunc, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

// TestPostAdminDeviceTransfer_OwnerlessNodeIsAdopted_B303 is the operator's
// screenshot: node 42 has NO node_owner_map row, and Transfer must adopt it
// instead of answering the raw "node not in node_owner_map" page.
func TestPostAdminDeviceTransfer_OwnerlessNodeIsAdopted_B303(t *testing.T) {
	src := b303TestDB(t)
	hs, _ := b303FakeHeadscale(t, `{"nodes":[{"id":"42","givenName":"cyborg","name":"42","user":{"id":"2","name":"daniil"},"tags":[]}]}`, 0)
	svc := b303Service(src, hs)

	rec := b303PostForm(t, svc.PostAdminDeviceTransfer, "/admin/devices/transfer",
		url.Values{"node_id": {"42"}, "target_username": {"daniil"}})

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (body=%q)", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if strings.Contains(loc, "err=") {
		t.Fatalf("Location = %q; want a success flash, not an error", loc)
	}
	if !strings.Contains(loc, "ok=") {
		t.Fatalf("Location = %q; want ?ok=", loc)
	}
	if msg := decodedQuery(t, loc, "ok"); !strings.Contains(msg, "42") || !strings.Contains(msg, "daniil") {
		t.Errorf("success flash = %q, want it to name node 42 and daniil", msg)
	}

	var username, tag, hostname string
	if err := src.db.QueryRow(
		`SELECT COALESCE(username,''), COALESCE(tag,''), COALESCE(hostname,'')
		   FROM node_owner_map WHERE node_id = '42'`).Scan(&username, &tag, &hostname); err != nil {
		t.Fatalf("node_owner_map row was not created by the transfer: %v", err)
	}
	if username != "daniil" || tag != "tag:dev-daniil-cyborg" {
		t.Errorf("row = (%q, %q), want (daniil, tag:dev-daniil-cyborg)", username, tag)
	}
	if hostname != "cyborg" {
		t.Errorf("hostname = %q, want cyborg (B272.5 stamp on the new row)", hostname)
	}
}

// TestPostAdminDeviceTransfer_MissingRowIsNotRawError_B303 pins the LITERAL
// regression: whatever happens, the response must never be the text/plain page
// from the screenshot.
func TestPostAdminDeviceTransfer_MissingRowIsNotRawError_B303(t *testing.T) {
	src := b303TestDB(t)
	hs, _ := b303FakeHeadscale(t, `{"nodes":[]}`, 0)
	svc := b303Service(src, hs)

	cases := []struct {
		name string
		form url.Values
	}{
		{"ownerless-but-node-gone", url.Values{"node_id": {"42"}, "target_username": {"daniil"}}},
		{"unknown-target", url.Values{"node_id": {"42"}, "target_username": {"ghost"}}},
		{"no-target", url.Values{"node_id": {"42"}}},
		{"bad-node-id", url.Values{"node_id": {"abc"}, "target_username": {"daniil"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := b303PostForm(t, svc.PostAdminDeviceTransfer, "/admin/devices/transfer", tc.form)
			if rec.Code != http.StatusSeeOther {
				t.Fatalf("status = %d, want 303 with a flash (body=%q)", rec.Code, rec.Body.String())
			}
			loc := rec.Header().Get("Location")
			if !strings.HasPrefix(loc, "/admin/devices?err=") {
				t.Fatalf("Location = %q, want /admin/devices?err=...", loc)
			}
			if strings.Contains(rec.Body.String(), "node not in node_owner_map") {
				t.Errorf("raw db error text leaked into the response body: %q", rec.Body.String())
			}
			if strings.Contains(decodedQuery(t, loc, "err"), "node not in node_owner_map") {
				t.Errorf("the raw db error must not be the operator-facing message: %q", loc)
			}
		})
	}
}

// TestPostAdminDeviceTransfer_HeadscaleDownNamesIt_B303: an ownerless node
// cannot be adopted with a stale/absent hostname, so the flash must name the
// unreachable headscale instead of building "tag:dev-daniil-".
func TestPostAdminDeviceTransfer_HeadscaleDownNamesIt_B303(t *testing.T) {
	src := b303TestDB(t)
	hs, _ := b303FakeHeadscale(t, "", http.StatusInternalServerError)
	svc := b303Service(src, hs)

	rec := b303PostForm(t, svc.PostAdminDeviceTransfer, "/admin/devices/transfer",
		url.Values{"node_id": {"42"}, "target_username": {"daniil"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (body=%q)", rec.Code, rec.Body.String())
	}
	msg := decodedQuery(t, rec.Header().Get("Location"), "err")
	if !strings.Contains(msg, "headscale is unreachable") {
		t.Errorf("err flash = %q, want it to name the unreachable headscale", msg)
	}
	var n int
	if err := src.db.QueryRow(`SELECT COUNT(*) FROM node_owner_map`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("node_owner_map rows = %d, want 0 (no half-adoption)", n)
	}
}

// TestPostAdminNodeTag_RecordsOwnershipWithoutARow_B303 is the second half of
// the operator's report. Two shapes are covered:
//   - headscale reports no user at all (empty user)
//   - the node sits under the synthetic `tagged-devices` user, for which the
//     pre-B303 code ran an UPDATE that matched nothing
//
// In both cases the tag must land in headscale AND a node_owner_map row must
// exist afterwards (with the live hostname), otherwise every per-device ACL
// rule misses the device and Transfer 400s.
func TestPostAdminNodeTag_RecordsOwnershipWithoutARow_B303(t *testing.T) {
	cases := []struct {
		name         string
		nodesJSON    string
		wantUsername string
	}{
		{
			name:         "no-user-at-all",
			nodesJSON:    `{"nodes":[{"id":"42","givenName":"cyborg","user":{"id":"0","name":""},"tags":[]}]}`,
			wantUsername: "",
		},
		{
			name:         "synthetic-tagged-devices",
			nodesJSON:    `{"nodes":[{"id":"42","givenName":"cyborg","user":{"id":"2147455555","name":"tagged-devices"},"tags":[]}]}`,
			wantUsername: "tagged-devices",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := b303TestDB(t)
			hs, tagCalls := b303FakeHeadscale(t, tc.nodesJSON, 0)
			svc := b303Service(src, hs)

			rec := b303PostForm(t, svc.PostAdminNodeTag, "/admin/nodes/42/tag",
				url.Values{"tag": {"tag:public"}})
			if rec.Code != http.StatusFound {
				t.Fatalf("status = %d, want 302 back to /admin/devices (body=%q)", rec.Code, rec.Body.String())
			}
			if len(*tagCalls) == 0 {
				t.Fatalf("headscale was never asked to apply the tag (REST tag calls=%v)", *tagCalls)
			}

			var username, tag, hostname string
			if err := src.db.QueryRow(
				`SELECT COALESCE(username,''), COALESCE(tag,''), COALESCE(hostname,'')
				   FROM node_owner_map WHERE node_id = '42'`).Scan(&username, &tag, &hostname); err != nil {
				t.Fatalf("tag applied without a node_owner_map row (the pre-B303 silent ignore): %v", err)
			}
			if tag != "tag:public" {
				t.Errorf("tag = %q, want tag:public", tag)
			}
			if username != tc.wantUsername {
				t.Errorf("username = %q, want %q (the live headscale user name)", username, tc.wantUsername)
			}
			if hostname != "cyborg" {
				t.Errorf("hostname = %q, want cyborg", hostname)
			}
		})
	}
}

// TestPostAdminNodeTag_HeadscaleUnreadableIsAFailureNotASilentTag_B303: the
// tag guard needs the node's current tags (to refuse an exit-node tag on a
// per-user device), so an unreadable headscale must stop the write and say so.
func TestPostAdminNodeTag_HeadscaleUnreadableIsAFailureNotASilentTag_B303(t *testing.T) {
	src := b303TestDB(t)
	hs, tagCalls := b303FakeHeadscale(t, "", http.StatusInternalServerError)
	svc := b303Service(src, hs)

	rec := b303PostForm(t, svc.PostAdminNodeTag, "/admin/nodes/42/tag", url.Values{"tag": {"tag:public"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 flash (body=%q)", rec.Code, rec.Body.String())
	}
	if msg := decodedQuery(t, rec.Header().Get("Location"), "err"); !strings.Contains(msg, "cannot read nodes from headscale") {
		t.Errorf("err flash = %q, want it to name the failed headscale read", msg)
	}
	if len(*tagCalls) != 0 {
		t.Errorf("headscale was asked to tag a node whose current tags we could not read: %v", *tagCalls)
	}
}

// TestPostAdminNodeTag_UnknownNodeIsAFlash_B303: a node that vanished from
// headscale must produce a flash, not a 500 from TagNode.
func TestPostAdminNodeTag_UnknownNodeIsAFlash_B303(t *testing.T) {
	src := b303TestDB(t)
	hs, tagCalls := b303FakeHeadscale(t, `{"nodes":[]}`, 0)
	svc := b303Service(src, hs)

	rec := b303PostForm(t, svc.PostAdminNodeTag, "/admin/nodes/42/tag", url.Values{"tag": {"tag:public"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 flash (body=%q)", rec.Code, rec.Body.String())
	}
	if msg := decodedQuery(t, rec.Header().Get("Location"), "err"); !strings.Contains(msg, "not in headscale") {
		t.Errorf("err flash = %q, want 'not in headscale'", msg)
	}
	if len(*tagCalls) != 0 {
		t.Errorf("tag was applied to a non-existent node: %v", *tagCalls)
	}
	var n int
	if err := src.db.QueryRow(`SELECT COUNT(*) FROM node_owner_map`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("node_owner_map rows = %d, want 0", n)
	}
}

// decodedQuery returns the decoded value of `key` in a Location header.
func decodedQuery(t *testing.T, location, key string) string {
	t.Helper()
	u, err := url.Parse(location)
	if err != nil {
		t.Fatalf("parse Location %q: %v", location, err)
	}
	return u.Query().Get(key)
}
