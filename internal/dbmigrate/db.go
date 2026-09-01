// Package dbmigrate — db.go provides the data-access helpers
// that the rest of the package (and the admin package) use
// to read migration runs. Kept separate from framework.go
// to keep the latter focused on the orchestrator logic.

package dbmigrate

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// LoadRun reads a single run + its steps from the DB. Steps
// are in ordinal order. Used by:
//   - admin.GetAdminDatabaseMigrateRun (renders the run page)
//   - the SSE stream's "initial state" payload (planned)
//
// Returns ErrRunNotFound when the id doesn't exist.
func LoadRun(d *sql.DB, id int64) (*MigrationRun, []MigrationStep, error) {
	var r MigrationRun
	var finishedAt *sql.NullTime
	err := d.QueryRow(`
		SELECT id, cluster_id, source_dsn, target_dsn, operator,
		       status, started_at, finished_at, error, created_at
		  FROM dbmigrate_run WHERE id = $1
	`, id).Scan(&r.ID, &r.ClusterID, &r.SourceDSN, &r.TargetDSN,
		&r.Operator, &r.Status, &r.StartedAt, &finishedAt, &r.Error, &r.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, ErrRunNotFound
		}
		return nil, nil, fmt.Errorf("load run %d: %w", id, err)
	}
	if finishedAt.Valid {
		r.FinishedAt = &finishedAt.Time
	}
	rows, err := d.Query(`
		SELECT id, run_id, step_name, ordinal, status,
		       started_at, finished_at, duration_ms, logs, error, metadata
		  FROM dbmigrate_step WHERE run_id = $1
		 ORDER BY ordinal
	`, id)
	if err != nil {
		return nil, nil, fmt.Errorf("load steps: %w", err)
	}
	defer rows.Close()
	var steps []MigrationStep
	for rows.Next() {
		var st MigrationStep
		var sa, fa *time.Time
		if err := rows.Scan(&st.ID, &st.RunID, &st.StepName, &st.Ordinal,
			&st.Status, &sa, &fa, &st.DurationMs,
			&st.Logs, &st.Error, &st.Metadata); err != nil {
			return nil, nil, fmt.Errorf("scan step: %w", err)
		}
		st.StartedAt = sa
		st.FinishedAt = fa
		steps = append(steps, st)
	}
	return &r, steps, nil
}

// ErrRunNotFound is returned by LoadRun when the id
// doesn't exist (vs an actual DB error).
var ErrRunNotFound = errors.New("dbmigrate_run not found")
