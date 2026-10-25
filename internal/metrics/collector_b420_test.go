// internal/metrics/collector_b420_test.go — skygate_db_size_bytes must
// report a real size on a SQLite deployment.
//
// B420 root cause: both size probes ran
// `SELECT pg_database_size(current_database())` unconditionally. On
// SQLite that is "no such function: pg_database_size", the error was
// swallowed by queryDBSizeBytes and the gauge stayed at 0 — a metric the
// operator alerts on, silently lying at exactly the moment (a small or
// empty database) it matters.
//
// The test drives the REAL DBPoolSource against a REAL SQLite database
// and cross-checks the answer against `page_count × page_size` computed
// independently here.
package metrics

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	skygatedb "skygate/internal/db"
)

func openMetrics420DB(t *testing.T) *sql.DB {
	t.Helper()
	conn, err := skygatedb.OpenFastSQLiteForTest(filepath.Join(t.TempDir(), "b420-metrics.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	prev := skygatedb.ActiveDialect()
	skygatedb.SetActiveDialect(skygatedb.DialectSQLite)
	t.Cleanup(func() { skygatedb.SetActiveDialect(prev) })
	if err := skygatedb.ApplyMigrations(conn, skygatedb.DialectSQLite); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	return conn
}

// TestB420_DBSizeBytesOnSQLite cross-checks the dialect-aware probe.
func TestB420_DBSizeBytesOnSQLite(t *testing.T) {
	conn := openMetrics420DB(t)
	src := &DBPoolSource{DB: skygatedb.FixedDBSource{DB: conn}, Cluster: "skygate-staging"}

	got, err := src.DBSizeBytes(context.Background())
	if err != nil {
		t.Fatalf("DBSizeBytes on SQLite: %v — the gauge would stay at 0", err)
	}
	if got <= 0 {
		t.Fatalf("DBSizeBytes on SQLite = %d, want > 0 for a migrated database", got)
	}

	// Cross-check against the raw pragmas so the test fails if the
	// helper ever returns a number that is not a real page count.
	var pageCount, pageSize int64
	if err := conn.QueryRow(`PRAGMA page_count`).Scan(&pageCount); err != nil {
		t.Fatalf("page_count: %v", err)
	}
	if err := conn.QueryRow(`PRAGMA page_size`).Scan(&pageSize); err != nil {
		t.Fatalf("page_size: %v", err)
	}
	if want := pageCount * pageSize; got != want {
		t.Fatalf("DBSizeBytes = %d, want page_count(%d) × page_size(%d) = %d", got, pageCount, pageSize, want)
	}
}

// TestB420_QueryDBSizeBytesThroughCollectorPath covers the
// SourceProvider fallback branch (not just the DBSizeBytes interface
// path), because that is the branch that used the raw PG statement.
func TestB420_QueryDBSizeBytesThroughCollectorPath(t *testing.T) {
	conn := openMetrics420DB(t)
	src := &DBPoolSource{DB: skygatedb.FixedDBSource{DB: conn}, Cluster: "skygate-staging"}

	size, ok := queryDBSizeBytes(context.Background(), src)
	if !ok {
		t.Fatal("queryDBSizeBytes reported failure on SQLite — the gauge would not update at all")
	}
	if size <= 0 {
		t.Fatalf("queryDBSizeBytes = %v, want > 0", size)
	}
}

// TestB420_PreFixSizeProbeStillFailsOnSQLite pins the defect class.
func TestB420_PreFixSizeProbeStillFailsOnSQLite(t *testing.T) {
	conn := openMetrics420DB(t)
	var size int64
	err := conn.QueryRowContext(context.Background(), `SELECT pg_database_size(current_database())`).Scan(&size)
	if err == nil {
		t.Fatal("the pre-B420 size probe succeeded on SQLite — re-measure and update the rationale")
	}
	t.Logf("pre-B420 pg_database_size probe correctly rejected on SQLite: %v", err)
}
