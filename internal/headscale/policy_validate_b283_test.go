// internal/headscale/policy_validate_b283_test.go — B283 (2026-09-22).
//
// Live case: the generated policy referenced `tag:dev-infra-exit-node-vps` in
// 19 per-CIDR `via` pins while `tagOwners` did not declare it. The document was
// valid JSON, and headscale still refused to start on it — the tailnet lost its
// control plane and every device disappeared from the portal until an older
// snapshot was restored by hand.
package headscale

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestUndeclaredTagsB283 pins the invariant headscale itself enforces.
func TestUndeclaredTagsB283(t *testing.T) {
	cases := []struct {
		name    string
		policy  string
		want    []string
		wantErr bool
	}{
		{
			name:   "all referenced tags declared",
			policy: `{"grants":[{"src":["tag:a"],"dst":["tag:b"],"via":["tag:c"]}],"tagOwners":{"tag:a":["u@d"],"tag:b":["u@d"],"tag:c":["u@d"]}}`,
		},
		{
			name:   "via pin missing from tagOwners (the live incident)",
			policy: `{"grants":[{"src":["tag:a"],"dst":["h-rule"],"via":["tag:dev-infra-exit-node-vps"]}],"tagOwners":{"tag:a":["u@d"]}}`,
			want:   []string{"tag:dev-infra-exit-node-vps"},
		},
		{
			name:   "src and dst tags missing too",
			policy: `{"grants":[{"src":["tag:a"],"dst":["tag:b"],"via":["tag:c"]}],"tagOwners":{"tag:a":["u@d"]}}`,
			want:   []string{"tag:b", "tag:c"},
		},
		{
			name:   "port wildcard is the same tag",
			policy: `{"grants":[{"src":["tag:a"],"dst":["tag:exit-node:*"]}],"tagOwners":{"tag:a":["u@d"]}}`,
			want:   []string{"tag:exit-node"},
		},
		{
			name:   "non-tag selectors are ignored",
			policy: `{"grants":[{"src":["*","autogroup:internet","daniil@example.com","100.64.0.2"],"dst":["autogroup:internet"]}],"tagOwners":{}}`,
		},
		{
			name:   "legacy acls array is checked too",
			policy: `{"acls":[{"action":"accept","src":["tag:legacy"],"dst":["*:*"]}],"tagOwners":{}}`,
			want:   []string{"tag:legacy"},
		},
		{
			name:   "HuJSON with comments still parses",
			policy: "{\n  // operator comment\n  \"grants\": [{\"src\": [\"tag:a\"], \"dst\": [\"*:*\"]}],\n  \"tagOwners\": {\"tag:a\": [\"u@d\"]},\n}",
		},
		{
			name:    "garbage is an error",
			policy:  `{"grants": [`,
			wantErr: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := UndeclaredTags(c.policy)
			if c.wantErr {
				if err == nil {
					t.Fatalf("UndeclaredTags(%q) = %v, want an error", c.policy, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("UndeclaredTags(%q): %v", c.policy, err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("UndeclaredTags = %v, want %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("UndeclaredTags[%d] = %q, want %q", i, got[i], c.want[i])
				}
			}
		})
	}
}

// TestSetPolicyRefusesUndeclaredTagsB283 is the production guard: skygate must
// not hand headscale a document it will refuse to load. The fake server returns
// 200 for every PUT, so a missing guard would make the call SUCCEED — the hit
// counter is what proves the refusal happened inside skygate.
func TestSetPolicyRefusesUndeclaredTagsB283(t *testing.T) {
	var puts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && r.URL.Path == "/api/v1/policy" {
			atomic.AddInt32(&puts, 1)
			_, _ = w.Write([]byte(`{"policy":"ok"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	c := New(srv.URL, "fake-key")

	// 1. A policy with an undeclared `via` tag must be refused, with the tag
	//    named in the error.
	err := c.SetPolicy(`{"grants":[{"src":["tag:a"],"dst":["h-rule"],"via":["tag:dev-infra-exit-node-vps"]}],"tagOwners":{"tag:a":["u@d"]}}`)
	if err == nil {
		t.Fatal("SetPolicy accepted a policy with a via tag missing from tagOwners — headscale would crash-loop on it")
	}
	if !strings.Contains(err.Error(), "tag:dev-infra-exit-node-vps") {
		t.Errorf("error does not name the offending tag: %v", err)
	}
	if n := atomic.LoadInt32(&puts); n != 0 {
		t.Errorf("the bad policy reached headscale (%d PUT(s)) — it must be refused before the request", n)
	}

	// 2. A malformed document is refused the same way.
	if err := c.SetPolicy(`{"grants": [`); err == nil {
		t.Fatal("SetPolicy accepted a document that does not parse")
	}
	if n := atomic.LoadInt32(&puts); n != 0 {
		t.Errorf("the malformed policy reached headscale (%d PUT(s))", n)
	}

	// 3. A healthy policy still goes through — the guard must not be
	//    over-broad (this is the regression that would break every apply).
	if err := c.SetPolicy(`{"grants":[{"src":["tag:a"],"dst":["h-rule"],"via":["tag:b"]}],"tagOwners":{"tag:a":["u@d"],"tag:b":["u@d"]}}`); err != nil {
		t.Fatalf("SetPolicy refused a valid policy: %v", err)
	}
	if n := atomic.LoadInt32(&puts); n != 1 {
		t.Errorf("valid policy PUT count = %d, want 1", n)
	}
}
