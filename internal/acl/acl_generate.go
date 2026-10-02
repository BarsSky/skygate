// acl_generate.go — the policy builders.
//
// Split out of acl.go in refactor Phase D (2026-10-01). GenerateACLForPlane is
// the single biggest function in the package (~640 lines) and it is the whole
// point of the package: build the headscale policy document from device_rules,
// the ownership map and the prefix assignment table. It lives alone here so the
// next round of work (extracting its per-section helpers) has one file to read
// instead of 2209 lines.

package acl

import (
	"database/sql"
	"fmt"
	"log"
	"strings"

	"skygate/internal/db"
	"skygate/internal/prefixowner"
)

// GenerateACL builds the per-user headscale 0.29 HuJSON policy
// for the global default plane (every portal user with no
// headscale_url override). Equivalent to
// GenerateACLForPlane(d, ""); kept as the v0.12.0 entry
// point for backward compat — the web form and the bot
// pipeline still call this when there's no per-plane
// routing wired (single-plane deploys).
//
// 2026-07-11: Этап 9 part 2 — SQL moved to db.GetACLEntries.
// 2026-07-13: signature widened to *sql.DB.
// 2026-07-16: v0.13.0 — wrapper around GenerateACLForPlane
// so the global-default path uses the same code that
// per-plane callers use. baseDomain hard-coded because the
// per-plane multi-deploy DNS refactor is a v0.16.0 follow-up.
func GenerateACL(d *sql.DB) (string, error) {
	return GenerateACLForPlane(d, "")
}

// GenerateACLWithVia builds the headscale policy that uses
// `grants[]` (the v0.29.0-beta.4+ replacement for `acls[]`)
// with the `via` field for per-user preferred exit-nodes.
//
// Why this exists: pre-v0.28.1, the policy's catch-all
// `* → autogroup:internet:*` rule (in `acls[]`) lets any
// device in the tailnet use ANY of the available exit-nodes
// (relay-1, relay-2, relay-3). The user could pick
// whichever one they wanted via the Tailscale GUI. That's
// fine for casual use but undesirable for the per-user
// isolation model — alice's laptop choosing relay-3
// instead of relay-1 means her traffic exits through a
// different country.
//
// The fix: each user can have a "preferred exit-node" stored
// in `user_exit_node_prefs`. The ACL is rendered with one
// per-user grant that includes `via: ["tag:exit-<hostname>"]`,
// restricting the user's exit-node choice to their preferred
// node. headscale enforces the `via` constraint at the
// packet-filter level — alice's laptop CAN'T pick relay-3
// even if the user clicks it in the Tailscale GUI.
//
// 2026-07-24: v0.28.1.
func GenerateACLWithVia(d *sql.DB) (string, error) {
	return GenerateACLWithViaForPlane(d, "")
}

// GenerateACLForPlane builds the per-user headscale 0.29
// HuJSON policy for ONE control plane. planeURL == "" means
// "the global default plane" (every portal user with
// headscale_url = ”). The policy lists only the identities
// that live on the given plane — headscale rejects unknown
// identities in tagOwners, so we can't mix plane A and
// plane B identities in one policy file.
//
// All other policy shape (per-user rules, tag:public /
// tag:exit-node / autogroup:internet fallback, SSH rules,
// tagOwners) is identical across planes — the only thing
// that varies per plane is the set of identities.
//
// 2026-07-16: v0.13.0 — refactored out of the old
// single-plane GenerateACL so the per-plane pipeline can
// build and push one policy per headscale instance.
func GenerateACLForPlane(d *sql.DB, planeURL string) (string, error) {
	aclRows, err := db.GetACLEntries(d)
	if err != nil {
		return "", err
	}

	type ruleEntry struct {
		deviceIP       string
		userName       string // v0.28.0: for tag:dev-<user>-<device> src
		deviceHostname string // v0.28.0: for tag:dev-<user>-<device> src
		target         string
		action         string
		// v1.5.2 (B188.3): exitNodeID is the device_rules
		// .exit_node_id (the hostname of the exit-node the
		// rule targets, e.g. "emilia" / "karolina"). Empty
		// when the rule was created before v0.28.x added
		// the column. The per-CIDR grant loop reads this
		// to attach a per-CIDR `via=[exit_node_tag]`
		// constraint when the device's per-device
		// exit_node_pref matches. ACLEntry.ExitNodeID is
		// already populated by db.GetACLEntries
		// (B188.2 added COALESCE(exit_node_id, '') to
		// qSelectEnabledACLEntries).
		exitNodeID string
	}
	var entries []ruleEntry
	for _, e := range aclRows {
		if e.TargetType == "subnet" || e.TargetType == "ip" {
			entries = append(entries, ruleEntry{
				deviceIP:       e.DeviceIP,
				userName:       e.UserName,
				deviceHostname: e.DeviceHostname,
				target:         e.TargetValue,
				action:         e.Action,
				exitNodeID:     e.ExitNodeID,
			})
		}
	}

	// v1.5.2 (B188.3): per-device exit_node_pref → viaByDevice
	// map (tag:dev-<user>-<device> → exit_node_tag). Used by
	// the per-CIDR grant loop below to attach a `via=[exit_node_tag]`
	// when the device's pref matches the rule's exit_node.
	// Mirrors the loading in GenerateACLWithViaForPlane (line
	// ~1053) so both the useVia=true and useVia=false paths emit
	// the same per-CIDR pin.
	devicePrefsOld, err := db.ListAllDeviceExitNodePrefs(d)
	if err != nil {
		return "", err
	}
	viaByDeviceOld := make(map[string]string, len(devicePrefsOld))
	// B284: the device tag must be the tag the node carries (node_owner_map),
	// never one synthesised from a user name — headscale refuses the whole
	// policy that references an undeclared tag and will not start on it.
	tagsByHostOld := prefixowner.TagsByHost(d)
	for _, dp := range devicePrefsOld {
		if dp.Username == "" || dp.DeviceHostname == "" || dp.ExitNodeTag == "" || !dp.ViaEnabled {
			continue
		}
		devTag := strings.TrimSpace(tagsByHostOld[strings.ToLower(strings.TrimSpace(dp.DeviceHostname))])
		if devTag == "" {
			continue
		}
		viaByDeviceOld[devTag] = dp.ExitNodeTag
	}

	baseDomain := envBaseDomain()
	usernames, err := db.GetPortalUsernamesForPlane(d, planeURL)
	if err != nil {
		return "", err
	}
	// 2026-07-17: v0.17.0 — pull per-user subnet CIDRs in
	// parallel. Users without an allocated subnet get an
	// empty cidr (skipped by the rule builder). The CIDR
	// is deterministic (10.0.<uid>.0/24) so the policy is
	// stable across rebuilds and audits.
	userSubnets, err := db.GetUserSubnetsForPlane(d, planeURL)
	if err != nil {
		return "", err
	}
	subByUser := make(map[string]string, len(userSubnets))
	for _, us := range userSubnets {
		if us.Username != "" {
			subByUser[us.Username] = us.CIDR
		}
	}
	// 2026-07-17: v0.17.1 — cross-user IP-level sharing.
	// For each user, collect the CIDRs of subnets that
	// OTHERS have shared with them. The per-user dst
	// list gets these appended. Shares are one-directional
	// (grantor → grantee), so a single (alice, bob) row
	// makes bob's dst include 10.0.<alice>.0/24 but
	// alice's dst unchanged.
	sharedSubnets, err := db.GetSharedSubnetsForPlane(d, planeURL)
	if err != nil {
		return "", err
	}
	sharedByUser := make(map[string][]string)
	for _, ss := range sharedSubnets {
		if ss.GranteeUser != "" && ss.GrantorCIDR != "" {
			sharedByUser[ss.GranteeUser] = append(sharedByUser[ss.GranteeUser], ss.GrantorCIDR)
		}
	}
	// 2026-07-20: v0.22.0 — mesh (shared network)
	// membership. For each user, collect the CIDRs of
	// all OTHER members of every active mesh the user
	// belongs to. The per-user dst list gets these
	// appended alongside the v0.17.1 share rows. The
	// two sources are merged into a single deduped
	// dst list per user (a user who is both shared-with
	// and mesh-mate of the same other user sees the
	// CIDR exactly once — first-match semantics handle
	// the deduplication at the headscale level too,
	// but a clean dedup keeps the policy readable).
	meshMemberships, err := db.GetMeshMembershipsForPlane(d, planeURL)
	if err != nil {
		return "", err
	}
	for _, mm := range meshMemberships {
		if mm.SelfUser != "" && mm.OtherCIDR != "" {
			sharedByUser[mm.SelfUser] = append(sharedByUser[mm.SelfUser], mm.OtherCIDR)
		}
	}

	// 2026-07-24: v0.28.0 — per-user-per-device tags for
	// the per-device ACL src. We need every (username,
	// tag) pair so the policy can register each tag
	// in tagOwners (without that, the headscale parser
	// rejects the policy with "tag not found"). One
	// query per GenerateACL call; the result is small
	// (one row per device on the plane).
	devTags, err := db.GetPerUserDeviceTags(d, planeURL)
	if err != nil {
		return "", err
	}
	// Group tags by username so we can emit one
	// tagOwners entry per user with all their tags.
	tagsByUser := make(map[string][]string, len(devTags))
	for _, dt := range devTags {
		tagsByUser[dt.Username] = append(tagsByUser[dt.Username], dt.Tag)
	}

	var identities []string
	for _, uname := range usernames {
		if uname != "" {
			identities = append(identities, uname+"@"+baseDomain)
		}
	}
	if identities == nil {
		identities = []string{}
	}

	var sb strings.Builder
	sb.WriteString("{\n  \"acls\": [\n")

	// Per-user rule: user can reach their OWN devices
	// only. v0.17.0: if they have a personal subnet,
	// extend the dst to include 10.0.<uid>.0/24. v0.17.1:
	// ALSO extend with every grantor's CIDR that has
	// shared their subnet with this user. The CIDRs are
	// unique per grantor, so the per-user dst list
	// becomes deterministic and headscale's first-match
	// semantics handle the isolation.
	for i, idn := range identities {
		if i > 0 {
			sb.WriteString(",\n")
		}
		// idn = "alice@tsnet.example.com" — extract the
		// bare username for the lookups.
		uname := strings.SplitN(idn, "@", 2)[0]
		// Build the dst list. Start with the user's own
		// identity (their own devices). Then add their
		// own CIDR (if any). Then add every shared CIDR
		// (v0.17.1 share rows + v0.22.0 mesh membership
		// rows, deduped — a user who is both shared-with
		// and mesh-mate of the same other user gets the
		// CIDR exactly once).
		dst := []string{idn + ":*"}
		if ownCIDR := subByUser[uname]; ownCIDR != "" {
			dst = append(dst, ownCIDR+":*")
		}
		// dedupSet tracks the CIDRs already in dst so
		// duplicate rows in user_subnet_shares +
		// mesh_members (e.g. alice shares with bob AND
		// alice and bob are in the same mesh) collapse
		// to a single dst entry. The dedup is purely
		// cosmetic — headscale's first-match semantics
		// handle duplicates correctly — but a clean
		// policy is easier to audit and diff across
		// deploys.
		dedupSet := make(map[string]bool, len(dst))
		for _, d := range dst {
			dedupSet[d] = true
		}
		for _, sharedCIDR := range sharedByUser[uname] {
			if sharedCIDR == "" {
				continue
			}
			entry := sharedCIDR + ":*"
			if dedupSet[entry] {
				continue
			}
			dedupSet[entry] = true
			dst = append(dst, entry)
		}
		// Render as a single-line JSON array.
		sb.WriteString("    { \"action\": \"accept\", \"src\": [\"" + idn + "\"], \"dst\": [")
		for j, d := range dst {
			if j > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString("\"")
			sb.WriteString(d)
			sb.WriteString("\"")
		}
		// 2026-07-25: v0.28.3 — add autogroup:internet to
		// the per-user dst list. Combined with the catch-all
		// change below (src=tag:public, not src=*), this is
		// the exit-node bypass fix: the only way a device
		// can reach autogroup:internet (and thus USE an
		// exit-node for internet) is via a per-user grant
		// or the tag:public relay path. Devices that match
		// no per-user grant (rare — every portal user's
		// device resolves to a user identity via
		// tagOwners) cannot piggyback on the catch-all to
		// pick whichever exit-node they want.
		sb.WriteString(", \"autogroup:internet:*\"")
		sb.WriteString("] }")
	}

	// 2026-07-27: v0.29.0 — per-DEVICE grant for TAGGED
	// devices. The per-user grant above uses src=user@
	// which in Tailscale v2 policy does NOT match tagged
	// devices (every device since v0.28.0 carries
	// tag:dev-<user>-<device>; the device's source
	// identity in ACL is the TAG, not the user). Symptom
	// observed in production (operator, 2026-07-27):
	// workstation-2 (Android, tag:dev-admin-workstation-2) could
	// not reach workstation-1 (Windows, tag:dev-admin-
	// workstation-1) over Tailscale for Moonlight, because
	// no rule had src=tag:dev-admin-workstation-2 and
	// dst=tag:dev-admin-workstation-1.
	//
	// Earlier design (reverted) tried src=tag:dev-<user>-*
	// (wildcard). headscale 0.29.2 rejects wildcards in
	// tagOwners with "src=tag not found". v0.29.0 uses
	// CONCRETE per-device tags (which headscale accepts
	// because they were registered by the v0.28.0
	// backfillNodeOwnership auto-tag) and emits one
	// per-DEVICE grant with dst=list of all OTHER devices
	// of the same user.
	//
	// 7 grants for admin (one per device). 2 grants
	// for user1. O(n) per user, not O(n²).
	//
	// Order: emitted AFTER the per-user grant (which
	// is for SSH and untagged identities) and BEFORE
	// per-device rules. This way:
	//   1. Per-device rules (most specific dst) win
	//   2. Per-user grant (user@) wins for SSH/untagged
	//   3. THIS per-DEVICE grant is the fallback for
	//      tagged devices talking to other tagged
	//      devices of the SAME user
	//   4. Per-device pref grant (with via) when set
	//   5. Loose per-device grant (autogroup:internet)
	//   6. Catch-alls last
	//
	// Implementation: writePerDeviceGrants is the shared
	// helper (v0.30.0 refactor) — same code shape, no
	// duplication. The function comment there is the
	// canonical reference for the format requirements
	// (separator pattern, `ip: ["*"]`, dst=tag-only).
	//
	// B316 (v1.5.81): the mesh source is DeviceTagsForMesh, not the ownership JOIN
	// alone. headscale rewrites a tagged node's user to the synthetic
	// `tagged-devices` (B287), so a row carrying that name used to drop its device
	// out of the mesh entirely and SILENTLY — live on `aro`: daniil's workpc/laptop
	// were invisible, he was left with one visible device,
	// `writePerDeviceGrants` skipped him (`len(userTags) < 2`) and the tailnet
	// ended up with NO device-to-device grant at all while both machines were
	// online. The resolved source takes the owner from the ownership row, then the
	// device's own tag, then the user its rules were created under; anything it
	// cannot attribute is NAMED in the journal instead of vanishing.
	meshTagsByUser, unattributed, meshErr := db.DeviceTagsForMesh(d)
	if meshErr != nil {
		log.Printf("acl: device mesh owner lookup failed (%v) — falling back to the ownership JOIN; some devices may lose their device-to-device grant", meshErr)
		meshTagsByUser = tagsByUser
	}
	if len(unattributed) > 0 {
		names := make([]string, 0, len(unattributed))
		for _, u := range unattributed {
			names = append(names, u.Hostname+" ("+u.Reason+")")
		}
		log.Printf("acl: %d device(s) cannot join the device mesh and get NO device-to-device grant: %s — adopt them on /admin/devices (or fix their tag) to restore it",
			len(unattributed), strings.Join(names, "; "))
	}
	writePerDeviceGrants(&sb, usernames, meshTagsByUser)

	// 2026-08-13: v1.3.11 (B111) — public access to
	// infra-owned exit nodes. The 'infra' user owns
	// skygate-host-* + exit nodes (per isInfraNode).
	// Per-user grants for skyadmin/michail/etc. include
	// autogroup:internet:* in their dst (so the user's
	// own devices can use exit nodes), but they do NOT
	// include specific `tag:dev-infra-<exit>` dst
	// entries — and Tailscale's exit-node forwarding
	// requires the client to be able to reach the exit
	// node's Tailscale IP (100.64.0.X) directly. Without
	// these catch-alls, skyadmin's android/cyborg/
	// nothing-phone-2 etc. would lose the ability to use
	// emilia/karolina/sharlotta/<polygon-vm-hostname> as exit
	// nodes the moment we move those nodes to the 'infra'
	// bucket.
	//
	// Why a catch-all (`src=*`) instead of a per-user
	// grant per (portal user × infra exit node): the
	// operator requested that the public access match
	// the pre-B93 behaviour. Pre-B93, emilia/karolina/
	// sharlotta had `tag:dev-skyadmin-<name>` and the
	// per-device mesh between skyadmin's 14 devices
	// gave every skyadmin device access to every other
	// skyadmin device. Post-B111, the per-device mesh
	// lives in the 'infra' bucket (only 5 infra devices
	// so far), and the per-user grants (which go to
	// autogroup:internet, NOT specific device IPs) don't
	// cover the exit-node IP. The `*` src is safe because
	// the dst is a per-infrastructure-exit-node tag (only
	// reachable by the operator's VPN traffic — no
	// public internet origin can reach 100.64.0.X).
	infraExitTags := getInfraExitNodeTags(tagsByUser)
	for _, exitTag := range infraExitTags {
		// Match the catch-all style: bare tag in dst
		// (no :* suffix — breaks v2 parser), `ip: ["*"]`
		// (required by headscale 0.29.2).
		sb.WriteString(",\n    { \"src\": [\"*\"], \"dst\": [\"" + exitTag + "\"], \"ip\": [\"*\"] }")
	}

	// Per-device exit-rules. v0.28.0: src prefers
	// tag:dev-<user>-<device> (set by the v0.28.0
	// backfillNodeOwnership auto-tag, survives IP changes)
	// over device_ip (legacy, set at rule INSERT time,
	// breaks if the device reconnects with a new Tailscale
	// IP). Falls back to device_ip for pre-v0.28.0 rows
	// where the backfill hasn't run yet, then to "*" for
	// rules that have neither.
	for _, e := range entries {
		src := "\"*\""
		var devTag string // tag:dev-<user>-<device> for the per-CIDR via= lookup
		switch {
		case e.userName != "" && e.deviceHostname != "":
			// tag:dev-<user>-<device> — preferred, robust.
			// B176 (v1.5.2): headscale 0.29 requires
			// tags to be lowercase, AND the tag we
			// actually apply to the node in the
			// backfill (nodeownership.go:599) is
			// already lowercased — so the ACL src
			// must match exactly. Without this
			// strings.ToLower, an ACL rule like
			//   src = ["tag:dev-skyadmin-SkyBars"]
			// would never match a node carrying
			//   tag:dev-skyadmin-skybars
			// (the actual headscale-side tag, which
			// is lowercase per the v0.28.0
			// convention + headscale 0.29
			// requirement). Pre-B176 the rule was
			// built uppercase + applied uppercase →
			// headscale rejected the apply with
			// "tag should be lowercase" → rule
			// never applied → per-device ACL for
			// the device effectively denied until
			// the operator noticed.
			devTag = "tag:dev-" + e.userName + "-" + strings.ToLower(e.deviceHostname)
			src = "\"" + devTag + "\""
		case e.deviceIP != "":
			// legacy device_ip src — works for the current
			// session, breaks on Tailscale IP change
			src = fmt.Sprintf("\"%s\"", e.deviceIP)
		}
		// v1.5.2 (B188.3): per-CIDR exit-node pin. Mirrors
		// the same logic in GenerateACLWithViaForPlane
		// (the useVia=true path) via the shared
		// resolvePerCIDRVia helper. The OLD function's
		// dst format is "<target>:*" (vs the NEW
		// function's bare alias), but the via= field
		// is identical. Pre-B188.3 the per-CIDR grant
		// in the useVia=false path was UNPINNED — the
		// device could use any exit-node for the
		// destination. Post-B188.3 it's pinned to the
		// device's preferred exit-node when the rule's
		// target exit matches. The catch-all
		// (autogroup:internet) stays UNPINNED in both
		// paths.
		viaForGrant := resolvePerCIDRVia(devTag, e.exitNodeID, viaByDeviceOld)
		if viaForGrant != "" {
			sb.WriteString(",\n    { \"action\": \"" + e.action + "\", \"src\": [" + src + "], \"dst\": [\"" + e.target + ":*\"], \"via\": [\"" + viaForGrant + "\"] }")
		} else {
			sb.WriteString(",\n    { \"action\": \"" + e.action + "\", \"src\": [" + src + "], \"dst\": [\"" + e.target + ":*\"] }")
		}
	}

	// 2026-07-15: v0.12.0.1 — the catch-all `"*:*" accept`
	// rule at the end of the ACL was a security bug. With
	// it in place, Tailscale's first-match semantics still
	// hit the per-user rules for self-traffic, but ANY
	// other traffic (e.g. alice trying to reach bob's
	// device) fell through to the catch-all and was
	// accepted. The result: the operator's Android Tailscale
	// client showed every other user's device in the
	// "local network" view (each device has a 100.x.x.x
	// Tailscale IP visible to the client, and the ACL
	// said "yes, you can route to any of them").
	//
	// 2026-07-15: v0.12.0.2 — the v0.12.0.1 fix was
	// over-broad: dropping the catch-all also removed the
	// internet egress that exit-node routing depends on.
	// On the operator's Windows box the loss was invisible
	// (Windows has 240 explicit per-device rules for
	// direct access to operator IPs), but on Android the
	// exit-node flow stopped working — Android was relying
	// on the catch-all as "allow all internet destinations
	// through whatever exit node the client picked". The
	// fix is to replace the literal `"*:*"` catch-all with
	// `autogroup:internet:*` (the Tailscale-recommended
	// internet-egress primitive). autogroup:internet
	// matches every IP outside the tailnet's 100.64.100.0/10
	// range, so:
	//
	//   * alice → bob's device  — bob is in 100.64.100.0/10,
	//     NOT in autogroup:internet. The rule does not
	//     match. The per-user rule (alice → alice:*) was
	//     already skipped (dst is not alice's). Falls
	//     off the end → denied. Security preserved.
	//
	//   * alice → 8.8.8.8 via exit node — 8.8.8.8 IS in
	//     autogroup:internet. The rule matches. Exit node
	//     routing restored on Android.
	//
	// The rule is appended LAST so it doesn't override any
	// more specific rule (Tailscale first-match). The
	// structural guarantee: the final rule in acls[] is
	// now `* → autogroup:internet:*`, NOT `* → *:*`.
	// TestGenerateACL_LastRuleIsAutogroupInternet pins
	// this. Help page (help.html) already documents
	// autogroup:internet as the recommended pattern.
	sb.WriteString(",\n    { \"action\": \"accept\", \"src\": [\"*\"], \"dst\": [\"tag:public:*\"] }")
	sb.WriteString(",\n    { \"action\": \"accept\", \"src\": [\"*\"], \"dst\": [\"tag:exit-node:*\"] }")
	// 2026-07-25: v0.28.3 — change the autogroup:internet
	// catch-all from src=* to src=tag:public. With src=*,
	// ANY device in the tailnet could use ANY exit-node
	// (relay-1, relay-2, relay-3) for arbitrary internet
	// destinations — including relay-3's 148 PrimaryRoutes
	// (Telegram/Google/Cloudflare/etc.). The user reported
	// this as "workstation-3 без правил имеет доступ к сайтам и
	// подсетям что только для workstation-1": workstation-3 (tag:dev-
	// user-workstation-3 → admin@<baseDomain>) has no per-device rules,
	// but the src=* catch-all let it reach relay-3's
	// PrimaryRoutes through the exit-node path.
	//
	// The fix has two parts:
	//   1. The per-user grant (above) now includes
	//      "autogroup:internet:*" in its dst — so each
	//      user CAN reach the public internet through
	//      the tag:public relay network, but only via
	//      their own grant (which the headscale policy
	//      engine checks first).
	//   2. The catch-all here is restricted to
	//      src=tag:public — only relay nodes (relay-1,
	//      relay-2, relay-3, plus any future
	//      tag:public infrastructure) can use
	//      autogroup:internet themselves (i.e. exit
	//      FORWARD traffic to the internet). End-user
	//      devices no longer match this catch-all.
	//
	// Net effect: workstation-3 (and every other end-user device)
	// still has internet egress (via the per-user grant
	// in the user's row), but only as that specific
	// user — and in the via=true path, that user's
	// grant has via=[<preferred exit-node>], so workstation-3 is
	// pinned to a specific relay, not free to pick.
	// tag:public and tag:exit-node catch-alls above are
	// unchanged — those are for tag:public SSH
	// reachability (admin SSH into relays) and don't
	// enable exit-node forwarding.
	sb.WriteString(",\n    { \"action\": \"accept\", \"src\": [\"tag:public\"], \"dst\": [\"autogroup:internet:*\"] }")
	sb.WriteString("\n  ],\n")

	sb.WriteString("  \"tagOwners\": {\n")
	// 2026-08-17: v1.3.18 (hotfix) — track which tag names
	// have already been emitted so the 4 emission blocks
	// (static `tag:public` / `tag:exit-node` / `tag:subnet-router`
	// / `tag:private`, the per-user tagOwners loop from
	// tagsByUser, the distinctVias loop, and the
	// augmentedTagsByUser / perDevTagOwners loop) can't
	// emit the same key twice. The headscale v2 JSON parser
	// rejects duplicate object keys with
	// "duplicate object member name" — see commit 6a0ec3a..HEAD
	// for the bug that surfaces after migrating legacy
	// `tag:exit-X` user_exit_node_prefs to `tag:dev-infra-X`
	// (the new tag already lives in tagsByUser via the
	// infra bucket, so re-emitting it via the via=tag
	// path produces a duplicate).
	emittedTagOwners := make(map[string]bool, 32)
	emitTagOwner := func(tag, ownerListJSON string) {
		if emittedTagOwners[tag] {
			return
		}
		emittedTagOwners[tag] = true
		sb.WriteString(",\n    \"" + tag + "\": " + ownerListJSON + "\n")
	}
	sb.WriteString("    \"tag:public\": [\"" + envAdminIdentity() + "@" + baseDomain + "\"]")
	emittedTagOwners["tag:public"] = true
	// 2026-08-17: v1.3.19 (B118) — tag:exit-node is owned by
	// `infra` per the DESIGN (AGENTS.md "Tag ownership
	// rules"). `infra` is the technical user that owns
	// all exit-nodes/hosts; admin's portal account
	// (skyadmin) does NOT own infra tags so the policy
	// distinguishes "operator's personal devices" from
	// "infrastructure nodes" at the headscale tagOwners
	// level. Pre-fix, this was `envAdminIdentity()` which
	// gave the tag to skyadmin — when headplane applied
	// `tag:exit-node` to a node owned by the infra
	// headscale user, the tagOwners check passed only
	// because the policy was loose. With infra@ as
	// the owner, the policy is consistent with the DB
	// (node_owner_map.username for all 4 tagged
	// exit-nodes is `infra`).
	emitTagOwner("tag:exit-node", "[\"infra@"+baseDomain+"\"]")
	if len(identities) > 1 {
		emitTagOwner("tag:private", "["+strings.Join(quoteAll(identities), ",")+"]")
	} else if len(identities) == 1 {
		emitTagOwner("tag:private", "[\""+identities[0]+"\"]")
	} else {
		// 2026-08-10: v0.33.1.41 — handle the empty
		// identities case. Pre-fix, the `else` branch
		// read identities[0] unconditionally, which
		// panicked when the V054 infra row was the only
		// portal user and headscale_user_id was still
		// NULL (so the qSelectPortalUsernamesForPlane
		// filter dropped it). The empty-ACL case is
		// degenerate (the policy has no per-user grants)
		// but shouldn't crash the admin Apply path. Emit
		// a tagOwners entry owned by the admin identity
		// (the per-user grants below will all be empty,
		// which headscale accepts as "no user-scoped
		// rules" — same shape as a fresh deployment
		// before any portal user is linked).
		emitTagOwner("tag:private", "[\""+envAdminIdentity()+"@"+baseDomain+"\"]")
	}
	// 2026-07-17: v0.17.0 — register tag:subnet-router as
	// owned by EVERY portal user. Each user's tailscale
	// sidecar (v0.16.7) registers with tag:subnet-router
	// via the preauth key issued by Skygate; the
	// auto-approver (also v0.16.7) approves the
	// 10.0.<uid>.0/24 route when the sidecar advertises
	// it. For headscale to accept nodes with this tag,
	// at least one user must own the tag in tagOwners —
	// we list every portal user so any of them can host a
	// sidecar (matching the v0.16.0 design decision that
	// "every portal user is eligible for a personal
	// subnet"). Without this entry, headscale rejects the
	// policy with "tag not found: tag:subnet-router".
	emitTagOwner("tag:subnet-router", "["+strings.Join(quoteAll(identities), ",")+"]")
	// 2026-07-24: v0.28.0 — per-user-per-device tags.
	// One tagOwners entry per (user, device) — headscale
	// expects each tag to have its own line. Without
	// these entries, the parser rejects the policy with
	// "tag not found" when it hits the per-device ACL
	// rules above. The output is sorted by (username,
	// tag) for stable diffs across deploys (important
	// for the operator's policy audit).
	//
	// B288 (2026-09-22): the declarations come from devTagsToDeclare (the
	// portal-user JOIN UNION every per-device tag node_owner_map records) and
	// the owners from devTagOwnerJSON — the same pair the grants generator and
	// the tag-writing paths emit. Two generators that render different
	// `tagOwners` for the same database is how the live policy ended up
	// permanently "stale" on `aro`.
	legacyKnown := make([]string, 0, len(tagsByUser))
	for _, tags := range tagsByUser {
		legacyKnown = append(legacyKnown, tags...)
	}
	legacyDevTags, legacyErr := devTagsToDeclare(d, legacyKnown)
	if legacyErr != nil {
		return "", legacyErr
	}
	for _, tag := range legacyDevTags {
		ownerJSON, oErr := devTagOwnerJSON(tag, baseDomain)
		if oErr != nil {
			return "", oErr
		}
		emitTagOwner(tag, ownerJSON)
	}
	sb.WriteString("  },\n")

	sb.WriteString("  \"groups\": {\n")
	for i, idn := range identities {
		if i > 0 {
			sb.WriteString(",\n")
		}
		parts := strings.SplitN(idn, "@", 2)
		groupName := "group:" + parts[0]
		sb.WriteString("    \"" + groupName + "\": [\"" + idn + "\"]")
	}
	sb.WriteString("\n  },\n")

	sb.WriteString("  \"ssh\": [\n")
	sb.WriteString("    {\n")
	sb.WriteString("      \"action\": \"accept\",\n")
	// 2026-09-18: src also carries the skygate host's own infra tag
	// (tag:dev-infra-skygate-*). Pre-fix only tag:private + the admin
	// identity were allowed, so skygate — which SSHes to the exit nodes
	// from the host it runs on — was refused with "tailnet policy does
	// not permit you to SSH to this node".
	// See getSkygateHostInfraTags in acl_perdevice.go.
	sb.WriteString("      \"src\": [" + strings.Join(quoteAll(sshRuleSrc(tagsByUser, envAdminIdentity(), baseDomain)), ", ") + "],\n")
	sb.WriteString("      \"dst\": [\"tag:exit-node\"],\n")
	sb.WriteString("      \"users\": [\"root\"]\n")
	sb.WriteString("    },\n")
	// 2026-07-14: Этап 14 v7 — allow admin to SSH into tag:public
	// relay nodes (relay-1, relay-2, relay-3) so they can be
	// reconfigured (e.g. enable --advertise-exit-node) without
	// needing direct public-IP SSH access. src is restricted to
	// the admin's identity only — no other user (tag:private
	// or otherwise) gets in. The existing tag:exit-node rule
	// above is preserved unchanged, so private devices that
	// happen to be tagged exit-node remain reachable.
	sb.WriteString("    {\n")
	sb.WriteString("      \"action\": \"accept\",\n")
	sb.WriteString("      \"src\": [\"" + envAdminIdentity() + "@" + baseDomain + "\"],\n")
	sb.WriteString("      \"dst\": [\"tag:public\"],\n")
	sb.WriteString("      \"users\": [\"root\"]\n")
	sb.WriteString("    }\n")
	sb.WriteString("  ]\n")

	sb.WriteString("}")
	return sb.String(), nil
}
