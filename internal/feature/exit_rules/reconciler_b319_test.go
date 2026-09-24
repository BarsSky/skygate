package exit_rules

// reconciler_b319_test.go — B319: the preferred-exit reconciler's rule lookup must
// run on PostgreSQL.
//
// # THE LIVE DEFECT
//
// Agent VM, every container start, twice (one line per device that has a preference):
//
//	preferred-reconciler: state for michail/basic: rules for michail/basic:
//	  ERROR: operator does not exist: integer = text (SQLSTATE 42883)
//	preferred-reconciler: state for skyadmin/skyworker: rules for skyadmin/skyworker: ERROR: …
//
// The query ORed a second rule-matching branch:
//
//	OR device_id IN (SELECT node_id FROM node_owner_map WHERE user_id = $1 AND hostname = $2)
//
// `device_rules.device_id` is INTEGER and `node_owner_map.node_id` is TEXT, so
// PostgreSQL refuses the comparison and the reconciler cannot compute the state for
// any device that has an ownership row — those devices are skipped on every tick, so
// a preferred exit node is never reconciled for them. SQLite happily compares 9 with
// '9', which is why the whole suite stayed green.
//
// The same line carried a second, quieter defect: `node_owner_map` has NO `user_id`
// column, so that predicate silently bound to the OUTER `device_rules.user_id` —
// it filtered nothing and correlated nothing. `node_id` is unique per node and the
// outer query already scopes the user, so the branch needs only the hostname.
//
// What these tests can prove locally is that the SQLite behaviour is UNCHANGED (the
// node-id branch still finds rules for a device whose denormalised hostname is stale)
// and that the query text no longer asks the database to compare an integer with a
// text column. The PostgreSQL side is pinned by scripts/check_b319_rules_owner_type.sh,
// which runs the real query against a live PG when one is available.

import (
	"database/sql"
	"os"
	"strings"
	"testing"

	skygatedb "skygate/internal/db"
)

func newB319DB(t *testing.T) *sql.DB {
	t.Helper()
	_, d, err := skygatedb.OpenWithDialect("sqlite:" + t.TempDir() + "/b319.db")
	if err != nil {
		t.Skipf("sqlite dialect unavailable in this build: %v", err)
	}
	if err := skygatedb.MigrateSQLite(d); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// seedB319 wires one user whose node 9 is called "skyworker" now, plus two rules: one
// carrying the CURRENT hostname (the post-sync case) and one whose denormalised
// hostname is still the OLD one (the pre-rename case the node-id branch exists for).
func seedB319(t *testing.T, d *sql.DB) {
	t.Helper()
	exec := func(q string, args ...interface{}) {
		t.Helper()
		if _, err := d.Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	exec(`INSERT INTO portal_users (id, username, password_hash, is_admin, headscale_user_id)
	      VALUES (1, 'skyadmin', 'x', 1, 7)`)
	exec(`INSERT INTO node_owner_map (node_id, headscale_user_id, username, tag, tagged_by_user_id, tagged_at, hostname)
	      VALUES ('9', 7, 'skyadmin', 'tag:dev-skyadmin-skyworker', 1, 0, 'skyworker')`)
	// Post-sync rule: denormalised hostname matches.
	exec(`INSERT INTO device_rules (user_id, device_id, user_name, exit_node_id, target_type, target_value, action, enabled, device_hostname)
	      VALUES (1, 9, 'skyadmin', 'karolina', 'ip', '104.16.0.0/12', 'accept', 1, 'skyworker')`)
	// Pre-rename rule: still filed under the OLD hostname, only reachable by node id.
	exec(`INSERT INTO device_rules (user_id, device_id, user_name, exit_node_id, target_type, target_value, action, enabled, device_hostname)
	      VALUES (1, 9, 'skyadmin', 'karolina', 'ip', '8.8.8.0/24', 'accept', 1, 'old-name')`)
	// A rule of the SAME user on ANOTHER device must never be counted.
	exec(`INSERT INTO device_rules (user_id, device_id, user_name, exit_node_id, target_type, target_value, action, enabled, device_hostname)
	      VALUES (1, 20, 'skyadmin', 'emilia', 'ip', '1.1.1.0/24', 'accept', 1, 'other-device')`)
}

// TestB319_RuleLookupFindsThePreRenameRuleByNodeID is the behaviour the node-id
// branch exists for: a rule whose denormalised hostname is stale must still be found
// (via node_owner_map) so the reconciler never sees "0 rules" for a device that has 2.
func TestB319_RuleLookupFindsThePreRenameRuleByNodeID(t *testing.T) {
	d := newB319DB(t)
	seedB319(t, d)
	svc := &Service{DB: &skygatedb.FixedDBSource{DB: d}}

	state, err := svc.collectDevicePrefState(t.Context(), 1, "skyadmin", "skyworker")
	if err != nil {
		t.Fatalf("collectDevicePrefState: %v (this is the live PostgreSQL failure on SQLite's dialect)", err)
	}
	if state.TotalRules != 2 {
		t.Fatalf("TotalRules = %d, want 2 (the current-hostname rule AND the stale one found by node id)", state.TotalRules)
	}
	if state.DominantExitHostname != "karolina" {
		t.Fatalf("DominantExitNode = %q, want karolina", state.DominantExitHostname)
	}
	if state.DistinctExitNodes != 1 {
		t.Fatalf("DistinctExitNodes = %d, want 1", state.DistinctExitNodes)
	}
}

// TestB319_RuleLookupDoesNotCompareIntegerWithTextColumn is the structural half: the
// query must not hand PostgreSQL a comparison it refuses. A bare
// `device_id IN (SELECT node_id …)` is exactly that, and it is invisible on SQLite.
func TestB319_RuleLookupDoesNotCompareIntegerWithTextColumn(t *testing.T) {
	// The query lives in collectDevicePrefState; read it from the source file so the
	// assertion cannot drift from the code.
	src, err := os.ReadFile("reconciler.go")
	if err != nil {
		t.Fatalf("read reconciler.go: %v", err)
	}
	text := string(src)
	// Scope the assertion to the QUERY, not the file: the doc comment above the query
	// quotes the old SQL on purpose (it is the record of the defect), and a contract
	// that cannot tell code from history would forbid documenting it.
	idx := strings.Index(text, "ruleRows, err := s.dbc().QueryContext")
	if idx < 0 {
		t.Fatal("the rule lookup query was not found — the contract cannot be checked")
	}
	query := text[idx:]
	if end := strings.Index(query, "`, userID, hostname)"); end > 0 {
		query = query[:end]
	}
	if strings.Contains(query, "OR device_id IN (") {
		t.Error("the rule lookup still compares INTEGER device_id with TEXT node_id directly — PostgreSQL answers `operator does not exist: integer = text` and the reconciler skips every device with an ownership row")
	}
	if !strings.Contains(query, "CAST(device_id AS TEXT) IN (") {
		t.Error("the rule lookup lost its explicit CAST(device_id AS TEXT) — the comparison is not portable")
	}
	// And the sub-select must not carry a column node_owner_map does not have: that
	// is what made `user_id` bind to the OUTER table and silently filter nothing.
	if strings.Contains(query, "user_id = $1 AND hostname = $2") {
		t.Error("the sub-select still uses user_id, which node_owner_map does not have (it correlated to the outer device_rules row)")
	}
}
