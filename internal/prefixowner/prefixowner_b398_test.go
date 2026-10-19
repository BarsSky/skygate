package prefixowner

// prefixowner_b398_test.go — B398 (2026-10-18): a device's own exit-node choice must be a
// CLAIM, not a silent absentee.
//
// Live case (the operator's installation): cyborg's 16 Google rules all carried an EMPTY
// `exit_node_id` ("the engine decides", B277.3), so `LoadClaims` returned them with
// `ExitNode == ""` and `Assign` could not count them for any relay. The engine spread
// those prefixes "for load" over emilia and sharlotta — relays the device never chose —
// and the per-CIDR ACL pin then pointed at them (B397 had to teach the sync to advertise
// them). A device with a recorded preference HAS made a choice: here it votes for the
// prefixes its own relay-less rules claim.
//
// The preference is only a HINT for the assignment: since B398 it no longer becomes an
// ACL `via` FILTER unless a human pinned it (internal/acl/acl_b398_test.go).

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func openB398ClaimsDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := sql.Open("sqlite", "file:"+t.TempDir()+"/b398.db")
	if err != nil {
		t.Skipf("sqlite driver unavailable: %v", err)
	}
	if _, err := d.Exec(`CREATE TABLE device_rules (
		id INTEGER PRIMARY KEY,
		user_id INTEGER DEFAULT 0,
		device_id INTEGER DEFAULT 0,
		user_name TEXT DEFAULT '',
		device_hostname TEXT DEFAULT '',
		exit_node_id TEXT DEFAULT '',
		target_type TEXT DEFAULT '',
		target_value TEXT DEFAULT '',
		enabled INTEGER DEFAULT 1
	)`); err != nil {
		t.Fatalf("create device_rules: %v", err)
	}
	if _, err := d.Exec(`CREATE TABLE device_exit_node_prefs (
		user_id INTEGER NOT NULL,
		device_hostname TEXT NOT NULL,
		exit_node_tag TEXT NOT NULL,
		set_by_user_id INTEGER DEFAULT 0,
		updated_at INTEGER DEFAULT 0,
		via_enabled INTEGER DEFAULT 1
	)`); err != nil {
		t.Fatalf("create device_exit_node_prefs: %v", err)
	}
	// node_owner_map is where the relay TAG becomes the relay HOSTNAME (the unit the
	// assignment table stores).
	if _, err := d.Exec(`CREATE TABLE node_owner_map (
		node_id TEXT,
		headscale_user_id INTEGER DEFAULT 0,
		username TEXT DEFAULT '',
		tag TEXT DEFAULT '',
		tagged_by_user_id INTEGER DEFAULT 0,
		tagged_at INTEGER DEFAULT 0,
		hostname TEXT DEFAULT ''
	)`); err != nil {
		t.Fatalf("create node_owner_map: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// TestB398_DevicePreferenceVotesForItsRelaylessRules is the regression: a rule with an
// empty exit_node_id plus a device preference must produce a claim NAMING the preferred
// relay, so the assignment follows the device instead of overruling it.
func TestB398_DevicePreferenceVotesForItsRelaylessRules(t *testing.T) {
	d := openB398ClaimsDB(t)
	if _, err := d.Exec(`INSERT INTO device_rules (device_id, device_hostname, exit_node_id, target_type, target_value, enabled)
	                     VALUES (29, 'cyborg', '', 'subnet', '142.250.0.0/15', 1)`); err != nil {
		t.Fatalf("insert rule: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO device_exit_node_prefs (user_id, device_hostname, exit_node_tag, set_by_user_id, updated_at, via_enabled)
	                     VALUES (1, 'cyborg', 'tag:dev-infra-karolina', 0, 0, 1)`); err != nil {
		t.Fatalf("insert pref: %v", err)
	}
	// the relay's own row: the TAG the preference stores → the HOSTNAME the table uses
	if _, err := d.Exec(`INSERT INTO node_owner_map (node_id, username, tag, hostname)
	                     VALUES ('102', 'infra', 'tag:dev-infra-karolina', 'karolina')`); err != nil {
		t.Fatalf("insert relay owner row: %v", err)
	}

	claims, err := LoadClaims(d)
	if err != nil {
		t.Fatalf("LoadClaims: %v", err)
	}
	var withRelay int
	for _, c := range claims {
		if c.Prefix == "142.250.0.0/15" && c.ExitNode == "karolina" && c.DeviceID == 29 {
			withRelay++
		}
	}
	if withRelay != 1 {
		t.Fatalf("LoadClaims produced %d claim(s) naming the device's preferred relay, want 1; claims=%+v", withRelay, claims)
	}

	// And the assignment must land on the device's relay, not on an engine spread.
	got := Assign(claims, []string{"karolina", "emilia", "sharlotta"}, nil)
	if len(got) != 1 {
		t.Fatalf("assignments = %d, want 1", len(got))
	}
	if got[0].ExitNode != "karolina" {
		t.Errorf("assigned to %q, want the device's own choice karolina", got[0].ExitNode)
	}
}

// TestB398_NoPreferenceStaysEngineDecided keeps the other half honest: with no
// preference the rule must still be engine-decided (no invented vote).
func TestB398_NoPreferenceStaysEngineDecided(t *testing.T) {
	d := openB398ClaimsDB(t)
	if _, err := d.Exec(`INSERT INTO device_rules (device_id, device_hostname, exit_node_id, target_type, target_value, enabled)
	                     VALUES (30, 'basic', '', 'subnet', '8.8.8.0/24', 1)`); err != nil {
		t.Fatalf("insert rule: %v", err)
	}
	claims, err := LoadClaims(d)
	if err != nil {
		t.Fatalf("LoadClaims: %v", err)
	}
	for _, c := range claims {
		if c.Prefix == "8.8.8.0/24" && c.ExitNode != "" {
			t.Fatalf("an engine-decided rule gained a relay claim %q out of nowhere", c.ExitNode)
		}
	}
}
