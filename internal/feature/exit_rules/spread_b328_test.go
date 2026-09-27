// spread_b328_test.go — B328, the «распространить на все мои устройства» action.
//
// The operator's scenario in miniature: a user has rules on ONE device and registers a
// second one later. Before this action the only path was to re-type every rule with the
// «все мои устройства» option selected — live, 33 rules on `skyworker` and not one of
// them on `cyborg`. The action must copy the named rule to the other device, record the
// intent so a device registered LATER inherits it, and refuse to invent rules.
package exit_rules

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"skygate/internal/auth"
	skygatedb "skygate/internal/db"
	"skygate/internal/headscale"
)

// b328SpreadFixture: one user, two devices, one rule on the FIRST device only.
func b328SpreadFixture(t *testing.T) (*Service, func(), *httptest.Server) {
	t.Helper()
	d := newB328DB(t)
	b328Exec(t, d, `INSERT INTO portal_users (id, username, password_hash, is_admin, headscale_user_id)
	                VALUES (1, 'skyadmin', 'x', 1, 1)`)
	b328Exec(t, d, `INSERT INTO node_owner_map (node_id, headscale_user_id, username, tag, tagged_by_user_id, tagged_at, hostname)
	                VALUES ('9', 1, 'skyadmin', 'tag:dev-skyadmin-skyworker', 1, 0, 'skyworker')`)
	b328Exec(t, d, `INSERT INTO node_owner_map (node_id, headscale_user_id, username, tag, tagged_by_user_id, tagged_at, hostname)
	                VALUES ('56', 1, 'skyadmin', 'tag:dev-skyadmin-cyborg', 1, 0, 'cyborg')`)
	// The rule the operator already had — on device 9 ONLY.
	b328Exec(t, d, `INSERT INTO device_rules (user_id, device_id, user_name, exit_node_id, target_type, target_value, action, enabled, all_devices)
	                VALUES (1, 9, 'skyadmin', 'karolina', 'ip', '104.16.0.0/12', 'accept', 1, 0)`)

	// The node stub must know both devices so the copy can carry the right device IP.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"nodes":[` +
			`{"id":"9","name":"skyworker","givenName":"skyworker","ipAddresses":["100.64.0.9"],"online":true,"user":{"id":"1","name":"skyadmin"}},` +
			`{"id":"56","name":"cyborg","givenName":"cyborg","ipAddresses":["100.64.0.20"],"online":true,"user":{"id":"1","name":"skyadmin"}}]}`))
	}))
	t.Cleanup(srv.Close)

	s := &Service{
		Backend: &b328Backend{claims: &auth.Claims{UserID: 1, Username: "skyadmin", IsAdmin: true}},
		DB:      skygatedb.FixedDBSource{DB: d},
		HS:      headscale.New(srv.URL, "test-token"),
	}
	return s, func() {}, srv
}

func countRuleRows(t *testing.T, s *Service, deviceID int, value string) int {
	t.Helper()
	var n int
	if err := s.dbc().QueryRow(`SELECT COUNT(*) FROM device_rules
	                            WHERE user_id = 1 AND device_id = ? AND target_value = ? AND enabled = 1`,
		deviceID, value).Scan(&n); err != nil {
		t.Fatalf("count rows for device %d: %v", deviceID, err)
	}
	return n
}

// TestB328_SpreadCopiesAnExistingRuleToTheOtherDevice is the operator's request: the 33
// rules on one device must be able to reach the second device without re-entry.
func TestB328_SpreadCopiesAnExistingRuleToTheOtherDevice(t *testing.T) {
	s, _, _ := b328SpreadFixture(t)
	const value = "104.16.0.0/12"

	if got := countRuleRows(t, s, 56, value); got != 0 {
		t.Fatalf("precondition: device 56 must start with no such rule, got %d", got)
	}

	form := url.Values{"exit_node": {"karolina"}, "target_type": {"ip"}, "target_value": {value}}
	req := httptest.NewRequest(http.MethodPost, "/my/exit-rules/spread", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.PostMyExitRuleSpread(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("spread answered %d, want 302; body: %s", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if strings.Contains(loc, "err=") {
		t.Fatalf("spread refused: %s", loc)
	}
	if !strings.Contains(loc, "spread=ok") {
		t.Fatalf("spread did not report success: %s", loc)
	}
	if got := countRuleRows(t, s, 56, value); got != 1 {
		t.Errorf("device 56 has %d copies of the rule, want 1 — the button did not spread it", got)
	}
	if got := countRuleRows(t, s, 9, value); got != 1 {
		t.Errorf("the source rule on device 9 was disturbed (now %d rows, want 1)", got)
	}

	// The intent must be recorded, so a device registered NEXT WEEK inherits it.
	var marked int
	if err := s.dbc().QueryRow(`SELECT COUNT(*) FROM device_rules
	                            WHERE user_id = 1 AND target_value = ? AND all_devices = 1`, value).Scan(&marked); err != nil {
		t.Fatalf("read all_devices marker: %v", err)
	}
	if marked != 2 {
		t.Errorf("rows carrying the all_devices marker = %d, want 2 (both devices) — without the marker the periodic pass cannot cover a device registered later", marked)
	}
}

// TestB328_SpreadRefusesToInventARule: the action must only ever widen a rule that
// exists. A hand-crafted POST for an unknown key changes nothing.
func TestB328_SpreadRefusesToInventARule(t *testing.T) {
	s, _, _ := b328SpreadFixture(t)

	form := url.Values{"exit_node": {"emilia"}, "target_type": {"ip"}, "target_value": {"198.51.100.0/24"}}
	req := httptest.NewRequest(http.MethodPost, "/my/exit-rules/spread", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.PostMyExitRuleSpread(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("spread answered %d, want 302", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "err=") {
		t.Fatalf("spread of a non-existent rule was accepted: %s", loc)
	}
	var n int
	if err := s.dbc().QueryRow(`SELECT COUNT(*) FROM device_rules WHERE target_value = '198.51.100.0/24'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("the action invented %d row(s) for a rule that does not exist", n)
	}
}

// TestB328_SpreadIsIdempotent: pressing the button twice must not duplicate rows, and the
// second press is reported as "already" rather than silently doing nothing.
func TestB328_SpreadIsIdempotent(t *testing.T) {
	s, _, _ := b328SpreadFixture(t)
	const value = "104.16.0.0/12"
	form := url.Values{"exit_node": {"karolina"}, "target_type": {"ip"}, "target_value": {value}}

	post := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/my/exit-rules/spread", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		s.PostMyExitRuleSpread(rec, req)
		return rec
	}

	if loc := post().Header().Get("Location"); !strings.Contains(loc, "spread=ok") {
		t.Fatalf("first press: %s", loc)
	}
	if loc := post().Header().Get("Location"); !strings.Contains(loc, "spread=already") {
		t.Fatalf("second press: want spread=already, got %s", loc)
	}
	if got := countRuleRows(t, s, 56, value); got != 1 {
		t.Errorf("device 56 has %d rows after two presses, want 1", got)
	}
	if got := countRuleRows(t, s, 9, value); got != 1 {
		t.Errorf("device 9 has %d rows after two presses, want 1", got)
	}
}
