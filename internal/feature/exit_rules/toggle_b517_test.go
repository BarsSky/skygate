// toggle_b517_test.go — B517: switching a rule (or a group) on and off.
//
// The operator's words were «сейчас можно только удалить или добавить», so the
// properties below are the ones that make the new verb SAFE rather than merely
// present:
//
//  1. A DOMAIN and everything resolved from it switch together — a domain row
//     that says «выключена» while its /32s still route is the lie this block
//     exists to prevent.
//  2. An «all my devices» rule switches in ALL its copies, because the periodic
//     fan-out pass widens a group and would otherwise resurrect an enabled copy on
//     the next device.
//  3. Switching a GROUP acts on exactly the rows the page rendered — the group is
//     rebuilt from the same model, so "this card" can never mean another set.
//  4. Another user's rule is unreachable through the form (the SQL is owner-scoped
//     AND the ids are intersected with the caller's rows).
//  5. The page returns to where the operator was (validated), and the outcome is
//     reported in the query string, not in a resubmittable POST.
package exit_rules

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"skygate/internal/auth"
	skygatedb "skygate/internal/db"
)

// b517Fixture: two portal users, a migratd SQLite DB and a Service wired to it.
// HS is deliberately nil: the toggle's ACL tail then reports "no headscale client"
// and the test asserts the RULE STATE, which is what the handler owns.
func b517Fixture(t *testing.T) (*Service, *sql.DB) {
	t.Helper()
	d := newB328DB(t)
	b328Exec(t, d, `INSERT INTO portal_users (id, username, password_hash, is_admin, headscale_user_id)
	                VALUES (1, 'skyadmin', 'x', 1, 1)`)
	b328Exec(t, d, `INSERT INTO portal_users (id, username, password_hash, is_admin, headscale_user_id)
	                VALUES (2, 'michail', 'x', 0, 2)`)
	s := &Service{
		Backend: &b328Backend{claims: &auth.Claims{UserID: 1, Username: "skyadmin", IsAdmin: true}},
		DB:      skygatedb.FixedDBSource{DB: d},
	}
	return s, d
}

// b517InsertRule seeds one device_rules row and returns its id.
func b517InsertRule(t *testing.T, d *sql.DB, userID, deviceID int, userName, exitNode, typ, value, parentDomain string, enabled, allDevices int) int {
	t.Helper()
	res, err := d.Exec(`INSERT INTO device_rules
	    (user_id, device_id, user_name, device_hostname, exit_node_id, target_type, target_value, action, enabled, parent_domain, all_devices)
	    VALUES (?, ?, ?, '', ?, ?, ?, 'accept', ?, ?, ?)`,
		userID, deviceID, userName, exitNode, typ, value, enabled, parentDomain, allDevices)
	if err != nil {
		t.Fatalf("seed rule %s %s: %v", typ, value, err)
	}
	id, _ := res.LastInsertId()
	return int(id)
}

func b517Enabled(t *testing.T, d *sql.DB, id int) int {
	t.Helper()
	var v int
	if err := d.QueryRow(`SELECT enabled FROM device_rules WHERE id = ?`, id).Scan(&v); err != nil {
		t.Fatalf("read enabled of rule %d: %v", id, err)
	}
	return v
}

// b517Post runs the handler with the given form and returns the redirect target.
func b517Post(t *testing.T, s *Service, form url.Values) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/my/exit-rules/toggle", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.PostMyExitRuleToggle(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("toggle answered %d, want 303; body: %s", rec.Code, rec.Body.String())
	}
	return rec.Header().Get("Location")
}

// TestB517_DomainCascadeSwitchesTheResolvedRows is property 1.
func TestB517_DomainCascadeSwitchesTheResolvedRows(t *testing.T) {
	s, d := b517Fixture(t)
	domainID := b517InsertRule(t, d, 1, 9, "skyadmin", "karolina", "domain", "example.com", "", 1, 0)
	plain32 := b517InsertRule(t, d, 1, 9, "skyadmin", "karolina", "subnet", "192.0.2.10/32", "example.com", 1, 0)
	cdnRange := b517InsertRule(t, d, 1, 9, "skyadmin", "karolina", "subnet", "104.16.0.0/12", "cdn:cloudflare:example.com", 1, 0)
	unrelated := b517InsertRule(t, d, 1, 9, "skyadmin", "karolina", "ip", "198.51.100.7", "", 1, 0)

	loc := b517Post(t, s, url.Values{
		"scope": {"rule"}, "id": {itoaB517(domainID)}, "enabled": {"0"},
		"back": {"/my/exit-rules?lens=source&group=dom:example.com"},
	})
	if !strings.Contains(loc, "toggled=off") || !strings.Contains(loc, "n=3") {
		t.Errorf("redirect = %q, want toggled=off&n=3 (the domain + its /32 + its CDN range)", loc)
	}
	for _, id := range []int{domainID, plain32, cdnRange} {
		if got := b517Enabled(t, d, id); got != 0 {
			t.Errorf("rule %d enabled = %d, want 0 — the domain and everything resolved from it switch together", id, got)
		}
	}
	if got := b517Enabled(t, d, unrelated); got != 1 {
		t.Errorf("unrelated rule enabled = %d, want 1 — the cascade must not reach a manual rule", got)
	}
	// The page the operator was on comes back, not the page root.
	if !strings.Contains(loc, "group=dom%3Aexample.com") {
		t.Errorf("redirect = %q, want the group page preserved (the operator must land where they were)", loc)
	}
}

// TestB517_FanOutCopiesSwitchTogether is property 2.
func TestB517_FanOutCopiesSwitchTogether(t *testing.T) {
	s, d := b517Fixture(t)
	copyA := b517InsertRule(t, d, 1, 9, "skyadmin", "karolina", "subnet", "104.16.0.0/12", "example.com", 1, 1)
	copyB := b517InsertRule(t, d, 1, 56, "skyadmin", "karolina", "subnet", "104.16.0.0/12", "example.com", 1, 1)
	otherRelay := b517InsertRule(t, d, 1, 9, "skyadmin", "emilia", "subnet", "104.16.0.0/12", "example.com", 1, 1)
	otherValue := b517InsertRule(t, d, 1, 9, "skyadmin", "karolina", "subnet", "104.17.0.0/12", "example.com", 1, 1)

	loc := b517Post(t, s, url.Values{"scope": {"rule"}, "id": {itoaB517(copyA)}, "enabled": {"0"}, "back": {"/my/exit-rules"}})
	if !strings.Contains(loc, "n=2") {
		t.Errorf("redirect = %q, want n=2 (both copies of the same logical rule)", loc)
	}
	for _, id := range []int{copyA, copyB} {
		if got := b517Enabled(t, d, id); got != 0 {
			t.Errorf("fan-out copy %d enabled = %d, want 0", id, got)
		}
	}
	for _, id := range []int{otherRelay, otherValue} {
		if got := b517Enabled(t, d, id); got != 1 {
			t.Errorf("rule %d enabled = %d, want 1 — only the same logical rule switches", id, got)
		}
	}
}

// TestB517_GroupScopeActsOnTheRenderedCard is property 3.
func TestB517_GroupScopeActsOnTheRenderedCard(t *testing.T) {
	s, d := b517Fixture(t)
	a1 := b517InsertRule(t, d, 1, 9, "skyadmin", "karolina", "domain", "a.example", "", 1, 0)
	a2 := b517InsertRule(t, d, 1, 9, "skyadmin", "karolina", "subnet", "192.0.2.1/32", "a.example", 1, 0)
	b1 := b517InsertRule(t, d, 1, 9, "skyadmin", "karolina", "domain", "b.example", "", 1, 0)
	b2 := b517InsertRule(t, d, 1, 9, "skyadmin", "karolina", "subnet", "192.0.2.2/32", "b.example", 1, 0)

	loc := b517Post(t, s, url.Values{
		"scope": {"group"}, "lens": {"source"}, "group": {"dom:a.example"},
		"enabled": {"0"}, "back": {"/my/exit-rules?lens=source"},
	})
	if strings.Contains(loc, "toggle_err=") {
		t.Fatalf("group toggle refused: %s", loc)
	}
	if got := b517Enabled(t, d, a1); got != 0 {
		t.Error("the card's domain row did not switch")
	}
	if got := b517Enabled(t, d, a2); got != 0 {
		t.Error("the card's resolved row did not switch")
	}
	for _, id := range []int{b1, b2} {
		if got := b517Enabled(t, d, id); got != 1 {
			t.Errorf("another card's rule %d switched too (enabled=%d)", id, got)
		}
	}
}

// TestB517_ForeignRuleIsUnreachable is property 4.
func TestB517_ForeignRuleIsUnreachable(t *testing.T) {
	s, d := b517Fixture(t)
	foreign := b517InsertRule(t, d, 2, 29, "michail", "karolina", "domain", "other.example", "", 1, 0)

	loc := b517Post(t, s, url.Values{"scope": {"rule"}, "id": {itoaB517(foreign)}, "enabled": {"0"}, "back": {"/my/exit-rules"}})
	if !strings.Contains(loc, "toggle_err=") {
		t.Errorf("redirect = %q, want a refusal (another user's rule must be unreachable)", loc)
	}
	if got := b517Enabled(t, d, foreign); got != 1 {
		t.Errorf("foreign rule enabled = %d, want 1 — the owner scope is the security boundary", got)
	}

	loc = b517Post(t, s, url.Values{"scope": {"ids"}, "ids": {itoaB517(foreign)}, "enabled": {"0"}, "back": {"/my/exit-rules"}})
	if !strings.Contains(loc, "toggle_err=") {
		t.Errorf("ids[] redirect = %q, want a refusal", loc)
	}
	if got := b517Enabled(t, d, foreign); got != 1 {
		t.Errorf("foreign rule enabled = %d after an ids[] attempt, want 1", got)
	}
}

// TestB517_ReEnableBringsTheRuleBack — the pair of verbs the operator asked for.
func TestB517_ReEnableBringsTheRuleBack(t *testing.T) {
	s, d := b517Fixture(t)
	id := b517InsertRule(t, d, 1, 9, "skyadmin", "karolina", "ip", "198.51.100.9", "", 0, 0)

	loc := b517Post(t, s, url.Values{"scope": {"rule"}, "id": {itoaB517(id)}, "enabled": {"1"}, "back": {"/my/exit-rules"}})
	if !strings.Contains(loc, "toggled=on") {
		t.Errorf("redirect = %q, want toggled=on", loc)
	}
	if got := b517Enabled(t, d, id); got != 1 {
		t.Errorf("rule enabled = %d, want 1 after switching it back on", got)
	}
}

// TestB517_UnknownGroupIsRefused — a stale card link must not silently do nothing.
func TestB517_UnknownGroupIsRefused(t *testing.T) {
	s, d := b517Fixture(t)
	id := b517InsertRule(t, d, 1, 9, "skyadmin", "karolina", "domain", "a.example", "", 1, 0)

	loc := b517Post(t, s, url.Values{
		"scope": {"group"}, "lens": {"source"}, "group": {"dom:gone.example"},
		"enabled": {"0"}, "back": {"/my/exit-rules?lens=source"},
	})
	if !strings.Contains(loc, "toggle_err=") {
		t.Errorf("redirect = %q, want a named refusal", loc)
	}
	if got := b517Enabled(t, d, id); got != 1 {
		t.Errorf("a stale group key switched a rule it does not own (enabled=%d)", got)
	}
}

// TestB517_DefaultStateFollowsTheSelection — a request that names no state switches
// the selection ON unless every row of it is already on.
func TestB517_DefaultStateFollowsTheSelection(t *testing.T) {
	rules := []RuleRow{
		{ID: 1, Enabled: true},
		{ID: 2, Enabled: true},
		{ID: 3, Enabled: false},
	}
	if got := majorityDesired(rules, []int64{1, 2}); got != false {
		t.Errorf("all-on selection → %v, want false (offer the opposite state)", got)
	}
	if got := majorityDesired(rules, []int64{1, 3}); got != true {
		t.Errorf("mixed selection → %v, want true", got)
	}
	if got := majorityDesired(rules, []int64{99}); got != true {
		t.Errorf("unknown selection → %v, want true (safe default)", got)
	}
}

// TestB517_BackURLIsValidated — a redirect target is an input.
func TestB517_BackURLIsValidated(t *testing.T) {
	ok := map[string]string{
		"/my/exit-rules":                       "/my/exit-rules",
		"/my/exit-rules?lens=source&page=2":    "/my/exit-rules?lens=source&page=2",
		"/my/exit-rules?group=dom%3Aa.example": "/my/exit-rules?group=dom%3Aa.example",
		"  /my/exit-rules?lens=groups  ":       "/my/exit-rules?lens=groups",
	}
	for in, want := range ok {
		if got := safeRulesBack(in); got != want {
			t.Errorf("safeRulesBack(%q) = %q, want %q", in, got, want)
		}
	}
	for _, in := range []string{
		"", "https://evil.example/my/exit-rules", "//evil.example/my/exit-rules",
		"/admin/exit-rules", "/my/exit-rules/../../admin", "/my/exit-rules\\x",
		"/my/exit-rules\nLocation: https://evil.example",
	} {
		if got := safeRulesBack(in); got != rulesToggleBackPath {
			t.Errorf("safeRulesBack(%q) = %q, want the page root", in, got)
		}
	}
}

// TestB517_FlashRoundTrip — the outcome survives the redirect and replaces an
// older one instead of piling up.
func TestB517_FlashRoundTrip(t *testing.T) {
	got := rulesBackWithFlash("/my/exit-rules?lens=source&toggled=on&n=9", "off", 3, "")
	if !strings.Contains(got, "toggled=off") || !strings.Contains(got, "n=3") {
		t.Errorf("flash = %q, want the new outcome", got)
	}
	if strings.Contains(got, "toggled=on") || strings.Contains(got, "n=9") {
		t.Errorf("flash = %q, an older outcome survived", got)
	}
	if got := rulesBackWithFlash("/my/exit-rules", "", 0, "boom"); !strings.Contains(got, "toggle_err=boom") {
		t.Errorf("error flash = %q, want toggle_err", got)
	}
	if got := toggleCount("12"); got != 12 {
		t.Errorf("toggleCount(12) = %d", got)
	}
	if got := toggleCount("nonsense"); got != 0 {
		t.Errorf("toggleCount(nonsense) = %d, want 0 (a bad query is presentation, not a 500)", got)
	}
}

// TestB517_StateParsingAndIntersection — the small pieces the handler leans on.
func TestB517_StateParsingAndIntersection(t *testing.T) {
	for raw, want := range map[string]bool{"0": false, "off": false, "1": true, "on": true} {
		got, ok := ruleToggleDesired(raw)
		if !ok || got != want {
			t.Errorf("ruleToggleDesired(%q) = %v,%v want %v,true", raw, got, ok, want)
		}
	}
	if _, ok := ruleToggleDesired("maybe"); ok {
		t.Error("ruleToggleDesired(maybe) reported a state")
	}
	rows := []RuleRow{{ID: 1}, {ID: 2}}
	if got := intersectRuleIDs(rows, []int64{1, 9}); len(got) != 1 || got[0] != 1 {
		t.Errorf("intersectRuleIDs = %v, want [1] (a foreign id must be dropped)", got)
	}
	if got := intersectRuleIDs(rows, nil); got != nil {
		t.Errorf("intersectRuleIDs(nil) = %v, want nil", got)
	}
}

// itoaB517 keeps the fixtures readable without importing strconv everywhere.
func itoaB517(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	out := ""
	for n > 0 {
		out = string(rune('0'+n%10)) + out
		n /= 10
	}
	if neg {
		out = "-" + out
	}
	return out
}
