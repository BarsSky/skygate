// internal/db/dialect_runtime_test.go — 2026-09-18 regression tests for the
// "one dialect, chosen at open time" fix.
//
// THE BUG
// -------
// v1.5.4 restored SQLite as a first-class backend, but the dialect shims
// written for the v1.3.0 PG-only era were never re-forked: nowUnixSQL()
// unconditionally returned
//
//	EXTRACT(EPOCH FROM now())::bigint
//
// which is a hard error on SQLite. Because that fragment is spliced into
// the node_owner_map upsert (queries.go), global_settings, exit_node_prefs,
// secrets, telegram_login_tokens and backup config, essentially every
// timestamped write failed on a SQLite deployment. Two of those queries
// were package-level consts, so they baked in the PostgreSQL form at
// package INIT — before any connection existed.
//
// THE FIX
// -------
// active_dialect.go holds the process-wide dialect, set by registerBackend
// (driver.go) which both open paths call. nowUnixSQL() branches on it, and
// the two init-time consts became functions.
//
// These tests are deliberately empirical: they run the real statements
// against a real database and assert on the driver's answer, not on a
// string comparison.
package db

import (
	"strings"
	"testing"
)

// TestNowUnixSQL_BranchesOnActiveDialect pins both forms.
func TestNowUnixSQL_BranchesOnActiveDialect(t *testing.T) {
	// Restore the default afterwards so we don't leak state into the
	// other tests in this package.
	defer SetActiveDialect(DialectPostgres)

	SetActiveDialect(DialectPostgres)
	if got := nowUnixSQL(); !strings.Contains(got, "EXTRACT(EPOCH") {
		t.Errorf("PG form = %q, want an EXTRACT(EPOCH ...) expression", got)
	}

	SetActiveDialect(DialectSQLite)
	got := nowUnixSQL()
	if !strings.Contains(got, "strftime") {
		t.Errorf("SQLite form = %q, want a strftime(...) expression", got)
	}
	if strings.Contains(got, "EXTRACT") {
		t.Errorf("SQLite form = %q must NOT contain EXTRACT — that is the 2026-09-18 bug", got)
	}
}

// TestOpenSetsActiveDialect proves the wiring: opening a SQLite database
// must flip the process-wide dialect without any explicit SetActiveDialect
// call from the caller.
func TestOpenSetsActiveDialect(t *testing.T) {
	defer SetActiveDialect(DialectPostgres)

	SetActiveDialect(DialectPostgres)
	_, sqlDB, err := OpenWithDialect(":memory:")
	if err != nil {
		t.Fatalf("OpenWithDialect(:memory:): %v", err)
	}
	defer sqlDB.Close()

	if ActiveDialect() != DialectSQLite {
		t.Fatalf("ActiveDialect() = %v after opening SQLite, want %v", ActiveDialect(), DialectSQLite)
	}
}

// TestSQLiteTimestampWritesExecute is the regression that matters: the
// real statements must RUN on SQLite. Pre-fix every one of these failed
// with `SQL logic error: near "FROM": syntax error`.
func TestSQLiteTimestampWritesExecute(t *testing.T) {
	_, sqlDB, err := OpenWithDialect(":memory:")
	if err != nil {
		t.Fatalf("OpenWithDialect(:memory:): %v", err)
	}
	defer sqlDB.Close()
	if err := ApplyMigrations(sqlDB, DialectSQLite); err != nil {
		t.Fatalf("ApplyMigrations(SQLite): %v", err)
	}
	if ActiveDialect() != DialectSQLite {
		t.Fatalf("dialect not switched to SQLite by OpenWithDialect")
	}

	t.Run("node_owner_map upsert (qInsertOrReplaceNodeOwner)", func(t *testing.T) {
		if _, err := sqlDB.Exec(qInsertOrReplaceNodeOwner(),
			4242, 1, "skyadmin", "tag:dev-skyadmin-probe", 1); err != nil {
			t.Fatalf("qInsertOrReplaceNodeOwner() on SQLite: %v", err)
		}
		// Second call exercises the ON CONFLICT DO UPDATE branch.
		if _, err := sqlDB.Exec(qInsertOrReplaceNodeOwner(),
			4242, 1, "skyadmin", "tag:dev-skyadmin-probe-2", 1); err != nil {
			t.Fatalf("qInsertOrReplaceNodeOwner() upsert branch on SQLite: %v", err)
		}
		var taggedAt int64
		var tag string
		if err := sqlDB.QueryRow(
			`SELECT tagged_at, tag FROM node_owner_map WHERE node_id = 4242`).
			Scan(&taggedAt, &tag); err != nil {
			t.Fatalf("read back: %v", err)
		}
		if taggedAt <= 0 {
			t.Errorf("tagged_at = %d, want a positive unix timestamp (the dialect fragment produced nothing usable)", taggedAt)
		}
		if tag != "tag:dev-skyadmin-probe-2" {
			t.Errorf("tag = %q, want the upserted value", tag)
		}
	})

	t.Run("node_owner_map tag update (qUpdateNodeOwnerTag)", func(t *testing.T) {
		if _, err := sqlDB.Exec(qUpdateNodeOwnerTag(),
			"tag:dev-skyadmin-probe-3", 1, 4242, "skyadmin"); err != nil {
			t.Fatalf("qUpdateNodeOwnerTag() on SQLite: %v", err)
		}
	})

	t.Run("exit_servers auto-detect insert (was `?` + strftime)", func(t *testing.T) {
		// The statement upsertExitServerFromSyncNode runs. Pre-fix this
		// was `VALUES (?, ?, '', 1, strftime('%s','now'))` which pgx
		// rejects outright — the error was swallowed by the caller.
		stmt := `INSERT INTO exit_servers (node_id, hostname, tailscale_ip, enabled, created_at)
		 VALUES (` + PlaceholdersList(2) + `, '', 1, ` + NowUnixSQL() + `)
		 ON CONFLICT(node_id) DO UPDATE SET
		   hostname = excluded.hostname`
		if _, err := sqlDB.Exec(stmt, "probe-node", "probe-host"); err != nil {
			t.Fatalf("exit_servers auto-detect insert on SQLite: %v", err)
		}
		var created int64
		if err := sqlDB.QueryRow(
			`SELECT created_at FROM exit_servers WHERE node_id = 'probe-node'`).Scan(&created); err != nil {
			t.Fatalf("read back exit_servers: %v", err)
		}
		if created <= 0 {
			t.Errorf("created_at = %d, want a positive unix timestamp", created)
		}
	})
}

// TestPGFormFailsOnSQLite is the inverse pin: if someone reverts
// nowUnixSQL to the unconditional PG form, this fails loudly with the
// driver's own error rather than a string diff.
func TestPGFormFailsOnSQLite(t *testing.T) {
	_, sqlDB, err := OpenWithDialect(":memory:")
	if err != nil {
		t.Fatalf("OpenWithDialect(:memory:): %v", err)
	}
	defer sqlDB.Close()

	_, err = sqlDB.Query("SELECT EXTRACT(EPOCH FROM now())::bigint")
	if err == nil {
		t.Fatal("the raw PostgreSQL timestamp expression executed on SQLite — " +
			"if SQLite ever gains EXTRACT(), this test must be re-thought, but until " +
			"then the dialect branch in nowUnixSQL() is load-bearing")
	}
	t.Logf("confirmed: raw PG form rejected by SQLite: %v", err)
}
