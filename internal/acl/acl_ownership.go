// acl_ownership.go — who owns a node, and which tag a rule must use.
//
// Split out of acl.go in refactor Phase D (2026-10-01). Every rule has to name
// its device by TAG (a hostname selector dies on re-registration), so the
// resolution chain node → device → owner → tag lives here, together with the
// exit-node tag parser it feeds. These were the first ~300 lines of the file
// and nothing in the generator below makes sense without them.

package acl

import (
	"database/sql"
	"strconv"
	"strings"

	"skygate/internal/db"
)

// exitNodeTagToHostname strips the headscale tag prefix
// from an exit-node tag and returns the bare hostname.
//
// Examples (v1.5.2 B188.2 conventions):
//
//	"tag:dev-infra-emilia"  -> "emilia"
//	"tag:dev-infra-karolina"-> "karolina"
//	"tag:dev-infra-skygate-host-1" -> "skygate-host-1"
//	"tag:exit-emilia"       -> "emilia" (legacy, pre-B118)
//	"tag:exit-node"         -> "" (B279.1 — see below)
//	"tag:public"            -> "" (B279.1)
//	"tag:invalid-foo"       -> "" (unrecognized shape — caller treats as non-match)
//
// The function tries the known buckets ("dev-infra", "dev",
// "exit") as a prefix and returns whatever follows. This is
// a known-bucket-prefixed-string, NOT a generic "strip
// after the last dash" — that approach is wrong because
// hostnames can themselves contain dashes (e.g.
// "skygate-host-1" has 2 dashes: one is the bucket separator,
// the others are part of the hostname).
//
// The function does NOT validate that the hostname resolves
// to a real headscale node — that's the caller's job
// (and a non-match is a safe default: B188.2's per-CIDR
// pin only fires when exit_node_id matches a real hostname,
// so a malformed tag simply produces an unpinned grant, which
// is the "fail open" behavior we want for /my/exit-rules
// selective routing).
//
// B279.1 (v1.5.46) — CONTRACT CHANGE for the class sentinel. This
// function used to return "node" for `tag:exit-node` and rely on the
// caller noticing that no rule can be called "node". That reliance was
// the whole live `aro` incident: the exit_rules package had the same
// "strip tag:exit-" rule WITHOUT the caller-side guard, produced the
// hostname "node", and wrote it into 23 device_rules rows (routes then
// advertised/approved for a relay that does not exist). The class-tag
// knowledge now comes from db.IsClassTag — the same predicate the rest
// of the tree calls — so this copy cannot drift from it again, and the
// "no via= pin" outcome is produced here rather than by a caller's
// comment.
//
// 2026-08-26: v1.5.2 (B188.2).  2026-09-22: B279.1.
func exitNodeTagToHostname(tag string) string {
	if tag == "" {
		return ""
	}
	// A class tag names a role shared by many nodes, never one node:
	// no hostname can be derived from it. Returning "" is the caller's
	// "no via= pin" signal.
	if db.IsClassTag(tag) {
		return ""
	}
	body := strings.TrimPrefix(tag, "tag:")
	if body == "" || body == tag {
		// no "tag:" prefix, or the whole string was "tag:"
		return ""
	}
	// Try known buckets in order of decreasing specificity.
	// The longest match wins (dev-infra- before dev- before
	// exit-) so multi-word buckets aren't mistaken for the
	// short ones.
	for _, bucket := range []string{"dev-infra-", "dev-", "exit-"} {
		if strings.HasPrefix(body, bucket) {
			return body[len(bucket):]
		}
	}
	// No known bucket matched. Returning "" is the safe
	// default — B188.2's caller treats this as "no via= pin",
	// which means the rule falls through to the per-user grant
	// or direct internet. That's the "fail open" default.
	return ""
}

// resolvePerCIDRVia — v1.5.2 (B188.3) — returns the
// `via=[exit_node_tag]` value to attach to a per-CIDR
// grant, or "" for "no via" (loose default).
//
// Used by BOTH GenerateACLForPlane (useVia=false path) and
// GenerateACLWithViaForPlane (useVia=true path) — extracted
// to avoid duplicating the matching logic AND to make it
// unit-testable without a DB. The OLD function (B188.3) and
// the NEW function (B188.2) now share this single source of
// truth for "should this per-CIDR grant be pinned?".
//
// Returns the via tag iff ALL of:
//   - devTag is non-empty (rule has a per-device src;
//     wildcard rules and legacy device_ip rules don't get
//     a per-CIDR pin)
//   - viaByDevice[devTag] != "" (the device has a per-device
//     exit_node_pref)
//   - the pref's hostname matches ruleExitNodeID (the
//     rule's target exit_node matches the device's preferred
//     exit-node)
//
// `tag:exit-node` (the headscale catch-all sentinel) is
// correctly NOT matched — exitNodeTagToHostname returns ""
// for it (B279.1), which never equals a real device_rule's
// exit_node_id. So devices that pin to a class tag get no via=.
//
// 2026-08-26: v1.5.2 (B188.3).
func resolvePerCIDRVia(devTag, ruleExitNodeID string, viaByDevice map[string]string) string {
	if devTag == "" || ruleExitNodeID == "" {
		return ""
	}
	prefTag, ok := viaByDevice[devTag]
	if !ok || prefTag == "" {
		return ""
	}
	prefHost := exitNodeTagToHostname(prefTag)
	if prefHost == "" || prefHost != ruleExitNodeID {
		return ""
	}
	return prefTag
}

// deviceOwner is the node_owner_map projection the ACL builder needs
// to tag a rule's device. See resolveNodeOwners.
type deviceOwner struct {
	Username string
	Hostname string // already lowercased
	// Tag is the tag headscale actually carries on this node
	// (node_owner_map.tag). It is the authoritative selector: the
	// generator declares exactly this tag in tagOwners, so a grant that
	// uses it can never reference an undeclared tag.
	//
	// B284 (2026-09-22): the pre-fix code instead SYNTHESISED
	// `tag:dev-<username>-<hostname>` from the denormalised
	// device_rules.user_name, which on the live host was headscale's
	// synthetic owner `tagged-devices` — producing
	// `tag:dev-tagged-devices-exit-node-vps` and
	// `tag:dev-tagged-devices-workpc`. Neither exists on any node or in
	// tagOwners, and headscale rejects the WHOLE policy that references
	// them ("tag not found"): on a `policy.mode: file` host the daemon then
	// refuses to START (crash-loop, control plane down, every device gone
	// from the portal).
	Tag string
}

// resolveNodeOwners reads node_owner_map and returns
// node_id (as int) → owner. B265 (2026-09-19).
//
// Failures are deliberately non-fatal: the caller falls back to the
// pre-B265 device_ip src path rather than failing the whole policy
// generation, because a partly-pinned policy is still better than no
// policy on a busy tailnet. The fallback is visible in the generated
// JSON (a `100.64.x.y` src), which is exactly what the /admin/acls
// export/inspect surface is for.
func resolveNodeOwners(d *sql.DB) map[int]deviceOwner {
	rows, err := db.ListAllNodeOwners(d)
	if err != nil {
		return nil
	}
	out := make(map[int]deviceOwner, len(rows))
	for _, n := range rows {
		id, convErr := strconv.Atoi(strings.TrimSpace(n.NodeID))
		if convErr != nil {
			continue
		}
		host := strings.ToLower(strings.TrimSpace(n.Hostname))
		if n.Username == "" || host == "" {
			continue
		}
		out[id] = deviceOwner{
			Username: n.Username,
			Hostname: host,
			Tag:      strings.TrimSpace(n.Tag),
		}
	}
	return out
}

// deviceTagForRule returns the per-device tag a rule's src should use,
// preferring the denormalised device_rules columns and falling back to
// node_owner_map via the rule's device_id. Returns "" when neither
// source yields a usable (username, hostname) pair — the caller then
// emits the legacy device_ip src.
//
// Why this exists (B265): a raw device-IP src is a DIFFERENT selector
// than the device's tag, so it never matches the per-device grants and
// it defeats the `via` exit-node pin (headscale's ViaRoutesForPeer
// drops the exit-route exclusion for a prefix as soon as any non-via
// grant from that viewer overlaps it). On the reference VM 173/328
// enabled rules had an empty user_name and emitted exactly such a src.
//
// Pure function (given the pre-resolved owner map) so it is
// unit-testable without a DB.
//
// B284 (2026-09-22): the tag now comes from node_owner_map (the tag the node
// actually carries, and the one this generator declares in tagOwners). The old
// code preferred the denormalised device_rules.user_name and built
// `tag:dev-<that user>-<host>` from it — live on `aro` that field held
// headscale's synthetic owner `tagged-devices`, so the policy referenced
// `tag:dev-tagged-devices-exit-node-vps` and `tag:dev-tagged-devices-workpc`:
// tags that exist on no node and in no tagOwners. headscale rejects such a
// document as a whole, and in file mode it will not start on it at all.
// When node_owner_map has no tag for the device we return "" and the caller
// falls back to the device_ip src — a weaker selector, but one that always
// matches, instead of a policy the daemon refuses to load.
func deviceTagForRule(e db.ACLEntry, owners map[int]deviceOwner) string {
	if o, ok := owners[e.DeviceID]; ok {
		if tag := strings.TrimSpace(o.Tag); tag != "" {
			return tag
		}
	}
	return ""
}
