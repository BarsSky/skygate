// Package exit_rules — sync_routes.go owns route advertisement: the
// all-nodes and per-node advertised-routes sync, the per-relay transport
// decision, the staggered background sync and the
// /admin/exit-rules/sync HTTP endpoint.
//
// Split out of sync.go (2026-10-08, PURE MOVE — the declarations below are
// byte-identical to what sync.go carried before the split). The ACL apply
// pipeline lives in sync_acl.go, the domain auto-updater in
// sync_domain.go; sync.go keeps the package doc.
package exit_rules

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"skygate/internal/headscale"
	"skygate/internal/prefixowner"

	"skygate/internal/db"
)

// SyncAdvertisedRoutes collects all enabled IP/subnet rules and pushes to exit nodes.
// Pure data-plane (advertised-routes sync). Returns a per-node status map
// (the HTTP handler marshals it as JSON).
//
// 2026-08-04 v0.33.1: per-node SSH config + combined result reporting.
// Two pre-v0.33.1 bugs were silently hiding failures from the operator:
//
//  1. SetAdvertisedRoutes was called with a hard-coded
//     /home/admin/.ssh/config which doesn't exist inside the
//     dockerised skygate — the SSH call always failed with
//     "Can't open user config file". The headscale approve-routes
//     step (run right after, unconditionally) succeeded, so
//     `result[node]` was overwritten to "ok approved=N" and the
//     operator thought the sync worked. The actual tailscaled
//     on the relay was never re-configured.
//
//  2. `ssh: <err>` was the value stored in result[node] when SSH
//     failed, but the unconditional approve step's success then
//     OVERWROTE that to "ok approved=N". Result: SSH failure
//     was invisible from the UI.
//
// Fix: read the per-exit-node SSH target + key path from
// exit_servers.ssh_target / ssh_key_path (with the
// Config.SSHKeyPath / SKYGATE_EXIT_SSH_KEY default as the
// global fallback), pass them to SetAdvertisedRoutes, and
// combine the SSH result with the approve result into a single
// string so neither side's failure can be hidden:
//
//	ssh=ok approved=214
//	ssh=err=<msg> approved=0
//	ssh=ok approve=err=<msg>
//	ssh=err=<msg> approve=err=<msg>
func (s *Service) SyncAdvertisedRoutes() map[string]string {
	result := map[string]string{}
	// B352: `exit_node_id <> ''` is load-bearing. A rule with no exit node is in
	// "engine auto" mode (B277.3): the assignment table decides who serves its prefix,
	// and the rule itself has no relay to configure. Including the empty string built a
	// node literally named "" and the sync reported it as
	// `"": ssh=err=no usable transport is configured for this relay` on every pass
	// (live: skyadmin's 22 youtube CDN rows), which reads like a broken relay.
	rows, err := s.dbc().Query("SELECT DISTINCT exit_node_id, target_value FROM device_rules WHERE enabled = 1 AND exit_node_id <> '' AND (target_type = 'ip' OR target_type = 'subnet') ORDER BY exit_node_id")
	if err != nil {
		result["error"] = err.Error()
		return result
	}
	defer rows.Close()
	exitRoutes := map[string][]string{}
	var claims []PrefixClaim
	for rows.Next() {
		var node, target string
		if err := rows.Scan(&node, &target); err != nil {
			continue
		}
		exitRoutes[node] = append(exitRoutes[node], target)
		claims = append(claims, PrefixClaim{Node: node, Prefix: target})
	}
	// B274: a prefix is advertised by exactly ONE relay. Without this,
	// two relays claim the same CIDR and headscale's primary for it
	// flaps between passes (see prefix_owner.go for the live case).
	owners := PrefixOwnership(claims)
	// B275: the persisted assignment table is the authority — it is
	// seeded here (explicit rules win, the rest is spread over the
	// relays and stays sticky), and it can be pinned per prefix by an
	// operator (prefixowner.SetManual) without touching the rules.
	// B274's in-memory computation remains the fallback for the very
	// first pass, before any row exists.
	//
	// B276: the same helper also re-applies the ACL when the table moved a prefix
	// to another relay, because the per-CIDR pin lives in the ACL and nothing else
	// regenerates it.
	if ins, chg, rerr := s.reconcilePrefixOwnership(); rerr != nil {
		log.Printf("prefix-owner: reconcile: %v", rerr)
	} else if ins > 0 || chg > 0 {
		log.Printf("prefix-owner: assignment table updated (inserted=%d changed=%d)", ins, chg)
	}
	if tbl := prefixowner.OwnerByPrefix(s.dbc()); len(tbl) > 0 {
		owners = tbl
	}
	reportPrefixLosers("SyncAdvertisedRoutes", claims, owners, result)
	// Default SSH key path comes from Config (set from
	// SKYGATE_EXIT_SSH_KEY, default /home/operator/.ssh/skygate_sync).
	// The operator can override per-exit-node via
	// exit_servers.ssh_key_path in the /admin/exit-nodes form.
	var defaultKeyPath string
	if s.Cfg != nil {
		defaultKeyPath = s.Cfg.SSHKeyPath
	}
	for node, routes := range exitRoutes {
		syncOneExitNode(s.HS, s.dbc(), s.lookupAcceptRoutes, defaultKeyPath, node, OwnedPrefixes(node, routes, owners), result)
	}
	// B279 (v1.5.46): sync the relays the ASSIGNMENT TABLE names even
	// when no rule mentions them.
	//
	// The loop above iterates `exitRoutes`, which is keyed by
	// device_rules.exit_node_id — but B275 made `prefix_owner` the
	// authority on who serves a prefix, and the two disagree whenever
	// the engine moves a prefix (its rule's relay went unhealthy, or an
	// operator pinned it). Before this, such a prefix was advertised by
	// NOBODY: the only relay that had it in its candidate list was
	// filtered down to "prefixes I own" (none) while its true owner was
	// never visited at all. The log said it plainly — "node drops 19
	// claimed prefix(es) owned by another relay" — while `prefix_owner`
	// pointed all 19 at `exit-node-vps`.
	for _, node := range relaysFromAssignment(owners, exitRoutes) {
		owned := OwnedPrefixesForRelay(node, owners)
		if len(owned) == 0 {
			continue
		}
		log.Printf("prefix-ownership: syncing %s from the assignment table (%d prefix(es) owned, no rule names this relay)",
			node, len(owned))
		syncOneExitNode(s.HS, s.dbc(), s.lookupAcceptRoutes, defaultKeyPath, node, owned, result)
	}
	// B352 (2026-10-05): a relay that owns NOTHING any more must still be visited, or
	// its last advertisement stays on the node forever — `tailscale set
	// --advertise-routes=` REPLACES, so the stale set survives every later pass that
	// skips it, and headscale keeps serving those prefixes from a relay the assignment
	// table no longer points at (live: emilia advertised 29 prefixes that had moved to
	// karolina, and every device pinned to karolina lost those 29 destinations). The
	// B279 loop above cannot cover them: it `continue`s on an empty owned set.
	{
		covered := map[string]bool{}
		for node := range exitRoutes {
			covered[node] = true
		}
		for _, owner := range owners {
			covered[owner] = true
		}
		for _, node := range s.relaysToKeepSynced(covered) {
			owned := s.ownedPrefixesForNodeFromTable(node)
			log.Printf("prefix-ownership: syncing %s down to its owned set (%d prefix(es)) — it has a recorded route application, so its advertisement must follow the assignment table",
				node, len(owned))
			syncOneExitNode(s.HS, s.dbc(), s.lookupAcceptRoutes, defaultKeyPath, node, owned, result)
		}
	}
	if len(exitRoutes) == 0 {
		result["info"] = "no IP/subnet rules configured"
	}
	return result
}

// relaysFromAssignment returns the relay hostnames the assignment table
// hands at least one prefix to and that the caller's per-rule map does
// not already cover, sorted for a stable log/order.
//
// 2026-09-22: B279 (v1.5.46).
func relaysFromAssignment(owners map[string]string, already map[string][]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, owner := range owners {
		if owner == "" || seen[owner] {
			continue
		}
		if _, covered := already[owner]; covered {
			continue
		}
		seen[owner] = true
		out = append(out, owner)
	}
	sort.Strings(out)
	return out
}

// 2026-08-18 (B132): SyncAdvertisedRoutesForNode syncs just ONE
// node. Same per-node logic as the loop body in
// SyncAdvertisedRoutes (extracted to syncOneExitNode so the two
// paths can't drift). Used by the new per-row "Re-sync" button
// on /admin/exit-nodes — the operator asked "не понятно что с
// этим делать" because the only available tool was the
// global "Sync all" (re-runs SetAdvertisedRoutes on every
// node, which is wasteful when only one is in mismatch).
//
// Returns a map with a single entry (same shape as
// SyncAdvertisedRoutes). The "info" key is set when the
// requested node has no enabled IP/subnet rules (no
// advertised routes to push).
func (s *Service) SyncAdvertisedRoutesForNode(node string) map[string]string {
	result := map[string]string{}
	if node == "" {
		result["error"] = "node hostname is empty"
		return result
	}
	// B352: `exit_node_id <> ''` — see SyncAdvertisedRoutes: an empty relay is "engine
	// auto", not a node to configure.
	rows, err := s.dbc().Query("SELECT target_value FROM device_rules WHERE enabled = 1 AND exit_node_id = $1 AND exit_node_id <> '' AND (target_type = 'ip' OR target_type = 'subnet') ORDER BY target_value", node)
	if err != nil {
		result["error"] = err.Error()
		return result
	}
	defer rows.Close()
	var routes []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			continue
		}
		routes = append(routes, t)
	}
	if len(routes) == 0 {
		// B352 (2026-10-05): "no rules" is not a reason to leave the relay alone. If
		// skygate has applied routes to this node before, its current advertisement is
		// skygate's own and must be allowed to shrink to nothing — otherwise the
		// operator's only tool for a stale relay was SSH. A node skygate has NEVER
		// configured keeps the old informational answer, so the button cannot claim to
		// have "fixed" a relay that was never part of the tailnet's route plan.
		if s.hasRelayApplyRecord(node) {
			owned := s.ownedPrefixesForNodeFromTable(node)
			result[node] = "info=no rules target this node — pruning its advertisement to its owned set"
			if len(owned) > 0 {
				result[node] = "info=no rules target this node — applying the assignment table instead"
			}
			var defaultKeyPath string
			if s.Cfg != nil {
				defaultKeyPath = s.Cfg.SSHKeyPath
			}
			syncOneExitNode(s.HS, s.dbc(), s.lookupAcceptRoutes, defaultKeyPath, node, owned, result)
			return result
		}
		result[node] = "info=no IP/subnet rules target this node"
		return result
	}
	var defaultKeyPath string
	if s.Cfg != nil {
		defaultKeyPath = s.Cfg.SSHKeyPath
	}
	// B274: this node advertises only the prefixes it OWNS — a prefix
	// another relay claims more strongly is left to that relay so
	// headscale's primary cannot flap. The single-node path uses the
	// same global ownership map as the all-nodes path.
	nodeClaims, _ := s.dbc().Query("SELECT exit_node_id, target_value FROM device_rules WHERE enabled = 1 AND (target_type = 'ip' OR target_type = 'subnet')")
	var allClaims []PrefixClaim
	if nodeClaims != nil {
		for nodeClaims.Next() {
			var n2, p2 string
			if nodeClaims.Scan(&n2, &p2) == nil {
				allClaims = append(allClaims, PrefixClaim{Node: n2, Prefix: p2})
			}
		}
		nodeClaims.Close()
	}
	owners := PrefixOwnership(allClaims)
	syncOneExitNode(s.HS, s.dbc(), s.lookupAcceptRoutes, defaultKeyPath, node, OwnedPrefixes(node, routes, owners), result)
	return result
}

// healthyExitRelays returns the relay hostnames the exit-node monitor last
// marked healthy. B275.2: prefixowner.Assign needs this list — with an empty
// one no rule is ever 'explicit' (every assignment degrades to 'auto') and
// sticky re-use is disabled, so the owners flip between passes and the ACL pins
// move under the clients. Live: after the v1.5.25 deploy the table showed
// everything as 'auto' and 104.16.0.0/12 / 142.250.0.0/15 swapped owners
// between two consecutive checks.
func healthyExitRelays(d *sql.DB) []string {
	rows, err := db.ListExitNodeHealth(d)
	if err != nil {
		return nil
	}
	var out []string
	for _, h := range rows {
		if h.Healthy && h.Hostname != "" {
			out = append(out, h.Hostname)
		}
	}
	return out
}

// reportPrefixLosers logs (and counts into the result map) every relay
// that claims a prefix it does not own. These are the rules the
// operator has to look at: the device keeps its own rule list, but the
// relay named by that rule will not serve the prefix until either the
// rule is re-pointed at the owner or the ownership changes. B274.
//
// B279 (v1.5.46): the comparison is now against the ASSIGNMENT TABLE
// (`owners`, the same map the sync loop filters with) instead of the
// in-memory `PrefixOwnership` vote. The old body took `owners` and
// ignored it, so the report described a different decision than the one
// that was actually applied — the log could stay silent while the loop
// skipped every prefix of a relay the table had replaced, and it could
// blame a relay for a prefix the table had long since given it. The
// in-memory vote remains the fallback for the very first pass, before
// any `prefix_owner` row exists.
func reportPrefixLosers(where string, claims []PrefixClaim, owners map[string]string, result map[string]string) {
	var losers map[string][]string
	if len(owners) > 0 {
		losers = map[string][]string{}
		for _, c := range claims {
			if c.Node == "" || c.Prefix == "" {
				continue
			}
			if owner, ok := owners[c.Prefix]; ok && owner != c.Node {
				losers[c.Node] = append(losers[c.Node], c.Prefix)
			}
		}
		for node := range losers {
			sort.Strings(losers[node])
		}
	} else {
		losers = PrefixLosers(claims)
	}
	if len(losers) == 0 {
		return
	}
	total := 0
	nodes := make([]string, 0, len(losers))
	for node := range losers {
		nodes = append(nodes, node)
	}
	sort.Strings(nodes)
	for _, node := range nodes {
		dropped := losers[node]
		total += len(dropped)
		log.Printf("prefix-ownership(%s): %s does NOT advertise %d prefix(es) it claims — the assignment table gives them to another relay (%v)",
			where, node, len(dropped), dropped)
	}
	result["prefix_conflicts"] = fmt.Sprintf("%d prefix(es) claimed by a rule but assigned to another relay; see log", total)
}

// liveExitNodeIP returns the first usable Tailscale address headscale reports
// for the relay named `hostname`, or "" when headscale has no such node (or no
// address for it).
//
// B292 (2026-09-23): the sync used to read the address ONLY from
// exit_servers.tailscale_ip, a column nothing backfills once the row exists
// (discovery INSERTs with OR IGNORE). The live view is what /admin/exit-nodes
// renders, so consulting it removes the "the page shows an IP but ssh says
// Could not resolve hostname" disagreement.
//
// Matching is case-insensitive against both GivenName and Hostname because
// device_rules.exit_node_id stores whichever the operator saw first, and
// tailscale rewrites the name when a host registers with a different one.
func liveExitNodeIP(hs *headscale.Client, hostname string) string {
	return db.FirstTailscaleIP(strings.Join(liveExitNodeIPs(hs, hostname), ","))
}

// liveExitNodeIPs returns EVERY address headscale reports for the relay named
// `hostname` (empty when headscale has no such node).
//
// B293: the full list matters — the local-transport decision compares it with the
// addresses this host's own daemon owns, and a node can be listed as IPv4 + IPv6
// (the operator's `aro` relay is `100.64.0.1, fd7a:115c:a1e0::1`), so looking at
// only the first entry would miss a match on the other family.
func liveExitNodeIPs(hs *headscale.Client, hostname string) []string {
	if hs == nil || strings.TrimSpace(hostname) == "" {
		return nil
	}
	nodes, err := hs.ListAllNodes()
	if err != nil {
		log.Printf("exit-node sync(%s): cannot read the node list to resolve the SSH target: %v", hostname, err)
		return nil
	}
	want := strings.TrimSpace(hostname)
	for _, n := range nodes {
		if !strings.EqualFold(n.GivenName, want) && !strings.EqualFold(n.Hostname, want) {
			continue
		}
		var out []string
		for _, ip := range n.IPAddresses {
			if ip = strings.TrimSpace(ip); ip != "" {
				out = append(out, ip)
			}
		}
		return out
	}
	return nil
}

// syncOneExitNode is the per-node sync body extracted from
// SyncAdvertisedRoutes. Both SyncAdvertisedRoutes (all-nodes
// loop) and SyncAdvertisedRoutesForNode (per-node) call this
// so the two paths share the same SetAdvertisedRoutes +
// ApproveAllRoutesWithList logic. The result map is written
// in-place (single key: node → "ssh=X approve=Y").
func syncOneExitNode(hs *headscale.Client, d *sql.DB, lookupAcceptRoutes func(string) int, defaultKeyPath, node string, routes []string, result map[string]string) {
	// 2026-07-08: prepend base exit-node routes (0.0.0.0/0, ::/0) so the
	// node stays an exit node after sync. SetAdvertisedRoutes already
	// adds these on the SSH side, but the headscale CLI approve-routes
	// call below only knows about the routes we pass explicitly.
	approveRoutes := []string{"0.0.0.0/0", "::/0"}
	seen := map[string]bool{"0.0.0.0/0": true, "::/0": true}
	for _, r := range routes {
		if !seen[r] {
			seen[r] = true
			approveRoutes = append(approveRoutes, r)
		}
	}
	// Resolve per-exit-node SSH config. The empty-row fallback
	// (no row for this hostname) is fine — SetAdvertisedRoutes
	// will use nodeHostname as the target and defaultKeyPath as
	// the key. The v0.33.1 signature refuses to run when both
	// per-row and default key are empty (so the operator sees
	// a clear "no ssh_key_path" error instead of a silent
	// "config file not found" from ssh).
	//
	// 2026-08-09 v0.33.1.29 B81: sshTarget is resolved through
	// the new LookupExitServerSSHTarget helper which applies
	// the fallback chain operator-override → root@<tailscale_ip>
	// → "". This fixes the "ssh root@<public-ip>:22: Operation
	// timed out" failure mode where the operator had set
	// ssh_target to a firewalled public IP and the SetAdvertisedRoutes
	// call had no way to fall back to the always-reachable
	// Tailscale IP. The legacy "ssh_target empty → nodeHostname"
	// fallback in SetAdvertisedRoutes still exists for the
	// "no exit_servers row at all" case but is intentionally
	// NOT used here — empty ssh_target with a row in
	// exit_servers means "use the auto-fallback", not "fall
	// through to a hostname that doesn't resolve".
	// B300 (2026-09-23): the transport decision (local vs SSH), the SSH target and
	// the approval now live in ONE shared helper — see applyRoutesToRelay. This
	// path and the aggregated staggered loop used to carry separate copies, and
	// the aggregated one was shorter, so it bypassed both B293's locality evidence
	// and B292's target repair.
	// B309: remember whether THIS relay could actually be configured. The prefix
	// assignment reads this record (healthyExitRelaysForAssignment), so a relay
	// that answers headscale but not skygate stops being an owner instead of
	// holding prefixes it will never advertise.
	outcome := applyRoutesToRelay(hs, d, lookupAcceptRoutes, defaultKeyPath, node, approveRoutes)
	recordRelayApply(d, node, outcome)
	result[node] = outcome.resultLabel()
}

// relayApplyOutcome is one relay's route application, rendered identically by
// both sync paths.
type relayApplyOutcome struct {
	// Label is the short result: "local=ok via helper", "ssh=ok", "local=err=…".
	Label string
	// Note carries the extra facts a flash line can afford (self-covering subnets
	// that were refused, the applier's first output line).
	Note string
	// Local reports which transport ran, for the caller's own log line.
	Local bool
	// Evidence names what proved (or ruled out) locality.
	Evidence string
	// Approved and ApproveErr come from the headscale approval step, which runs
	// for BOTH transports: the operator may have approved the routes by other
	// means, and a transport failure must not hide the approval side.
	Approved   int
	ApproveErr error
	// Via names the transport that actually carried the routes — "tailnet",
	// "public" or "name" for the SSH ladder (B310), empty for the local transport
	// (whose Label already says "local=ok via <transport>").
	Via string
	// Endpoint is the concrete address that worked ("tailnet 100.64.0.2:18022"),
	// recorded and rendered so "which path is management using?" has an answer.
	Endpoint string
	// NotEvidence marks an application that failed for a reason that is NOT a
	// fact about the relay: the portal itself is off the tailnet, and every
	// candidate (and every jump hop) can only be reached over that tailnet
	// (B370). The state store keeps the PREVIOUS record — so the relay is not
	// demoted and its prefixes do not migrate — and the reason is logged.
	NotEvidence bool
	// NotEvidenceReason names the condition, for the log line and the page.
	NotEvidenceReason string
}

// resultLabel renders "<label> approved=N|approve=err=…<note>" — the exact shape
// the per-node path has always put in the result map (and therefore in the flash
// message), so the pages keep their wording.
func (o relayApplyOutcome) resultLabel() string {
	approve := "approved=0"
	switch {
	case o.ApproveErr != nil:
		approve = "approve=err=" + o.ApproveErr.Error()
	case o.Approved > 0:
		approve = fmt.Sprintf("approved=%d", o.Approved)
	}
	return o.Label + " " + approve + o.Note
}

// applyRoutesToRelay decides how ONE relay gets its advertised routes and runs it.
//
// B300 (2026-09-23) — WHY THIS IS SHARED. Two paths configure the same relay: the
// per-node one (`syncOneExitNode`, used by SyncAdvertisedRoutes,
// SyncAdvertisedRoutesForNode and the per-row Re-sync button) and the aggregated
// one (`staggeredSync(aggregated)`, which is what the periodic tick and the domain
// auto-updater actually run). The aggregated path carried its own SHORTER copy of
// this body: it never asked `DetectRelayPlacement` and never ran B292's target
// repair. Live on `aro` — where the local daemon IS the relay
// (`Self.HostName=exit-node-vps`, `100.64.0.1`, byte-identical to the headscale
// node) and `exit_servers` has neither `ssh_target` nor `tailscale_ip` — every
// tick therefore handed `SetAdvertisedRoutes` an EMPTY target, ssh fell back to
// the bare node name and died on `Could not resolve hostname exit-node-vps`,
// while the local transport the evidence proves would match was never considered
// (`routes-apply.*` was never even created). Both call sites now ask the same
// questions in the same order.
//
// Order of decisions:
//  1. is this relay THIS host? (B293 evidence chain: the live daemon's
//     Self.TailscaleIPs, then this host's interfaces; a NEGATIVE daemon answer is
//     final, "could not ask" falls back to the interfaces);
//  2. local → refuse to advertise a subnet this host sits inside, then apply
//     through the privilege ladder (direct → sudo -n → the root-owned helper);
//  3. remote → resolve the SSH target (operator override → the live Tailscale IP
//     from headscale, persisted into the empty column → a NAMED warning) and use
//     the SSH transport;
//  4. approve the routes through headscale, whichever transport ran.
func applyRoutesToRelay(hs *headscale.Client, d *sql.DB, lookupAcceptRoutes func(string) int, defaultKeyPath, node string, approveRoutes []string) relayApplyOutcome {
	out := relayApplyOutcome{Label: "ssh=ok"}

	placement := headscale.DetectRelayPlacement(liveExitNodeIPs(hs, node))
	out.Evidence = placement.Evidence
	if placement.Local {
		// Refuse to advertise a subnet this host sits INSIDE (the documented
		// route loop: the host's own traffic to its LAN peers would enter the
		// tunnel and come back). The exit-node bases are exempt.
		kept, skipped := headscale.SelfCoveringRoutes(approveRoutes, placement.SelfIPs)
		if len(skipped) > 0 {
			log.Printf("exit-node sync(%s): local relay is INSIDE %v — NOT advertising those (a co-located relay advertising its own network loops the host's own traffic; docs/networking.md, L-45)", node, skipped)
			out.Note = fmt.Sprintf(" self_subnet_skipped=%s", strings.Join(skipped, ","))
		}
		transport, cmdOut, applyErr := hs.ApplyRoutesLocally(kept, lookupAcceptRoutes(node))
		out.Local = true
		out.Via = "local"
		out.Endpoint = "local (" + transport.Name + ")"
		if applyErr != nil {
			out.Label = "local=err=" + applyErr.Error()
		} else {
			out.Label = "local=ok via " + transport.Name
			if cmdOut != "" {
				out.Note += " out=" + firstLine(cmdOut)
			}
			log.Printf("exit-node sync(%s): routes applied locally via %s (relay IS this host, matched %s by %s) — no SSH involved", node, transport.Name, placement.MatchedIP, placement.Evidence)
		}
	} else {
		if placement.DaemonErr != nil {
			// B293.1: the daemon could not be asked AND the interface list did not
			// match, so this is either a genuinely remote relay or a host whose
			// local addresses are not visible — either way SSH is the transport,
			// and the reason is logged so a silent fallback never hides a broken
			// local path.
			log.Printf("exit-node sync(%s): not a local relay (local daemon unreadable: %v) — using the SSH transport", node, placement.DaemonErr)
		} else {
			// B300: a negative answer from a daemon that DID answer used to be
			// silent, so "why is this relay managed over ssh?" had no answer in
			// the journal. Name the evidence instead.
			log.Printf("exit-node sync(%s): not a local relay (the local daemon answered: matched %q among %v) — using the SSH transport", node, placement.MatchedIP, placement.SelfIPs)
		}

		// Resolve per-exit-node SSH config. The empty-row fallback (no row for
		// this hostname) is fine — the ladder then only has the tailnet
		// addresses headscale reports plus the node name.
		//
		// B310 (2026-09-23): the transport is a LADDER, not a single target — see
		// relay_transport_tailnet_b310.go. The live measurement behind it: the
		// operator's relay row pointed at the TAILNET address 100.64.0.2:18022
		// (the right idea — a tailnet path survives a blocked public IP) while this
		// host had no tailscaled at all, so `ip route get 100.64.0.2` answered
		// "via 192.168.13.1 dev ens18" and every sync ended in an unexplained
		// "Operation timed out".
		cfg := lookupRelaySSHConfig(d, node)
		sshKeyPath := cfg.KeyPath
		if sshKeyPath == "" {
			sshKeyPath = defaultKeyPath
		}
		// B292 (2026-09-23): the row has neither ssh_target nor tailscale_ip, so
		// the B81 chain resolved to "" and SetAdvertisedRoutes fell back to the
		// bare node name — ssh answered "Could not resolve hostname
		// exit-node-vps" while /admin/exit-nodes displayed the relay's Tailscale
		// IP (100.64.0.1) read from headscale. Two sources of truth, one of them
		// invisible.
		//
		// The live headscale view is the authority here: if it reports an address
		// for this relay, use it AND persist it (only when the column is still
		// empty), so the next pass — and the "Use Tailscale IP" button, which
		// resolves through the same helper — work without operator action.
		//
		// B310 keeps the persistence (the page renders the column and the ladder
		// uses it) but no longer FORCES it into the ssh target: an address skygate
		// cannot route to is not a repair, it is the bug.
		if cfg.TailscaleIP == "" {
			if ip := liveExitNodeIP(hs, node); ip != "" {
				log.Printf("exit-node sync(%s): exit_servers has no ssh_target and no tailscale_ip — using the live Tailscale IP %s from headscale (B292)", node, ip)
				if err := db.SetExitServerTailscaleIPIfEmpty(d, node, ip); err != nil {
					log.Printf("exit-node sync(%s): could not persist tailscale_ip=%s: %v", node, ip, err)
				}
				cfg.TailscaleIP = ip
			}
		}
		if cfg.SSHTarget == "" && cfg.TailscaleIP == "" {
			log.Printf("exit-node sync(%s): no ssh_target, no tailscale_ip and headscale reports no address — ssh will be given the bare node name and will most likely fail on DNS (B292)", node)
		}
		// SSH first. On error the approval below still runs — the operator may
		// have approved these routes some other way (e.g. directly via the
		// headscale CLI) and the SSH failure should be visible without blocking
		// the approval side.
		cands, notes := relaySSHEndpoints(hs, cfg, node)
		// B353 (2026-10-06): the ladder's direct candidates can ALL be dead while a
		// peer relay on the same tailnet reaches the target fine — measured live:
		// emilia's DERP home (Helsinki) is unreachable from this site's network
		// while karolina answers it, so the portal had no path to a healthy relay.
		// The jump rung is appended last, so a reachable relay never pays for it.
		var jumpNotes []string
		cands, jumpNotes = appendJumpCandidates(d, hs, cfg, node, cands)
		notes = append(notes, jumpNotes...)
		ladder := applyRoutesOverSSHLadder(hs, node, approveRoutes, lookupAcceptRoutes(node), sshKeyPath, cands, notes)
		if ladder.OK {
			out.Label = "ssh=ok via " + ladder.Via
			out.Via = ladder.Via
			out.Endpoint = ladder.Endpoint
		} else {
			parts := append([]string{}, ladder.Attempts...)
			parts = append(parts, ladder.Notes...)
			out.Label = "ssh=err=" + strings.Join(parts, "; ")
			// B370: was this failure even ABOUT the relay? If the portal is off
			// the tailnet and every candidate needs that tailnet, the answer is
			// no — the portal's own missing interface is not evidence about a
			// relay (measured live: the first pass after an /admin/update
			// recreated the container demoted karolina and moved her prefixes).
			if state := SkygateTailnetState(); !state.Ready && AllCandidatesNeedThePortalTailnet(cands) {
				out.NotEvidence = true
				out.NotEvidenceReason = "the portal is not on the tailnet (" + firstLine(state.Reason) + ")"
			}
		}
	}

	// Approve all routes (including base 0.0.0.0/0, ::/0) via the headscale
	// install-kind ladder. 2026-07-08: pass the full list (base + per-rule) so the
	// node keeps its exit-node capability (default route advertised AND approved).
	if approved, approveErr := hs.ApproveAllRoutesWithList(node, approveRoutes); approveErr != nil {
		out.ApproveErr = approveErr
	} else {
		out.Approved = approved
	}
	return out
}

// firstLine returns the first non-empty line of a command's output, trimmed —
// the sync result string is rendered in a flash message, so it must stay short.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			if len(line) > 120 {
				line = line[:120] + "…"
			}
			return line
		}
	}
	return ""
}

// 2026-07-09: aggregated sync per node (issue: stale batches overwrote each other).
//
// Previous implementation called SetAdvertisedRoutes once per 20-rule batch within
// a single node. Because `tailscale set --advertise-routes=` REPLACES the node's
// advertised-route list, every batch wiped the previous one - only the last
// batch survived. For relay-3 (145 rules) that meant roughly 7 of 8 subnets
// were silently lost after every staggered sync.
//
// New behaviour: even when SKYGATE_STAGGER_SYNC=true and totalRules > batchSize,
// we still call SetAdvertisedRoutes exactly ONCE per node with the full
// de-duplicated list (with 0.0.0.0/0 + ::/0 always prepended). Approve follows
// in the same call. The stagger flag is kept for back-compat but is effectively
// a no-op now - headscale accepts the full payload in one round-trip.
//
// `interval` is still applied between NODES (not between batches within a
// node) so headscale isn't hammered when many exit-nodes sync at once.
func (s *Service) StaggeredSync() {
	if s.Cfg == nil || !s.Cfg.StaggerSync {
		s.SyncAdvertisedRoutes()
		return
	}
	interval := s.Cfg.StaggerInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	// Collect exit_nodes with their rule counts
	rows, _ := s.dbc().Query("SELECT exit_node_id, COUNT(*) FROM device_rules WHERE enabled=1 AND exit_node_id != '' GROUP BY exit_node_id")
	if rows == nil {
		s.SyncAdvertisedRoutes()
		return
	}
	defer rows.Close()
	type nodeRules struct {
		name  string
		count int
	}
	var nodes []nodeRules
	totalRules := 0
	for rows.Next() {
		var n string
		var c int
		if rows.Scan(&n, &c) == nil {
			nodes = append(nodes, nodeRules{n, c})
			totalRules += c
		}
	}
	// B352 (2026-10-05): this list is `GROUP BY exit_node_id` over the RULES, so a relay
	// that lost its last rule is never visited again and keeps its last advertisement
	// (live: emilia advertised 29 prefixes after the assignment table had moved them all
	// to karolina). Append every relay skygate has a recorded apply for, with a zero
	// rule count — the loop below then applies its owned set, which is empty and
	// therefore prunes the stale routes down to the base ones.
	{
		seen := map[string]bool{}
		for _, n := range nodes {
			seen[n.name] = true
		}
		for _, extra := range s.relaysToKeepSynced(seen) {
			nodes = append(nodes, nodeRules{name: extra, count: 0})
		}
	}
	if len(nodes) == 0 {
		s.SyncAdvertisedRoutes()
		return
	}
	// Old behaviour fell through to SyncAdvertisedRoutes when totalRules <= batchSize.
	// SyncAdvertisedRoutes already does aggregated per-node sync, so just call it.
	// Old staggered path is replaced entirely: one SetAdvertisedRoutes per node,
	// not per batch.
	log.Printf("staggeredSync(aggregated): %d rules across %d nodes, interval=%s",
		totalRules, len(nodes), interval)
	go func() {
		// B274: compute prefix ownership ONCE per pass, from the same
		// enabled ip/subnet rules the per-node lists are built from, so
		// two relays can never advertise the same prefix (the live
		// cause of the flapping primary that broke `skyworker`'s
		// Cloudflare/Google destinations).
		// B276: one claim per (relay, prefix) here as well. This query feeds the
		// B274 in-memory fallback (`PrefixOwnership`), whose counts are compared
		// against the table — counting duplicate derived rows would let the
		// auto-updater's churn decide the fallback owner, and (before the table
		// exists) the first applied pins.
		claimRows, cerr := s.dbc().Query("SELECT DISTINCT exit_node_id, target_value FROM device_rules WHERE enabled = 1 AND exit_node_id != '' AND target_type IN ('subnet', 'ip')")
		var claims []PrefixClaim
		if cerr == nil && claimRows != nil {
			for claimRows.Next() {
				var cn, cp string
				if claimRows.Scan(&cn, &cp) == nil {
					claims = append(claims, PrefixClaim{Node: cn, Prefix: cp})
				}
			}
			claimRows.Close()
		}
		owners := PrefixOwnership(claims)
		// B275: the staggered path is the one that actually runs on a
		// normal install (staggered sync defaults ON), so the assignment
		// table must be reconciled HERE too — wiring it into
		// SyncAdvertisedRoutes alone left the table empty and the ACL
		// falling back to each rule's own exit node (live: prefix_owner
		// had 0 rows after the v1.5.22 deploy while staggeredSync kept
		// advertising).
		//
		// B276: and when the table moves a prefix, the ACL must be re-applied in
		// the SAME pass — otherwise this path keeps advertising the new owner
		// while the pin still names the old one.
		if ins, chg, rerr := s.reconcilePrefixOwnership(); rerr != nil {
			log.Printf("prefix-owner: reconcile: %v", rerr)
		} else if ins > 0 || chg > 0 {
			log.Printf("prefix-owner: assignment table updated (inserted=%d changed=%d)", ins, chg)
		}
		if tbl := prefixowner.OwnerByPrefix(s.dbc()); len(tbl) > 0 {
			owners = tbl
		}
		for _, n := range nodes {
			rules, _ := s.dbc().Query("SELECT target_value FROM device_rules WHERE enabled=1 AND exit_node_id=$1 AND target_type IN ('subnet', 'ip')", n.name)
			if rules == nil {
				continue
			}
			var routeList []string
			for rules.Next() {
				var v string
				if rules.Scan(&v) == nil {
					routeList = append(routeList, v)
				}
			}
			rules.Close()
			// B274: drop the prefixes this relay does not own.
			owned := OwnedPrefixes(n.name, routeList, owners)
			droppedCount := len(routeList) - (len(owned))
			if droppedCount > 0 {
				log.Printf("prefix-ownership(staggeredSync): %s drops %d claimed prefix(es) owned by another relay", n.name, droppedCount)
			}
			routeList = owned
			// Always include base exit-node routes.
			batch := []string{"0.0.0.0/0", "::/0"}
			seen := map[string]bool{"0.0.0.0/0": true, "::/0": true}
			for _, r := range routeList {
				if !seen[r] {
					seen[r] = true
					batch = append(batch, r)
				}
			}
			log.Printf("staggeredSync(aggregated): %s advertising %d unique routes (was: per-batch, lost all but last batch)",
				n.name, len(batch))
			// B300 (2026-09-23): the SAME transport decision as the per-node path.
			// This loop used to carry a shorter copy that never asked
			// `DetectRelayPlacement` and never ran B292's target repair — on `aro`
			// (local daemon IS the relay, `exit_servers` empty) that meant an empty
			// ssh target, the bare node name and `Could not resolve hostname
			// exit-node-vps` on every tick, while the local transport that the
			// evidence proves would match was never considered.
			defaultKeyPath := ""
			if s.Cfg != nil {
				defaultKeyPath = s.Cfg.SSHKeyPath
			}
			applied := applyRoutesToRelay(s.HS, s.dbc(), s.lookupAcceptRoutes, defaultKeyPath, n.name, batch)
			// B309: this loop is the one that actually runs on a normal install
			// (staggered sync defaults ON), so the per-relay transport record has
			// to be written HERE too — otherwise an unreachable relay keeps
			// owning prefixes forever while every tick logs its ssh timeout.
			recordRelayApply(s.dbc(), n.name, applied)
			switch {
			case applied.Local:
				log.Printf("staggeredSync(aggregated): %s applied LOCALLY: %s — no SSH involved", n.name, applied.Label)
			case applied.NotEvidence:
				// B370: not an SSH error — the portal itself had no tailnet, so
				// the attempt was not recorded and the relay keeps its prefixes.
				log.Printf("staggeredSync(aggregated): %s SKIPPED (not recorded): %s", n.name, applied.NotEvidenceReason)
			case strings.HasPrefix(applied.Label, "ssh=err="):
				log.Printf("staggeredSync(aggregated): %s SSH err: %s", n.name, strings.TrimPrefix(applied.Label, "ssh=err="))
			default:
				log.Printf("staggeredSync(aggregated): %s advertised: %s", n.name, applied.Label)
			}
			if applied.ApproveErr != nil {
				log.Printf("staggeredSync(aggregated): %s approve err: %v", n.name, applied.ApproveErr)
			}
			time.Sleep(interval)
		}
		log.Printf("staggeredSync(aggregated): done")
	}()
}

// PostSyncAdvertisedRoutes triggers route sync (admin only).
// HTTP entry point for the /admin/exit-rules/sync button.
// Just calls SyncAdvertisedRoutes and returns JSON.
//
// 2026-08-04 v0.33.1: writes an audit_log row with action
// `sync_advertised_routes` and a per-node result summary. The
// audit row is the operator's source of truth when the UI's
// `result` map isn't visible (e.g. triggered by the periodic
// autoupdater rather than the manual button). Without this
// row, the v0.32.x "ok approved=N with broken SSH" bug
// stayed invisible for weeks — the audit_log is the
// dashboard the operator grep's when something looks off.
func (s *Service) PostSyncAdvertisedRoutes(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
		return
	}
	result := s.SyncAdvertisedRoutes()
	// Compose a compact detail string: "nodes=2 ssh_ok=1
	// ssh_err=1 approve_err=0 karolina=ssh=ok approved=214
	// emilia=ssh=err=... approved=0". The per-node breakdown
	// is the diagnostic gold when the operator greps
	// /admin/audit for the failure mode.
	detailParts := []string{}
	sshOK, sshErr, approveErr := 0, 0, 0
	for _, v := range result {
		switch {
		case strings.HasPrefix(v, "ssh=ok "):
			sshOK++
		case strings.HasPrefix(v, "ssh=err="):
			sshErr++
		}
		if strings.Contains(v, "approve=err=") {
			approveErr++
		}
	}
	detailParts = append(detailParts,
		fmt.Sprintf("nodes=%d ssh_ok=%d ssh_err=%d approve_err=%d",
			len(result), sshOK, sshErr, approveErr))
	for node, v := range result {
		detailParts = append(detailParts, fmt.Sprintf("%s=%s", node, v))
	}
	if s.Backend != nil && c != nil {
		s.Backend.Audit(c.UserID, c.Username, "sync_advertised_routes", strings.Join(detailParts, " "))
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}
