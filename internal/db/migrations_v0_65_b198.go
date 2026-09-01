// v1.5.0+ (B198) — DB migration workflow tables.
//
// Phase 1.4 of docs/internal/cluster-management.md. The
// migration framework lives in internal/dbmigrate/. This
// migration adds the persistence layer (dbmigrate_run +
// dbmigrate_step).
//
// Idempotent: CREATE TABLE IF NOT EXISTS. Safe to re-run.

package db

import (
	"database/sql"
)

// migrateV065PG — v1.5.0+ (B198) — dbmigrate_run + dbmigrate_step.
//
//	dbmigrate_run: one row per migration attempt
//	  id           BIGSERIAL PRIMARY KEY
//	  cluster_id   TEXT NOT NULL
//	  source_dsn   TEXT NOT NULL               -- redacted (password replaced by ***)
//	  target_dsn   TEXT NOT NULL               -- redacted
//	  operator     TEXT NOT NULL               -- who initiated
//	  status       TEXT NOT NULL               -- pending|running|success|failed|rolled_back
//	  started_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
//	  finished_at  TIMESTAMPTZ
//	  error        TEXT
//	  created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
//
//	dbmigrate_step: one row per step per run
//	  id           BIGSERIAL PRIMARY KEY
//	  run_id       BIGINT NOT NULL REFERENCES dbmigrate_run(id) ON DELETE CASCADE
//	  step_name    TEXT NOT NULL               -- "precheck", "dump", "restore", "verify", "flip", "cleanup"
//	  ordinal      INT NOT NULL                -- 0-based
//	  status       TEXT NOT NULL               -- pending|running|success|failed|skipped|rolled_back
//	  started_at   TIMESTAMPTZ
//	  finished_at  TIMESTAMPTZ
//	  duration_ms  BIGINT NOT NULL DEFAULT 0
//	  logs         TEXT NOT NULL DEFAULT '[]'   -- JSON array of {at, level, msg}
//	  error        TEXT
//	  metadata     TEXT                         -- JSON: step-specific results
//	  UNIQUE(run_id, step_name)
func migrateV065PG(d *sql.DB) error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS dbmigrate_run (
			id          BIGSERIAL PRIMARY KEY,
			cluster_id  TEXT NOT NULL DEFAULT 'skygate-staging',
			source_dsn  TEXT NOT NULL DEFAULT '',
			target_dsn  TEXT NOT NULL DEFAULT '',
			operator    TEXT NOT NULL DEFAULT '',
			status      TEXT NOT NULL DEFAULT 'pending',
			started_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			finished_at TIMESTAMPTZ,
			error       TEXT NOT NULL DEFAULT '',
			created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_dbmigrate_run_started ON dbmigrate_run(started_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_dbmigrate_run_status ON dbmigrate_run(status)`,
		`CREATE TABLE IF NOT EXISTS dbmigrate_step (
			id          BIGSERIAL PRIMARY KEY,
			run_id      BIGINT NOT NULL REFERENCES dbmigrate_run(id) ON DELETE CASCADE,
			step_name   TEXT NOT NULL,
			ordinal     INT NOT NULL,
			status      TEXT NOT NULL DEFAULT 'pending',
			started_at  TIMESTAMPTZ,
			finished_at TIMESTAMPTZ,
			duration_ms BIGINT NOT NULL DEFAULT 0,
			logs        TEXT NOT NULL DEFAULT '[]',
			error       TEXT NOT NULL DEFAULT '',
			metadata    TEXT,
			UNIQUE(run_id, step_name)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_dbmigrate_step_run ON dbmigrate_step(run_id, ordinal)`,
	}
	for _, q := range queries {
		if _, err := d.Exec(q); err != nil {
			return err
		}
	}
	return nil
}
