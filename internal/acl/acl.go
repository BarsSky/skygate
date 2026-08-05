// Package acl — shared headscale ACL pipeline.
//
// 2026-07-13: Этап 11 part 2b. The "rules changed, sync to headscale"
// sequence was previously inlined in three places (web form, web
// API, web delete) plus the bot would need a fourth copy. Extracting
// it into this package lets the bot (which can't import handlers
// without a cycle) reuse the same logic AND lets future web paths
// share the helper without re-implementing the order-sensitive
// dance between GenerateACL → SaveACLSnapshot → SetPolicy →
// Mark + Log.
//
// The pipeline is intentionally narrow: it does the four DB+HS
// steps and nothing more. Caller-specific side effects
// (Notifier.SendAlert, SyncAdvertisedRoutes) stay at the call site
// because the bot skips them while the web form does both.
package acl

import (
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"sort"
	"strings"

	"skygate/internal/db"
	"skygate/internal/headscale"
)

// 2026-08-03: v0.32.29 — moved the headscale tailnet
// domain and admin identity out of source-level constants
// and into env-driven config. The defaults are placeholders
// so the public github repo carries no operator-specific DNS
// or usernames; live deployments set SKYGATE_BASE_DOMAIN
// and SKYGATE_ADMIN_IDENTITY in their .env to the real
// values.
func envBaseDomain() string {
	if d := os.Getenv("SKYGATE_BASE_DOMAIN"); d != "" {
		return d
	}
	return "tsnet.example.com"
}

func envAdminIdentity() string {
	if a := os.Getenv("SKYGATE_ADMIN_IDENTITY"); a != "" {
		return a
	}
	return "admin"
}

// Alerter is the minimal interface SaveACLSnapshot needs from a
// notifier. The full telegram.Notifier (which has SendTelegram +
// SendAlert) satisfies this implicitly — Go interfaces are
// structural. Defined locally to avoid an import cycle with
// internal/telegram (which would be the natural home but already
// depends on internal/handlers via App.Notifier).
//
// The SendAlert signature mirrors telegram.Notifier.SendAlert
// (returns int64 = alert id, 0 when not configured). SaveACLSnapshot
// discards the return value — it only needs the side effect of
// dispatching the alert.
//
// Pass nil to suppress the alert (e.g. bot path, where audit_log
// is enough and the operator doesn't need a Telegram ping for
// every /add_rule).
type Alerter interface {
	SendAlert(text string) int64
}

// NoopAlerter discards every SendAlert. Useful as a default in
// code paths that don't have a real notifier wired in.
type NoopAlerter struct{}

// SendAlert is the no-op implementation of Alerter.
func (NoopAlerter) SendAlert(string) int64 { return 0 }

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
// headscale_url = ''). The policy lists only the identities
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
			})
		}
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
	writePerDeviceGrants(&sb, usernames, tagsByUser)

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
		switch {
		case e.userName != "" && e.deviceHostname != "":
			// tag:dev-<user>-<device> — preferred, robust
			src = fmt.Sprintf("\"tag:dev-%s-%s\"", e.userName, e.deviceHostname)
		case e.deviceIP != "":
			// legacy device_ip src — works for the current
			// session, breaks on Tailscale IP change
			src = fmt.Sprintf("\"%s\"", e.deviceIP)
		}
		sb.WriteString(",\n    { \"action\": \"" + e.action + "\", \"src\": [" + src + "], \"dst\": [\"" + e.target + ":*\"] }")
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
	sb.WriteString("    \"tag:public\": [\"" + envAdminIdentity() + "@" + baseDomain + "\"]\n")
	// 2026-07-14: Этап 14 v7 — register tag:exit-node as
	// owned by admin so the headscale parser accepts the
	// policy. The SSH rule (and the per-user ACL) references
	// this tag; without an entry in tagOwners the policy
	// load fails with "tag not found: tag:exit-node". We
	// never *apply* this tag through skygate (it stays as
	// a headplane admin task), but headscale still requires
	// the owner entry to be present in the policy file.
	// 2026-07-14: Этап 14 v7 — register tag:exit-node as
	// owned by admin so the headscale parser accepts the
	// policy. The SSH rule (and the per-user ACL) references
	// this tag; without an entry in tagOwners the policy
	// load fails with "tag not found: tag:exit-node". We
	// never *apply* this tag through skygate (it stays as
	// a Headplane admin task — see docs/headplane.md), but
	// headscale still requires the owner entry to be
	// present in the policy file.
	sb.WriteString(",\n    \"tag:exit-node\": [\"" + envAdminIdentity() + "@" + baseDomain + "\"]\n")
	if len(identities) > 1 {
		sb.WriteString(",\n    \"tag:private\": [" + strings.Join(quoteAll(identities), ",") + "]\n")
	} else {
		sb.WriteString(",\n    \"tag:private\": [\"" + (identities[0]) + "\"]\n")
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
	sb.WriteString(",\n    \"tag:subnet-router\": [" + strings.Join(quoteAll(identities), ",") + "]\n")
	// 2026-07-24: v0.28.0 — per-user-per-device tags.
	// One tagOwners entry per (user, device) — headscale
	// expects each tag to have its own line. Without
	// these entries, the parser rejects the policy with
	// "tag not found" when it hits the per-device ACL
	// rules above. The output is sorted by (username,
	// tag) for stable diffs across deploys (important
	// for the operator's policy audit).
	type tagOwner struct {
		tag, owner string
	}
	var tagOwners []tagOwner
	for uname, tags := range tagsByUser {
		for _, tag := range tags {
			tagOwners = append(tagOwners, tagOwner{tag: tag, owner: uname + "@" + baseDomain})
		}
	}
	sort.Slice(tagOwners, func(i, j int) bool {
		if tagOwners[i].tag != tagOwners[j].tag {
			return tagOwners[i].tag < tagOwners[j].tag
		}
		return tagOwners[i].owner < tagOwners[j].owner
	})
	for _, to := range tagOwners {
		sb.WriteString(",\n    \"" + to.tag + "\": [\"" + to.owner + "\"]\n")
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
	sb.WriteString("      \"src\": [\"tag:private\", \"" + envAdminIdentity() + "@" + baseDomain + "\"],\n")
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

func quoteAll(ss []string) []string {
	res := make([]string, len(ss))
	for i, s := range ss {
		res[i] = strconv.Quote(s)
	}
	return res
}

// SaveACLSnapshot inserts one row into acl_snapshots and returns
// the new version. The alerter is optional — pass nil to skip the
// "🛡️ ACL #N" Telegram alert (the bot path, which records the
// change in audit_log instead).
//
// Moved out of (*App).saveACLSnapshot so the telegram bot can
// reuse it.
func SaveACLSnapshot(d *sql.DB, config, username string, alerter Alerter) int {
	ver, _ := db.NextACLVersion(d)
	_ = db.SaveACLSnapshot(d, ver, config, username)
	if alerter != nil {
		// Async to avoid blocking the caller on a Telegram API
		// round-trip. Mirrors the previous (a *App) behaviour.
		go alerter.SendAlert(fmt.Sprintf("🛡️ ACL #%d by %s\nLength: %d bytes", ver, username, len(config)))
	}
	return ver
}

// ApplyResult is the typed return of ApplyACLPipeline so callers
// can branch on "applied to headscale" without juggling three
// separate return values. Err is non-nil when GenerateACL or
// SetPolicy failed; Version is the snapshot version (always set
// on the success path, may be 0 on GenerateACL failure); Applied
// is true iff SetPolicy succeeded.
type ApplyResult struct {
	Version int
	Applied bool
	Err     error
}

// ApplyACLPipeline runs the standard "rules changed, sync to
// headscale" pipeline for the global default plane:
//
//   1. GenerateACL          — build the policy JSON from device_rules
//   2. SaveACLSnapshot      — persist the snapshot (always, so the
//                             operator can roll back even on failure)
//   3. HS.SetPolicy         — push to headscale
//   4. MarkACLApplied/Fail  + AppendExitRuleLog
//
// detailForLog is written to exit_rule_logs on both the success
// and failure path so an operator scanning the audit trail sees
// the human-readable context that triggered the sync.
//
// The Alerter receives a Telegram alert on the SaveACLSnapshot
// step (mirroring the existing web behaviour). Pass nil to skip.
// Notifier alerts for success/failure and SyncAdvertisedRoutes
// are intentionally NOT in this helper: those are caller-specific
// (the web form does both, the bot does neither for v1) and the
// caller chains them after this function returns.
//
// 2026-07-16: v0.13.0 — kept as a thin wrapper around
// ApplyACLPipelineForPlane(d, hs, "", alerter, username,
// detailForLog) so the global-default and per-plane code
// share a single implementation.
func ApplyACLPipeline(d *sql.DB, hs *headscale.Client, alerter Alerter, username, detailForLog string, useVia bool) ApplyResult {
	return ApplyACLPipelineForPlane(d, hs, "", alerter, username, detailForLog, useVia)
}

// ApplyACLPipelineForPlane runs the 4-step pipeline for ONE
// control plane. planeURL == "" means the global default
// plane. Use this directly when you have a specific
// *headscale.Client (e.g. App.HSForUser returned a per-user
// override); the caller is responsible for choosing the
// right client.
//
// 2026-07-16: v0.13.0.
// 2026-07-25: v0.28.2 — read SKYGATE_ACL_VIA_ENABLED
// from the env at call time. Most callers don't have
// access to *App (the handlers package), so threading
// a.Cfg.ACLWithViaEnabled through every call site is
// intrusive. Reading the env directly here means the
// v0.28.2 dispatch is global and consistent: ANY call
// to ApplyACLPipelineForPlane honors the env var. This
// is the right behavior for v0.28.2 — the env var is
// the operator's global "via enabled" toggle, and
// mixed acls/grants policies are a footgun.
func ApplyACLPipelineForPlane(d *sql.DB, hs *headscale.Client, planeURL string, alerter Alerter, username, detailForLog string, useVia bool) ApplyResult {
	var acl string
	var err error
	// If the caller explicitly passed useVia, use
	// that. If useVia is the zero value (false),
	// check the env var — that way existing
	// call sites that pass `false` as the
	// legacy default still honor the operator's
	// global toggle. (v0.28.1 docs noted this
	// behavior; v0.28.2 makes it the actual
	// default.)
	if !useVia {
		useVia = os.Getenv("SKYGATE_ACL_VIA_ENABLED") == "true"
	}
	if useVia {
		acl, err = GenerateACLWithViaForPlane(d, planeURL)
	} else {
		acl, err = GenerateACLForPlane(d, planeURL)
	}
	if err != nil {
		return ApplyResult{Version: 0, Applied: false, Err: fmt.Errorf("generate ACL: %w", err)}
	}
	ver := SaveACLSnapshot(d, acl, username, alerter)
	if setErr := hs.SetPolicy(acl); setErr != nil {
		db.MarkACLFail(d, ver, setErr.Error())
		db.AppendExitRuleLog(d, ver, db.ExitRuleActionApplyFail, detailForLog+": "+setErr.Error())
		return ApplyResult{Version: ver, Applied: false, Err: setErr}
	}
	db.MarkACLApplied(d, ver)
	db.AppendExitRuleLog(d, ver, db.ExitRuleActionApply, detailForLog)
	return ApplyResult{Version: ver, Applied: true, Err: nil}
}

// ApplyACLForAllPlanes iterates every distinct control plane
// (one entry per distinct headscale_url, plus the global
// default) and runs ApplyACLPipelineForPlane on each, using
// the per-plane *headscale.Client the closure returns. The
// single global pipeline that was wired into the web form
// pre-v0.13.0 is now the union of all per-plane pipelines
// — same operator-visible behaviour (every plane's policy
// gets pushed) but scoped to the right headscale instance.
//
// 2026-07-16: v0.13.0.
//
// hsForPlane is called once per distinct plane; the caller
// typically binds `a.HSForUser` style logic that reads
// portal_users.headscale_url + headscale_api_key_enc and
// returns the cached client (or the global fallback for the
// "" URL). The alerter is shared across planes so a
// single "🛡️ ACL #N by <user>" alert covers the run.
func ApplyACLForAllPlanes(d *sql.DB, hsForPlane func(planeURL string) *headscale.Client, alerter Alerter, username, detailForLog string, useVia bool) []ApplyResult {
	planes, err := db.ListControlPlanes(d)
	if err != nil {
		return []ApplyResult{{Version: 0, Applied: false, Err: fmt.Errorf("list control planes: %w", err)}}
	}
	out := make([]ApplyResult, 0, len(planes))
	for _, p := range planes {
		hs := hsForPlane(p.URL)
		if hs == nil {
			// No client for this plane (e.g. SKYGATE_SECRET_KEY
			// is missing or the per-plane key is corrupt).
			// Skip — single-plane deploys never hit this branch.
			out = append(out, ApplyResult{Version: 0, Applied: false, Err: fmt.Errorf("no headscale client for plane %q", p.URL)})
			continue
		}
		r := ApplyACLPipelineForPlane(d, hs, p.URL, alerter, username, detailForLog, useVia)
		out = append(out, r)
	}
	return out
}

// GenerateACLWithViaForPlane builds the grants-based policy
// for ONE control plane. planeURL == "" means the global
// default plane.
//
// 2026-07-24: v0.28.1.
func GenerateACLWithViaForPlane(d *sql.DB, planeURL string) (string, error) {
	aclRows, err := db.GetACLEntries(d)
	if err != nil {
		return "", err
	}

	baseDomain := envBaseDomain()

	usernames, err := db.GetPortalUsernamesForPlane(d, planeURL)
	if err != nil {
		return "", err
	}

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

	meshMemberships, err := db.GetMeshMembershipsForPlane(d, planeURL)
	if err != nil {
		return "", err
	}
	for _, mm := range meshMemberships {
		if mm.SelfUser != "" && mm.OtherCIDR != "" {
			sharedByUser[mm.SelfUser] = append(sharedByUser[mm.SelfUser], mm.OtherCIDR)
		}
	}

	exitPrefs, err := db.ListAllUserExitNodePrefs(d)
	if err != nil {
		return "", err
	}
	viaByUser := make(map[string]string, len(exitPrefs))
	for _, ep := range exitPrefs {
		// 2026-07-25: v0.28.5 — only emit via for users
		// that have via_enabled=1. The opt-out default
		// (via_enabled=0) is the Android-friendly path:
		// the per-user grant has dst=autogroup:internet
		// with NO via, so the user can use any exit-node
		// (no headscale packet-filter pinning).
		if ep.Username != "" && ep.ExitNodeTag != "" && ep.ViaEnabled {
			viaByUser[ep.Username] = ep.ExitNodeTag
		}
	}

	// 2026-07-25: v0.28.4 — per-device preferred exit-node.
	// A row in device_exit_node_prefs means a specific device
	// has its own via override, independent of the user's
	// per-user pref. The ACL builder emits a per-device grant
	// BEFORE the per-user grant (Tailscale first-match wins),
	// with src=tag:dev-<user>-<device> and via=[<device-pref>].
	// The per-device grant covers autogroup:internet only —
	// the user's own stuff (own devices, own subnet) is still
	// covered by the per-user grant below.
	devicePrefs, err := db.ListAllDeviceExitNodePrefs(d)
	if err != nil {
		return "", err
	}
	// viaByDevice is keyed on the device tag (e.g.
	// "tag:dev-admin-workstation-3") so the per-device grant
	// builder can look up the via in O(1).
	viaByDevice := make(map[string]string, len(devicePrefs))
	for _, dp := range devicePrefs {
		// 2026-07-25: v0.28.5 — only emit per-device
		// grants for devices that have via_enabled=1.
		// The opt-out default (via_enabled=0) means
		// the device falls back to the per-user grant
		// (which itself may or may not have via, per
		// its own via_enabled flag). This is the same
		// opt-in model as the per-user prefs.
		if dp.Username == "" || dp.DeviceHostname == "" || dp.ExitNodeTag == "" || !dp.ViaEnabled {
			continue
		}
		// The tag is "tag:dev-<user>-<device-lowercased>".
		// v0.28.0 backfill lowercases the hostname when
		// emitting the tag — we do the same here so the
		// lookup matches the per-device ACL src exactly.
		devTag := "tag:dev-" + dp.Username + "-" + strings.ToLower(dp.DeviceHostname)
		viaByDevice[devTag] = dp.ExitNodeTag
	}

	devTags, err := db.GetPerUserDeviceTags(d, planeURL)
	if err != nil {
		return "", err
	}
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

	// 2026-07-25: v0.28.2 — headscale 0.29.2 grants
	// parser (policy v2 / AliasEnc) does NOT accept
	// CIDR:port in dst. Workaround: emit each CIDR
	// referenced by a grant as a host alias in the
	// `hosts:` block, then reference the alias in
	// the grant's dst. Pre-collect all (name, cidr)
	// pairs we need across the whole policy (per-user
	// subnets + shared + mesh) and emit one host entry
	// per unique pair BEFORE the grants block.
	//
	// Naming convention: "h-user-<uname>-subnet" for
	// personal subnets, "h-shared-<sanitized-cidr>" for
	// shared / mesh entries. The "h-" prefix is unique
	// enough to never collide with a username, group,
	// tag, or autogroup (which all use their own
	// reserved prefixes per headscale's parseAlias).
	type hostEntry struct {
		name string
		cidr string
	}
	seenHost := make(map[string]bool)
	var hosts []hostEntry
	addHost := func(name, cidr string) {
		if name == "" || cidr == "" {
			return
		}
		key := name + "|" + cidr
		if seenHost[key] {
			return
		}
		seenHost[key] = true
		hosts = append(hosts, hostEntry{name: name, cidr: cidr})
	}
	for _, uname := range usernames {
		if uname == "" {
			continue
		}
		if ownCIDR := subByUser[uname]; ownCIDR != "" {
			addHost("h-user-"+uname+"-subnet", ownCIDR)
		}
	}
	// Shared / mesh CIDRs (deduplicated by the addHost
	// closure). Iterate every user's sharedByUser to
	// catch both v0.17.1 share rows and v0.22.0 mesh
	// memberships (both feed into sharedByUser in
	// the pre-pass above).
	for _, cidrs := range sharedByUser {
		for _, cidr := range cidrs {
			if cidr == "" {
				continue
			}
			// Sanitize the CIDR for use as a host
			// alias name: "." and "/" → "-", ":" (IPv6)
			// → "_". The result is unique per
			// CIDR (dedup via seenHost).
			name := "h-shared-" + strings.NewReplacer(
				".", "-", "/", "-", ":", "_").Replace(cidr)
			addHost(name, cidr)
		}
	}
	// 2026-07-25: v0.28.2 — per-device rule targets
	// (Telegram CIDRs, custom IPs) ALSO need host
	// aliases. Pre-collect them here so the hosts
	// block (emitted below) contains every alias
	// referenced in the grants block.
	for _, e := range aclRows {
		if e.TargetType != "subnet" && e.TargetType != "ip" {
			continue
		}
		if e.Action != "accept" {
			continue
		}
		if e.TargetValue == "" {
			continue
		}
		ruleAlias := "h-rule-" + strings.NewReplacer(
			".", "-", "/", "-", ":", "_").Replace(e.TargetValue)
		addHost(ruleAlias, e.TargetValue)
	}

	var sb strings.Builder
	sb.WriteString("{\n  \"hosts\": {\n")
	// Emit the hosts block. headscale accepts an
	// empty {} object but the v2 parser is strict
	// about the JSON shape — for safety we always
	// emit at least one entry. When no per-user /
	// shared / mesh CIDRs exist, we emit a single
	// placeholder entry pointing at an RFC 5737
	// documentation range (TEST-NET-1, 192.0.2.0/24)
	// so headscale doesn't reject the policy for
	// being malformed. The placeholder never appears
	// in any grant's dst — it's purely a syntactic
	// anchor.
	first := true
	if len(hosts) == 0 {
		sb.WriteString("    \"_placeholder\": \"0.0.0.0/32\"")
		first = false
	}
	for _, h := range hosts {
		if first {
			sb.WriteString("    \"" + h.name + "\": \"" + h.cidr + "\"")
			first = false
		} else {
			sb.WriteString(",\n    \"" + h.name + "\": \"" + h.cidr + "\"")
		}
	}
	sb.WriteString("\n  },\n")

	sb.WriteString("  \"grants\": [\n")

	// 2026-07-25: v0.28.4 — per-device preferred exit-node
	// grants. Emitted FIRST so Tailscale's first-match
	// semantics pick the per-device via over the per-user
	// via. The per-device grant is narrower (src is the
	// exact device tag, not the user identity) AND has
	// higher priority by virtue of position in the list.
	//
	// Per-device grant shape:
	//   { "src": ["tag:dev-<user>-<device>"],
	//     "dst": ["autogroup:internet"],
	//     "ip":  ["*"],
	//     "via": ["<device-pref>"] }
	//
	// dst is JUST autogroup:internet — the per-user grant
	// below covers the user's own stuff (own devices, own
	// subnet, shared/mesh CIDRs). The per-device grant
	// exists ONLY to override the via for autogroup:internet
	// (exit-node routing).
	//
	// Why emit per-device grants before per-user grants:
	// Tailscale ACL is order-sensitive (first match wins).
	// workstation-3 (tag:dev-admin-workstation-3) has a per-device via for
	// relay-3. With per-device grant first, workstation-3's packets
	// to autogroup:internet match the per-device grant
	// (src=tag:dev-admin-workstation-3, dst=autogroup:internet,
	// via=tag:exit-relay-3) and use relay-3. Without the
	// per-device grant (or with it AFTER the per-user
	// grant), workstation-3 would fall through to the per-user grant
	// (src=admin@<baseDomain>, via=tag:exit-relay-1) and be
	// pinned to relay-1.
	//
	// Devices WITHOUT a per-device pref don't get a
	// per-device grant — they fall through to the per-user
	// grant (and inherit the user's via, if any).
	perDeviceGrantEmitted := false
	for devTag, via := range viaByDevice {
		if perDeviceGrantEmitted {
			sb.WriteString(",\n")
		}
		sb.WriteString("    { \"src\": [\"" + devTag + "\"], \"dst\": [\"autogroup:internet\"], \"ip\": [\"*\"], \"via\": [\"" + via + "\"] }")
		perDeviceGrantEmitted = true
	}

	for i, idn := range identities {
		if i > 0 {
			sb.WriteString(",\n")
		} else if perDeviceGrantEmitted {
			// The per-device block above wrote its last
			// entry WITHOUT a trailing comma (the loop
			// pattern is "leading separator" — comma
			// before every entry except the first).
			// The first per-user grant needs a leading
			// separator to keep the JSON list valid.
			sb.WriteString(",\n")
		}
		uname := strings.SplitN(idn, "@", 2)[0]
		dst := []string{idn + ":*"}
		// 2026-07-25: v0.28.2 — reference the
		// pre-collected host alias instead of the
		// raw CIDR. The alias resolves to the
		// same IP range at headscale load time;
		// the only difference is that the v2
		// policy parser accepts a host alias in
		// dst but not a CIDR+port.
		if ownCIDR := subByUser[uname]; ownCIDR != "" {
			// 2026-07-25: v0.28.2 — headscale 0.29.2's
			// parseAlias does NOT split the alias and
			// the port. The dst string "h-user-X:*"
			// gets passed to parseAlias whole, and
			// isHost("h-user-X:*") returns false
			// because the host is defined without
			// the :* suffix. The fix: drop the :*
			// from dst; the `ip: ["*"]` below
			// already means "any port", so the dst
			// can be the bare alias.
			dst = append(dst, "h-user-"+uname+"-subnet")
		}
		dedupSet := make(map[string]bool, len(dst))
		for _, d := range dst {
			dedupSet[d] = true
		}
		for _, sharedCIDR := range sharedByUser[uname] {
			if sharedCIDR == "" {
				continue
			}
			// Same sanitization as the hosts
			// pre-pass above so the alias names
			// match exactly.
			hostName := "h-shared-" + strings.NewReplacer(
				".", "-", "/", "-", ":", "_").Replace(sharedCIDR)
			// 2026-07-25: v0.28.2 — same as
			// above: drop the :* (ip: ["*"] covers
			// any port).
			if dedupSet[hostName] {
				continue
			}
			dedupSet[hostName] = true
			dst = append(dst, hostName)
		}
		sb.WriteString("    { \"src\": [\"" + idn + "\"], \"dst\": [")
		for j, d := range dst {
			if j > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString("\"")
			sb.WriteString(d)
			sb.WriteString("\"")
		}
		// 2026-07-25: v0.28.3 — add autogroup:internet
		// to the per-user grant dst. Combined with the
		// catch-all change below (src=tag:public, not
		// src=*), this closes the catch-all bypass
		// where any device could use any exit-node
		// (relay-1/relay-2/relay-3) for arbitrary
		// internet destinations — including relay-3's
		// 148 PrimaryRoutes. workstation-1 (tag:dev-admin-workstation-1
		// → admin@<baseDomain>) was the example: without
		// per-device rules, workstation-1 could reach workstation-2's
		// internet resources through relay-3's
		// subnet-route + exit-node path.
		//
		// The per-user grant now matches workstation-1's packets
		// (src resolves to admin@<baseDomain> via tagOwners),
		// and via=[tag:exit-relay-1] pins the exit-node
		// choice. workstation-3 CAN still reach autogroup:internet
		// (via the per-user grant), but ONLY through
		// relay-1 — relay-3 is locked out by the via
		// constraint at the headscale packet filter.
		sb.WriteString(", \"autogroup:internet\"")
		sb.WriteString("], \"ip\": [\"*\"]")
		if via := viaByUser[uname]; via != "" {
			sb.WriteString(", \"via\": [\"" + via + "\"]")
		}
		sb.WriteString(" }")
	}

	// 2026-07-27: v0.29.0 — per-user grant for TAGGED
	// devices. The grant above uses src=user@ which in
	// Tailscale v2 policy does NOT match tagged devices
	// (every device since v0.28.0 carries
	// tag:dev-<user>-<device>; the device's source
	// identity in ACL is the TAG, not the user). Symptom
	// observed in production (operator, 2026-07-27):
	// workstation-2 (Android, tag:dev-admin-workstation-2) could
	// not reach workstation-1 (Windows, tag:dev-admin-
	// workstation-1) over Tailscale for Moonlight, because
	// no rule had src=tag:dev-admin-* and dst=tag:dev-
	// admin-*. The per-user grant was a no-op for
	// every device in the tailnet.
	//
	// FIX: emit a per-DEVICE grant per user. Earlier
	// design (reverted) tried src=tag:dev-<user>-*
	// (wildcard). headscale 0.29.2 rejects wildcards
	// in tagOwners with "src=tag not found". v0.29.0
	// uses CONCRETE per-device tags (which headscale
	// accepts because they were registered by the
	// v0.28.0 backfillNodeOwnership auto-tag) and
	// emits one per-DEVICE grant with dst = list of
	// all OTHER devices of the same user.
	//
	// 7 grants for admin (one per device). 2 grants
	// for user1. O(n) per user, not O(n²).
	//
	// Implementation: see writePerDeviceGrants for the
	// shared helper (v0.30.0 refactor). Same code
	// shape as GenerateACLForPlane; the duplication
	// introduced by the v0.28.7 fix is now consolidated.
	writePerDeviceGrants(&sb, usernames, tagsByUser)

	for _, e := range aclRows {
		if e.TargetType != "subnet" && e.TargetType != "ip" {
			continue
		}
		if e.Action != "accept" {
			continue
		}
		src := "\"*\""
		switch {
		case e.UserName != "" && e.DeviceHostname != "":
			src = fmt.Sprintf("\"tag:dev-%s-%s\"", e.UserName, e.DeviceHostname)
		case e.DeviceIP != "":
			src = fmt.Sprintf("\"%s\"", e.DeviceIP)
		}
		// 2026-07-25: v0.28.2 — per-device rules
		// reference the host alias (pre-collected
		// above). Bare alias in dst (no :*) — the
		// v2 parser doesn't split alias:port.
		ruleAlias := "h-rule-" + strings.NewReplacer(
			".", "-", "/", "-", ":", "_").Replace(e.TargetValue)
		sb.WriteString(",\n    { \"src\": [" + src + "], \"dst\": [\"" + ruleAlias + "\"], \"ip\": [\"*\"] }")
	}

	// 2026-07-25: v0.28.5b — loose per-device grants.
	//
	// WHY: Tailscale v2 policy uses first-match semantics
	// on `src`. For a TAGGED device (every device since
	// v0.28.0 carries tag:dev-<user>-<device>), the source
	// must be the device's tag (or a tag the user owns via
	// tagOwners). The per-user grant above uses src=user@,
	// which in v2 does NOT match tagged devices. The
	// per-device rule loop above is for specific targets
	// (Telegram IPs etc.), NOT for autogroup:internet.
	//
	// The v0.28.4 per-device grant loop only emitted when
	// the device had a per-device pref AND via_enabled=1.
	// For all other devices (e.g. skygate-host-1 — the skygate
	// container's own tailnet client — which never had a
	// per-device pref), no grant covers autogroup:internet
	// and exit-node routing is silently REJECTED by the
	// client. Symptom: 100% packet loss on skygate-host-1
	// after v0.28.3 deploy, even with the per-user grant
	// dst including autogroup:internet.
	//
	// FIX: for every device tag the user owns (one row per
	// device in devTags), emit a grant with src=tag:dev-...,
	// dst=autogroup:internet, NO via. This is the loose
	// default — every device can reach the public internet
	// through whatever exit-node the user picks in the
	// Tailscale client. The per-device pref grant above
	// (when via_enabled=1) is the more specific override
	// that pins the device to a specific exit-node.
	//
	// Order: emitted AFTER per-device rules (which are
	// for specific dst like h-rule-91-108-12-0-22) and
	// AFTER the per-user grant. This way:
	//   1. Per-device rules (most specific dst) win for
	//      their exact targets.
	//   2. Per-device pref grant (with via) wins for
	//      autogroup:internet when set.
	//   3. Per-user grant (with via) wins for the user's
	//      other (un-tagged) devices and own dst.
	//   4. This loose per-device grant is the fallback for
	//      tagged devices WITHOUT a per-device pref.
	//   5. Catch-alls last.
	for _, uname := range usernames {
		if uname == "" {
			continue
		}
		for _, devTag := range tagsByUser[uname] {
			sb.WriteString(",\n    { \"src\": [\"" + devTag + "\"], \"dst\": [\"autogroup:internet\"], \"ip\": [\"*\"] }")
		}
	}

	// 2026-07-25: v0.28.2 — catch-all dst references
	// dropped the :* suffix too. The "tag:public" /
	// "tag:exit-node" / "autogroup:internet" aliases
	// are accepted by parseAlias (isTag / isAutogroup),
	// but the trailing :* breaks the v2 parser. With
	// ip: ["*"] the dst is "any port" anyway.
	sb.WriteString(",\n    { \"src\": [\"*\"], \"dst\": [\"tag:public\"], \"ip\": [\"*\"] }")
	sb.WriteString(",\n    { \"src\": [\"*\"], \"dst\": [\"tag:exit-node\"], \"ip\": [\"*\"] }")
	// 2026-07-25: v0.28.3 — autogroup:internet catch-all
	// restricted to src=tag:public. The legacy src=*
	// catch-all let any device use any exit-node for
	// arbitrary internet destinations, including
	// relay-3's 148 PrimaryRoutes (Telegram/Google/
	// Cloudflare/etc.). With the per-user grant above
	// now including autogroup:internet, every end-user
	// device already has its own grant that covers
	// public internet egress (and in the via=true path,
	// that grant has via=[<preferred>], pinning the
	// exit-node). The catch-all below exists only so
	// tag:public relay nodes can FORWARD their own
	// exit-node traffic to the actual internet — the
	// per-user grants above don't cover src=tag:public,
	// so without this the relays couldn't forward.
	// See GenerateACLForPlane comment for the full
	// bypass analysis.
	sb.WriteString(",\n    { \"src\": [\"tag:public\"], \"dst\": [\"autogroup:internet\"], \"ip\": [\"*\"] }")
	sb.WriteString("\n  ],\n")

	sb.WriteString("  \"tagOwners\": {\n")
	sb.WriteString("    \"tag:public\": [\"" + envAdminIdentity() + "@" + baseDomain + "\"]")
	sb.WriteString(",\n    \"tag:exit-node\": [\"" + envAdminIdentity() + "@" + baseDomain + "\"]")
	if len(identities) > 1 {
		sb.WriteString(",\n    \"tag:private\": [" + strings.Join(quoteAll(identities), ",") + "]")
	} else {
		sb.WriteString(",\n    \"tag:private\": [\"" + identities[0] + "\"]")
	}
	sb.WriteString(",\n    \"tag:subnet-router\": [" + strings.Join(quoteAll(identities), ",") + "]")

	distinctVias := make(map[string]bool, len(viaByUser))
	for _, via := range viaByUser {
		distinctVias[via] = true
	}
	// 2026-08-05 v0.33.1.15 — also include via tags from
	// per-device prefs. The per-device grant loop above
	// emits a grant with via:[<device-pref>] for every
	// row in device_exit_node_prefs. headscale requires
	// every tag referenced (in grants[].src, grants[].dst,
	// AND grants[].via) to be present in tagOwners,
	// otherwise the policy parser rejects with "tag not
	// found". Pre-v0.33.1.15 the distinctVias block only
	// covered the per-user prefs (viaByUser), so a fresh
	// per-device pref would silently break the apply.
	for _, via := range viaByDevice {
		distinctVias[via] = true
	}
	var exitNodeTags []string
	for tag := range distinctVias {
		exitNodeTags = append(exitNodeTags, tag)
	}
	sort.Strings(exitNodeTags)
	for _, tag := range exitNodeTags {
		sb.WriteString(",\n    \"" + tag + "\": [\"" + envAdminIdentity() + "@" + baseDomain + "\"]")
	}

	type perDevTagOwner struct {
		tag, owner string
	}
	// 2026-08-05 v0.33.1.15 — the per-device-pref device
	// tags (from viaByDevice) need to be in tagOwners too.
	// Pre-v0.33.1.15 the perDevTagOwners block was built
	// ONLY from GetPerUserDeviceTags (a JOIN on
	// node_owner_map), so devices that had a per-device
	// pref but were missing from node_owner_map (e.g. the
	// skygate-host-1 host node before it gets backfilled)
	// would produce a per-device grant with src=tag:dev-
	// <user>-<device> that the parser couldn't find in
	// tagOwners. Symptom: every ACL apply for the last
	// 22+ hours has been failing with "headscale PUT
	// /api/v1/policy: 500 src=tag not found:
	// tag:dev-skyadmin-skygate-host-1". The fix: also
	// include every per-device-pref's tag in tagOwners.
	augmentedTagsByUser := make(map[string][]string, len(tagsByUser))
	for k, v := range tagsByUser {
		augmentedTagsByUser[k] = append([]string(nil), v...)
	}
	for devTag := range viaByDevice {
		// parse username from "tag:dev-<user>-<device>"
		const prefix = "tag:dev-"
		if !strings.HasPrefix(devTag, prefix) {
			continue
		}
		rest := strings.TrimPrefix(devTag, prefix)
		idx := strings.Index(rest, "-")
		if idx < 0 {
			continue
		}
		uname := rest[:idx]
		// dedupe
		already := false
		for _, t := range augmentedTagsByUser[uname] {
			if t == devTag {
				already = true
				break
			}
		}
		if !already {
			augmentedTagsByUser[uname] = append(augmentedTagsByUser[uname], devTag)
		}
	}
	var perDevTagOwners []perDevTagOwner
	for uname, tags := range augmentedTagsByUser {
		for _, tag := range tags {
			perDevTagOwners = append(perDevTagOwners, perDevTagOwner{tag: tag, owner: uname + "@" + baseDomain})
		}
	}
	sort.Slice(perDevTagOwners, func(i, j int) bool {
		if perDevTagOwners[i].tag != perDevTagOwners[j].tag {
			return perDevTagOwners[i].tag < perDevTagOwners[j].tag
		}
		return perDevTagOwners[i].owner < perDevTagOwners[j].owner
	})
	for _, to := range perDevTagOwners {
		sb.WriteString(",\n    \"" + to.tag + "\": [\"" + to.owner + "\"]")
	}
	sb.WriteString("\n  },\n")

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
	sb.WriteString("      \"src\": [\"tag:private\", \"" + envAdminIdentity() + "@" + baseDomain + "\"],\n")
	sb.WriteString("      \"dst\": [\"tag:exit-node\"],\n")
	sb.WriteString("      \"users\": [\"root\"]\n")
	sb.WriteString("    },\n")
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

// SetACLForAllPlanes pushes a PRE-BUILT policy (e.g. one
// loaded from disk by /admin/acls/import) to every plane
// and writes an acl_snapshots row. Skips the GenerateACL
// step — the caller already has the JSON.
//
// 2026-07-16: v0.13.0 — ACL import/export. The dry-run page
// shows the imported policy next to the current one; when
// the operator clicks "Apply", this function pushes it to
// every plane in one go.
func SetACLForAllPlanes(d *sql.DB, hsForPlane func(planeURL string) *headscale.Client, alerter Alerter, username, detailForLog, policy string) []ApplyResult {
	planes, err := db.ListControlPlanes(d)
	if err != nil {
		return []ApplyResult{{Version: 0, Applied: false, Err: fmt.Errorf("list control planes: %w", err)}}
	}
	out := make([]ApplyResult, 0, len(planes))
	for _, p := range planes {
		hs := hsForPlane(p.URL)
		if hs == nil {
			out = append(out, ApplyResult{Version: 0, Applied: false, Err: fmt.Errorf("no headscale client for plane %q", p.URL)})
			continue
		}
		// Save snapshot (always, so the operator can roll
		// back even on failure).
		ver := SaveACLSnapshot(d, policy, username, alerter)
		if setErr := hs.SetPolicy(policy); setErr != nil {
			db.MarkACLFail(d, ver, setErr.Error())
			db.AppendExitRuleLog(d, ver, db.ExitRuleActionApplyFail, detailForLog+": "+setErr.Error())
			out = append(out, ApplyResult{Version: ver, Applied: false, Err: setErr})
			continue
		}
		db.MarkACLApplied(d, ver)
		db.AppendExitRuleLog(d, ver, db.ExitRuleActionApply, detailForLog)
		out = append(out, ApplyResult{Version: ver, Applied: true, Err: nil})
	}
	return out
}

