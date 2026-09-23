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
	now := time.Now().Unix()
	if failed, detail := relayApplyFailedFromOutcome(out); failed {
		if len(detail) > 300 {
			detail = detail[:300]
		}
		_ = db.SetGlobalSetting(d, RelayApplyStateKey(relay), formatRelayApplyState(RelayApplyState{At: now, Detail: detail}))
		return
	}
	_ = db.SetGlobalSetting(d, RelayApplyStateKey(relay), formatRelayApplyState(RelayApplyState{At: now, OK: true}))
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
	return ParseRelayApplyState(raw)
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
func healthyExitRelaysForAssignment(d *sql.DB) []string {
	base := healthyExitRelays(d)
	if len(base) == 0 {
		return base
	}
	now := time.Now().Unix()
	out := make([]string, 0, len(base))
	for _, relay := range base {
		st := RelayApplyStateOf(d, relay)
		if st.Failed(now, RelayApplyFailureWindow) {
			log.Printf("prefix-owner: %s excluded from the healthy set — its last route application failed %s ago: %s (its prefixes move to a relay that can be configured; it returns automatically after the next successful apply)",
				relay, time.Since(time.Unix(st.At, 0)).Truncate(time.Second), st.Detail)
			continue
		}
		out = append(out, relay)
	}
	return out
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
		out[strings.TrimPrefix(key, SettingRelayApplyStatePrefix)] = ParseRelayApplyState(val)
	}
	return out
}
