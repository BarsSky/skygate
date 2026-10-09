// Package exit_rules — sync_routes_converge_b374.go (B374, 2026-10-09).
//
// THE ADVERTISED ROUTES HAD NO PERIODIC OWNER.
//
// Measured live on the reference deployment, 2026-10-09. The container was
// recreated at 14:08 UTC; the first route-apply pass after the recreate could not
// reach karolina (SSH 100.64.0.2:18022 timed out), so the B352 machinery excluded
// it and moved 197 prefixes to emilia in `prefix_owner` and re-pinned the device
// preferences (skyworker/basic/cyborg → tag:dev-infra-emilia). That part worked.
//
// What never happened is the APPLY. emilia's `--advertise-routes` was still the set
// from before the move: `headscale nodes list` showed `emilia available=2
// approved=2` (only 0.0.0.0/0 and ::/0) while karolina still held `199/199`, and
// `global_settings[relay_apply_state:emilia]` kept its 13:01:07 timestamp for the
// next 75+ minutes. The container's whole log held ZERO lines containing
// `advertis` or `staggeredSync`. Every device whose ACL grant pins
// `via=[tag:dev-infra-emilia]` (e.g. skyworker) therefore lost YouTube, Telegram
// and every other pinned prefix. A manual "Re-sync all"
// (`POST /admin/exit-nodes/sync`) fixed it in seconds: emilia 205/205 approved,
// karolina 199→2, both `relay_apply_state = ok`.
//
// ROOT CAUSE. The ONLY periodic caller of `StaggeredSync()` was the domain
// auto-updater loop (internal/handlers/handlers.go, RunDomainAutoUpdater):
//
//	added, removed, err := a.exitRulesSvc.DomainAutoUpdater()
//	if added > 0 || removed > 0 {
//	    a.exitRulesSvc.StaggeredSync()
//	}
//
// and the live journal said, every tick:
//
//	auto-updater: 18 domain(s) skipped (re-resolve interval 6h0m0s; the derived
//	rows and the ACL stay put until then)
//
// i.e. `added=0 removed=0` → no route sync for hours. The assignment table IS
// recomputed by maintenance ticks that are independent of DNS churn
// (RunPreferredExitReconciler → ReconcileDeviceExitNodePrefs, and the ownership
// pass that hangs off DomainAutoUpdater), but NOTHING periodically pushed the
// result to the relays. Domain churn was the only trigger of the data plane, and
// the data plane is exactly what the control plane (`prefix_owner` + the per-CIDR
// `via=` pins) is derived from.
//
// THE FIX. `ConvergeAdvertisedRoutes(reason)` is a periodic, throttled, idempotent
// reconciliation of "what each relay advertises" against "what `prefix_owner`
// assigns it", called from the ~5-minute maintenance tick with its own budget
// (`routeConvergenceInterval`) and its own log prefix (`route-converge:`).
//
// WHY IT DOES NOT BECOME AN SSH STORM (the hard requirement). The decision is the
// pure set comparison `RoutesNeedConvergence`, evaluated BEFORE any transport is
// touched. It normalises both sides to a set (order-insensitive), treats
// 0.0.0.0/0 and ::/0 as always-required on both sides, and answers "no" for every
// relay whose advertisement already equals its owned set — including the common
// case of a relay that has never advertised anything (an empty advertised list
// normalises to exactly {0.0.0.0/0, ::/0}, which is what a relay that owns no
// prefixes requires). For such a relay `ConvergeAdvertisedRoutes` performs NO SQL
// write, NO headscale call beyond the single node read the pass already does, and
// NO call into the apply path — so no `ssh` (and no local `tailscale set`) process
// is ever created. Only a relay with a genuine difference is applied, through the
// SAME shared path the manual Re-sync and StaggeredSync use (`syncOneExitNodeFn`),
// so the two cannot drift.
//
// The pass is deliberately NOT fatal: every failure is logged with its reason and
// the loop continues (a headscale read that fails aborts only this tick).
package exit_rules

import (
	"database/sql"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"skygate/internal/db"
)

// routeConvergenceInterval bounds how often the periodic route-convergence pass
// may apply anything. It matches the ~5-minute maintenance tick the pass is wired
// into, so a difference is acted on within one tick and a converged host pays one
// headscale node read per tick.
//
// WHY A THROTTLE AT ALL when the predicate already suppresses the converged case:
// the throttle is the second belt for the case the predicate CANNOT suppress — a
// difference the apply cannot clear (an unreachable relay, an advertisement
// headscale refuses). Without it, three independent callers (a manual sync, the
// auto-updater tick and the maintenance tick) could each open a transport inside
// one interval. With it, at most one apply pass runs per interval, and the
// deferral is logged so it can never be silent (L-60).
const routeConvergenceInterval = 5 * time.Minute

// routeConvergenceLogPrefix is the journal prefix of this pass. It is distinct from
// `prefix-owner:`, `acl-drift:` and `staggeredSync(aggregated):` on purpose: the
// whole point of B374 is that the journal must show WHETHER the route plane ran
// and what it did — exactly what was missing for 75 minutes on 2026-10-09.
const routeConvergenceLogPrefix = "route-converge:"

// SettingRelayAdvertiseStalePrefix holds the B374 part-3 record: a relay that was
// excluded from the healthy set AND is still advertising prefixes the assignment
// table no longer gives it. The portal could not reach it, so it could not prune
// the advertisement; the fact is named here instead of staying invisible.
//
// Key "<prefix><relay>", value
// "<unix>|<relay> still advertises N prefix(es) that prefix_owner no longer assigns to it: <detail>".
const SettingRelayAdvertiseStalePrefix = "relay_advertise_stale:"

// syncOneExitNodeFn is the ONE per-relay apply step, as a variable so a test can
// inject a recorder instead of running a real transport.
//
// WHY A SEAM IS NEEDED AT ALL (B374). The property this block must prove is "the
// periodic pass applies a relay whose advertised set differs and does NOT touch a
// converged one". Proving the second half by running the real path is impossible
// (it would need a reachable relay) and proving the first half by running the real
// path is exactly what a test must never do — ssh, a tailscaled socket or the
// privileged helper. The seam is deliberately the SMALLEST one that answers the
// question: it is the same `syncOneExitNode` every other path calls (assigned
// below), so production behaviour is unchanged, and an injected recorder observes
// the same call the operator's "Re-sync" button would make. It cannot hide a
// transport regression, because no production caller ever replaces it.
//
// NOTE for anyone tempted to add a second seam: this one exists for the reason
// above and for nothing else. A test that wants to observe the TRANSPORT should
// drive `applyRoutesToRelay` directly (as the B293/B300/B310/B353 tests do); a test
// that wants to observe the DECISION uses this.
var syncOneExitNodeFn = syncOneExitNode

var (
	// routeConvergenceMu guards routeConvergenceLastRun — the same one-slot shape
	// takeACLApplySlot (B361) uses, so the budget is one pure decision.
	routeConvergenceMu      sync.Mutex
	routeConvergenceLastRun time.Time
)

// StaleAdvertisementKey is the global_settings key of one relay's B374 record.
func StaleAdvertisementKey(relay string) string {
	return SettingRelayAdvertiseStalePrefix + strings.ToLower(strings.TrimSpace(relay))
}

// StaleAdvertisement is the decoded B374 record (the page renders Reason).
type StaleAdvertisement struct {
	// Relay is the relay name as recorded (lower-cased).
	Relay string
	// At is the unix time the fact was recorded (0 = no record).
	At int64
	// Count is how many advertised prefixes the assignment table no longer gives
	// this relay.
	Count int
	// Reason is the whole stored sentence, so the page can show it verbatim.
	Reason string
}

// ParseStaleAdvertisement decodes "<unix>|<sentence>". An unparseable value is
// treated as "no record" — a storage hiccup must not invent a warning.
func ParseStaleAdvertisement(raw string) StaleAdvertisement {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return StaleAdvertisement{}
	}
	atRaw, reason, _ := strings.Cut(raw, "|")
	at, err := strconv.ParseInt(strings.TrimSpace(atRaw), 10, 64)
	if err != nil {
		return StaleAdvertisement{}
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return StaleAdvertisement{}
	}
	// The count is the integer following "advertises" in the sentence; 0 when the
	// sentence carries no number (a foreign value).
	count := 0
	fields := strings.Fields(reason)
	for i, f := range fields {
		if f == "advertises" && i+1 < len(fields) {
			count, _ = strconv.Atoi(fields[i+1])
			break
		}
	}
	return StaleAdvertisement{At: at, Count: count, Reason: reason}
}

// recordStaleAdvertisement writes the B374 fact for one relay and returns the value
// it stored (empty when it stored nothing). Best effort: a storage failure only
// costs the operator's visibility for this tick and must never fail the pass.
func recordStaleAdvertisement(d *sql.DB, relay string, count int, detail string) string {
	relay = strings.TrimSpace(relay)
	if d == nil || relay == "" || count <= 0 {
		return ""
	}
	reason := fmt.Sprintf("%s still advertises %d prefix(es) that prefix_owner no longer assigns to it: %s",
		strings.ToLower(relay), count, detail)
	if len(reason) > 400 {
		reason = reason[:400]
	}
	value := fmt.Sprintf("%d|%s", time.Now().Unix(), reason)
	if err := db.SetGlobalSetting(d, StaleAdvertisementKey(relay), value); err != nil {
		log.Printf("%s %s: cannot record the stale advertisement: %v", routeConvergenceLogPrefix, relay, err)
	}
	return value
}

// clearStaleAdvertisement removes one relay's B374 record — the case it describes no
// longer holds, and a warning that outlives its reason is the L-60 class of bug.
func clearStaleAdvertisement(d *sql.DB, relay string) {
	if d == nil || strings.TrimSpace(relay) == "" {
		return
	}
	if err := db.DeleteGlobalSetting(d, StaleAdvertisementKey(relay)); err != nil {
		log.Printf("%s %s: cannot clear the stale-advertisement record: %v", routeConvergenceLogPrefix, relay, err)
	}
}

// ListStaleAdvertisements returns every recorded B374 fact, keyed by relay name.
// Exported so /admin/exit-nodes renders it next to the B309 transport banner — the
// page already surfaces "a relay could not be configured"; this is the half that
// says the failed relay is still SERVING the prefixes the table took away.
func ListStaleAdvertisements(d *sql.DB) map[string]StaleAdvertisement {
	out := map[string]StaleAdvertisement{}
	if d == nil {
		return out
	}
	rows, err := d.Query(`SELECT key, COALESCE(value, '') FROM global_settings WHERE key LIKE $1`,
		SettingRelayAdvertiseStalePrefix+"%")
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var key, val string
		if rows.Scan(&key, &val) != nil {
			continue
		}
		relay := strings.TrimPrefix(key, SettingRelayAdvertiseStalePrefix)
		st := ParseStaleAdvertisement(val)
		if st.At == 0 {
			continue
		}
		st.Relay = relay
		out[relay] = st
	}
	return out
}

// routeSet is a de-duplicated, trimmed route set with no implied members. The
// exit-node bases are added by `requiredRouteSet` (the OWNED side) rather than by the
// shared normaliser, because the advertised side must be able to be MISSING a base —
// that is a real, applyable difference (the live emilia advertised both bases but
// still 197 prefixes short of what it owned, and the mirror case is a relay that lost
// its exit-node role).
func routeSet(routes []string) map[string]bool {
	out := map[string]bool{}
	for _, r := range routes {
		if r = strings.TrimSpace(r); r != "" {
			out[r] = true
		}
	}
	return out
}

// requiredRouteSet is the canonical OWNED set: whatever `prefix_owner` assigns, plus
// the two exit-node base routes, which every relay must advertise or it stops being an
// exit node. It is the single definition of "what this relay must advertise".
func requiredRouteSet(routes []string) map[string]bool {
	out := routeSet(routes)
	out["0.0.0.0/0"] = true
	out["::/0"] = true
	return out
}

// RoutesNeedConvergence is the PURE predicate that decides whether one relay's
// advertisement must be rewritten. It opens no transport, reads no database and
// touches no global state, which is what makes "a converged host never opens ssh" a
// property of one function instead of a promise about a call graph.
//
// Semantics:
//   - both lists are normalised to SETS: ordering and duplicates are ignored, so two
//     passes that produce the same routes in a different order do not apply;
//   - both sides always carry 0.0.0.0/0 and ::/0 (`NormalizeRouteSet`): they are the
//     exit-node base routes and `tailscale set --advertise-routes=` REPLACES the whole
//     list, so every relay must advertise them — and the apply path injects them
//     unconditionally (`syncOneExitNode`), which means the comparison and the command
//     it guards cannot describe different lists;
//   - an EMPTY advertisement is treated as the base pair on the wire, which is what
//     `tailscale status` reports for a node that never ran `--advertise-routes=` (a
//     freshly registered relay). This is what keeps an idle relay from being applied
//     once per tick forever (the B374 SSH-storm requirement);
//   - the verdict is "the symmetric difference is non-empty". Equal cardinality alone
//     is not enough: swapping two prefixes of the same size would be invisible, and the
//     live case was a size difference (emilia advertised 2 while it owned 199).
//
// The second return value is a short operator sentence naming what differs; it is
// empty exactly when the first value is false.
func RoutesNeedConvergence(owned, advertised []string) (bool, string) {
	advertisedRoutes := advertised
	if len(routeSet(advertised)) == 0 {
		advertisedRoutes = []string{"0.0.0.0/0", "::/0"}
	}
	ownedSet := NormalizeRouteSet(owned)
	advertisedSet := NormalizeRouteSet(advertisedRoutes)
	if len(ownedSet) == len(advertisedSet) {
		same := true
		for r := range ownedSet {
			if !advertisedSet[r] {
				same = false
				break
			}
		}
		if same {
			return false, ""
		}
	}
	var missing, extra []string
	for r := range ownedSet {
		if !advertisedSet[r] {
			missing = append(missing, r)
		}
	}
	for r := range advertisedSet {
		if !ownedSet[r] {
			extra = append(extra, r)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	parts := []string{fmt.Sprintf("owned=%d advertised=%d", len(ownedSet), len(advertisedSet))}
	if len(missing) > 0 {
		parts = append(parts, "missing="+joinRoutesCapped(missing))
	}
	if len(extra) > 0 {
		parts = append(parts, "extra="+joinRoutesCapped(extra))
	}
	return true, strings.Join(parts, " ")
}

// NormalizeRouteSet is the canonical route set this package compares and applies:
// the caller's routes, trimmed and de-duplicated, plus the two always-required
// exit-node base routes (0.0.0.0/0, ::/0). This is what makes "the routes a relay
// MUST advertise" a single definition instead of a convention at each call site.
// Exported because it is the contract the apply path and the tests share.
func NormalizeRouteSet(routes []string) map[string]bool {
	out := map[string]bool{"0.0.0.0/0": true, "::/0": true}
	for _, r := range routes {
		if r = strings.TrimSpace(r); r != "" {
			out[r] = true
		}
	}
	return out
}

// normalizeRouteList is NormalizeRouteSet as a sorted slice — the shape the apply path
// takes.
func normalizeRouteList(routes []string) []string {
	set := requiredRouteSet(routes)
	out := make([]string, 0, len(set))
	for r := range set {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// joinRoutesCapped renders a route list for a log line: at most six entries, then
// "… (+N more)". The journal must stay readable when a relay owns 200 prefixes.
func joinRoutesCapped(routes []string) string {
	const cap = 6
	if len(routes) <= cap {
		return strings.Join(routes, ",")
	}
	return fmt.Sprintf("%s,… (+%d more)", strings.Join(routes[:cap], ","), len(routes)-cap)
}

// advertisedOutsideOwned returns the advertised prefixes that are NOT in the owned
// set, sorted. The base routes are always in the owned set after normalisation, so
// they can never be reported as stale.
func advertisedOutsideOwned(owned, advertised []string) []string {
	ownedSet := NormalizeRouteSet(owned)
	var out []string
	for r := range routeSet(advertised) {
		if !ownedSet[r] {
			out = append(out, r)
		}
	}
	sort.Strings(out)
	return out
}

// OwnedRoutesForRelay is the complete route list a relay must advertise: the
// prefixes `prefix_owner` assigns it, plus the always-present exit-node bases.
// Exported so tests (and any future page) read the same contract the convergence
// pass applies.
func (s *Service) OwnedRoutesForRelay(node string) []string {
	return normalizeRouteList(s.ownedPrefixesForNodeFromTable(node))
}

// routeConvergenceHealth is the observable outcome of one convergence pass. It is
// returned by the internal pass (rather than only logged) so a test — and any future
// page — can assert what the pass decided without parsing the journal. The EXPORTED
// entry point returns the two counts the maintenance tick logs, because that is all
// its caller uses; the richer value stays inside the package so no second caller has
// to be taught about it.
type routeConvergenceHealth struct {
	// Considered is how many relays the pass examined.
	Considered int
	// Applied lists the relays whose advertisement genuinely differed and were
	// handed to the apply path.
	Applied []string
	// Converged lists the relays whose advertisement already matched. For every one
	// of them the pass created NO ssh process and wrote nothing.
	Converged []string
	// Skipped names the relays whose difference could not be acted on, with the
	// reason ("not in headscale", "excluded: <why>", "apply failed: <err>").
	// Reported, never silently dropped.
	Skipped []string
	// Stale lists the relays recorded as still advertising prefixes the assignment
	// table no longer gives them (the B374 part-3 fact).
	Stale []string
	// ReadErr is why the advertised plane could not be read at all (empty when it
	// was read). A pass with a read error applies nothing: "we could not ask
	// headscale" is not evidence that every relay needs rewriting.
	ReadErr string
	// Throttled is true when the pass ran inside its interval and therefore only
	// observed.
	Throttled bool
}

// ConvergeAdvertisedRoutes is the B374 periodic route-convergence pass: it returns
// how many relays it APPLIED and how many it SKIPPED (the two numbers the maintenance
// tick logs). It never returns an error — a convergence failure must not stop the
// tick that also heals preferences, and every failure is already logged with its
// reason.
//
// It computes, for every relay in the healthy set or with a recorded route
// application, the owned set from `prefix_owner` (plus the exit-node bases) and
// compares it with what the relay ACTUALLY advertises right now, as reported by the
// headscale node view the rest of the code already reads
// (`s.HS.ListAllNodes()` → NodeView.AvailableRoutes — the same source
// SyncAdvertisedRoutes / syncOneExitNode / the /admin/exit-nodes АНОНС column use).
// Only relays whose set genuinely differs are applied, and through the shared apply
// path, so the manual Re-sync, StaggeredSync and this pass cannot drift.
//
// It is idempotent (a converged host writes and applies nothing), throttled
// (routeConvergenceInterval) and never fatal.
//
// `reason` is the caller's own description ("maintenance tick"), rendered in the log
// line so the journal says who asked.
func (s *Service) ConvergeAdvertisedRoutes(reason string) (applied int, skipped int) {
	h := s.convergeAdvertisedRoutes(reason)
	return len(h.Applied), len(h.Skipped)
}

// convergeAdvertisedRoutes is the pass body, returning the full outcome for the
// package's own tests.
func (s *Service) convergeAdvertisedRoutes(reason string) routeConvergenceHealth {
	var health routeConvergenceHealth
	if s == nil || s.dbc() == nil {
		return health
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "periodic"
	}
	if !takeRouteConvergenceSlot(time.Now(), routeConvergenceInterval) {
		log.Printf("%s pass skipped by its own budget (%s) — the last pass ran %s ago; a difference found by the next maintenance tick is acted on then",
			routeConvergenceLogPrefix, routeConvergenceInterval, time.Since(routeConvergenceSlotLastRun()).Round(time.Second))
		health.Throttled = true
		return health
	}

	// The advertised plane comes from ONE node read. Its failure mode matters: an
	// empty map would make every relay look like it advertises nothing, and acting
	// on that would rewrite every relay on every tick (the SSH storm this pass
	// exists to avoid). So a read failure ABORTS the pass and says so.
	if s.HS == nil {
		health.ReadErr = "no headscale client is wired"
		log.Printf("%s cannot compare the advertised routes with the assignment table: %s — nothing is applied this tick", routeConvergenceLogPrefix, health.ReadErr)
		return health
	}
	nodes, err := s.HS.ListAllNodes()
	if err != nil {
		health.ReadErr = err.Error()
		log.Printf("%s cannot read the advertised routes from headscale (%v) — this tick applies nothing (an unreadable plane is not evidence that every relay needs rewriting)", routeConvergenceLogPrefix, err)
		return health
	}
	advertised := map[string][]string{}
	present := map[string]bool{}
	for _, n := range nodes {
		for _, name := range []string{n.Hostname, n.GivenName} {
			if name = strings.ToLower(strings.TrimSpace(name)); name != "" {
				advertised[name] = n.AvailableRoutes
				present[name] = true
			}
		}
	}

	// B309/B352 exclusion set: a relay whose last apply failed inside the window, or
	// which is unproven while a proven relay exists. The same decision the prefix
	// assignment makes, from the same records.
	excluded := excludedFromAssignment(s.dbc())

	relays := s.routeConvergenceRelays()
	var defaultKeyPath string
	if s.Cfg != nil {
		defaultKeyPath = s.Cfg.SSHKeyPath
	}

	for _, relay := range relays {
		health.Considered++
		owned := s.OwnedRoutesForRelay(relay)
		if !present[strings.ToLower(relay)] {
			// headscale has no node by this name: we cannot know what it
			// advertises, and guessing would open a transport on every tick.
			health.Skipped = append(health.Skipped, relay+": not in headscale")
			continue
		}
		got := advertised[strings.ToLower(relay)]
		need, detail := RoutesNeedConvergence(owned, got)
		if !need {
			// CONVERGED: no ssh, no SQL write, no further API call. This is the
			// line a healthy host prints, and the whole reason the pass is
			// affordable every five minutes.
			health.Converged = append(health.Converged, relay)
			clearStaleAdvertisement(s.dbc(), relay)
			continue
		}
		if why, isExcluded := excluded[strings.ToLower(relay)]; isExcluded {
			// The B374 part-3 case: the assignment table no longer gives this relay
			// these prefixes, and the portal cannot reach it to prune them. Do NOT
			// attempt the apply — B309 already proved the transport fails, and
			// retrying every tick is the SSH storm this pass must not become.
			// Record the fact instead, name it in the journal, and let the page show
			// it.
			health.Skipped = append(health.Skipped, relay+": excluded from the healthy set ("+why+")")
			health.Stale = append(health.Stale, relay)
			extra := advertisedOutsideOwned(owned, got)
			if len(extra) == 0 {
				extra = normalizeRouteList(got) // no owned-set delta to name: report the whole advertised set
			}
			recordStaleAdvertisement(s.dbc(), relay, len(extra),
				fmt.Sprintf("%s (excluded from the healthy set: %s; skygate cannot prune the advertisement until the relay answers — after the next successful apply this clears itself)",
					joinRoutesCapped(extra), why))
			log.Printf("%s %s DECLARED STALE — it still advertises %d prefix(es) that prefix_owner no longer assigns to it (%s), and it is excluded from the healthy set: %s. Recorded as %s; the page shows it next to the transport banner",
				routeConvergenceLogPrefix, relay, len(extra), detail, why, StaleAdvertisementKey(relay))
			continue
		}
		log.Printf("%s %s differs from its owned set (%s) — applying through the shared path (no rule or operator action was needed for this)", routeConvergenceLogPrefix, relay, detail)
		if applyErr := s.applyRoutesForConvergence(relay, owned, defaultKeyPath); applyErr != nil {
			log.Printf("%s %s: %v — the next tick retries", routeConvergenceLogPrefix, relay, applyErr)
			health.Skipped = append(health.Skipped, relay+": apply failed: "+applyErr.Error())
			continue
		}
		health.Applied = append(health.Applied, relay)
	}
	sort.Strings(health.Applied)
	sort.Strings(health.Converged)
	sort.Strings(health.Skipped)
	sort.Strings(health.Stale)
	// B374 part 2: a relay whose advertisement had to be rewritten is the DATA-plane
	// half of "the planes disagree". The ACL's per-CIDR `via=` pins are the control
	// half, and the churn budget (30m) is the WRONG budget for that: this is an
	// ownership move, not a rotating /32. Ask the drift check through the
	// operator-class budget, with a detail line that names the real trigger — and let
	// its own five-minute guard and the shared apply slot absorb a burst.
	if len(health.Applied) > 0 {
		log.Printf("%s %d relay(s) had to be rewritten — asking the ACL drift check with the OPERATOR budget, because the advertised and owned planes disagree (this is an ownership move, not derived-rule churn)",
			routeConvergenceLogPrefix, len(health.Applied))
		s.periodicDriftCheckAfterOwnershipMove()
	}
	log.Printf("%s pass (%s): considered=%d applied=%d converged=%d skipped=%d stale=%d%s",
		routeConvergenceLogPrefix, reason, health.Considered, len(health.Applied), len(health.Converged), len(health.Skipped), len(health.Stale), convergeNotes(health))
	return health
}

// convergeNotes renders the non-empty outcome lists so ONE grep of
// `route-converge:` answers "what did it do?" without a second log line.
func convergeNotes(h routeConvergenceHealth) string {
	var parts []string
	if len(h.Applied) > 0 {
		parts = append(parts, "applied="+strings.Join(h.Applied, ","))
	}
	if len(h.Stale) > 0 {
		parts = append(parts, "stale="+strings.Join(h.Stale, ","))
	}
	if len(h.Skipped) > 0 {
		parts = append(parts, "skipped=["+strings.Join(h.Skipped, "; ")+"]")
	}
	if len(parts) == 0 {
		return ""
	}
	return " " + strings.Join(parts, " ")
}

// routeConvergenceRelays lists the relays the pass must examine: every relay the
// assignment table names, plus every relay skygate has a recorded apply for. It is
// the same union B352's keep-synced list uses, computed without a caller-supplied
// `already` set (this pass is the periodic one and has no rule-derived list).
func (s *Service) routeConvergenceRelays() []string {
	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, name)
	}
	for _, name := range s.relaysToKeepSynced(map[string]bool{}) {
		add(name)
	}
	if rows, err := s.dbc().Query(`SELECT DISTINCT exit_node_id FROM prefix_owner`); err == nil {
		for rows.Next() {
			var name string
			if rows.Scan(&name) == nil {
				add(name)
			}
		}
		rows.Close()
	}
	sort.Strings(out)
	return out
}

// takeRouteConvergenceSlot is the B374 budget: a pure decision on
// (lastRun, now, throttle) plus the atomic claim of the package slot, so a test can
// drive it without a clock. Same shape as takeACLApplySlot (B361) — one mechanism,
// one explanation.
func takeRouteConvergenceSlot(now time.Time, throttle time.Duration) bool {
	routeConvergenceMu.Lock()
	defer routeConvergenceMu.Unlock()
	if !routeConvergenceLastRun.IsZero() && throttle > 0 && now.Sub(routeConvergenceLastRun) < throttle {
		return false
	}
	routeConvergenceLastRun = now
	return true
}

// routeConvergenceSlotLastRun reads the slot for the DEFERRAL LOG ONLY (the same
// pattern sync_acl.go's aclApplySlotLastRun uses: the decision lives in one
// function, the observation of it does not become a second throttle).
func routeConvergenceSlotLastRun() time.Time {
	routeConvergenceMu.Lock()
	defer routeConvergenceMu.Unlock()
	return routeConvergenceLastRun
}

// resetRouteConvergenceSlot clears the budget (tests only).
func resetRouteConvergenceSlot() {
	routeConvergenceMu.Lock()
	routeConvergenceLastRun = time.Time{}
	routeConvergenceMu.Unlock()
}

// excludedFromAssignment returns relay → reason for every relay that answers
// headscale but is NOT a usable owner (B309/B352). It is the same computation
// healthyExitRelaysForAssignment makes, exposed as a set so the convergence pass can
// name the reason instead of re-deriving it, and it reuses that function verbatim so
// the two can never disagree about who is excluded.
func excludedFromAssignment(d *sql.DB) map[string]string {
	out := map[string]string{}
	base := healthyExitRelays(d)
	if len(base) == 0 {
		return out
	}
	assignment := healthyExitRelaysForAssignment(d)
	kept := map[string]bool{}
	for _, relay := range assignment {
		kept[strings.ToLower(strings.TrimSpace(relay))] = true
	}
	now := time.Now().Unix()
	for _, relay := range base {
		name := strings.ToLower(strings.TrimSpace(relay))
		if name == "" || kept[name] {
			continue
		}
		st := RelayApplyStateOf(d, relay)
		switch {
		case st.Failed(now, RelayApplyFailureWindow):
			out[name] = fmt.Sprintf("its last route application failed %s ago: %s",
				time.Since(time.Unix(st.At, 0)).Truncate(time.Second), st.Detail)
		case st.At > 0:
			out[name] = "its last recorded route application FAILED and the exclusion window has passed, so it is unproven until the next successful apply"
		default:
			out[name] = "skygate has never applied routes to it"
		}
	}
	return out
}

// applyRoutesForConvergence hands one relay's owned set to the SAME apply path the
// manual Re-sync and StaggeredSync use. It exists so the convergence pass cannot grow
// its own transport logic — the B300 lesson: two copies of that decision already
// drifted once, and the shorter copy silently skipped the local-transport and
// SSH-target repairs.
func (s *Service) applyRoutesForConvergence(relay string, owned []string, defaultKeyPath string) error {
	// syncOneExitNodeFn writes into the result map and records the relay's apply
	// state (B309) — both are wanted here: the record is what keeps the relay in the
	// next pass's relay list, and the label is what a test asserts.
	result := map[string]string{}
	syncOneExitNodeFn(s.HS, s.dbc(), s.lookupAcceptRoutes, defaultKeyPath, relay, owned, result)
	label := strings.TrimSpace(result[relay])
	if label == "" {
		return fmt.Errorf("the apply path produced no result for %s", relay)
	}
	if strings.Contains(label, "=err=") {
		return fmt.Errorf("%s", label)
	}
	return nil
}
