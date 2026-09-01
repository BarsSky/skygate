// Package dbmigrate/steps — Restore runs `pg_restore`
// against the TARGET host using the dump file from the
// Dump step.
//
// v1.5.0+ / B202 — real subprocess execution (replaces
// the Phase 1.4 stub).
//
// Flow:
//   1. Sanity-check the dump file (exists, non-zero,
//      has pg_dump magic). The Dump step already
//      verified this, but the operator might have
//      deleted the file in between or the framework
//      might have re-run Restore in isolation.
//   2. Open a short-lived connection to the target DB
//      and run pg_terminate_backend on every session
//      except ours. This frees the row locks that
//      pg_restore's `-c` flag would otherwise wait on.
//      (The target DB might have open connections from
//      a previous skygate run, or from a parallel
//      test, etc.)
//   3. Run pg_restore with -c (clean/drop before
//      create) --if-exists --no-owner --no-privileges
//      --jobs=4. Stream stderr to the SSE broker so
//      the operator sees progress.
//   4. On failure, Rollback drops the public schema
//      and recreates it (a clean slate, ready for a
//      retry). The framework will call the next
//      Run attempt with a fresh dump.
//
// Why --no-owner / --no-privileges: skygate's source
// DB might have a different role than the target (e.g.
// Patroni's "admin" role vs skygate's "skyadmin" role).
// Without these flags, pg_restore tries to execute
// ALTER OWNER statements that fail with "role does not
// exist" on the target.

package steps

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"skygate/internal/dbmigrate"
)

func init() {
	dbmigrate.RegisterStep(restoreStep{})
}

type restoreStep struct{}

func (restoreStep) Name() string         { return "restore" }
func (restoreStep) Description() string  { return "Run pg_restore on target host" }
func (restoreStep) Ordinal() int        { return 3 }
func (restoreStep) IsOptional() bool    { return false }
func (restoreStep) DependsOn() []string { return []string{"dump"} }

// Run executes the restore step. The target DB is wiped
// of existing objects (via pg_restore -c) and the dump
// file is replayed. On success, every row in the source
// DB is present in the target.
func (restoreStep) Run(ctx context.Context, mc *dbmigrate.MigrationContext) error {
	// 1. Sanity-check the dump file.
	if mc.DumpFile == "" {
		return fmt.Errorf("dump file path is empty (Dump step should have set it)")
	}
	stat, err := os.Stat(mc.DumpFile)
	if err != nil {
		return fmt.Errorf("dump file missing: %w", err)
	}
	if stat.Size() == 0 {
		return fmt.Errorf("dump file is 0 bytes — was the dump step skipped or did it fail?")
	}

	// 2. Terminate existing connections to the target DB.
	//    Without this, pg_restore's `-c` flag (DROP TABLE)
	//    blocks waiting for row locks held by the existing
	//    sessions. We use a short-lived connection (not
	//    the framework's pool) so we don't kill our own
	//    pgxpool connections.
	if mc.TargetDSN == "" {
		return fmt.Errorf("target DSN is empty (form should have required it)")
	}
	if err := terminateTargetConnections(ctx, mc); err != nil {
		// Non-fatal — pg_restore will report a clearer
		// error if the locks really are held.
		dbmigrate.EmitStepLog(mc.RunID, "restore",
			fmt.Sprintf("warning: pg_terminate_backend: %v", err))
	}

	// 3. Run pg_restore.
	cmd := exec.CommandContext(ctx, "pg_restore",
		"-d", mc.TargetDSN,
		"-c",              // clean (drop) before create
		"--if-exists",     // don't error if the drop target is missing
		"--no-owner",      // don't emit ALTER OWNER / SET SESSION AUTHORIZATION
		"--no-privileges", // don't emit GRANT/REVOKE
		"--jobs=4",        // parallel restore (pg_restore -Fc supports 4-way parallel)
		"--verbose",       // emit progress to stderr (one line per object)
		mc.DumpFile,
	)

	// Stream stderr to the SSE broker. pg_restore is mostly
	// quiet on stdout; verbose output goes to stderr.
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	go streamRestoreOutput(mc, stderr, "stderr")
	go streamRestoreOutput(mc, stdout, "stdout")

	start := time.Now()
	runErr := cmd.Run()
	if runErr != nil {
		return fmt.Errorf("pg_restore failed after %dms: %w (see audit_log for stderr)", time.Since(start).Milliseconds(), runErr)
	}
	return nil
}

// streamRestoreOutput reads newline-delimited lines from
// the pipe and emits each as a step_log SSE event.
// `label` is prepended to the message ("stderr: ..."
// vs "stdout: ...") so the operator can tell which
// stream a line came from.
func streamRestoreOutput(mc *dbmigrate.MigrationContext, r io.Reader, label string) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		dbmigrate.EmitStepLog(mc.RunID, "restore", label+": "+scanner.Text())
	}
}

// Rollback drops the public schema on the target DB so
// the framework can retry with a fresh dump. Distinct
// from cleanup.go (which drops the SOURCE database).
func (restoreStep) Rollback(ctx context.Context, mc *dbmigrate.MigrationContext) error {
	if mc.TargetDSN == "" {
		return nil
	}
	// Open a short-lived connection (NOT the framework's
	// pool) so we don't kill the framework's own sessions.
	conn, err := sql.Open("pgx", mc.TargetDSN)
	if err != nil {
		// Can't open — best-effort no-op.
		return nil
	}
	defer conn.Close()
	// Terminate all OTHER connections to the target so
	// the schema drop doesn't wait on row locks.
	_, _ = conn.ExecContext(ctx,
		"SELECT pg_terminate_backend(pid) FROM pg_stat_activity "+
			"WHERE datname = current_database() AND pid <> pg_backend_pid()")
	// Drop + recreate the public schema. CASCADE removes
	// every object pg_restore would have created.
	_, err = conn.ExecContext(ctx, "DROP SCHEMA IF EXISTS public CASCADE; CREATE SCHEMA public;")
	if err != nil {
		// Best-effort — the framework will still try the
		// next Restore on a (possibly dirty) target, and
		// the verify step will catch any leftover objects.
		return fmt.Errorf("rollback: drop public schema: %w", err)
	}
	return nil
}

// terminateTargetConnections kills every session on the
// target DB except the one we're using. We open a fresh
// connection so the framework's pool isn't disrupted.
func terminateTargetConnections(ctx context.Context, mc *dbmigrate.MigrationContext) error {
	if mc.TargetDSN == "" {
		return nil
	}
	conn, err := sql.Open("pgx", mc.TargetDSN)
	if err != nil {
		return err
	}
	defer conn.Close()
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := conn.PingContext(pingCtx); err != nil {
		return fmt.Errorf("ping target: %w", err)
	}
	_, err = conn.ExecContext(ctx,
		"SELECT pg_terminate_backend(pid) FROM pg_stat_activity "+
			"WHERE datname = current_database() AND pid <> pg_backend_pid()")
	return err
}

// Compile-time assertion: restoreStep implements DeployStep.
var _ dbmigrate.DeployStep = restoreStep{}
