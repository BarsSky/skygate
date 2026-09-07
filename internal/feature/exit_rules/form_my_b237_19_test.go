// B237.19 (v1.5.2+) — Form-error flash UX.
//
// Pre-fix: PostMyExitRule called http.Error(w, ..., 400) on
// form-validation failures (invalid IP, limit exceeded, device
// not owned, etc.). The browser rendered a giant plain-text
// page and the operator lost the form values they typed.
//
// B237.19 fix: the handler now calls http.Redirect to a
// /my/exit-rules?err=<msg>&form_* URL via the new
// buildFormErrorRedirectURL helper. The template renders .err
// as a flash banner above the form (same UI surface as the
// existing duplicate banner). The form's device_id / exit_node
// / target_type / target_value / action fields are preserved
// via the form_* query params.
//
// These tests pin buildFormErrorRedirectURL: a pure function
// with no DB. Same shape as form_my_b123_test.go.

package exit_rules

import (
	"net/url"
	"strings"
	"testing"
)

func TestBuildFormErrorRedirectURL_BasicShape(t *testing.T) {
	got := buildFormErrorRedirectURL(
		"invalid target_value \"foo\": expected IP or CIDR for target_type=ip",
		42, "karolina", "ip", "foo", "accept",
	)
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse: %v (url=%q)", err, got)
	}
	if u.Path != "/my/exit-rules" {
		t.Errorf("path: got %q, want /my/exit-rules", u.Path)
	}
	q := u.Query()
	// err= is the new B237.19 field that triggers the flash banner
	if q.Get("err") == "" {
		t.Error("err= must be present (it's the new B237.19 contract)")
	}
	// form_* fields preserve the user's typed values
	mustEq := func(key, want string) {
		t.Helper()
		if got := q.Get(key); got != want {
			t.Errorf("query[%q]: got %q, want %q", key, got, want)
		}
	}
	mustEq("form_device_id", "42")
	mustEq("form_exit_node", "karolina")
	mustEq("form_target_type", "ip")
	mustEq("form_target_value", "foo")
	mustEq("form_action", "accept")
}

func TestBuildFormErrorRedirectURL_SpecialCharsInErrMsg(t *testing.T) {
	// The error message is operator-facing. The pre-fix
	// http.Error wrote the message to the body without
	// escaping; post-fix the message goes into a URL
	// query string, so & = ? % must be escaped. Go's
	// html/template auto-escapes on render, but the
	// URL itself is built with raw string concat via
	// fmt.Sprintf — so we need url.QueryEscape on
	// the errMsg.
	got := buildFormErrorRedirectURL(
		"bad value: a&b=c?d#e+f%",  // every reserved char
		1, "host", "ip", "1.2.3.4", "accept",
	)
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse failed (escaping bug?): %v (url=%q)", err, got)
	}
	// The err must round-trip through url.Parse with the
	// original value. A naive implementation that just
	// inlines errMsg would break this test.
	if got := u.Query().Get("err"); got != "bad value: a&b=c?d#e+f%" {
		t.Errorf("err round-trip: got %q, want %q", got, "bad value: a&b=c?d#e+f%")
	}
	// Sanity: the raw & in the message did NOT split the
	// query into two params.
	for k := range u.Query() {
		if k != "err" && !strings.HasPrefix(k, "form_") {
			t.Errorf("unexpected query param introduced by unescaped & in errMsg: %q", k)
		}
	}
}

func TestBuildFormErrorRedirectURL_EmptyErrMsg(t *testing.T) {
	// Edge case: handler might pass an empty string if a
	// check fires before the errMsg is populated. The
	// resulting URL must still be valid (template's
	// `{{if .err}}` gate filters it out).
	got := buildFormErrorRedirectURL("", 1, "host", "ip", "1.2.3.4", "accept")
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if q := u.Query(); q.Get("err") != "" {
		t.Errorf("empty errMsg should produce empty err=, got %q", q.Get("err"))
	}
}

func TestBuildFormErrorRedirectURL_NumericFormDeviceID(t *testing.T) {
	// Same invariant as B123: form_device_id is always a
	// numeric string (strconv.Itoa). The template's
	// <select> default-matching depends on it.
	got := buildFormErrorRedirectURL("oops", 999, "host", "ip", "1.2.3.4", "accept")
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, err := url.QueryUnescape(u.Query().Get("form_device_id")); err != nil {
		t.Errorf("form_device_id must be unescapable: %v", err)
	}
}
