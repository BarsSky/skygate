// Package dbmigrate — DB migration workflow (Phase 1.4 of
// docs/internal/cluster-management.md).
//
// v1.5.0+ / B198.
//
// The DB migration workflow is a state machine that moves
// skygate's PostgreSQL from one host to another. The 6 steps
// (PreCheck, Dump, Restore, Verify, Flip, Cleanup) are
// registered via the same self-registering init() pattern
// B194 uses for the auto-deploy framework — adding a new
// step is one new file in steps/ + one RegisterStep call.
//
// Each step has:
//   - Name, Description
//   - Run(ctx, *MigrationContext) error   (does the work)
//   - Rollback(ctx, *MigrationContext) error (undoes if a
//     later step fails)
//   - IsOptional() bool    (skipped on failure without abort)
//   - DependsOn() []string (ordering constraints, informational)
//
// Live progress is emitted via SSE (handlers.go) so the
// /admin/database page can show "dumping..." → "restoring..."
// → "verifying..." in real time.
//
// The workflow does NOT change headscale config (preauth keys,
// ACLs, etc.) so it doesn't require operator approval per the
// "headscale config change" rule. It DOES change skygate's
// cluster_database (per D8) and the local .env (where
// skygate runs), which is in scope.
package dbmigrate

import (
	"context"
	"database/sql"
	"time"
)

// StepStatus is the status of a single step in a migration run.
type StepStatus string

const (
	StepPending   StepStatus = "pending"
	StepRunning   StepStatus = "running"
	StepSuccess   StepStatus = "success"
	StepFailed    StepStatus = "failed"
	StepSkipped   StepStatus = "skipped"
	StepRolledBack StepStatus = "rolled_back"
)

// DeployStep is the interface every step implements. The
// pattern matches B194's internal/deployrun/steps/ so future
// readers can transfer context easily.
type DeployStep interface {
	Name() string
	Description() string
	Run(ctx context.Context, mc *MigrationContext) error
	Rollback(ctx context.Context, mc *MigrationContext) error
	IsOptional() bool
	DependsOn() []string
}

// MigrationStatus is the status of the whole run.
type MigrationStatus string

const (
	RunPending   MigrationStatus = "pending"
	RunRunning   MigrationStatus = "running"
	RunSuccess   MigrationStatus = "success"
	RunFailed    MigrationStatus = "failed"
	RunRolledBack MigrationStatus = "rolled_back"
)

// MigrationContext is the per-run state. The framework
// constructs one and passes it to every step. Steps can
// stash intermediate results in fields here (the dump file
// path, the source row count, etc.) for downstream steps.
type MigrationContext struct {
	// Source DSN (where skygate is reading from today).
	// From the live env, sourced at run start.
	SourceDSN string

	// Target DSN (where skygate will read from after flip).
	// From the migration form.
	TargetDSN string

	// Target host/port/dbname/user (parsed for the audit
	// log and for the verify step's "connect to target"
	// reachability check).
	TargetHost     string
	TargetPort     string
	TargetDBName   string
	TargetUsername string
	TargetSSLMode  string

	// Dump file. The Dump step writes a `pg_dump -Fc`
	// file here; the Restore step reads from it.
	// Default: /tmp/skygate-migrate-{run_id}.dump
	DumpFile string

	// Stashed by Verify step. Source row count, target
	// row count, mismatch (if any).
	SourceRowCount int64
	TargetRowCount int64
	RowCountMatch  bool

	// Operator who initiated the run (for audit + Flash).
	Operator string

	// Run ID (set by the framework at start).
	RunID int64

	// Started / finished wall times.
	StartedAt  time.Time
	FinishedAt time.Time

	// DB is the live *sql.DB the framework (and steps that
	// need it, like the Flip step that writes to
	// cluster_database) use for the migration's own
	// bookkeeping tables. Set by the framework at start.
	DB interface {
		Exec(query string, args ...any) (sql.Result, error)
		Query(query string, args ...any) (*sql.Rows, error)
		QueryRow(query string, args ...any) *sql.Row
	}
}

// MigrationRun is the DB row representing a single migration
// run. Persisted in dbmigrate_run.
type MigrationRun struct {
	ID          int64
	ClusterID   string
	SourceDSN   string
	TargetDSN   string
	Operator    string
	Status      MigrationStatus
	StartedAt   time.Time
	FinishedAt  *time.Time
	Error       string
	CreatedAt   time.Time
}

// MigrationStep is the DB row for a single step within a run.
// Persisted in dbmigrate_step.
type MigrationStep struct {
	ID          int64
	RunID       int64
	StepName    string
	Ordinal     int
	Status      StepStatus
	StartedAt   *time.Time
	FinishedAt  *time.Time
	DurationMs  int64
	Logs        string // JSON array of log lines
	Error       string
	Metadata    string // JSON: step-specific results
}

// StepLog is a single log line emitted by a step. The
// framework writes these to dbmigrate_step.logs and emits
// them via SSE.
type StepLog struct {
	At    time.Time `json:"at"`
	Level string    `json:"level"` // "info", "warn", "error"
	Msg   string    `json:"msg"`
}
