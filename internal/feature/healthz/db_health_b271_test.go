// B271 (2026-09-19) — the /db/health sampler must run DIALECT-CORRECT SQL.
//
// Live case (native SQLite install, host aro): every 30 s tick logged
//
//	db_health: tick: db_health: 4 query error(s): [server: SQL logic error:
//	no such function: pg_is_in_recovery (1) database.size: … current_database (1)
//	maintenance: … pg_stat_user_tables (1) xlog.current: … pg_current_wal_lsn (1)]
//
// The sampler is PostgreSQL-shaped (pg_is_in_recovery, pg_database_size,
// pg_stat_user_tables, pg_current_wal_lsn) and ran unchanged on a SQLite
// backend — hard-coded PG SQL leaking into the SQLite path (LESSONS L-16).
// Result: a permanently degraded DB-health badge and five useless errors per
// tick on a perfectly healthy database.
package healthz

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// sqliteSampler builds a sampler bound to a real in-memory SQLite database.
func sqliteSampler(t *testing.T) (*Sampler, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	cfg := DefaultDBHealthConfig()
	cfg.Dialect = "sqlite"
	cfg.Logger = func(string, ...any) {}
	return NewDBHealthSampler(cfg, NewFixedDBSource(db)), db
}

// TestB271_SQLiteDialectRunsNoPostgresSQL is the regression guard: the SQLite
// branch must succeed against a real SQLite database (the PG branch cannot).
func TestB271_SQLiteDialectRunsNoPostgresSQL(t *testing.T) {
	s, _ := sqliteSampler(t)
	sample := &DBHealthSample{}
	if err := s.collect(context.Background(), s.src.Current(), sample); err != nil {
		t.Fatalf("sqlite collect failed: %v (the SQL is not SQLite-compatible)", err)
	}
	if sample.Database.SizeBytes <= 0 {
		t.Errorf("size_bytes = %d, want > 0 (page_count * page_size)", sample.Database.SizeBytes)
	}
	if sample.Database.SizeHuman == "" {
		t.Error("size_human must be populated so the page can render a size")
	}
	if sample.Server.Version == "" {
		t.Error("sqlite_version() must populate the version field")
	}
	if sample.Server.IsReplica {
		t.Error("a file-backed SQLite database is never a replica")
	}
	// PostgreSQL-only panels stay empty rather than reporting a bogus value.
	if sample.XLog.Location != "" {
		t.Errorf("xlog_location = %q, want empty on SQLite (no WAL position)", sample.XLog.Location)
	}
	if sample.Replication.ReplayLSN != "" {
		t.Errorf("replay_lsn = %q, want empty on SQLite", sample.Replication.ReplayLSN)
	}
}

// TestB271_PostgresDialectStillFailsOnSQLite proves the branch is real: the PG
// queries cannot run against SQLite, which is exactly why the dialect matters.
func TestB271_PostgresDialectStillFailsOnSQLite(t *testing.T) {
	s, db := sqliteSampler(t)
	s.cfg.Dialect = "postgres" // force the PG branch
	err := s.collect(context.Background(), db, &DBHealthSample{})
	if err == nil {
		t.Fatal("the PostgreSQL branch succeeded on SQLite — the dialect switch is not wired to the SQL")
	}
	if !strings.Contains(err.Error(), "pg_is_in_recovery") {
		t.Errorf("error = %v, want it to name pg_is_in_recovery (proving the PG SQL ran)", err)
	}
}

// TestB271_DefaultDialectIsPostgres keeps the historical behaviour for every
// caller that does not set Dialect (all pre-B271 tests + embedded users).
func TestB271_DefaultDialectIsPostgres(t *testing.T) {
	if got := DefaultDBHealthConfig().Dialect; got != "postgres" {
		t.Fatalf("DefaultDBHealthConfig().Dialect = %q, want postgres", got)
	}
	zeroCfg := DBHealthConfig{}
	if zeroCfg.isSQLite() {
		t.Error("the zero config must not select the SQLite branch")
	}
	sqliteCfg := DBHealthConfig{Dialect: "sqlite"}
	if !sqliteCfg.isSQLite() {
		t.Error("Dialect=sqlite must select the SQLite branch")
	}
}
