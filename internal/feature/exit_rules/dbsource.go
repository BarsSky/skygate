// Package exit_rules — dbsource.go now just hosts the
// per-Service `dbc()` helper. The DBSource interface
// itself moved to internal/db/dbsource.go in B210.1
// (consolidation of the 5 local copies introduced by
// B208.1 + B210 + earlier B204/B206). The Service.DB
// field type stays `db.DBSource` (imported from
// skygate/internal/db) — see service.go.

package exit_rules

import (
	"database/sql"

	"skygate/internal/db"
)

type DBSource = db.DBSource

// dbc returns the exit_rules Service's current *sql.DB.
// See skygate/internal/db.DBCurrent for the free-function
// variant (no Service receiver required).
func (s *Service) dbc() *sql.DB {
	if s.DB == nil {
		return nil
	}
	return s.DB.Current()
}
