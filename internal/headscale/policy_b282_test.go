// internal/headscale/policy_b282_test.go — B282 (2026-09-22).
//
// Live case: the native `aro` host's headscale answers
// GET /api/v1/policy with a STRINGIFIED policy
// (`{"policy":"{…escaped…}"}`). GetACL cached the quoted literal, so
// /admin/headscale/acl rendered
//
//	500 list acl: unmarshal policy: json: cannot unmarshal string into
//	    Go value of type admin.ACLView
//
// and the exit_rules.all_in_headscale_acl system test died on the same
// json.Unmarshal. PolicyJSON is the one normaliser for all four shapes
// (object, stringified, HuJSON with comments, HuJSON with trailing
// commas).
package headscale

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestPolicyJSONShapesB282 pins every shape the policy API is known to
// hand back, plus the two failure modes that must stay errors.
func TestPolicyJSONShapesB282(t *testing.T) {
	huJSON := `{
		// the operator's policy.hujson carries comments
		"acls": [
			{ "action": "accept", "src": ["*"], "dst": ["*:*"] },
		],
	}`
	stringified := `"{\"acls\":[{\"action\":\"accept\",\"src\":[\"*\"],\"dst\":[\"*:*\"]}]}"`

	cases := []struct {
		name    string
		in      string
		wantErr bool
		wantACL int
	}{
		{"json object passes through", `{"acls":[{"action":"accept","src":["*"],"dst":["*:*"]}]}`, false, 1},
		{"stringified policy is unquoted", stringified, false, 1},
		{"HuJSON comments + trailing comma are standardised", huJSON, false, 1},
		{"empty object is still a policy", `{}`, false, 0},
		{"garbage is an error", `policy: {`, true, 0},
		{"bare prose is an error", `not json at all`, true, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := PolicyJSON(c.in)
			if c.wantErr {
				if err == nil {
					t.Fatalf("PolicyJSON(%q) = %s, want an error", c.in, out)
				}
				return
			}
			if err != nil {
				t.Fatalf("PolicyJSON(%q): %v", c.in, err)
			}
			var decoded struct {
				ACLs []struct {
					Action string `json:"action"`
				} `json:"acls"`
			}
			if err := json.Unmarshal(out, &decoded); err != nil {
				t.Fatalf("output is not strict JSON (%v): %s", err, out)
			}
			if len(decoded.ACLs) != c.wantACL {
				t.Errorf("acls = %d, want %d (out=%s)", len(decoded.ACLs), c.wantACL, out)
			}
		})
	}
}

// TestGetACLUnquotesStringifiedPolicyB282 is the regression that
// reproduces the live 500 at its source: GetACL itself must hand the
// caller the policy DOCUMENT, not a JSON string literal.
func TestGetACLUnquotesStringifiedPolicyB282(t *testing.T) {
	_, c, _ := fakeACLHS(t, http.StatusOK, `{"policy":"{\"acls\":[{\"action\":\"accept\"}]}","data":""}`)
	got, err := c.GetACL()
	if err != nil {
		t.Fatalf("GetACL: %v", err)
	}
	if got == "" {
		t.Fatal("GetACL returned an empty policy")
	}
	// The pre-B282 behaviour: the quoted literal was cached verbatim, so
	// every consumer's json.Unmarshal failed with "cannot unmarshal
	// string into …".
	if got[0] == '"' {
		t.Fatalf("GetACL returned the QUOTED policy %q — consumers unmarshalling it fail with "+
			"`cannot unmarshal string` (the live B282 ACL-page 500)", got)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatalf("GetACL output is not a policy document (%v): %s", err, got)
	}
	if _, ok := decoded["acls"]; !ok {
		t.Errorf("decoded policy has no acls key: %v", decoded)
	}
}

// TestGetACLKeepsObjectShapeB282 guards the other direction: an object
// (headscale 0.29.x) must not be double-encoded by the unquoting path.
func TestGetACLKeepsObjectShapeB282(t *testing.T) {
	_, c, _ := fakeACLHS(t, http.StatusOK, `{"policy":{"grants":[{"src":["tag:a"]}]}}`)
	got, err := c.GetACL()
	if err != nil {
		t.Fatalf("GetACL: %v", err)
	}
	if got != `{"grants":[{"src":["tag:a"]}]}` {
		t.Errorf("GetACL(object shape) = %q — the object must pass through unchanged", got)
	}
	if same, err := PolicyEquivalent(got, `{"grants":[{"src":["tag:a"]}]}`); err != nil || !same {
		t.Errorf("PolicyEquivalent(object, object) = %v/%v, want true/nil", same, err)
	}
}

// TestPolicyEquivalentAcceptsStringifiedSideB282 pins that the B276
// comparison shares PolicyJSON's normalisation: one side stringified,
// the other an object, must compare EQUAL (otherwise every read of a
// stringified policy makes the /admin/exit-nodes page cry "политика
// headscale УСТАРЕЛА" and re-apply an identical policy forever).
func TestPolicyEquivalentAcceptsStringifiedSideB282(t *testing.T) {
	object := `{"grants":[{"action":"accept","src":["tag:a"],"dst":["tag:b"]}]}`
	stringified := `"{\"grants\":[{\"action\":\"accept\",\"src\":[\"tag:a\"],\"dst\":[\"tag:b\"]}]}"`
	same, err := PolicyEquivalent(object, stringified)
	if err != nil {
		t.Fatalf("PolicyEquivalent: %v", err)
	}
	if !same {
		t.Error("stringified and object forms of the same policy compared as DIFFERENT — " +
			"the ACL would be reported stale (and re-applied) on every pass")
	}
}

// TestGetACLStringifiedPolicyThenACLPagePathB282 drives the exact call
// chain the broken page uses: GetACL → PolicyJSON → json.Unmarshal into
// a view. Pre-B282 the first step returned a quoted literal and the last
// step failed with `cannot unmarshal string into Go value`.
func TestGetACLStringifiedPolicyThenACLPagePathB282(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/policy" && r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"policy":"{\"grants\":[{\"src\":[\"tag:a\"],\"dst\":[\"tag:b\"]}]}"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	c := New(srv.URL, "fake-key")
	raw, err := c.GetACL()
	if err != nil {
		t.Fatalf("GetACL: %v", err)
	}
	std, err := PolicyJSON(raw)
	if err != nil {
		t.Fatalf("PolicyJSON: %v", err)
	}
	var view struct {
		Grants []struct {
			Src []string `json:"src"`
		} `json:"grants"`
	}
	if err := json.Unmarshal(std, &view); err != nil {
		t.Fatalf("json.Unmarshal into the ACL view failed: %v (the live B282 500)", err)
	}
	if len(view.Grants) != 1 || len(view.Grants[0].Src) != 1 || view.Grants[0].Src[0] != "tag:a" {
		t.Errorf("grants decoded as %+v, want one grant with src=[tag:a]", view.Grants)
	}
}
