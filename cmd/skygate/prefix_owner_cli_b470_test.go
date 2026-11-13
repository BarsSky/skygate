// cmd/skygate/prefix_owner_cli_b470_test.go — B470 (2026-11-14).
//
// THE CLI MUST PUSH THE SCHEMA THE INSTALLATION ACTUALLY RUNS.
//
// The first live run of `skygate prefix-owner --apply` pinned 21 prefixes and then failed the ACL
// push, because the CLI was started from a bare shell with no SKYGATE_ACL_VIA_ENABLED and therefore
// generated the legacy `acls[]` policy for a deployment that serves `grants[]`+`via`:
//
//	headscale PUT /api/v1/policy: 500 … cannot unmarshal JSON string into Go v2.AliasWithPorts
//	within "/acls/5/dst/0": dst="tag:dev-skyadmin-cyborg": invalid port range format
//
// Making that legacy policy VALID (the generator fix in internal/acl) would have turned the failure
// into something worse: it would apply, and every `via` pin would be gone until the app's own next
// pass. So the CLI asks headscale which schema it is serving and regenerates THAT one.
package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"skygate/internal/headscale"
)

// b470FakeHeadscale serves GET /api/v1/policy with the envelope headscale uses.
func b470FakeHeadscale(t *testing.T, policyJSON string) *headscale.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/policy" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"policy":` + policyJSON + `}`))
	}))
	t.Cleanup(srv.Close)
	return headscale.New(srv.URL, "test-key")
}

// TestB470_LivePolicyUsesVia — the two schemas a real installation can be serving, the stringified
// `policy` form some hosts answer, and the unparsable fallback that must not answer "legacy" by
// accident.
func TestB470_LivePolicyUsesVia(t *testing.T) {
	cases := []struct {
		name      string
		policy    string
		wantVia   bool
		wantWhyIn string
	}{
		{
			name:      "grants[] is the via-enabled schema",
			policy:    `{"grants":[{"src":["tag:dev-a"],"dst":["autogroup:internet"],"ip":["*"],"via":["tag:dev-infra-karolina"]}],"via":["tag:dev-infra-karolina"]}`,
			wantVia:   true,
			wantWhyIn: "grants[] with 1 grant(s)",
		},
		{
			name:      "acls[] is the legacy schema",
			policy:    `{"acls":[{"src":["tag:dev-a"],"dst":["autogroup:internet:*"]}],"ip":["*"]}`,
			wantVia:   false,
			wantWhyIn: "acls[] with 1 rule(s)",
		},
		{
			// headscale 0.29 on the native host answers the policy as a JSON STRING;
			// GetACL unquotes it, so the detector sees a document either way. This case
			// pins that the CLI does not depend on the envelope.
			name:      "stringified policy document",
			policy:    `"{\"grants\":[{\"src\":[\"tag:dev-a\"],\"dst\":[\"autogroup:internet\"]}],\"via\":[]}"`,
			wantVia:   true,
			wantWhyIn: "grants[] with 1 grant(s)",
		},
		{
			// An empty grants[] means a policy that grants nothing — treat it as legacy so the
			// CLI can still regenerate something usable, and say so rather than claiming "via".
			name:      "empty grants[] falls back to acls[]",
			policy:    `{"grants":[],"acls":[{"src":["*"],"dst":["*:*"]}]}`,
			wantVia:   false,
			wantWhyIn: "acls[] with 1 rule(s)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hs := b470FakeHeadscale(t, tc.policy)
			got, why, err := livePolicyUsesVia(hs)
			if err != nil {
				t.Fatalf("livePolicyUsesVia: %v", err)
			}
			if got != tc.wantVia {
				t.Errorf("useVia = %v, want %v (%s)", got, tc.wantVia, why)
			}
			if !strings.Contains(why, tc.wantWhyIn) {
				t.Errorf("reason %q does not mention %q — the operator has to be able to read WHY "+
					"the CLI chose a schema", why, tc.wantWhyIn)
			}
		})
	}
}

// TestB470_LivePolicyUsesVia_ErrorIsReported — an unreachable headscale must surface as an error so
// reapplyACL can fall back to the configured schema AND say so, instead of silently deciding.
func TestB470_LivePolicyUsesVia_ErrorIsReported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"code":2,"message":"boom"}`))
	}))
	defer srv.Close()
	hs := headscale.New(srv.URL, "test-key")
	// GetACL has docker-CLI fallbacks after the REST failure; on a machine with a live
	// headscale container it may answer something. Either outcome is acceptable here — what
	// must NOT happen is a panic or a silent "false".
	useVia, why, err := livePolicyUsesVia(hs)
	if err == nil && why == "" {
		t.Fatalf("no error and no reason (useVia=%v) — the caller cannot explain its choice", useVia)
	}
}
