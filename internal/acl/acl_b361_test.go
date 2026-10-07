// acl_b361_test.go — B361 (2026-10-07).
//
// THE HALF B356 COULD NOT SEE. B356 made the per-device `autogroup:internet` pin
// conditional on the preferred relay being USABLE (the B273 predicate). On the same
// day the operator reported a device that was broken while its relay was perfectly
// healthy:
//
//	device a71    ZERO rows in device_rules
//	              one device_exit_node_prefs row, set_by_user_id = 1 (a HUMAN),
//	              ~46 days old, naming tag:dev-infra-emilia
//	prefix_owner  120 rows, ALL of them karolina
//	emilia        advertises 2 routes (0.0.0.0/0, ::/0) — none of the routing
//	emilia state  `online`
//
// So the health gate passed and the ACL kept emitting
// `via: ["tag:dev-infra-emilia"]` — a permission FILTER that allowed a71 to exit only
// through a relay carrying none of the destinations karolina had taken over. The
// device had NO rules of its own, so its egress depended entirely on what that relay
// advertised, and the relay advertised nothing of the tailnet's routing.
//
// What these tests pin, as the scripted timelines the operator asked for:
//
//	(A) the a71 case verbatim: no rules + a human pin + a relay that owns 0 prefixes
//	    → the UNPINNED autogroup:internet grant, the stored row UNCHANGED (with
//	    set_by_user_id intact), and the reason named;
//	(B) the same device when the relay owns ≥1 prefix → the pin IS emitted (no
//	    behaviour change for the devices that work);
//	(C) a device WITH rules whose relay owns none of them → unpinned, reason named;
//	(D) a device WITH rules whose relay owns one of them → pinned (the working case);
//	(E) the exact B356 gap: a relay that is `online` in the monitor and owns nothing
//	    never receives a pin, while the health verdict itself is untouched;
//	(F) unknown is not broken: an EMPTY assignment table keeps every pin.
package acl

import (
	"database/sql"
	"strings"
	"testing"

	"skygate/internal/db"
	"skygate/internal/prefixowner"
)

// b361ACLDB builds a migrated SQLite database with one portal user, one device and
// the two relays of the live tailnet (`relay-1` — the pinned one — and `relay-2` — the
// one that owns the routing).
func b361ACLDB(t *testing.T) *sql.DB {
	t.Helper()
	t.Setenv("SKYGATE_BASE_DOMAIN", "ts.example.com")
	t.Setenv("SKYGATE_ADMIN_USER", "admin")
	_, sqlDB, err := db.OpenWithDialect("sqlite:" + t.TempDir() + "/b361acl.db")
	if err != nil {
		t.Skipf("sqlite dialect unavailable: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.MigrateSQLite(sqlDB); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := sqlDB.Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	mustExec(`INSERT INTO portal_users (id, username, password_hash, is_admin, headscale_user_id)
	          VALUES (1, 'skyadmin', 'x', 1, 1) ON CONFLICT (id) DO NOTHING`)
	mustExec(`INSERT INTO node_owner_map (node_id, headscale_user_id, username, tag, tagged_by_user_id, tagged_at, hostname) VALUES
	          ('101', 1, 'skyadmin', 'tag:dev-skyadmin-a71',     1, 0, 'a71'),
	          ('102', 1, 'skyadmin', 'tag:dev-skyadmin-workpc',  1, 0, 'workpc'),
	          ('103', 1, 'infra',    'tag:dev-infra-relay-1',    1, 0, 'relay-1'),
	          ('104', 1, 'infra',    'tag:dev-infra-relay-2',    1, 0, 'relay-2')`)
	return sqlDB
}

// seedB361Routing makes `relay-2` the relay that owns the per-destination routing, and
// leaves `relay-1` (the preferred one in the a71 case) with the given number of owned
// prefixes.
func seedB361Routing(t *testing.T, d *sql.DB, relay1Owned int) {
	t.Helper()
	rows := []struct {
		prefix string
		owner  string
	}{
		{"142.250.0.0/15", "relay-2"},
		{"172.217.0.0/16", "relay-2"},
		{"104.16.0.0/12", "relay-2"},
	}
	for i, r := range rows {
		if i < relay1Owned {
			r.owner = "relay-1"
		}
		if _, err := d.Exec(`INSERT INTO prefix_owner (prefix, exit_node_id, source, claims, devices, updated_at)
		                     VALUES ($1, $2, 'explicit', 1, 1, 0)`, r.prefix, r.owner); err != nil {
			t.Fatalf("seed prefix_owner %s: %v", r.prefix, err)
		}
	}
}

// seedB361Health writes the monitor's verdict for a relay, exactly as B273 stores it.
func seedB361Health(t *testing.T, d *sql.DB, host, state string, healthy bool) {
	t.Helper()
	if err := db.UpsertExitNodeHealth(d, db.ExitNodeHealth{
		NodeID: host, Hostname: host, State: state, Healthy: healthy, AdvertisedRoutesOK: healthy,
	}); err != nil {
		t.Fatalf("seed exit_node_health %s: %v", host, err)
	}
}

// seedB361Rule adds one enabled ip rule claiming `prefix` for the given device
// hostname — "the device HAS rules" half of the predicate.
func seedB361Rule(t *testing.T, d *sql.DB, deviceID int, hostname, prefix string) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO device_rules (user_id, device_id, device_hostname, user_name, exit_node_id, target_type, target_value, action, enabled)
	                     VALUES (1, $1, $2, 'skyadmin', '', 'subnet', $3, 'accept', 1)`, deviceID, hostname, prefix); err != nil {
		t.Fatalf("seed device_rules %s: %v", prefix, err)
	}
}

// seedB361Pref writes the device's stored preference. `setBy` is the provenance: != 0
// means a HUMAN chose it, and the whole block exists to prove that this row survives.
func seedB361Pref(t *testing.T, d *sql.DB, hostname, tag string, setBy int64) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO device_exit_node_prefs (user_id, device_hostname, exit_node_tag, set_by_user_id, updated_at, via_enabled)
	                     VALUES (1, $1, $2, $3, 0, 1)`, hostname, tag, setBy); err != nil {
		t.Fatalf("seed device_exit_node_prefs %s: %v", hostname, err)
	}
}

// b361Policy generates the live grants document for the current database state.
func b361Policy(t *testing.T, d *sql.DB) string {
	t.Helper()
	policy, err := GenerateACLWithViaForPlane(d, "")
	if err != nil {
		t.Fatalf("GenerateACLWithViaForPlane: %v", err)
	}
	return policy
}

// TestB361_A71CaseVerbatim is (A) — the operator's device, reproduced exactly: no
// rules, a HUMAN pin recorded 46 days earlier, and a relay that owns ZERO of the 120
// prefixes in the assignment table.
func TestB361_A71CaseVerbatim(t *testing.T) {
	d := b361ACLDB(t)
	seedB361Routing(t, d, 0) // relay-1 (the pinned one) owns nothing
	seedB361Health(t, d, "relay-1", "online", true)
	seedB361Health(t, d, "relay-2", "online", true)
	seedB361Pref(t, d, "a71", "tag:dev-infra-relay-1", 1)

	policy := b361Policy(t, d)
	pinned := countGrantsText(t, policy, "tag:dev-skyadmin-a71", `"autogroup:internet"`, true)
	loose := countGrantsText(t, policy, "tag:dev-skyadmin-a71", `"autogroup:internet"`, false)
	if pinned != 0 {
		t.Errorf("pinned autogroup:internet grants = %d, want 0 — `via` is a permission FILTER, and this relay owns none of the routing", pinned)
	}
	if loose != 1 {
		t.Errorf("loose (unpinned) autogroup:internet grants = %d, want 1 — the device must keep egress through a relay that serves", loose)
	}
	if strings.Contains(policy, `"via": ["tag:dev-infra-relay-1"]`) {
		t.Errorf("the generated policy still pins a device to a relay that owns nothing:\n%s", policy)
	}

	// The stored row is UNTOUCHED — the whole point of the block: only the PIN is
	// withheld, so the operator's own choice takes effect again by itself the moment
	// that relay serves something.
	var tag string
	var setBy int64
	var via int
	if err := d.QueryRow(`SELECT exit_node_tag, set_by_user_id, via_enabled FROM device_exit_node_prefs
	                      WHERE user_id = 1 AND device_hostname = 'a71'`).Scan(&tag, &setBy, &via); err != nil {
		t.Fatalf("the stored preference is GONE (%v) — the generator must never delete or rewrite the operator's row", err)
	}
	if tag != "tag:dev-infra-relay-1" || setBy != 1 || via != 1 {
		t.Errorf("stored row = (tag=%q set_by_user_id=%d via_enabled=%d), want it byte-identical (tag:dev-infra-relay-1 / 1 / 1)", tag, setBy, via)
	}
}

// TestB361_SameDeviceWhenTheRelayServesKeepsThePin is (B): the moment the preferred
// relay owns at least one prefix the pin comes back — the safety net must not become a
// permanent behaviour change for the devices that work.
func TestB361_SameDeviceWhenTheRelayServesKeepsThePin(t *testing.T) {
	d := b361ACLDB(t)
	seedB361Routing(t, d, 1) // relay-1 owns one prefix
	seedB361Health(t, d, "relay-1", "online", true)
	seedB361Pref(t, d, "a71", "tag:dev-infra-relay-1", 1)

	policy := b361Policy(t, d)
	pinned := countGrantsText(t, policy, "tag:dev-skyadmin-a71", `"autogroup:internet"`, true)
	loose := countGrantsText(t, policy, "tag:dev-skyadmin-a71", `"autogroup:internet"`, false)
	if pinned != 1 || loose != 0 {
		t.Errorf("pinned=%d loose=%d, want pinned=1 loose=0 — a relay that serves something keeps the operator's pin", pinned, loose)
	}
}

// TestB361_DeviceWithRulesWhoseRelayOwnsNoneOfThem is (C): the device HAS rules, and
// the preferred relay owns prefixes — just none of the ones those rules claim. The
// pin is withheld for the same reason and with a reason the operator can act on.
func TestB361_DeviceWithRulesWhoseRelayOwnsNoneOfThem(t *testing.T) {
	d := b361ACLDB(t)
	seedB361Routing(t, d, 1) // relay-1 owns 142.250.0.0/15
	seedB361Health(t, d, "relay-1", "online", true)
	// The device's rules claim the OTHER two prefixes, all of them relay-2's.
	seedB361Rule(t, d, 102, "workpc", "172.217.0.0/16")
	seedB361Rule(t, d, 102, "workpc", "104.16.0.0/12")
	seedB361Pref(t, d, "workpc", "tag:dev-infra-relay-1", 0)

	policy := b361Policy(t, d)
	pinned := countGrantsText(t, policy, "tag:dev-skyadmin-workpc", `"autogroup:internet"`, true)
	loose := countGrantsText(t, policy, "tag:dev-skyadmin-workpc", `"autogroup:internet"`, false)
	if pinned != 0 || loose != 1 {
		t.Errorf("pinned=%d loose=%d, want pinned=0 loose=1 — none of this device's prefixes is served by its preferred relay", pinned, loose)
	}
}

// TestB361_DeviceWithRulesWhoseRelayOwnsOneOfThem is (D): the same device, with the
// preferred relay owning one of its claimed prefixes. The pin stands — partial service
// is still service, and unpinning here would break a device that works.
func TestB361_DeviceWithRulesWhoseRelayOwnsOneOfThem(t *testing.T) {
	d := b361ACLDB(t)
	seedB361Routing(t, d, 2) // relay-1 owns 142.250.0.0/15 + 172.217.0.0/16
	seedB361Health(t, d, "relay-1", "online", true)
	seedB361Rule(t, d, 102, "workpc", "172.217.0.0/16")
	seedB361Rule(t, d, 102, "workpc", "104.16.0.0/12")
	seedB361Pref(t, d, "workpc", "tag:dev-infra-relay-1", 0)

	policy := b361Policy(t, d)
	if pinned := countGrantsText(t, policy, "tag:dev-skyadmin-workpc", `"autogroup:internet"`, true); pinned != 1 {
		t.Errorf("pinned autogroup:internet grants = %d, want 1 — the preferred relay serves one of the device's prefixes", pinned)
	}
}

// TestB361_HealthyButOwnerlessRelayNeverGetsThePin is (E) — the EXACT B356 gap,
// asserted as a PAIR so the two halves cannot drift apart: the monitor really does call
// the relay `online` (monitoring.ExitNodeUsable), and the generator still refuses to
// pin a device to it.
//
// This is the property the operator's device needed: "the relay is up" is not "the
// relay carries what this device needs", and a check that only asks the first question
// leaves the second one unanswerable.
func TestB361_HealthyButOwnerlessRelayNeverGetsThePin(t *testing.T) {
	d := b361ACLDB(t)
	seedB361Routing(t, d, 0)
	seedB361Health(t, d, "relay-1", "online", true)
	seedB361Pref(t, d, "a71", "tag:dev-infra-relay-1", 1)

	// Half one: the health verdict is what B356 was built on, and it says USABLE.
	verdicts := relayVerdicts(d)
	if v, ok := verdicts["relay-1"]; !ok || !v.Usable {
		t.Fatalf("relayVerdicts = %+v, want relay-1 usable (the ownerless relay IS online — that is the whole gap)", verdicts)
	}
	if state, dead := unusablePreferredRelay("tag:dev-infra-relay-1", verdicts); dead {
		t.Fatalf("B356's health predicate called the relay %q — it must not, or this test would be asserting nothing new", state)
	}
	// Half two: and the ownership predicate withholds the pin anyway.
	if reason, blocked := servesNothing("tag:dev-infra-relay-1", prefixOwnerTags(t, d), nil); !blocked {
		t.Fatalf("servesNothing = (%q, false), want blocked — a relay that owns no prefix gives a no-rules device nothing but a restriction", reason)
	} else if !strings.Contains(reason, "no prefix at all") {
		t.Errorf("reason = %q, want it to name the ownership fact", reason)
	}
}

// TestB361_EmptyAssignmentTableKeepsEveryPin is (F): unknown is not broken. With no
// ownership data at all the generator cannot answer the question, and the pre-B361
// document must stand — otherwise the FIRST generation on a fresh install would strip
// every pin in the tailnet.
func TestB361_EmptyAssignmentTableKeepsEveryPin(t *testing.T) {
	d := b361ACLDB(t)
	seedB361Health(t, d, "relay-1", "online", true)
	seedB361Pref(t, d, "a71", "tag:dev-infra-relay-1", 1)

	if reason, blocked := servesNothing("tag:dev-infra-relay-1", prefixOwnerTags(t, d), nil); blocked {
		t.Fatalf("servesNothing = (%q, true) on an EMPTY assignment table — absence of evidence must not rewrite a policy", reason)
	}
	policy := b361Policy(t, d)
	if pinned := countGrantsText(t, policy, "tag:dev-skyadmin-a71", `"autogroup:internet"`, true); pinned != 1 {
		t.Errorf("pinned autogroup:internet grants = %d, want 1 with no ownership data at all", pinned)
	}
}

// prefixOwnerTags is test sugar for the map the generator passes to servesNothing —
// read through the SAME projection the generator uses (`prefixowner.TagByPrefix`), so a
// divergence between the test's view and production's would show up as a failure here.
func prefixOwnerTags(t *testing.T, d *sql.DB) map[string]string {
	t.Helper()
	return prefixowner.TagByPrefix(d)
}
