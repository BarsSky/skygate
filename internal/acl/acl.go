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
	"strconv"
	"sort"
	"strings"

	"skygate/internal/db"
	"skygate/internal/headscale"
)

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

	const baseDomain = "tsnet.skynas.ru"
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
		// idn = "alice@tsnet.skynas.ru" — extract the
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
		sb.WriteString("] }")
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
	// matches every IP outside the tailnet's 100.64.0.0/10
	// range, so:
	//
	//   * alice → bob's device  — bob is in 100.64.0.0/10,
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
	sb.WriteString(",\n    { \"action\": \"accept\", \"src\": [\"*\"], \"dst\": [\"autogroup:internet:*\"] }")
	sb.WriteString("\n  ],\n")

	sb.WriteString("  \"tagOwners\": {\n")
	sb.WriteString("    \"tag:public\": [\"skyadmin@" + baseDomain + "\"]\n")
	// 2026-07-14: Этап 14 v7 — register tag:exit-node as
	// owned by skyadmin so the headscale parser accepts the
	// policy. The SSH rule (and the per-user ACL) references
	// this tag; without an entry in tagOwners the policy
	// load fails with "tag not found: tag:exit-node". We
	// never *apply* this tag through skygate (it stays as
	// a headplane admin task), but headscale still requires
	// the owner entry to be present in the policy file.
	// 2026-07-14: Этап 14 v7 — register tag:exit-node as
	// owned by skyadmin so the headscale parser accepts the
	// policy. The SSH rule (and the per-user ACL) references
	// this tag; without an entry in tagOwners the policy
	// load fails with "tag not found: tag:exit-node". We
	// never *apply* this tag through skygate (it stays as
	// a Headplane admin task — see docs/headplane.md), but
	// headscale still requires the owner entry to be
	// present in the policy file.
	sb.WriteString(",\n    \"tag:exit-node\": [\"skyadmin@" + baseDomain + "\"]\n")
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
	// Each user gets a tagOwners entry covering every
	// tag:dev-<user>-<device> the user owns (every
	// device in node_owner_map for that user). Without
	// these entries, the headscale parser rejects the
	// policy with "tag not found" when it hits the
	// per-device ACL rules above.
	// The output is sorted by username for stable diffs
	// across deploys (important for the operator's
	// policy audit).
	var usersWithDevTags []string
	for uname := range tagsByUser {
		usersWithDevTags = append(usersWithDevTags, uname)
	}
	sort.Strings(usersWithDevTags)
	for _, uname := range usersWithDevTags {
		tags := tagsByUser[uname]
		sort.Strings(tags) // stable order within user
		sb.WriteString(",\n    \"" + strings.Join(tags, "\",\"") + "\": [\"" + uname + "@" + baseDomain + "\"]\n")
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
	sb.WriteString("      \"src\": [\"tag:private\", \"skyadmin@" + baseDomain + "\"],\n")
	sb.WriteString("      \"dst\": [\"tag:exit-node\"],\n")
	sb.WriteString("      \"users\": [\"root\"]\n")
	sb.WriteString("    },\n")
	// 2026-07-14: Этап 14 v7 — allow admin to SSH into tag:public
	// relay nodes (emilia, sharlotta, karolina) so they can be
	// reconfigured (e.g. enable --advertise-exit-node) without
	// needing direct public-IP SSH access. src is restricted to
	// the admin's identity only — no other user (tag:private
	// or otherwise) gets in. The existing tag:exit-node rule
	// above is preserved unchanged, so private devices that
	// happen to be tagged exit-node remain reachable.
	sb.WriteString("    {\n")
	sb.WriteString("      \"action\": \"accept\",\n")
	sb.WriteString("      \"src\": [\"skyadmin@" + baseDomain + "\"],\n")
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
func ApplyACLPipeline(d *sql.DB, hs *headscale.Client, alerter Alerter, username, detailForLog string) ApplyResult {
	return ApplyACLPipelineForPlane(d, hs, "", alerter, username, detailForLog)
}

// ApplyACLPipelineForPlane runs the 4-step pipeline for ONE
// control plane. planeURL == "" means the global default
// plane. Use this directly when you have a specific
// *headscale.Client (e.g. App.HSForUser returned a per-user
// override); the caller is responsible for choosing the
// right client.
//
// 2026-07-16: v0.13.0.
func ApplyACLPipelineForPlane(d *sql.DB, hs *headscale.Client, planeURL string, alerter Alerter, username, detailForLog string) ApplyResult {
	acl, err := GenerateACLForPlane(d, planeURL)
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
func ApplyACLForAllPlanes(d *sql.DB, hsForPlane func(planeURL string) *headscale.Client, alerter Alerter, username, detailForLog string) []ApplyResult {
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
		r := ApplyACLPipelineForPlane(d, hs, p.URL, alerter, username, detailForLog)
		out = append(out, r)
	}
	return out
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

