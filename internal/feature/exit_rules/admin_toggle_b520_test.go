// admin_toggle_b520_test.go — B520: the admin page switches another user's rule.
//
// The three things this handler can get wrong, pinned here:
//
//  1. the UPDATE must hit the OWNER's row (a crafted id from another user is still
//     a legitimate target here — that is the whole point of the admin page — but the
//     write is scoped by the owner resolved from the row, never by the form);
//  2. a NON-ADMIN is refused (the route is behind authMW, which means "logged in",
//     so the handler is the gate — the same defense-in-depth B379 uses);
//  3. the switched-off view survives the action, or the operator loses the row they
//     just switched off.
package exit_rules

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"skygate/internal/auth"
	skygatedb "skygate/internal/db"
)

// b520Post runs the admin toggle and returns the redirect target.
func b520Post(t *testing.T, s *Service, form url.Values) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/admin/exit-rules/toggle", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.PostAdminExitRuleToggle(rec, req)
	return rec.Code, rec.Header().Get("Location")
}

// TestB520_AdminSwitchesAnotherUsersRuleOff — the owner is resolved from the row.
func TestB520_AdminSwitchesAnotherUsersRuleOff(t *testing.T) {
	s, d := b517Fixture(t)
	foreign := b517InsertRule(t, d, 2, 29, "michail", "karolina", "domain", "other.example", "", 1, 0)

	code, loc := b520Post(t, s, url.Values{
		"id": {itoaB517(foreign)}, "enabled": {"0"},
		"back": {"/admin/exit-rules?page=2"},
	})
	if code != http.StatusSeeOther {
		t.Fatalf("answered %d, want 303", code)
	}
	if !strings.Contains(loc, "admin_toggled=off") || !strings.Contains(loc, "admin_n=1") {
		t.Errorf("redirect = %q, want admin_toggled=off&admin_n=1", loc)
	}
	if got := b517Enabled(t, d, foreign); got != 0 {
		t.Errorf("the other user's rule enabled = %d, want 0", got)
	}
	if !strings.Contains(loc, "page=2") {
		t.Errorf("redirect = %q, the page the admin was on must survive", loc)
	}
}

// TestB520_SwitchedOffViewSurvivesTheAction — property 3.
func TestB520_SwitchedOffViewSurvivesTheAction(t *testing.T) {
	s, d := b517Fixture(t)
	id := b517InsertRule(t, d, 1, 9, "skyadmin", "karolina", "ip", "198.51.100.20", "", 1, 0)

	_, loc := b520Post(t, s, url.Values{
		"id": {itoaB517(id)}, "enabled": {"0"}, "show_disabled": {"1"},
		"back": {"/admin/exit-rules"},
	})
	if !strings.Contains(loc, "show_disabled=1") {
		t.Errorf("redirect = %q, the switched-off view must survive (the row would vanish)", loc)
	}
}

// TestB520_NonAdminIsRefused — property 2.
func TestB520_NonAdminIsRefused(t *testing.T) {
	_, d := b517Fixture(t)
	id := b517InsertRule(t, d, 2, 29, "michail", "karolina", "ip", "198.51.100.21", "", 1, 0)
	nonAdmin := &Service{
		Backend: &b328Backend{claims: &auth.Claims{UserID: 2, Username: "michail"}},
		DB:      skygatedb.FixedDBSource{DB: d},
	}

	code, _ := b520Post(t, nonAdmin, url.Values{"id": {itoaB517(id)}, "enabled": {"0"}})
	if code != http.StatusForbidden {
		t.Fatalf("a non-admin answered %d, want 403", code)
	}
	if got := b517Enabled(t, d, id); got != 1 {
		t.Errorf("a refused request changed the rule (enabled=%d)", got)
	}
}

// TestB520_UnknownRuleAndBadFormsAreRefused — no silent no-ops.
func TestB520_UnknownRuleAndBadFormsAreRefused(t *testing.T) {
	s, _ := b517Fixture(t)

	_, loc := b520Post(t, s, url.Values{"id": {"999999"}, "enabled": {"0"}, "back": {"/admin/exit-rules"}})
	if !strings.Contains(loc, "admin_err=") {
		t.Errorf("unknown id = %q, want a named refusal", loc)
	}
	_, loc = b520Post(t, s, url.Values{"enabled": {"0"}, "back": {"/admin/exit-rules"}})
	if !strings.Contains(loc, "admin_err=") {
		t.Errorf("empty form = %q, want a named refusal", loc)
	}
	_, loc = b520Post(t, s, url.Values{"id": {"1"}, "enabled": {"maybe"}, "back": {"/admin/exit-rules"}})
	if !strings.Contains(loc, "admin_err=") {
		t.Errorf("bad state = %q, want a named refusal", loc)
	}
}

// TestB520_ReturnPathIsValidated — the admin flash must not become a redirect to
// another origin, and must not leak to the user page (where the admin is not a user).
func TestB520_ReturnPathIsValidated(t *testing.T) {
	s, d := b517Fixture(t)
	id := b517InsertRule(t, d, 1, 9, "skyadmin", "karolina", "ip", "198.51.100.22", "", 1, 0)

	for _, evil := range []string{"https://evil.example/x", "//evil.example/x", "/my/exit-rules", "/admin/exit-rules/../../x"} {
		_, loc := b520Post(t, s, url.Values{"id": {itoaB517(id)}, "enabled": {"0"}, "back": {evil}})
		if !strings.HasPrefix(loc, "/admin/exit-rules?") {
			t.Errorf("back=%q produced %q, want the admin page", evil, loc)
		}
	}
}
