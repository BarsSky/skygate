// B272.7 (v1.5.35) — EnsureTagOwners: N missing tags, ONE policy write.
//
// The B245/B251 tests (tags_test.go, tags_b251_test.go) pin the single-tag
// contract. This file pins the batch contract, which is what the reconciler
// actually uses since the live `aro` case where three devices needed three
// dev-tags, the per-device read-modify-write raced the asynchronous policy
// applier, and only the first tag survived (`applied=1 failed=2`).
package headscale

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// policyStub is a minimal headscale policy endpoint: GET returns the current
// policy (in headscale 0.29's `{"policy": <object>}` shape, which is what the
// live API returns), PUT records the write and replaces it.
type policyStub struct {
	mu     sync.Mutex
	policy map[string]interface{}
	puts   int
}

func newPolicyStub(tagOwners map[string]interface{}) (*policyStub, *httptest.Server) {
	st := &policyStub{policy: map[string]interface{}{
		"acls":      []interface{}{},
		"tagOwners": tagOwners,
	}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st.mu.Lock()
		defer st.mu.Unlock()
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v1/policy":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"policy": st.policy})
		case r.Method == "PUT" && r.URL.Path == "/api/v1/policy":
			var pb struct {
				Policy json.RawMessage `json:"policy"`
			}
			if err := json.NewDecoder(r.Body).Decode(&pb); err != nil {
				http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
				return
			}
			raw := bytes.TrimSpace(pb.Policy)
			next := map[string]interface{}{}
			if len(raw) > 0 && raw[0] == '"' {
				var s string
				if err := json.Unmarshal(raw, &s); err != nil {
					http.Error(w, "bad stringified policy: "+err.Error(), http.StatusBadRequest)
					return
				}
				if err := json.Unmarshal([]byte(s), &next); err != nil {
					http.Error(w, "bad policy hujson: "+err.Error(), http.StatusBadRequest)
					return
				}
			} else if err := json.Unmarshal(raw, &next); err != nil {
				http.Error(w, "bad policy json: "+err.Error(), http.StatusBadRequest)
				return
			}
			st.puts++
			st.policy = next
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"policy":"ok"}`))
		default:
			http.Error(w, "unexpected: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	return st, srv
}

func (st *policyStub) tagOwners() map[string]interface{} {
	st.mu.Lock()
	defer st.mu.Unlock()
	to, _ := st.policy["tagOwners"].(map[string]interface{})
	return to
}

// TestEnsureTagOwners_OneWriteForManyTags is the regression test for the live
// `aro` symptom: three missing tags used to cost three read-modify-write cycles
// against an applier that had not caught up, so writes 2 and 3 were computed
// from a snapshot that did not contain write 1's result.
func TestEnsureTagOwners_OneWriteForManyTags(t *testing.T) {
	st, srv := newPolicyStub(map[string]interface{}{})
	defer srv.Close()
	c := New(srv.URL, "fake-token")

	wants := map[string][]string{
		"tag:dev-daniil-workpc": {"daniil@ts.example.com", "tagged-devices@ts.example.com"},
		"tag:dev-daniil-laptop": {"daniil@ts.example.com", "tagged-devices@ts.example.com"},
		"tag:dev-daniil-homepc": {"daniil@ts.example.com", "tagged-devices@ts.example.com"},
	}
	if err := c.EnsureTagOwners(wants); err != nil {
		t.Fatalf("EnsureTagOwners: %v", err)
	}
	if st.puts != 1 {
		t.Fatalf("PUT count = %d, want 1 (three tags, one policy write)", st.puts)
	}
	to := st.tagOwners()
	for tag := range wants {
		if _, ok := to[tag]; !ok {
			t.Errorf("tagOwners missing %s after the batch write; got %v", tag, to)
		}
	}

	// Idempotent: everything is present now, so no further write at all.
	if err := c.EnsureTagOwners(wants); err != nil {
		t.Fatalf("EnsureTagOwners (2nd call): %v", err)
	}
	if st.puts != 1 {
		t.Errorf("PUT count = %d after the idempotent call, want 1", st.puts)
	}
}

// TestEnsureTagOwners_PreservesExistingEntries: the batch must never widen or
// rewrite an entry the operator configured, and must not drop the tags that
// were already in the policy while adding the new ones.
func TestEnsureTagOwners_PreservesExistingEntries(t *testing.T) {
	st, srv := newPolicyStub(map[string]interface{}{
		"tag:dev-skyadmin-emilia": []interface{}{"skyadmin@ts.example.com"},
	})
	defer srv.Close()
	c := New(srv.URL, "fake-token")

	err := c.EnsureTagOwners(map[string][]string{
		"tag:dev-skyadmin-emilia": {"someone-else@ts.example.com"}, // must be preserved as-is
		"tag:dev-skyadmin-karolina": {
			"skyadmin@ts.example.com", "tagged-devices@ts.example.com",
		},
	})
	if err != nil {
		t.Fatalf("EnsureTagOwners: %v", err)
	}
	if st.puts != 1 {
		t.Fatalf("PUT count = %d, want 1 (only the new tag required a write)", st.puts)
	}
	to := st.tagOwners()
	existing, ok := to["tag:dev-skyadmin-emilia"].([]interface{})
	if !ok || len(existing) != 1 || existing[0] != "skyadmin@ts.example.com" {
		t.Errorf("pre-existing tag:dev-skyadmin-emilia was rewritten: %v", to["tag:dev-skyadmin-emilia"])
	}
	if _, ok := to["tag:dev-skyadmin-karolina"]; !ok {
		t.Errorf("new tag was not added; got %v", to)
	}
}

// TestEnsureTagOwners_EmptyRequestIsNotAWrite: the reconciler calls the batch
// unconditionally, including on a pass where every tag already landed. An empty
// request must be a nil no-op — on a file-mode host a needless write restarts
// headscale.
func TestEnsureTagOwners_EmptyRequestIsNotAWrite(t *testing.T) {
	st, srv := newPolicyStub(map[string]interface{}{})
	defer srv.Close()
	c := New(srv.URL, "fake-token")

	if err := c.EnsureTagOwners(nil); err != nil {
		t.Errorf("EnsureTagOwners(nil) = %v, want nil", err)
	}
	if err := c.EnsureTagOwners(map[string][]string{}); err != nil {
		t.Errorf("EnsureTagOwners(empty) = %v, want nil", err)
	}
	if st.puts != 0 {
		t.Errorf("PUT count = %d, want 0 (nothing to permit)", st.puts)
	}
}

// TestEnsureTagOwners_ValidatesBeforeWriting: a malformed entry must be
// rejected before ANY write, so one bad tag cannot take a whole batch down with
// it half-applied.
func TestEnsureTagOwners_ValidatesBeforeWriting(t *testing.T) {
	st, srv := newPolicyStub(map[string]interface{}{})
	defer srv.Close()
	c := New(srv.URL, "fake-token")

	if err := c.EnsureTagOwners(map[string][]string{
		"tag:dev-good":    {"daniil@ts.example.com"},
		"tag:dev-noowner": {},
	}); err == nil {
		t.Error("EnsureTagOwners with an empty owner list: got nil, want error")
	}
	if err := c.EnsureTagOwners(map[string][]string{"": {"daniil@ts.example.com"}}); err == nil {
		t.Error("EnsureTagOwners with an empty tag: got nil, want error")
	}
	var nilC *Client
	if err := nilC.EnsureTagOwners(map[string][]string{"tag:x": {"a@b"}}); err == nil {
		t.Error("EnsureTagOwners on a nil client: got nil, want error")
	}
	if st.puts != 0 {
		t.Errorf("PUT count = %d, want 0 — validation happens before the write", st.puts)
	}
}

// TestEnsureTagOwner_DelegatesToBatch keeps the single-tag entry point (used by
// the adopt/transfer admin paths) on exactly one code path, so both can never
// disagree about how a tagOwners entry is written.
func TestEnsureTagOwner_DelegatesToBatch(t *testing.T) {
	st, srv := newPolicyStub(map[string]interface{}{})
	defer srv.Close()
	c := New(srv.URL, "fake-token")

	if err := c.EnsureTagOwner("tag:dev-daniil-workpc", []string{"daniil@ts.example.com"}); err != nil {
		t.Fatalf("EnsureTagOwner: %v", err)
	}
	if st.puts != 1 {
		t.Fatalf("PUT count = %d, want 1", st.puts)
	}
	if _, ok := st.tagOwners()["tag:dev-daniil-workpc"]; !ok {
		t.Error("tag:dev-daniil-workpc was not added")
	}
}
