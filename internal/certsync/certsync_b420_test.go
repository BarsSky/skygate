// internal/certsync/certsync_b420_test.go — the certsync audit trail
// must actually be written.
//
// B420 root cause: writeAudit spliced PostgreSQL's `now()` into
// audit_log.created_at, which is an INTEGER epoch column in BOTH
// migration chains. PostgreSQL rejected the statement (type mismatch),
// SQLite rejected it too ("no such function: now"), and because the
// helper is best-effort the only trace was a log line — so a cert pull
// happened and left no record on any backend.
//
// The test calls the REAL writeAudit against a REAL SQLite database and
// pins that the pre-fix shape still fails there.
package certsync

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"skygate/internal/db"
)

func openCertsyncB420DB(t *testing.T) *sql.DB {
	t.Helper()
	conn, err := db.OpenFastSQLiteForTest(filepath.Join(t.TempDir(), "b420-certsync.db"))
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

// TestB420_WriteAuditPersistsOnSQLite proves the row lands with a usable
// epoch.
func TestB420_WriteAuditPersistsOnSQLite(t *testing.T) {
	conn := openCertsyncB420DB(t)
	c := &CertSync{}
	c.writeAudit(context.Background(), CertSyncDeps{DB: conn}, "certsync.pull", "sha256=deadbeef")

	var (
		action  string
		detail  string
		created int64
	)
	if err := conn.QueryRow(
		`SELECT action, detail, created_at FROM audit_log WHERE action = $1`, "certsync.pull",
	).Scan(&action, &detail, &created); err != nil {
		t.Fatalf("certsync audit row missing: %v — the pull left no record", err)
	}
	if detail != "sha256=deadbeef" {
		t.Fatalf("detail = %q", detail)
	}
	if now := time.Now().Unix(); created <= 0 || created < now-86400 || created > now+86400 {
		t.Fatalf("created_at = %d, want a plausible epoch (now=%d)", created, now)
	}
}

// TestB420_PreFixNowWriteStillFailsOnSQLite pins the defect class.
func TestB420_PreFixNowWriteStillFailsOnSQLite(t *testing.T) {
	conn := openCertsyncB420DB(t)
	_, err := conn.Exec(
		`INSERT INTO audit_log (user_id, username, action, detail, created_at)
		 VALUES (0, 'certsync', $1, $2, now())`, "certsync.pull", "x")
	if err == nil {
		t.Fatal("the pre-B420 `now()` INSERT succeeded on SQLite — re-measure and update the rationale")
	}
	t.Logf("pre-B420 now() INSERT correctly rejected on SQLite: %v", err)
}
