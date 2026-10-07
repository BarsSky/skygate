// reservation_b360.go — B360 (2026-10-07): a failover must be a RESERVATION,
// and a recovered exit node must get its prefixes back.
//
// THE LIVE CASE THE DESIGN ANSWERS
// --------------------------------
// Measured on the reference deployment:
//
//	2026-10-06 19:49:23Z  the operator sets the panel override
//	                      `global_settings.prefix_owner_force_relay = emilia`
//	                      (audit `prefix_owner_force`, username skyadmin)
//	during the outage     `prefix_owner` = 139 rows, all emilia, all source='global'
//	                      while LoadClaims still saw 194 ip/subnet claims naming
//	                      karolina — the data plane disagreed with the rules for
//	                      ~19 hours and nothing said so
//	karolina returns      `exit_node_health` = all three relays online, healthy=1,
//	                      routes approved … and NOTHING returned, because the move
//	                      had never been recorded as a failover and the override is
//	                      a latch with no expiry and no surface
//	operator clears it    the very next pass returned all 139 prefixes to karolina
//	                      (source='explicit', changed=139) — the engine CAN return;
//	                      it simply had no memory and no obligation to re-ask
//
// B360 therefore does four things:
//
//  1. RECORD the reservation when a prefix is taken from an unhealthy relay
//     (`prefix_owner.failover_from/at/flaps`, V078) and CLEAR it when the prefix
//     moves for a genuine reason — a reservation must not outlive the reason that
//     justified it (docs/LESSONS.md L-60).
//  2. RETURN it when the reserved relay is healthy, PROVEN (skygate has a recorded
//     successful route application for it), still CLAIMS the prefix, and has been
//     continuously healthy for at least `hysteresis`.
//  3. QUARANTINE a flapping relay: every quick flap doubles the window it must
//     stay healthy for, capped, and the streak persists in the table.
//  4. Make the operator override VISIBLE — its age and the assignment that would
//     be in force without it — and warn once per window while it overrides relays
//     that are healthy again. It is NEVER auto-cleared: a human's deliberate
//     choice is not the engine's to undo.
//
// The planner (`PlanReturns`) is a pure function: claims, assignments, the stored
// rows, the healthy/proven/"healthy since" sets and the config go in, a decision
// with a NAMED reason per prefix comes out. No clock, no database, no logging — so
// `reservation_b360_test.go` can drive it as a scripted health timeline.
package prefixowner

import (
	"database/sql"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"skygate/internal/db"
	"skygate/internal/monitoring"
)

// Default hysteresis/quarantine knobs. Both are configurable through
// ReservationConfig so a test (and a future panel setting) can shorten them.
const (
	// DefaultHysteresis is how long a recovered relay must have been continuously
	// healthy before the prefix returns. Ten minutes is two sync ticks: long
	// enough that a relay which is merely flapping cannot yank its prefixes back,
	// short enough that a real recovery restores the operator's routing within one
	// maintenance pass.
	DefaultHysteresis = 10 * time.Minute
	// DefaultQuarantine is the window that classifies the NEXT failover as a quick
	// flap: returned to R and taken away from R again within this window doubles
	// the hysteresis for that prefix.
	DefaultQuarantine = 30 * time.Minute
	// DefaultMaxHysteresis caps the doubling. Without a cap a prefix that flaps
	// five times would wait five hours and effectively never come back — an
	// unreachable rule is the same failure as no rule at all.
	DefaultMaxHysteresis = 6 * time.Hour
)

// Named reasons. Every reservation that is NOT handed back is reported with one
// of these, and every one of them is asserted by the tests and by
// scripts/check_b360_prefix_reservation.sh. L-54's rule: the difference between
// "nothing to do" and "I cannot decide" must never be silent.
const (
	// ReasonNotHealthy — the reserved relay is not in the healthy set (offline or
	// degraded: B273's `online`/`untagged` is the only usable boundary, so a relay
	// whose 0.0.0.0/0 route is not approved never takes a prefix back).
	ReasonNotHealthy = "return-not-healthy"
	// ReasonNotProven — no recorded SUCCESSFUL route application
	// (`relay_apply_state:<relay>` = "<unix>|ok", B309). A relay skygate has never
	// configured is `online` by definition; handing prefixes to it is how
	// sharlotta owned 95 prefixes while advertising 2 (B352).
	ReasonNotProven = "return-not-proven"
	// ReasonNoLongerClaimed — the reserved relay no longer claims the prefix (its
	// enabled ip/subnet rule count for it is 0). The reservation is stale and is
	// DROPPED, not returned.
	ReasonNoLongerClaimed = "return-no-longer-claimed"
	// ReasonTooSoon — healthy and proven, but for less than the hysteresis window.
	ReasonTooSoon = "return-too-soon"
	// ReasonQuarantined — as ReasonTooSoon, but this prefix has already flapped at
	// least once, so the required window is hysteresis * 2^flaps (capped).
	ReasonQuarantined = "return-quarantined"
	// ReasonOverridden — the operator's global override
	// (`global_settings.prefix_owner_force_relay`) is in force and healthy. The
	// reservation is kept so that clearing the override returns the prefix by the
	// normal rules; nothing is returned while the human's choice is active.
	ReasonOverridden = "return-overridden"
	// ReasonManualPin — the row carries a stale reservation from before the
	// operator pinned the prefix by hand. A manual pin is never reserved and never
	// returned; the stale columns are dropped, and this name makes the drop
	// visible instead of leaving a reservation nobody will ever act on.
	ReasonManualPin = "return-manual-pin"
)

// ReservationConfig carries the two hysteresis knobs. The zero value is not
// useful on purpose: `DefaultReservationConfig()` names the defaults, and
// `normalize()` fills any unset field so a half-filled config cannot silently
// disable the protection.
type ReservationConfig struct {
	Hysteresis    time.Duration
	Quarantine    time.Duration
	MaxHysteresis time.Duration
}

// DefaultReservationConfig is the shipped configuration.
func DefaultReservationConfig() ReservationConfig {
	return ReservationConfig{
		Hysteresis:    DefaultHysteresis,
		Quarantine:    DefaultQuarantine,
		MaxHysteresis: DefaultMaxHysteresis,
	}
}

// normalize replaces a zero/negative field with its default. A config that
// disables the hysteresis by accident is the class of bug this whole file exists
// to prevent, so an unset value can never mean "return immediately".
func (c ReservationConfig) normalize() ReservationConfig {
	d := DefaultReservationConfig()
	if c.Hysteresis <= 0 {
		c.Hysteresis = d.Hysteresis
	}
	if c.Quarantine <= 0 {
		c.Quarantine = d.Quarantine
	}
	if c.MaxHysteresis <= 0 {
		c.MaxHysteresis = d.MaxHysteresis
	}
	if c.MaxHysteresis < c.Hysteresis {
		c.MaxHysteresis = c.Hysteresis
	}
	return c
}

// Return is one prefix's reservation decision.
//
// An entry in `returns` (Reason == "") means: hand the prefix back to `To` and
// clear the reservation. An entry in `kept` means: withhold the return, report
// `Reason`, and (unless Drop) keep `Pin` serving the prefix.
type Return struct {
	Prefix string
	// From is the relay currently serving the prefix (the substitute).
	From string
	// To is `failover_from` — the relay the reservation owes the prefix to.
	To string
	// Pin is the owner to keep while the return is withheld. Empty means "leave
	// the assignment's own decision" (which is what the global override wants).
	Pin string
	// PinSource is the source column to record with Pin.
	PinSource string
	// Source is the source column to record when the return IS performed.
	Source string
	// Reason is a named skip reason for a kept reservation ("" = allowed).
	Reason string
	// Drop marks a stale reservation that must be cleared rather than returned.
	Drop bool
	// Flaps is the persisted consecutive-quick-flap streak of this prefix.
	Flaps int
	// FailoverAt is when the reservation was recorded (unix seconds).
	FailoverAt int64
	// Required is the hysteresis window this return has to satisfy.
	Required time.Duration
	// Waited is how long the reserved relay has been continuously healthy.
	Waited time.Duration
}

// requiredHysteresis applies the anti-flap doubling: hysteresis * 2^flaps,
// capped at MaxHysteresis.
func requiredHysteresis(flaps int, cfg ReservationConfig) time.Duration {
	cfg = cfg.normalize()
	req := cfg.Hysteresis
	for i := 0; i < flaps && req < cfg.MaxHysteresis; i++ {
		req *= 2
	}
	if req > cfg.MaxHysteresis {
		req = cfg.MaxHysteresis
	}
	return req
}

// nextFlapStreak computes the streak to store on a NEW reservation for a prefix
// whose last event was stored in `prev`.
//
// The streak counts consecutive QUICK flaps: the prefix was returned to a relay
// (failover_from == "", failover_at = the return stamp) and failed over away from
// it again before `quarantine` elapsed. A failover that happens at least one
// quarantine window after the return means the relay held the prefix long enough
// to call the episode over, and the streak resets.
//
// An ACTIVE reservation (failover_from != "") is not a return cycle: a second
// unhealthy relay cascading the prefix elsewhere keeps the original reservation
// and its streak untouched.
func nextFlapStreak(prev Existing, now int64, quarantine time.Duration) int {
	if prev.FailoverFrom != "" {
		return prev.FailoverFlaps
	}
	if prev.FailoverAt > 0 && now >= prev.FailoverAt &&
		now-prev.FailoverAt < int64(quarantine.Seconds()) {
		return prev.FailoverFlaps + 1
	}
	return 0
}

// reserveOnMove records, carries over or clears the reservation of one decided
// prefix. It is the only place the assignment engine writes the three reservation
// columns, and every branch is deliberate:
//
//  1. nothing moved → carry the reservation over untouched;
//  2. the decision hands the prefix to the relay it was TAKEN FROM → that is the
//     return path, and whether it may happen yet is PlanReturns' decision, so the
//     reservation MUST survive this function. Clearing it here would bypass the
//     hysteresis for exactly the prefixes that come back on their own (the rules'
//     explicit majority);
//  3. the decision takes the prefix from an UNHEALTHY previous owner → RECORD it
//     (or keep the original reservation when one is already active and only the
//     substitute changed, e.g. a cascade);
//  4. the decision takes it from a HEALTHY previous owner → a GENUINE decision
//     change (the majority moved, a rebalance, a manual pin appeared): CLEAR the
//     reservation. One that outlives its reason is the L-60 bug class — the next
//     operator would read "this prefix belongs to karolina" about a prefix
//     karolina has no claim on.
//
// A manual pin is never reserved and never returned: the operator's explicit
// choice about ONE prefix is not a failover, and SetManual clears any stale
// reservation for the same reason.
func reserveOnMove(a *Assignment, prev Existing, healthySet map[string]bool, now int64, cfg ReservationConfig) {
	a.FailoverFrom, a.FailoverAt, a.FailoverFlaps = "", 0, 0
	if prev.Prefix == "" || prev.Source == "manual" {
		return
	}
	if a.ExitNode == prev.ExitNode {
		if prev.FailoverFrom != "" && strings.EqualFold(prev.ExitNode, prev.FailoverFrom) {
			// The prefix is ALREADY on the relay the reservation owes it to — the
			// substitute disappeared while the reservation was pending, so the
			// natural decision is the reserved relay itself and there is nothing to
			// withhold. Keep only the TIMING of the return: clearing the origin and
			// stamping `failover_at` is what lets the next failover be recognised as
			// a quick flap (without this, a reservation would sit "active" on a
			// prefix that had already gone home, and the streak would never grow).
			a.FailoverFrom = ""
			a.FailoverAt = now
			a.FailoverFlaps = prev.FailoverFlaps
			return
		}
		a.FailoverFrom, a.FailoverAt, a.FailoverFlaps = prev.FailoverFrom, prev.FailoverAt, prev.FailoverFlaps
		return
	}
	if prev.FailoverFrom != "" && strings.EqualFold(a.ExitNode, prev.FailoverFrom) {
		// The natural pass is returning the prefix to the relay it was taken
		// from. PlanReturns decides whether that is allowed yet.
		a.FailoverFrom, a.FailoverAt, a.FailoverFlaps = prev.FailoverFrom, prev.FailoverAt, prev.FailoverFlaps
		return
	}
	if prev.ExitNode != "" && !healthySet[strings.ToLower(prev.ExitNode)] {
		if prev.FailoverFrom != "" {
			// A reservation is already active; the reserved relay is still the
			// one we owe the prefix to and only the substitute changed.
			a.FailoverFrom, a.FailoverAt, a.FailoverFlaps = prev.FailoverFrom, prev.FailoverAt, prev.FailoverFlaps
			return
		}
		a.FailoverFrom = prev.ExitNode
		a.FailoverAt = now
		a.FailoverFlaps = nextFlapStreak(prev, now, cfg.normalize().Quarantine)
		return
	}
	// previous owner healthy, prefix moved anyway → genuine decision change.
}

// PlanReturns is the pure B360 decision: for every prefix carrying a reservation,
// may it go back to the relay it was taken from?
//
// Inputs, all owned by the caller so this function stays testable:
//
//	claims        — the enabled ip/subnet rules (what still claims what);
//	assignments   — the natural decision, WITH the reservation carried by
//	                AssignWithReservations (an entry whose FailoverFrom is empty
//	                had its reservation resolved by the assignment pass and is not
//	                a candidate);
//	existing      — the stored rows (the reservation's origin and streak);
//	healthy       — the relays that may carry traffic (B273 online/untagged, plus
//	                B309's apply-failure exclusion as applied by the caller);
//	proven        — relays with a recorded SUCCESSFUL route application (B309);
//	healthySince  — relay → unix second it became continuously healthy;
//	force         — the operator's global override ("" = none);
//	forceHealthy  — whether that override is usable right now.
//
// A relay missing from `healthySince` has NO evidence of sustained health and is
// therefore never returned to — withholding a return is recoverable, a flap is
// not. That is also the contract scripts/check_b360_prefix_reservation.sh pins.
//
// A source='manual' row is never a return candidate: if it somehow carries a
// reservation (one written before the pin), that reservation is DROPPED with the
// named reason ReasonManualPin — reported, never acted on.
func PlanReturns(
	claims []Claim,
	assignments []Assignment,
	existing []Existing,
	healthy []string,
	proven map[string]bool,
	healthySince map[string]int64,
	force string,
	forceHealthy bool,
	now int64,
	cfg ReservationConfig,
) (returns []Return, kept []Return) {
	cfg = cfg.normalize()
	healthySet := map[string]bool{}
	for _, h := range healthy {
		if h != "" {
			healthySet[strings.ToLower(h)] = true
		}
	}
	byPrefix := make(map[string]Assignment, len(assignments))
	for _, a := range assignments {
		byPrefix[a.Prefix] = a
	}
	// Who still claims which prefix, and how many devices say so. A reservation
	// for a prefix the reserved relay no longer claims is stale (B341: the rules
	// are the source of intent).
	claimants := map[string]map[string]int{}
	for _, c := range claims {
		if c.Prefix == "" || c.ExitNode == "" {
			continue
		}
		m := claimants[c.Prefix]
		if m == nil {
			m = map[string]int{}
			claimants[c.Prefix] = m
		}
		m[strings.ToLower(c.ExitNode)]++
	}

	ordered := make([]Existing, 0, len(existing))
	for _, e := range existing {
		if e.Prefix != "" && e.FailoverFrom != "" {
			ordered = append(ordered, e)
		}
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Prefix < ordered[j].Prefix })

	for _, e := range ordered {
		r := Return{
			Prefix:     e.Prefix,
			From:       e.ExitNode,
			To:         e.FailoverFrom,
			Flaps:      e.FailoverFlaps,
			FailoverAt: e.FailoverAt,
			Required:   requiredHysteresis(e.FailoverFlaps, cfg),
		}
		if since, ok := healthySince[strings.ToLower(e.FailoverFrom)]; ok && since > 0 && now >= since {
			r.Waited = time.Duration(now-since) * time.Second
		}
		if e.Source == "manual" {
			// A manual pin is never reserved and never returned (§1). A
			// reservation left on such a row is stale by definition: report and
			// drop it, never act on it.
			r.Reason = ReasonManualPin
			r.Drop = true
			kept = append(kept, r)
			continue
		}

		a, hasAssignment := byPrefix[e.Prefix]
		switch {
		case !hasAssignment:
			// No enabled rule claims the prefix any more. The row is stale; Prune
			// will remove it, and this report is the named reason that makes the
			// drop visible instead of silent.
			r.Reason = ReasonNoLongerClaimed
			r.Drop = true
			kept = append(kept, r)
			continue
		case a.FailoverFrom == "":
			// The assignment pass already resolved this reservation (the previous
			// owner was healthy and the prefix moved for a real reason, or the
			// row's relay is no longer what it was). Nothing to plan.
			continue
		}

		// The pin keeps the prefix where it is while the return is withheld. The
		// stored owner may itself have gone down since (a cascade), in which case
		// the assignment's own choice is the only healthy answer.
		pin, pinSource := e.ExitNode, e.Source
		if !healthySet[strings.ToLower(pin)] {
			pin, pinSource = a.ExitNode, a.Source
		}
		if strings.EqualFold(pin, e.FailoverFrom) {
			// Already back on the reserved relay (the bookkeeping simply was not
			// cleared): report the return so the caller clears it. Do not "pin"
			// against it.
			pin, pinSource = "", ""
		}
		r.Pin, r.PinSource = pin, pinSource
		r.Source = a.Source
		if !strings.EqualFold(a.ExitNode, e.FailoverFrom) {
			// The natural pass did not choose the reserved relay (an auto/sticky
			// prefix whose substitute is still healthy): the return is the
			// engine's own restoration, so it is recorded as an automatic choice.
			r.Source = "auto"
		}

		switch {
		case forceHealthy && force != "":
			r.Reason = ReasonOverridden
			// The override owns the decision; do not pin against it.
			r.Pin, r.PinSource = "", ""
			kept = append(kept, r)
		case !healthySet[strings.ToLower(e.FailoverFrom)]:
			r.Reason = ReasonNotHealthy
			kept = append(kept, r)
		case !proven[strings.ToLower(e.FailoverFrom)]:
			r.Reason = ReasonNotProven
			kept = append(kept, r)
		case claimants[e.Prefix][strings.ToLower(e.FailoverFrom)] == 0:
			r.Reason = ReasonNoLongerClaimed
			r.Drop = true
			kept = append(kept, r)
		case r.Waited < r.Required:
			if r.Flaps > 0 {
				r.Reason = ReasonQuarantined
			} else {
				r.Reason = ReasonTooSoon
			}
			kept = append(kept, r)
		default:
			returns = append(returns, r)
		}
	}
	return returns, kept
}

// ApplyPlan folds the planner's decision into the assignment list. Pure, so the
// caller can assert the resulting table without a database.
//
// A withheld return is written back with the reservation intact (and the pinned
// owner, when the pin does not fight the caller's global override); a dropped
// reservation is cleared; an allowed return clears the reservation and stamps
// `failover_at` with the return time, which is what makes the NEXT failover
// within the quarantine window recognisable as a quick flap.
func ApplyPlan(as []Assignment, returns, kept []Return, now int64) []Assignment {
	byPrefix := make(map[string]Return, len(returns)+len(kept))
	for _, r := range returns {
		byPrefix[r.Prefix] = r
	}
	for _, r := range kept {
		if r.Drop {
			byPrefix[r.Prefix] = r
			continue
		}
		if _, ok := byPrefix[r.Prefix]; !ok {
			byPrefix[r.Prefix] = r
		}
	}
	for i := range as {
		r, ok := byPrefix[as[i].Prefix]
		if !ok {
			continue
		}
		switch {
		case r.Reason == "":
			as[i].ExitNode = r.To
			if r.Source != "" {
				as[i].Source = r.Source
			}
			as[i].FailoverFrom = ""
			as[i].FailoverAt = now
			as[i].FailoverFlaps = r.Flaps
		case r.Drop:
			as[i].FailoverFrom, as[i].FailoverAt, as[i].FailoverFlaps = "", 0, 0
		default:
			if r.Pin != "" {
				as[i].ExitNode = r.Pin
				if r.PinSource != "" {
					as[i].Source = r.PinSource
				}
			}
			as[i].FailoverFrom = r.To
			as[i].FailoverAt = r.FailoverAt
			as[i].FailoverFlaps = r.Flaps
		}
	}
	return as
}

// clearDroppedReservations removes the reservation columns of a DROPPED entry the
// assignment pass does not write to.
//
// ApplyPlan can only clear the columns of a prefix that is in the assignment list,
// but a stale reservation routinely sits on a row that is NOT: a prefix nobody
// claims any more (Prune deletes it on the same pass, and a manual pin is kept by
// Prune even when unclaimed). "Dropped" has to mean the columns are gone from the
// table, otherwise the next reader sees a reservation no pass will ever act on.
// Idempotent: a second run matches no row.
func clearDroppedReservations(d *sql.DB, kept []Return, as []Assignment) int {
	inPlan := make(map[string]bool, len(as))
	for _, a := range as {
		inPlan[a.Prefix] = true
	}
	cleared := 0
	for _, r := range kept {
		if !r.Drop || inPlan[r.Prefix] {
			continue
		}
		res, err := d.Exec(`UPDATE prefix_owner SET failover_from = '', failover_at = 0, failover_flaps = 0
		                    WHERE prefix = $1 AND failover_from <> ''`, r.Prefix)
		if err != nil {
			log.Printf("prefix-owner: could not drop the stale reservation of %s: %v", r.Prefix, err)
			continue
		}
		if n, _ := res.RowsAffected(); n > 0 {
			cleared += int(n)
		}
	}
	return cleared
}

// DescribePlan renders one line per decision — the human-readable half of
// "REPORTED, never silently dropped". The caller logs it once per pass; the panel
// can render the same strings.
func DescribePlan(returns, kept []Return) string {
	parts := make([]string, 0, len(returns)+1)
	for _, r := range returns {
		parts = append(parts, fmt.Sprintf("%s %s→%s", r.Prefix, r.From, r.To))
	}
	if len(returns) > 0 {
		parts = append(parts, fmt.Sprintf("(%d returned)", len(returns)))
	}
	byReason := map[string][]string{}
	for _, r := range kept {
		byReason[r.Reason] = append(byReason[r.Reason], r.Prefix)
	}
	reasons := make([]string, 0, len(byReason))
	for reason := range byReason {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	for _, reason := range reasons {
		prefixes := byReason[reason]
		sample := prefixes
		if len(sample) > 3 {
			sample = sample[:3]
		}
		parts = append(parts, fmt.Sprintf("%s: %d (%s…)", reason, len(prefixes), strings.Join(sample, ",")))
	}
	return strings.Join(parts, "; ")
}

// HealthySince returns, per relay hostname (lower-cased), the unix second at
// which that relay became continuously healthy in its CURRENT episode.
//
// WHERE THIS COMES FROM, AND WHY IT IS NOT `last_state_change_at` ALONE
// ---------------------------------------------------------------------
// The durable record of "this relay came back" is `exit_node_state_changes` —
// the monitor's append-only transition log (B273 era). It is read here as the
// authority: the last transition row for the hostname, when its `to_state` is
// usable (`online`/`untagged`), dates the start of the current healthy episode.
//
// `exit_node_health.last_state_change_at` is consulted as a FALLBACK, only for a
// relay with no transition row at all. Measured while building this block:
// `internal/monitoring/exit_node_monitor.go` (`computeSnapshot`, the
// `LastStateChangeAt: now` field) writes that column on EVERY tick, so it is a
// "last check" stamp, not a "since" stamp — the comment beside it says "updated
// only on actual transitions below" and no such code exists. Hysteresis measured
// from it would be `now - (at most one monitor tick)` and a 10-minute window
// would NEVER be satisfied: a dead mechanism is the same failure as the latch
// this block removes (docs/LESSONS.md L-60). The fallback is kept so that fixing
// the monitor (preserve the stamp while the state is unchanged) needs no change
// here, and a relay with neither record is reported as `return-too-soon` rather
// than returned on faith.
func HealthySince(d *sql.DB, now int64) map[string]int64 {
	out := map[string]int64{}
	if d == nil {
		return out
	}
	type snap struct {
		healthy    bool
		lastChange int64
	}
	snaps := map[string]snap{}
	if rows, err := d.Query(`SELECT COALESCE(hostname,''), COALESCE(healthy,0), COALESCE(last_state_change_at,0)
	                         FROM exit_node_health`); err == nil {
		for rows.Next() {
			var host string
			var healthy int
			var at int64
			if rows.Scan(&host, &healthy, &at) == nil && host != "" {
				snaps[strings.ToLower(host)] = snap{healthy: healthy != 0, lastChange: at}
			}
		}
		rows.Close()
	}
	// The transition log is ordered so the LAST row per hostname wins. It is a
	// cold-path table (one row per state change), and reading it in one ordered
	// pass is dialect-portable — a correlated MAX() subquery would be the only
	// other option and buys nothing here.
	fromTransition := map[string]int64{}
	if rows, err := d.Query(`SELECT COALESCE(hostname,''), COALESCE(to_state,''), COALESCE(detected_at,0)
	                         FROM exit_node_state_changes
	                         ORDER BY detected_at ASC, id ASC`); err == nil {
		for rows.Next() {
			var host, toState string
			var at int64
			if rows.Scan(&host, &toState, &at) != nil || host == "" {
				continue
			}
			key := strings.ToLower(host)
			if monitoring.ExitNodeUsable(toState) {
				fromTransition[key] = at
			} else {
				delete(fromTransition, key)
			}
		}
		rows.Close()
	}
	for host, s := range snaps {
		if at, ok := fromTransition[host]; ok && at > 0 {
			out[host] = at
			continue
		}
		if s.healthy && s.lastChange > 0 {
			out[host] = s.lastChange
		}
	}
	return out
}

// ProvenRelays returns the relays skygate has a recorded SUCCESSFUL route
// application for — `global_settings.relay_apply_state:<relay>` = "<unix>|ok"
// (B309).
//
// internal/prefixowner must not import internal/feature/exit_rules (that package
// imports this one), so the key is read directly. The format is pinned by B309 and
// a value that does not parse as "<unix>|ok" is treated as "not proven": a relay
// is only allowed to take prefixes back on EVIDENCE, and a missing/aired failure
// record is not evidence of success.
func ProvenRelays(d *sql.DB) map[string]bool {
	out := map[string]bool{}
	if d == nil {
		return out
	}
	rows, err := d.Query(`SELECT key, COALESCE(value,'') FROM global_settings WHERE key LIKE $1`,
		relayApplyStateKeyPrefix+"%")
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var key, value string
		if rows.Scan(&key, &value) != nil {
			continue
		}
		relay := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(key, relayApplyStateKeyPrefix)))
		if relay == "" {
			continue
		}
		parts := strings.SplitN(strings.TrimSpace(value), "|", 3)
		if len(parts) < 2 {
			continue
		}
		at, perr := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
		if perr != nil || at <= 0 {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(parts[1]), "ok") {
			out[relay] = true
		}
	}
	return out
}

// relayApplyStateKeyPrefix mirrors exit_rules.SettingRelayApplyStatePrefix (B309).
// It is duplicated deliberately: importing internal/feature/exit_rules from here
// would be an import cycle, and a check in
// scripts/check_b360_prefix_reservation.sh pins the two literals together.
const relayApplyStateKeyPrefix = "relay_apply_state:"

// overrideWarnedKey is the global_settings row that rate-limits the "the override
// is still active and the relays it replaced are healthy again" warning to once
// per window.
const overrideWarnedKey = "prefix_owner_force_warned_at"

// OverrideWarnWindow is how often that warning may be logged.
const OverrideWarnWindow = 6 * time.Hour

// OverrideStatus is the read-only description of the operator's global override
// (B360 §4): the override is an operator TOOL, and its state — how long it has
// been on, and what the assignment would be without it — must be visible instead
// of silently persisting for 19 hours.
type OverrideStatus struct {
	// Relay is the override's relay ("" = no override).
	Relay string
	// SetAt is `global_settings.updated_at` for the override row (zero when the
	// row carries no timestamp, or there is no override).
	SetAt time.Time
	// Age is now - SetAt (zero when SetAt is unknown).
	Age time.Duration
	// Active is Relay != "".
	Active bool
	// Healthy is whether the override's relay may carry traffic right now.
	Healthy bool
	// WouldBe counts the prefixes per owner in the assignment that would be in
	// force WITHOUT the override (only owners other than Relay).
	WouldBe map[string]int
	// WouldBeTotal is the number of prefixes the override is currently replacing.
	WouldBeTotal int
	// Superseded is true when the override is active and healthy but every relay
	// it replaces is healthy again — the exact silent state that persisted for 19
	// hours on the reference deployment.
	Superseded bool
}

// ForceRelaySetAt returns when the operator's override was last written, from
// `global_settings.updated_at`. `false` means "no override, or the row carries no
// timestamp".
func ForceRelaySetAt(d *sql.DB) (time.Time, bool) {
	if d == nil {
		return time.Time{}, false
	}
	var ts int64
	if err := d.QueryRow(`SELECT COALESCE(updated_at,0) FROM global_settings WHERE key = $1`,
		forceRelaySettingKey).Scan(&ts); err != nil || ts <= 0 {
		return time.Time{}, false
	}
	return time.Unix(ts, 0).UTC(), true
}

// WouldBeAssignmentsWithoutForce returns the assignment the engine would write if
// the operator's override were not set. It is the pure AssignWithPreference over
// the current rules and health — no writes, no side effects — so the panel can
// answer «до форсажа владельцы были: karolina (139/139)» cheaply.
func WouldBeAssignmentsWithoutForce(d *sql.DB, healthyRelays []string) ([]Assignment, error) {
	if d == nil {
		return nil, fmt.Errorf("prefixowner: no database")
	}
	claims, err := LoadClaims(d)
	if err != nil {
		return nil, err
	}
	existing, err := LoadExisting(d)
	if err != nil {
		return nil, err
	}
	return AssignWithPreference(claims, healthyRelays, existing, nil), nil
}

// DescribeOverride is the read-only status the panel and the log render. It never
// clears anything.
func DescribeOverride(d *sql.DB, healthyRelays []string, now time.Time) OverrideStatus {
	st := OverrideStatus{Relay: ForceRelay(d), WouldBe: map[string]int{}}
	if st.Relay == "" {
		return st
	}
	st.Active = true
	st.Healthy = relayIsHealthy(st.Relay, healthyRelays)
	if at, ok := ForceRelaySetAt(d); ok {
		st.SetAt = at
		st.Age = now.Sub(at)
		if st.Age < 0 {
			st.Age = 0
		}
	}
	would, err := WouldBeAssignmentsWithoutForce(d, healthyRelays)
	if err != nil {
		return st
	}
	for _, a := range would {
		if a.ExitNode == "" || strings.EqualFold(a.ExitNode, st.Relay) {
			continue
		}
		st.WouldBe[a.ExitNode]++
		st.WouldBeTotal++
	}
	st.Superseded = st.Healthy && st.WouldBeTotal > 0
	return st
}

// DescribeOverrideLine renders the one-line summary the operator asked for:
// «форсаж N ч, до него владельцы: karolina (139/139)» — the age of the override
// and the owners it displaced.
func DescribeOverrideLine(st OverrideStatus) string {
	if !st.Active {
		return ""
	}
	owners := make([]string, 0, len(st.WouldBe))
	for owner := range st.WouldBe {
		owners = append(owners, owner)
	}
	sort.Strings(owners)
	parts := make([]string, 0, len(owners))
	for _, owner := range owners {
		parts = append(parts, fmt.Sprintf("%s (%d/%d)", owner, st.WouldBe[owner], st.WouldBeTotal))
	}
	age := "age unknown"
	if !st.SetAt.IsZero() {
		age = st.Age.Truncate(time.Minute).String()
	}
	health := "healthy"
	if !st.Healthy {
		health = "NOT healthy (the override is ignored by the engine)"
	}
	return fmt.Sprintf("prefix_owner_force_relay=%s active for %s (%s); without it: %s",
		st.Relay, age, health, strings.Join(parts, ", "))
}

// warnWhileOverrideSuperseded logs the 19-hour state ONCE per override window:
// the override is active and healthy while every relay it displaced is healthy
// again. It never changes anything — clearing the override stays a human
// decision.
//
// Returns whether it logged, so a test can assert the rate limit without reading
// the process log.
func warnWhileOverrideSuperseded(d *sql.DB, healthyRelays []string, now time.Time) bool {
	// Cheap exits first: with no override (or one already reported inside the
	// window) this must cost one SELECT, because it runs on every maintenance pass.
	if ForceRelay(d) == "" {
		return false
	}
	if last, err := db.GetGlobalSetting(d, overrideWarnedKey, ""); err == nil {
		if lastAt, perr := strconv.ParseInt(strings.TrimSpace(last), 10, 64); perr == nil &&
			now.Unix()-lastAt < int64(OverrideWarnWindow.Seconds()) {
			return false
		}
	}
	st := DescribeOverride(d, healthyRelays, now)
	if !st.Superseded {
		return false
	}
	if err := db.SetGlobalSetting(d, overrideWarnedKey, strconv.FormatInt(now.Unix(), 10)); err != nil {
		log.Printf("prefix-owner: could not record the override warning time: %v", err)
	}
	log.Printf("prefix-owner: OPERATOR OVERRIDE STILL ACTIVE — %s. Every relay it replaces is healthy again, so the failover it was set for is over; skygate will NOT clear it by itself (a human's deliberate choice). Clear it from /admin/exit-nodes/prefix-owner-force to return each prefix to the relays above.",
		DescribeOverrideLine(st))
	return true
}
