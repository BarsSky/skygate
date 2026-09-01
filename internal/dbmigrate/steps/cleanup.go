// Package dbmigrate/steps — Cleanup drops the source DB
// (the OLD skygate DB) after a successful migration. This
// is OPTIONAL — many operators prefer to keep the old DB
// around for a few days as a safety net (the dump file
// is also kept, but having a live replica is sometimes
// useful for forensics).
//
// Phase 1.4 limitation: this is a no-op until B200 wires
// the local psql / pg_terminate + drop sequence.

package steps

import (
	"context"
	"fmt"
	"os/exec"

	"skygate/internal/dbmigrate"
)

func init() {
	dbmigrate.RegisterStep(cleanupStep{})
}

type cleanupStep struct{}

func (cleanupStep) Name() string        { return "cleanup" }
func (cleanupStep) Description() string { return "Drop source DB (optional, off by default)" }
func (cleanupStep) IsOptional() bool   { return true }
func (cleanupStep) DependsOn() []string { return []string{"flip"} }

// Run drops the source DB. This is gated by
// SKYGATE_MIGRATE_DROP_SOURCE=true in the env (off by
// default for safety).
func (cleanupStep) Run(ctx context.Context, mc *dbmigrate.MigrationContext) error {
	// Phase 1.4 stub — we don't drop the source DB
	// automatically. The operator does it manually after
	// confirming the new DSN works:
	//   psql "$source_dsn" -c "DROP DATABASE skygate_staging;"
	// Or keeps it for N days as a rollback target.
	_ = ctx
	_ = mc
	_ = exec.CommandContext // imported for B200
	return fmt.Errorf("STUB: drop-source is not auto-run for Phase 1.4; run manually if you want the old DB gone: DROP DATABASE skygate_staging")
}

func (cleanupStep) Rollback(_ context.Context, _ *dbmigrate.MigrationContext) error {
	// Nothing to undo — the source DB was already
	// kept untouched (the stub didn't drop it).
	return nil
}

var _ dbmigrate.DeployStep = cleanupStep{}
