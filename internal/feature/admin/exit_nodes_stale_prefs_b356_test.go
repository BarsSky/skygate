// exit_nodes_stale_prefs_b356_test.go — B356 (2026-10-07).
//
// The page half of the block. The reconciler repairs a stale DERIVED preference and
// deliberately refuses to touch a HUMAN one — so for a human pin the page is the only
// place the consequence is visible, and the operator's report was precisely that
// nothing said so ("no page or log said so").
//
// These tests pin the rows `loadStaleExitPrefs` hands to the template:
//
//	(1) a derived preference on an unusable relay is shown, with the relay's measured
//	    state and the relay the data plane chose;
//	(2) the same row set by a HUMAN is marked as such (the engine will not fix it);
//	(3) a preference naming the usable owner produces NO row — the page must not warn
//	    about devices that work;
//	(4) the human/derived count the banner renders is the count of human rows.
package admin

import (
	"database/sql"
	"testing"

	skygatedb "skygate/internal/db"
)

func b356AdminDB(t *testing.T) *sql.DB {
	t.Helper()
	_, d, err := skygatedb.OpenWithDialect("sqlite:" + t.TempDir() + "/b356admin.db")
	if err != nil {
		t.Skipf("sqlite dialect unavailable: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := skygatedb.MigrateSQLite(d); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	return d
}

// seedB356Admin writes the live shape: `skyworker` is pinned to `karolina`, which owns
// nothing and whose health row says offline, while `emilia` owns both of the device's
// prefixes.
func seedB356Admin(t *testing.T, d *sql.DB, prefTag string, setByUserID int64) {
	t.Helper()
	mustExec := func(q string, args ...interface{}) {
		t.Helper()
		if _, err := d.Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	mustExec(`INSERT INTO portal_users (id, username, password_hash, is_admin) VALUES (1, 'skyadmin', 'x', 1)`)
	mustExec(`INSERT INTO node_owner_map (node_id, headscale_user_id, username, tag, hostname) VALUES
	          ('56', 0, 'tagged-devices', 'tag:dev-skyadmin-skyworker', 'skyworker'),
	          ('3',  0, 'tagged-devices', 'tag:dev-infra-emilia',      'emilia'),
	          ('9',  0, 'tagged-devices', 'tag:dev-infra-karolina',    'karolina')`)
	mustExec(`INSERT INTO prefix_owner (prefix, exit_node_id, source, claims, devices, updated_at) VALUES
	          ('142.250.0.0/15', 'emilia', 'explicit', 2, 2, 0),
	          ('172.217.0.0/16', 'emilia', 'explicit', 2, 2, 0)`)
	mustExec(`INSERT INTO device_rules (user_id, device_id, device_hostname, user_name, exit_node_id, target_type, target_value, action, enabled) VALUES
	          (1, 56, 'skyworker', 'skyadmin', '', 'subnet', '142.250.0.0/15', 'accept', 1),
	          (1, 56, 'skyworker', 'skyadmin', '', 'subnet', '172.217.0.0/16', 'accept', 1)`)
	mustExec(`INSERT INTO device_exit_node_prefs (user_id, device_hostname, exit_node_tag, set_by_user_id, updated_at, via_enabled)
	          VALUES (1, 'skyworker', $1, $2, 0, 1)`, prefTag, setByUserID)
	for _, h := range []struct {
		host    string
		state   string
		healthy bool
	}{{"karolina", "offline", false}, {"emilia", "online", true}} {
		if err := skygatedb.UpsertExitNodeHealth(d, skygatedb.ExitNodeHealth{
			NodeID: h.host, Hostname: h.host, State: h.state, Healthy: h.healthy, AdvertisedRoutesOK: h.healthy,
		}); err != nil {
			t.Fatalf("seed health %s: %v", h.host, err)
		}
	}
}

// TestB356Page_ShowsStaleDerivedPref: the operator's devices `basic` and `skyworker`
// were exactly this shape, and no page named them.
func TestB356Page_ShowsStaleDerivedPref(t *testing.T) {
	d := b356AdminDB(t)
	seedB356Admin(t, d, "tag:dev-infra-karolina", 0)
	s := &Service{DB: skygatedb.FixedDBSource{DB: d}}

	rows := s.loadStaleExitPrefs()
	if len(rows) != 1 {
		t.Fatalf("rows = %d (%+v), want 1 — the device pinned to an offline relay must be visible", len(rows), rows)
	}
	r := rows[0]
	if r.DeviceHostname != "skyworker" || r.Username != "skyadmin" {
		t.Errorf("row = %s/%s, want skyadmin/skyworker", r.Username, r.DeviceHostname)
	}
	if r.PrefTag != "tag:dev-infra-karolina" {
		t.Errorf("PrefTag = %q, want the stored value", r.PrefTag)
	}
	if r.RelayState != "offline" || !r.RelayKnown {
		t.Errorf("RelayState = %q known=%v, want offline/true — the page must name what the monitor measured", r.RelayState, r.RelayKnown)
	}
	if r.Cause != "unusable" {
		t.Errorf("Cause = %q, want unusable", r.Cause)
	}
	if r.HumanPinned {
		t.Error("HumanPinned = true for a row with set_by_user_id = 0 (the engine derived it)")
	}
	if r.CandidateTag != "tag:dev-infra-emilia" {
		t.Errorf("CandidateTag = %q, want tag:dev-infra-emilia (where the data plane points)", r.CandidateTag)
	}
	if countHumanStalePrefs(rows) != 0 {
		t.Errorf("countHumanStalePrefs = %d, want 0", countHumanStalePrefs(rows))
	}
}

// TestB356Page_MarksAnOperatorPin: for a human row the page is the ONLY surface, since
// the reconciler refuses to act. It must say so.
func TestB356Page_MarksAnOperatorPin(t *testing.T) {
	d := b356AdminDB(t)
	seedB356Admin(t, d, "tag:dev-infra-karolina", 7)
	s := &Service{DB: skygatedb.FixedDBSource{DB: d}}

	rows := s.loadStaleExitPrefs()
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if !rows[0].HumanPinned || rows[0].SetByUserID != 7 {
		t.Errorf("HumanPinned=%v SetByUserID=%d, want true/7", rows[0].HumanPinned, rows[0].SetByUserID)
	}
	if got := countHumanStalePrefs(rows); got != 1 {
		t.Errorf("countHumanStalePrefs = %d, want 1", got)
	}
}

// TestB356Page_SilentForAWorkingDevice is the "do not cry wolf" half: the devices that
// work today (`a71`, `cyborg` — pinned to emilia, which owns the prefixes) must produce
// no banner at all. A page that warns about healthy devices is a page nobody reads.
func TestB356Page_SilentForAWorkingDevice(t *testing.T) {
	d := b356AdminDB(t)
	seedB356Admin(t, d, "tag:dev-infra-emilia", 1)
	s := &Service{DB: skygatedb.FixedDBSource{DB: d}}

	if rows := s.loadStaleExitPrefs(); len(rows) != 0 {
		t.Fatalf("rows = %d (%+v), want 0 — a preference naming the usable owner is not stale", len(rows), rows)
	}
}

// TestB356Page_ShowsTheOwnerDisagreement: the relay is UP and serves prefixes, but the
// data plane gives THIS DEVICE's prefixes to another relay. The row is still stale (the
// device's per-CIDR pins name emilia while the device itself is pinned to karolina), and
// the page has to say the cause is the owner, not the relay's health.
//
// SETUP WIDENED 2026-10-07 (B361) — the property is unchanged, the fixture is now
// honest about which case it describes. The old fixture left karolina owning ZERO rows
// in `prefix_owner`, and the old predicate could not tell that apart from "the owner
// disagrees", because it was built from the device's owners only. B361 asks the sharper
// question first (does the relay own ANYTHING?), so "karolina owns nothing" is now its
// own named case (`stale-pref-relay-owns-nothing` — the operator's a71 shape) and the
// owner-disagreement case needs a relay that really does serve: karolina owns one
// prefix, just not the two this device's rules claim. That is the distinction the
// operator needs in order to act, and the two cases now render differently.
func TestB356Page_ShowsTheOwnerDisagreement(t *testing.T) {
	d := b356AdminDB(t)
	seedB356Admin(t, d, "tag:dev-infra-karolina", 0)
	// karolina recovers AND serves something — merely not this device's prefixes.
	if _, err := d.Exec(`INSERT INTO prefix_owner (prefix, exit_node_id, source, claims, devices, updated_at)
	                     VALUES ('203.0.113.0/24', 'karolina', 'explicit', 1, 1, 0)`); err != nil {
		t.Fatalf("seed karolina's own prefix: %v", err)
	}
	if err := skygatedb.UpsertExitNodeHealth(d, skygatedb.ExitNodeHealth{
		NodeID: "karolina", Hostname: "karolina", State: "online", Healthy: true, AdvertisedRoutesOK: true,
	}); err != nil {
		t.Fatalf("heal karolina: %v", err)
	}
	s := &Service{DB: skygatedb.FixedDBSource{DB: d}}

	rows := s.loadStaleExitPrefs()
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1 (the owner disagrees with the preference)", len(rows))
	}
	if rows[0].Cause != "owner" {
		t.Errorf("Cause = %q, want owner (the relay is healthy and serves prefixes; the ASSIGNMENT for THIS device disagrees)", rows[0].Cause)
	}
	if rows[0].RelayState != "online" {
		t.Errorf("RelayState = %q, want online", rows[0].RelayState)
	}
	// B361: and the one-click fix names the relay that owns this device's routing.
	if rows[0].CandidateTag != "tag:dev-infra-emilia" {
		t.Errorf("CandidateTag = %q, want tag:dev-infra-emilia (where the data plane points)", rows[0].CandidateTag)
	}
}

// TestB361Page_ShowsARelayThatOwnsNothing is the a71 case on the page: a device with NO
// rules pinned to a relay that owns ZERO prefixes. The health predicate has nothing to
// say (the relay is online) and the owner comparison has nothing to compare (there are
// no rules), which is exactly why the operator saw no state at all.
func TestB361Page_ShowsARelayThatOwnsNothing(t *testing.T) {
	d := b356AdminDB(t)
	// The preference names the relay that owns NOTHING; the two seeded prefixes belong
	// to emilia. (Seeding it the other way round would make the fixture describe a
	// device that is actually fine — `servesNothing` is false when the relay owns
	// prefix_owner rows.)
	seedB356Admin(t, d, "tag:dev-infra-karolina", 1)
	// The device loses its rules; the preference stays (the live a71 shape).
	if _, err := d.Exec(`DELETE FROM device_rules`); err != nil {
		t.Fatalf("delete rules: %v", err)
	}
	// Only emilia owns prefixes; the pinned karolina owns none.
	if _, err := d.Exec(`INSERT INTO prefix_owner (prefix, exit_node_id, source, claims, devices, updated_at)
	                     VALUES ('198.51.100.0/24', 'emilia', 'explicit', 1, 1, 0)`); err != nil {
		t.Fatalf("seed emilia's prefix: %v", err)
	}
	if err := skygatedb.UpsertExitNodeHealth(d, skygatedb.ExitNodeHealth{
		NodeID: "karolina", Hostname: "karolina", State: "online", Healthy: true, AdvertisedRoutesOK: true,
	}); err != nil {
		t.Fatalf("heal karolina: %v", err)
	}
	s := &Service{DB: skygatedb.FixedDBSource{DB: d}}

	rows := s.loadStaleExitPrefs()
	if len(rows) != 1 {
		t.Fatalf("rows = %d (%+v), want 1 — the a71 shape must be visible", len(rows), rows)
	}
	r := rows[0]
	if r.Cause != "no-rules" || r.Reason != "stale-pref-no-rules" {
		t.Errorf("Cause/Reason = %q/%q, want no-rules/stale-pref-no-rules", r.Cause, r.Reason)
	}
	if r.Rules != 0 {
		t.Errorf("Rules = %d, want 0 (the device has none — that is the whole point)", r.Rules)
	}
	if r.PrefRelayPrefixes != 0 {
		t.Errorf("PrefRelayPrefixes = %d, want 0 (the pinned relay serves nothing)", r.PrefRelayPrefixes)
	}
	if r.AlternativeRelay != "emilia" || r.AlternativePrefixes == 0 {
		t.Errorf("AlternativeRelay = %q (%d prefixes), want emilia with a non-zero count — the page must name who DOES serve the routing",
			r.AlternativeRelay, r.AlternativePrefixes)
	}
	if r.CandidateTag != "tag:dev-infra-emilia" {
		t.Errorf("CandidateTag = %q, want tag:dev-infra-emilia (the relay that serves most of the routing)", r.CandidateTag)
	}
	if !r.HumanPinned || r.SetByUserID != 1 {
		t.Errorf("HumanPinned=%v SetByUserID=%d, want true/1 (the stored row is a human's and must stay untouched)", r.HumanPinned, r.SetByUserID)
	}
}
