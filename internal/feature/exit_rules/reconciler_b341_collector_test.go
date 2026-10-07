// reconciler_b341_collector_test.go — B341.1 (2026-10-02, measured live).
//
// THE DEFECT THESE TESTS PIN. B341 taught the planner to derive a device's exit
// node from `prefix_owner` when the device's rules name NO relay — but the
// condition it keys on (`DistinctExitNodes == 0`) was computed by a loop that
// counted the SQL GROUP BY's EMPTY group as a relay. A device whose rules name no
// relay produces exactly one group, `(”, 11)`, so the collector reported
// `DistinctExitNodes == 1` and the derive branch was UNREACHABLE in production
// while the unit test — which hand-built the state with `DistinctExitNodes: 0` —
// passed. The live VM said it plainly, one tick after the B341 build started:
//
//	preferred-reconciler: SKIP skyadmin/cyborg —
//	reason=missing-pref-relay-untagged rules=11 distinct_relays=0 most=""
//
// `cyborg` had 11 youtube rules, every one with `exit_node_id = ”`, no
// `device_exit_node_prefs` row, and the prefixes' owner (`emilia`) sitting in
// `prefix_owner` the whole time — i.e. exactly the case B341 was written for,
// reported as a different one.
//
// These tests drive the COLLECTOR (not just the pure planner) against a migrated
// database seeded with the live shape, so "the state the DB produces" is what is
// asserted. The query itself is `$N` + `CAST(device_id AS TEXT)` and is exercised
// on PostgreSQL by internal/db's B341 tests; the logic below is backend-agnostic.
package exit_rules

import (
	"context"
	"database/sql"
	"testing"

	skygatedb "skygate/internal/db"
)

func b3411DB(t *testing.T) *sql.DB {
	t.Helper()
	_, d, err := skygatedb.OpenWithDialect("sqlite:" + t.TempDir() + "/b3411.db")
	if err != nil {
		t.Skipf("sqlite dialect unavailable: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := skygatedb.MigrateSQLite(d); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	return d
}

// seedB3411 writes the measured live shape of the `cyborg` case: an owner in
// `prefix_owner` for every prefix the rules cover, and rules that name no relay.
func seedB3411(t *testing.T, d *sql.DB) {
	t.Helper()
	mustExec := func(q string, args ...interface{}) {
		t.Helper()
		if _, err := d.Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	// device_rules.user_id has a real FK to portal_users.
	mustExec(`INSERT INTO portal_users (id, username, password_hash, is_admin) VALUES (1, 'skyadmin', 'x', 0) ON CONFLICT (id) DO NOTHING`)
	// The live tailnet: cyborg is node 56 (synthetic owner), emilia is the relay
	// and carries the per-node tag the preference must be written with.
	mustExec(`INSERT INTO node_owner_map (node_id, headscale_user_id, username, tag, hostname) VALUES
	          ('56', 0, 'tagged-devices', 'tag:dev-skyadmin-cyborg', 'cyborg'),
	          ('3',  0, 'tagged-devices', 'tag:dev-infra-emilia',    'emilia'),
	          ('9',  0, 'tagged-devices', 'tag:dev-skyadmin-skyworker', 'skyworker')`)
	// Every youtube prefix the CDN expansion produced is owned by emilia — one
	// owner, which is what makes the derivation possible (B275).
	mustExec(`INSERT INTO prefix_owner (prefix, exit_node_id, source, claims, devices, updated_at) VALUES
	          ('142.250.0.0/15',  'emilia', 'explicit', 3, 3, 0),
	          ('172.217.0.0/16',  'emilia', 'explicit', 3, 3, 0),
	          ('173.194.0.0/16',  'emilia', 'explicit', 3, 3, 0),
	          ('216.58.192.0/19', 'emilia', 'explicit', 3, 3, 0),
	          ('74.125.0.0/16',   'emilia', 'explicit', 3, 3, 0),
	          ('8.15.202.0/24',   'emilia', 'explicit', 3, 3, 0),
	          ('8.34.208.0/20',   'emilia', 'explicit', 3, 3, 0),
	          ('8.35.192.0/20',   'emilia', 'explicit', 3, 3, 0),
	          ('8.8.8.0/24',      'emilia', 'explicit', 3, 3, 0),
	          ('8.8.4.0/24',      'emilia', 'explicit', 3, 3, 0)`)
	// cyborg's 11 rules, EVERY one with an empty relay (10 subnets + the domain
	// row the CDN expansion came from).
	mustExec(`INSERT INTO device_rules (user_id, device_id, device_hostname, user_name, exit_node_id, target_type, target_value, action, enabled) VALUES
	          (1, 56, 'cyborg', 'skyadmin', '', 'subnet', '142.250.0.0/15',  'accept', 1),
	          (1, 56, 'cyborg', 'skyadmin', '', 'subnet', '172.217.0.0/16',  'accept', 1),
	          (1, 56, 'cyborg', 'skyadmin', '', 'subnet', '173.194.0.0/16',  'accept', 1),
	          (1, 56, 'cyborg', 'skyadmin', '', 'subnet', '216.58.192.0/19', 'accept', 1),
	          (1, 56, 'cyborg', 'skyadmin', '', 'subnet', '74.125.0.0/16',   'accept', 1),
	          (1, 56, 'cyborg', 'skyadmin', '', 'subnet', '8.15.202.0/24',   'accept', 1),
	          (1, 56, 'cyborg', 'skyadmin', '', 'subnet', '8.34.208.0/20',   'accept', 1),
	          (1, 56, 'cyborg', 'skyadmin', '', 'subnet', '8.35.192.0/20',   'accept', 1),
	          (1, 56, 'cyborg', 'skyadmin', '', 'subnet', '8.8.8.0/24',      'accept', 1),
	          (1, 56, 'cyborg', 'skyadmin', '', 'subnet', '8.8.4.0/24',      'accept', 1),
	          (1, 56, 'cyborg', 'skyadmin', '', 'domain', 'youtube.com',     'accept', 1)`)
}

// TestCollectDevicePrefState_NoRelayIsNotARelay_B341 is the regression pin: a
// device whose rules all carry an empty relay must report ZERO relays, and the
// planner must then derive the owner from `prefix_owner` instead of skipping.
func TestCollectDevicePrefState_NoRelayIsNotARelay_B341(t *testing.T) {
	d := b3411DB(t)
	seedB3411(t, d)
	s := &Service{DB: skygatedb.FixedDBSource{DB: d}}

	st, err := s.collectDevicePrefState(context.Background(), 1, "skyadmin", "cyborg", nil)
	if err != nil {
		t.Fatalf("collectDevicePrefState: %v", err)
	}
	if st.TotalRules != 11 {
		t.Errorf("TotalRules = %d, want 11 (the empty group is still RULES)", st.TotalRules)
	}
	if st.DistinctExitNodes != 0 {
		t.Fatalf("DistinctExitNodes = %d, want 0 — an empty exit_node_id is not a relay; "+
			"with 1 the B341 derive branch is unreachable (the live cyborg defect)", st.DistinctExitNodes)
	}
	if st.DominantExitHostname != "" {
		t.Errorf("DominantExitHostname = %q, want \"\" (no rule names a relay)", st.DominantExitHostname)
	}
	if st.OwnerDistinct != 1 {
		t.Fatalf("OwnerDistinct = %d, want 1 (emilia owns all ten prefixes)", st.OwnerDistinct)
	}
	if st.OwnerCanonicalTag != "tag:dev-infra-emilia" {
		t.Fatalf("OwnerCanonicalTag = %q, want tag:dev-infra-emilia (the owner's per-node tag, from node_owner_map)", st.OwnerCanonicalTag)
	}
	ch, ok := PlanDevicePrefChange(st)
	if !ok || ch == nil {
		t.Fatal("PlanDevicePrefChange = no change, want a create")
	}
	if ch.Action != "create" || ch.NewTag != "tag:dev-infra-emilia" || ch.Reason != "missing-pref-owner-derived" {
		t.Fatalf("plan = %s/%s (%s), want create/tag:dev-infra-emilia/missing-pref-owner-derived",
			ch.Action, ch.NewTag, ch.Reason)
	}
}

// TestCollectDevicePrefState_OwnerBeatsRuleRelay_B345 — the live cyborg case of
// 2026-10-03: the rules DO name a relay, but it is not the owner of the prefixes
// they cover.
//
// Measured: `cyborg` had 10 youtube subnets with «авто» plus a DUPLICATE
// `youtube.com` domain rule naming **karolina**, while `prefix_owner` said
// **emilia** owned all ten prefixes — and emilia was the only node advertising them
// (37 routes, all approved; karolina advertised none). The reconciler pinned the
// DEVICE to the relay the RULE named, so the preference said karolina while every
// per-CIDR ACL pin (and the actual route) said emilia. `via` is a permission
// FILTER, so the device's chosen exit node could not serve the destinations its own
// grants allowed: YouTube stayed unreachable however many rules were added.
//
// The planner must therefore take the OWNER, and say why.
func TestCollectDevicePrefState_OwnerBeatsRuleRelay_B345(t *testing.T) {
	d := b3411DB(t)
	seedB3411(t, d)
	if _, err := d.Exec(`INSERT INTO device_rules (user_id, device_id, device_hostname, user_name, exit_node_id, target_type, target_value, action, enabled) VALUES
	          (1, 56, 'cyborg', 'skyadmin', 'karolina', 'domain', 'youtube.com', 'accept', 1)`); err != nil {
		t.Fatalf("seed the karolina domain rule: %v", err)
	}
	// karolina's per-node tag exists, so nothing can be skipped for a missing tag:
	// the point is that the OWNER decides, not that a tag is absent.
	if _, err := d.Exec(`INSERT INTO node_owner_map (node_id, headscale_user_id, username, tag, hostname) VALUES ('11', 0, 'tagged-devices', 'tag:dev-infra-karolina', 'karolina')`); err != nil {
		t.Fatalf("seed karolina's owner row: %v", err)
	}
	s := &Service{DB: skygatedb.FixedDBSource{DB: d}}
	st, err := s.collectDevicePrefState(context.Background(), 1, "skyadmin", "cyborg", nil)
	if err != nil {
		t.Fatalf("collectDevicePrefState: %v", err)
	}
	if st.DistinctExitNodes != 1 || st.CanonicalTag != "tag:dev-infra-karolina" {
		t.Fatalf("the rules must be seen as naming karolina (got distinct=%d canonical=%q) — otherwise this test proves nothing",
			st.DistinctExitNodes, st.CanonicalTag)
	}
	if st.OwnerDistinct != 1 || st.OwnerCanonicalTag != "tag:dev-infra-emilia" {
		t.Fatalf("the owner must be computed even when the rules name a relay (got distinct=%d tag=%q)",
			st.OwnerDistinct, st.OwnerCanonicalTag)
	}
	ch, ok := PlanDevicePrefChange(st)
	if !ok || ch == nil {
		t.Fatal("expected a change (the owner must win)")
	}
	if ch.Action != "create" || ch.NewTag != "tag:dev-infra-emilia" || ch.Reason != "owner-overrides-rule-relay" {
		t.Fatalf("plan = %s/%s (%s), want create/tag:dev-infra-emilia/owner-overrides-rule-relay",
			ch.Action, ch.NewTag, ch.Reason)
	}
	// An EXISTING preference naming the non-owner is repaired the same way — that is
	// the state the live host was actually in.
	st.ExistingPrefTag = "tag:dev-infra-karolina"
	st.ExistingPrefVia = true
	ch, ok = PlanDevicePrefChange(st)
	if !ok || ch == nil || ch.Action != "update" || ch.NewTag != "tag:dev-infra-emilia" ||
		ch.Reason != "owner-overrides-rule-relay" {
		t.Fatalf("existing pref naming the non-owner: plan = %v, want update to tag:dev-infra-emilia (owner-overrides-rule-relay)", ch)
	}
}

// TestCollectDevicePrefState_RealRelayPlusEmptyGroup_B341 guards the other
// direction: a device that DOES name a relay and also has relay-less rows must
// still count exactly one relay (pre-fix it counted two and looked "split").
func TestCollectDevicePrefState_RealRelayPlusEmptyGroup_B341(t *testing.T) {
	d := b3411DB(t)
	seedB3411(t, d)
	if _, err := d.Exec(`INSERT INTO device_rules (user_id, device_id, device_hostname, user_name, exit_node_id, target_type, target_value, action, enabled) VALUES
	          (1, 9, 'skyworker', 'skyadmin', 'karolina', 'subnet', '1.1.1.0/24', 'accept', 1),
	          (1, 9, 'skyworker', 'skyadmin', 'karolina', 'subnet', '9.9.9.0/24', 'accept', 1),
	          (1, 9, 'skyworker', 'skyadmin', '',         'subnet', '8.8.8.0/24', 'accept', 1)`); err != nil {
		t.Fatalf("seed skyworker: %v", err)
	}
	s := &Service{DB: skygatedb.FixedDBSource{DB: d}}
	st, err := s.collectDevicePrefState(context.Background(), 1, "skyadmin", "skyworker", nil)
	if err != nil {
		t.Fatalf("collectDevicePrefState: %v", err)
	}
	if st.DistinctExitNodes != 1 {
		t.Errorf("DistinctExitNodes = %d, want 1 (only karolina names a relay)", st.DistinctExitNodes)
	}
	if st.DominantExitHostname != "karolina" {
		t.Errorf("DominantExitHostname = %q, want karolina — a relay-less row must not shadow the relay", st.DominantExitHostname)
	}
	if st.TotalRules != 3 {
		t.Errorf("TotalRules = %d, want 3", st.TotalRules)
	}
}
