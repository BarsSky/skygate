// Package auth — dbsource.go defines the DBSource
// interface for the auth Service. B210 (v1.5.0+,
// 2026-09-02) — closes the B203 hot-reload regression
// for the auth flow (login + display prefs + password
// change + API tokens).
//
// The regression
//
// The skygate-watchdog (B203) hot-reloads the pgxpool
// when cluster_database.current_dsn differs from the
// env DSN. On each swap, the old *sql.DB is Close()d
// in a goroutine; the new pool is re-pointed on the
// ResettableDB. The auth Service captured `*sql.DB` at
// construction time (DB: app.DB in main.go), so every
// `s.DB.X` query after the first swap used the closed
// pool → "sql: database is closed" → PostLogin failed
// silently (audit log shows login_fail or no audit at
// all depending on which call hits the closed pool
// first) → user sees the login page with
// "Неверные учётные данные" (Invalid credentials).
//
// The fix
//
// Replace `DB *sql.DB` (frozen pointer) with
// `DB DBSource` (live getter). Every call site reads
// the current pool via `s.dbc().Current()`. The
// ResettableDB from internal/db (B203) satisfies
// DBSource directly via its Current() method — main.go
// passes the wrapper instead of the captured *sql.DB.
//
// Why a local DBSource (matching the B208.1 admin
// pattern) instead of an import from internal/db
//
// B208.1's AGENTS.md notes that the shared shape
// (one method) is small and that "a future
// internal/db/dbsource.go consolidation is a separate
// refactor." B210 is that refactor — but the
// consolidation is split out as a follow-up because
// updating 5 packages at once is risky. The auth
// package uses its own DBSource for now; the next
// B-block (B210.x) collapses all 4 copies (admin,
// auth, healthz, elector — and the new internal/db one)
// into a single shared definition in internal/db/.
//
// Why this regression slipped past B-check + live-verify
//
// Same root cause as the B208.1 admin regression: the
// B-check scripts call handlers in sequence and the
// B203 watchdog's first hot-reload fires ~5s after
// container start. The B-checks themselves pass
// (handler is reachable, query shape is correct), but
// the actual SQL query on a closed pool fails silently.
// The user reported it via the symptoms (empty devices
// tab + unchanged theme) when they tried to use the
// restored data, and B209 e2e on the live agent
// surfaced the broker-elector state but not the auth
// flow (the e2e script doesn't login).

package auth

import "database/sql"

// DBSource is the minimum surface the auth Service
// needs to obtain the current *sql.DB. The ResettableDB
// wrapper from internal/db (B203) satisfies it via its
// Current() method. A plain *sql.DB also satisfies it
// (every call returns the same pointer) — the test
// suite uses a fixed adapter.
type DBSource interface {
	Current() *sql.DB
}

// dbc returns the auth Service's current *sql.DB. It's
// a one-liner that should be used at every call site:
//
//	rows, err := s.dbc().QueryContext(ctx, ...)
//
// instead of the captured-pointer pattern `s.DB.X`.
// The watchdog will call dbc on every handler call,
// transparently following B203 hot-reloads.
func (s *Service) dbc() *sql.DB {
	if s.DB == nil {
		return nil
	}
	return s.DB.Current()
}
