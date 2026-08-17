// B123 (Goal 39 follow-up) — Exit Rules duplicate alert UX.
//
// Before B123, the /my/exit-rules?duplicate=1 redirect only carried
// ?existing=<targetValue> (the value the user tried to add). The
// alert said "правило для X уже существует" but never told the
// user WHICH rule already covered X, especially painful in the
// shared-IP case where one /32 already exists for a DIFFERENT
// parent_domain.
//
// B123 surfaces the blocking IP, the conflicting rule's ID (for a
// jump-to link), and the parent_domain that owns the IP. The
// redirect also re-fills form_* so the user can tweak and retry.
//
// These tests pin buildDuplicateRedirectURL: a pure function with
// no DB, so they're fast and exercise the URL contract directly.
// Integration coverage of the full POST→redirect flow needs a DB
// harness (Phase 2 PG-rewrite follow-up, see store_test.go).
package exit_rules

import (
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestBuildDuplicateRedirectURL_AllParamsPresent(t *testing.T) {
	got := buildDuplicateRedirectURL(
		"1.2.3.4",     // target
		42,            // existingID
		"1.2.3.4/32",  // blockingIP
		"",            // parentDomain (manual IP rule, no parent)
		9,             // devID
		"karolina",    // exitNode
		"ip",          // typeToInsert
		"1.2.3.4",     // targetValue
		"accept",      // action
	)
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse: %v (url=%q)", err, got)
	}
	if u.Path != "/my/exit-rules" {
		t.Errorf("path: got %q, want /my/exit-rules", u.Path)
	}
	q := u.Query()
	mustEq := func(key, want string) {
		t.Helper()
		if got := q.Get(key); got != want {
			t.Errorf("query[%q]: got %q, want %q", key, got, want)
		}
	}
	mustEq("duplicate", "1")
	mustEq("target", "1.2.3.4")
	mustEq("existing_id", "42")
	mustEq("blocking_ip", "1.2.3.4/32")
	mustEq("parent_domain", "")
	mustEq("form_device_id", "9")
	mustEq("form_exit_node", "karolina")
	mustEq("form_target_type", "ip")
	mustEq("form_target_value", "1.2.3.4")
	mustEq("form_action", "accept")
}

func TestBuildDuplicateRedirectURL_SharedIP_HasParentDomain(t *testing.T) {
	// B123's main motivation: when another domain's /32 already
	// covers the IP, the alert should show that parent_domain so
	// the user understands why a "new" rule for example.com is
	// rejected.
	got := buildDuplicateRedirectURL(
		"example.com",     // target (domain)
		123,               // existingID of the /32 that already exists
		"1.2.3.4/32",      // blockingIP
		"other-domain.com", // parentDomain (the domain that "owns" the /32)
		1,                 // devID
		"emilia",          // exitNode
		"subnet",          // typeToInsert
		"example.com",     // targetValue
		"accept",
	)
	q, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse: %v (url=%q)", err, got)
	}
	vals := q.Query()
	if vals.Get("parent_domain") != "other-domain.com" {
		t.Errorf("parent_domain: got %q, want other-domain.com", vals.Get("parent_domain"))
	}
	if vals.Get("blocking_ip") != "1.2.3.4/32" {
		t.Errorf("blocking_ip: got %q, want 1.2.3.4/32", vals.Get("blocking_ip"))
	}
	if vals.Get("existing_id") != "123" {
		t.Errorf("existing_id: got %q, want 123", vals.Get("existing_id"))
	}
}

func TestBuildDuplicateRedirectURL_SpecialCharsAreEscaped(t *testing.T) {
	// B123 invariant: all string values go through url.QueryEscape.
	// A value containing & = ? % or + would otherwise corrupt the
	// query string (e.g. unescaped & would split a single param
	// into two and break the template's q.Get() calls).
	got := buildDuplicateRedirectURL(
		"a&b=c",         // target with & and =
		7,
		"1.2.3.4/32&x=1", // blockingIP with & and =
		"foo.com?bar=1", // parentDomain with ? and =
		1,
		"host&one", // exitNode with &
		"ip",
		"1.2.3.4",
		"accept",
	)
	// We can parse the URL because the escaping is correct.
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse failed (escaping bug?): %v (url=%q)", err, got)
	}
	// And the unescaped values round-trip.
	if got := u.Query().Get("target"); got != "a&b=c" {
		t.Errorf("target round-trip: got %q, want %q", got, "a&b=c")
	}
	if got := u.Query().Get("blocking_ip"); got != "1.2.3.4/32&x=1" {
		t.Errorf("blocking_ip round-trip: got %q, want %q", got, "1.2.3.4/32&x=1")
	}
	if got := u.Query().Get("parent_domain"); got != "foo.com?bar=1" {
		t.Errorf("parent_domain round-trip: got %q, want %q", got, "foo.com?bar=1")
	}
	if got := u.Query().Get("form_exit_node"); got != "host&one" {
		t.Errorf("form_exit_node round-trip: got %q, want %q", got, "host&one")
	}
	// Sanity: no raw "=" in the param values before the next &.
	if strings.Contains(got, "target=a&b") {
		t.Errorf("unescaped & in target would split the query: %q", got)
	}
}

func TestBuildDuplicateRedirectURL_ZeroExistingID_StillValid(t *testing.T) {
	// B123 robustness: if insertRuleUnique returns existingID=0
	// for some reason (defensive — should never happen on the
	// "all dups" path), the URL must still parse and have
	// existing_id=0 so the template's {{if gt .existing_id 0}}
	// gates the link away.
	got := buildDuplicateRedirectURL(
		"1.2.3.4",
		0, // existingID=0 (no link)
		"1.2.3.4/32",
		"",
		1, "karolina", "ip", "1.2.3.4", "accept",
	)
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if u.Query().Get("existing_id") != "0" {
		t.Errorf("existing_id: got %q, want 0", u.Query().Get("existing_id"))
	}
	if !strings.HasPrefix(got, "/my/exit-rules?duplicate=1") {
		t.Errorf("missing duplicate=1 prefix: %q", got)
	}
}

func TestBuildDuplicateRedirectURL_NumericFormDeviceID(t *testing.T) {
	// B123 invariant: form_device_id is always a numeric string
	// (strconv.Itoa). The template uses it in <select> default
	// matching, so a non-numeric value would break the form
	// re-fill.
	got := buildDuplicateRedirectURL(
		"1.2.3.4", 1, "1.2.3.4/32", "", 999, "karolina", "ip", "1.2.3.4", "accept",
	)
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := u.Query().Get("form_device_id"); got != "999" {
		t.Errorf("form_device_id: got %q, want 999 (strconv.Itoa invariant)", got)
	}
	if _, err := strconv.Atoi(u.Query().Get("form_device_id")); err != nil {
		t.Errorf("form_device_id must be strconv.Atoi-clean, got %q (%v)", u.Query().Get("form_device_id"), err)
	}
}
