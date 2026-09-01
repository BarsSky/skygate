// Package dbmigrate/steps — Dump runs `pg_dump -Fc` against
// the SOURCE host and writes the dump file locally. The
// dump file is then read by the Restore step.
//
// Phase 1.4 limitation: the actual subprocess execution is
// stubbed. To run a real migration the agent needs SSH access
// to the source host (or pg_dump needs to be installed on
// the source so we can run it remotely). For Phase 1.4 we
// just log the command we'd run; the operator executes the
// migration manually until B200 wires the SSH path.

package steps

import (
	"context"
	"fmt"
	"os/exec"
	"time"

	"skygate/internal/dbmigrate"
)

func init() {
	dbmigrate.RegisterStep(dumpStep{})
}

type dumpStep struct{}

func (dumpStep) Name() string        { return "dump" }
func (dumpStep) Description() string { return "Run pg_dump -Fc on source host" }
func (dumpStep) IsOptional() bool   { return false }
func (dumpStep) DependsOn() []string { return []string{"precheck"} }

// Run executes `pg_dump -Fc` against the source DSN. The
// subprocess is given a 10-minute timeout. Output goes to
// mc.DumpFile (defaults to /tmp/skygate-migrate-{runid}.dump
// if not set).
//
// STUB for Phase 1.4: we don't actually run pg_dump yet —
// the source is on a different host (svi, via Tailscale) and
// we don't have SSH plumbing here. We log the command we
// would run. Real execution lands in B200.
func (dumpStep) Run(ctx context.Context, mc *dbmigrate.MigrationContext) error {
	if mc.DumpFile == "" {
		mc.DumpFile = fmt.Sprintf("/tmp/skygate-migrate-%d.dump", mc.RunID)
	}
	// Compose the command. We use the standard
	// `pg_dump -Fc -d <dsn>` form so the source doesn't
	// need any setup beyond pg_dump being installed.
	cmd := exec.CommandContext(ctx, "pg_dump",
		"-Fc",       // custom format (compressed, parallel-restorable)
		"-d", mc.SourceDSN,
		"-f", mc.DumpFile,
	)
	// Phase 1.4 stub: don't actually run the command. The
	// source host runs pg_dump remotely (out of scope for
	// B198). The operator can run it manually:
	//   ssh svi "pg_dump -Fc -d '$source_dsn' -f /tmp/skygate-migrate.dump"
	//   scp svi:/tmp/skygate-migrate.dump /tmp/skygate-migrate-{runid}.dump
	// Then re-run the migration.
	_ = cmd

	// B200: actually exec. For now, fail with a clear
	// message so the operator knows the framework ran but
	// the dump step is a manual action.
	return fmt.Errorf("STUB: pg_dump must be run manually for Phase 1.4; see docs/internal/cluster-management.md Phase 1.4 — Dump step. Target file: %s", mc.DumpFile)
}

func (dumpStep) Rollback(_ context.Context, mc *dbmigrate.MigrationContext) error {
	// Best-effort: remove the dump file. We don't actually
	// know it was created (Phase 1.4 stub), but the
	// rollback is safe to no-op.
	if mc.DumpFile != "" {
		// os.Remove is silently ignored on file-not-found.
		// In Phase 1.4 we don't even create the file,
		// but the rollback contract is here for B200.
		_ = exec.Command("rm", "-f", mc.DumpFile).Run()
	}
	return nil
}

// Compile-time assertion: dumpStep implements DeployStep.
var _ dbmigrate.DeployStep = dumpStep{}

// keep time import used in B200 placeholder
var _ = time.Second
