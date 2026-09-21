// B272.4 — the policy-helper handoff must survive the headscale restart it
// causes, and must NOT retry an operator problem.
//
// Live on the native host aro the reconciler lost a whole 5-minute tick twice
// with `ensure-tag-owner: get ACL: api: Get http://127.0.0.1:8081/api/v1/policy:
// dial tcp 127.0.0.1:8081: connect: connection refused` — the privileged applier
// restarts headscale after writing the policy, so the next call can hit a daemon
// that is still coming up. Each lost tick is five minutes of a device without its
// tag.
package nodeownership

import (
	"errors"
	"sort"
	"testing"
	"time"

	"skygate/internal/db"
	"skygate/internal/headscale"
)

// flakyTagOwner is a minimal nodeLister whose EnsureTagOwner fails `failN` times
// with `fail` before succeeding.
type flakyTagOwner struct {
	calls int
	failN int
	fail  error
}

func (f *flakyTagOwner) InvalidateCache()                            {}
func (f *flakyTagOwner) ListAllNodes() ([]headscale.NodeView, error) { return nil, nil }
func (f *flakyTagOwner) AddTag(int64, string) error                  { return nil }
func (f *flakyTagOwner) UntagNode(int64, string) error               { return nil }
func (f *flakyTagOwner) EnsureTagOwner(string, []string) error {
	f.calls++
	if f.calls <= f.failN {
		return f.fail
	}
	return nil
}

// EnsureTagOwners delegates to the per-tag recorder so the counters above keep
// describing policy writes exactly (B272.4 added the batch form to nodeLister).
func (f *flakyTagOwner) EnsureTagOwners(wants map[string][]string) error {
	tags := make([]string, 0, len(wants))
	for tag := range wants {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	for _, tag := range tags {
		if err := f.EnsureTagOwner(tag, wants[tag]); err != nil {
			return err
		}
	}
	return nil
}

func TestB2724_TransientHeadscaleDownIsRetried(t *testing.T) {
	hs := &flakyTagOwner{
		failN: 1,
		fail:  errors.New("ensure-tag-owner: get ACL: api: Get \"http://127.0.0.1:8081/api/v1/policy\": dial tcp 127.0.0.1:8081: connect: connection refused"),
	}
	row := db.NodeOwner{NodeID: "2", Hostname: "workpc", Username: "daniil", Tag: "tag:dev-daniil-workpc"}
	start := time.Now()
	err := ensureTagIsPermittedRetry(hs, row, "ts.example.com", map[string]bool{})
	if err != nil {
		t.Fatalf("err = %v, want nil (the refused connection is transient by construction)", err)
	}
	if hs.calls != 2 {
		t.Errorf("EnsureTagOwner calls = %d, want 2 (one refusal + one retry)", hs.calls)
	}
	if elapsed := time.Since(start); elapsed < 500*time.Millisecond {
		t.Errorf("retry happened after %v — expected the first backoff to be about a second", elapsed)
	}
}

func TestB2724_PermissionRefusalIsNotRetried(t *testing.T) {
	hs := &flakyTagOwner{
		failN: 3,
		fail:  errors.New("api: headscale POST /api/v1/node/3/tags: 400 {\"code\":3,\"message\":\"requested tags [tag:dev-daniil-laptop] are invalid or not permitted\"}"),
	}
	row := db.NodeOwner{NodeID: "3", Hostname: "laptop", Username: "daniil", Tag: "tag:dev-daniil-laptop"}
	err := ensureTagIsPermittedRetry(hs, row, "ts.example.com", map[string]bool{})
	if err == nil {
		t.Fatal("err = nil, want the refusal to surface (an operator problem must not be retried away)")
	}
	if hs.calls != 1 {
		t.Errorf("EnsureTagOwner calls = %d, want 1 — a permission refusal must not be retried", hs.calls)
	}
}

func TestB2724_IsTransientHeadscaleDown(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"dial tcp 127.0.0.1:8081: connect: connection refused", true},
		{"read tcp: connection reset by peer", true},
		{"Get \"http://x\": i/o timeout", true},
		{"context deadline exceeded", true},
		{"400 requested tags [tag:x] are invalid or not permitted", false},
		{"SKYGATE_BASE_DOMAIN is not set", false},
	}
	for _, c := range cases {
		if got := isTransientHeadscaleDown(errors.New(c.msg)); got != c.want {
			t.Errorf("isTransientHeadscaleDown(%q) = %v, want %v", c.msg, got, c.want)
		}
	}
	if isTransientHeadscaleDown(nil) {
		t.Error("isTransientHeadscaleDown(nil) = true, want false")
	}
}
