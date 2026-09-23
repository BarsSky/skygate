// internal/headscale/acl_read_b294_test.go — B294 (2026-09-23).
//
// Live `aro` (the operator's prefix-assignment page):
//
//	состояние политики неизвестно: read live policy: api: Get
//	"http://127.0.0.1:8081/api/v1/policy": dial tcp 127.0.0.1:8081: connect:
//	connection refused; cli: all variants failed
//
// Two defects in that one sentence:
//
//  1. the READ path had only two rungs — the API and a docker-only
//     `headscale policy get` — so on a native install with an unreachable API it
//     gave up immediately and blamed a CLI it never ran ("all variants failed");
//     the policy FILE headscale actually serves in `file` mode was never read,
//     even though the WRITE path has used it since B272;
//  2. with no live policy and no live node list, `/admin/exit-nodes` rendered
//     «никто не объявляет: N» / «нет маршрута» for every prefix — absence of
//     evidence presented as a fact (see the page-side test in
//     internal/feature/admin/exit_nodes_b294_test.go).
package headscale

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// b294Client returns a client pointed at a port nothing listens on (the live
// symptom), with caching disabled so every call re-walks the chain.
func b294Client(t *testing.T) *Client {
	t.Helper()
	c := New("http://127.0.0.1:1", "stub-key")
	c.SetCacheTTL(0)
	return c
}

// TestGetACLReadsThePolicyFileWhenTheAPIIsDown_B294: the file rung must work on a
// native `policy.mode: file` host whose API is unreachable — no docker, no CLI.
func TestGetACLReadsThePolicyFileWhenTheAPIIsDown_B294(t *testing.T) {
	dir := t.TempDir()
	policyPath := filepath.Join(dir, "policy.hujson")
	policy := `{"tagOwners":{"tag:exit-node":["infra@example.com"]},"grants":[{"src":["*"],"dst":["*"]}]}`
	if err := os.WriteFile(policyPath, []byte(policy), 0o644); err != nil {
		t.Fatalf("write policy file: %v", err)
	}
	t.Setenv("SKYGATE_HEADSCALE_POLICY_PATH", policyPath)

	c := b294Client(t)
	got, err := c.GetACL()
	if err != nil {
		t.Fatalf("GetACL must fall back to the policy FILE when the API is down (live `aro`), got: %v", err)
	}
	if strings.TrimSpace(got) != policy {
		t.Errorf("GetACL = %q, want the file's document", got)
	}

	// The path is remembered, so the next read does not rediscover it.
	if c.PolicyPath != policyPath {
		t.Errorf("PolicyPath = %q, want it to be cached as %q", c.PolicyPath, policyPath)
	}
}

// TestGetACLNamesEveryRungInTheError_B294: when nothing works, the message must
// name the real blocker instead of the pre-B294 "cli: all variants failed" (which
// claimed a CLI failure that never happened).
func TestGetACLNamesEveryRungInTheError_B294(t *testing.T) {
	// Point the discovery away from any real headscale config: an absolute path
	// that does not exist, and a policy path that is not readable either.
	t.Setenv("SKYGATE_HEADSCALE_POLICY_PATH", filepath.Join(t.TempDir(), "nope.hujson"))
	t.Setenv("SKYGATE_HEADSCALE_CONFIG", filepath.Join(t.TempDir(), "no-config.yaml"))

	c := b294Client(t)
	_, err := c.GetACL()
	if err == nil {
		t.Fatal("with no API, no file and no CLI the read must fail loudly")
	}
	msg := err.Error()
	for _, want := range []string{"api:", "policy file:", "headscale CLI:"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error = %q, want it to name the %q rung", msg, want)
		}
	}
	if strings.Contains(msg, "cli: all variants failed") {
		t.Errorf("error = %q still carries the pre-B294 wording (it blames a docker-only CLI that was never tried)", msg)
	}
	// The live symptom is a reachability failure, so the fix must be named.
	if !strings.Contains(msg, "HEADSCALE_URL") {
		t.Errorf("error = %q, want the HEADSCALE_URL hint for a connection refusal", msg)
	}
	if !strings.Contains(msg, "127.0.0.1:1") {
		t.Errorf("error = %q, want the address skygate actually tried", msg)
	}
}

// TestACLReadHint_B294 — the hint is for reachability failures only: a 500 from a
// reachable headscale must not send the operator hunting for a network problem.
func TestACLReadHint_B294(t *testing.T) {
	refused := aclReadHint("http://127.0.0.1:8081", errStub("Get \"http://127.0.0.1:8081/api/v1/policy\": dial tcp 127.0.0.1:8081: connect: connection refused"))
	if !strings.Contains(refused, "HEADSCALE_URL") || !strings.Contains(refused, "127.0.0.1:8081") {
		t.Errorf("hint = %q, want the env var and the address", refused)
	}
	if !strings.Contains(refused, "never 127.0.0.1") {
		t.Errorf("hint = %q, want the container-loopback warning", refused)
	}
	for _, other := range []string{
		"headscale GET /api/v1/policy: 500 update is disabled for modes other than database",
		"headscale GET /api/v1/policy: 401 unauthorized",
	} {
		if got := aclReadHint("http://headscale:8080", errStub(other)); got != "" {
			t.Errorf("aclReadHint(%q) = %q, want no reachability hint", other, got)
		}
	}
	if got := aclReadHint("http://headscale:8080", nil); got != "" {
		t.Errorf("aclReadHint(nil error) = %q, want empty", got)
	}
}

// TestACLReadHintForNilClient_B294: the page passes whatever client it has; a nil
// one must not panic.
func TestACLReadHintForNilClient_B294(t *testing.T) {
	if got := ACLReadHintFor(nil, errStub("connect: connection refused")); !strings.Contains(got, "HEADSCALE_URL") {
		t.Errorf("ACLReadHintFor(nil, …) = %q, want the hint without a client", got)
	}
}

// errStub is a tiny error with a fixed message.
type errStub string

func (e errStub) Error() string { return string(e) }
