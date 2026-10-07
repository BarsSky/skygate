// reservation_b360_test.go — B360 (2026-10-07): the "artificial tests" the
// operator asked for («искусственные тесты и проверки работоспособности
// механизма»).
//
// Every test here drives a SCRIPTED health timeline over the real migrated SQLite
// schema (the house style of prefixowner_b275_test.go / prefixowner_b277_test.go):
// the relays change state at chosen unix seconds, the engine runs a pass at those
// seconds, and the assertion is the OWNER SEQUENCE of the assignment table —
// which relay serves the prefix after each step, and what the reservation row says
// about it. The hysteresis is therefore checked as behaviour, not as a constant.
package prefixowner

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	skygatedb "skygate/internal/db"
)

// b360Base is an arbitrary unix second; every timeline is expressed as an offset
// from it, so the tests never depend on the wall clock.
const b360Base = int64(1_800_000_000)

const b360Prefix = "104.16.0.0/12"

func b360Open(t *testing.T) *sql.DB {
	t.Helper()
	_, d, err := skygatedb.OpenWithDialect("sqlite:" + t.TempDir() + "/b360.db")
	if err != nil {
		t.Skipf("sqlite dialect unavailable: %v", err)
	}
	if err := skygatedb.MigrateSQLite(d); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	// device_rules.user_id is a real FOREIGN KEY on both dialects (SQLite enforces
	// it once the schema exists), so the owner row has to be there before a claim.
	if _, err := d.Exec(`INSERT INTO portal_users (id, username, password_hash, is_admin, headscale_user_id)
	                     VALUES (1, 'skyadmin', 'x', 1, 1)`); err != nil {
		t.Fatalf("seed portal_users: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// b360Relay writes the two rows the engine and the planner read for one relay:
// the health snapshot and the transition log. A relay that is `untagged` is
// healthy too (B273), `degraded` is NOT (its 0.0.0.0/0 route is not approved).
func b360Relay(t *testing.T, d *sql.DB, host, state string, at int64) {
	t.Helper()
	healthy := state == "online" || state == "untagged"
	if err := skygatedb.UpsertExitNodeHealth(d, skygatedb.ExitNodeHealth{
		NodeID: host, Hostname: host, Online: healthy, State: state, Healthy: healthy,
		AdvertisedRoutesOK: healthy, HasExitTag: true,
		LastCheckAt: time.Unix(at, 0).UTC(), LastStateChangeAt: time.Unix(at, 0).UTC(),
	}); err != nil {
		t.Fatalf("seed health %s=%s: %v", host, state, err)
	}
	if _, err := skygatedb.RecordExitNodeStateChange(d, skygatedb.ExitNodeStateChange{
		NodeID: host, Hostname: host, FromState: "unknown", ToState: state,
		DetectedAt: time.Unix(at, 0).UTC(),
	}); err != nil {
		t.Fatalf("seed transition %s→%s: %v", host, state, err)
	}
}

// b360Prove records a successful route application for a relay (B309's
// `relay_apply_state:<relay>` = "<unix>|ok"), which is what makes it eligible to
// take a prefix BACK.
func b360Prove(t *testing.T, d *sql.DB, host string, at int64) {
	t.Helper()
	if err := skygatedb.SetGlobalSetting(d, "relay_apply_state:"+host,
		fmt.Sprintf("%d|ok", at)); err != nil {
		t.Fatalf("seed apply state %s: %v", host, err)
	}
}

func b360Claim(t *testing.T, d *sql.DB, deviceID int, relay, prefix string) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO device_rules (user_id, device_id, exit_node_id, target_type, target_value, enabled)
	                     VALUES (1, $1, $2, 'ip', $3, 1)`, deviceID, relay, prefix); err != nil {
		t.Fatalf("seed claim %s→%s: %v", prefix, relay, err)
	}
}

// b360SeedReservation plants the live shape directly: the prefix is served by
// `substitute`, reserved from `takenFrom` at `at` with `flaps` quick flaps so far.
func b360SeedReservation(t *testing.T, d *sql.DB, prefix, substitute, takenFrom, source string, at int64, flaps int) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO prefix_owner
		(prefix, exit_node_id, source, claims, devices, updated_at, failover_from, failover_at, failover_flaps)
		VALUES ($1,$2,$3,1,1,0,$4,$5,$6)`, prefix, substitute, source, takenFrom, at, flaps); err != nil {
		t.Fatalf("seed reservation %s: %v", prefix, err)
	}
}

type b360Row struct {
	owner  string
	source string
	from   string
	at     int64
	flaps  int
}

func b360Read(t *testing.T, d *sql.DB, prefix string) b360Row {
	t.Helper()
	var r b360Row
	if err := d.QueryRow(`SELECT exit_node_id, COALESCE(source,''), COALESCE(failover_from,''),
	                             COALESCE(failover_at,0), COALESCE(failover_flaps,0)
	                      FROM prefix_owner WHERE prefix = $1`, prefix).
		Scan(&r.owner, &r.source, &r.from, &r.at, &r.flaps); err != nil {
		t.Fatalf("read %s: %v", prefix, err)
	}
	return r
}

func b360Reason(kept []Return, prefix string) string {
	for _, r := range kept {
		if r.Prefix == prefix {
			return r.Reason
		}
	}
	return ""
}

// b360LiveWorld is the minimum the engine needs: two relays that are both online
// and proven, and one prefix claimed through karolina.
func b360LiveWorld(t *testing.T) *sql.DB {
	t.Helper()
	d := b360Open(t)
	b360Claim(t, d, 9, "karolina", b360Prefix)
	b360Relay(t, d, "karolina", "online", b360Base-86400)
	b360Relay(t, d, "emilia", "online", b360Base-86400)
	b360Prove(t, d, "karolina", b360Base-3600)
	b360Prove(t, d, "emilia", b360Base-3600)
	return d
}

// TestB360_ScriptedTimeline_FailoverThenReturn is the timeline the block exists
// for, asserted as the owner sequence:
//
//	emilia owns X ... no: karolina owns X
//	karolina goes down        → X moves to emilia and records failover_from=karolina
//	karolina returns, 3 min   → STILL emilia, reason return-too-soon, and the pass
//	                            writes nothing (changed=0 — no ACL churn)
//	11 min of health          → X returns to karolina and the reservation is cleared
//	a second identical pass   → nothing changes (idempotent)
func TestB360_ScriptedTimeline_FailoverThenReturn(t *testing.T) {
	d := b360LiveWorld(t)
	cfg := DefaultReservationConfig()
	owner := func() string { return b360Read(t, d, b360Prefix).owner }

	// ── T0 — the rules name karolina, karolina is healthy ────────────────────
	rep, err := ReconcileReportAt(d, []string{"karolina", "emilia"}, nil, cfg, b360Base)
	if err != nil {
		t.Fatalf("T0 reconcile: %v", err)
	}
	got := b360Read(t, d, b360Prefix)
	if got.owner != "karolina" || got.source != "explicit" || got.from != "" {
		t.Fatalf("T0: row = %+v, want karolina/explicit with no reservation", got)
	}
	if len(rep.Returns) != 0 || len(rep.Kept) != 0 {
		t.Fatalf("T0: nothing is reserved yet, got returns=%v kept=%v", rep.Returns, rep.Kept)
	}

	// ── T1 — karolina goes down: the move is a FAILOVER ──────────────────────
	tDown := b360Base + 600
	b360Relay(t, d, "karolina", "offline", tDown)
	if _, err := ReconcileReportAt(d, []string{"emilia"}, nil, cfg, tDown); err != nil {
		t.Fatalf("T1 reconcile: %v", err)
	}
	got = b360Read(t, d, b360Prefix)
	if got.owner != "emilia" {
		t.Fatalf("T1: owner = %q, want emilia (karolina is down)", got.owner)
	}
	if got.from != "karolina" || got.at != tDown || got.flaps != 0 {
		t.Fatalf("T1: reservation = %+v, want failover_from=karolina at %d with 0 flaps", got, tDown)
	}
	if owner() != "emilia" {
		t.Fatalf("T1: owner changed under us")
	}

	// ── T2 — karolina returns; three minutes later the return is TOO SOON ────
	tUp := tDown + 1800
	b360Relay(t, d, "karolina", "online", tUp)
	rep, err = ReconcileReportAt(d, []string{"karolina", "emilia"}, nil, cfg, tUp+180)
	if err != nil {
		t.Fatalf("T2 reconcile: %v", err)
	}
	got = b360Read(t, d, b360Prefix)
	if got.owner != "emilia" {
		t.Fatalf("T2: owner = %q after only 3 min of health, want emilia (hysteresis)", got.owner)
	}
	if got.from != "karolina" {
		t.Fatalf("T2: the reservation was lost: %+v", got)
	}
	if reason := b360Reason(rep.Kept, b360Prefix); reason != ReasonTooSoon {
		t.Fatalf("T2: reason = %q, want %q (kept=%v)", reason, ReasonTooSoon, rep.Kept)
	}
	if len(rep.Returns) != 0 {
		t.Fatalf("T2: nothing may return yet: %v", rep.Returns)
	}
	if rep.Changed != 0 {
		t.Errorf("T2: changed = %d, want 0 — withholding a return must not rewrite the row and re-apply the ACL", rep.Changed)
	}

	// ── T3 — eleven minutes of continuous health: the prefix comes back ───────
	nowRet := tUp + 660
	rep, err = ReconcileReportAt(d, []string{"karolina", "emilia"}, nil, cfg, nowRet)
	if err != nil {
		t.Fatalf("T3 reconcile: %v", err)
	}
	got = b360Read(t, d, b360Prefix)
	if got.owner != "karolina" {
		t.Fatalf("T3: owner = %q, want karolina (11 min of health > 10 min hysteresis)", got.owner)
	}
	if got.source != "explicit" {
		t.Errorf("T3: source = %q, want explicit (the rules named karolina)", got.source)
	}
	if got.from != "" || got.flaps != 0 {
		t.Errorf("T3: reservation = %+v, want it cleared", got)
	}
	if got.at != nowRet {
		t.Errorf("T3: failover_at = %d, want %d — the return stamp is what dates the next quick-flap check", got.at, nowRet)
	}
	if len(rep.Returns) != 1 || rep.Returns[0].To != "karolina" || rep.Returns[0].From != "emilia" {
		t.Errorf("T3: returns = %+v, want emilia→karolina", rep.Returns)
	}

	// ── T4 — the same pass twice: idempotent, no second decision ─────────────
	rep, err = ReconcileReportAt(d, []string{"karolina", "emilia"}, nil, cfg, nowRet)
	if err != nil {
		t.Fatalf("T4 reconcile: %v", err)
	}
	if rep.Inserted != 0 || rep.Changed != 0 || len(rep.Returns) != 0 || len(rep.Kept) != 0 {
		t.Errorf("T4: a second pass with the same inputs must change nothing, got %+v", rep)
	}
	if again := b360Read(t, d, b360Prefix); again != got {
		t.Errorf("T4: row drifted on a no-op pass: %+v vs %+v", again, got)
	}
}

// TestB360_QuickFlapDoublesTheWindow: a prefix that returns and is taken away
// again inside the quarantine window must wait TWICE the hysteresis for its next
// return, so a relay that flaps every ten minutes cannot yank its prefixes back
// on every pass.
func TestB360_QuickFlapDoublesTheWindow(t *testing.T) {
	d := b360LiveWorld(t)
	cfg := DefaultReservationConfig()

	// T0 — karolina owns the prefix (a reservation can only be recorded against a
	// previous owner, so the timeline has to start from the healthy state).
	if _, err := ReconcileReportAt(d, []string{"karolina", "emilia"}, nil, cfg, b360Base); err != nil {
		t.Fatalf("T0: %v", err)
	}
	if got := b360Read(t, d, b360Prefix); got.owner != "karolina" || got.from != "" {
		t.Fatalf("T0: row = %+v, want karolina with no reservation", got)
	}

	// Failover #1 and a return after 11 minutes of health.
	tDown := b360Base + 600
	b360Relay(t, d, "karolina", "offline", tDown)
	if _, err := ReconcileReportAt(d, []string{"emilia"}, nil, cfg, tDown); err != nil {
		t.Fatalf("failover 1: %v", err)
	}
	tUp := tDown + 1800
	b360Relay(t, d, "karolina", "online", tUp)
	if _, err := ReconcileReportAt(d, []string{"karolina", "emilia"}, nil, cfg, tUp+660); err != nil {
		t.Fatalf("return 1: %v", err)
	}
	if got := b360Read(t, d, b360Prefix); got.owner != "karolina" {
		t.Fatalf("setup: owner = %q, want karolina", got.owner)
	}

	// It flaps again 5 minutes later — inside the 30-minute quarantine.
	tDown2 := tUp + 660 + 300
	b360Relay(t, d, "karolina", "offline", tDown2)
	if _, err := ReconcileReportAt(d, []string{"emilia"}, nil, cfg, tDown2); err != nil {
		t.Fatalf("failover 2: %v", err)
	}
	got := b360Read(t, d, b360Prefix)
	if got.owner != "emilia" || got.from != "karolina" {
		t.Fatalf("failover 2: row = %+v, want emilia reserved from karolina", got)
	}
	if got.flaps != 1 {
		t.Fatalf("failover 2: flaps = %d, want 1 (a return followed by a failover inside the quarantine window)", got.flaps)
	}

	// karolina returns. 11 minutes is enough for a FIRST return and must NOT be
	// enough for this one: the window is now 20 minutes.
	tUp2 := tDown2 + 1800
	b360Relay(t, d, "karolina", "online", tUp2)
	rep, err := ReconcileReportAt(d, []string{"karolina", "emilia"}, nil, cfg, tUp2+660)
	if err != nil {
		t.Fatalf("return 2 (too soon): %v", err)
	}
	if got := b360Read(t, d, b360Prefix); got.owner != "emilia" {
		t.Fatalf("after a flap, 11 min of health returned the prefix (owner=%q) — the window must be doubled", got.owner)
	}
	if reason := b360Reason(rep.Kept, b360Prefix); reason != ReasonQuarantined {
		t.Fatalf("reason = %q, want %q", reason, ReasonQuarantined)
	}
	if rep.Kept[0].Required != 2*DefaultHysteresis {
		t.Errorf("required window = %s, want %s", rep.Kept[0].Required, 2*DefaultHysteresis)
	}

	// 21 minutes of health does it.
	rep, err = ReconcileReportAt(d, []string{"karolina", "emilia"}, nil, cfg, tUp2+1260)
	if err != nil {
		t.Fatalf("return 2: %v", err)
	}
	if got := b360Read(t, d, b360Prefix); got.owner != "karolina" || got.from != "" {
		t.Fatalf("after the doubled window: row = %+v, want karolina with the reservation cleared", got)
	}
	if len(rep.Returns) != 1 {
		t.Errorf("returns = %+v, want one", rep.Returns)
	}
}

// TestB360_DegradedRelayIsNeverReturned: a relay that is online but whose
// 0.0.0.0/0 route is NOT approved cannot carry a prefix, so it never takes one
// back — the state B273 exists to tell apart from a false "online".
func TestB360_DegradedRelayIsNeverReturned(t *testing.T) {
	d := b360Open(t)
	b360Claim(t, d, 9, "karolina", b360Prefix)
	b360Relay(t, d, "emilia", "online", b360Base-86400)
	b360Prove(t, d, "emilia", b360Base-3600)
	b360Prove(t, d, "karolina", b360Base-3600)
	// karolina is up and proven, but its default route is not approved.
	b360Relay(t, d, "karolina", "degraded", b360Base+600)
	b360SeedReservation(t, d, b360Prefix, "emilia", "karolina", "auto", b360Base+600, 0)

	rep, err := ReconcileReportAt(d, []string{"emilia"}, nil, DefaultReservationConfig(), b360Base+7200)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := b360Read(t, d, b360Prefix); got.owner != "emilia" || got.from != "karolina" {
		t.Fatalf("degraded karolina took the prefix back: %+v", got)
	}
	if reason := b360Reason(rep.Kept, b360Prefix); reason != ReasonNotHealthy {
		t.Errorf("reason = %q, want %q", reason, ReasonNotHealthy)
	}
}

// TestB360_UnprovenRelayIsNeverReturned: `sharlotta` live was online with 95
// prefixes and 2 advertised routes, because "headscale says online" is true for a
// relay skygate has never configured. A return therefore needs a recorded
// SUCCESSFUL application, not just health.
func TestB360_UnprovenRelayIsNeverReturned(t *testing.T) {
	d := b360Open(t)
	b360Claim(t, d, 9, "karolina", b360Prefix)
	b360Relay(t, d, "emilia", "online", b360Base-86400)
	b360Prove(t, d, "emilia", b360Base-3600)
	// karolina: online, healthy for a day, but skygate has NEVER applied routes to
	// it (no relay_apply_state row).
	b360Relay(t, d, "karolina", "online", b360Base-86400)
	b360SeedReservation(t, d, b360Prefix, "emilia", "karolina", "auto", b360Base+600, 0)

	rep, err := ReconcileReportAt(d, []string{"karolina", "emilia"}, nil, DefaultReservationConfig(), b360Base+7200)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := b360Read(t, d, b360Prefix); got.owner != "emilia" {
		t.Fatalf("an unproven relay took the prefix back: %+v", got)
	}
	if reason := b360Reason(rep.Kept, b360Prefix); reason != ReasonNotProven {
		t.Errorf("reason = %q, want %q", reason, ReasonNotProven)
	}

	// Control: record one successful application and the very next pass returns it.
	b360Prove(t, d, "karolina", b360Base+7201)
	rep, err = ReconcileReportAt(d, []string{"karolina", "emilia"}, nil, DefaultReservationConfig(), b360Base+7202)
	if err != nil {
		t.Fatalf("reconcile after proving: %v", err)
	}
	if got := b360Read(t, d, b360Prefix); got.owner != "karolina" || got.from != "" {
		t.Fatalf("a proven relay must get its prefix back: %+v", got)
	}
	if len(rep.Returns) != 1 {
		t.Errorf("returns = %+v, want one", rep.Returns)
	}
}

// TestB360_ReservationForAnUnclaimedPrefixIsDropped: a reservation is only
// meaningful while somebody still wants the prefix. When the last rule goes away
// the reservation is DROPPED with a named reason — never silently kept, never
// "returned" to a relay for a prefix nobody asked for.
func TestB360_ReservationForAnUnclaimedPrefixIsDropped(t *testing.T) {
	d := b360Open(t)
	b360Relay(t, d, "karolina", "online", b360Base-86400)
	b360Relay(t, d, "emilia", "online", b360Base-86400)
	b360Prove(t, d, "karolina", b360Base-3600)
	b360Prove(t, d, "emilia", b360Base-3600)
	// The reservation exists, but no enabled rule claims the prefix any more.
	b360SeedReservation(t, d, b360Prefix, "emilia", "karolina", "auto", b360Base+600, 0)

	rep, err := ReconcileReportAt(d, []string{"karolina", "emilia"}, nil, DefaultReservationConfig(), b360Base+7200)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(rep.Returns) != 0 {
		t.Fatalf("a prefix nobody claims was RETURNED: %v", rep.Returns)
	}
	if reason := b360Reason(rep.Kept, b360Prefix); reason != ReasonNoLongerClaimed {
		t.Fatalf("reason = %q, want %q (kept=%v)", reason, ReasonNoLongerClaimed, rep.Kept)
	}
	var rows int
	if err := d.QueryRow(`SELECT COUNT(*) FROM prefix_owner WHERE prefix = $1`, b360Prefix).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 0 {
		t.Errorf("the stale reservation row survived the pass (%d row(s)) — Prune must drop it", rows)
	}
}

// TestB360_ReservationDroppedWhenTheRelayStopsClaiming is the other half of "still
// claims": the prefix IS wanted, but no longer by the reserved relay (the operator
// moved the rules), so the reservation is stale and must be dropped rather than
// returning the prefix to a relay the rules no longer name.
func TestB360_ReservationDroppedWhenTheRelayStopsClaiming(t *testing.T) {
	claims := []Claim{{Prefix: b360Prefix, ExitNode: "sharlotta", DeviceID: 9}}
	assignments := []Assignment{{
		Prefix: b360Prefix, ExitNode: "emilia", Source: "auto",
		FailoverFrom: "karolina", FailoverAt: b360Base,
	}}
	existing := []Existing{{
		Prefix: b360Prefix, ExitNode: "emilia", Source: "auto",
		FailoverFrom: "karolina", FailoverAt: b360Base,
	}}
	returns, kept := PlanReturns(claims, assignments, existing,
		[]string{"emilia", "karolina"}, map[string]bool{"karolina": true},
		map[string]int64{"karolina": b360Base - 86400}, "", false, b360Base+86400, DefaultReservationConfig())
	if len(returns) != 0 {
		t.Fatalf("karolina does not claim the prefix any more but was returned to: %v", returns)
	}
	if len(kept) != 1 || kept[0].Reason != ReasonNoLongerClaimed || !kept[0].Drop {
		t.Fatalf("kept = %+v, want one DROPPED return-no-longer-claimed", kept)
	}
}

// TestB360_ManualPinIsNeverReserved is the operator's absolute: a per-prefix pin
// is a deliberate human decision about ONE prefix, so it is never recorded as a
// failover, never returned, and a reservation that predates it is dropped.
func TestB360_ManualPinIsNeverReserved(t *testing.T) {
	d := b360Open(t)
	b360Claim(t, d, 9, "emilia", b360Prefix)
	b360Relay(t, d, "emilia", "online", b360Base-86400)
	b360Relay(t, d, "karolina", "online", b360Base-86400)
	b360Prove(t, d, "emilia", b360Base-3600)
	b360Prove(t, d, "karolina", b360Base-3600)
	if err := SetManual(d, b360Prefix, "emilia"); err != nil {
		t.Fatalf("SetManual: %v", err)
	}

	// emilia goes down: B275 moves the prefix (a pin to a dead relay carries
	// nothing), but the move is NOT a reservation.
	tDown := b360Base + 600
	b360Relay(t, d, "emilia", "offline", tDown)
	rep, err := ReconcileReportAt(d, []string{"karolina"}, nil, DefaultReservationConfig(), tDown)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	got := b360Read(t, d, b360Prefix)
	if got.from != "" {
		t.Fatalf("a manual pin was recorded as a failover reservation: %+v (report %+v)", got, rep)
	}

	// And a stale reservation on a manual row is dropped, not returned.
	b360SeedReservation(t, d, "8.8.8.0/24", "emilia", "karolina", "manual", tDown, 0)
	rep, err = ReconcileReportAt(d, []string{"karolina", "emilia"}, nil, DefaultReservationConfig(), tDown+3600)
	if err != nil {
		t.Fatalf("reconcile 2: %v", err)
	}
	if len(rep.Returns) != 0 {
		t.Fatalf("a manual row was RETURNED: %v", rep.Returns)
	}
	if reason := b360Reason(rep.Kept, "8.8.8.0/24"); reason != ReasonManualPin {
		t.Fatalf("reason = %q, want %q (kept=%v)", reason, ReasonManualPin, rep.Kept)
	}
	stale := b360Read(t, d, "8.8.8.0/24")
	if stale.from != "" || stale.flaps != 0 || stale.at != 0 {
		t.Errorf("the stale reservation on the manual row survived: %+v", stale)
	}
}

// TestB360_GlobalOverrideIsNotALatch reproduces the LIVE case end to end: 139
// prefixes held on emilia by `prefix_owner_force_relay`, reserved from karolina,
// and the operator clearing the override. The assignment must come back to the
// majority claimant on the next pass — and while the override is active every
// withheld return must be REPORTED as such.
func TestB360_GlobalOverrideIsNotALatch(t *testing.T) {
	d := b360Open(t)
	claims := []string{"104.16.0.0/12", "142.250.0.0/15"}
	for _, p := range claims {
		b360Claim(t, d, 9, "karolina", p)
		// The live shape after the outage: everything on emilia, reserved from
		// karolina, all source='global'.
		b360SeedReservation(t, d, p, "emilia", "karolina", "global", b360Base+600, 0)
	}
	b360Relay(t, d, "karolina", "online", b360Base+1800)
	b360Relay(t, d, "emilia", "online", b360Base-86400)
	b360Prove(t, d, "karolina", b360Base-3600)
	b360Prove(t, d, "emilia", b360Base-3600)
	if err := SetForceRelay(d, "emilia"); err != nil {
		t.Fatalf("SetForceRelay: %v", err)
	}
	cfg := DefaultReservationConfig()

	// The override is in force and every relay it replaces is healthy again — the
	// state that silently persisted for 19 hours. Nothing may move, and the plan
	// must say why.
	now := b360Base + 1800 + 7200
	rep, err := ReconcileReportAt(d, []string{"karolina", "emilia"}, nil, cfg, now)
	if err != nil {
		t.Fatalf("reconcile with the override: %v", err)
	}
	for _, p := range claims {
		if got := b360Read(t, d, p); got.owner != "emilia" || got.source != "global" || got.from != "karolina" {
			t.Fatalf("%s: row = %+v, want emilia/global with the reservation kept", p, got)
		}
		if reason := b360Reason(rep.Kept, p); reason != ReasonOverridden {
			t.Fatalf("%s: reason = %q, want %q", p, reason, ReasonOverridden)
		}
	}
	// The override was NOT auto-cleared by the warning path.
	if ForceRelay(d) != "emilia" {
		t.Fatalf("the engine cleared the operator's override by itself")
	}

	// The operator clears it: the very next pass returns every prefix to the
	// majority claimant (the live measurement: changed=139, source='explicit').
	if err := SetForceRelay(d, ""); err != nil {
		t.Fatalf("clear override: %v", err)
	}
	rep, err = ReconcileReportAt(d, []string{"karolina", "emilia"}, nil, cfg, now+60)
	if err != nil {
		t.Fatalf("reconcile after clearing: %v", err)
	}
	if len(rep.Returns) != len(claims) {
		t.Fatalf("returns = %+v, want %d", rep.Returns, len(claims))
	}
	for _, p := range claims {
		got := b360Read(t, d, p)
		if got.owner != "karolina" || got.source != "explicit" {
			t.Fatalf("%s: row = %+v, want karolina/explicit after clearing the override", p, got)
		}
		if got.from != "" {
			t.Fatalf("%s: the reservation was not cleared: %+v", p, got)
		}
	}
	if rep.Changed != len(claims) {
		t.Errorf("changed = %d, want %d (each return is an ownership change that re-applies the ACL)", rep.Changed, len(claims))
	}
}

// TestB360_OverrideStatusIsVisibleAndNeverClears: B360 §4 — the override is an
// operator tool, and its age plus the assignment it replaces must be readable, so
// the panel can offer «вернуть как было» as one action instead of the operator
// discovering a 19-hour-old latch by accident.
func TestB360_OverrideStatusIsVisibleAndNeverClears(t *testing.T) {
	d := b360LiveWorld(t)
	if err := SetForceRelay(d, "emilia"); err != nil {
		t.Fatalf("SetForceRelay: %v", err)
	}
	st := DescribeOverride(d, []string{"karolina", "emilia"}, time.Now())
	if !st.Active || st.Relay != "emilia" || !st.Healthy {
		t.Fatalf("status = %+v, want an active, healthy override on emilia", st)
	}
	if st.SetAt.IsZero() {
		t.Errorf("SetAt is zero — the override's age comes from global_settings.updated_at")
	}
	if st.WouldBe["karolina"] != 1 || st.WouldBeTotal != 1 {
		t.Fatalf("would-be owners = %v (total %d), want karolina 1/1", st.WouldBe, st.WouldBeTotal)
	}
	if !st.Superseded {
		t.Errorf("Superseded = false — this is exactly the state that persisted silently for 19 hours")
	}
	line := DescribeOverrideLine(st)
	for _, want := range []string{"prefix_owner_force_relay=emilia", "without it:", "karolina (1/1)"} {
		if !strings.Contains(line, want) {
			t.Errorf("DescribeOverrideLine() = %q, missing %q", line, want)
		}
	}
	// Reading the status must never clear the override.
	if ForceRelay(d) != "emilia" {
		t.Fatalf("DescribeOverride cleared the operator's override")
	}
}

// TestB360_OverrideWarningIsRateLimited pins the "once per window" half: the 19
// hours of silence were the bug, but a warning on every five-minute tick would be
// its own noise.
func TestB360_OverrideWarningIsRateLimited(t *testing.T) {
	d := b360Open(t)
	b360Claim(t, d, 9, "karolina", b360Prefix)
	b360SeedReservation(t, d, b360Prefix, "emilia", "karolina", "global", b360Base, 0)
	b360Relay(t, d, "karolina", "online", b360Base-86400)
	b360Relay(t, d, "emilia", "online", b360Base-86400)
	if err := SetForceRelay(d, "emilia"); err != nil {
		t.Fatalf("SetForceRelay: %v", err)
	}
	now := time.Unix(b360Base+7200, 0).UTC()
	if !warnWhileOverrideSuperseded(d, []string{"karolina", "emilia"}, now) {
		t.Fatalf("the warning did not fire on the 19-hour state")
	}
	if warnWhileOverrideSuperseded(d, []string{"karolina", "emilia"}, now) {
		t.Fatalf("the warning fired twice inside one window")
	}
	// Past the window it fires again, so the operator cannot forget the latch.
	old := now.Add(-2 * OverrideWarnWindow)
	if err := skygatedb.SetGlobalSetting(d, overrideWarnedKey,
		fmt.Sprintf("%d", old.Unix())); err != nil {
		t.Fatalf("rewind the warned key: %v", err)
	}
	if !warnWhileOverrideSuperseded(d, []string{"karolina", "emilia"}, now) {
		t.Fatalf("the warning did not fire again after the window elapsed")
	}
	// And a status with nothing replaced is not the state this warns about.
	if err := skygatedb.SetGlobalSetting(d, overrideWarnedKey, "0"); err != nil {
		t.Fatalf("reset the warned key: %v", err)
	}
	if err := SetForceRelay(d, "karolina"); err != nil {
		t.Fatalf("SetForceRelay(karolina): %v", err)
	}
	if warnWhileOverrideSuperseded(d, []string{"karolina", "emilia"}, now) {
		t.Errorf("the warning fired although the override replaces nobody")
	}
}

// TestB360_HealthySinceUsesTheTransitionLog pins the hysteresis INPUT: the durable
// "this relay came back at T" record is the monitor's transition log. The
// snapshot's own `last_state_change_at` is a fallback only — measured while
// building B360, internal/monitoring's computeSnapshot writes it on EVERY tick
// (`LastStateChangeAt: now`), so a window measured from it would never be
// satisfied and the whole mechanism would be dead (L-60).
func TestB360_HealthySinceUsesTheTransitionLog(t *testing.T) {
	d := b360Open(t)
	b360Relay(t, d, "karolina", "online", b360Base-86400)
	b360Relay(t, d, "karolina", "offline", b360Base-7200)
	b360Relay(t, d, "karolina", "online", b360Base-3600)
	// The snapshot says "checked 30 s ago", which is what the monitor writes.
	if err := skygatedb.UpsertExitNodeHealth(d, skygatedb.ExitNodeHealth{
		NodeID: "karolina", Hostname: "karolina", Online: true, State: "online", Healthy: true,
		AdvertisedRoutesOK: true, HasExitTag: true,
		LastCheckAt:       time.Unix(b360Base-30, 0).UTC(),
		LastStateChangeAt: time.Unix(b360Base-30, 0).UTC(),
	}); err != nil {
		t.Fatalf("upsert snapshot: %v", err)
	}
	since := HealthySince(d, b360Base)
	if since["karolina"] != b360Base-3600 {
		t.Errorf("HealthySince = %d, want %d (the transition log, not the per-tick snapshot stamp)",
			since["karolina"], b360Base-3600)
	}
	// A relay with no transition row at all falls back to the snapshot stamp; it is
	// a "last check" value, so the planner treats it as NOT sustained rather than
	// returning on faith.
	b360Relay(t, d, "emilia", "online", b360Base-30)
	if _, err := d.Exec(`DELETE FROM exit_node_state_changes WHERE hostname = $1`, "emilia"); err != nil {
		t.Fatalf("delete transitions: %v", err)
	}
	if got := HealthySince(d, b360Base)["emilia"]; got != b360Base-30 {
		t.Errorf("emilia since = %d, want the snapshot fallback %d", got, b360Base-30)
	}
}

// TestB360_NoSustainedInputBlocksEveryReturn is the contract the block's check
// script pins: a return can NEVER happen without the hysteresis input. An empty
// (or missing) "healthy since" map means "no evidence", and no evidence means the
// prefix stays where it is.
func TestB360_NoSustainedInputBlocksEveryReturn(t *testing.T) {
	claims := []Claim{{Prefix: b360Prefix, ExitNode: "karolina", DeviceID: 9}}
	assignments := []Assignment{{
		Prefix: b360Prefix, ExitNode: "karolina", Source: "explicit",
		FailoverFrom: "karolina", FailoverAt: b360Base,
	}}
	existing := []Existing{{
		Prefix: b360Prefix, ExitNode: "emilia", Source: "auto",
		FailoverFrom: "karolina", FailoverAt: b360Base,
	}}
	cfg := DefaultReservationConfig()

	// No healthy-since input at all: healthy and proven is not enough.
	returns, kept := PlanReturns(claims, assignments, existing,
		[]string{"karolina", "emilia"}, map[string]bool{"karolina": true},
		nil, "", false, b360Base+86400, cfg)
	if len(returns) != 0 {
		t.Fatalf("a return happened with NO hysteresis input: %v", returns)
	}
	if len(kept) != 1 || kept[0].Reason != ReasonTooSoon {
		t.Fatalf("kept = %+v, want one %s", kept, ReasonTooSoon)
	}
	// Control: the same inputs WITH the input return.
	returns, kept = PlanReturns(claims, assignments, existing,
		[]string{"karolina", "emilia"}, map[string]bool{"karolina": true},
		map[string]int64{"karolina": b360Base}, "", false, b360Base+86400, cfg)
	if len(returns) != 1 || len(kept) != 0 {
		t.Fatalf("with a sustained input: returns=%v kept=%v, want one return", returns, kept)
	}
}

// TestB360_RequiredHysteresisDoublesAndIsCapped pins the anti-flap arithmetic.
func TestB360_RequiredHysteresisDoublesAndIsCapped(t *testing.T) {
	cfg := DefaultReservationConfig()
	for _, tc := range []struct {
		flaps int
		want  time.Duration
	}{
		{0, 10 * time.Minute},
		{1, 20 * time.Minute},
		{2, 40 * time.Minute},
		{3, 80 * time.Minute},
		{4, 160 * time.Minute},
		{5, 320 * time.Minute},
		{6, 6 * time.Hour}, // 640 min would exceed the cap
		{20, 6 * time.Hour},
	} {
		if got := requiredHysteresis(tc.flaps, cfg); got != tc.want {
			t.Errorf("requiredHysteresis(%d) = %s, want %s", tc.flaps, got, tc.want)
		}
	}
	// A zero ReservationConfig must never mean "return immediately".
	if got := requiredHysteresis(0, ReservationConfig{}); got != DefaultHysteresis {
		t.Errorf("an unset config gave a %s window, want the default %s", got, DefaultHysteresis)
	}
}

// TestB360_StreakResetsAfterAQuarantine: the streak is about CONSECUTIVE quick
// flaps. A relay that held the prefix for longer than the quarantine before
// failing again starts from zero, otherwise a relay would accumulate a penalty
// for its whole life.
func TestB360_StreakResetsAfterAQuarantine(t *testing.T) {
	returned := Existing{Prefix: b360Prefix, ExitNode: "karolina", FailoverAt: b360Base, FailoverFlaps: 3}
	quick := nextFlapStreak(returned, b360Base+600, DefaultQuarantine)
	if quick != 4 {
		t.Errorf("nextFlapStreak inside the quarantine = %d, want 4", quick)
	}
	slow := nextFlapStreak(returned, b360Base+int64(2*DefaultQuarantine.Seconds()), DefaultQuarantine)
	if slow != 0 {
		t.Errorf("nextFlapStreak after the quarantine = %d, want 0 (the streak is over)", slow)
	}
	// An ACTIVE reservation is not a return cycle: a second relay cascading the
	// prefix elsewhere must not inflate the streak.
	active := Existing{Prefix: b360Prefix, ExitNode: "emilia", FailoverFrom: "karolina",
		FailoverAt: b360Base, FailoverFlaps: 2}
	if got := nextFlapStreak(active, b360Base+60, DefaultQuarantine); got != 2 {
		t.Errorf("nextFlapStreak on an active reservation = %d, want the streak unchanged (2)", got)
	}
}

// TestB360_ReturnBookkeepingClosesWhenThePrefixIsAlreadyHome: when the substitute
// disappears while a reservation is pending, the natural decision IS the reserved
// relay. The prefix is home; only the bookkeeping is stale, and stale bookkeeping
// is what would hide the next quick flap.
func TestB360_ReturnBookkeepingClosesWhenThePrefixIsAlreadyHome(t *testing.T) {
	claims := []Claim{{Prefix: b360Prefix, ExitNode: "karolina", DeviceID: 9}}
	prev := []Existing{{
		Prefix: b360Prefix, ExitNode: "karolina", Source: "explicit",
		FailoverFrom: "karolina", FailoverAt: b360Base, FailoverFlaps: 2,
	}}
	got := AssignWithReservations(claims, []string{"karolina"}, prev, nil,
		b360Base+300, DefaultReservationConfig())
	if len(got) != 1 || got[0].ExitNode != "karolina" {
		t.Fatalf("assignments = %+v, want karolina", got)
	}
	if got[0].FailoverFrom != "" {
		t.Fatalf("the prefix is already on the reserved relay but the reservation is still active: %+v", got[0])
	}
	if got[0].FailoverAt != b360Base+300 || got[0].FailoverFlaps != 2 {
		t.Errorf("row = %+v, want the return stamped at %d with the streak kept (2)", got[0], b360Base+300)
	}
}

// TestB360_ProvenRelaysOnlyAcceptsARecordedSuccess pins the B309 record parsing:
// a failure, a zero timestamp or garbage is not evidence that skygate can
// configure the relay.
func TestB360_ProvenRelaysOnlyAcceptsARecordedSuccess(t *testing.T) {
	d := b360Open(t)
	for key, value := range map[string]string{
		"relay_apply_state:karolina":    fmt.Sprintf("%d|ok", b360Base),
		"relay_apply_state:emilia":      fmt.Sprintf("%d|err|i/o timeout", b360Base),
		"relay_apply_state:sharlotta":   "0|ok",
		"relay_apply_state:svyatoslava": "garbage",
	} {
		if err := skygatedb.SetGlobalSetting(d, key, value); err != nil {
			t.Fatalf("seed %s: %v", key, err)
		}
	}
	proven := ProvenRelays(d)
	if !proven["karolina"] {
		t.Errorf("karolina must be proven: %v", proven)
	}
	for _, relay := range []string{"emilia", "sharlotta", "svyatoslava"} {
		if proven[relay] {
			t.Errorf("%s must NOT be proven (value did not record a success): %v", relay, proven)
		}
	}
}

// TestB360_ReservationOutlivesTheReasonIsCleared is the L-60 half of the recording
// rule: a prefix whose previous owner is HEALTHY and which moves anyway is a
// genuine decision change, so a stale reservation is cleared instead of being
// carried into the next operator's reading of the table.
func TestB360_ReservationOutlivesTheReasonIsCleared(t *testing.T) {
	claims := []Claim{{Prefix: b360Prefix, ExitNode: "sharlotta", DeviceID: 9}}
	existing := []Existing{{
		Prefix: b360Prefix, ExitNode: "emilia", Source: "auto",
		FailoverFrom: "karolina", FailoverAt: b360Base,
	}}
	// emilia (the previous owner) is healthy; the rules now name sharlotta, which
	// is healthy too → the majority moved, and karolina has nothing to do with it.
	got := AssignWithReservations(claims, []string{"emilia", "sharlotta", "karolina"},
		existing, nil, b360Base+3600, DefaultReservationConfig())
	if len(got) != 1 {
		t.Fatalf("assignments = %+v, want one", got)
	}
	if got[0].ExitNode != "sharlotta" {
		t.Fatalf("owner = %q, want sharlotta", got[0].ExitNode)
	}
	if got[0].FailoverFrom != "" || got[0].FailoverAt != 0 || got[0].FailoverFlaps != 0 {
		t.Fatalf("a stale reservation survived a genuine decision change: %+v", got[0])
	}
}

// TestB360_RecordOnlyWhenTheOwnerWasUnhealthy pins the RECORDING rule from the
// other side: a prefix that stays on an unhealthy-then-recovered relay is not a
// failover, and a failover that cascades to a third relay keeps the ORIGINAL
// reservation rather than rewriting it to the intermediate relay.
func TestB360_RecordOnlyWhenTheOwnerWasUnhealthy(t *testing.T) {
	claims := []Claim{{Prefix: b360Prefix, DeviceID: 9}} // nobody named a relay
	prev := []Existing{{
		Prefix: b360Prefix, ExitNode: "emilia", Source: "auto",
		FailoverFrom: "karolina", FailoverAt: b360Base, FailoverFlaps: 1,
	}}
	// emilia also fails: the prefix cascades to sharlotta, but the reservation is
	// still karolina's and its streak is untouched.
	got := AssignWithReservations(claims, []string{"sharlotta"}, prev, nil,
		b360Base+600, DefaultReservationConfig())
	if len(got) != 1 || got[0].ExitNode != "sharlotta" {
		t.Fatalf("assignments = %+v, want sharlotta", got)
	}
	if got[0].FailoverFrom != "karolina" || got[0].FailoverFlaps != 1 || got[0].FailoverAt != b360Base {
		t.Fatalf("a cascade rewrote the reservation: %+v", got[0])
	}

	// Control: with no reservation active, a move away from an unhealthy owner
	// records one — and the quick-flap streak is computed from the return stamp.
	noRes := []Existing{{Prefix: b360Prefix, ExitNode: "emilia", Source: "auto", FailoverAt: b360Base, FailoverFlaps: 2}}
	got = AssignWithReservations(claims, []string{"sharlotta"}, noRes, nil,
		b360Base+60, DefaultReservationConfig())
	if got[0].FailoverFrom != "emilia" || got[0].FailoverAt != b360Base+60 {
		t.Fatalf("the failover was not recorded: %+v", got[0])
	}
	if got[0].FailoverFlaps != 3 {
		t.Errorf("flaps = %d, want 3 (a return followed by a failover inside the quarantine window)", got[0].FailoverFlaps)
	}
}
