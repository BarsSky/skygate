// Package exit_rules — dbsource.go defines the DBSource
// interface for the exit_rules Service. B210 (v1.5.0+,
// 2026-09-02) — fixes the B203 hot-reload regression
// for the /my/exit-rules + /admin/exit-rules/* routes.
//
// The regression
//
// The exit_rules Service captured `*sql.DB` at
// construction time. After the B203 watchdog's first
// swap on container start, the captured pointer points
// at a closed pool → "sql: database is closed" → every
// exit-rule handler 500s. The most visible routes are
// /my/exit-rules (preference UI) and
// /admin/exit-rules/* (admin rule management +
// sync / cleanup / preferred-check).
//
// The fix
//
// Replace `DB *sql.DB` (frozen pointer) with
// `DB DBSource` (live getter). `s.dbc()` returns the
// current pool on every call. The ResettableDB from
// internal/db (B203) satisfies DBSource directly.
// main.go passes the ResettableDB (named `d`) instead
// of `app.DB`. The internal call sites (form_my.go's
// 28 usages, sync.go's 19, form_admin.go's 7, etc.)
// all read via s.dbc().

package exit_rules

import "database/sql"

// DBSource is the minimum surface the exit_rules
// Service needs to obtain the current *sql.DB. The
// ResettableDB wrapper from internal/db (B203)
// satisfies it via its Current() method.
type DBSource interface {
	Current() *sql.DB
}

// dbc returns the exit_rules Service's current
// *sql.DB. Used at every call site (form_my.go,
// sync.go, form_admin.go, store.go, nodes_load.go,
// api.go, cleanup.go, form_reapply.go,
// form_rollback.go, routescript_data.go) so the
// watchdog's hot-reload is followed transparently.
func (s *Service) dbc() *sql.DB {
	if s.DB == nil {
		return nil
	}
	return s.DB.Current()
}
