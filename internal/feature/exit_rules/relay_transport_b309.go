// internal/feature/exit_rules/relay_transport_b309.go — B309 (v1.5.74).
//
// WHY THIS EXISTS (live evidence, agent VM, 2026-09-23)
//
// /admin/exit-nodes showed «владелец не объявляет» on 29 prefixes and a table
// full of «нет маршрута», and pressing «Пересобрать и применить ACL» could not
// help, because the OWNER relay was unreachable:
//
//	staggeredSync(aggregated): karolina advertising 114 unique routes
//	exit-node sync(karolina): not a local relay (local daemon unreadable) — using the SSH transport
//	staggeredSync(aggregated): karolina SSH err: ssh root@100.64.0.2:18022 … Operation timed out
//	staggeredSync(aggregated): emilia advertised: ssh=ok approved=1…
//
// The prefix table kept karolina as the owner of 75 prefixes because the healthy
// set only looked at the exit-node health table, which is derived from headscale
// node state — and an online tailnet node that skygate cannot configure is
// "healthy" there while it can never advertise anything. So the prefixes stayed
// assigned to a relay that never served them, the ACL pinned `via=karolina`, and
// the operator saw "нет маршрута" forever with no line of the journal explaining
// why the assignment would not move.
//
// B309 makes the transport part of the health decision:
//
//  1. every route application records its outcome per relay — timestamp plus
//     reason, in global_settings, so no schema change and both dialects work;
//  2. a relay whose LAST application failed within RelayApplyFailureWindow is
//     excluded from the healthy set used for prefix assignment, and the
//     exclusion is logged with the reason and the age;
//  3. prefixowner.Assign already implements "an unhealthy owner loses the
//     prefix" (B275) — so the prefixes move to a relay that actually answers,
//     the ACL pin follows in the same pass (B276), and the operator's table
//     finally loses its "нет маршрута" rows;
//  4. a SUCCESSFUL application clears the record, so the relay returns to the
//     healthy set on its own on the next tick after it recovers — no manual
//     un-blocking, no permanent demotion, and nothing to remember.
//
// It deliberately does NOT touch the exit-node health table: a relay can be a
// perfectly healthy tailnet node and still be unreachable for configuration.
// Conflating the two would make the health page lie in the other direction (the
// B273 failure mode), and would also drop a working relay from the alerting
// denominator.
package exit_rules

import (
	"database/sql"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"skygate/internal/db"
)

// SettingRelayApplyStatePrefix is the global_settings key prefix holding one
// relay's last route-application state: "<unix>|ok" or "<unix>|err|<reason>".
const SettingRelayApplyStatePrefix = "relay_apply_state:"

// SettingRelayApplyViaPrefix holds WHERE that application went (B310):
// "<kind>|<endpoint>", e.g. "tailnet|tailnet 100.64.0.2:18022". It lives in its own
// key so the B309 record's third field (the error text) never has to be parsed
// around a separator that an ssh message may itself contain.
const SettingRelayApplyViaPrefix = "relay_apply_via:"

// RelayApplyFailureWindow is how long a failed application keeps a relay out of
// the healthy set used for prefix assignment. Fifteen minutes is three sync
// ticks: long enough that a transient SSH hiccup does not move 75 prefixes (and
// rewrite the ACL, which on a file-mode host restarts headscale), short enough
// that a recovered relay is back within a tick or two.
const RelayApplyFailureWindow = 15 * time.Minute

// RelayApplyState is the decoded per-relay state. Exported because the admin
// page renders the reason next to a relay whose prefixes were moved away.
type RelayApplyState struct {
	// At is the unix time of the recorded application (0 = never recorded).
	At int64
	// OK is true when the last application succeeded.
	OK bool
	// Detail names the failure ("" when OK).
	Detail string
	// Via is the transport that carried the routes ("local", "tailnet", "public",
	// "name") — empty for rows written before B310.
	Via string
	// Endpoint is the address the application used ("tailnet 100.64.0.2:18022").
	Endpoint string
}

// Failed reports whether this record describes a failure that is still inside
// the window at `now`.
func (st RelayApplyState) Failed(now int64, window time.Duration) bool {
	if st.At == 0 || st.OK {
		return false
	}
	if window <= 0 {
		return true
	}
	if now < st.At {
		return true // clock skew: trust the recorded failure
	}
	return now-st.At < int64(window.Seconds())
}

// RelayApplyStateKey is the global_settings key of one relay's record.
func RelayApplyStateKey(relay string) string {
	return SettingRelayApplyStatePrefix + strings.ToLower(strings.TrimSpace(relay))
}

// ParseRelayApplyState decodes the stored value. An unparseable value is treated
// as "never recorded" (the relay is healthy) — never as a failure, because a
// storage hiccup must not silently reassign the operator's prefixes.
func ParseRelayApplyState(raw string) RelayApplyState {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return RelayApplyState{}
	}
	parts := strings.SplitN(raw, "|", 3)
	at, err := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
	if err != nil {
		return RelayApplyState{}
	}
	st := RelayApplyState{At: at}
	if len(parts) >= 2 && strings.TrimSpace(parts[1]) == "ok" {
		st.OK = true
		return st
	}
	if len(parts) >= 3 {
		st.Detail = strings.TrimSpace(parts[2])
	}
	if st.Detail == "" {
		st.Detail = "route application failed (no detail recorded)"
	}
	return st
}

func formatRelayApplyState(st RelayApplyState) string {
	if st.At == 0 {
		return ""
	}
	if st.OK {
		return fmt.Sprintf("%d|ok", st.At)
	}
	return fmt.Sprintf("%d|err|%s", st.At, st.Detail)
}

// relayApplyFailedFromOutcome decides whether one application counts as a
// failure and names it. A transport error is the case the operator hit; an
// approval error counts too, because a prefix whose routes headscale refuses to
// approve is not served either — and either way the relay must not keep owning
// prefixes it cannot serve.
func relayApplyFailedFromOutcome(out relayApplyOutcome) (bool, string) {
	for _, prefix := range []string{"ssh=err=", "local=err="} {
		if strings.HasPrefix(out.Label, prefix) {
			return true, strings.TrimSpace(strings.TrimPrefix(out.Label, prefix))
		}
	}
	if out.ApproveErr != nil {
		return true, "approve failed: " + out.ApproveErr.Error()
	}
	return false, ""
}

// recordRelayApply stores the outcome of one relay's route application. Best
// effort by design: a storage failure only costs the demotion decision of this
// pass, and must never make a sync fail.
func recordRelayApply(d *sql.DB, relay string, out relayApplyOutcome) {
	if d == nil || strings.TrimSpace(relay) == "" {
		return
	}
	// B370: an outcome that is not evidence about the relay must not demote it.
	// The portal having no tailnet of its own says nothing about whether this
	// relay works, and treating it as a failure moved the relay's prefixes to
	// another relay and painted the page red after every /admin/update.
	if out.NotEvidence {
		log.Printf("relay-apply(%s): NOT recorded as a failure — %s, so this attempt says nothing about the relay; the previous state stands and its prefixes stay put. Enable Tailscale on /admin/tailscale (or give the container a real SKYGATE_TS_AUTHKEY_FILE) so the next pass can run over the tailnet", relay, out.NotEvidenceReason)
		return
	}
	now := time.Now().Unix()
	if failed, detail := relayApplyFailedFromOutcome(out); failed {
		if len(detail) > 300 {
			detail = detail[:300]
		}
		_ = db.SetGlobalSetting(d, RelayApplyStateKey(relay), formatRelayApplyState(RelayApplyState{At: now, Detail: detail}))
	} else {
		_ = db.SetGlobalSetting(d, RelayApplyStateKey(relay), formatRelayApplyState(RelayApplyState{At: now, OK: true}))
	}
	// B310: and WHERE it went (or where every attempt failed), so the page can
	// answer "is management using the tailnet or the public address?" without
	// reading the journal.
	via := strings.TrimSpace(out.Via)
	ep := strings.TrimSpace(out.Endpoint)
	if via == "" && ep == "" {
		via = "unknown"
	}
	_ = db.SetGlobalSetting(d, RelayApplyViaKey(relay), via+"|"+ep)
}

// RelayApplyViaKey is the global_settings key holding "kind|endpoint" for one relay.
func RelayApplyViaKey(relay string) string {
	return SettingRelayApplyViaPrefix + strings.ToLower(strings.TrimSpace(relay))
}

// parseRelayApplyVia decodes "<kind>|<endpoint>".
func parseRelayApplyVia(raw string) (via, endpoint string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	parts := strings.SplitN(raw, "|", 2)
	via = strings.TrimSpace(parts[0])
	if via == "unknown" {
		via = ""
	}
	if len(parts) == 2 {
		endpoint = strings.TrimSpace(parts[1])
	}
	return via, endpoint
}

// RelayApplyStateOf reads one relay's recorded transport state (free function;
// the Service method below is the same read for handler code).
func RelayApplyStateOf(d *sql.DB, relay string) RelayApplyState {
	if d == nil {
		return RelayApplyState{}
	}
	raw, err := db.GetGlobalSetting(d, RelayApplyStateKey(relay), "")
	if err != nil {
		return RelayApplyState{}
	}
	st := ParseRelayApplyState(raw)
	if viaRaw, verr := db.GetGlobalSetting(d, RelayApplyViaKey(relay), ""); verr == nil {
		st.Via, st.Endpoint = parseRelayApplyVia(viaRaw)
	}
	return st
}

// RelayApplyState reads one relay's recorded transport state.
func (s *Service) RelayApplyState(relay string) RelayApplyState {
	if s == nil {
		return RelayApplyState{}
	}
	return RelayApplyStateOf(s.dbc(), relay)
}

// healthyExitRelaysForAssignment is the healthy set the prefix assignment uses:
// the headscale-derived health (B275) MINUS every relay whose last route
// application failed inside the window. The second half is B309 — an online
// relay that skygate cannot configure is not a usable owner.
//
// The exclusion is logged ONCE per pass with the reason and the age, so the
// journal explains why the prefixes moved instead of the operator seeing an
// unexplained reassignment.
//
// B352 (2026-10-05) — "never applied" is not the same as "healthy", but it is not the
// same as "broken" either. The rule is a PREFERENCE, not a veto:
//
//   - a relay whose last application FAILED inside the window is excluded outright
//     (B309 — it cannot carry traffic right now);
//   - a relay skygate has successfully applied to at least once is PROVEN: if any
//     proven relay exists, only proven relays may take prefixes. Live: `sharlotta`
//     owned 95 prefixes while advertising 2 routes, because the sync lists are built
//     from `device_rules.exit_node_id`, no rule ever named it, and it had never been
//     configured — so it was elected owner of prefixes whose `via=` pin pointed at a
//     relay that could not serve them, and every device with such a rule lost the
//     destination;
//   - when NOTHING is proven yet (a fresh install, or a database restored without the
//     records) the healthy set is returned unchanged, because an empty candidate set
//     would push every prefix onto the rules' own relays and quietly change the model.
//
// Either way a relay only stops being a candidate for a RECORDED reason, and the
// reason is logged once per pass.
func healthyExitRelaysForAssignment(d *sql.DB) []string {
	base := healthyExitRelays(d)
	if len(base) == 0 {
		return base
	}
	now := time.Now().Unix()
	proven := make([]string, 0, len(base))
	var staleFailure, neverApplied []string
	for _, relay := range base {
		st := RelayApplyStateOf(d, relay)
		if st.Failed(now, RelayApplyFailureWindow) {
			log.Printf("prefix-owner: %s excluded from the healthy set — its last route application failed %s ago: %s (its prefixes move to a relay that can be configured; it returns automatically after the next successful apply)",
				relay, time.Since(time.Unix(st.At, 0)).Truncate(time.Second), st.Detail)
			continue
		}
		switch {
		case st.At > 0 && st.OK:
			proven = append(proven, relay)
		case st.At > 0:
			// The failure has aged out of the exclusion window, so B309 no longer
			// removes the relay — but it still has no proof that skygate CAN configure
			// it, so it does not get to outrank a relay that does. This is a distinct
			// state from "never applied" and must be logged as such: an operator who
			// read "never applied" about a relay they configured yesterday would be
			// looking for a lost record instead of a failed transport (L-10.2).
			staleFailure = append(staleFailure, relay)
		default:
			neverApplied = append(neverApplied, relay)
		}
	}
	if len(proven) == 0 {
		// Nothing has been applied successfully yet: keep every healthy relay (B309's
		// original behaviour). The engine has no evidence to prefer any of them.
		return append(append(proven, staleFailure...), neverApplied...)
	}
	for _, relay := range staleFailure {
		log.Printf("prefix-owner: %s excluded from the healthy set — its last recorded route application FAILED and the exclusion window has passed, so it is unproven until the next successful apply (a proven relay is available: %s)",
			relay, strings.Join(proven, ","))
	}
	for _, relay := range neverApplied {
		log.Printf("prefix-owner: %s excluded from the healthy set — skygate has never applied routes to it, so it cannot carry a prefix while a proven relay (%s) is available",
			relay, strings.Join(proven, ","))
	}
	return proven
}

// ListRelayApplyStates returns the recorded transport state of every relay that
// has one, keyed by the relay name as stored (lower-cased, the key suffix).
func ListRelayApplyStates(d *sql.DB) map[string]RelayApplyState {
	out := map[string]RelayApplyState{}
	if d == nil {
		return out
	}
	rows, err := d.Query(`SELECT key, value FROM global_settings WHERE key LIKE $1`,
		SettingRelayApplyStatePrefix+"%")
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var key, val string
		if err := rows.Scan(&key, &val); err != nil {
			continue
		}
		name := strings.TrimPrefix(key, SettingRelayApplyStatePrefix)
		st := ParseRelayApplyState(val)
		if raw, verr := db.GetGlobalSetting(d, SettingRelayApplyViaPrefix+name, ""); verr == nil {
			st.Via, st.Endpoint = parseRelayApplyVia(raw)
		}
		out[name] = st
	}
	return out
}
