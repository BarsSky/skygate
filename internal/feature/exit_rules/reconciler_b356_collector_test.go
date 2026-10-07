// reconciler_b356_collector_test.go — B356 (2026-10-07).
//
// The pure planner tests (reconciler_b356_test.go) pin the DECISION. These pin the
// PASS: the subject set it walks, the health snapshot it reads, and what actually
// lands in the database, because the live defect was not a wrong decision — it was
// that the reconciler never looked at the row at all.
//
// THE LIVE SHAPE, reproduced here:
//
//	device_exit_node_prefs   (1, 'skyworker', 'tag:dev-infra-karolina', set_by_user_id=0)
//	prefix_owner             21 prefixes, every one on 'emilia'
//	exit_node_health         karolina state=offline healthy=0, emilia state=online healthy=1
//
// Before B356 the pass built its subject list from `device_rules` only, so a
// (user, device) with no rules was never examined; and even when it was, "the stored
// value names a different relay than the owner" is not the same question as "the
// stored value names a relay that is DOWN" — with no owner to compare against
// (no rules, split owners, untagged owner) the row stayed where it was forever.
//
// The database is SQLite in a temp dir (the B341.1 pattern): these assertions are
// about logic, not about a dialect, and they must run in every environment.
package exit_rules

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	skygatedb "skygate/internal/db"
)

// b356DB opens a migrated SQLite database private to this test.
func b356DB(t *testing.T) *sql.DB {
	t.Helper()
	_, d, err := skygatedb.OpenWithDialect("sqlite:" + t.TempDir() + "/b356.db")
	if err != nil {
		t.Skipf("sqlite dialect unavailable: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := skygatedb.MigrateSQLite(d); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	return d
}

// seedB356 lays out the live tailnet: `skyadmin` with a device `skyworker`, a relay
// `emilia` that owns every prefix, a relay `karolina` that owns none, and a health
// snapshot in which karolina is offline.
func seedB356(t *testing.T, d *sql.DB, prefTag string, setByUserID int64) {
	t.Helper()
	mustExec := func(q string, args ...interface{}) {
		t.Helper()
		if _, err := d.Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	mustExec(`INSERT INTO portal_users (id, username, password_hash, is_admin) VALUES (1, 'skyadmin', 'x', 1)`)
	// The device carries the tag headscale actually applies; both relays carry their
	// per-node tags, so nothing can be skipped for a missing tag.
	mustExec(`INSERT INTO node_owner_map (node_id, headscale_user_id, username, tag, hostname) VALUES
	          ('56', 0, 'tagged-devices', 'tag:dev-skyadmin-skyworker', 'skyworker'),
	          ('3',  0, 'tagged-devices', 'tag:dev-infra-emilia',      'emilia'),
	          ('9',  0, 'tagged-devices', 'tag:dev-infra-karolina',    'karolina')`)
	// emilia owns every prefix the device's rules cover; karolina owns nothing.
	mustExec(`INSERT INTO prefix_owner (prefix, exit_node_id, source, claims, devices, updated_at) VALUES
	          ('142.250.0.0/15', 'emilia', 'explicit', 2, 2, 0),
	          ('172.217.0.0/16', 'emilia', 'explicit', 2, 2, 0)`)
	// The device's rules exist and name no relay (the B341 shape) — so the owner is
	// derivable and the preference has somewhere to go.
	mustExec(`INSERT INTO device_rules (user_id, device_id, device_hostname, user_name, exit_node_id, target_type, target_value, action, enabled) VALUES
	          (1, 56, 'skyworker', 'skyadmin', '', 'subnet', '142.250.0.0/15', 'accept', 1),
	          (1, 56, 'skyworker', 'skyadmin', '', 'subnet', '172.217.0.0/16', 'accept', 1)`)
	// The stale preference.
	mustExec(`INSERT INTO device_exit_node_prefs (user_id, device_hostname, exit_node_tag, set_by_user_id, updated_at, via_enabled)
	          VALUES (1, 'skyworker', $1, $2, 0, 1)`, prefTag, setByUserID)
	seedRelayHealth(t, d, "karolina", "offline", false)
	seedRelayHealth(t, d, "emilia", "online", true)
}

// seedRelayHealth writes one exit_node_health row (the monitor's snapshot).
func seedRelayHealth(t *testing.T, d *sql.DB, host, state string, healthy bool) {
	t.Helper()
	if err := skygatedb.UpsertExitNodeHealth(d, skygatedb.ExitNodeHealth{
		NodeID:             host,
		Hostname:           host,
		State:              state,
		Healthy:            healthy,
		AdvertisedRoutesOK: healthy,
	}); err != nil {
		t.Fatalf("seed exit_node_health %s: %v", host, err)
	}
}

// b356Notifier is a ReconcilerNotifier that records what the pass told the operator.
type b356Notifier struct{ alerts []string }

func (n *b356Notifier) SendAlert(text string) int64 {
	n.alerts = append(n.alerts, text)
	return 1
}

// prefTagOf reads the stored preference for (1, 'skyworker').
func prefTagOf(t *testing.T, d *sql.DB) string {
	t.Helper()
	var tag string
	if err := d.QueryRow(`SELECT exit_node_tag FROM device_exit_node_prefs WHERE user_id = 1 AND device_hostname = 'skyworker'`).Scan(&tag); err != nil {
		t.Fatalf("read stored preference: %v", err)
	}
	return tag
}

// changesWithReason filters a pass result by the named reason.
func changesWithReason(changes []ReconcilerChange, reason string) []ReconcilerChange {
	var out []ReconcilerChange
	for _, c := range changes {
		if c.Reason == reason {
			out = append(out, c)
		}
	}
	return out
}

// TestB356_PassRepointsStaleDerivedPref is the operator's report reproduced: a row
// the engine derived, naming a relay the monitor calls offline, must be re-pointed to
// the relay that owns the device's prefixes — with the named reason, an audit row and
// a notification.
func TestB356_PassRepointsStaleDerivedPref(t *testing.T) {
	ResetAlertThrottle()
	d := b356DB(t)
	seedB356(t, d, "tag:dev-infra-karolina", 0)
	s := &Service{DB: skygatedb.FixedDBSource{DB: d}}
	n := &b356Notifier{}

	changes, err := s.ReconcileDeviceExitNodePrefs(context.Background(), n)
	if err != nil {
		t.Fatalf("ReconcileDeviceExitNodePrefs: %v", err)
	}
	if got := changesWithReason(changes, StalePrefRelayUnusable); len(got) != 1 {
		t.Fatalf("changes with reason %s = %d, want 1 (all changes: %+v)", StalePrefRelayUnusable, len(got), changes)
	} else if got[0].Action != "update" || got[0].NewTag != "tag:dev-infra-emilia" {
		t.Fatalf("change = %s → %s, want update → tag:dev-infra-emilia", got[0].Action, got[0].NewTag)
	}
	if tag := prefTagOf(t, d); tag != "tag:dev-infra-emilia" {
		t.Errorf("stored preference = %q, want tag:dev-infra-emilia (the device must not stay pinned to a dead relay)", tag)
	}
	var auditDetail string
	if err := d.QueryRow(`SELECT detail FROM audit_log WHERE action = 'preferred_exit_reconciled' ORDER BY id DESC LIMIT 1`).Scan(&auditDetail); err != nil {
		t.Fatalf("read the audit row: %v", err)
	}
	for _, want := range []string{"UPDATE pref", "tag:dev-infra-karolina", "tag:dev-infra-emilia", StalePrefRelayUnusable} {
		if !strings.Contains(auditDetail, want) {
			t.Errorf("audit detail %q does not mention %q", auditDetail, want)
		}
	}
	if len(n.alerts) == 0 {
		t.Fatal("the operator was not notified — the report was exactly that nothing said so")
	}
}

// TestB356_PassNeverRewritesAnOperatorPref is item 2 of the report as a PASS-level
// property: an operator's own pin must survive the reconciler byte for byte, and the
// pass must still say what it refused to do (audit + notification).
func TestB356_PassNeverRewritesAnOperatorPref(t *testing.T) {
	ResetAlertThrottle()
	d := b356DB(t)
	seedB356(t, d, "tag:dev-infra-karolina", 7) // set_by_user_id = 7 = a human
	s := &Service{DB: skygatedb.FixedDBSource{DB: d}}
	n := &b356Notifier{}

	changes, err := s.ReconcileDeviceExitNodePrefs(context.Background(), n)
	if err != nil {
		t.Fatalf("ReconcileDeviceExitNodePrefs: %v", err)
	}
	if got := changesWithReason(changes, "stale-pref-human-pinned"); len(got) != 1 {
		t.Fatalf("changes with reason stale-pref-human-pinned = %d, want 1 (all changes: %+v)", len(got), changes)
	}
	if tag := prefTagOf(t, d); tag != "tag:dev-infra-karolina" {
		t.Errorf("stored preference = %q, want it UNCHANGED — a human's choice is not the engine's to rewrite", tag)
	}
	var nAudit int
	// The audit trail must carry the refusal: the engine acted (logged, audited,
	// notified) and its decision was "do not touch this row".
	if err := d.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'preferred_exit_reconciled' AND detail LIKE 'STALE-PREF%'`).Scan(&nAudit); err != nil {
		t.Fatalf("count stale-pref audit rows: %v", err)
	}
	if nAudit != 1 {
		t.Errorf("STALE-PREF audit rows = %d, want 1 — an ignored human pin must be visible, not silent", nAudit)
	}
	if len(n.alerts) == 0 {
		t.Fatal("the operator was not notified about their own pin to a dead relay")
	}
}

// TestB356_PassIsIdempotent: the pass runs on the maintenance tick (L-60), so a
// converged host must produce nothing on the second run — no second update, no second
// audit row, no second notification.
func TestB356_PassIsIdempotent(t *testing.T) {
	ResetAlertThrottle()
	d := b356DB(t)
	seedB356(t, d, "tag:dev-infra-karolina", 0)
	s := &Service{DB: skygatedb.FixedDBSource{DB: d}}
	n := &b356Notifier{}

	if _, err := s.ReconcileDeviceExitNodePrefs(context.Background(), n); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	firstAudit := countPrefAudit(t, d)

	// A second pass (a new process would also have an empty alert throttle; reset it
	// so the assertion is about the DECISION, not about the rate limiter).
	ResetAlertThrottle()
	changes, err := s.ReconcileDeviceExitNodePrefs(context.Background(), n)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if got := changesWithReason(changes, StalePrefRelayUnusable); len(got) != 0 {
		t.Fatalf("the second pass produced another repair (%+v) — the pass does not converge", got)
	}
	if tag := prefTagOf(t, d); tag != "tag:dev-infra-emilia" {
		t.Errorf("stored preference = %q after the second pass, want tag:dev-infra-emilia", tag)
	}
	if after := countPrefAudit(t, d); after != firstAudit {
		t.Errorf("audit rows grew from %d to %d on a converged pass — the journal would fill", firstAudit, after)
	}
}

// TestB356_HealthyOwnerKeepsItsPref is item 3 of the report: a device whose preference
// names the usable owner must take the pre-B356 path untouched (the working devices
// `a71` and `cyborg` are pinned to emilia).
func TestB356_HealthyOwnerKeepsItsPref(t *testing.T) {
	ResetAlertThrottle()
	d := b356DB(t)
	seedB356(t, d, "tag:dev-infra-emilia", 0)
	s := &Service{DB: skygatedb.FixedDBSource{DB: d}}

	changes, err := s.ReconcileDeviceExitNodePrefs(context.Background(), &b356Notifier{})
	if err != nil {
		t.Fatalf("ReconcileDeviceExitNodePrefs: %v", err)
	}
	if len(changesWithReason(changes, StalePrefRelayUnusable)) != 0 {
		t.Fatalf("a preference naming the usable owner was reported stale: %+v", changes)
	}
	if tag := prefTagOf(t, d); tag != "tag:dev-infra-emilia" {
		t.Errorf("stored preference = %q, want it untouched", tag)
	}
}

// TestB356_SubjectSetIncludesPrefRowsWithoutRules is the L-54 half, at the level where
// it actually bit: the pass used to build its subjects from `device_rules`, so a device
// whose rules are gone (or were never there) was never examined — while the B265
// per-device grant still pins ALL of its internet traffic. The row must be seen.
func TestB356_SubjectSetIncludesPrefRowsWithoutRules(t *testing.T) {
	ResetAlertThrottle()
	d := b356DB(t)
	seedB356(t, d, "tag:dev-infra-karolina", 0)
	// Remove every rule: the device keeps its preference and its B265 pin, and its
	// prefixes leave prefix_owner (nothing claims them any more).
	if _, err := d.Exec(`DELETE FROM device_rules`); err != nil {
		t.Fatalf("delete rules: %v", err)
	}
	s := &Service{DB: skygatedb.FixedDBSource{DB: d}}

	changes, err := s.ReconcileDeviceExitNodePrefs(context.Background(), &b356Notifier{})
	if err != nil {
		t.Fatalf("ReconcileDeviceExitNodePrefs: %v", err)
	}
	// There is no owner to move the row to, so it is a NAMED skip — but it is SEEN.
	if got := changesWithReason(changes, "stale-pref-no-owner"); len(got) != 1 {
		t.Fatalf("a preference row with no rules was not examined (changes: %+v)", changes)
	}
	if tag := prefTagOf(t, d); tag != "tag:dev-infra-karolina" {
		t.Errorf("stored preference = %q, want it kept — with no owner there is nothing to re-point to, and a guess is worse", tag)
	}
}

// TestB356_UnknownHealthDoesNotDriveTheRepair: a relay with no health row has never
// been measured. The B356 branch must not fire, and the row is then judged by the
// pre-existing B345 rule (the owner overrides a derived row) — never by a guess about
// a relay nobody looked at.
func TestB356_UnknownHealthDoesNotDriveTheRepair(t *testing.T) {
	ResetAlertThrottle()
	d := b356DB(t)
	seedB356(t, d, "tag:dev-infra-karolina", 0)
	if _, err := d.Exec(`DELETE FROM exit_node_health WHERE hostname = 'karolina'`); err != nil {
		t.Fatalf("delete karolina health row: %v", err)
	}
	s := &Service{DB: skygatedb.FixedDBSource{DB: d}}

	changes, err := s.ReconcileDeviceExitNodePrefs(context.Background(), &b356Notifier{})
	if err != nil {
		t.Fatalf("ReconcileDeviceExitNodePrefs: %v", err)
	}
	if len(changesWithReason(changes, StalePrefRelayUnusable)) != 0 {
		t.Fatalf("a relay with NO health row was treated as broken: %+v", changes)
	}
	if got := changesWithReason(changes, "owner-overrides-rule-relay"); len(got) != 1 {
		t.Fatalf("the B345 rule must still apply when the owner is known (changes: %+v)", changes)
	}
}

// countPrefAudit counts the reconciler's audit rows.
func countPrefAudit(t *testing.T, d *sql.DB) int {
	t.Helper()
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'preferred_exit_reconciled'`).Scan(&n); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	return n
}
