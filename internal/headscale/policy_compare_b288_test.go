// internal/headscale/policy_compare_b288_test.go — B288 (2026-09-22).
//
// The live `aro` case: /admin/exit-nodes showed «политика headscale УСТАРЕЛА»
// (generated 5082 bytes / live 11341 bytes) while every exit rule was green and
// the tailnet had exactly one relay — so the operator had nothing to choose and
// nothing to fix. The difference was 16 DUPLICATED grants (the pre-B274 generator
// emitted one grant per rule row and the CDN expansion had duplicated rows) plus
// two tag declarations. A grant list is a SET in headscale — a packet is allowed
// when any grant matches — so the byte difference said nothing about behaviour.
//
// These tests pin the set semantics, and pin that a REAL difference is still
// reported (and named) instead of being normalised away.
package headscale

import (
	"strings"
	"testing"
)

const b288Base = `{
  "grants": [
    { "src": ["tag:dev-daniil-workpc"], "dst": ["h-1"], "ip": ["*"], "via": ["tag:dev-infra-vps"] },
    { "src": ["tag:dev-daniil-laptop"], "dst": ["h-2"], "ip": ["*"], "via": ["tag:dev-infra-vps"] }
  ],
  "hosts": { "h-1": "104.16.0.0/12", "h-2": "1.2.3.0/24" },
  "tagOwners": { "tag:dev-daniil-workpc": ["daniil@ts.example.com", "tagged-devices@ts.example.com"] },
  "groups": { "group:daniil": ["daniil@ts.example.com"] }
}`

// b288WithDuplicatedGrants reproduces the live document: the first grant listed
// twice (the pre-B274 generator emitted one grant per rule row).
const b288WithDuplicatedGrants = `{
  "grants": [
    { "src": ["tag:dev-daniil-workpc"], "dst": ["h-1"], "ip": ["*"], "via": ["tag:dev-infra-vps"] },
    { "src": ["tag:dev-daniil-laptop"], "dst": ["h-2"], "ip": ["*"], "via": ["tag:dev-infra-vps"] },
    { "src": ["tag:dev-daniil-workpc"], "dst": ["h-1"], "ip": ["*"], "via": ["tag:dev-infra-vps"] }
  ],
  "hosts": { "h-1": "104.16.0.0/12", "h-2": "1.2.3.0/24" },
  "tagOwners": { "tag:dev-daniil-workpc": ["tagged-devices@ts.example.com", "daniil@ts.example.com"] },
  "groups": { "group:daniil": ["daniil@ts.example.com"] }
}`

// TestB288_DuplicateGrantsAreNotDrift is the regression for the live banner: a
// document that only repeats a grant (and lists the owners of a tag in another
// order) describes the SAME policy.
func TestB288_DuplicateGrantsAreNotDrift(t *testing.T) {
	same, err := PolicyEquivalent(b288Base, b288WithDuplicatedGrants)
	if err != nil {
		t.Fatalf("PolicyEquivalent: %v", err)
	}
	if !same {
		t.Error("a duplicated grant and a reordered owner list were reported as DIFFERENT policies; " +
			"headscale treats grants as a set, so this is the false 'политика УСТАРЕЛА' the operator saw on aro")
	}
	if detail, err := PolicyDriftDetail(b288Base, b288WithDuplicatedGrants); err != nil {
		t.Fatalf("PolicyDriftDetail: %v", err)
	} else if detail != "" {
		t.Errorf("PolicyDriftDetail on equivalent documents = %q, want \"\"", detail)
	}
}

// TestB288_RealDifferencesStillCount: normalisation must not swallow a genuine
// difference — an owner added/removed, a grant added/removed, a declaration
// missing.
func TestB288_RealDifferencesStillCount(t *testing.T) {
	cases := []struct {
		name   string
		other  string
		detail string
	}{
		{
			name: "extra owner on a tag",
			other: `{
  "grants": [
    { "src": ["tag:dev-daniil-workpc"], "dst": ["h-1"], "ip": ["*"], "via": ["tag:dev-infra-vps"] },
    { "src": ["tag:dev-daniil-laptop"], "dst": ["h-2"], "ip": ["*"], "via": ["tag:dev-infra-vps"] }
  ],
  "hosts": { "h-1": "104.16.0.0/12", "h-2": "1.2.3.0/24" },
  "tagOwners": { "tag:dev-daniil-workpc": ["daniil@ts.example.com"] },
  "groups": { "group:daniil": ["daniil@ts.example.com"] }
}`,
			detail: "different owners: tag:dev-daniil-workpc",
		},
		{
			name: "declaration only in the live policy",
			other: `{
  "grants": [
    { "src": ["tag:dev-daniil-workpc"], "dst": ["h-1"], "ip": ["*"], "via": ["tag:dev-infra-vps"] },
    { "src": ["tag:dev-daniil-laptop"], "dst": ["h-2"], "ip": ["*"], "via": ["tag:dev-infra-vps"] }
  ],
  "hosts": { "h-1": "104.16.0.0/12", "h-2": "1.2.3.0/24" },
  "tagOwners": {
    "tag:dev-daniil-workpc": ["daniil@ts.example.com", "tagged-devices@ts.example.com"],
    "tag:dev-daniil-homepc": ["daniil@ts.example.com"]
  },
  "groups": { "group:daniil": ["daniil@ts.example.com"] }
}`,
			detail: "only in the live policy: tag:dev-daniil-homepc",
		},
		{
			name: "an extra grant",
			other: `{
  "grants": [
    { "src": ["tag:dev-daniil-workpc"], "dst": ["h-1"], "ip": ["*"], "via": ["tag:dev-infra-vps"] },
    { "src": ["tag:dev-daniil-laptop"], "dst": ["h-2"], "ip": ["*"], "via": ["tag:dev-infra-vps"] },
    { "src": ["tag:dev-daniil-workpc"], "dst": ["h-2"], "ip": ["*"], "via": ["tag:dev-infra-vps"] }
  ],
  "hosts": { "h-1": "104.16.0.0/12", "h-2": "1.2.3.0/24" },
  "tagOwners": { "tag:dev-daniil-workpc": ["daniil@ts.example.com", "tagged-devices@ts.example.com"] },
  "groups": { "group:daniil": ["daniil@ts.example.com"] }
}`,
			detail: "grants:",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			same, err := PolicyEquivalent(b288Base, c.other)
			if err != nil {
				t.Fatalf("PolicyEquivalent: %v", err)
			}
			if same {
				t.Fatalf("a real difference was normalised away — the banner would stay silent about it")
			}
			detail, err := PolicyDriftDetail(b288Base, c.other)
			if err != nil {
				t.Fatalf("PolicyDriftDetail: %v", err)
			}
			if !strings.Contains(detail, c.detail) {
				t.Errorf("PolicyDriftDetail = %q, want it to name %q (the banner must not always blame the via pins)", detail, c.detail)
			}
		})
	}
}

// TestB288_LegacyRuleOrderStaysSignificant: the legacy `acls`/`rules` array is
// FIRST-MATCH, so reordering it changes behaviour and must still count as drift
// (that is why the normaliser is selective instead of sorting everything).
func TestB288_LegacyRuleOrderStaysSignificant(t *testing.T) {
	a := `{"acls":[{"action":"accept","src":["a@x"],"dst":["*:*"]},{"action":"accept","src":["b@x"],"dst":["1.2.3.0/24:*"]}],"tagOwners":{}}`
	b := `{"acls":[{"action":"accept","src":["b@x"],"dst":["1.2.3.0/24:*"]},{"action":"accept","src":["a@x"],"dst":["*:*"]}],"tagOwners":{}}`
	same, err := PolicyEquivalent(a, b)
	if err != nil {
		t.Fatalf("PolicyEquivalent: %v", err)
	}
	if same {
		t.Error("a reordered legacy acls[] list was treated as equivalent — in a first-match list that changes which rule wins")
	}

	// A duplicated rule in a first-match list is a no-op, but removing it would
	// move the later rules up, so the normaliser must keep it (and therefore must
	// NOT report these two as equivalent either).
	c := `{"acls":[{"action":"accept","src":["a@x"],"dst":["*:*"]},{"action":"accept","src":["a@x"],"dst":["*:*"]}],"tagOwners":{}}`
	d := `{"acls":[{"action":"accept","src":["a@x"],"dst":["*:*"]}],"tagOwners":{}}`
	same, err = PolicyEquivalent(c, d)
	if err != nil {
		t.Fatalf("PolicyEquivalent: %v", err)
	}
	if same {
		t.Error("a duplicated legacy rule was de-duplicated — in an ordered first-match list the length is significant")
	}
}

// TestB288_UnparseablePolicyIsAnError: "cannot tell" must stay an error, never a
// silent drift verdict (that path would trigger a pointless apply).
func TestB288_UnparseablePolicyIsAnError(t *testing.T) {
	if _, err := PolicyEquivalent("{not json", b288Base); err == nil {
		t.Error("PolicyEquivalent with an unparseable policy: got nil error, want an error")
	}
	if _, err := PolicyDriftDetail(b288Base, "{not json"); err == nil {
		t.Error("PolicyDriftDetail with an unparseable policy: got nil error, want an error")
	}
}
