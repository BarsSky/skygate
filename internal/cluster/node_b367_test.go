// B367 — `COALESCE(roles, ”)` is not "the empty array" on PostgreSQL, and the
// statements that carried it were the operator-facing ones.
//
// MEASURED LIVE, 2026-10-09 (reference deployment, PostgreSQL). The operator
// pressed "Слить и удалить" (Drain & remove) on the row of an exit-node relay
// that the pre-B359 discovery pass had adopted into the cluster, and the panel
// answered:
//
//	drain+remove: lookup node: ERROR: malformed array literal: "" (SQLSTATE 22P02)
//
// `cluster_node.roles` is TEXT[] on PostgreSQL, so the untyped literal ” in
// `COALESCE(roles, ”)` is coerced to text[] and REJECTED AT PARSE TIME — the
// statement never runs, whatever the data says. Reproduced directly against the
// live database:
//
//	SELECT id, COALESCE(state,''), COALESCE(roles,'') FROM cluster_node …;
//	ERROR:  malformed array literal: ""
//
// Five statements carried it: the lookup inside RemoveNode, DrainNode and
// DrainAndRemoveNode (internal/cluster/node.go) and — worse — the two in
// FindClusterPrimary / NodeRoles (internal/db/cluster_sql_b291.go), which are
// the FAILOVER and the failover-DRILL transactions. On SQLite the same column is
// the `{a,b}` TEXT literal, so `'{}'` parses identically and the defect is
// invisible on a native install — the L-16 dialect-leak class, in the one place
// the operator reaches for during an incident.
//
// These tests are BEHAVIOURAL on purpose: they call the real functions, so they
// fail with the old SQL and pass with the fix. The PostgreSQL halves SKIP when
// SKYGATE_TEST_PG_DSN is unset (AGENTS rule 1) — which is also WHY the defect
// survived CI: the default job is SQLite-only.
package cluster

import (
	"database/sql"
	"os"
	"strings"
	"testing"

	"skygate/internal/db"
)

// readClusterSource returns one of THIS package's source files, so the
// source-level contract below reads what the production code actually sends.
func readClusterSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// seedRelayRow inserts the shape the live table actually had: a relay adopted by
// discovery, `state=failed`, one role.
func seedRelayRow(t *testing.T, d *sql.DB, hostname string) {
	t.Helper()
	if err := EnsureCluster(d, DefaultClusterID, "staging"); err != nil {
		t.Fatalf("EnsureCluster: %v", err)
	}
	if _, err := AddNode(d, DefaultClusterID, hostname, "100.64.0.3",
		[]string{NodeRoleStandby}, "(discovered via Tailscale)"); err != nil {
		t.Fatalf("AddNode(%s): %v", hostname, err)
	}
	if _, err := d.Exec(`UPDATE cluster_node SET state = 'failed' WHERE hostname = $1`, hostname); err != nil {
		t.Fatalf("set failed: %v", err)
	}
}

// TestB367_DrainAndRemoveWorksOnSQLite keeps the native install working (the
// behaviour the fix must not regress).
func TestB367_DrainAndRemoveWorksOnSQLite(t *testing.T) {
	d := newB291SQLite(t)
	seedRelayRow(t, d, "relay-sqlite")

	if err := DrainAndRemoveNode(d, DefaultClusterID, "relay-sqlite", "skyadmin", "cleanup"); err != nil {
		t.Fatalf("DrainAndRemoveNode on SQLite: %v", err)
	}
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM cluster_node WHERE hostname = 'relay-sqlite'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("row survived DrainAndRemoveNode: %d", n)
	}
}

// TestB367_DrainAndRemoveWorksOnPostgres is the regression for the operator's
// exact error. It runs against a real PostgreSQL server (skipped without the
// DSN) because the whole defect is dialect-specific: SQLite cannot see it.
func TestB367_DrainAndRemoveWorksOnPostgres(t *testing.T) {
	d := db.OpenTestPG(t)
	seedRelayRow(t, d, "relay-pg")

	// The other two callers of the same broken lookup, in the same test so one
	// PG round-trip covers all three statements.
	if err := DrainNode(d, DefaultClusterID, "relay-pg", "skyadmin", "drain first"); err != nil {
		t.Fatalf("DrainNode on PostgreSQL: %v (was: malformed array literal)", err)
	}
	if err := RemoveNode(d, DefaultClusterID, "relay-pg"); err != nil {
		t.Fatalf("RemoveNode on PostgreSQL: %v (was: malformed array literal)", err)
	}

	seedRelayRow(t, d, "relay-pg-2")
	if err := DrainAndRemoveNode(d, DefaultClusterID, "relay-pg-2", "skyadmin", "cleanup"); err != nil {
		t.Fatalf("DrainAndRemoveNode on PostgreSQL: %v (was: malformed array literal)", err)
	}
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM cluster_node WHERE hostname LIKE 'relay-pg%'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("rows survived on PostgreSQL: %d", n)
	}
}

// TestB367_EmptyRolesRoundTrip pins what the literal must mean: an EMPTY role
// list reads back as an empty list, not as a parse error. (The column is
// `TEXT[] NOT NULL DEFAULT '{}'` on BOTH migration chains, so the COALESCE is
// defensive — the point of the constant is that the literal is VALID for the
// column's type, which ” is not on PostgreSQL.)
func TestB367_EmptyRolesRoundTrip(t *testing.T) {
	d := newB291SQLite(t)
	seedRelayRow(t, d, "relay-empty")
	if _, err := d.Exec(`UPDATE cluster_node SET roles = '{}' WHERE hostname = 'relay-empty'`); err != nil {
		t.Fatalf("write the empty array literal: %v", err)
	}
	n, err := LookupNode(d, DefaultClusterID, "relay-empty")
	if err != nil {
		t.Fatalf("LookupNode with an empty roles literal: %v", err)
	}
	if len(n.Roles) != 0 {
		t.Fatalf("empty roles read back as %v, want an empty list", n.Roles)
	}
}

// TestB367_TheColumnIsNotNullOnBothChains records the premise the fix relies on
// and would break if a future migration made `roles` nullable on one backend
// only: the defensive COALESCE is only as good as the literal's validity.
func TestB367_TheColumnIsNotNullOnBothChains(t *testing.T) {
	for _, f := range []string{"../db/migrations_v0_64_b195.go", "../db/migrations_sqlite.go"} {
		if !strings.Contains(readClusterSource(t, f), "roles           TEXT[] NOT NULL DEFAULT '{}'") {
			t.Errorf("%s no longer declares `roles TEXT[] NOT NULL DEFAULT '{}'` — re-check the two "+
				"COALESCE sites before relaxing this", f)
		}
	}
}

// TestB367_TheFixIsTheEmptyArrayLiteral is the source-level half: the SQL the
// three node.go statements send must not contain the rejected form, and must go
// through the one constant that documents why. It is deliberately a read of the
// package source, because the statement is built by concatenation.
func TestB367_TheFixIsTheEmptyArrayLiteral(t *testing.T) {
	src := readClusterSource(t, "node.go")
	if strings.Contains(src, "COALESCE(roles, '')") {
		t.Error("internal/cluster/node.go still builds COALESCE(roles, '') — PostgreSQL rejects it with " +
			"SQLSTATE 22P02 (malformed array literal) before the statement runs")
	}
	if n := strings.Count(src, "db.EmptyTextArrayLiteral"); n < 3 {
		t.Errorf("only %d of the 3 drain/remove lookups use db.EmptyTextArrayLiteral", n)
	}
}
