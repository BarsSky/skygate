// acl_b476_test.go — B476 (2026-11-15).
//
// EVERY `grants[]` ENTRY MUST CARRY `ip` (OR `app`).
//
// Measured live on 2026-11-15 while a device was being re-registered: the
// drift pass could not apply the generated policy at all —
//
//	acl-drift: re-apply FAILED (headscale PUT /api/v1/policy: 500
//	"setting policy: parsing policy: multiple errors:
//	 ip and app can not both be empty" ×16)
//
// — and the tailnet kept running on the last successfully applied policy, so
// no rule change reached headscale. The 16 lines were the per-DEVICE mesh
// grants (one per device in the tailnet): B470 taught writePerDeviceGrants
// the legacy `acls` dst shape (":*" instead of `ip`) and dropped the `ip`
// field for BOTH schemas on the way. The grants parser has no port suffixes,
// so those entries had neither `ip` nor `app` and headscale rejected the
// whole document.
//
// The regression is pinned at the generator, and each test fails on the
// pre-fix tree for its own reason:
//   - TestB476_EveryGrantCarriesIPOrApp      → mesh grants have no `ip`
//   - TestB476_LegacySchemaStillHasNoIPField → legacy must NOT gain `ip`
//   - TestB476_NoGrantWithEmptyDst           → duplicate tags → `"dst": []`
//   - TestB476_ValidatorDiscriminates        → the validator itself must
//     reject the pre-fix shape (anti-false-green, no shell mutation needed)
package acl

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// grantDoc is the part of a generated `grants` policy this block is about.
type grantDoc struct {
	Grants []struct {
		Src []string       `json:"src"`
		Dst []string       `json:"dst"`
		IP  []string       `json:"ip"`
		App map[string]any `json:"app"`
	} `json:"grants"`
}

// grantViolations is the single validator shared by the live-shape test and
// the anti-false-green test: it reproduces what headscale 0.29.3 rejects
// ("ip and app can not both be empty") plus the empty-dst shape that neither
// schema accepts.
func grantViolations(policy string) ([]string, error) {
	var body grantDoc
	if err := json.Unmarshal([]byte(policy), &body); err != nil {
		return nil, fmt.Errorf("generated grants policy is not valid JSON: %w", err)
	}
	if len(body.Grants) == 0 {
		return nil, fmt.Errorf("no grants emitted — the sweep would verify nothing")
	}
	var out []string
	for i, g := range body.Grants {
		if len(g.IP) == 0 && len(g.App) == 0 {
			out = append(out, fmt.Sprintf(
				"grant %d (src=%v dst=%v) has neither `ip` nor `app` — headscale rejects the WHOLE "+
					"policy with \"ip and app can not both be empty\" and the tailnet keeps the stale one",
				i, g.Src, g.Dst))
		}
		if len(g.Dst) == 0 {
			out = append(out, fmt.Sprintf("grant %d (src=%v) has an EMPTY dst — invalid in both schemas", i, g.Src))
		}
	}
	return out, nil
}

// TestB476_EveryGrantCarriesIPOrApp is the live failure, reproduced on the
// generator's own fixture.
func TestB476_EveryGrantCarriesIPOrApp(t *testing.T) {
	d := b470DB(t)
	policy, err := GenerateACLWithVia(d)
	if err != nil {
		t.Fatalf("GenerateACLWithVia: %v", err)
	}
	violations, err := grantViolations(policy)
	if err != nil {
		t.Fatalf("%v\npolicy:\n%s", err, policy)
	}
	for _, v := range violations {
		t.Errorf("%s (B476)\npolicy:\n%s", v, policy)
	}
	// The mesh grants are the ones that were broken; make sure the sweep above
	// actually covered them instead of passing on a policy that lost them.
	var body grantDoc
	if err := json.Unmarshal([]byte(policy), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	mesh := 0
	for _, g := range body.Grants {
		if len(g.Src) == 1 && strings.HasPrefix(g.Src[0], "tag:dev-michail-") &&
			len(g.Dst) > 0 && strings.HasPrefix(g.Dst[0], "tag:dev-") {
			mesh++
		}
	}
	if mesh != 3 {
		t.Fatalf("expected 3 per-device mesh grants for michail (one per device), got %d\npolicy:\n%s",
			mesh, policy)
	}
}

// TestB476_LegacySchemaStillHasNoIPField — the flag is per schema. The legacy
// `acls` parser is `AliasWithPorts` and rejects unknown fields (B265, B470),
// so "fix the grants document" must not leak `ip` into the legacy one.
func TestB476_LegacySchemaStillHasNoIPField(t *testing.T) {
	d := b470DB(t)
	policy, err := GenerateACL(d)
	if err != nil {
		t.Fatalf("GenerateACL: %v", err)
	}
	var body struct {
		ACLs []map[string]json.RawMessage `json:"acls"`
	}
	if err := json.Unmarshal([]byte(policy), &body); err != nil {
		t.Fatalf("generated legacy policy is not valid JSON: %v", err)
	}
	if len(body.ACLs) == 0 {
		t.Fatalf("no legacy acls emitted:\n%s", policy)
	}
	for i, rule := range body.ACLs {
		if _, ok := rule["ip"]; ok {
			t.Errorf("legacy acl %d carries an `ip` field — that schema's parser rejects unknown "+
				"fields and headscale then refuses the whole document (B476)\npolicy:\n%s", i, policy)
		}
	}
}

// TestB476_NoGrantWithEmptyDst drives the helper directly: a repeated tag used
// to pass the `len(userTags) >= 2` test and then leave an empty dst after the
// self-exclusion. De-duplication must collapse it to "no inter-device traffic"
// (zero grants) rather than a malformed grant.
func TestB476_NoGrantWithEmptyDst(t *testing.T) {
	cases := []struct {
		name string
		tags []string
		want int
	}{
		{name: "duplicate only", tags: []string{"tag:dev-michail-phone", "tag:dev-michail-phone"}, want: 0},
		{name: "duplicate plus one", tags: []string{"tag:dev-michail-phone", "tag:dev-michail-phone", "tag:dev-michail-tablet"}, want: 2},
		{name: "empty entries", tags: []string{"", "tag:dev-michail-phone", ""}, want: 0},
		{name: "distinct pair", tags: []string{"tag:dev-michail-phone", "tag:dev-michail-tablet"}, want: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var sb strings.Builder
			writePerDeviceGrants(&sb, []string{"michail"},
				map[string][]string{"michail": tc.tags}, "", true)
			out := sb.String()
			if got := strings.Count(out, `"src":`); got != tc.want {
				t.Fatalf("grants written = %d, want %d\ngenerated:\n%s", got, tc.want, out)
			}
			if strings.Contains(out, `"dst": []`) {
				t.Fatalf("a grant with an EMPTY dst was emitted (B476):\n%s", out)
			}
			if tc.want > 0 && !strings.Contains(out, `"ip": ["*"]`) {
				t.Fatalf("grants schema entry without `ip` (B476):\n%s", out)
			}
		})
	}
}

// TestB476_ValidatorDiscriminates is the anti-false-green proof: the same
// validator used above must REJECT an ip-less grant document. It builds that
// document with the helper's LEGACY shape (ipField=false) wrapped in a
// `grants` envelope — byte-for-byte the shape that froze the live policy —
// so a validator that only checks "the policy parses" can never pass this
// test. No shell-level source mutation is involved, which keeps the gate
// from ever leaving a mutated file in the tree.
func TestB476_ValidatorDiscriminates(t *testing.T) {
	var sb strings.Builder
	writePerDeviceGrants(&sb, []string{"michail"},
		map[string][]string{"michail": {"tag:dev-michail-phone", "tag:dev-michail-tablet"}}, "", false)
	broken := `{"grants": [` + strings.TrimPrefix(sb.String(), ",\n") + `]}`
	violations, err := grantViolations(broken)
	if err != nil {
		t.Fatalf("the discriminating document must still parse: %v\n%s", err, broken)
	}
	if len(violations) == 0 {
		t.Fatalf("the validator accepted ip-less grants — it cannot catch the B476 regression:\n%s", broken)
	}
}
