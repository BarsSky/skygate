// Package dbmigrate/steps — Restore runs `pg_restore` against
// the TARGET host using the dump file from the Dump step.
//
// Phase 1.4 limitation: like Dump, the actual subprocess
// is stubbed. The operator runs pg_restore manually until
// B200 wires the local execution path.

package steps

import (
	"context"
	"fmt"
	"os/exec"

	"skygate/internal/dbmigrate"
)

func init() {
	dbmigrate.RegisterStep(restoreStep{})
}

type restoreStep struct{}

func (restoreStep) Name() string        { return "restore" }
func (restoreStep) Description() string { return "Run pg_restore on target host" }
func (restoreStep) IsOptional() bool   { return false }
func (restoreStep) DependsOn() []string { return []string{"dump"} }

func (restoreStep) Run(ctx context.Context, mc *dbmigrate.MigrationContext) error {
	if mc.DumpFile == "" {
		return fmt.Errorf("dump file is empty (Dump step should have set it)")
	}
	cmd := exec.CommandContext(ctx, "pg_restore",
		"-d", mc.TargetDSN,
		"-c", // clean (drop) before create
		"--if-exists",
		mc.DumpFile,
	)
	_ = cmd

	// Phase 1.4 stub — the operator runs pg_restore
	// manually:
	//   pg_restore -d "$target_dsn" -c --if-exists /tmp/skygate-migrate-{runid}.dump
	return fmt.Errorf("STUB: pg_restore must be run manually for Phase 1.4; target DSN: %s, dump file: %s", redactForLog(mc.TargetDSN), mc.DumpFile)
}

func (restoreStep) Rollback(_ context.Context, mc *dbmigrate.MigrationContext) error {
	// Drop the target DB to clean up. We use DROP DATABASE
	// via psql since pg_restore doesn't have a "drop
	// everything" command in the standard custom format.
	// Phase 1.4: the target DB doesn't exist yet (manual
	// step), so this is a no-op. B200: wire to psql.
	if mc.TargetDSN == "" {
		return nil
	}
	_ = exec.Command("psql", mc.TargetDSN, "-c",
		"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = current_database() AND pid <> pg_backend_pid()",
	).Run()
	return nil
}

func redactForLog(dsn string) string {
	// tiny helper to avoid importing the main redact for a
	// stub — we don't want the password in stderr.
	out := []byte(dsn)
	at := -1
	for i, c := range out {
		if c == '@' {
			at = i
			break
		}
	}
	if at > 0 {
		for i := at - 1; i >= 0; i-- {
			if out[i] == ':' {
				return string(out[:i+1]) + "***" + string(out[at:])
			}
		}
	}
	return dsn
}

var _ dbmigrate.DeployStep = restoreStep{}
