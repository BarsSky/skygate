package exit_rules

// api_b507_test.go — B507 (2026-11-20)
//
// The notes from the assistant's live run, item by item:
//
//  1. `duplicates` was always 0 and an existing rule was counted as new, so a
//     RETRY of a batch looked like a fresh insert — the agent package promises the
//     opposite.
//  2. B480's "this target is already pinned to ANOTHER relay" was answered as
//     success: the assistant believed it had pinned emilia while the traffic
//     stayed on the old relay.
//  3. `target_type` / `target_value` / `exit_node` were unvalidated, so
//     `subnet 1.2.3.4/99`, `domain mega..nz` and a misspelled relay became inert
//     rows with no error anywhere.
//  4. The answer never said whether the ACL was applied.
//  (3b) The per-device route script answered "no rules" on PostgreSQL because two
//     filters shared `$1`.
//
// These tests pin the pure halves: the validators, the outcome→status mapping, and
// the placeholder arithmetic that broke item 3b.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"skygate/internal/auth"
	skygatedb "skygate/internal/db"
	"skygate/internal/headscale"
)

func TestB507_ValidateRuleTarget(t *testing.T) {
	cases := []struct {
		name, tt, tv string
		wantOK       bool
		wantReason   string // substring
	}{
		{"a plain /32", "subnet", "1.2.3.4/32", true, ""},
		{"a range", "subnet", "104.16.0.0/12", true, ""},
		{"the whole space", "subnet", "0.0.0.0/0", true, ""},
		{"an out-of-range prefix", "subnet", "1.2.3.4/99", false, "not an IP address or CIDR"},
		{"a bare host as ip", "ip", "8.8.8.8", true, ""},
		{"a bare host as subnet", "subnet", "1.2.3.4", false, "needs a prefix length"},
		{"garbage", "ip", "not-an-ip", false, "not an IP address or CIDR"},
		{"a normal domain", "domain", "mega.nz", true, ""},
		{"the double dot from the live run", "domain", "mega..nz", false, "empty label"},
		{"a leading dot", "domain", ".mega.nz", false, "must not start or end with a dot"},
		{"a single label", "domain", "localhost", false, "no dot"},
		{"an illegal character", "domain", "exa mple.com", false, "not allowed in a hostname"},
		{"a hyphen at the edge", "domain", "-bad.example.com", false, "hyphen"},
		{"the empty type", "", "1.2.3.4", false, "target_type is empty"},
		{"an invented type", "port", "22", false, "is not one of ip, subnet, domain"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := validateRuleTarget(tc.tt, tc.tv)
			if got.OK != tc.wantOK {
				t.Fatalf("validateRuleTarget(%q, %q).OK = %v, want %v (reason: %s)", tc.tt, tc.tv, got.OK, tc.wantOK, got.Reason)
			}
			if tc.wantReason != "" && !strings.Contains(got.Reason, tc.wantReason) {
				t.Errorf("reason = %q, want it to contain %q", got.Reason, tc.wantReason)
			}
		})
	}
}

// TestB507_ValidateExitNodeNamesTheAvailableOnes — a typo must be refused WITH the
// list, because the assistant has no other way to learn the relay names.
func TestB507_ValidateExitNodeNamesTheAvailableOnes(t *testing.T) {
	relays := []string{"emilia", "karolina", "sharlotta", "aisha"}
	if v := validateExitNode("emilia", relays); !v.OK {
		t.Errorf("a real relay was refused: %s", v.Reason)
	}
	if v := validateExitNode("EMILIA", relays); !v.OK {
		t.Errorf("relay matching must be case-insensitive: %s", v.Reason)
	}
	if v := validateExitNode("", relays); !v.OK {
		t.Error("an empty exit_node means 'preferred relay' and must be accepted")
	}
	v := validateExitNode("emila", relays)
	if v.OK {
		t.Fatal("a misspelled relay was accepted — it would become an inert rule")
	}
	for _, want := range []string{"emila", "aisha", "emilia", "karolina", "sharlotta"} {
		if !strings.Contains(v.Reason, want) {
			t.Errorf("reason %q does not name %q", v.Reason, want)
		}
	}
	if v := validateExitNode("emilia", nil); v.OK || !strings.Contains(v.Reason, "no exit node is currently registered") {
		t.Errorf("an empty relay set must say so, got ok=%v reason=%q", v.OK, v.Reason)
	}
}

func TestB507_ValidateRuleAction(t *testing.T) {
	for _, good := range []string{"", "accept", "ACCEPT", "deny"} {
		if v := validateRuleAction(good); !v.OK {
			t.Errorf("action %q refused: %s", good, v.Reason)
		}
	}
	if v := validateRuleAction("allow"); v.OK {
		t.Error("an unknown action was accepted")
	}
}

// TestB507_OutcomeToStatus is the heart of items 1 and 2: the response must not
// call a no-op or a refusal "success".
func TestB507_OutcomeToStatus(t *testing.T) {
	if s, r := ruleStatusFor(ruleInsertResult{Outcome: ruleInsertCreated, ID: 7}); s != "created" || r != "" {
		t.Errorf("created → %q/%q", s, r)
	}
	if s, r := ruleStatusFor(ruleInsertResult{Outcome: ruleInsertExisting, ID: 7}); s != "existing" || r != "" {
		t.Errorf("existing → %q/%q — a retry must be reported as existing, not as a new row", s, r)
	}
	s, r := ruleStatusFor(ruleInsertResult{Outcome: ruleInsertOtherRelay, ID: 3, OtherRelay: "karolina"})
	if s != "error" {
		t.Fatalf("the B480 case must be an ERROR, got %q", s)
	}
	if !strings.Contains(r, "karolina") || !strings.Contains(r, "request") {
		t.Errorf("reason %q must name the relay that holds the target and the way to move it", r)
	}
	if s, _ := ruleStatusFor(ruleInsertResult{Outcome: ruleInsertFailed}); s != "error" {
		t.Errorf("failed → %q, want error", s)
	}
}

// TestB507_DeviceScriptIsNotEmpty — item 3b, behaviourally. The operator's own
// suggestion: "a script for a device with rules must contain its CIDR".
//
// The bug was a shared `$1`: the query passed (userID, deviceID) but filtered
// `device_id = $1` twice, so on SQLite the device filter compared the device id
// against the USER id (0 rows) and on PostgreSQL the argument count did not match.
// Measured live: 368 routes without `device_id`, empty with `device_id=9`.
func TestB507_DeviceScriptIsNotEmpty(t *testing.T) {
	d := newB379DB(t)
	b379SeedUser(t, d, 7, "michail")
	// The script loader asks headscale which relays exist (to place each group's
	// routes on a relay IP), so the fixture needs a node stub, not a live tailnet.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"nodes":[` +
			`{"id":"3","name":"emilia","givenName":"emilia","ipAddresses":["100.64.0.3"],"online":true,"tags":["tag:exit-node"],"user":{"id":"85","name":"infra"}}]}`))
	}))
	t.Cleanup(srv.Close)
	s := &Service{
		Backend: &b379Backend{claims: &auth.Claims{UserID: 7, Username: "michail"}, db: d},
		DB:      skygatedb.FixedDBSource{DB: d},
		HS:      headscale.New(srv.URL, "test-token"),
	}
	mustInsert := func(deviceID int, value string, allDevices int) {
		t.Helper()
		if _, err := d.Exec(`INSERT INTO device_rules
			(user_id, device_id, exit_node_id, target_type, target_value, action, enabled, device_ip, all_devices)
			VALUES (7, $1, 'emilia', 'subnet', $2, 'accept', 1, '100.64.0.20', $3)`,
			deviceID, value, allDevices); err != nil {
			t.Fatalf("seed a rule for device %d: %v", deviceID, err)
		}
	}
	mustInsert(42, "192.0.2.7/32", 0)   // only device 42
	mustInsert(9, "198.51.100.0/24", 0) // only device 9
	mustInsert(42, "203.0.113.0/24", 1) // device 42, but marked "all my devices"

	valuesFor := func(deviceID int) []string {
		t.Helper()
		groups, err := s.loadRoutesForScriptGroups(7, deviceID)
		if err != nil {
			t.Fatalf("loadRoutesForScriptGroups(7, %d): %v", deviceID, err)
		}
		var out []string
		for _, g := range groups {
			for _, e := range g.Routes {
				out = append(out, e.targetVal)
			}
		}
		return out
	}
	has := func(list []string, want string) bool {
		for _, v := range list {
			if v == want {
				return true
			}
		}
		return false
	}

	got42 := valuesFor(42)
	if len(got42) == 0 {
		t.Fatal("the script for device 42 has NO routes — this is the defect that made the documented per-device step answer '# No IP/subnet exit rules configured' for a device with rules")
	}
	if !has(got42, "192.0.2.7/32") {
		t.Errorf("device 42's script is missing its own CIDR: %v", got42)
	}
	if has(got42, "198.51.100.0/24") {
		t.Errorf("device 42's script contains device 9's CIDR: %v", got42)
	}
	if !has(got42, "203.0.113.0/24") {
		t.Errorf("device 42's script is missing its all-devices rule: %v", got42)
	}

	got9 := valuesFor(9)
	if !has(got9, "198.51.100.0/24") {
		t.Errorf("device 9's script is missing its own CIDR: %v", got9)
	}
	if has(got9, "192.0.2.7/32") {
		t.Errorf("device 9's script contains device 42's CIDR: %v", got9)
	}
	if !has(got9, "203.0.113.0/24") {
		t.Errorf("device 9's script is missing the all-devices rule (B276.1 fan-out intent must reach every device): %v", got9)
	}

	// deviceID 0 means "the whole account", and must still see everything.
	if all := valuesFor(0); len(all) != 3 {
		t.Errorf("the account-wide script has %d route(s), want 3: %v", len(all), all)
	}
}

// TestB507_PlaceholderIsTheSecondOne — item 3b. The route-script query passes
// (userID, deviceID); the filter for the device must therefore be $2, not $1.
// Pinning the arithmetic keeps the bug from coming back on the next edit.
func TestB507_PlaceholderIsTheSecondOne(t *testing.T) {
	if got := skygatedb.PlaceholdersList(1); got != "$1" {
		t.Fatalf("precondition: PlaceholdersList(1) = %q, want $1 on both backends", got)
	}
	if got := skygatedb.PlaceholdersRange(2, 2); got != "$2" {
		t.Errorf("PlaceholdersRange(2,2) = %q, want $2 — the device filter is the SECOND argument", got)
	}
}
