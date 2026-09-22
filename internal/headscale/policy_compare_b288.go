// B288 (2026-09-22) — set semantics for the policy comparison, plus a
// human-readable "what actually differs" summary for /admin/exit-nodes.
//
// The problem this closes. `PolicyEquivalent` (B276) compared the decoded
// documents with `reflect.DeepEqual`, i.e. byte-for-byte-modulo-key-order. A
// headscale policy is not a byte string, it is a SET of statements:
//
//   - `grants` are additive — a packet is allowed when ANY grant matches, so
//     listing the same grant twice changes nothing;
//   - the entries of `src`/`dst`/`via`/`ip` are alternatives (OR), and the
//     owners in `tagOwners` / the members in `groups` are sets;
//   - only the legacy `acls`/`rules` array is order-sensitive, because there the
//     FIRST matching rule wins.
//
// Live on `aro` (2026-09-22, right after v1.5.50 was installed) the operator
// reported the red banner «политика headscale УСТАРЕЛА» on /admin/exit-nodes
// while every rule on Exit Rules was green and there was exactly one exit node
// to choose from. The generated policy was 5082 bytes, the live one 11341 — but
// the difference was 16 DUPLICATED workpc grants (the pre-B274 generator emitted
// one grant per rule row, and the CDN expansion had created duplicate rows;
// B274's CollapseDuplicateDerivedRules fixed the generator, the live document
// kept the duplicates). Not one pin was wrong. The banner was technically true
// and practically noise, and the only thing its button offered was a rewrite
// that changed no behaviour.
//
// Two more differences were real, and they are why "just press the button" was
// not a safe answer either — both are fixed in B288:
//
//   - the live document declared `tag:dev-daniil-homepc` / `tag:dev-daniil-laptop`
//     which the generator would have dropped (they are recorded in
//     `node_owner_map` but owned by the synthetic `tagged-devices`, so the
//     portal-user JOIN could not see them — the B285/B287 blind spot, now closed
//     by `ListDevTagsFromOwnerMap`);
//   - the owners of a per-device tag differed between the two writers
//     (`<user>@` from the ACL generator vs `<user>@ + tagged-devices@` from the
//     tag path), so an apply would strip the sentinel owner the next tag write
//     needs.
//
// With this file, `PolicyEquivalent` answers the operator's actual question —
// "does headscale serve the policy I would generate?" — and `PolicyDriftDetail`
// says WHICH section differs, so the next banner names its cause instead of
// blaming the `via` pins by default.
package headscale

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// normalizePolicy canonicalises a decoded policy document so two documents that
// describe the same policy compare equal: every set-like array is sorted and
// de-duplicated, while the order-sensitive legacy rule list keeps its order (and
// its duplicates — removing one would move later rules up in a first-match
// list).
func normalizePolicy(v interface{}) interface{} {
	m, ok := v.(map[string]interface{})
	if !ok {
		return canonicalPolicyValue(v)
	}
	out := make(map[string]interface{}, len(m))
	for k, val := range m {
		switch k {
		case "acls", "rules":
			out[k] = canonicalPolicyList(val, false)
		case "grants", "ssh":
			out[k] = canonicalPolicyList(val, true)
		case "tagOwners", "groups", "hosts", "autoApprovers", "nodeAttrs", "randomizeClientPort", "postures":
			// `tagOwners`/`groups`/`hosts` are objects whose values are sets;
			// the remaining keys are scalars or objects we canonicalise
			// recursively, which cannot change their meaning.
			out[k] = canonicalPolicyValue(val)
		default:
			out[k] = canonicalPolicyValue(val)
		}
	}
	return out
}

// canonicalPolicyValue recursively canonicalises a policy value: arrays become
// sorted, de-duplicated arrays; objects keep their keys; scalars are returned
// unchanged.
func canonicalPolicyValue(v interface{}) interface{} {
	switch t := v.(type) {
	case []interface{}:
		return canonicalPolicyList(t, true)
	case map[string]interface{}:
		out := make(map[string]interface{}, len(t))
		for k, val := range t {
			out[k] = canonicalPolicyValue(val)
		}
		return out
	default:
		return v
	}
}

// canonicalPolicyList canonicalises each element and, when dedupe is set, sorts
// the result and drops duplicates (identity = the canonical JSON of the
// element).
func canonicalPolicyList(v interface{}, dedupe bool) interface{} {
	arr, ok := v.([]interface{})
	if !ok {
		return canonicalPolicyValue(v)
	}
	canon := make([]interface{}, 0, len(arr))
	for _, el := range arr {
		canon = append(canon, canonicalPolicyValue(el))
	}
	if !dedupe {
		return canon
	}
	seen := make(map[string]bool, len(canon))
	out := make([]interface{}, 0, len(canon))
	for _, el := range canon {
		key := canonicalPolicyKey(el)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, el)
	}
	sort.Slice(out, func(i, j int) bool {
		return canonicalPolicyKey(out[i]) < canonicalPolicyKey(out[j])
	})
	return out
}

// canonicalPolicyKey is the identity of one element during de-duplication.
func canonicalPolicyKey(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%#v", v)
	}
	return string(b)
}

// PolicyDriftDetail describes, in operator-readable terms, which sections of two
// policy documents differ. It returns "" when they are equivalent (after the
// same normalisation PolicyEquivalent uses).
//
// Why it exists: the /admin/exit-nodes banner used to explain every drift with
// the `via` pin story, which on `aro` pointed the operator at the one thing that
// was NOT wrong. Naming the differing section (grants / tagOwners / hosts / …)
// is the difference between "your routing may be broken" and "your policy
// declares two tags the generator would not".
func PolicyDriftDetail(a, b string) (string, error) {
	am, err := decodePolicyValue(a)
	if err != nil {
		return "", fmt.Errorf("policy A: %w", err)
	}
	bm, err := decodePolicyValue(b)
	if err != nil {
		return "", fmt.Errorf("policy B: %w", err)
	}
	an, _ := normalizePolicy(am).(map[string]interface{})
	bn, _ := normalizePolicy(bm).(map[string]interface{})
	if reflect.DeepEqual(an, bn) {
		return "", nil
	}
	keys := make([]string, 0, len(an)+len(bn))
	seen := map[string]bool{}
	for k := range an {
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	for k := range bn {
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	var parts []string
	for _, k := range keys {
		av, aok := an[k]
		bv, bok := bn[k]
		switch {
		case !aok:
			parts = append(parts, fmt.Sprintf("%s: only in the generated policy", k))
		case !bok:
			parts = append(parts, fmt.Sprintf("%s: only in the live policy", k))
		case reflect.DeepEqual(av, bv):
			continue
		case k == "grants":
			onlyA, onlyB := diffNormalizedLists(av, bv)
			parts = append(parts, fmt.Sprintf("grants: %d only in the generated policy, %d only in the live policy", len(onlyA), len(onlyB)))
		case k == "tagOwners" || k == "groups" || k == "hosts":
			parts = append(parts, k+": "+describeMapDiff(av, bv))
		case k == "acls" || k == "rules":
			parts = append(parts, k+": the rule order differs (first match wins)")
		default:
			parts = append(parts, k+": differs")
		}
	}
	if len(parts) == 0 {
		return "", nil
	}
	return strings.Join(parts, "; "), nil
}

// diffNormalizedLists returns the elements present in exactly one of two
// already-normalised lists.
func diffNormalizedLists(a, b interface{}) (onlyA, onlyB []interface{}) {
	al, _ := a.([]interface{})
	bl, _ := b.([]interface{})
	inB := make(map[string]bool, len(bl))
	for _, el := range bl {
		inB[canonicalPolicyKey(el)] = true
	}
	inA := make(map[string]bool, len(al))
	for _, el := range al {
		inA[canonicalPolicyKey(el)] = true
	}
	for _, el := range al {
		if !inB[canonicalPolicyKey(el)] {
			onlyA = append(onlyA, el)
		}
	}
	for _, el := range bl {
		if !inA[canonicalPolicyKey(el)] {
			onlyB = append(onlyB, el)
		}
	}
	return onlyA, onlyB
}

// describeMapDiff summarises a difference between two tagOwners/groups/hosts
// maps: the names only one side has, plus a few names whose values differ.
func describeMapDiff(a, b interface{}) string {
	am, _ := a.(map[string]interface{})
	bm, _ := b.(map[string]interface{})
	var onlyA, onlyB, changed []string
	for k, av := range am {
		bv, ok := bm[k]
		switch {
		case !ok:
			onlyA = append(onlyA, k)
		case !reflect.DeepEqual(av, bv):
			changed = append(changed, k)
		}
	}
	for k := range bm {
		if _, ok := am[k]; !ok {
			onlyB = append(onlyB, k)
		}
	}
	sort.Strings(onlyA)
	sort.Strings(onlyB)
	sort.Strings(changed)
	var parts []string
	if len(onlyA) > 0 {
		parts = append(parts, "only in the generated policy: "+strings.Join(capList(onlyA, 3), ", "))
	}
	if len(onlyB) > 0 {
		parts = append(parts, "only in the live policy: "+strings.Join(capList(onlyB, 3), ", "))
	}
	if len(changed) > 0 {
		parts = append(parts, "different owners: "+strings.Join(capList(changed, 3), ", "))
	}
	return strings.Join(parts, "; ")
}

// capList joins at most n names and adds "… (+N more)" for the rest, so the
// banner line stays a line.
func capList(names []string, n int) []string {
	if len(names) <= n {
		return names
	}
	out := append([]string(nil), names[:n]...)
	return append(out, fmt.Sprintf("… (+%d more)", len(names)-n))
}
