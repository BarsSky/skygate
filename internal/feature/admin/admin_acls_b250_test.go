// internal/feature/admin/admin_acls_b250_test.go — B250 ACL pretty-print tests.
//
// B250 (2026-09-15): the /admin/acls page was rendering the
// raw headscale ACL JSON on a single line because the API
// returns a compact string. The `<pre>` tag preserves
// whitespace but can't ADD newlines that aren't in the
// source. The fix: call json.Indent on the policy string
// before passing it to the template.
//
// These tests pin the pretty-print logic without spinning up
// the full HTTP handler stack (the live verify on
// C:\skygate-test exercises the actual rendering).
package admin

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// prettyPrintACL mirrors the json.Indent step in
// GetAdminACLs. Extracted so the test can pin it directly
// without touching the rendering pipeline.
//
// Returns the compact string unchanged if json.Indent fails
// (defensive: a malformed policy shouldn't 500 the admin page).
func prettyPrintACL(compact string) string {
	if compact == "" {
		return compact
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(compact), "", "  "); err != nil {
		return compact
	}
	return buf.String()
}

// TestPrettyPrintACL_HostsBlock pins the most common case —
// the operator's actual ACL with the per-user subnet blocks.
func TestPrettyPrintACL_HostsBlock(t *testing.T) {
	compact := `{"hosts":{"h-user-skyadmin-subnet":["10.0.1.0/24"],"h-user-michail-subnet":["10.0.6.0/24"],"h-user-guest":["10.0.99.0/24"]},"acls":[{"action":"accept","src":["*"],"dst":["*:*"]}]}`
	got := prettyPrintACL(compact)

	// Pin the multi-line output. Exact whitespace matters:
	// 2-space indent + newline after each { } [ ] , : .
	want := `{
  "hosts": {
    "h-user-skyadmin-subnet": [
      "10.0.1.0/24"
    ],
    "h-user-michail-subnet": [
      "10.0.6.0/24"
    ],
    "h-user-guest": [
      "10.0.99.0/24"
    ]
  },
  "acls": [
    {
      "action": "accept",
      "src": [
        "*"
      ],
      "dst": [
        "*:*"
      ]
    }
  ]
}`
	if got != want {
		t.Errorf("prettyPrintACL output mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestPrettyPrintACL_EmptyString pins the "no policy yet"
// branch. The handler returns "" from hs.GetACL() when
// headscale has no policy file — prettyPrintACL must not
// crash on empty input.
func TestPrettyPrintACL_EmptyString(t *testing.T) {
	if prettyPrintACL("") != "" {
		t.Errorf("prettyPrintACL(\"\") should return empty string")
	}
}

// TestPrettyPrintACL_MalformedFallback pins the defensive
// fallback. If the policy string isn't valid JSON (e.g.
// headscale returned a partial response or the CLI fallback
// gave us something weird), json.Indent returns an error —
// the helper must return the original string so the admin
// page still renders something (even if ugly) instead of
// 500-ing.
func TestPrettyPrintACL_MalformedFallback(t *testing.T) {
	malformed := `{"hosts": ["unclosed`
	got := prettyPrintACL(malformed)
	if got != malformed {
		t.Errorf("prettyPrintACL(malformed) = %q, want original %q", got, malformed)
	}
}

// TestPrettyPrintACL_AlreadyIndentedNoop pins that pretty-
// printing an already-indented string is idempotent (no
// additional indentation, no whitespace stripping).
func TestPrettyPrintACL_AlreadyIndentedNoop(t *testing.T) {
	alreadyIndented := `{
  "acls": []
}`
	got := prettyPrintACL(alreadyIndented)
	if got != alreadyIndented {
		t.Errorf("prettyPrintACL(alreadyIndented) = %q\nwant %q", got, alreadyIndented)
	}
}

// TestPrettyPrintACL_LineCount_GrowsFrom1ToMany pins the
// behavior that the operator reported: pre-fix the ACL was
// 1 line; post-fix it's many lines. The exact count varies
// with ACL content, but at minimum the output must have
// more than 1 newline (otherwise the `<pre>` rendering
// shows a single line again).
func TestPrettyPrintACL_LineCount_GrowsFrom1ToMany(t *testing.T) {
	compact := `{"acls":[{"action":"accept","src":["*"],"dst":["*:*"]}]}`
	got := prettyPrintACL(compact)
	lines := strings.Count(got, "\n")
	if lines < 3 {
		t.Errorf("prettyPrintACL should produce multiple lines, got %d newlines (1 means the <pre> tag has nothing to wrap)", lines)
	}
}
