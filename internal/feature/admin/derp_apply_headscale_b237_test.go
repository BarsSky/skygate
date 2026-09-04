package admin

// derp_apply_headscale_b237_test.go — v1.5.2+ (B237) —
// unit tests for the headscale derp.urls rewriter.

import (
	"strings"
	"testing"
)

func TestRewriteDerpURLs_InsertsBothURLs(t *testing.T) {
	const input = `# config
log:
  level: info

database:
  type: sqlite

policy:
  mode: file
`
	out := rewriteDerpURLsOut(t, input, "http://skygate:8080/admin/derp/relays/derpmap.json")
	if !contains(out, "derp:") {
		t.Errorf("rewritten config missing `derp:` block:\n%s", out)
	}
	if !contains(out, "https://controlplane.tailscale.com/derpmap/default") {
		t.Errorf("rewritten config missing public Tailscale derpmap URL:\n%s", out)
	}
	if !contains(out, "http://skygate:8080/admin/derp/relays/derpmap.json") {
		t.Errorf("rewritten config missing skygate derpmap URL:\n%s", out)
	}
	// The original `policy:` block should still be there
	// (proves we didn't accidentally eat the next section).
	if !contains(out, "policy:") {
		t.Errorf("rewritten config lost the `policy:` block:\n%s", out)
	}
}

func TestRewriteDerpURLs_ReplacesExisting(t *testing.T) {
	const input = `derp:
  urls:
  - https://controlplane.tailscale.com/derpmap/default

log:
  level: info
`
	out := rewriteDerpURLsOut(t, input, "http://skygate:8080/admin/derp/relays/derpmap.json")
	// The old `derp:` block should be replaced (not
	// duplicated).
	if countOccurrences(out, "derp:") != 1 {
		t.Errorf("rewritten config has %d `derp:` blocks (expected 1):\n%s", countOccurrences(out, "derp:"), out)
	}
	// Public URL still present.
	if !contains(out, "https://controlplane.tailscale.com/derpmap/default") {
		t.Errorf("rewritten config missing public URL:\n%s", out)
	}
	// skygate URL added.
	if !contains(out, "http://skygate:8080/admin/derp/relays/derpmap.json") {
		t.Errorf("rewritten config missing skygate URL:\n%s", out)
	}
	// log: block still present.
	if !contains(out, "log:") {
		t.Errorf("rewritten config lost the `log:` block:\n%s", out)
	}
}

func TestRewriteDerpURLs_Idempotent(t *testing.T) {
	// Re-running rewriteDerpURLs with the same skygateURL
	// must be a no-op (the public URL appears once, the
	// skygate URL appears once, no duplicate `derp:` block).
	const input = `derp:
  urls:
  - https://controlplane.tailscale.com/derpmap/default
  - http://skygate:8080/admin/derp/relays/derpmap.json

log:
  level: info
`
	const skygateURL = "http://skygate:8080/admin/derp/relays/derpmap.json"
	out1 := rewriteDerpURLsOut(t, input, skygateURL)
	out2 := rewriteDerpURLsOut(t, out1, skygateURL)
	if out1 != out2 {
		t.Errorf("rewriteDerpURLs is not idempotent:\nbefore:\n%s\nafter:\n%s", out1, out2)
	}
	if countOccurrences(out2, skygateURL) != 1 {
		t.Errorf("skygate URL appears %d times after idempotent re-apply (expected 1):\n%s", countOccurrences(out2, skygateURL), out2)
	}
}

func TestRewriteDerpURLs_RejectsEmptyURL(t *testing.T) {
	_, err := rewriteDerpURLs("derp:\n  urls:\n  - foo\n", "")
	if err == nil {
		t.Error("expected error for empty skygateURL, got nil")
	}
}

func TestRewriteDerpURLs_HandlesFlowStyle(t *testing.T) {
	// Some headscale configs use flow-style: `urls: [a, b]`.
	// Our rewriter should still produce a valid block.
	const input = `derp:
  urls: [https://controlplane.tailscale.com/derpmap/default]

log:
  level: info
`
	out := rewriteDerpURLsOut(t, input, "http://skygate:8080/admin/derp/relays/derpmap.json")
	if !contains(out, "http://skygate:8080/admin/derp/relays/derpmap.json") {
		t.Errorf("rewriter dropped the skygate URL when input used flow-style:\n%s", out)
	}
	// The old `derp:` should have been replaced (not
	// duplicated).
	if countOccurrences(out, "derp:") != 1 {
		t.Errorf("flow-style: %d `derp:` blocks (expected 1):\n%s", countOccurrences(out, "derp:"), out)
	}
}

// helpers (file-local — no other file in this package
// needs them).
func rewriteDerpURLsOut(t *testing.T, yaml, skygateURL string) string {
	t.Helper()
	out, err := rewriteDerpURLs(yaml, skygateURL)
	if err != nil {
		t.Fatalf("rewriteDerpURLs: %v", err)
	}
	return out
}

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

func countOccurrences(s, substr string) int {
	return strings.Count(s, substr)
}
