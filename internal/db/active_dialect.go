// internal/db/active_dialect.go — the process-wide active SQL dialect.
//
// WHY THIS EXISTS
// ---------------
// v1.5.4 restored SQLite as a first-class backend, but the dialect
// "shims" written for the v1.3.0 PG-only era were never re-forked:
// now_unix.go, placeholders.go and on_conflict.go were generated
// wrappers that unconditionally delegated to the *_postgres.go
// implementation. The most damaging one was nowUnixSQL(), which returns
//
//	EXTRACT(EPOCH FROM now())::bigint
//
// — valid on PostgreSQL, a hard error on SQLite:
//
//	SQL logic error: near "FROM": syntax error
//
// That single expression is spliced into a dozen write paths
// (node_owner_map upserts, global_settings, exit_node_prefs, secrets,
// telegram_login_tokens, backup config), so on SQLite essentially every
// timestamped write failed. Verified by running it against a real
// :memory: database, not by reading the code.
//
// The two backends therefore need one piece of runtime knowledge: which
// dialect is this process actually talking to. skygate holds exactly one
// database connection per process, so a process-wide value set at open
// time is both sufficient and race-free in practice.
//
// DESIGN NOTES
// ------------
//   - The default is DialectPostgres, i.e. the pre-fix behaviour. A
//     caller that never opens a DB (unit tests exercising pure helpers)
//     keeps the old output, so this change is inert until a connection
//     is registered.
//   - The value is set from registerBackend (driver.go), which BOTH open
//     paths already call: openDSNPing for the dual-dialect runtime path
//     and openSQLite for direct SQLite opens. That keeps the wiring in
//     one place instead of duplicating it per caller.
//   - Placeholders deliberately do NOT branch: SQLite accepts `$N` as a
//     named parameter and modernc.org/sqlite binds `$NNN` by Go
//     argument ordinal, while PostgreSQL rejects `?` outright. `$N` is
//     therefore the one universal form, and placeholdersList keeps
//     emitting it for both backends (see placeholders.go).
//   - Query strings built at PACKAGE INIT (const/var blocks) cannot see
//     this value, because init runs before any connection is opened.
//     Any such string that embeds dialect-specific SQL must be a
//     function instead — see qInsertOrReplaceNodeOwner /
//     qUpdateNodeOwnerTag in queries.go.
package db

import "sync"

var (
	activeDialectMu sync.RWMutex
	activeDialect   = DialectPostgres
)

// SetActiveDialect records the dialect this process is connected to.
// Called from registerBackend on every successful open.
func SetActiveDialect(k DialectKind) {
	activeDialectMu.Lock()
	activeDialect = k
	activeDialectMu.Unlock()
}

// ActiveDialect returns the dialect recorded by the most recent open.
// Defaults to DialectPostgres when nothing has been opened yet.
func ActiveDialect() DialectKind {
	activeDialectMu.RLock()
	defer activeDialectMu.RUnlock()
	return activeDialect
}

// backendToDialectKind maps a Backend (what OpenDSN registers) onto the
// DialectKind the SQL-fragment helpers branch on.
func backendToDialectKind(b Backend) DialectKind {
	if b == BackendSQLite {
		return DialectSQLite
	}
	return DialectPostgres
}
