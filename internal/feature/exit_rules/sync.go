// Package exit_rules — sync.go owns the advertised-routes
// sync logic, the DNS autoupdater (background goroutine),
// and the /admin/exit-rules/sync HTTP endpoint.
//
// refactor-v0.30 Phase B step 4 (2026-07-29): moved from
// internal/handlers/exit_rules_sync.go. The handlers used
// to be methods on *App; they now live on *Service. The
// HTTP handler (PostSyncAdvertisedRoutes) is exposed for
// the /admin/exit-rules/sync route — it just calls
// SyncAdvertisedRoutes and returns JSON.
//
// RunDomainAutoUpdater stays on *App (see
// internal/handlers/exit_rules_sync.go for the boot-time
// wrapper that main.go calls) — the long-lived context +
// ticker lifecycle are managed there. The wrapper
// delegates to the Service's DomainAutoUpdater +
// staggeredSync methods via the exitRulesRunner interface
// (see internal/handlers/handlers.go).
package exit_rules

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"skygate/internal/acl"
	"skygate/internal/headscale"
	"skygate/internal/prefixowner"

	"skygate/internal/db"
)

// B276 — the ACL must follow the assignment table.
//
// The ownership table and the advertised routes are recomputed every few minutes
// (SyncAdvertisedRoutes / StaggeredSync), while the ACL — whose per-CIDR grants
// carry `via=[owner]` since B275 — was regenerated ONLY when a rule, user or
// device changed. So a prefix that changed relay kept an ACL pin naming the OLD
// relay: headscale's `via` is a permission filter, the client then never receives
// that route, and the traffic silently falls back to the direct path. Live on the
// reference host: the ACL was applied at 18:19, the table moved 28 Cloudflare/
// Google prefixes to the other relay after 19:26, and the operator's device lost
// its whole Cloudflare set while every log line stayed quiet.
//
// These fields live on the package (not on Service) because the apply is triggered
// from two goroutines — the admin/API path and the staggered background sync — and
// they must share one throttle: a churning table must not turn into an apply storm
// (each apply writes an acl_snapshots row and restarts nothing, but it does hit the
// policy API, and on a file-mode host a policy write restarts headscale).
var (
	ownershipACLMu      sync.Mutex
	ownershipACLLastRun time.Time
)

// ownershipACLThrottle bounds how often an ownership-driven ACL re-apply may run.
// Ownership flips are rare once B276's per-device claim counting is in place, so
// this only absorbs a genuinely churning table (the domain auto-updater rewrites
// derived rows every few minutes).
const ownershipACLThrottle = 60 * time.Second

// periodicDriftCheckInterval bounds how often the ownership-stable path compares
// the live policy with the one the database implies (B288). The comparison costs
// one `GenerateACLLiveFormat` + one headscale policy read and writes nothing when
// the two describe the same policy, so it is cheap — but it does not need to run
// on every staggered-sync pass either.
const periodicDriftCheckInterval = 5 * time.Minute

var (
	periodicDriftMu      sync.Mutex
	periodicDriftLastRun time.Time
)

// reconcilePrefixOwnership brings the assignment table up to date with the rules
// and regenerates the ACL when the two disagree — either because this pass MOVED
// a prefix to another relay (B276) or because the live policy was already stale
// for some other reason (B288).
//
// Returns the (inserted, changed) counts the caller logs, so both sync paths report
// the same numbers they used to.
func (s *Service) reconcilePrefixOwnership() (int, int, error) {
	ins, chg, err := prefixowner.Reconcile(s.dbc(), healthyExitRelays(s.dbc()))
	if err != nil {
		return ins, chg, err
	}
	if chg > 0 {
		s.applyACLAfterOwnershipChange(ins, chg)
		return ins, chg, nil
	}
	s.periodicDriftCheck()
	return ins, chg, nil
}

// periodicDriftCheck re-applies the ACL when headscale is serving a policy the
// current database state no longer implies, even though the assignment table did
// not move.
//
// B288 (2026-09-22) — this is the case the B276 commentary already claimed to
// cover ("a pre-existing mismatch created before this release"), but the call
// site gated the whole check on `chg > 0`: the table is stable on a healthy
// install, so a document written by an older generator — or one whose apply
// failed once — stayed stale FOREVER. Live on `aro`: «политика headscale
// УСТАРЕЛА» on /admin/exit-nodes while every exit rule was green and the tailnet
// has a single relay, and nothing in the system was ever going to change it.
//
// Safety: `applyACLIfDrifted` reads the live policy fresh, compares it with
// `headscale.PolicyEquivalent` (set semantics, B288) and returns WITHOUT a write
// when the two describe the same policy — so a converged host does one policy
// read per interval and no write at all. The write itself stays behind
// `ownershipACLThrottle`, and the no-op line is suppressed (the periodic path
// runs unattended; the ownership-triggered path keeps its log).
func (s *Service) periodicDriftCheck() {
	periodicDriftMu.Lock()
	if !periodicDriftLastRun.IsZero() && time.Since(periodicDriftLastRun) < periodicDriftCheckInterval {
		periodicDriftMu.Unlock()
		return
	}
	periodicDriftLastRun = time.Now()
	periodicDriftMu.Unlock()
	s.applyACLIfDriftedMode("skygate-periodic-drift",
		"periodic drift check (the assignment table did not move)", false)
}

// applyACLAfterOwnershipChange regenerates the policy and pushes it when (and only
// when) headscale is actually serving a different policy than the one the current
// ownership table implies.
//
// The comparison is why this is safe to call on every flip: an unchanged table, an
// unchanged rule set or a policy skygate already applied all end in "nothing to
// do" without a write. Failures are logged with the reason and never propagate —
// the routes sync must still finish (the alternative would be a half-synced pass
// where the routes moved and the ACL did not, which is exactly the bug).
func (s *Service) applyACLAfterOwnershipChange(ins, chg int) {
	s.applyACLIfDrifted("skygate-prefix-owner",
		fmt.Sprintf("prefix ownership changed (inserted=%d changed=%d) — ACL regenerated so every per-CIDR via= pin follows the new owner", ins, chg))
}

// applyACLIfDrifted regenerates the policy and pushes it when headscale is serving
// something else (B276).
//
// This is the single "make the control plane match the database" step, and it is
// deliberately trigger-agnostic: the three ways the live policy goes stale are an
// ownership flip (the per-CIDR pin), a rule change (the domain auto-updater adds and
// removes resolved CIDRs on every tick — measured live: 8 of the 15 newest rules had
// no alias in the policy at all), and a pre-existing mismatch created before this
// release. All three are the same question — "is the live policy the one I would
// generate right now?" — so they share one implementation, one throttle and one
// audit trail.
//
// `actor` is recorded as the acl_snapshots author (a stable system name for the
// automatic paths, so the audit trail says what decided).
//
// v1.5.42 (B-pending-write, 2026-09-21): the helper now returns
// acl.ApplyResult instead of bool so callers (PostMyExitRule,
// PostDeleteExitRule) can read the snapshot Version for their
// audit-detail line. Pre-v1.5.42 the handler wrote the snapshot
// silently via acl.ApplyGeneratedPolicy and the calling site had
// no way to know it happened. Callers that only care about the
// boolean "did a write happen?" can check `res.Applied`.
//
// Result semantics:
//   - {Version: 0, Applied: false, Err: nil} — throttle / no
//     headscale client / live-policy already matches. No write.
//   - {Version: 0, Applied: false, Err: <non-nil>} — DB or
//     regeneration failure. The live policy stays stale; the
//     next pass retries.
//   - {Version: N>0, Applied: true, Err: nil} — fresh snapshot
//     written, headscale SetPolicy succeeded. `Version` is the
//     acl_snapshots row id; the calling site should reference
//     it in audit_log and the user-facing success line.
func (s *Service) applyACLIfDrifted(actor, detail string) acl.ApplyResult {
	return s.applyACLIfDriftedMode(actor, detail, true)
}

// applyACLIfDriftedMode is applyACLIfDrifted with control over the "already
// matches" log line: the unattended periodic path (B288) suppresses it so a
// converged host does not write one line per interval forever.
func (s *Service) applyACLIfDriftedMode(actor, detail string, logNoop bool) acl.ApplyResult {
	ownershipACLMu.Lock()
	if !ownershipACLLastRun.IsZero() && time.Since(ownershipACLLastRun) < ownershipACLThrottle {
		ownershipACLMu.Unlock()
		log.Printf("acl-drift: %s needs a re-apply but one ran %s ago — deferring to the next pass (throttle %s)",
			detail, time.Since(ownershipACLLastRun).Round(time.Second), ownershipACLThrottle)
		return acl.ApplyResult{Version: 0, Applied: false, Err: nil}
	}
	ownershipACLLastRun = time.Now()
	ownershipACLMu.Unlock()

	if s.HS == nil {
		log.Printf("acl-drift: %s but no headscale client is wired — the live policy is STALE; re-apply it manually on /admin/exit-rules", detail)
		return acl.ApplyResult{Version: 0, Applied: false, Err: nil}
	}
	gen, err := acl.GenerateACLLiveFormat(s.dbc())
	if err != nil {
		log.Printf("acl-drift: cannot regenerate the ACL (%s): %v — the live policy stays stale", detail, err)
		return acl.ApplyResult{Version: 0, Applied: false, Err: err}
	}
	// Compare against what headscale is serving. The cached policy may predate the
	// last apply, so force a fresh read: a stale "in sync" verdict here would skip
	// exactly the re-apply this function exists for.
	s.HS.InvalidateCache()
	live, err := s.HS.GetACL()
	if err != nil {
		log.Printf("acl-drift: cannot read the live policy to decide whether a re-apply is needed (%v) — applying unconditionally", err)
	} else if same, cmpErr := headscale.PolicyEquivalent(gen, live); cmpErr == nil && same {
		if logNoop {
			log.Printf("acl-drift: live policy already matches the generated one (generated=%d live=%d bytes) — %s", len(gen), len(live), detail)
		}
		return acl.ApplyResult{Version: 0, Applied: false, Err: nil}
	} else if cmpErr != nil {
		log.Printf("acl-drift: cannot compare the live policy with the generated one (%v) — applying unconditionally", cmpErr)
	}
	// B288.1: if the privileged applier reported a failure the last time it ran,
	// say so HERE. The handoff (a rename into the watched directory) succeeds
	// even when the write later fails or is rolled back, so without this line the
	// journal shows a successful apply every tick while the live policy never
	// changes — which is exactly how the live host stayed stale for days with a
	// green acl_snapshots row every five minutes.
	if st, ok := headscale.ReadPolicyApplyStatus(); ok && st.Result != "ok" && st.Result != "unchanged" {
		log.Printf("acl-drift: the privileged policy applier last reported %s — the write is NOT landing; check %s and %s",
			st.Summary(), headscale.PolicyApplyStatusPath(), "the applier's log next to it")
	}

	res := acl.ApplyGeneratedPolicy(s.dbc(), s.HS, gen, actor, detail, nil)
	if res.Err != nil {
		log.Printf("acl-drift: re-apply FAILED (%v) — the live policy stays stale; the next pass retries", res.Err)
		if s.Notifier != nil {
			go s.Notifier.SendAlert(fmt.Sprintf("❌ the headscale policy is stale but the re-apply failed\n  %s\n  err: %v", detail, res.Err))
		}
		return res
	}
	log.Printf("acl-drift: ACL re-applied (snapshot v%d, generated=%d bytes) — %s", res.Version, len(gen), detail)
	return res
}

// knownSubdomains maps a main domain to its known subdomain hosts for static assets.
// 2026-07-07: issue #9 — Cloudflare-routed sites have static on different subdomains.
var knownSubdomains = map[string][]string{
	"rutracker.org": {"static.rutracker.cc"},
	"rutracker.cc":  {"static.rutracker.cc"},
}

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
	rows, err := s.dbc().Query("SELECT DISTINCT exit_node_id, target_value FROM device_rules WHERE enabled = 1 AND (target_type = 'ip' OR target_type = 'subnet') ORDER BY exit_node_id")
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
	rows, err := s.dbc().Query("SELECT target_value FROM device_rules WHERE enabled = 1 AND exit_node_id = $1 AND (target_type = 'ip' OR target_type = 'subnet') ORDER BY target_value", node)
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
	sshRow, _ := db.LookupExitServerSSH(d, node)
	sshTarget, _ := db.LookupExitServerSSHTarget(d, node)
	sshKeyPath := sshRow.KeyPath
	if sshKeyPath == "" {
		sshKeyPath = defaultKeyPath
	}
	// B292 (2026-09-23): the row has neither ssh_target nor tailscale_ip, so the
	// B81 chain resolved to "" and SetAdvertisedRoutes fell back to the bare node
	// name — ssh answered "Could not resolve hostname exit-node-vps" while
	// /admin/exit-nodes displayed the relay's Tailscale IP (100.64.0.1) read from
	// headscale. Two sources of truth, one of them invisible.
	//
	// The live headscale view is the authority here: if it reports an address for
	// this relay, use it AND persist it (only when the column is still empty), so
	// the next pass — and the "Use Tailscale IP" button, which resolves through
	// the same helper — work without operator action.
	if strings.TrimSpace(sshTarget) == "" {
		if ip := liveExitNodeIP(hs, node); ip != "" {
			log.Printf("exit-node sync(%s): exit_servers has no ssh_target and no tailscale_ip — using the live Tailscale IP %s from headscale (B292)", node, ip)
			if err := db.SetExitServerTailscaleIPIfEmpty(d, node, ip); err != nil {
				log.Printf("exit-node sync(%s): could not persist tailscale_ip=%s: %v", node, ip, err)
			}
			sshTarget = "root@" + ip
		} else {
			log.Printf("exit-node sync(%s): no ssh_target, no tailscale_ip and headscale reports no address — ssh will be given the bare node name and will most likely fail on DNS (B292)", node)
		}
	}
	// SSH first. On error, we STILL try the headscale approve
	// step below — the operator may have already approved these
	// routes some other way (e.g. directly via the headscale
	// CLI) and the SSH failure should be visible but not block
	// the approval side.
	//
	// B293 (2026-09-23): unless this relay IS this host. On the operator's `aro`
	// the local tailscaled is `exit-node-vps` (100.64.0.1) — headscale, skygate
	// and the exit node live on one machine — so managing it over SSH meant an
	// SSH session from the host to itself, through the tailnet it configures:
	// pointless (a key + authorized_keys on the same box) and fragile (it fails
	// exactly when the local tailscaled is the thing needing repair). The
	// decision is made from EVIDENCE — the live daemon's own Self.TailscaleIPs
	// against the relay's headscale addresses — and "I could not ask" keeps the
	// SSH path.
	routeLabel := "ssh=ok"
	transportNote := ""
	if localSelf, selfErr := headscale.LocalTailscaleSelf(); selfErr == nil {
		if matchedIP, isLocal := headscale.IsLocalRelay(localSelf, liveExitNodeIPs(hs, node)); isLocal {
			// Refuse to advertise a subnet this host sits INSIDE (the documented
			// route loop: the host's own traffic to its LAN peers would enter the
			// tunnel and come back). The exit-node bases are exempt.
			kept, skipped := headscale.SelfCoveringRoutes(approveRoutes, localSelf.IPs)
			if len(skipped) > 0 {
				log.Printf("exit-node sync(%s): local relay is INSIDE %v — NOT advertising those (a co-located relay advertising its own network loops the host's own traffic; docs/networking.md, L-45)", node, skipped)
				transportNote = fmt.Sprintf(" self_subnet_skipped=%s", strings.Join(skipped, ","))
				result[node+"_skipped"] = strings.Join(skipped, ",")
			}
			transport, out, applyErr := hs.ApplyRoutesLocally(kept, lookupAcceptRoutes(node))
			switch {
			case applyErr != nil:
				routeLabel = "local=err=" + applyErr.Error()
			default:
				routeLabel = "local=ok via " + transport.Name
				if out != "" {
					transportNote += " out=" + firstLine(out)
				}
				log.Printf("exit-node sync(%s): routes applied locally via %s (relay IS this host, matched %s) — no SSH involved", node, transport.Name, matchedIP)
			}
			approveLabel := "approved=0"
			if approved, approveErr := hs.ApproveAllRoutesWithList(node, approveRoutes); approveErr != nil {
				approveLabel = "approve=err=" + approveErr.Error()
			} else if approved > 0 {
				approveLabel = fmt.Sprintf("approved=%d", approved)
			}
			result[node] = routeLabel + " " + approveLabel + transportNote
			return
		}
	} else {
		log.Printf("exit-node sync(%s): cannot ask the local tailscaled who it is (%v) — using the SSH transport", node, selfErr)
	}
	sshLabel := "ok"
	_, sshErr := hs.SetAdvertisedRoutes(node, approveRoutes, lookupAcceptRoutes(node), sshTarget, sshKeyPath)
	if sshErr != nil {
		sshLabel = "err=" + sshErr.Error()
	}
	routeLabel = "ssh=" + sshLabel
	// Approve all routes (including base 0.0.0.0/0, ::/0) for this exit
	// node via headscale CLI (docker exec).
	// 2026-07-08: pass full list (base + per-rule) so the node keeps
	// its exit-node capability (default route advertised AND approved).
	approveLabel := "approved=0"
	if approved, approveErr := hs.ApproveAllRoutesWithList(node, approveRoutes); approveErr != nil {
		approveLabel = "approve=err=" + approveErr.Error()
	} else if approved > 0 {
		approveLabel = fmt.Sprintf("approved=%d", approved)
	}
	result[node] = routeLabel + " " + approveLabel
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
			// 2026-08-04 v0.33.1: per-node SSH config (was hard-coded
			// /home/admin/.ssh/config + nodeHostname, both of which
			// broke in the dockerised skygate).
			// 2026-08-09 v0.33.1.29 B81: sshTarget uses the new
			// helper with the operator-override → Tailscale IP
			// fallback chain (see SyncAdvertisedRoutes for the
			// full rationale). The key path stays on
			// LookupExitServerSSH + Cfg.SSHKeyPath fallback.
			sshRow, _ := db.LookupExitServerSSH(s.dbc(), n.name)
			sshTarget, _ := db.LookupExitServerSSHTarget(s.dbc(), n.name)
			sshKeyPath := sshRow.KeyPath
			if sshKeyPath == "" && s.Cfg != nil {
				sshKeyPath = s.Cfg.SSHKeyPath
			}
			msg, sshErr := s.HS.SetAdvertisedRoutes(n.name, batch, s.lookupAcceptRoutes(n.name), sshTarget, sshKeyPath)
			// 2026-07-11: `tailscale set` on unix exits 0 with empty stdout, so
			// `msg` is often "". Render an "ok" marker instead of a dangling colon.
			if strings.TrimSpace(msg) == "" && sshErr == nil {
				msg = "ok"
			}
			if sshErr != nil {
				log.Printf("staggeredSync(aggregated): %s SSH err: %v", n.name, sshErr)
			} else {
				log.Printf("staggeredSync(aggregated): %s advertised: %s", n.name, msg)
			}
			if _, err := s.HS.ApproveAllRoutesWithList(n.name, batch); err != nil {
				log.Printf("staggeredSync(aggregated): %s approve err: %v", n.name, err)
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

// 2026-07-07: issue #6 — DomainAutoUpdater
// Background job: resolves all domain rules every interval, reconciles with /32 IP rules.
// Returns count of changes (added + removed) and writes log entries.
func (s *Service) DomainAutoUpdater() (added, removed int, err error) {
	// B274: collapse the redundant rows the CDN expansion produces.
	// One domain rule for a Cloudflare-fronted site yields the CDN's
	// whole published range set, so N domains of the same CDN create N
	// identical rows per CIDR (live: `basic` carried 5 rows for
	// `104.16.0.0/12`, one per discord.*/rutracker.org parent). The rows
	// are not wrong (parent_domain is part of the natural key by B183),
	// but they inflate rule counts, the admin "expected routes" counter
	// and the prefix-ownership weight. Keep the most informative parent
	// (the cdn:-prefixed one), drop the rest. Pure data hygiene — the
	// ACL is unaffected because the generator collapses CIDRs into one
	// host alias anyway.
	//
	// It runs DEFERRED, after the resolve loop below: the loop inserts
	// one row per (domain, CIDR) pair — that is what creates the
	// duplicates — so deduplicating before it would be undone by the
	// very same pass (observed live: 47 groups right after a tick that
	// started with 0).
	defer func() {
		if n, derr := s.CollapseDuplicateDerivedRules(); derr != nil {
			log.Printf("auto-updater: dedup: %v", derr)
		} else if n > 0 {
			log.Printf("auto-updater: dedup removed %d redundant derived rule row(s)", n)
		}
	}()
	rows, qerr := s.dbc().Query("SELECT id, user_id, device_id, exit_node_id, target_value, action, COALESCE(device_ip,'') FROM device_rules WHERE enabled = 1 AND target_type = 'domain'")
	if qerr != nil {
		return 0, 0, qerr
	}
	defer rows.Close()
	type domainRule struct {
		id       int
		userID   int64
		deviceID int
		exitNode string
		domain   string
		action   string
		deviceIP string
	}
	var domains []domainRule
	for rows.Next() {
		var r domainRule
		var uid int64
		if err := rows.Scan(&r.id, &uid, &r.deviceID, &r.exitNode, &r.domain, &r.action, &r.deviceIP); err == nil {
			r.userID = uid
			domains = append(domains, r)
		}
	}

	for _, d := range domains {
		// 2026-07-28: CDN detection — short-circuit before DNS if
		// we already have a CDN range rule for THIS SPECIFIC
		// domain. The marker format is "cdn:<name>:<domain>".
		// The ranges don't churn (stable network allocations),
		// so the autoupdater has nothing to do for these.
		//
		// The check is per-domain, NOT per-(user, device,
		// exit_node): once auth.docker.io got its CDN marker,
		// a naive (user, device, exit_node) check would also
		// short-circuit artstation.com (because both share the
		// same user=1/device=9/exit_node=relay-3 tuple), even
		// though artstation doesn't yet have a CDN marker. The
		// autoupdate would never process artstation again.
		//
		// We use LIKE 'cdn:%:<domain>' so the CDN-name slot
		// matches any CDN (cloudflare/fastly/google/akamai)
		// without us having to know the CDN name in advance.
		// A future autoupdate tick that runs AFTER CDN detection
		// has inserted the marker will match here and short-
		// circuit; the per-tick no-op.
		existingMarker := ""
		_ = s.dbc().QueryRow(
			"SELECT parent_domain FROM device_rules WHERE user_id=$1 AND device_id=$2 AND exit_node_id=$3 AND target_type='subnet' AND parent_domain LIKE $4 LIMIT 1",
			d.userID, d.deviceID, d.exitNode, cdnParentMarkerGuess(d.domain),
		).Scan(&existingMarker)
		if isCDNMarker(existingMarker) {
			// Already have a CDN range rule for this domain.
			// Nothing to do.
			continue
		}

		addrs, lerr := net.LookupHost(d.domain)
		if lerr != nil {
			s.logAutoUpdate(d.id, d.domain, 0, 0, "lookup failed: "+lerr.Error())
			continue
		}
		currentIPs := map[string]bool{}
		for _, addr := range addrs {
			if strings.Contains(addr, ":") {
				continue // skip IPv6
			}
			currentIPs[addr] = true
		}
		if extraIPs := s.resolveDomainSubdomains(d.domain); extraIPs != nil {
			for ip := range extraIPs {
				currentIPs[ip] = true
			}
		}

		// 2026-07-28: CDN detection — if all currentIPs fall in
		// a known CDN's published ranges, replace the per-IP /32
		// approach with the CDN's CIDR ranges. The ranges are
		// stable, so the autoupdater doesn't churn for these
		// domains.
		if cdnName, cdnCIDRs, isCDN := detectCDN(currentIPs); isCDN {
			marker := cdnParentMarker(cdnName, d.domain)
			// Insert each CDN range. The marker lets the next
			// tick short-circuit (see the existingMarker check
			// at the top of the loop).
			cdnAdded := 0
			for _, cidr := range cdnCIDRs {
				// B125: rely on the UNIQUE INDEX
				// device_rules_natural_key_uniq (added in
				// migrateV056PG, re-created in migrateV068PG as
				// 6-col to match B188.2's intent) + ON CONFLICT
				// DO NOTHING to close the SELECT-then-INSERT race
				// that previously let duplicate rows accumulate.
				// The pre-check SELECT is still useful for the
				// cdnAdded counter (to know if a NEW row was
				// created vs an existing one was hit), but the
				// race is closed by the conflict target.
				//
				// 2026-09-07 (B237.23): conflict target is 6
				// columns (WITH parent_domain) to match the
				// 6-col UNIQUE INDEX on the live DB and the
				// qInsertDeviceRule contract in queries.go.
				// The pre-B237.23 5-col target (B183) was a
				// silent code/index drift: V068 (B232) recreated
				// the index as 6-col but didn't update sync.go,
				// so every INSERT here hit
				// `no unique or exclusion constraint matching`
				// and the `if err != nil { continue }` below
				// silently swallowed it. Net effect: /32 rows
				// for the 15 Cloudflare CIDRs were never
				// created when the marker (cdn:cloudflare:foo)
				// already had rows under a DIFFERENT marker
				// (cdn:cloudflare:discordapp.com); the autoupdate
				// logged `added=0` and the UI's B184 status
				// check saw "no resolved subnets" → ⏳ orange
				// forever (false positive — the rules work,
				// karolina's ApprovedRoutes has the IP).
				tag, err := s.dbc().Exec(
					`INSERT INTO device_rules (user_id, device_id, exit_node_id, target_type, target_value, action, device_ip, parent_domain)
					 VALUES ($1, $2, $3, 'subnet', $4, $5, $6, $7)
					 ON CONFLICT (user_id, device_id, exit_node_id, target_type, target_value, parent_domain) DO NOTHING`,
					d.userID, d.deviceID, d.exitNode, cidr, d.action, d.deviceIP, marker)
				if err != nil {
					continue
				}
				if n, _ := tag.RowsAffected(); n > 0 {
					cdnAdded++
				}
			}
			// Remove the legacy per-IP /32 rules for this
			// domain — they have parent_domain = d.domain (no
			// cdn: prefix). Now that the CDN marker covers the
			// domain, the /32 rules are dead weight.
			legacyRemoved := 0
			if _, err := s.dbc().Exec(
				"DELETE FROM device_rules WHERE user_id=$1 AND device_id=$2 AND exit_node_id=$3 AND target_type='subnet' AND COALESCE(parent_domain,'')=$4",
				d.userID, d.deviceID, d.exitNode, d.domain,
			); err == nil {
				// RowsAffected isn't in the go-sqlite3 driver
				// by default; we count via a SELECT instead.
				// (legacyRemoved is a coarse metric — used for
				//  the log line only.)
				_ = legacyRemoved
			}
			added += cdnAdded
			s.logAutoUpdate(d.id, d.domain, cdnAdded, 0, "CDN detected: "+cdnName+" — using "+strconv.Itoa(len(cdnCIDRs))+" published ranges")
			continue
		}

		// Get existing /32 rules for this domain
		existing := map[string]int{} // IP -> rule id
		rows2, eerr := s.dbc().Query("SELECT id, target_value FROM device_rules WHERE user_id=$1 AND device_id=$2 AND exit_node_id=$3 AND target_type='subnet' AND target_value LIKE '%/32'",
			d.userID, d.deviceID, d.exitNode)
		if eerr != nil {
			continue
		}
		// Filter: only IPs that are NOT explicitly in currentIPs (could be from other rules)
		// Strategy: for each IP in currentIPs that's not in DB → INSERT
		//           for each /32 IP in DB that resolves to a removed domain IP → DELETE
		// We track: for THIS domain, which /32 IPs correspond?
		// Simplification: we know d.domain is the source, so any /32 that matches
		// the pattern and exists in oldIPs but not in currentIPs is from this domain.
		_ = existing
		rows2.Close()

		// Find all /32 rules for (user, device, exit_node) that LOOK like auto-resolved from this domain
		// We track them via a side table OR a heuristic: for this domain, list all /32 rules where
		// the same domain's last resolved IPs included them.
		// Pragmatic approach: maintain a comment-style hint in another table? Or use a marker.
		// Simpler: for this domain, list ALL /32 rules and diff against currentIPs.
		// User-added /32 rules (manual) get deleted if we don't track — TOO DANGEROUS.
		// Better: introduce column `parent_domain` (NULL = manual).
		all32 := map[string]int{}
		rows3, _ := s.dbc().Query("SELECT id, target_value FROM device_rules WHERE user_id=$1 AND device_id=$2 AND exit_node_id=$3 AND target_type='subnet' AND target_value LIKE '%/32' AND COALESCE(parent_domain,'')=$4",
			d.userID, d.deviceID, d.exitNode, d.domain)
		if rows3 != nil {
			for rows3.Next() {
				var rid int
				var val string
				if rows3.Scan(&rid, &val) == nil {
					// strip /32
					ip := strings.TrimSuffix(val, "/32")
					all32[ip] = rid
				}
			}
			rows3.Close()
		}

		// Add new IPs
		for ip := range currentIPs {
			if _, exists := all32[ip]; exists {
				continue
			}
			// B125: use ON CONFLICT DO NOTHING (against the
			// UNIQUE INDEX device_rules_natural_key_uniq from
			// migrateV056PG, re-created in migrateV068PG as
			// 6-col) instead of the pre-check + INSERT race.
			// The pre-check is preserved for the "shared IP
			// between domains" case (B123 alert UX) — when
			// another domain already added the /32, the
			// conflict target skips silently.
			//
			// 2026-09-07 (B237.23): conflict target is 6
			// columns (WITH parent_domain) to match the
			// 6-col UNIQUE INDEX on the live DB. Pre-B237.23
			// the 5-col target (B183) silently failed every
			// INSERT because V068 (B232) recreated the index
			// as 6-col but didn't update sync.go. With 6-col
			// ON CONFLICT, two parent_domains resolving to
			// the same /32 (e.g. www.harness.io and
			// harness.io both → 44.246.83.163) each get
			// their own row — the B184 status check
			// correctly finds the /32 for the parent_domain
			// it's looking for. See B237.23 entry in
			// AGENTS.md for the full regression analysis.
			tag, ierr := s.dbc().Exec(
				`INSERT INTO device_rules (user_id, device_id, exit_node_id, target_type, target_value, action, device_ip, parent_domain)
				 VALUES ($1, $2, $3, 'subnet', $4, $5, $6, $7)
				 ON CONFLICT (user_id, device_id, exit_node_id, target_type, target_value, parent_domain) DO NOTHING`,
				d.userID, d.deviceID, d.exitNode, ip+"/32", d.action, d.deviceIP, d.domain)
			if ierr != nil {
				continue
			}
			if n, _ := tag.RowsAffected(); n > 0 {
				added++
			}
		}
		// Remove old IPs
		for ip, rid := range all32 {
			if currentIPs[ip] {
				continue
			}
			if _, derr := s.dbc().Exec("DELETE FROM device_rules WHERE id=$1", rid); derr == nil {
				removed++
			}
		}

		if len(currentIPs) > 0 || len(all32) > 0 {
			s.logAutoUpdate(d.id, d.domain, added, removed, "")
		}
	}

	// B276: the rule set just changed (or did not — the derived rows are rewritten
	// on every tick), so ask the only question that matters for the control plane:
	// is headscale serving the policy skygate would generate right now? Live, 8 of
	// the 15 newest rules had NO alias in the policy at all — the ACL was only ever
	// regenerated by an explicit rule/user/device change, so resolved domains and
	// reassigned prefixes silently outran it. Running the comparison here makes the
	// policy converge on its own, without the operator pressing anything, and the
	// equivalence guard means a quiet tick costs one GenerateACL and nothing else.
	//
	// B276.1 runs FIRST so the ACL generated below already covers the devices a
	// user's "all my devices" rules were just extended to; otherwise those rows
	// would wait a whole tick for their grants.
	defer func() {
		if n, perr := s.propagateAllDeviceRules(); perr != nil {
			log.Printf("all-devices: propagation failed: %v", perr)
		} else if n > 0 {
			log.Printf("all-devices: %d rule row(s) added for newly registered devices", n)
		}
		s.applyACLIfDrifted("skygate-auto-updater",
			fmt.Sprintf("auto-updater tick changed %d rule(s) (added=%d removed=%d)", added+removed, added, removed))
	}()

	return added, removed, nil
}

// resolveDomainSubdomains resolves known subdomains and (optionally) fetches
// the main page to discover subdomains from href/src attributes. Returns a set
// of IPv4 addresses to add to the rule list.
func (s *Service) resolveDomainSubdomains(domain string) map[string]bool {
	httpClient := &http.Client{Timeout: 8 * time.Second}
	var body []byte

	// Check known subdomains first (fast path)
	ips := map[string]bool{}
	for _, sd := range knownSubdomains[domain] {
		if addrs, err := net.LookupHost(sd); err == nil {
			for _, ip := range addrs {
				if !strings.Contains(ip, ":") {
					ips[ip] = true
				}
			}
		}
	}
	if len(ips) > 0 {
		s.logAutoUpdate(0, domain, len(ips), 0, "known subdomains resolved: "+strconv.Itoa(len(knownSubdomains[domain])))
		return ips
	}

	for _, scheme := range []string{"https", "http"} {
		resp, err := httpClient.Get(scheme + "://" + domain + "/")
		if err != nil {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
		resp.Body.Close()
		if err == nil {
			body = b
			break
		}
	}
	if len(body) == 0 {
		return nil
	}

	subdomains := map[string]bool{}
	hostRe := regexp.MustCompile(`(?:href|src)=["']https?://([^/\s"']+)`)
	for _, m := range hostRe.FindAllStringSubmatch(string(body), -1) {
		host := m[1]
		// Skip self and subdomains of self
		if host == domain || strings.HasSuffix(host, "."+domain) {
			continue
		}
		subdomains[host] = true
	}
	for host := range subdomains {
		if addrs, err := net.LookupHost(host); err == nil {
			for _, ip := range addrs {
				if !strings.Contains(ip, ":") {
					ips[ip] = true
				}
			}
		}
	}
	if len(ips) > 0 {
		s.logAutoUpdate(0, domain, len(ips), 0, "subdomains resolved: "+strconv.Itoa(len(subdomains)))
	}
	return ips
}

func (s *Service) logAutoUpdate(ruleID int, domain string, added, removed int, errMsg string) {
	detail := fmt.Sprintf("domain=%s added=%d removed=%d", domain, added, removed)
	if errMsg != "" {
		detail += " err=" + errMsg
	}
	_ = db.AppendExitRuleLog(s.dbc(), db.ExitRuleLogNoVersion, db.ExitRuleActionAutoupdate, detail)
}

// lookupAcceptRoutes returns the per-exit-node Tailscale AcceptRoutes
// preference stored in exit_servers.accept_routes:
//
//	-1 -> --accept-routes=false (nodes that co-host another VPN, e.g. Amnezia-AWG)
//	 0 -> unset, do not change AcceptRoutes on the node
//	 1 -> --accept-routes=true
//
// Lookup is keyed on the node's hostname. Falls back to 0 (do not change)
// if the node is not in exit_servers or the column is missing.
//
// 2026-07-12: Этап 10 part 5 — moved the SELECT to db.LookupExitServerAcceptRoutes
// (which centralises the column name + the no-row fallback to 0).
func (s *Service) lookupAcceptRoutes(nodeHostname string) int {
	if s == nil || s.dbc() == nil || nodeHostname == "" {
		return 0
	}
	accept, _ := db.LookupExitServerAcceptRoutes(s.dbc(), nodeHostname)
	return accept
}
