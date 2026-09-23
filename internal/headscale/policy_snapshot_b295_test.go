// internal/headscale/policy_snapshot_b295_test.go — B295 (2026-09-23).
//
// Live `aro`: the prefix page showed «состояние политики неизвестно … connect:
// connection refused» and the journal showed
//
//	acl-drift: cannot read the live policy to decide whether a re-apply is needed
//	(…) — applying unconditionally
//
// followed by a WRITE — which on a `policy.mode: file` host means
// `systemctl restart headscale` (0.29 re-reads the policy only at startup). The
// read failure therefore caused another restart, which caused the next read
// failure: the observation fed the outage.
//
// The last successfully applied policy is in `acl_snapshots`, so the same question
// can be answered with no API at all. These tests pin that decision and the retry
// that covers a restart window.
package headscale

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCompareWithSnapshot_B295(t *testing.T) {
	gen := `{"tagOwners":{"tag:x":["a@example.com"]},"grants":[{"src":["tag:x"],"dst":["*"]}]}`

	// 1. The live read failed, but the generated document equals what skygate last
	//    applied → NOTHING to write (and therefore no restart).
	v := CompareWithSnapshot(gen, 7, gen)
	if v.Apply || !v.InSync {
		t.Fatalf("identical snapshot: %+v, want InSync and no apply", v)
	}
	if v.Version != 7 || !strings.Contains(v.Reason, "v7") {
		t.Errorf("reason = %q, want it to name snapshot v7", v.Reason)
	}
	if !strings.Contains(v.Reason, "not restarted") {
		t.Errorf("reason = %q, want it to say headscale is not restarted", v.Reason)
	}

	// 2. A semantically equal but textually different snapshot (key order, duplicate
	//    grants) is still "nothing to do" — PolicyEquivalent is a set comparison.
	messy := `{"grants":[{"dst":["*"],"src":["tag:x"]},{"dst":["*"],"src":["tag:x"]}],"tagOwners":{"tag:x":["a@example.com"]}}`
	if v := CompareWithSnapshot(gen, 3, messy); v.Apply || !v.InSync {
		t.Errorf("semantically equal snapshot: %+v, want InSync and no apply", v)
	}

	// 3. The document really changed → apply (the read may just have missed the
	//    previous write).
	changed := `{"tagOwners":{"tag:x":["a@example.com"]},"grants":[{"src":["tag:x"],"dst":["10.0.0.0/8"]}]}`
	v = CompareWithSnapshot(changed, 4, gen)
	if !v.Apply || v.InSync {
		t.Errorf("differing snapshot: %+v, want apply", v)
	}
	if !strings.Contains(strings.ToLower(v.Reason), "differs") {
		t.Errorf("reason = %q, want it to say the documents differ", v.Reason)
	}

	// 4. No snapshot at all (fresh install) → keep the pre-B295 behaviour, but SAY
	//    that the decision was made blind.
	for _, tc := range []struct {
		name    string
		version int
		snap    string
	}{
		{"no version", 0, gen},
		{"empty snapshot", 6, "   "},
	} {
		v := CompareWithSnapshot(gen, tc.version, tc.snap)
		if !v.Apply || v.InSync {
			t.Errorf("%s: %+v, want apply", tc.name, v)
		}
		if !strings.Contains(strings.ToLower(v.Reason), "blind") {
			t.Errorf("%s: reason = %q, want it to admit the blind decision", tc.name, v.Reason)
		}
	}

	// 5. An unparseable snapshot must not be treated as "in sync" — that would skip
	//    a write that may be needed.
	if v := CompareWithSnapshot(gen, 2, "{not json"); v.InSync {
		t.Errorf("unparseable snapshot: %+v, want apply", v)
	}
}

func TestSnapshotInSyncHint_B295(t *testing.T) {
	hint := SnapshotInSyncHint(12)
	if !strings.Contains(hint, "v12") || !strings.Contains(hint, "APPLIED snapshot") {
		t.Errorf("hint = %q, want it to name the snapshot source", hint)
	}
}

// TestIsTransientReadError_B295 — the retry must cover a restarting daemon and
// must NOT waste time on a daemon that answered with an HTTP error.
func TestIsTransientReadError_B295(t *testing.T) {
	transient := []string{
		`Get "http://127.0.0.1:8081/api/v1/policy": dial tcp 127.0.0.1:8081: connect: connection refused`,
		"connectex: No connection could be made because the target machine actively refused it.",
		"headscale list nodes: timeout after 4s",
		"read tcp 127.0.0.1:1: i/o timeout",
		"unexpected EOF",
	}
	for _, msg := range transient {
		if !isTransientReadError(errStub(msg)) {
			t.Errorf("isTransientReadError(%q) = false, want true", msg)
		}
	}
	// An HTTP answer means the daemon is UP: retrying is pointless and the
	// file/CLI fallbacks are the interesting part.
	for _, code := range []int{401, 404, 500} {
		if isTransientReadError(&APIError{Method: "GET", Path: "/api/v1/policy", StatusCode: code, Body: "nope"}) {
			t.Errorf("isTransientReadError(HTTP %d) = true, want false", code)
		}
	}
	if isTransientReadError(nil) {
		t.Error("isTransientReadError(nil) = true, want false")
	}
}

// TestGetACLRetriesThenFallsBackToTheFile_B295 is the behavioural half: a read
// that fails transiently must be retried, and if the daemon stays down the file
// rung must still answer. The retry delay is shortened to nothing here.
func TestGetACLRetriesThenFallsBackToTheFile_B295(t *testing.T) {
	origRetries, origDelay := aclReadRetries, aclReadRetryDelay
	aclReadRetries, aclReadRetryDelay = 2, time.Millisecond
	t.Cleanup(func() { aclReadRetries, aclReadRetryDelay = origRetries, origDelay })

	dir := t.TempDir()
	policyPath := filepath.Join(dir, "policy.hujson")
	policy := `{"tagOwners":{},"grants":[]}`
	if err := os.WriteFile(policyPath, []byte(policy), 0o644); err != nil {
		t.Fatalf("write policy: %v", err)
	}
	t.Setenv("SKYGATE_HEADSCALE_POLICY_PATH", policyPath)

	c := b294Client(t)
	start := time.Now()
	got, err := c.GetACL()
	if err != nil {
		t.Fatalf("GetACL: %v", err)
	}
	if strings.TrimSpace(got) != policy {
		t.Errorf("GetACL = %q, want the file document", got)
	}
	// Three attempts (1 + 2 retries) with a 1 ms delay must still be fast; if the
	// retry delay were the production 1.2 s this would take >2 s, which is the
	// point of asserting it here.
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("GetACL took %s — the retry loop is not bounded by the injected delay", elapsed)
	}
}
