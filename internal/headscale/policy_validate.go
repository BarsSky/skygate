// internal/headscale/policy_validate.go — B283 (2026-09-22).
//
// headscale's policy parser refuses the WHOLE document when a grant references
// a tag that `tagOwners` does not declare ("tag not found: <tag>"). On a
// `policy.mode: file` host that is not a rejected API call — it is the file the
// daemon reads at STARTUP, so headscale crash-loops and the tailnet loses its
// control plane entirely.
//
// Live on `aro` (2026-09-22): the generated policy carried
// `via: ["tag:dev-infra-exit-node-vps"]` on 19 per-CIDR grants while
// `tagOwners` listed only the per-user/per-device tags (the B275 assignment
// table's owner tag was never added to the declared set). The document was
// valid JSON — `python3 -m json.tool` accepted it — and headscale still would
// not start on it: restart counter 248, every device gone from the portal until
// an older snapshot was restored by hand.
//
// UndeclaredTags is the cheap, pure check that turns that class into a NAMED
// refusal inside skygate instead of a dead daemon.
package headscale

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// UndeclaredTags parses a policy document and returns every `tag:…` referenced
// by its grants (`src`, `dst`, `via`) that `tagOwners` does not declare.
//
// Both policy shapes are handled: the modern `grants[]` (with `via`) and the
// legacy `acls[]`. A `dst` entry of the form `tag:x:*` is reduced to `tag:x`
// (the `:*` is a port wildcard, not part of the tag). `*`, `autogroup:*` and
// user/host selectors are not tags and are ignored.
//
// The returned slice is sorted and contains no duplicates. An error means the
// document does not parse at all — the caller should refuse it for that reason
// (see SetPolicy).
func UndeclaredTags(policy string) ([]string, error) {
	raw, err := PolicyJSON(policy)
	if err != nil {
		return nil, err
	}
	var doc struct {
		TagOwners map[string]json.RawMessage `json:"tagOwners"`
		Grants    []map[string]json.RawMessage `json:"grants"`
		ACLs      []map[string]json.RawMessage `json:"acls"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse policy: %w", err)
	}

	declared := make(map[string]bool, len(doc.TagOwners))
	for tag := range doc.TagOwners {
		declared[tag] = true
	}

	missing := map[string]bool{}
	for _, set := range [][]map[string]json.RawMessage{doc.Grants, doc.ACLs} {
		for _, grant := range set {
			for _, key := range []string{"src", "dst", "via"} {
				var list []string
				field, ok := grant[key]
				if !ok {
					continue
				}
				if err := json.Unmarshal(field, &list); err != nil {
					// A non-list selector (hosts/groups shorthand) is not a
					// tag reference we can check — skip it rather than
					// inventing a failure.
					continue
				}
				for _, sel := range list {
					tag := normalizeTagSelector(sel)
					if tag == "" || declared[tag] {
						continue
					}
					missing[tag] = true
				}
			}
		}
	}
	if len(missing) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(missing))
	for tag := range missing {
		out = append(out, tag)
	}
	sort.Strings(out)
	return out, nil
}

// normalizeTagSelector returns the tag named by one grant selector, or "" when
// the selector is not a tag reference.
//
//	tag:dev-infra-emilia      → tag:dev-infra-emilia
//	tag:exit-node:*           → tag:exit-node   (port wildcard)
//	* / autogroup:internet    → ""              (not a tag)
//	user@example.com          → ""              (not a tag)
func normalizeTagSelector(sel string) string {
	s := strings.TrimSpace(sel)
	if !strings.HasPrefix(s, "tag:") {
		return ""
	}
	// Only the `:*` port wildcard suffix belongs to the selector, and even a
	// tag with an embedded ":" (none exist) must keep its name intact — so
	// strip a trailing ":*" only.
	s = strings.TrimSuffix(s, ":*")
	if s == "tag:" {
		return ""
	}
	return s
}
