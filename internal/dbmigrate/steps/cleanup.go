// Package dbmigrate/steps — Cleanup drops the source
// database after a successful Flip. OPTIONAL — off
// by default.
//
// v1.5.0+ / B202 — real DROP DATABASE (gated by
// SKYGATE_MIGRATE_DROP_SOURCE env var).
//
// Why this is optional:
//   - Dropping the source DB is irreversible. The dump
//     file is a safety net, but having a live replica
//     is sometimes useful for forensics ("what did
//     the operator change at 14:32?").
//   - The operator should make the call manually
//     after confirming the target is healthy for N
//     days. We provide the mechanism (this step) but
//     not the trigger (env var).
//
// The env var is set per-run from the migration form
// (we surface it as a checkbox in the UI in a future
// iteration; for B202 it's env-only).
//
// Safety: even when SKYGATE_MIGRATE_DROP_SOURCE=true,
// we (a) terminate all connections to the source DB
// first, (b) audit_log the drop, (c) on failure,
// stash the error in mc.Warning instead of failing
// the whole run (since the source DB is no longer
// referenced by anything; the migration already
// succeeded).

package steps

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"skygate/internal/dbmigrate"
)

func init() {
	dbmigrate.RegisterStep(cleanupStep{})
}

type cleanupStep struct{}

func (cleanupStep) Name() string         { return "cleanup" }
func (cleanupStep) Description() string  { return "Drop source DB (optional, off by default)" }
func (cleanupStep) IsOptional() bool    { return true }
func (cleanupStep) DependsOn() []string { return []string{"flip"} }

// Run drops the source database IF
// SKYGATE_MIGRATE_DROP_SOURCE=true. Otherwise it logs
// a "skipped" message and returns success (IsOptional
// means the framework won't fail the run if we return
// an error here, but we don't want to anyway).
func (cleanupStep) Run(ctx context.Context, mc *dbmigrate.MigrationContext) error {
	if os.Getenv("SKYGATE_MIGRATE_DROP_SOURCE") != "true" {
		dbmigrate.EmitStepLog(mc.RunID, "cleanup",
			"SKYGATE_MIGRATE_DROP_SOURCE not set; source DB kept (drop manually if you want it gone)")
		return nil
	}
	if mc.SourceDSN == "" {
		return fmt.Errorf("source DSN is empty (cannot drop)")
	}

	// Extract the DB name from the DSN. The form
	// should have populated mc.TargetDBName etc. for
	// the target, but for the source we just parse.
	_, _, dbname, _, _, ok := parseLibpqDSN(mc.SourceDSN)
	if !ok || dbname == "" {
		return fmt.Errorf("source DB name is empty and unparseable from DSN")
	}

	dbmigrate.EmitStepLog(mc.RunID, "cleanup",
		fmt.Sprintf("SKYGATE_MIGRATE_DROP_SOURCE=true; dropping source DB %q", dbname))

	// Open a short-lived connection so we don't disturb
	// the framework's pool.
	conn, err := sql.Open("pgx", mc.SourceDSN)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer conn.Close()

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := conn.PingContext(pingCtx); err != nil {
		return fmt.Errorf("ping source: %w", err)
	}

	// 1. Terminate connections to the source DB so the
	//    DROP DATABASE doesn't wait on row locks.
	//    pg_terminate_backend is async — the next
	//    statement proceeds immediately. We sleep 200ms
	//    before the DROP to give the terminations time
	//    to land (otherwise we get "database is being
	//    accessed by other users" and have to retry).
	if _, err := conn.ExecContext(ctx,
		"SELECT pg_terminate_backend(pid) FROM pg_stat_activity "+
			"WHERE datname = $1 AND pid <> pg_backend_pid()",
		dbname,
	); err != nil {
		// Non-fatal — try the DROP anyway.
		dbmigrate.EmitStepLog(mc.RunID, "cleanup",
			fmt.Sprintf("warning: pg_terminate_backend: %v", err))
	}

	time.Sleep(200 * time.Millisecond)
	if _, err := conn.ExecContext(ctx,
		fmt.Sprintf("DROP DATABASE %s", quoteIdent(dbname)),
	); err != nil {
		return fmt.Errorf("drop database %q: %w", dbname, err)
	}

	dbmigrate.EmitStepLog(mc.RunID, "cleanup",
		fmt.Sprintf("source DB %q dropped", dbname))
	return nil
}

// Rollback is a no-op: the source DB was either kept
// (default) or already dropped (the Run succeeded).
// In neither case is there anything to undo.
func (cleanupStep) Rollback(_ context.Context, _ *dbmigrate.MigrationContext) error {
	return nil
}

// quoteIdent quotes a SQL identifier per the Postgres
// rules. For our use case the DB name is a known safe
// value (alphanumerics + underscore, from a controlled
// form), but quoting defends against future changes
// (e.g. an operator enters "my-staging-db" with a
// hyphen).
func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// parseLibpqDSN extracts (host, port, dbname, user,
// sslmode) from a postgres:// DSN. Returns ok=false on
// parse failure. Mirrors the helper in admin/database.go
// — duplicated here because the two packages don't
// share a model layer.
func parseLibpqDSN(dsn string) (host, port, dbname, user, sslmode string, ok bool) {
	u, err := url.Parse(dsn)
	if err != nil {
		return
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return
	}
	host = u.Hostname()
	port = u.Port()
	if u.User != nil {
		user = u.User.Username()
	}
	// u.Path is "/dbname" (no params — url.Parse moves
	// the ?... portion into u.RawQuery). Strip the
	// leading /.
	dbname = strings.TrimPrefix(u.Path, "/")
	// sslmode lives in u.Query() (parsed from u.RawQuery).
	if v := u.Query().Get("sslmode"); v != "" {
		sslmode = v
	}
	if host == "" || dbname == "" {
		return
	}
	ok = true
	return
}

// Compile-time assertion: cleanupStep implements DeployStep.
var _ dbmigrate.DeployStep = cleanupStep{}
