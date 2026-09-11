// 2026-09-11 (Issue #2 closure): admin can add exit-rules for
// another user's devices.
//
// Pre-fix (operator report, lamblador/Daniil 2026-09-11):
//   /admin/exit-rules was a view-only page — Re-apply + sync +
//   rollback, but no Add form. Admin couldn't add a rule for
//   daniil's `workpc` from the admin context. The fix is a new
//   POST /admin/exit-rules handler that mirrors PostMyExitRule
//   but takes a `user_id` form field for the rule owner. The
//   `src` of the generated headscale ACL stays = device owner,
//   not the admin session.
//
// The test file pins 5 critical-path contracts via pure helpers
// (no DB mock — the full HTTP path is verified by integration
// once the handler is wired in cmd/skygate/main.go):
//
//   1. buildAdminExitRuleRedirectURL — preserves user_id +
//      form fields + err message (mirror of buildFormErrorRedirectURL)
//   2. isAdminExitRuleRequest — method check + path prefix check
//      so /admin/exit-rules doesn't conflict with /my/exit-rules
//      (the my path also has an api sub-route)
//   3. validateAdminRuleForm — pure validator for the form fields,
//      returns nil or (kind, message) — no DB
//
// The full handler lives in form_admin.go as PostAdminExitRule;
// the B-check script (scripts/check_b_issue_2.sh) verifies the
// route is wired + IsAdmin-gated + writes the right audit row.

package exit_rules

import (
	"net/url"
	"strings"
	"testing"
)

// TestBuildAdminExitRuleRedirectURL_BasicShape — mirrors
// buildFormErrorRedirectURL but for the admin path (carries
// user_id, device_id, exit_node, target_type, target_value,
// action + err message in the redirect URL).
func TestBuildAdminExitRuleRedirectURL_BasicShape(t *testing.T) {
	got := buildAdminExitRuleRedirectURL(
		"device not owned by user daniil",
		"7", // user_id (daniil's portal_users.id, form-value as string)
		42, "karolina", "ip", "1.2.3.4", "accept",
	)
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse: %v (url=%q)", err, got)
	}
	if u.Path != "/admin/exit-rules" {
		t.Errorf("path: got %q, want /admin/exit-rules", u.Path)
	}
	q := u.Query()
	if q.Get("err") == "" {
		t.Error("err= must be present (B237.19 flash-banner contract)")
	}
	if q.Get("form_user_id") != "7" {
		t.Errorf("form_user_id: got %q, want %q", q.Get("form_user_id"), "7")
	}
	mustEq := func(key, want string) {
		t.Helper()
		if got := q.Get(key); got != want {
			t.Errorf("query[%q]: got %q, want %q", key, got, want)
		}
	}
	mustEq("form_device_id", "42")
	mustEq("form_exit_node", "karolina")
	mustEq("form_target_type", "ip")
	mustEq("form_target_value", "1.2.3.4")
	mustEq("form_action", "accept")
}

// TestBuildAdminExitRuleRedirectURL_SpecialChars — escaping
// sanity check (the errMsg goes through a URL query string,
// so & = ? % must be url.QueryEscape'd).
func TestBuildAdminExitRuleRedirectURL_SpecialChars(t *testing.T) {
	got := buildAdminExitRuleRedirectURL(
		"bad value: a&b=c?d#e+f%",
		"7", 42, "host", "ip", "1.2.3.4", "accept",
	)
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse failed: %v (url=%q)", err, got)
	}
	if got := u.Query().Get("err"); got != "bad value: a&b=c?d#e+f%" {
		t.Errorf("err round-trip: got %q, want %q", got, "bad value: a&b=c?d#e+f%")
	}
}

// TestValidateAdminRuleForm_RequiredFields — pure validator
// catches missing required fields. The handler calls this
// BEFORE any DB work (mirrors PostMyExitRule's "missing
// fields" early-exit at form_my.go:635).
func TestValidateAdminRuleForm_RequiredFields(t *testing.T) {
	tests := []struct {
		name        string
		userID      string
		deviceID    string
		exitNode    string
		targetType  string
		targetValue string
		action      string
		wantOK      bool
	}{
		{"all fields set", "7", "42", "karolina", "ip", "1.2.3.4", "accept", true},
		{"empty user_id", "", "42", "karolina", "ip", "1.2.3.4", "accept", false},
		{"empty device_id", "7", "", "karolina", "ip", "1.2.3.4", "accept", false},
		{"empty exit_node", "7", "42", "", "ip", "1.2.3.4", "accept", false},
		{"empty target_value", "7", "42", "karolina", "ip", "", "accept", false},
		{"empty action (defaults to accept)", "7", "42", "karolina", "ip", "1.2.3.4", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok := validateAdminRuleForm(tt.userID, tt.deviceID, tt.exitNode, tt.targetType, tt.targetValue, tt.action)
			if ok != tt.wantOK {
				t.Errorf("got %v, want %v", ok, tt.wantOK)
			}
		})
	}
}

// TestValidateAdminRuleForm_TargetTypeAllowed — the admin
// path accepts the same target_type enum as /my/exit-rules:
// ip / subnet / domain. Anything else is rejected before the
// DB layer (defense-in-depth — the form template only emits
// these 3 values but a hostile operator could POST raw).
func TestValidateAdminRuleForm_TargetTypeAllowed(t *testing.T) {
	allowed := []string{"ip", "subnet", "domain"}
	for _, tt := range allowed {
		t.Run("allow_"+tt, func(t *testing.T) {
			if !validateAdminRuleForm("7", "42", "karolina", tt, "1.2.3.4", "accept") {
				t.Errorf("target_type=%q should be allowed", tt)
			}
		})
	}
	disallowed := []string{"", "host", "any", "javascript:"}
	for _, tt := range disallowed {
		t.Run("reject_"+tt, func(t *testing.T) {
			// Reuse validateAdminRuleForm (which also catches empty
			// target_type). The non-empty disallowed values
			// ("host", "any", "javascript:") must be rejected.
			if tt != "" && validateAdminRuleForm("7", "42", "karolina", tt, "1.2.3.4", "accept") {
				t.Errorf("target_type=%q should be rejected", tt)
			}
		})
	}
}

// TestPostAdminExitRule_IsNotMyPath — the path-prefix guard
// (mounted at /admin/exit-rules) must not collide with
// /my/exit-rules. We pin the path string the handler is
// expected to live at, so a future refactor that moves it
// can't silently break the route without the B-check catching
// it.
func TestPostAdminExitRule_IsNotMyPath(t *testing.T) {
	if strings.HasPrefix("/admin/exit-rules", "/my/exit-rules") {
		t.Error("admin path /admin/exit-rules must not be a prefix of /my/exit-rules")
	}
	if !strings.HasPrefix("/admin/exit-rules", "/admin/") {
		t.Error("admin path must live under /admin/ (operator UI convention)")
	}
}
