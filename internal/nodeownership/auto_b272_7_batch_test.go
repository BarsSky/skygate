// B272.7 (v1.5.35) — the reconciler must permit every tag it is about to apply
// in ONE policy write.
//
// Live on the native host `aro` (2026-09-20, right after the privileged policy
// helper was finally installed): three devices needed three dev-tags and the
// first tick permitted exactly ONE of them —
//
//	tag-reconcile: checked=4 applied=1 failed=2
//	400 requested tags [tag:dev-daniil-laptop] are invalid or not permitted
//
// while `grep -c 'tag:dev-' /etc/headscale/policy.hujson` showed 1 for three
// devices. Root cause: EnsureTagOwner is a read-modify-write of the WHOLE
// policy, it was called once per device, and on a file-mode host the write is
// performed asynchronously by `skygate-policy.path`. Call N read the policy
// before the applier had written call N-1's result, so every write after the
// first started from the same stale snapshot and dropped its predecessor's tag.
// The fix is structural — one combined write per pass — so there is nothing left
// to interleave.
package nodeownership

import (
	"errors"
	"testing"
	"time"

	"skygate/internal/db"
	"skygate/internal/headscale"
)

// TestB2727_ThreeDevicesOnePolicyWrite reproduces the live `aro` state exactly:
// three portal-owned devices, three different dev-tags, none of them present in
// headscale's tagOwners yet.
func TestB2727_ThreeDevicesOnePolicyWrite(t *testing.T) {
	hs := newFakeLister(
		headscale.NodeView{ID: "2", Hostname: "workpc", UserID: "1"},
		headscale.NodeView{ID: "3", Hostname: "laptop", UserID: "1"},
		headscale.NodeView{ID: "4", Hostname: "homepc", UserID: "1"},
	)
	rows := []db.NodeOwner{
		{NodeID: "2", Hostname: "workpc", Username: "daniil", Tag: "tag:dev-daniil-workpc"},
		{NodeID: "3", Hostname: "laptop", Username: "daniil", Tag: "tag:dev-daniil-laptop"},
		{NodeID: "4", Hostname: "homepc", Username: "daniil", Tag: "tag:dev-daniil-homepc"},
	}

	res := ReconcileTags(&fakeDBSource{}, hs, hs.nodes, rows, "ts.example.com", nil)

	if res.Checked != 3 || res.Applied != 3 || res.Failed != 0 {
		t.Fatalf("result = %+v, want checked=3 applied=3 failed=0 (the aro symptom was applied=1 failed=2)", res)
	}
	if hs.ownerBatches != 1 {
		t.Errorf("EnsureTagOwners calls = %d, want 1 — N tags must cost ONE policy write, otherwise the asynchronous applier races itself", hs.ownerBatches)
	}
	if hs.ownerCalls != 0 {
		t.Errorf("per-tag EnsureTagOwner calls = %d, want 0 — the batch already permitted every tag", hs.ownerCalls)
	}
	for _, tag := range []string{"tag:dev-daniil-workpc", "tag:dev-daniil-laptop", "tag:dev-daniil-homepc"} {
		owners, ok := hs.tagOwners[tag]
		if !ok {
			t.Errorf("%s was never permitted — headscale answers 'are invalid or not permitted' for it", tag)
			continue
		}
		if len(owners) != 2 || owners[0] != "daniil@ts.example.com" || owners[1] != "tagged-devices@ts.example.com" {
			t.Errorf("owners[%s] = %v, want [daniil@ts.example.com tagged-devices@ts.example.com]", tag, owners)
		}
	}
}

// TestB2727_BatchPermitsOnlyTagsThatWillBeApplied: the batch must mirror the
// main loop's predicates. A row whose node is already tagged (or gone from
// headscale) must NOT create a tagOwners entry — permitting a tag nothing will
// apply is policy drift, and the operator reads that file by hand.
func TestB2727_BatchPermitsOnlyTagsThatWillBeApplied(t *testing.T) {
	hs := newFakeLister(
		headscale.NodeView{ID: "2", Hostname: "workpc", Tags: []string{"tag:dev-daniil-workpc"}},
		headscale.NodeView{ID: "3", Hostname: "laptop"},
	)
	rows := []db.NodeOwner{
		{NodeID: "2", Username: "daniil", Tag: "tag:dev-daniil-workpc"}, // already tagged
		{NodeID: "99", Username: "daniil", Tag: "tag:dev-daniil-gone"},  // node deleted
		{NodeID: "3", Username: "daniil", Tag: "tag:dev-daniil-laptop"}, // needs it
	}

	res := ReconcileTags(&fakeDBSource{}, hs, hs.nodes, rows, "ts.example.com", nil)

	if res.Applied != 1 || res.Failed != 0 || res.Missing != 1 {
		t.Fatalf("result = %+v, want applied=1 missing=1 failed=0", res)
	}
	if hs.ownerBatches != 1 {
		t.Fatalf("EnsureTagOwners calls = %d, want 1", hs.ownerBatches)
	}
	if _, ok := hs.tagOwners["tag:dev-daniil-workpc"]; ok {
		t.Error("tagOwners gained a tag for a node that already carries it")
	}
	if _, ok := hs.tagOwners["tag:dev-daniil-gone"]; ok {
		t.Error("tagOwners gained a tag for a node headscale no longer has")
	}
	if _, ok := hs.tagOwners["tag:dev-daniil-laptop"]; !ok {
		t.Error("tagOwners is missing the tag of the device that needs it")
	}
}

// TestB2727_NoPendingTagsMeansNoWrite: an in-sync tailnet must produce zero
// policy writes — the reconciler runs every five minutes, and the file-mode
// write restarts headscale.
func TestB2727_NoPendingTagsMeansNoWrite(t *testing.T) {
	hs := newFakeLister(headscale.NodeView{ID: "2", Hostname: "workpc", Tags: []string{"tag:dev-daniil-workpc"}})
	rows := []db.NodeOwner{{NodeID: "2", Hostname: "workpc", Username: "daniil", Tag: "tag:dev-daniil-workpc"}}

	res := ReconcileTags(&fakeDBSource{}, hs, hs.nodes, rows, "ts.example.com", nil)

	if res.Applied != 0 || res.Failed != 0 {
		t.Fatalf("result = %+v, want a no-op pass", res)
	}
	if hs.ownerBatches != 0 {
		t.Errorf("EnsureTagOwners calls = %d, want 0 for an in-sync tailnet (empty request is not a write)", hs.ownerBatches)
	}
}

// TestB2727_TransientAddTagIsRetried: the same restart window that makes the
// permit step transient applies to the apply itself — and the batch makes the
// restart happen in the SAME tick as the applies, so without a retry here the
// tick that just permitted every tag can lose every AddTag to a daemon that is
// still coming up.
func TestB2727_TransientAddTagIsRetried(t *testing.T) {
	hs := newFakeLister(headscale.NodeView{ID: "2", Hostname: "workpc"})
	hs.addErr = errors.New(`api: headscale POST /api/v1/node/2/tags: dial tcp 127.0.0.1:8081: connect: connection refused`)
	hs.addFailN = 1
	rows := []db.NodeOwner{{NodeID: "2", Hostname: "workpc", Username: "daniil", Tag: "tag:dev-daniil-workpc"}}

	start := time.Now()
	res := ReconcileTags(&fakeDBSource{}, hs, hs.nodes, rows, "ts.example.com", nil)

	if res.Applied != 1 || res.Failed != 0 {
		t.Fatalf("result = %+v, want applied=1 failed=0 (a refused connection is transient by construction)", res)
	}
	if hs.addCalls != 2 {
		t.Errorf("AddTag calls = %d, want 2 (one refusal + one retry)", hs.addCalls)
	}
	if elapsed := time.Since(start); elapsed < 500*time.Millisecond {
		t.Errorf("retry happened after %v — expected the first backoff to be about a second", elapsed)
	}
}

// TestB2727_PermissionRefusalOnAddTagIsNotRetried: a 400 is an operator problem,
// not a timing problem; retrying it would only delay the alert.
func TestB2727_PermissionRefusalOnAddTagIsNotRetried(t *testing.T) {
	hs := newFakeLister(headscale.NodeView{ID: "2", Hostname: "workpc"})
	hs.addErr = errors.New(`api: headscale POST /api/v1/node/2/tags: 400 {"code":3,"message":"requested tags [tag:dev-daniil-workpc] are invalid or not permitted"}`)
	hs.addFailN = 5
	rows := []db.NodeOwner{{NodeID: "2", Hostname: "workpc", Username: "daniil", Tag: "tag:dev-daniil-workpc"}}

	res := ReconcileTags(&fakeDBSource{}, hs, hs.nodes, rows, "ts.example.com", nil)

	if res.Failed != 1 || res.Applied != 0 {
		t.Fatalf("result = %+v, want failed=1 applied=0", res)
	}
	if hs.addCalls != 1 {
		t.Errorf("AddTag calls = %d, want 1 — a permission refusal must not be retried", hs.addCalls)
	}
}

// TestB2727_BatchFailureStillReportsPerTag: when the single write fails — e.g.
// the policy is not writable at all, the pre-v1.5.16 `aro` state — the pass
// must still report the failure per device through the alert sink, so the
// metric reason names the fix instead of the failure disappearing into one
// batch error. The per-tag path stays as the fallback.
func TestB2727_BatchFailureStillReportsPerTag(t *testing.T) {
	hs := newFakeLister(
		headscale.NodeView{ID: "2", Hostname: "workpc", UserID: "1"},
		headscale.NodeView{ID: "3", Hostname: "laptop", UserID: "1"},
	)
	hs.ownerErr = errors.New("ensure-tag-owners: set policy: api: headscale PUT /api/v1/policy: 500 update is disabled for modes other than database")
	rows := []db.NodeOwner{
		{NodeID: "2", Hostname: "workpc", Username: "daniil", Tag: "tag:dev-daniil-workpc"},
		{NodeID: "3", Hostname: "laptop", Username: "daniil", Tag: "tag:dev-daniil-laptop"},
	}

	res := ReconcileTags(&fakeDBSource{}, hs, hs.nodes, rows, "ts.example.com", nil)

	if res.Failed != 2 || res.Applied != 0 {
		t.Fatalf("result = %+v, want failed=2 applied=0 (each device is reported, not just the batch)", res)
	}
	if hs.ownerBatches != 1 {
		t.Errorf("EnsureTagOwners calls = %d, want 1 (the batch is still tried first)", hs.ownerBatches)
	}
	if hs.ownerCalls != 2 {
		t.Errorf("per-tag EnsureTagOwner calls = %d, want 2 (fallback path after the batch failed)", hs.ownerCalls)
	}
	if len(hs.tagged) != 0 {
		t.Error("AddTag must not be attempted when the policy update failed — headscale would reject it anyway")
	}
}
