// v1.5.0 (B194) — auto-deploy framework tables.
//
// Adds the deploy_runs + deploy_run_steps tables that
// the B194 framework uses to persist its per-run state.
// Each "Add + auto-deploy standby" operator action
// creates one deploy_runs row + N deploy_run_steps
// rows (one per step in the registry).
//
// The framework writes per-step state (running →
// success/failed/skipped) as the run progresses, and
// the operator sees the live progress via the SSE
// stream at /admin/deploys/{id}/stream.
//
// Idempotent: re-running this migration is a no-op
// (CREATE TABLE IF NOT EXISTS, CREATE INDEX IF NOT
// EXISTS).

package db

import (
	"database/sql"
)

// migrateV063PG — v1.5.0 (B194) — deploy_runs + deploy_run_steps.
//
// Schema:
//
//	deploy_runs: one row per auto-deploy run
//	  id          BIGSERIAL PRIMARY KEY
//	  type        TEXT NOT NULL            -- "standby" | "replica" | "drill"
//	  status      TEXT NOT NULL            -- pending|running|success|failed|rolled_back
//	  started_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
//	  finished_at TIMESTAMPTZ
//	  operator    TEXT NOT NULL            -- username of the admin who triggered
//	  form_data   TEXT                     -- JSON-encoded url.Values
//	  hostname    TEXT NOT NULL            -- denormalized for query convenience
//	  error       TEXT                     -- error message on failure/rollback
//
//	deploy_run_steps: one row per step per run
//	  id          BIGSERIAL PRIMARY KEY
//	  run_id      BIGINT NOT NULL REFERENCES deploy_runs(id) ON DELETE CASCADE
//	  step_name   TEXT NOT NULL            -- "GeneratePreauthKey" etc.
//	  ordinal     INT  NOT NULL            -- 0-based execution order
//	  status      TEXT NOT NULL            -- pending|running|success|failed|skipped|rolled_back
//	  started_at  TIMESTAMPTZ
//	  finished_at TIMESTAMPTZ
//	  duration_ms BIGINT
//	  logs        TEXT                     -- JSON array of log lines
//	  error       TEXT
//	  metadata    TEXT                     -- JSON: {"key_id": "...", "expires": "..."}
//	  UNIQUE(run_id, step_name)
func migrateV063PG(d *sql.DB) error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS deploy_runs (
			id          BIGSERIAL PRIMARY KEY,
			type        TEXT NOT NULL,
			status      TEXT NOT NULL,
			started_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			finished_at TIMESTAMPTZ,
			operator    TEXT NOT NULL DEFAULT '',
			form_data   TEXT NOT NULL DEFAULT '',
			hostname    TEXT NOT NULL DEFAULT '',
			error       TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS deploy_run_steps (
			id          BIGSERIAL PRIMARY KEY,
			run_id      BIGINT NOT NULL REFERENCES deploy_runs(id) ON DELETE CASCADE,
			step_name   TEXT NOT NULL,
			ordinal     INT  NOT NULL,
			status      TEXT NOT NULL,
			started_at  TIMESTAMPTZ,
			finished_at TIMESTAMPTZ,
			duration_ms BIGINT NOT NULL DEFAULT 0,
			logs        TEXT NOT NULL DEFAULT '[]',
			error       TEXT,
			metadata    TEXT,
			UNIQUE(run_id, step_name)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_deploy_runs_started ON deploy_runs(started_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_deploy_runs_hostname ON deploy_runs(hostname)`,
		`CREATE INDEX IF NOT EXISTS idx_deploy_run_steps_run ON deploy_run_steps(run_id, ordinal)`,
	}
	for _, q := range queries {
		if _, err := d.Exec(q); err != nil {
			return err
		}
	}
	return nil
}
