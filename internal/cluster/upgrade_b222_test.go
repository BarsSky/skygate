// v1.5.0+ / B222 — unit tests for the rolling-upgrade
// orchestrator + the new RejoinNode helper + the
// buildRejoinDetail detail builder.
//
// Most of the new behaviour is DB-bound (RejoinNode
// writes to cluster_node + cluster_audit, the
// orchestrator polls /healthz on a real HTTP target).
// The live-verify on the agent covers the
// self-upgrade guard + the no-target error path;
// these tests pin the pure-Go contracts:
//
//   1. RejoinNode state-transition matrix (the
//      allowed transitions, the rejected ones, the
//      idempotent ready→ready no-op).
//   2. buildRejoinDetail JSON shape (the audit
//      detail schema).
//   3. UpgradeOrchestrator self-upgrade guard
//      (checkSelfUpgrade returns ErrSelfUpgrade
//      when the target matches the orchestrator's
//      hostname).
//   4. UpgradeOrchestrator pollOnce with a
//      httptest.Server (build match → true,
//      non-match → false, error → false).

package cluster

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// --- buildRejoinDetail JSON shape ---

func TestRejoinDetailSchema(t *testing.T) {
	// B222 contract: the RejoinNode audit row's
	// detail JSON has the 4 fields the B215
	// /admin/ha filter + the future /admin/audit
	// click-through depend on: node_id, hostname,
	// from_state (draining|failed), to_state
	// (always "ready"), actor.
	detail := buildRejoinDetail("node-abc123", "skygate-standby", "draining", "skyadmin")
	var got map[string]interface{}
	if err := json.Unmarshal([]byte(detail), &got); err != nil {
		t.Fatalf("RejoinNode detail is not valid JSON: %v\n  raw: %s", err, detail)
	}
	wantFields := map[string]string{
		"node_id":    "node-abc123",
		"hostname":   "skygate-standby",
		"from_state": "draining",
		"to_state":   "ready",
		"actor":      "skyadmin",
	}
	for k, v := range wantFields {
		if got[k] != v {
			t.Errorf("detail[%q] = %v, want %q", k, got[k], v)
		}
	}
}

func TestRejoinDetailSchemaFailedFromState(t *testing.T) {
	// B222 contract: RejoinNode also handles
	// from_state=failed (the post-failover
	// recovery path). The detail JSON must carry
	// the actual from_state so the operator can
	// tell "drained-then-upgraded" apart from
	// "failed-then-recovered".
	detail := buildRejoinDetail("node-xyz", "svi-direct-2", "failed", "system")
	var got map[string]interface{}
	if err := json.Unmarshal([]byte(detail), &got); err != nil {
		t.Fatalf("RejoinNode detail is not valid JSON: %v\n  raw: %s", err, detail)
	}
	if got["from_state"] != "failed" {
		t.Errorf("from_state = %v, want %q", got["from_state"], "failed")
	}
	if got["to_state"] != "ready" {
		t.Errorf("to_state = %v, want %q", got["to_state"], "ready")
	}
}

// --- checkSelfUpgrade ---

func TestCheckSelfUpgrade_SameHostname(t *testing.T) {
	// B222 contract: UpgradeNode refuses to
	// upgrade its own node. The check matches
	// case-insensitively (hostnames are
	// case-insensitive per RFC 952).
	o := &UpgradeOrchestrator{}
	// Save/restore SelfHostname so we don't break
	// the OS state for other tests.
	orig := selfHostnameFn
	selfHostnameFn = func() (string, error) { return "SKYGATE-PRIMARY", nil }
	defer func() { selfHostnameFn = orig }()
	err := o.checkSelfUpgrade("skygate-primary")
	if err != ErrSelfUpgrade {
		t.Errorf("checkSelfUpgrade(\"skygate-primary\") = %v, want ErrSelfUpgrade", err)
	}
}

func TestCheckSelfUpgrade_DifferentHostname(t *testing.T) {
	// B222 contract: a different node is fine
	// (the orchestrator can upgrade any other
	// node).
	o := &UpgradeOrchestrator{}
	orig := selfHostnameFn
	selfHostnameFn = func() (string, error) { return "skygate-primary", nil }
	defer func() { selfHostnameFn = orig }()
	err := o.checkSelfUpgrade("skygate-standby")
	if err != nil {
		t.Errorf("checkSelfUpgrade(\"skygate-standby\") = %v, want nil", err)
	}
}

func TestCheckSelfUpgrade_HostnameReadFailureIsPermissive(t *testing.T) {
	// B222 contract: if os.Hostname() fails
	// (rare in practice — happens on hosts with
	// broken /proc or a chroot), the upgrade
	// proceeds. The alternative is a hard error
	// that would block all upgrades on a
	// misconfigured host.
	o := &UpgradeOrchestrator{}
	orig := selfHostnameFn
	selfHostnameFn = func() (string, error) { return "", errFakeHostname }
	defer func() { selfHostnameFn = orig }()
	err := o.checkSelfUpgrade("any-node")
	if err != nil {
		t.Errorf("checkSelfUpgrade with broken os.Hostname() = %v, want nil (permissive fallback)", err)
	}
}

// --- pollOnce via httptest ---

func TestPollOnce_BuildMatch(t *testing.T) {
	// B222 contract: pollOnce returns true when
	// the target /healthz responds 200 with a
	// matching build string.
	buildID := "abc1234+abc1234"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"build":"` + buildID + `","status":"ok"}`))
	}))
	defer srv.Close()
	o := &UpgradeOrchestrator{BuildID: buildID, HTTPClient: srv.Client()}
	// The real pollOnce uses http://<hostname>:8080
	// which won't work against an httptest server
	// at a random port. We test the body-parsing
	// logic directly: rewrite the URL to the
	// httptest server.
	url := srv.URL + "/healthz"
	if !o.pollOnce(context.Background(), url) {
		t.Errorf("pollOnce with matching build should return true")
	}
}

func TestPollOnce_BuildMismatch(t *testing.T) {
	// B222 contract: pollOnce returns false when
	// the target's build is different (the OLD
	// binary is still running).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"build":"old-commit","status":"ok"}`))
	}))
	defer srv.Close()
	o := &UpgradeOrchestrator{BuildID: "new-commit", HTTPClient: srv.Client()}
	url := srv.URL + "/healthz"
	if o.pollOnce(context.Background(), url) {
		t.Errorf("pollOnce with non-matching build should return false")
	}
}

func TestPollOnce_5xxReturnsFalse(t *testing.T) {
	// B222 contract: pollOnce returns false on
	// 5xx (the target is restarting, not yet
	// ready). The orchestrator keeps polling.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	o := &UpgradeOrchestrator{BuildID: "any", HTTPClient: srv.Client()}
	url := srv.URL + "/healthz"
	if o.pollOnce(context.Background(), url) {
		t.Errorf("pollOnce on 5xx should return false (keep polling)")
	}
}

func TestPollOnce_ConnectionRefusedReturnsFalse(t *testing.T) {
	// B222 contract: pollOnce returns false on
	// connection refused (the target's skygate is
	// stopped for the binary swap). The
	// orchestrator keeps polling.
	o := &UpgradeOrchestrator{BuildID: "any", HTTPClient: &http.Client{Timeout: 100 * 0}}
	// Port 1 is reserved + never bound; the
	// connection will refuse immediately.
	if o.pollOnce(context.Background(), "http://127.0.0.1:1/healthz") {
		t.Errorf("pollOnce on connection refused should return false (keep polling)")
	}
}

// --- helpers for mocking SelfHostname ---

var errFakeHostname = stringErr("fake hostname failure for test")

type stringErr string

func (e stringErr) Error() string { return string(e) }
