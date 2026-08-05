package admin

// v0.33.1.8 — Telegram egress relay admin-UI selector tests.
//
// 3 tests pin the contract:
//   1. handleTelegramClearEgress clears the global_settings
//      row + writes an audit_log entry (idempotent: a second
//      clear is a no-op, not an error).
//   2. handleTelegramSetEgress without an enabled exit-server
//      row short-circuits with an "err=" flash (no SSH attempted,
//      no DB write).
//   3. loadTelegramUIState populates the Egress selector with
//      every enabled exit_servers row + the currently selected
//      hostname.
//
// Why no test for the SSH happy-path: the helper
// headscale.Client.SetAdvertisedRoutes shells out to `ssh`.
// Tests that exercise the real SSH path require a live relay
// (out of scope for the in-memory SQLite test rig). The
// contract we can pin in-process is "given a configured exit
// server, the handler accepts the form, SSHes (and we mock
// the failure), and writes the right audit + global_settings
// row". The "err=" branch (test #2) is what we can pin here.

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"skygate/internal/db"
)

// seedExitServer inserts an enabled exit_servers row for the
// egress tests. Mirrors the production schema (qInsertOrReplaceExitServer
// expects id PK + node_id UNIQUE + hostname + tailscale_ip +
// ssh_target + ssh_key_path + description + enabled +
// accept_routes). Returns the node_id so the test can refer
// to it in form data.
func seedExitServer(t *testing.T, d *sql.DB, nodeID, hostname, sshTarget, sshKey string) {
	t.Helper()
	_, err := d.Exec(
		`INSERT INTO exit_servers(node_id, hostname, tailscale_ip, ssh_target, ssh_key_path, description, enabled, accept_routes)
		 VALUES (?, ?, ?, ?, ?, ?, 1, 0)`,
		nodeID, hostname, "100.64.100.10", sshTarget, sshKey, "test relay",
	)
	if err != nil {
		t.Fatalf("seed exit_servers %s: %v", nodeID, err)
	}
}

// invokeEgressAction builds a POST /admin/telegram request
// with the given action + form, attaches the X-Test-User
// headers (admin) and the CSRF cookie + form field, runs
// s.AdminTelegramPost. Same shape as invokeSendTest but for
// the egress actions.
func invokeEgressAction(t *testing.T, s *Service, csrfCookie *http.Cookie, action string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	if form == nil {
		form = url.Values{}
	}
	form.Set("action", action)
	if csrfCookie != nil {
		form.Set("csrf", csrfCookie.Value)
	}
	req := httpRequestWithForm(t, "/admin/telegram", form, csrfCookie)
	w := httptest.NewRecorder()
	s.AdminTelegramPost(w, req)
	return w
}

func httpRequestWithForm(t *testing.T, path string, form url.Values, csrfCookie *http.Cookie) *http.Request {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if csrfCookie != nil {
		req.AddCookie(csrfCookie)
	}
	req.Header.Set("X-Test-User", "admin")
	req.Header.Set("X-Test-UserID", "1")
	req.Header.Set("X-Test-IsAdmin", "1")
	return req
}

// TestHandleTelegramClearEgress: clearing the egress selection
// (a) sets global_settings.telegram.egress_node_id to "" and
// (b) writes an audit row with action=telegram_egress_clear.
// A second clear is a no-op (handler returns ok either way).
func TestHandleTelegramClearEgress(t *testing.T) {
	s := newTestService(t)
	// Pre-seed a selection.
	if err := db.SetGlobalSetting(s.DB, "telegram.egress_node_id", "999"); err != nil {
		t.Fatalf("seed egress_node_id: %v", err)
	}
	csrfCookie, _ := issueTelegramCSRF(t)

	// First clear: must succeed and wipe the global_settings row.
	w := invokeEgressAction(t, s, csrfCookie, "clear_egress", nil)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body=%s", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "ok=") {
		t.Errorf("Location = %q, want ok= flash", loc)
	}
	v, err := db.GetGlobalSetting(s.DB, "telegram.egress_node_id", "")
	if err != nil {
		t.Fatalf("read egress_node_id: %v", err)
	}
	if v != "" {
		t.Errorf("egress_node_id = %q, want \"\" after Clear", v)
	}
	// Audit row recorded.
	var count int
	if err := s.DB.QueryRow(
		`SELECT COUNT(*) FROM audit_log WHERE action='telegram_egress_clear'`,
	).Scan(&count); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if count != 1 {
		t.Errorf("audit_log rows for telegram_egress_clear = %d, want 1", count)
	}

	// Second clear (idempotent): still 303, still no error.
	w2 := invokeEgressAction(t, s, csrfCookie, "clear_egress", nil)
	if w2.Code != http.StatusSeeOther {
		t.Fatalf("second clear: status = %d, want 303; body=%s", w2.Code, w2.Body.String())
	}
}

// TestHandleTelegramSetEgress_NodeNotFound: when admin picks
// a node_id that isn't in exit_servers (or is disabled), the
// handler must return an "err=" flash and NOT touch
// global_settings.telegram.egress_node_id. This is the
// "admin can't pick a node that isn't a real relay" guard.
func TestHandleTelegramSetEgress_NodeNotFound(t *testing.T) {
	s := newTestService(t)
	csrfCookie, _ := issueTelegramCSRF(t)

	// Form posts a node_id that doesn't exist in exit_servers.
	form := url.Values{"node_id": {"42"}}
	w := invokeEgressAction(t, s, csrfCookie, "set_egress", form)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body=%s", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "err=") {
		t.Errorf("Location = %q, want err= flash for unknown node_id", loc)
	}
	// global_settings row should NOT have been written.
	v, err := db.GetGlobalSetting(s.DB, "telegram.egress_node_id", "")
	if err != nil {
		t.Fatalf("read egress_node_id: %v", err)
	}
	if v != "" {
		t.Errorf("egress_node_id = %q, want \"\" after rejected Set", v)
	}
}

// TestHandleTelegramSetEgress_DisabledRow: a node_id that
// exists in exit_servers but is enabled=0 must also be
// rejected. The admin UI only lists enabled rows; this
// guards against a hand-crafted curl request.
func TestHandleTelegramSetEgress_DisabledRow(t *testing.T) {
	s := newTestService(t)
	csrfCookie, _ := issueTelegramCSRF(t)
	// Seed a DISABLED row.
	_, err := s.DB.Exec(
		`INSERT INTO exit_servers(node_id, hostname, enabled) VALUES (?, ?, 0)`,
		"77", "disabled-relay",
	)
	if err != nil {
		t.Fatalf("seed disabled exit_server: %v", err)
	}
	form := url.Values{"node_id": {"77"}}
	w := invokeEgressAction(t, s, csrfCookie, "set_egress", form)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body=%s", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "err=") {
		t.Errorf("Location = %q, want err= flash for disabled row", loc)
	}
}

// TestLoadTelegramUIState_Egress: loadTelegramUIState populates
// the Egress selector with every ENABLED exit_servers row +
// the currently selected hostname. The template renders the
// <select> from .State.Egress.Available, so a row missing
// from Available means the operator can't pick it.
func TestLoadTelegramUIState_Egress(t *testing.T) {
	s := newTestService(t)
	// Seed 3 rows: 2 enabled + 1 disabled.
	seedExitServer(t, s.DB, "1", "relay-1", "root@relay-1", "/ssh/k1")
	seedExitServer(t, s.DB, "2", "relay-2", "root@relay-2", "/ssh/k2")
	_, err := s.DB.Exec(
		`INSERT INTO exit_servers(node_id, hostname, enabled) VALUES (?, ?, 0)`,
		"3", "relay-3-off",
	)
	if err != nil {
		t.Fatalf("seed disabled: %v", err)
	}
	// Pretend the admin picked relay-2.
	if err := db.SetGlobalSetting(s.DB, "telegram.egress_node_id", "2"); err != nil {
		t.Fatalf("seed egress: %v", err)
	}
	state := s.loadTelegramUIState()
	if got, want := len(state.Egress.Available), 2; got != want {
		t.Errorf("Egress.Available count = %d, want %d (disabled rows must be filtered out)", got, want)
	}
	if state.Egress.SelectedNodeID != "2" {
		t.Errorf("SelectedNodeID = %q, want %q", state.Egress.SelectedNodeID, "2")
	}
	if state.Egress.SelectedHostname != "relay-2" {
		t.Errorf("SelectedHostname = %q, want %q", state.Egress.SelectedHostname, "relay-2")
	}
	// And the disabled row is not in the list.
	for _, e := range state.Egress.Available {
		if e.NodeID == "3" {
			t.Errorf("disabled row 3 leaked into Egress.Available")
		}
	}
}
