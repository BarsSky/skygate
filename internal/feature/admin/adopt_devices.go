// B257 (v1.5.8+, 2026-09-16): adoption of pre-existing headscale
// devices into skygate's portal_users + node_owner_map.
//
// # Problem
//
// When skygate is deployed as a sidecar to a pre-existing headscale
// (the common "sidecar install" path documented in
// deploy/deploy.sh), the operator's devices that already exist in
// headscale are NOT visible to the portal — they have no
// `tag:dev-<user>-<host>` tag and no `node_owner_map` row, so the
// /my/devices auto-attribution backfill (B77) skips them on every
// tick:
//
//   - Strategy A (PreAuthKeyID match): the device was registered
//     via an OIDC login OR an operator-issued preauth key — neither
//     has a row in skygate's `preauth_keys` table, so no match.
//   - Strategy B (created within 1h of a preauth key): same gap.
//   - Strategy C (existing `tag:dev-<user>-*`): no such tag yet —
//     that's what we're trying to CREATE.
//   - Strategy D (OIDC `n.UserName == portalUsername`,
//     PreAuthKeyID == "" guard): the node was registered via a
//     non-OIDC preauth key (`PreAuthKeyID` populated), so the
//     OIDC-guard rejects. (Strategy E per the comments.)
//
// The operator had to SSH into the VM and either re-tag every
// device manually (`headscale nodes tag -i <ID> -t
// tag:dev-<user>-<host>`) or `headscale nodes register --expiry
// ...` + a fresh device-side `tailscale up` with a fresh authkey.
// Both are "no thanks" UX — the operator reported this gap on the
// 2026-09-14 deploy.
//
// # Solution
//
//  1. ListUnadoptedDevices — find headscale nodes that
//     (a) have no `node_owner_map` row in skygate, AND
//     (b) belong to a headscale user whose ID matches
//     `portal_users.headscale_user_id` for some portal user.
//     The matching portal user becomes the candidate owner.
//     We skip the synthetic `tagged-devices` user (whose nodes
//     are already adopted via the dev-tag).
//  2. /admin/devices renders a "Devices awaiting adoption"
//     card at the top of the page when the list is non-empty,
//     listing each candidate with hostname, IP, OS, online status,
//     and a one-click "Assign to <user>" form.
//  3. PostAdminDeviceAdopt wires the form. It does the same
//     work the /my/devices backfill does for a self-service
//     node: inserts into `node_owner_map`, calls
//     `EnsureTagOwner` to add `tag:dev-<user>-<host>` to the policy
//     if not present (idempotent — the B245 fix), then `AddTag`
//     on the node itself. The operator must then click "Re-apply
//     ACL" on /admin/exit-rules once to push the new `tagOwners`
//     into headscale (matches the PostAdminDeviceTransfer flow
//     from B171).
//
// # Why a new handler (not Transfer)
//
// `PostAdminDeviceTransfer` requires the node to ALREADY have a
// `node_owner_map` row (`db.GetNodeOwner` returns ErrNodeOwnerNotFound
// otherwise — see the handler's defensive check). That's the wrong
// shape for this adoption scenario: the row doesn't exist yet.
// `PostAdminDeviceAdopt` is the symmetric "first time we adopt"
// handler — no existing row, INSERT instead of UPDATE, no
// `UntagNode` step (no prior dev-tag to drop).

package admin

import (
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"skygate/internal/db"
	"skygate/internal/devicemeta"
	"skygate/internal/headscale"
	"skygate/internal/nodeownership"
)

// adminTagAlertSink builds the B227 alert sink for tag failures that happen
// on the ADMIN action path (adopt / transfer / tag buttons) — B272.
//
// Before B272 these paths wrote their own audit rows and nothing else: no
// Prometheus counter, no Telegram alert, so the 2026-09-19 live failure
// ("headscale PUT /api/v1/policy: 500 update is disabled for modes other than
// database" → every dev-tag unapplied) never showed up in
// skygate_tag_autoupdate_failures_total. Now one failure → one metric
// increment, one audit row, one rate-limited alert, regardless of whether the
// autoupdater or an operator click triggered it.
func adminTagAlertSink(s *Service) *nodeownership.TagAlertSink {
	var notifier nodeownership.AlertSink = nodeownership.NoopAlertSink
	if s != nil && s.Notifier != nil {
		notifier = adminNotifierSink{s.Notifier}
	}
	// The sink resolves the live pool per call (B224 ResettableDB pattern),
	// so the B203 watchdog's hot swap is followed transparently.
	return nodeownership.NewTagAlertSink(notifier, s.DB)
}

// adminNotifierSink adapts telegram.Notifier to the nodeownership AlertSink
// interface (the same adapter pattern main.go uses for the autoupdater, kept
// local here to avoid widening the admin package's imports).
type adminNotifierSink struct {
	n interface{ SendAlert(string) int64 }
}

func (a adminNotifierSink) SendAlert(text string) int64 { return a.n.SendAlert(text) }

// AdoptionCandidate is the per-device row the /admin/devices
// "Devices awaiting adoption" card renders. Each row corresponds
// to one headscale node that the B77 backfill can't claim
// automatically but a portal user already owns in headscale
// (matched by headscale_user_id) — the operator confirms the
// adoption with one click.
type AdoptionCandidate struct {
	headscale.NodeView
	// PortalUserID is portal_users.id for the matching owner
	// (0 means "no match" — the row then needs the operator to
	// choose one, see NeedsOwnerPick).
	PortalUserID int64
	// PortalUsername is the matching portal_users.username.
	// Renders in the "Assign to <user>" button label.
	PortalUsername string
	// NeedsOwnerPick (B303) is true when NO portal user could be inferred for this
	// node: headscale reported no user at all, the node sits under its synthetic
	// `tagged-devices` user (which never has a portal_users row), or the reported
	// headscale user has not been linked in the portal yet.
	//
	// WHY this exists: those nodes used to be dropped from the adoption card
	// entirely, and the per-row Transfer button rejects a node without a
	// node_owner_map row (it exists to REASSIGN, not to adopt). Together the two
	// actions left an ownerless device with NO working path from the panel —
	// live report: «при попытке переместить устройство что оказалось никому не
	// принадлежащим открылась отдельная страница … node not in node_owner_map»
	// plus «устройство не привязанное тегом не дается для тегирования
	// администратору (словно проигнорировано)». The card now lists them and the
	// UI asks the operator to name the owner instead of guessing one.
	NeedsOwnerPick bool
	// OS is the auto-detected OS label ("linux", "android",
	// "windows", "ios", "macos", or "unknown"). Pre-filled
	// from the auto-detect so the UI doesn't show "unknown"
	// when the test would succeed.
	OS string
	// DeviceType is the auto-detected role label ("client",
	// "exit-node", "subnet-router", "server", or "unknown").
	DeviceType string
}

// findAdoptionCandidates returns the headscale nodes that need
// manual adoption:
//   - Not already in node_owner_map (so /my/devices can't see them)
//   - Either owned by a headscale user that maps to some portal_users
//     row (the row then offers a one-click "Assign to <user>"), or
//     ownerless / under the synthetic `tagged-devices` user (the row
//     then offers an explicit owner picker — NeedsOwnerPick, B303)
//
// Pre-B303 the second group was filtered out entirely, which is the
// deadlock the operator reported; the classification helper documents it.
//
// The OS + DeviceType per row are best-effort auto-detect; the
// helper does not persist them (the real persist happens on
// /my/devices load). The /admin/devices UI just shows the chips.
//
// `hs` may be nil (the B203 hot-reload path where headscale is
// briefly unavailable); the function returns an empty slice so
// the UI gracefully hides the "adoption" card.
func (s *Service) findAdoptionCandidates(r *http.Request) []AdoptionCandidate {
	if s == nil || s.HSGlobalFn == nil {
		return nil
	}
	hs := s.HSGlobalFn()
	if hs == nil {
		return nil
	}
	nodes, err := hs.ListAllNodes()
	if err != nil {
		return nil
	}
	portalUsers, err := db.GetAllPortalUsers(s.dbc())
	if err != nil {
		return nil
	}
	// portalUserIDByHeadscaleUserID maps headscale's numeric
	// user id (passed around as a string in NodeView.UserID)
	// to portal_users.id for matched adoption candidates.
	// Loop once to avoid the O(N*M) cross-check.
	portalByHSID := make(map[string]portalByHS, len(portalUsers))
	for _, u := range portalUsers {
		if u.HeadscaleUserID > 0 {
			portalByHSID[strconv.FormatInt(u.HeadscaleUserID, 10)] = portalByHS{
				id:       u.ID,
				username: u.Username,
			}
		}
	}
	// OwnedNodeIDs is the set of node_ids already in
	// node_owner_map — anything in here is adopted (even if
	// the dev-tag hasn't landed yet), so it's not an
	// adoption candidate.
	ownedRows, err := db.ListAllNodeOwners(s.dbc())
	if err != nil {
		ownedRows = nil
	}
	ownedSet := make(map[string]struct{}, len(ownedRows))
	for _, o := range ownedRows {
		if o.NodeID != "" {
			ownedSet[o.NodeID] = struct{}{}
		}
	}
	candidates := []AdoptionCandidate{}
	for _, n := range nodes {
		c, ok := classifyNodeForAdoption(n, ownedSet, portalByHSID)
		if !ok {
			continue
		}
		candidates = append(candidates, *c)
	}
	return candidates
}

// portalByHS is the per-row shape the classification helper
// expects. Defined here (rather than inline in the caller) so
// tests can build the map without depending on db.User.
type portalByHS struct {
	id       int64
	username string
}

// classifyNodeForAdoption returns the (candidate, true) for a
// headscale node that needs manual adoption, or (nil, false) to
// skip. Pure function — no DB, no API client. The reject
// rules are pinned by TestClassifyNodeForAdoption_*:
//
//  1. empty NodeID                → skip (defensive — headscale returns non-empty IDs in practice)
//  2. already in ownedNodeIDs     → skip (it's adopted)
//  3. empty UserName              → CANDIDATE with NeedsOwnerPick (B303: ownerless node, operator names the owner)
//  4. no matching portal_user     → CANDIDATE with NeedsOwnerPick (B303 — includes the synthetic `tagged-devices`)
//  5. matching portal_user with id==0 → CANDIDATE with NeedsOwnerPick (B303: no owner may be auto-selected)
//
// Rules 3–5 were "skip" before B303 (v1.5.68) and that skip is exactly
// what the operator hit: the node appeared nowhere on the page, so the
// only action left was the per-row Transfer button, which refused to run
// because such a node has no node_owner_map row to reassign — a deadlock
// with a raw 400 page as its only feedback.
func classifyNodeForAdoption(n headscale.NodeView, ownedNodeIDs map[string]struct{}, portalByHSID map[string]portalByHS) (*AdoptionCandidate, bool) {
	if n.ID == "" {
		return nil, false
	}
	if _, ok := ownedNodeIDs[n.ID]; ok {
		return nil, false
	}
	// headscale's synthetic `tagged-devices` user owns all
	// nodes that already carry dev-tags. Skip — Strategy D
	// in the backfill handles them on the next tick after
	// we add the dev-tag (or already has). What we want
	// here are nodes that headscale still has under the
	// "real" user (skyadmin, michail, ...) without a
	// dev-tag.
	if n.UserName == "" {
		// B303: headscale reported no user for this node. That is not a reason to
		// hide it — it is an ownerless node the operator must be able to adopt.
		osLabel := devicemeta.DetectOS(n.Hostname)
		return &AdoptionCandidate{
			NodeView:       n,
			OS:             osLabel,
			DeviceType:     devicemeta.DetectType(n.Tags, n.ApprovedRoutes, n.AvailableRoutes, osLabel),
			NeedsOwnerPick: true,
		}, true
	}
	pu, ok := portalByHSID[n.UserID]
	if !ok || pu.id == 0 {
		// Either the headscale user doesn't have a matching portal_user row yet,
		// OR the headscale user is the synthetic `tagged-devices` (no
		// portal_users row ever gets that) — B303: both are nodes an operator
		// adopts BY HAND, so they belong on this card with an explicit owner
		// picker rather than nowhere at all. (The B77 backfill may still claim
		// them on its next tick via Strategy D when they carry a dev-tag; the
		// card simply no longer hides them until it does.)
		osLabel := devicemeta.DetectOS(n.Hostname)
		return &AdoptionCandidate{
			NodeView:       n,
			OS:             osLabel,
			DeviceType:     devicemeta.DetectType(n.Tags, n.ApprovedRoutes, n.AvailableRoutes, osLabel),
			NeedsOwnerPick: true,
		}, true
	}
	osLabel := devicemeta.DetectOS(n.Hostname)
	typeLabel := devicemeta.DetectType(n.Tags, n.ApprovedRoutes, n.AvailableRoutes, osLabel)
	return &AdoptionCandidate{
		NodeView:       n,
		PortalUserID:   pu.id,
		PortalUsername: pu.username,
		OS:             osLabel,
		DeviceType:     typeLabel,
	}, true
}

// liveUserLinkedToPortal reports whether headscale's user id for a node maps to a
// portal_users row. B303: that is the difference between "this node already has a
// portal owner, so a different target is a mistake" and "this node has no portal
// owner at all, which is exactly the adoption case".
func liveUserLinkedToPortal(users []db.User, liveUserID string) bool {
	if strings.TrimSpace(liveUserID) == "" {
		return false
	}
	for i := range users {
		if users[i].HeadscaleUserID > 0 &&
			strconv.FormatInt(users[i].HeadscaleUserID, 10) == liveUserID {
			return true
		}
	}
	return false
}

// PostAdminDeviceAdopt wires the per-row "Assign to <user>"
// button on the "Devices awaiting adoption" card. The form
// fields are node_id and target_username (portal_username);
// the handler validates both, ensures the corresponding portal
// user exists and has a headscale_user_id set, then inserts
// into node_owner_map and applies the dev-tag.
//
// Follow-up step the operator must run manually (called out in
// the flash message): /admin/exit-rules/reapply to push the
// new tagOwners entries into headscale's policy. The
// dev-tag-on-node assignment + node_owner_map INSERT succeed
// even without the re-apply — only the per-tag grant in the
// policy requires the re-apply to take effect.
func (s *Service) PostAdminDeviceAdopt(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		log.Printf("admin.devices.adopt: ParseForm: %v", err)
		http.Redirect(w, r, "/admin/devices?err="+url.QueryEscape(s.I18n.T(s.I18n.LangFromRequest(r), "error.db")), http.StatusSeeOther)
		return
	}
	nodeIDStr := strings.TrimSpace(r.FormValue("node_id"))
	targetUsername := strings.TrimSpace(r.FormValue("target_username"))
	if nodeIDStr == "" || targetUsername == "" {
		http.Redirect(w, r, "/admin/devices?err="+url.QueryEscape("node_id and target_username are required"), http.StatusSeeOther)
		return
	}
	// Look up the target portal user + verify they have a
	// headscale_user_id (which is what tells us they're
	// eligible to adopt headscale nodes).
	users, err := db.GetAllPortalUsers(s.dbc())
	if err != nil {
		log.Printf("admin.devices.adopt: GetAllPortalUsers: %v", err)
		http.Redirect(w, r, "/admin/devices?err="+url.QueryEscape(s.I18n.T(s.I18n.LangFromRequest(r), "error.db")), http.StatusSeeOther)
		return
	}
	var target *db.User
	for i := range users {
		if users[i].Username == targetUsername {
			target = &users[i]
			break
		}
	}
	if target == nil {
		http.Redirect(w, r, "/admin/devices?err="+url.QueryEscape("target user not found: "+targetUsername), http.StatusSeeOther)
		return
	}
	if target.HeadscaleUserID == 0 {
		http.Redirect(w, r, "/admin/devices?err="+url.QueryEscape(
			"target user "+targetUsername+" has no headscale_user_id yet — provision them on /admin/users first"), http.StatusSeeOther)
		return
	}
	// Reject the second adoption: refuse to "adopt" a node
	// that's already in node_owner_map (that's PostAdminDeviceTransfer's
	// job, not ours).
	if existing, _ := db.GetNodeOwner(s.dbc(), nodeIDStr); existing != nil {
		http.Redirect(w, r,
			"/admin/devices?err="+url.QueryEscape(
				"Node "+nodeIDStr+" is already adopted by "+existing.Username+
					". Use 'Transfer' (not 'Adopt') to reassign."),
			http.StatusSeeOther)
		return
	}
	if s.HSGlobalFn == nil {
		log.Printf("admin.devices.adopt: HSGlobalFn is nil (headscale client unavailable)")
		http.Redirect(w, r, "/admin/devices?err="+url.QueryEscape("headscale client is not configured"), http.StatusSeeOther)
		return
	}
	hs := s.HSGlobalFn()
	if hs == nil {
		log.Printf("admin.devices.adopt: headscale client unavailable")
		http.Redirect(w, r, "/admin/devices?err="+url.QueryEscape("headscale client is not configured"), http.StatusSeeOther)
		return
	}
	// Pull the live hostname + UserID from headscale. We
	// build the dev-tag from the LIVE Hostname (not from a
	// possibly stale row) because node names can be
	// renamed in headscale directly without us knowing.
	var liveHostname string
	var liveUserID string
	if nodes, err := hs.ListAllNodes(); err == nil {
		for _, n := range nodes {
			if n.ID == nodeIDStr {
				liveHostname = n.Hostname
				liveUserID = n.UserID
				break
			}
		}
	}
	if liveHostname == "" {
		http.Redirect(w, r, "/admin/devices?err="+url.QueryEscape(
			"node "+nodeIDStr+" not found in headscale (it may have been deleted; refresh and retry)"), http.StatusSeeOther)
		return
	}
	// Defensive: confirm headscale agrees the node belongs to the target user —
	// but ONLY when the live user is a REAL, portal-linked user. A node under
	// headscale's synthetic `tagged-devices` user (or one whose headscale user has
	// no portal row) has no portal owner to agree with, and that is exactly the
	// node this action exists to adopt by hand (B303): refusing here deadlocked
	// both actions the panel offers — adopt said "node belongs to a different
	// headscale user … Use 'Transfer' to reassign", and Transfer answered with a
	// raw 400 because there was no node_owner_map row to reassign:
	//
	//	node not in node_owner_map: db: node_owner_map: no row
	//
	// Live report: «при попытке переместить устройство что оказалось никому не
	// принадлежащим открылась отдельная страница» + «устройство не привязанное
	// тегом не дается для тегирования администратору».
	if liveUserID != "" && liveUserID != strconv.FormatInt(target.HeadscaleUserID, 10) && liveUserLinkedToPortal(users, liveUserID) {
		log.Printf("admin.devices.adopt: refusing node=%s live_user=%s target=%s(hs=%d) — the live user IS a linked portal user",
			nodeIDStr, liveUserID, targetUsername, target.HeadscaleUserID)
		http.Redirect(w, r, "/admin/devices?err="+url.QueryEscape(
			"node belongs to a different headscale user (live UserID="+liveUserID+
				", target's headscale_user_id="+strconv.FormatInt(target.HeadscaleUserID, 10)+
				"). Use 'Transfer' to reassign."),
			http.StatusSeeOther)
		return
	}
	// B176: lowercase the hostname before building the tag —
	// headscale 0.29 rejects uppercase tags with
	// `tag should be lowercase`.
	newDevTag := "tag:dev-" + targetUsername + "-" + strings.ToLower(liveHostname)
	nodeID, err := strconv.ParseInt(nodeIDStr, 10, 64)
	if err != nil || nodeID <= 0 {
		http.Redirect(w, r, "/admin/devices?err="+url.QueryEscape("bad node id: "+nodeIDStr), http.StatusSeeOther)
		return
	}
	// Step 1: ensure the policy knows this dev-tag.
	// EnsureTagOwner is idempotent (B245); no-op if tagOwners
	// already lists the tag.
	if s.Cfg != nil && s.Cfg.BaseDomain != "" {
		baseDomain := s.Cfg.BaseDomain
		owners := []string{
			targetUsername + "@" + baseDomain,
			"tagged-devices@" + baseDomain,
		}
		if err := hs.EnsureTagOwner(newDevTag, owners); err != nil {
			// B272: surface through the same sink the B77 autoupdater uses,
			// so this failure reaches /metrics + the Telegram alert path too
			// instead of only an audit row that nobody reads. The adopt path
			// used to have its OWN action names and no metric at all — the
			// live 2026-09-19 case ("update is disabled for modes other than
			// database") was invisible in skygate_tag_autoupdate_failures_total.
			adminTagAlertSink(s).ReportFailure(nodeIDStr, liveHostname, newDevTag, err)
			s.Backend.Audit(c.UserID, c.Username, "device_adopt_ensure_tag_owner_failed",
				"node="+nodeIDStr+" tag="+newDevTag+" err="+err.Error())
		}
	}
	// Step 2: insert into node_owner_map. We do this BEFORE
	// AddTag so that even if headscale rejects the tag (e.g.
	// because tagOwners wasn't updated), the UI immediately
	// shows the node in /my/devices for the adopted user —
	// the operator then sees the AddTag failure in the
	// audit log and can re-run /admin/devices/force-backfill-
	// tags.
	if err := db.UpsertNodeOwner(s.dbc(), nodeIDStr, target.HeadscaleUserID, targetUsername, newDevTag, c.UserID); err != nil {
		log.Printf("admin.devices.adopt: UpsertNodeOwner node=%s user=%s tag=%s: %v", nodeIDStr, targetUsername, newDevTag, err)
		http.Redirect(w, r, "/admin/devices?err="+url.QueryEscape(s.I18n.T(s.I18n.LangFromRequest(r), "error.db")), http.StatusSeeOther)
		return
	}
	// Step 3: AddTag the dev-tag on the headscale node.
	// Idempotent (no-op if already present). Failures are
	// non-fatal — the operator can re-run via
	// /admin/devices/force-backfill-tags.
	if err := hs.AddTag(nodeID, newDevTag); err != nil {
		adminTagAlertSink(s).ReportFailure(nodeIDStr, liveHostname, newDevTag, err)
		s.Backend.Audit(c.UserID, c.Username, "device_adopt_addtag_failed",
			"node="+nodeIDStr+" tag="+newDevTag+" err="+err.Error())
	}
	hs.InvalidateCache()
	s.Backend.Audit(c.UserID, c.Username, "device_adopt",
		"node="+nodeIDStr+" user="+targetUsername+" tag="+newDevTag)
	http.Redirect(w, r,
		"/admin/devices?ok="+url.QueryEscape(
			"Node "+nodeIDStr+" ("+liveHostname+") adopted by "+targetUsername+
				" (tag="+newDevTag+"). Run 'Re-apply ACL' on /admin/exit-rules to push the new tagOwners."), http.StatusSeeOther)
}
