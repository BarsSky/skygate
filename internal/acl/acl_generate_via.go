// acl_generate_via.go — the via= variant of the generator.
//
// Split out of acl.go in refactor Phase D (2026-10-01). GenerateACLWithViaForPlane
// is ~880 lines: the B188.2/B275 per-CIDR `via=` pinning variant of the builder
// in acl_generate.go. Kept as a pure move — the two builders are near-duplicates
// and de-duplicating them is a behavioural change that needs its own block and
// its own live verification, not a ride-along in a file split.

package acl

import (
	"database/sql"
	"fmt"
	"log"
	"sort"
	"strings"

	"skygate/internal/db"
	"skygate/internal/prefixowner"
)

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
	// B284: resolve the device tag from node_owner_map (the tag the node
	// actually carries and the one this generator declares in tagOwners).
	// Synthesising `tag:dev-<username>-<host>` from the pref row minted
	// `tag:dev-tagged-devices-<host>` on the live host — headscale's synthetic
	// owner — and headscale refuses the WHOLE policy that references it
	// ("tag not found"): in file mode the daemon then will not start at all.
	tagsByHost := prefixowner.TagsByHost(d)
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
		// B284: node_owner_map.tag for this device; skip the pref when the node
		// has no tag, because an invented tag cannot be declared anywhere.
		devTag := strings.TrimSpace(tagsByHost[strings.ToLower(strings.TrimSpace(dp.DeviceHostname))])
		if devTag == "" {
			continue
		}
		viaByDevice[devTag] = dp.ExitNodeTag
	}
	// B275: prefix → owning relay tag, from the assignment table. Used
	// below to pin a per-CIDR grant to the relay that can actually serve
	// the prefix (see prefixowner.ViaForPrefix).
	ownerTagByPrefix := prefixowner.TagByPrefix(d)
	// B356: relay → is it usable (B273 predicate). Read once per generation; see
	// acl_relay_health_b356.go for why an unusable preferred relay must lose its
	// `via` pin instead of filtering the device's egress away.
	relayHealth := relayVerdicts(d)

	devTags, err := db.GetPerUserDeviceTags(d, planeURL)
	if err != nil {
		return "", err
	}
	tagsByUser := make(map[string][]string, len(devTags))
	for _, dt := range devTags {
		tagsByUser[dt.Username] = append(tagsByUser[dt.Username], dt.Tag)
	}

	// B265 (2026-09-19) — device_rules → device tag fallback.
	//
	// device_rules carries denormalised user_name / device_hostname
	// columns, but on the reference VM 173 of 328 enabled rows had an
	// EMPTY user_name and 52 an empty device_hostname (legacy rows
	// from before v0.44, plus rows written by the autoupdater). Those
	// rows fell through to the `device_ip` branch of the rule loop,
	// which is harmful for two reasons:
	//
	//   1. A non-tag `src` (a raw 100.64.x.y) is a different selector
	//      than the device's tag, so it can never match the
	//      per-device grants — headscale's ViaRoutesForPeer drops the
	//      exit-route EXCLUDE for a prefix as soon as ANY non-via
	//      grant from that viewer overlaps it. The matching tag-src
	//      grant (which carries via=[preferred]) was therefore
	//      neutralised, i.e. the "preferred exit node" was not
	//      enforced for exactly the devices with the most rules.
	//   2. A raw device IP src dies the moment the node's tailnet IP
	//      changes (re-registration), silently breaking the rule.
	//
	// node_owner_map is the authoritative node → owner mapping
	// (populated by nodeownership.Backfill from the live headscale
	// node list), and its hostnames are matched case-insensitively
	// because the tag minted on the node is lowercased.
	ownerByNodeID := resolveNodeOwners(d)

	// B361 (2026-10-07): EVERY prefix the device's own enabled ip/subnet rules
	// claim, in ONE pass over aclRows — the input the per-device pin needs in order
	// to ask "does the preferred relay own anything this device's rules cover?".
	// Built from the same `deviceTagForRule` resolution as the per-CIDR grant loop
	// below, so a rule whose denormalised owner columns are empty is attributed
	// correctly instead of silently dropping the device into the no-rules branch.
	claimedByDevice := map[string]map[string]bool{}
	for _, e := range aclRows {
		if e.TargetType != "subnet" && e.TargetType != "ip" {
			continue
		}
		if e.Action != "accept" || e.TargetValue == "" {
			continue
		}
		tag := deviceTagForRule(e, ownerByNodeID)
		if tag == "" {
			continue
		}
		set := claimedByDevice[tag]
		if set == nil {
			set = map[string]bool{}
			claimedByDevice[tag] = set
		}
		set[e.TargetValue] = true
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

	// v1.5.2 (B188.2): REMOVED the per-device
	// autogroup:internet grant with via=[exit_node_tag]
	// (which used to live here). The pre-B188.2 design
	// pinned autogroup:internet to the device's per-device
	// exit_node_pref — which defeated the purpose of
	// /my/exit-rules. The user-facing UI is a selective
	// routing mechanism: "youtube.com via emilia,
	// banking.com direct". If we pin the catch-all to
	// emilia, EVERY traffic goes through emilia and the
	// per-CIDR rules are redundant.
	//
	// Post-B188.2: the per-CIDR grant loop below
	// (around line 1246) adds via=[exit_node_tag] to
	// each rule whose exit_node matches the device's
	// per-device pref. That's the selective pin. The
	// autogroup:internet catch-all is emitted at line
	// 1314+ with NO via — non-pinned traffic goes
	// direct. (The "loose" per-device grant at line
	// 1314 was always there; it just was being
	// shadowed by the via-pinned grant we removed.)
	//
	// viaByDevice is still populated above (line 919+)
	// and consumed by the per-CIDR grant loop below.
	// We just don't emit the catch-all pin here anymore.

	for i, idn := range identities {
		if i > 0 {
			sb.WriteString(",\n")
		}
		// v1.5.2 (B188.2): removed the per-device
		// autogroup:internet block above, so the
		// first per-user grant is also the first grant
		// in the list (no leading comma needed). The
		// previous `else if perDeviceGrantEmitted`
		// branch is gone with that block.
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
	//
	// B316: same resolved mesh source as the live-format generator (see the comment
	// there) — an ownership row whose username is headscale's synthetic
	// `tagged-devices` must not cost the device its device-to-device grant.
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
	// infra-owned exit nodes (mirrors the same block in
	// GenerateACLForPlane). See getInfraExitNodeTags
	// in acl_perdevice.go for the full rationale.
	infraExitTags := getInfraExitNodeTags(tagsByUser)
	for _, exitTag := range infraExitTags {
		sb.WriteString(",\n    { \"src\": [\"*\"], \"dst\": [\"" + exitTag + "\"], \"ip\": [\"*\"] }")
	}

	// B285 (2026-09-22): every tag a grant names must end up in tagOwners —
	// headscale rejects the whole document otherwise, and on a
	// `policy.mode: file` host that means the daemon will not start. Collect
	// them here; the sweep at the end of this function declares any the other
	// blocks did not cover.
	grantTags := map[string]bool{}
	for _, e := range aclRows {
		if e.TargetType != "subnet" && e.TargetType != "ip" {
			continue
		}
		if e.Action != "accept" {
			continue
		}
		src := "\"*\""
		devTag := deviceTagForRule(e, ownerByNodeID) // tag:dev-<user>-<device>; "" when unresolvable
		if devTag != "" {
			grantTags[devTag] = true
		}
		switch {
		case devTag != "":
			// B265: prefer the per-device TAG as src, resolving the
			// owner from node_owner_map when device_rules' denormalised
			// user_name/device_hostname are empty (173/328 live rows).
			// A raw device-IP src is a different selector and cancels
			// the `via` exit-node pin — see deviceTagForRule.
			src = "\"" + devTag + "\""
		case e.DeviceIP != "":
			src = fmt.Sprintf("\"%s\"", e.DeviceIP)
		}
		// 2026-07-25: v0.28.2 — per-device rules
		// reference the host alias (pre-collected
		// above). Bare alias in dst (no :*) — the
		// v2 parser doesn't split alias:port.
		ruleAlias := "h-rule-" + strings.NewReplacer(
			".", "-", "/", "-", ":", "_").Replace(e.TargetValue)
		// v1.5.2 (B188.2): per-CIDR exit-node pin. The
		// user-facing /my/exit-rules UI is a SELECTIVE
		// routing mechanism: "youtube.com via emilia,
		// banking.com direct". For each per-device rule,
		// attach a `via=[exit_node_tag]` constraint ONLY
		// when the device's per-device exit_node_pref
		// matches the rule's exit_node. See
		// resolvePerCIDRVia (above) for the matching
		// logic — the same helper is used by both
		// GenerateACLWithViaForPlane (this loop) and
		// GenerateACLForPlane (the useVia=false path,
		// added in B188.3). The catch-all autogroup:internet
		// stays UNPINNED in both paths.
		viaForGrant := resolvePerCIDRVia(devTag, e.ExitNodeID, viaByDevice)
		// B275: the assignment table wins. A rule that names a relay
		// which does not own the prefix would otherwise be pinned to a
		// relay headscale never routes the prefix through, which blocks
		// the device outright (live: skyworker's rutracker/Cloudflare).
		if owner := prefixowner.ViaForPrefix(e.TargetValue, ownerTagByPrefix); owner != "" {
			viaForGrant = owner
		}
		if viaForGrant != "" {
			sb.WriteString(",\n    { \"src\": [" + src + "], \"dst\": [\"" + ruleAlias + "\"], \"ip\": [\"*\"], \"via\": [\"" + viaForGrant + "\"] }")
		} else {
			sb.WriteString(",\n    { \"src\": [" + src + "], \"dst\": [\"" + ruleAlias + "\"], \"ip\": [\"*\"] }")
		}
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
	//
	// B265 (2026-09-19) — REAL exit-node pinning for tagged devices.
	//
	// Pre-B265 this loop emitted ONE grant per device:
	//
	//	{ "src": ["tag:dev-u-d"], "dst": ["autogroup:internet"], "ip": ["*"] }
	//
	// with no `via`. The only `via` that reached `autogroup:internet`
	// sat on the per-USER grant (`src: user@…`), and headscale skips
	// tagged nodes when resolving a user selector — every skygate
	// device carries `tag:dev-<user>-<device>`, so that pin matched
	// nothing. The per-CIDR pins emitted above (the aclRows loop) DO
	// carry `via`, but headscale only uses `via` for exit-node
	// selection when the grant's dst is `autogroup:internet`
	// (policy/v2 compileViaForNode + ViaRoutesForPeer treat a `via`
	// on any other dst as subnet-route steering only).
	//
	// Net effect before B265: NOTHING in the generated policy
	// constrained which exit node a tagged device may use — the
	// device's "preferred exit node" in /my/exit-rules was advisory,
	// and any device could relay through any `tag:exit-node` peer.
	// Tailscale grants are also ADDITIVE (no first-match, no deny),
	// so adding a pinned grant without removing the loose one would
	// leave both matching and the pin would still be defeated.
	//
	// Post-B265:
	//   - device WITH a preference (device_exit_node_prefs.via_enabled
	//     and a resolved tag) → exactly ONE grant, carrying
	//     `via: [<preferred tag>]`. That is the
	//     `grantHasAutoGroupInternet` shape headscale honours, so the
	//     pin is enforced.
	//   - device WITHOUT a preference → the previous loose grant, so
	//     the global rule keeps working exactly as before. This
	//     preserves the operator's "the device itself chose to send
	//     everything through an exit node" case: the client's own
	//     exit-node selection (RouteAll) is not overridden.
	//
	// The `via` tag comes from the same `viaByDevice` map the per-CIDR
	// pins use, so it can only be a tag the reconciler resolved for
	// THIS device.
	//
	// B356 (2026-10-07) — AND ONLY IF THAT RELAY CAN ACTUALLY SERVE.
	//
	// "The device has a preference" was conflated with "the device has a usable
	// preference". Because the pinned grant replaces the loose one, a preference
	// naming an offline relay is a full egress outage for that device, and it lasts
	// until the preference reconciler re-points the row — which it may legitimately
	// never do (the pin is the operator's own, or no single owner can be derived).
	// The `via` pin is now conditional on the B273 usability predicate as well, so
	// the failure mode degrades to "the device uses any working exit node" instead
	// of "the device has no exit node". Every fallback is logged and counted — a
	// silent fallback would be indistinguishable from a bug.
	// B361 (2026-10-07) — AND ONLY IF THAT RELAY SERVES SOMETHING THIS DEVICE CAN
	// USE. B356 asked "is the relay up?"; the live case of this block was a relay
	// that was `online` and owned ZERO of the 120 prefixes in `prefix_owner` (all
	// karolina), while the device had no rules of its own and was pinned to it by a
	// HUMAN ~46 days earlier. Since B265 that pin is a permission FILTER, so the
	// destinations karolina carried became unreachable and nothing logged, counted
	// or rendered the reason.
	//
	// The predicate (`servesNothing`, acl_relay_ownership_b361.go): with rules, at
	// least one claimed prefix must be owned by the preferred relay; with no rules,
	// the relay must own at least one prefix at all. Failing that, the gate below
	// falls through to the UNPINNED emit — the device keeps working through any
	// available relay. The stored row is NEVER deleted or rewritten here: only the
	// pin is withheld, so the operator's choice takes effect again by itself the
	// moment that relay serves something again (and it is what makes "never undo a
	// human's choice" and "never lock the device out" both true at once).
	pinFallbacks := 0
	for _, uname := range usernames {
		if uname == "" {
			continue
		}
		for _, devTag := range tagsByUser[uname] {
			// B265: pinned ONLY when this device has a resolved
			// preference (viaByDevice is populated from
			// device_exit_node_prefs WHERE via_enabled=1). The
			// lookup is literal so the source contract
			// scripts/check_b188_2.sh can distinguish the
			// conditional pin from the pre-B188.2 unconditional
			// one; an empty value is falsy in Go anyway.
			if via := viaByDevice[devTag]; via != "" {
				if state, dead := unusablePreferredRelay(via, relayHealth); dead {
					pinFallbacks++
					log.Printf("acl: B356 via-pin FALLBACK — %s prefers %s, which the exit-node monitor reports as %q (NOT usable): emitting the autogroup:internet grant UNPINNED, so the device keeps egress instead of being filtered into a black hole. Set a working preference for this device on /admin/exit-nodes.", devTag, via, state)
				} else if reason, servesNothingForThisDevice := servesNothing(via, ownerTagByPrefix, claimedByDevice[devTag]); servesNothingForThisDevice {
					pinFallbacks++
					_, ownedN := relayOwnsAnyPrefix(via, ownerTagByPrefix)
					log.Printf("acl: B361 via-pin WITHHELD — %s prefers %s, which serves nothing this device can use (%s; it owns %d prefix(es) in prefix_owner). Emitting the autogroup:internet grant UNPINNED so the device keeps egress through any working relay. The stored preference is UNCHANGED — it takes effect again as soon as that relay serves this device again. Fix it on /admin/exit-nodes (or /my/exit-nodes).", devTag, via, reason, ownedN)
				} else {
					sb.WriteString(",\n    { \"src\": [\"" + devTag + "\"], \"dst\": [\"autogroup:internet\"], \"ip\": [\"*\"], \"via\": [\"" + via + "\"] }")
					continue
				}
			}
			sb.WriteString(",\n    { \"src\": [\"" + devTag + "\"], \"dst\": [\"autogroup:internet\"], \"ip\": [\"*\"] }")
		}
	}
	if pinFallbacks > 0 {
		// The historical wording ("dropped") is kept deliberately: it is the vocabulary
		// the operator's journal and scripts/check_b356_exit_pref_failover.sh contract D4
		// already use for "this device did not get a pin it asked for", and B361 is the
		// same event with one more reason. The per-device line above distinguishes the two
		// causes.
		log.Printf("acl: B356/B361 — %d per-device autogroup:internet pin(s) were dropped because the preferred relay is either not usable or serves nothing this device can use; those devices fall back to the unpinned grant (this is a safety net, not a repair: fix the preferences on /admin/exit-nodes)", pinFallbacks)
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
	// 2026-08-17: v1.3.18 (hotfix) — same dedup helper
	// as GenerateACLForPlane. Pre-fix, an exit-node tag
	// (e.g. tag:dev-infra-emilia) could be emitted via
	// BOTH the via-pref loop AND the per-user tagOwners
	// loop if a user had a via-enabled pref for a node
	// that already had a tag from the infra bucket. The
	// headscale v2 parser rejects duplicate object keys
	// with "duplicate object member name" (see commit
	// 6a0ec3a..HEAD — the bug surfaces after migrating
	// legacy tag:exit-X user_exit_node_prefs to
	// tag:dev-infra-X, which collides with the existing
	// tagOwners entries from the infra bucket).
	emittedTagOwners2 := make(map[string]bool, 32)
	emitTagOwner2 := func(tag, ownerListJSON string) {
		if emittedTagOwners2[tag] {
			return
		}
		emittedTagOwners2[tag] = true
		sb.WriteString(",\n    \"" + tag + "\": " + ownerListJSON)
	}
	sb.WriteString("    \"tag:public\": [\"" + envAdminIdentity() + "@" + baseDomain + "\"]")
	emittedTagOwners2["tag:public"] = true
	// 2026-08-17: v1.3.19 (B118) — tag:exit-node is owned by
	// `infra` per the DESIGN (see GenerateACLForPlane's
	// tag:exit-node comment for the full rationale).
	emitTagOwner2("tag:exit-node", "[\"infra@"+baseDomain+"\"]")
	if len(identities) > 1 {
		emitTagOwner2("tag:private", "["+strings.Join(quoteAll(identities), ",")+"]")
	} else if len(identities) == 1 {
		emitTagOwner2("tag:private", "[\""+identities[0]+"\"]")
	} else {
		emitTagOwner2("tag:private", "[\""+envAdminIdentity()+"@"+baseDomain+"\"]")
	}
	emitTagOwner2("tag:subnet-router", "["+strings.Join(quoteAll(identities), ",")+"]")

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
	// B283 (2026-09-22): the per-CIDR pin can ALSO come from the B275
	// assignment table (`prefix_owner.exit_node_id` → that relay's tag in
	// node_owner_map), see `prefixowner.ViaForPrefix` in the grants loop
	// above. Those tags were never added here, and headscale's policy parser
	// rejects the WHOLE document when a `grants[].via` names a tag that
	// `tagOwners` does not declare ("tag not found") — it does not fall back
	// to the default exit node, and it does not start at all on a
	// `policy.mode: file` host.
	//
	// Live on `aro` (2026-09-22): after the relay was given its own
	// `tag:dev-infra-exit-node-vps`, the generated policy carried
	// `via: ["tag:dev-infra-exit-node-vps"]` on 19 per-CIDR grants while
	// `tagOwners` listed only the viaByUser/viaByDevice tags. The document was
	// valid JSON (python's json.tool accepted it) and headscale still refused
	// to start on it — crash-loop, control plane down, every device gone from
	// the portal until an older snapshot was restored by hand.
	for _, tag := range ownerTagByPrefix {
		if strings.TrimSpace(tag) != "" {
			distinctVias[tag] = true
		}
	}
	var exitNodeTags []string
	for tag := range distinctVias {
		exitNodeTags = append(exitNodeTags, tag)
	}
	sort.Strings(exitNodeTags)
	// 2026-08-17: v1.3.19 (B118) — tag owner for via tags
	// comes from the tag name (tag:dev-<user>-<device> →
	// <user>@domain), not from a hardcoded admin identity.
	// The DESIGN (AGENTS.md "Tag ownership rules") requires
	// `infra` to own infra tags, `skyadmin` to own
	// skyadmin tags, etc. — so headscale's tagOwners list
	// reflects the actual owner. Pre-fix, the via loop
	// emitted every via tag with envAdminIdentity()
	// (= skyadmin@), which overwrote the correct owner
	// for infra tags (e.g. tag:dev-infra-emilia showed
	// `skyadmin@` instead of `infra@`) due to the
	// first-write-wins dedup at the top of this block.
	// The dedup order (static → via → per-user) meant
	// the via path always won, so the per-user path's
	// correct `infra@` was discarded. Fix: parse the
	// owner from the tag name. Falls back to admin
	// identity for non-`tag:dev-*` via tags (none in
	// production today, but defensive).
	//
	// B288 (2026-09-22): a per-device tag is owned by the user its NAME names
	// PLUS the `tagged-devices` sentinel — the same pair the tag-writing paths
	// emit (`db.TagOwnersForUser`). Emitting `<user>@` alone meant every ACL
	// apply stripped the sentinel owner that the tag reconciler needs to
	// (re)apply a tag to an already-tagged node (headscale moves such a node
	// into the sentinel user), so the two writers traded the entry back and
	// forth and the drift banner never cleared.
	for _, tag := range exitNodeTags {
		if user, ok := db.PerDeviceTagUser(tag); ok {
			owners, oErr := db.TagOwnersForUser(user, baseDomain)
			if oErr != nil {
				return "", oErr
			}
			emitTagOwner2(tag, ownerListJSON(owners))
			continue
		}
		emitTagOwner2(tag, "[\""+envAdminIdentity()+"@"+baseDomain+"\"]")
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
	// /api/v1/policy: http.StatusInternalServerError src=tag not found:
	// tag:dev-skyadmin-skygate-host-1". The fix: also
	// include every per-device-pref's tag in tagOwners.
	//
	// B288 (2026-09-22) — the declarations are now the UNION of three sources,
	// all emitted through the one owner derivation:
	//
	//	1. `tagsByUser`  — GetPerUserDeviceTags, a JOIN on portal_users, which
	//	                   cannot see a row whose `username` is headscale's
	//	                   synthetic `tagged-devices` (i.e. every tagged node,
	//	                   B287);
	//	2. `viaByDevice` — tags a per-device exit preference references;
	//	3. node_owner_map — the OWNERSHIP RECORD: every `tag:dev-<user>-<host>`
	//	                   the table holds, whatever its `username` column says.
	//
	// Source 3 is why an apply can no longer REMOVE a declaration a node needs.
	// Live on `aro`: the live policy declared `tag:dev-daniil-homepc` and
	// `tag:dev-daniil-laptop` while the generated one declared neither, so the
	// "stale" banner described a document that was in one respect MORE correct
	// than its replacement. And because the tag reconciler (nodeownership)
	// derives the owners from the row while this generator derived them from the
	// portal-user JOIN, the two writers disagreed about the sentinel owner and
	// rewrote each other's entry forever.
	knownDevTags := make([]string, 0, len(tagsByUser)+len(viaByDevice))
	for _, tags := range tagsByUser {
		knownDevTags = append(knownDevTags, tags...)
	}
	for devTag := range viaByDevice {
		knownDevTags = append(knownDevTags, devTag)
	}
	sortedDevTags, devTagErr := devTagsToDeclare(d, knownDevTags)
	if devTagErr != nil {
		return "", devTagErr
	}
	for _, tag := range sortedDevTags {
		ownerJSON, oErr := devTagOwnerJSON(tag, baseDomain)
		if oErr != nil {
			return "", oErr
		}
		emitTagOwner2(tag, ownerJSON)
	}

	// B285 (2026-09-22) — the last line of defence for the invariant headscale
	// itself enforces: EVERY tag a grant references must be declared in
	// tagOwners, because the parser rejects the whole document otherwise
	// ("tag not found") and a `policy.mode: file` host then will not START on
	// it (live: crash-loop 248, control plane down, every device gone).
	//
	// The grants take their tag from node_owner_map (B284), while the blocks
	// above derive tagOwners from the per-user/per-device sources — and on
	// `aro` the two disagreed for `tag:dev-daniil-workpc`: the node's
	// node_owner_map row is owned by headscale's synthetic `tagged-devices`,
	// so no per-user block emitted it, and the generated policy referenced a
	// tag it never declared. The refusal was correct (headscale would have
	// refused the document anyway) but it also meant the ACL could never
	// apply, so the sweep closes the gap instead of only reporting it.
	missingGrantTags := make([]string, 0, len(grantTags))
	for tag := range grantTags {
		if !emittedTagOwners2[tag] {
			missingGrantTags = append(missingGrantTags, tag)
		}
	}
	sort.Strings(missingGrantTags) // stable document for the drift comparison
	for _, tag := range missingGrantTags {
		// B288: the same owner derivation as every other writer — a per-device
		// tag is owned by the user its name names plus the sentinel; anything
		// else keeps the admin identity.
		if user, ok := db.PerDeviceTagUser(tag); ok {
			owners, oErr := db.TagOwnersForUser(user, baseDomain)
			if oErr != nil {
				return "", oErr
			}
			emitTagOwner2(tag, ownerListJSON(owners))
			continue
		}
		emitTagOwner2(tag, "[\""+envAdminIdentity()+"@"+baseDomain+"\"]")
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
	// 2026-09-18: same skygate-host src fix as GenerateACLForPlane.
	sb.WriteString("      \"src\": [" + strings.Join(quoteAll(sshRuleSrc(tagsByUser, envAdminIdentity(), baseDomain)), ", ") + "],\n")
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
