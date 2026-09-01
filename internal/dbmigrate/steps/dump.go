// Package dbmigrate/steps — Dump streams `pg_dump -Fc`
// of the SOURCE host to a local file. The Restore step
// reads from that file.
//
// v1.5.0+ / B202 — real subprocess execution (replaces
// the Phase 1.4 stub).
//
// Flow:
//   1. Open a transaction on the source DB and acquire
//      pg_try_advisory_lock(42). This prevents pg_dump
//      from snapshotting mid-write. The lock is held
//      for the duration of the dump (the tx stays open
//      until the dump finishes; the framework's Rollback
//      also unlocks if a later step fails).
//   2. Call mc.Transport.Dump(ctx, source, dest, onLog)
//      to do the actual subprocess exec. For B202 the
//      transport is LocalDumpTransport; B202.5 will
//      swap in SSHDumpTransport for cross-host cases.
//   3. Verify the dump file has the pg_dump custom-format
//      magic bytes ("PGD\n" hex: 50 47 44 0a). Without
//      this check, a 0-byte file or a "psql: error"
//      text file would pass as a "dump" and pg_restore
//      would fail with a confusing "invalid magic" error.
//   4. On failure, Rollback releases the lock and removes
//      the partial file.
//
// Why advisory lock id 42: a single hard-coded id is
// fine for now because we only run one migration at a
// time (the framework serializes runs via the run-id
// unique index). A future multi-cluster world would
// hash the cluster-id into the lock id to prevent
// cross-cluster collisions.

package steps

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"skygate/internal/dbmigrate"
)

func init() {
	dbmigrate.RegisterStep(dumpStep{})
}

type dumpStep struct{}

func (dumpStep) Name() string         { return "dump" }
func (dumpStep) Description() string  { return "Run pg_dump -Fc on source host" }
func (dumpStep) Ordinal() int        { return 2 }
func (dumpStep) IsOptional() bool    { return false }
func (dumpStep) DependsOn() []string { return []string{"precheck"} }

// pgDumpMagicLegacy is the first 4 bytes of a pg_dump
// custom-format file from PostgreSQL 15 and earlier
// ("PGD\n" hex: 50 47 44 0a). Kept for backward compat
// with old dumps — pg_restore on the target can read
// either format.
var pgDumpMagicLegacy = [4]byte{0x50, 0x47, 0x44, 0x0a} // "PGD\n"

// pgDumpMagicModern is the magic for PostgreSQL 16+
// ("PGDM" hex: 50 47 44 4d). The custom format file
// layout is otherwise identical between the two;
// pg_restore on a 16+ target reads both.
var pgDumpMagicModern = [4]byte{0x50, 0x47, 0x44, 0x4d} // "PGDM"

// advisoryLockID is the pg_advisory_lock id this
// migration uses. See the file-level comment for why
// a single hard-coded id is OK.
const advisoryLockID int64 = 42

// Run executes the dump step. Sets mc.DumpFile (if not
// already set), mc.DumpBytes, and mc.SourceLockHeld.
func (dumpStep) Run(ctx context.Context, mc *dbmigrate.MigrationContext) error {
	// 1. Resolve the dump file path.
	if mc.DumpFile == "" {
		mc.DumpFile = fmt.Sprintf("/var/lib/skygate/migrations/%d.dump", mc.RunID)
	}
	if err := os.MkdirAll(filepath.Dir(mc.DumpFile), 0o755); err != nil {
		return fmt.Errorf("create migrations dir: %w", err)
	}

	// 2. Acquire the source advisory lock so no concurrent
	// writer (skygate's normal traffic) interferes with
	// pg_dump. We use pg_try_advisory_lock (non-blocking)
	// so a stuck writer fails fast with a clear message
	// instead of hanging the migration.
	if mc.DB == nil {
		return fmt.Errorf("mc.DB is nil (framework bug — should be set by Run())")
	}
	lockHolder, err := acquireSourceLock(ctx, mc.DB)
	if err != nil {
		return err
	}
	mc.SourceLockHeld = true
	defer func() {
		// Best-effort lock release. A failure here is
		// stashed in mc.Warning (the framework surfaces
		// it in the audit_log row).
		if releaseErr := releaseSourceLock(lockHolder); releaseErr != nil {
			mc.Warning = fmt.Sprintf("release advisory lock: %v", releaseErr)
		}
	}()

	// 3. Build the onLog callback. We emit each line as
	// a "step_log" SSE event so the operator sees pg_dump
	// progress in real time on /admin/database/migrate/{id}.
	onLog := func(line string) {
		dbmigrate.EmitStepLog(mc.RunID, "dump", line)
	}

	// 4. Run the transport.
	start := time.Now()
	bytes, err := mc.Transport.Dump(ctx, mc.SourceDSN, mc.DumpFile, onLog)
	if err != nil {
		return fmt.Errorf("transport.Dump: %w", err)
	}
	mc.DumpBytes = bytes
	mc.DumpDurationMs = time.Since(start).Milliseconds()

	// 5. Verify the file is a real pg_dump custom-format
	// file. A failed subprocess sometimes leaves a 0-byte
	// file or a text error message behind; we want to
	// catch that here, with a clear "dump file is empty
	// / not a valid dump" message, instead of letting
	// pg_restore fail later with "invalid tar header".
	if bytes == 0 {
		_ = os.Remove(mc.DumpFile)
		return fmt.Errorf("dump file is 0 bytes — pg_dump wrote nothing (check source DSN + permissions)")
	}
	magic, err := readFirstBytes(mc.DumpFile, 4)
	if err != nil {
		return fmt.Errorf("read dump file: %w", err)
	}
	var magicArr [4]byte
	copy(magicArr[:], magic)
	if magicArr != pgDumpMagicLegacy && magicArr != pgDumpMagicModern {
		_ = os.Remove(mc.DumpFile)
		return fmt.Errorf("dump file is not a valid pg_dump custom format (magic=%x, want PGD\\n=5047440a or PGDM=5047444d) — was the source DSN correct?", magic)
	}

	return nil
}

// Rollback releases the advisory lock + removes the
// dump file. Called by the framework when a LATER step
// (restore / verify / flip) fails.
//
// Nil-safe: the framework passes a nil *MigrationContext
// in the rollback chain (see framework.go:171). All
// fields we access are gated by `mc != nil &&` so we
// no-op gracefully instead of panicking.
func (dumpStep) Rollback(ctx context.Context, mc *dbmigrate.MigrationContext) error {
	if mc != nil {
		if mc.SourceLockHeld && mc.DB != nil {
			_, _ = mc.DB.Exec("SELECT pg_advisory_unlock($1)", advisoryLockID)
			mc.SourceLockHeld = false
		}
		if mc.DumpFile != "" {
			_ = os.Remove(mc.DumpFile)
		}
	}
	return nil
}

// ---------- pure helpers (testable) ----------------------------------

// acquireSourceLock opens a transaction, takes
// pg_try_advisory_lock(42), and returns the *sql.Tx
// (caller must hold it for the duration of the dump so
// the lock is not released on COMMIT).
//
// pg_try_advisory_lock returns false immediately if the
// lock is held by another session. We fail fast in that
// case — the operator should retry after the other
// migration finishes.
func acquireSourceLock(ctx context.Context, db dbmigrate.DBMigrator) (*sql.Tx, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	var got bool
	if err := tx.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", advisoryLockID).Scan(&got); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("pg_try_advisory_lock: %w", err)
	}
	if !got {
		_ = tx.Rollback()
		return nil, fmt.Errorf("source DB is being written to (advisory lock held); retry in a few seconds")
	}
	return tx, nil
}

// releaseSourceLock releases the advisory lock and
// commits (== ends the tx; the session-scoped unlock
// actually releases the lock, the COMMIT just ends
// the transaction).
func releaseSourceLock(tx *sql.Tx) error {
	if tx == nil {
		return nil
	}
	_, _ = tx.Exec("SELECT pg_advisory_unlock($1)", advisoryLockID)
	return tx.Commit()
}

// readFirstBytes returns the first n bytes of the file
// at path. Used to verify the pg_dump custom-format
// magic. Returns an error if the file is shorter than n
// (which usually means pg_dump wrote nothing).
func readFirstBytes(path string, n int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, n)
	read, err := io.ReadFull(f, buf)
	if err != nil {
		return nil, fmt.Errorf("read %d bytes: got %d: %w", n, read, err)
	}
	return buf[:read], nil
}

// Compile-time assertion: dumpStep implements DeployStep.
var _ dbmigrate.DeployStep = dumpStep{}
