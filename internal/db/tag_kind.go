// tag_kind.go — B279 (v1.5.46) — one predicate for "is this headscale
// tag the IDENTITY of one node, or a tailnet-wide CLASS tag?".
//
// Live case (native host `aro`, 2026-09-22): the operator's only relay
// carried the class tag `tag:exit-node` and nothing else. Four
// independent copies of the "strip tag: to get a hostname" logic
// disagreed about that value:
//
//	internal/acl/acl.go                 -> "node" + a caller that treats
//	                                       it as a non-match (correct)
//	internal/feature/exit_rules         -> "node" used AS a hostname
//	internal/db/exit_node_prefs.go      -> accepted it as a per-node tag
//	internal/feature/admin/user_subnet  -> had its own inline guard
//
// The exit_rules copy wrote the phantom hostname into 23 device_rules
// rows (`my_exit_rules_apply_preferred preferred=node updated=21` in the
// live audit_log), so every rule named a relay that does not exist:
// nothing was advertised, nothing was approved, the ACL pin resolved to
// no node, and the whole feature looked "broken after the rule model
// changed" (B274/B275/B276 made exit_node_id the RELAY IDENTITY, so a
// stale value stopped being cosmetic).
//
// This file is the single source of truth. Every other place must call
// it instead of re-deriving the distinction from string prefixes:
//
//	tag:exit-node      CLASS  — "any relay in the tailnet" sentinel
//	tag:public         CLASS  — shared infrastructure
//	tag:private        CLASS  — every user's own devices
//	tag:subnet-router  CLASS  — sidecar subnet routers
//
//	tag:dev-infra-<host>  PER-NODE (infra / exit-node convention)
//	tag:dev-<user>-<host> PER-NODE (per-user device convention)
//	tag:exit-<host>       PER-NODE (legacy pre-B118 form)
//
// "tag:untagged" is neither: it is the portal's "no tag yet" marker and
// every caller already skips it explicitly.
//
// 2026-09-22: B279.
package db

import "strings"

// tailnetClassTags are the tag values that describe a ROLE shared by
// many nodes, never the identity of one node. A hostname can never be
// derived from them: stripping "tag:exit-" off "tag:exit-node" yields
// "node", which is exactly the phantom that broke the live host.
var tailnetClassTags = map[string]bool{
	"tag:exit-node":     true,
	"tag:public":        true,
	"tag:private":       true,
	"tag:subnet-router": true,
}

// IsClassTag reports whether tag is a tailnet-wide class tag (a role
// shared by many nodes) rather than one node's identity.
//
// Comparison is case-insensitive: headscale normalises tags to
// lowercase, but a hand-typed value or an older row can carry another
// case, and treating "TAG:Exit-Node" as a per-node tag would resurrect
// the phantom hostname "Exit-Node".
func IsClassTag(tag string) bool {
	return tailnetClassTags[strings.ToLower(strings.TrimSpace(tag))]
}

// IsPerNodeTag reports whether tag is the identity of exactly one node
// — the only kind of tag from which a hostname may be derived and the
// only kind that may be stored as a device's preferred exit-node.
//
// A bare "tag:dev-infra-" (no hostname) and "tag:untagged" are NOT
// per-node tags: neither names a node.
func IsPerNodeTag(tag string) bool {
	t := strings.TrimSpace(tag)
	if t == "" || IsClassTag(t) {
		return false
	}
	switch {
	case strings.HasPrefix(t, "tag:dev-infra-"):
		return len(t) > len("tag:dev-infra-")
	case strings.HasPrefix(t, "tag:dev-"):
		return len(t) > len("tag:dev-")
	case strings.HasPrefix(t, "tag:exit-"):
		// Reached only for non-class values (IsClassTag above), i.e.
		// the legacy per-node form "tag:exit-<host>".
		return len(t) > len("tag:exit-")
	default:
		return false
	}
}

// IsExitNodeTagForm reports whether tag is the PER-NODE identity of one
// node in an exit-node-shaped form:
//
//	"tag:dev-infra-<host>"  (B111+ infra convention: emilia, karolina)
//	"tag:exit-<host>"       (legacy pre-B93 convention, still accepted)
//
// It is deliberately narrower than IsPerNodeTag: a per-user device tag
// ("tag:dev-<user>-<host>") is a valid per-node identity but a
// meaningless exit-node preference — storing one makes the via= grant
// self-referential, which is the TD-17.1 michail/basic case.
//
// B279.1 (v1.5.46): this decision used to live inside
// internal/db/exit_node_prefs.go's unexported isExitNodeTagForm, while
// internal/feature/admin/user_subnet.go re-derived it inline
// (`HasPrefix(tag, "tag:exit-") && !HasPrefix(tag, "tag:exit-node")`) —
// a second copy of the same knowledge, one `!` away from the phantom
// relay. It now lives here, next to IsClassTag, so there is exactly one
// answer.
func IsExitNodeTagForm(tag string) bool {
	t := strings.TrimSpace(tag)
	if !IsPerNodeTag(t) {
		return false
	}
	return strings.HasPrefix(t, "tag:dev-infra-") || strings.HasPrefix(t, "tag:exit-")
}

// PickPerNodeTag chooses the tag that identifies the node out of
// headscale's tag list, or "" when the node carries no per-node tag.
//
// Why not Tags[0]: headscale returns the node's tags in database
// order, and a node routinely carries BOTH a class tag and its own
// tag (live: `emilia` = [tag:dev-infra-emilia, tag:exit-node,
// tag:private]). Taking the first element made the DB tag depend on
// the order of an array nobody controls — and on `aro` it took
// `tag:exit-node` and reverted the operator's intended per-node tag on
// every monitor tick.
//
// Preference order: infra identity first (that is the convention for
// exit-nodes/relays), then a per-user device tag, then the legacy
// form. Class tags are never returned.
func PickPerNodeTag(tags []string) string {
	var infra, dev, legacy string
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if !IsPerNodeTag(t) {
			continue
		}
		switch {
		case infra == "" && strings.HasPrefix(t, "tag:dev-infra-"):
			infra = t
		case dev == "" && strings.HasPrefix(t, "tag:dev-"):
			dev = t
		case legacy == "" && strings.HasPrefix(t, "tag:exit-"):
			legacy = t
		}
	}
	switch {
	case infra != "":
		return infra
	case dev != "":
		return dev
	default:
		return legacy
	}
}
