package acl

// acl_perdevice.go — shared helper for the v0.28.7 per-DEVICE grant block.
//
// v0.28.7 introduced a per-DEVICE grant block in the generated headscale
// policy: for each portal user with N≥2 tagged devices, emit N grants
// (one per device as src), with dst = the list of all OTHER devices of
// the same user, and `ip: ["*"]` (required by headscale 0.29.2).
//
// Why this exists separately from acl.go:
//   - Both GenerateACLForPlane and GenerateACLWithViaForPlane need the
//     block (the via variant takes a parallel code path because the
//     v0.28.1 per-user via feature split the generator into two).
//   - v0.28.7 first shipped with the block inlined in both functions
//     (commits 5b9826e + 06c8582 + 78cb707). The duplication made it
//     easy to miss the leading-separator fix in one of the two copies.
//   - v0.30.0 refactor extracted the shared helper here so any future
//     change to the per-DEVICE block touches exactly one place.
//
// Format constraints (carried by the helper, not the call sites):
//   - Each grant has shape:
//     { "src": ["<tag:dev-<user>-<device>>"],
//       "dst": ["<other-tag:dev-<user>-*>", ...],
//       "ip":  ["*"] }
//   - `ip: ["*"]` is mandatory: headscale 0.29.2 returns
//     "ip and app can not both be empty" without it.
//   - dst is bare tags (no `:*` suffix) — `ip: ["*"]` covers any port.
//   - Leading separator is always `,\n` (including the first grant
//     in the block) — the per-user block above ends with `] }` and
//     does NOT emit a trailing comma, so the first per-DEVICE grant
//     must bring its own separator. Without this, headscale's HuJSON
//     parser returns:
//       invalid character '{' after array value (expecting ',' or ']')
//     and the reapply returns HTTP http.StatusInternalServerError.
//
// Skip rule: users with <2 tagged devices produce no grants
// (no inter-device traffic to allow). Same applies to users with 0
// devices (e.g. fresh user accounts, or users whose only device
// has been offline long enough to drop from headscale).

import (
	"sort"
	"strings"
)

// writePerDeviceGrants writes the v0.28.7 per-DEVICE grant block
// to sb. See the file-level comment above for the format contract.
//
// Parameters:
//   - sb: the policy string builder (must already contain the
//     opening "{\n  \"acls\": [\n" and the per-user grant block)
//   - usernames: ordered list of bare usernames (without the
//     "@<baseDomain>" suffix); same slice used by the per-user
//     loop in the caller. Empty entries are skipped (defensive —
//     shouldn't occur in practice)
//   - tagsByUser: map from username to the ordered list of
//     `tag:dev-<user>-<device>` strings registered by the
//     v0.28.0 backfillNodeOwnership auto-tag
//
// O(N) per user, N = len(tagsByUser[user]). Total grants =
// sum over all users of len(tagsByUser[user]).
func writePerDeviceGrants(sb *strings.Builder, usernames []string, tagsByUser map[string][]string) {
	for _, uname := range usernames {
		if uname == "" {
			continue
		}
		userTags := tagsByUser[uname]
		if len(userTags) < 2 {
			// Need at least 2 devices for inter-device
			// traffic. With 0 or 1, there's nothing to
			// allow (no dst set). Skip.
			continue
		}
		// For each device, emit one grant with dst =
		// all OTHER devices of the same user.
		//
		// Separator pattern: ALWAYS write ",\n" before
		// each grant (including the first). See file
		// comment for the full HuJSON rationale.
		for _, srcTag := range userTags {
			sb.WriteString(",\n")
			// Build dst = all OTHER tags of the same user
			dstTags := make([]string, 0, len(userTags)-1)
			for _, otherTag := range userTags {
				if otherTag != srcTag {
					dstTags = append(dstTags, otherTag)
				}
			}
			sb.WriteString("    { \"src\": [\"" + srcTag + "\"], \"dst\": [")
			for j, t := range dstTags {
				if j > 0 {
					sb.WriteString(", ")
				}
				sb.WriteString("\"")
				sb.WriteString(t)
				sb.WriteString("\"")
			}
			// `ip: ["*"]` is required by headscale 0.29.2
			// (otherwise it returns "ip and app can not
			// both be empty"). The bare tag in dst
			// (no :* suffix) means "this tag, any port".
			sb.WriteString("], \"ip\": [\"*\"] }")
		}
	}
}

// getInfraExitNodeTags — v1.3.11 (B111) — returns the
// `tag:dev-infra-<hostname>` strings for every infra-owned
// exit node currently in `tagsByUser["infra"]`.
//
// The caller emits a `src=*, dst=tag:dev-infra-<exit>` catch-all
// per returned tag so any Tailscale client in the tailnet
// can use the infra-owned exit nodes (preserves the
// pre-B93 skyadmin-mesh behaviour where every skyadmin
// device could reach every other skyadmin device, including
// the relay VPSs that became 'infra'-owned in B93).
//
// Only `tag:dev-infra-*` tags are returned (not
// `tag:dev-infra-skygate-host-1` or any other non-exit
// infra device — we don't want the skygate host itself
// to be publicly routeable as if it were an exit node).
//
// Why a heuristic, not a SQL query for "exit nodes" tags:
// the policy generator doesn't know which `tag:dev-infra-X`
// tags map to exit nodes vs skygate VMs vs future
// infrastructure. The operator decides what "exit node"
// means; today it's 4 VPS nodes (emilia, karolina, sharlotta,
// svyatoslava-1) plus the catch-all `* → tag:exit-node` for
// any node that headscale tags `tag:exit-node`. Future
// operator-added exit nodes are automatically picked up
// because BackfillInfra (with the v1.3.11 isInfraNode
// rule 3) re-attributes them to 'infra' and adds them to
// tagsByUser["infra"], so the next policy reapply emits
// the catch-all automatically.
//
// Skips empty/nil tags defensively (shouldn't happen in
// production — writePerDeviceGrants already filters empty
// tags — but cheap insurance).
func getInfraExitNodeTags(tagsByUser map[string][]string) []string {
	infraTags := tagsByUser["infra"]
	if len(infraTags) == 0 {
		return nil
	}
	out := make([]string, 0, len(infraTags))
	for _, t := range infraTags {
		if t == "" {
			continue
		}
		// Skip the skygate host itself. The infra user
		// owns skygate-host-* nodes (hostname prefix
		// `skygate-`); we only want exit nodes in the
		// public-access catch-all.
		if strings.HasPrefix(t, "tag:dev-infra-skygate") {
			continue
		}
		out = append(out, t)
	}
	sort.Strings(out) // deterministic policy output
	return out
}
