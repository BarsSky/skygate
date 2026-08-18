// v1.4.0 B140 — per-row accept_routes toggle on /admin/exit-nodes.
// Unit tests for parseAcceptRoutesFormValue (the form-value → int
// converter). The function is pure (no DB, no globals, no headscale),
// so a Go-only test pins the contract without a PG/SQLite setup.
//
// The contract:
//   "1"   →  1  (true — node accepts routes from peers)
//   "0"   →  0  (unset — Tailscale decides the default)
//   "-1"  → -1  (false — node does NOT accept routes from peers)
//   anything else → error (no silent fallback to 0)
//
// Whitespace is trimmed ("  1 " → 1) so the form's auto-added
// spaces don't break the parse.
//
// These tests are the PG-free unit test for B140. The runtime
// path (db.SetExitServerAcceptRoutes + the HTTP handler) is
// pinned by check_b140.sh's code-level grep contracts; the
// runtime test (live UI toggle) is done manually on the VM
// after deploy.

package admin

import "testing"

func TestParseAcceptRoutesFormValue_True(t *testing.T) {
	got, err := parseAcceptRoutesFormValue("1")
	if err != nil {
		t.Fatalf("expected no error for \"1\", got %v", err)
	}
	if got != 1 {
		t.Errorf("expected 1, got %d", got)
	}
}

func TestParseAcceptRoutesFormValue_Default(t *testing.T) {
	got, err := parseAcceptRoutesFormValue("0")
	if err != nil {
		t.Fatalf("expected no error for \"0\", got %v", err)
	}
	if got != 0 {
		t.Errorf("expected 0, got %d", got)
	}
}

func TestParseAcceptRoutesFormValue_False(t *testing.T) {
	got, err := parseAcceptRoutesFormValue("-1")
	if err != nil {
		t.Fatalf("expected no error for \"-1\", got %v", err)
	}
	if got != -1 {
		t.Errorf("expected -1, got %d", got)
	}
}

func TestParseAcceptRoutesFormValue_TrimsWhitespace(t *testing.T) {
	cases := []string{"  1 ", "\t-1\n", " 0 "}
	want := []int{1, -1, 0}
	for i, c := range cases {
		got, err := parseAcceptRoutesFormValue(c)
		if err != nil {
			t.Errorf("case %d (%q): unexpected error %v", i, c, err)
			continue
		}
		if got != want[i] {
			t.Errorf("case %d (%q): got %d, want %d", i, c, got, want[i])
		}
	}
}

func TestParseAcceptRoutesFormValue_RejectsUnknown(t *testing.T) {
	// Unknown values must return an error, not silently map to 0.
	// Silently mapping to 0 would mean a typo (e.g. "2" or "yes")
	// in the form would change the operator's intent. The error
	// is wrapped in the URL's err= query param so the page can
	// render a flash message.
	cases := []string{"", "2", "-2", "yes", "true", "false", "default", "1.0", "1e0"}
	for _, c := range cases {
		_, err := parseAcceptRoutesFormValue(c)
		if err == nil {
			t.Errorf("expected error for %q, got nil (would silently map to 0)", c)
		}
	}
}

func TestParseAcceptRoutesFormValue_EmptyStringIsError(t *testing.T) {
	// Empty string is the "no state submitted" case — e.g. a
	// crafted form with the select cleared. Must be an error
	// so the handler can render a clear "missing state" flash
	// instead of writing a meaningless 0 to the DB.
	_, err := parseAcceptRoutesFormValue("")
	if err == nil {
		t.Fatal("expected error for empty string, got nil")
	}
}
