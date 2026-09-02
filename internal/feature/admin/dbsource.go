// Package admin — dbsource.go now just hosts the
// per-Service `dbc()` helper. The DBSource interface
// itself moved to internal/db/dbsource.go in B210.1
// (consolidation of the 5 local copies introduced by
// B208.1 + B210 + earlier B204/B206). The Service.DB
// field type stays `db.DBSource` (imported from
// skygate/internal/db) — see service.go.
//
// B210.1 (v1.5.0+, 2026-09-02): closes the "5 copies of
// the same one-method interface" duplication. The shape
// is unchanged — admin (B208.1) was already the
// canonical copy; B210 + B204 + B206 each declared the
// same `interface { Current() *sql.DB }` locally. They
// now all import it from internal/db.
//
// The `dbc()` method stays here because it references
// the Service's DB field, which is in this package. For
// callers that don't have a Service (background tasks,
// scripts, tests), use skygate/internal/db.DBCurrent(s)
// — it does the same thing without the Service receiver.

package admin

import (
	"database/sql"

	"skygate/internal/db"
)

// Re-export the interface so the Service struct's
// `DB db.DBSource` field type is discoverable from this
// package's docs (gopls shows the alias).
type DBSource = db.DBSource

// dbc returns the admin Service's current *sql.DB.
// Equivalent to skygate/internal/db.DBCurrent(s.DB) but
// keeps the most-common-call-site path one method-call
// short:
//
//	rows, err := s.dbc().QueryContext(ctx, ...)
//
// instead of `rows, err := skygate/internal/db.DBCurrent(s.DB).QueryContext(ctx, ...)`.
// The watchdog calls dbc on every handler call,
// transparently following the B203 hot-reload.
func (s *Service) dbc() *sql.DB {
	if s.DB == nil {
		return nil
	}
	return s.DB.Current()
}
