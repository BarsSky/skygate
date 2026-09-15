// internal/feature/admin/admin_acls_b250_test.go — B250 ACL pretty-print tests.
//
// B250 (2026-09-15): the /admin/acls page was rendering the
// raw headscale ACL JSON on a single line because the API
// returns a compact string. The `<pre>` tag preserves
// whitespace but can't ADD newlines that aren't in the
// source. The fix: prettyPrintACL strips any outer JSON-string
// wrapping (headscale returns the policy as a stringified
// value `"{\"acls\":...}"`), unmarshals, and re-marshals with
// MarshalIndent.
//
// These tests pin the pretty-print logic without spinning up
// the full HTTP handler stack.
package admin

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestPrettyPrintACL_Object pins the canonical case where
// headscale returns the policy as a raw JSON object (no
// outer string quotes). This is the B246 (GetACL) success
// path: `p.Policy[0] == '{'` so the raw bytes are cached.
func TestPrettyPrintACL_Object(t *testing.T) {
	compact := `{"hosts":{"skyadmin":["10.0.1.0/24"],"michail":["10.0.6.0/24"]},"acls":[{"action":"accept","src":["*"],"dst":["*:*"]}]}`
	got := prettyPrintACL(compact)
	if strings.Count(got, "\n") < 3 {
		t.Errorf("expected multi-line output, got %d newlines:\n%s", strings.Count(got, "\n"), got)
	}
	// Round-trip parse to verify the output is still valid JSON.
	var v interface{}
	if err := json.Unmarshal([]byte(got), &v); err != nil {
		t.Errorf("prettyPrintACL output is not valid JSON: %v\noutput:\n%s", err, got)
	}
}

// TestPrettyPrintACL_StringifiedObject pins the B250 critical
// case: headscale returns the policy as a STRINGIFIED JSON
// value (with surrounding double quotes). This is what
// production sees — pre-B250, json.Indent choked on the
// leading `"` and returned the raw string with literal `\n`.
//
// Input: `"{\"hosts\":{...}}"` (the value is the JSON object,
// the outer quotes are JSON string boundaries).
//
// Output: a properly indented JSON object on multiple lines.
func TestPrettyPrintACL_StringifiedObject(t *testing.T) {
	// Simulate what headscale returns: `"<json>"` — the JSON-encoded
	// string containing a JSON object. Note the `\"` inside are
	// JSON-escaped quotes (which the outer `"` delimit).
	stringified := `"{\"hosts\":{\"skyadmin\":[\"10.0.1.0/24\"]},\"acls\":[]}"`
	got := prettyPrintACL(stringified)

	if strings.Count(got, "\n") < 3 {
		t.Errorf("stringified input: expected multi-line output, got %d newlines:\n%s",
			strings.Count(got, "\n"), got)
	}
	// The output must START with `{` (the object), not `"` —
	// otherwise the template would render `&#34;{...}` which
	// looks like a quoted string, not JSON.
	trimmed := strings.TrimSpace(got)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		t.Errorf("output must start with `{` (JSON object), got %q", string(trimmed[0]))
	}
	// Round-trip parse.
	var v interface{}
	if err := json.Unmarshal([]byte(got), &v); err != nil {
		t.Errorf("prettyPrintACL output is not valid JSON: %v\noutput:\n%s", err, got)
	}
}

// TestPrettyPrintACL_LegacyDataWrapper pins the older headscale
// shape where the policy is wrapped in `{"data":"<stringified>"}`.
// In the current code path, GetACL has already extracted + unquoted
// the `data` field before we see it — so the function never sees
// the literal wrapper. The legacy wrapper case is just "what if
// the upstream caller forgot to unwrap" — defensive coverage.
//
// Pre-B250: json.Indent would fail because the wrapper isn't
// the policy. The test pins that the function still produces
// multi-line output even if called with the raw wrapper shape.
func TestPrettyPrintACL_LegacyDataWrapper(t *testing.T) {
	// The shape: an outer object with `policy` empty and `data`
	// stringified. (Currently GetACL pre-processes this before
	// prettyPrintACL sees it, but the function is defensive.)
	wrapped := `{"policy":"","data":"{\"acls\":[{\"action\":\"accept\",\"src\":[\"*\"],\"dst\":[\"*:*\"]}]}"}`
	got := prettyPrintACL(wrapped)

	// Multi-line output (whether we unwrap or just pretty-print
	// the wrapper object, both should produce ≥3 lines).
	if strings.Count(got, "\n") < 3 {
		t.Errorf("legacy wrapper: expected multi-line output, got %d newlines:\n%s",
			strings.Count(got, "\n"), got)
	}
}

// TestPrettyPrintACL_EmptyString pins the "no policy yet"
// branch. Must not crash on empty input.
func TestPrettyPrintACL_EmptyString(t *testing.T) {
	if prettyPrintACL("") != "" {
		t.Errorf("empty string should return empty string")
	}
}

// TestPrettyPrintACL_MalformedFallback pins the defensive
// fallback. If everything fails (strip, unmarshal, indent),
// return the original string. A malformed policy shouldn't
// 500 the admin page.
func TestPrettyPrintACL_MalformedFallback(t *testing.T) {
	garbage := "not json at all"
	got := prettyPrintACL(garbage)
	if got != garbage {
		t.Errorf("malformed input: expected fallback to original %q, got %q", garbage, got)
	}
}

// TestPrettyPrintACL_LineCount_GrowsFrom1ToMany pins the
// operator's visible behaviour: the output has more than 1
// line, so <pre> renders the policy on multiple lines.
func TestPrettyPrintACL_LineCount_GrowsFrom1ToMany(t *testing.T) {
	compact := `{"acls":[{"action":"accept","src":["*"],"dst":["*:*"]}]}`
	got := prettyPrintACL(compact)
	lines := strings.Count(got, "\n")
	if lines < 3 {
		t.Errorf("prettyPrintACL should produce multiple lines, got %d newlines", lines)
	}
}

// TestPrettyPrintACL_NoTrailingNewline pins the encoder
// stripping behaviour. json.Encoder.Encode appends a trailing
// newline; we strip it so the rendered <pre> doesn't have a
// spurious blank line.
func TestPrettyPrintACL_NoTrailingNewline(t *testing.T) {
	compact := `{"acls":[]}`
	got := prettyPrintACL(compact)
	if len(got) > 0 && got[len(got)-1] == '\n' {
		t.Errorf("prettyPrintACL output has trailing newline (would render as blank line): %q", got[len(got)-1:])
	}
}

// TestPrettyPrintACL_HTMLEscapesAngleBrackets pins the SetEscapeHTML
// behaviour. The output is wrapped in <pre> on /admin/acls,
// so any `<` in the policy (e.g. in a tagOwners value) would
// otherwise be parsed as an HTML tag by the browser, breaking
// the JSON rendering. SetEscapeHTML(true) converts them to
// \u003c. Verify by checking the output doesn't contain raw
// `<` outside of any safe context.
func TestPrettyPrintACL_HTMLEscapesAngleBrackets(t *testing.T) {
	// Most policies don't contain `<`, but if a future operator
	// puts a tagOwners key like "<custom>" it must not break
	// the page.
	compact := `{"tagOwners":{"<weird-tag>":["user@example.com"]}}`
	got := prettyPrintACL(compact)
	if strings.Contains(got, "<weird-tag>") {
		t.Errorf("output contains raw `<weird-tag>` — should be HTML-escaped to \\u003c\\u003e:\n%s", got)
	}
}
