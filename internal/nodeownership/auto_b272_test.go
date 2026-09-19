// B272 (2026-09-19) — the tag reconciler must repair database→headscale drift.
//
// Live case: `node_owner_map` said `tag:dev-daniil-workpc` while headscale had
// no tags on that node. Because B77's Backfill only walks nodes that ALREADY
// carry a dev-tag in headscale, the node was skipped on every 5-minute tick —
// the drift was permanent and completely silent (no audit row, no metric).
package nodeownership

import (
	"errors"
	"testing"

	"skygate/internal/db"
	"skygate/internal/headscale"
)

// fakeLister records AddTag/UntagNode/EnsureTagOwner calls and can be told to
// fail them.
type fakeLister struct {
	nodes     []headscale.NodeView
	tagged    map[int64]string
	addErr    error
	listErr   error
	untagCall int
	// tagOwners records EnsureTagOwner calls (tag → owners); ownerErr forces
	// the policy update to fail (B272.1).
	tagOwners map[string][]string
	ownerErr  error
}

func newFakeLister(nodes ...headscale.NodeView) *fakeLister {
	return &fakeLister{nodes: nodes, tagged: map[int64]string{}, tagOwners: map[string][]string{}}
}

func (f *fakeLister) InvalidateCache()                            {}
func (f *fakeLister) ListAllNodes() ([]headscale.NodeView, error) { return f.nodes, f.listErr }
func (f *fakeLister) UntagNode(int64, string) error               { f.untagCall++; return nil }
func (f *fakeLister) EnsureTagOwner(tag string, owners []string) error {
	if f.ownerErr != nil {
		return f.ownerErr
	}
	f.tagOwners[tag] = owners
	return nil
}
func (f *fakeLister) AddTag(nodeID int64, tag string) error {
	if f.addErr != nil {
		return f.addErr
	}
	f.tagged[nodeID] = tag
	return nil
}

// fakeDBSource (shared with auto_b227_test.go) is the minimal DBSource the
// reconciler needs — it only nil-checks the source; the owner rows are passed
// in explicitly so this package's tests need no database.

func TestB272_ReconcileAppliesMissingTag(t *testing.T) {
	hs := newFakeLister(headscale.NodeView{ID: "2", Hostname: "workpc", UserID: "1"})
	rows := []db.NodeOwner{{NodeID: "2", Username: "daniil", Tag: "tag:dev-daniil-workpc"}}

	res := ReconcileTags(&fakeDBSource{}, hs, hs.nodes, rows, "ts.example.com", nil)

	if res.Checked != 1 || res.Applied != 1 || res.Failed != 0 {
		t.Fatalf("result = %+v, want checked=1 applied=1 failed=0", res)
	}
	if hs.tagged[2] != "tag:dev-daniil-workpc" {
		t.Fatalf("AddTag was not called with the database tag (got %v)", hs.tagged)
	}
}

func TestB272_ReconcileIsIdempotent(t *testing.T) {
	// The node already carries the tag: no write at all (the autoupdater runs
	// every 5 minutes — a write per tick would hammer headscale).
	hs := newFakeLister(headscale.NodeView{ID: "2", Hostname: "workpc", Tags: []string{"tag:dev-daniil-workpc"}})
	rows := []db.NodeOwner{{NodeID: "2", Username: "daniil", Tag: "tag:dev-daniil-workpc"}}

	res := ReconcileTags(&fakeDBSource{}, hs, hs.nodes, rows, "ts.example.com", nil)

	if res.Applied != 0 || res.Failed != 0 {
		t.Fatalf("result = %+v, want no writes for an already-tagged node", res)
	}
	if len(hs.tagged) != 0 {
		t.Fatalf("AddTag was called for a tag that is already present: %v", hs.tagged)
	}
}

func TestB272_ReconcileReportsFailureWithReason(t *testing.T) {
	hs := newFakeLister(headscale.NodeView{ID: "2", Hostname: "workpc"})
	hs.addErr = errors.New("rpc error: code = InvalidArgument desc = requested tags are invalid or not permitted")
	rows := []db.NodeOwner{{NodeID: "2", Username: "daniil", Tag: "tag:dev-daniil-workpc"}}

	res := ReconcileTags(&fakeDBSource{}, hs, hs.nodes, rows, "ts.example.com", nil)

	if res.Failed != 1 || res.Applied != 0 {
		t.Fatalf("result = %+v, want failed=1 applied=0", res)
	}
	// The failure is classified so the operator learns whether to fix the
	// policy or just retry.
	if got := ClassifyFailure(hs.addErr); got != ReasonACLReject {
		t.Errorf("ClassifyFailure = %q, want acl_reject", got)
	}
}

func TestB272_ReconcileCountsMissingNodes(t *testing.T) {
	// The database row points at a node headscale no longer has: the row is
	// GC'd by the per-user pass, so the reconciler must not report a failure.
	hs := newFakeLister()
	rows := []db.NodeOwner{{NodeID: "99", Username: "daniil", Tag: "tag:dev-daniil-gone"}}

	res := ReconcileTags(&fakeDBSource{}, hs, hs.nodes, rows, "ts.example.com", nil)

	if res.Missing != 1 || res.Failed != 0 || res.Applied != 0 {
		t.Fatalf("result = %+v, want missing=1 and no writes", res)
	}
}

func TestB272_ReconcileFlagsUntaggedUnattributedNode(t *testing.T) {
	// node 3 belongs to nobody: no tags, no row. Before B272 this produced no
	// signal whatsoever, so the device had no per-device rule forever.
	hs := newFakeLister(
		headscale.NodeView{ID: "2", Hostname: "workpc", Tags: []string{"tag:dev-daniil-workpc"}},
		headscale.NodeView{ID: "3", Hostname: "laptop"},
	)
	rows := []db.NodeOwner{{NodeID: "2", Username: "daniil", Tag: "tag:dev-daniil-workpc"}}

	res := ReconcileTags(&fakeDBSource{}, hs, hs.nodes, rows, "ts.example.com", nil)

	if res.Unattrib != 1 {
		t.Fatalf("result = %+v, want unattributed=1 (node 3)", res)
	}
	if got := ClassifyFailure(ErrNoStrategyMatch); got != ReasonNoStrategy {
		t.Errorf("ClassifyFailure(ErrNoStrategyMatch) = %q, want no_strategy", got)
	}
	if got := ClassifyFailure(ErrTagNotInHeadscale); got != ReasonTagMissing {
		t.Errorf("ClassifyFailure(ErrTagNotInHeadscale) = %q, want tag_missing", got)
	}
}

// TestB272_ReconcileEnsuresTagOwnerFirst is the live follow-up (B272.1): on the
// operator's host the reconciler found the right node and the right tag, called
// AddTag, and headscale answered
//
//	400 {"code":3, "message":"requested tags [tag:dev-daniil-workpc] are invalid or not permitted"}
//
// because the tag had no entry in the policy's tagOwners. The pass must create
// that entry from the database row BEFORE asking headscale to apply the tag.
func TestB272_ReconcileEnsuresTagOwnerFirst(t *testing.T) {
	hs := newFakeLister(headscale.NodeView{ID: "2", Hostname: "workpc", UserID: "1"})
	rows := []db.NodeOwner{{NodeID: "2", Username: "daniil", Tag: "tag:dev-daniil-workpc"}}

	res := ReconcileTags(&fakeDBSource{}, hs, hs.nodes, rows, "ts.example.com", nil)

	if res.Applied != 1 || res.Failed != 0 {
		t.Fatalf("result = %+v, want applied=1 failed=0", res)
	}
	owners, ok := hs.tagOwners["tag:dev-daniil-workpc"]
	if !ok {
		t.Fatal("EnsureTagOwner was never called — headscale would reject the tag with 'are invalid or not permitted'")
	}
	if len(owners) != 2 || owners[0] != "daniil@ts.example.com" || owners[1] != "tagged-devices@ts.example.com" {
		t.Errorf("owners = %v, want [daniil@ts.example.com tagged-devices@ts.example.com]", owners)
	}
}

// TestB272_ReconcileReportsOwnerFailure: if the policy cannot be updated (e.g.
// the service user cannot write the file), the failure must be reported with
// the reason instead of retrying AddTag blindly every tick.
func TestB272_ReconcileReportsOwnerFailure(t *testing.T) {
	hs := newFakeLister(headscale.NodeView{ID: "2", Hostname: "workpc"})
	hs.ownerErr = errors.New("api: headscale PUT /api/v1/policy: 500 update is disabled for modes other than database")
	rows := []db.NodeOwner{{NodeID: "2", Username: "daniil", Tag: "tag:dev-daniil-workpc"}}

	res := ReconcileTags(&fakeDBSource{}, hs, hs.nodes, rows, "ts.example.com", nil)

	if res.Failed != 1 || res.Applied != 0 {
		t.Fatalf("result = %+v, want failed=1 applied=0", res)
	}
	if len(hs.tagged) != 0 {
		t.Error("AddTag must not be attempted when the policy update failed — headscale would reject it anyway")
	}
	// The 500 body is neither an ACL reject nor one of the transient gRPC code
	// names, so it lands in `unknown` — deliberately: the raw text (which names
	// the headscale policy mode) is the actionable part, and pretending it is a
	// retryable rpc_error would hide that from the operator.
	if got := ClassifyFailure(hs.ownerErr); got != ReasonUnknown {
		t.Errorf("ClassifyFailure = %q, want unknown (the raw 500 text is what the operator needs)", got)
	}
}

// TestB272_ReconcileMissingBaseDomainIsReported: without the base domain the
// owner cannot be expressed, and the tag can never become permitted. Say so.
func TestB272_ReconcileMissingBaseDomainIsReported(t *testing.T) {
	hs := newFakeLister(headscale.NodeView{ID: "2", Hostname: "workpc"})
	rows := []db.NodeOwner{{NodeID: "2", Username: "daniil", Tag: "tag:dev-daniil-workpc"}}

	res := ReconcileTags(&fakeDBSource{}, hs, hs.nodes, rows, "", nil)

	if res.Failed != 1 {
		t.Fatalf("result = %+v, want failed=1 when SKYGATE_BASE_DOMAIN is unset", res)
	}
	if len(hs.tagOwners) != 0 {
		t.Error("EnsureTagOwner must not be called without a base domain")
	}
}

func TestB272_ReconcileSkipsTaggedNodesAndNilClients(t *testing.T) {
	// A tagged node is not "unattributed" even without a database row — the
	// synthetic tagged-devices pool is legitimate.
	hs := newFakeLister(headscale.NodeView{ID: "1", Hostname: "exit-node-vps", Tags: []string{"tag:exit"}})
	res := ReconcileTags(&fakeDBSource{}, hs, hs.nodes, nil, "ts.example.com", nil)
	if res.Unattrib != 0 {
		t.Fatalf("result = %+v, want unattributed=0 for a tagged node", res)
	}

	// Defensive guards: no client / no DB source must be a no-op, not a panic.
	if got := ReconcileTags(&fakeDBSource{}, nil, hs.nodes, nil, "ts.example.com", nil); got.Checked != 0 {
		t.Errorf("nil lister: result = %+v, want a no-op", got)
	}
	if got := ReconcileTags(nil, hs, hs.nodes, nil, "ts.example.com", nil); got.Checked != 0 {
		t.Errorf("nil DB source: result = %+v, want a no-op", got)
	}
}
