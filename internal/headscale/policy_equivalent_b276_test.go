// B276 — the equality check that makes "is the live policy stale?" answerable.
//
// skygate owns two halves of the same decision: the assignment table / advertised
// routes (recomputed every few minutes) and the ACL whose per-CIDR grants pin a
// prefix to a named relay (regenerated only on a rule/user/device change). When the
// first moves and the second does not, every pin for the moved prefix names a relay
// that no longer serves it, and the client silently drops the route. Comparing the
// live policy with the generated one is how that becomes visible.
package headscale

import "testing"

// TestB276_PolicyEquivalent_SamePolicyDifferentForm: headscale returns the policy as
// an object (0.29) or as a stringified blob (older), with its own key order and
// indentation, while the generator emits compact JSON. All of those must compare
// equal — otherwise every pass would "detect" drift and re-apply the policy (which
// on a file-mode host restarts headscale).
func TestB276_PolicyEquivalent_SamePolicyDifferentForm(t *testing.T) {
	compact := `{"grants":[{"src":["tag:a"],"dst":["h-rule-104-16-0-0-12"],"ip":["*"],"via":["tag:dev-infra-emilia"]}],"hosts":{"h-rule-104-16-0-0-12":"104.16.0.0/12"}}`
	object := "{\n  \"hosts\": {\n    \"h-rule-104-16-0-0-12\": \"104.16.0.0/12\"\n  },\n  \"grants\": [\n    {\n      \"via\": [\"tag:dev-infra-emilia\"],\n      \"ip\": [\"*\"],\n      \"dst\": [\"h-rule-104-16-0-0-12\"],\n      \"src\": [\"tag:a\"]\n    }\n  ]\n}"
	hujson := `{
  // the generator writes no comments, but the operator's file may
  "hosts": { "h-rule-104-16-0-0-12": "104.16.0.0/12" },
  "grants": [ { "src": ["tag:a"], "dst": ["h-rule-104-16-0-0-12"], "ip": ["*"], "via": ["tag:dev-infra-emilia"] }, ],
}`
	for _, tc := range []struct {
		name string
		a, b string
	}{
		{"compact-vs-object", compact, object},
		{"compact-vs-hujson-comments", compact, hujson},
		{"object-vs-object", object, object},
	} {
		same, err := PolicyEquivalent(tc.a, tc.b)
		if err != nil {
			t.Errorf("%s: PolicyEquivalent error: %v", tc.name, err)
			continue
		}
		if !same {
			t.Errorf("%s: policies are equivalent but PolicyEquivalent said no", tc.name)
		}
	}
}

// TestB276_PolicyEquivalent_DetectsTheStalePin is the live case: the same alias,
// pinned to a different relay.
func TestB276_PolicyEquivalent_DetectsTheStalePin(t *testing.T) {
	oldPin := `{"grants":[{"src":["tag:dev-michail-basic"],"dst":["h-rule-104-16-0-0-12"],"ip":["*"],"via":["tag:dev-infra-karolina"]}],"hosts":{"h-rule-104-16-0-0-12":"104.16.0.0/12"}}`
	newPin := `{"grants":[{"src":["tag:dev-michail-basic"],"dst":["h-rule-104-16-0-0-12"],"ip":["*"],"via":["tag:dev-infra-emilia"]}],"hosts":{"h-rule-104-16-0-0-12":"104.16.0.0/12"}}`
	same, err := PolicyEquivalent(oldPin, newPin)
	if err != nil {
		t.Fatalf("PolicyEquivalent: %v", err)
	}
	if same {
		t.Error("the two policies pin the same prefix to different relays and must NOT compare equal")
	}
}

// TestB276_PolicyEquivalent_UnparseableIsAnError: an unreadable policy is "cannot
// tell", never "stale" — guessing would trigger a pointless apply on every pass and
// hide the real problem (something skygate cannot even parse).
func TestB276_PolicyEquivalent_UnparseableIsAnError(t *testing.T) {
	if _, err := PolicyEquivalent("not json at all", `{}`); err == nil {
		t.Error("PolicyEquivalent(garbage, {}): want an error, got nil")
	}
	if _, err := PolicyEquivalent(`{}`, ""); err == nil {
		t.Error("PolicyEquivalent({}, empty): want an error, got nil")
	}
	// A stringified policy on one side and an object on the other is the normal
	// headscale-version difference (the API envelope `{"policy": …}` is unwrapped by
	// GetACL before anything gets here), not an error.
	same, err := PolicyEquivalent(`"{\"grants\":[]}"`, `{"grants":[]}`)
	if err != nil {
		t.Fatalf("stringified vs object: %v", err)
	}
	if !same {
		t.Error("stringified and object forms of the same policy must compare equal")
	}
}
