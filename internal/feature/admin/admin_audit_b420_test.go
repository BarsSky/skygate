// internal/feature/admin/admin_audit_b420_test.go — the two audit
// readers on /admin/certificates and /admin/deploy must WORK, not merely
// exist.
//
// B420 root cause: both queries selected
// `EXTRACT(EPOCH FROM created_at)::bigint` from a column that is ALREADY
// an INTEGER epoch in both migration chains. On SQLite that is a syntax
// error ("near FROM"), on PostgreSQL a type error (extract() has no
// integer overload), and the caller dropped the error — so the "Recent
// events" tables rendered empty on every backend, on every install, with
// nothing in the log.
//
// These tests run the REAL statements against a REAL SQLite database
// (built by the real migration chain, not a hand-written schema) and
// additionally pin that the OLD shape still fails there — a regression
// that reintroduced it would be caught by the first assertion, and the
// second proves the class is still detectable rather than assumed.
package admin

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"skygate/internal/db"
)

// openAuditB420DB opens a migrated throwaway SQLite database and forces
// the process-wide dialect to SQLite, because qCertAuditEvents /
// qDeployAuditEvents embed db.NowUnixSQL() for their sibling write paths
// and the assertions compare against that same clock.
func openAuditB420DB(t *testing.T) *sql.DB {
	t.Helper()
	conn, err := db.OpenFastSQLiteForTest(filepath.Join(t.TempDir(), "b420-audit.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	prev := db.ActiveDialect()
	db.SetActiveDialect(db.DialectSQLite)
	t.Cleanup(func() { db.SetActiveDialect(prev) })
	if err := db.ApplyMigrations(conn, db.DialectSQLite); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	return conn
}

func insertAuditB420(t *testing.T, conn *sql.DB, action, detail string) {
	t.Helper()
	if _, err := conn.Exec(
		`INSERT INTO audit_log (user_id, username, action, detail, created_at)
		 VALUES (0, 'operator', $1, $2, `+db.NowUnixSQL()+`)`, action, detail); err != nil {
		t.Fatalf("insert audit row %q: %v", action, err)
	}
}

// TestB420_CertAuditEventsQueryWorksOnSQLite executes the real
// qCertAuditEvents statement and requires the rows to come back.
func TestB420_CertAuditEventsQueryWorksOnSQLite(t *testing.T) {
	conn := openAuditB420DB(t)
	insertAuditB420(t, conn, "certsync.pull", "sha256=deadbeef")
	insertAuditB420(t, conn, "certs.dns01_toggle", "enabled=true")
	insertAuditB420(t, conn, "unrelated.action", "must not be listed")

	rows, err := conn.Query(qCertAuditEvents, 10)
	if err != nil {
		t.Fatalf("qCertAuditEvents failed on SQLite: %v — the page would render an empty table", err)
	}
	defer rows.Close()

	type event struct {
		when   int64
		actor  string
		action string
		detail string
	}
	var got []event
	for rows.Next() {
		var e event
		if err := rows.Scan(&e.when, &e.actor, &e.action, &e.detail); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, e)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("got %d cert audit rows, want the 2 certs.*/certsync.* rows (the third action must be filtered out): %+v", len(got), got)
	}
	now := time.Now().Unix()
	for _, e := range got {
		if e.when <= 0 {
			t.Errorf("%s: created_at scanned as %d — the reader is no longer producing an epoch", e.action, e.when)
		}
		// The row was inserted seconds ago; a wildly different value
		// means the write and the read disagree about the clock.
		if e.when < now-86400 || e.when > now+86400 {
			t.Errorf("%s: created_at=%d is not within a day of now=%d", e.action, e.when, now)
		}
		if e.actor != "operator" {
			t.Errorf("%s: actor=%q, want operator", e.action, e.actor)
		}
	}
}

// TestB420_DeployAuditEventsQueryWorksOnSQLite is the same proof for
// /admin/deploy's qDeployAuditEvents.
func TestB420_DeployAuditEventsQueryWorksOnSQLite(t *testing.T) {
	conn := openAuditB420DB(t)
	insertAuditB420(t, conn, "ha.promote", `{"target":"skygate-standby"}`)
	insertAuditB420(t, conn, "ha.deploy.push", `{"target":"skygate-standby"}`)
	insertAuditB420(t, conn, "certs.upload", "out of scope for this table")

	rows, err := conn.Query(qDeployAuditEvents, 10)
	if err != nil {
		t.Fatalf("qDeployAuditEvents failed on SQLite: %v — the page would render an empty table", err)
	}
	defer rows.Close()

	var actions []string
	for rows.Next() {
		var (
			when   int64
			actor  string
			action string
			detail string
		)
		if err := rows.Scan(&when, &actor, &action, &detail); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if when <= 0 {
			t.Errorf("%s: created_at scanned as %d", action, when)
		}
		actions = append(actions, action)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(actions) != 2 {
		t.Fatalf("got %d deploy audit rows (%v), want the 2 ha.*/deploy.* rows", len(actions), actions)
	}
}

// TestB420_PreFixAuditSelectStillFailsOnSQLite pins the defect the fix
// removed. If a future SQLite driver version starts accepting
// `EXTRACT(EPOCH FROM created_at)::bigint`, this test tells us the
// rationale changed instead of letting the lesson rot into folklore.
func TestB420_PreFixAuditSelectStillFailsOnSQLite(t *testing.T) {
	conn := openAuditB420DB(t)
	insertAuditB420(t, conn, "certsync.pull", "x")

	_, err := conn.Query(`SELECT EXTRACT(EPOCH FROM created_at)::bigint, COALESCE(username, ''),
		        action, COALESCE(detail, '')
		 FROM audit_log LIMIT $1`, 10)
	if err == nil {
		t.Fatal("the pre-B420 SELECT succeeded on SQLite — the premise of this block is now wrong: re-measure and update the comments")
	}
	t.Logf("pre-B420 SELECT correctly rejected on SQLite: %v", err)
}

// TestB420_PreFixNowWriteStillFailsOnSQLite pins the same for the write
// side: `now()` into the INTEGER epoch columns fails on SQLite.
func TestB420_PreFixNowWriteStillFailsOnSQLite(t *testing.T) {
	conn := openAuditB420DB(t)
	_, err := conn.Exec(
		`INSERT INTO audit_log (user_id, username, action, detail, created_at)
		 VALUES (0, 'operator', $1, $2, now())`, "certsync.pull", "x")
	if err == nil {
		t.Fatal("the pre-B420 `now()` INSERT succeeded on SQLite — re-measure the rationale")
	}
	t.Logf("pre-B420 now() INSERT correctly rejected on SQLite: %v", err)
}
