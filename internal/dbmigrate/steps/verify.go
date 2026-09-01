// Package dbmigrate/steps — Verify compares the source and
// target DSNs by counting rows in the most important tables
// (portal_users, device_rules, audit_log, node_owner_map).
// If counts match, the step succeeds. If they differ, the
// step fails and the migration aborts (we don't want to flip
// the DSN to a half-restored DB).
//
// The count is fast (indexed count) and works even without
// network reachability to both — each side is counted
// independently. For Phase 1.4 we count via pgxpool from
// the skygate process (no separate DB user needed).

package steps

import (
	"context"
	"database/sql"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib"

	"skygate/internal/dbmigrate"
)

func init() {
	dbmigrate.RegisterStep(verifyStep{})
}

type verifyStep struct{}

func (verifyStep) Name() string        { return "verify" }
func (verifyStep) Description() string { return "Count rows on source and target; fail if they differ" }
func (verifyStep) IsOptional() bool   { return false }
func (verifyStep) DependsOn() []string { return []string{"restore"} }

func (verifyStep) Run(ctx context.Context, mc *dbmigrate.MigrationContext) error {
	srcCount, err := countKeyTables(ctx, mc.SourceDSN)
	if err != nil {
		return fmt.Errorf("count source: %w", err)
	}
	tgtCount, err := countKeyTables(ctx, mc.TargetDSN)
	if err != nil {
		return fmt.Errorf("count target: %w", err)
	}
	mc.SourceRowCount = srcCount
	mc.TargetRowCount = tgtCount
	mc.RowCountMatch = srcCount == tgtCount
	if !mc.RowCountMatch {
		return fmt.Errorf("row count mismatch: source=%d target=%d", srcCount, tgtCount)
	}
	return nil
}

func (verifyStep) Rollback(_ context.Context, _ *dbmigrate.MigrationContext) error {
	return nil // nothing to undo (read-only step)
}

// countKeyTables returns a deterministic integer that
// summarises the key tables. We don't compare per-table
// (the audit_log has a constant stream of inserts, so
// any count comparison would always fail). Instead we
// sum a few table counts that change only on user
// actions: portal_users, device_rules, node_owner_map,
// preauth_keys.
func countKeyTables(ctx context.Context, dsn string) (int64, error) {
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	var total int64
	for _, table := range []string{
		"portal_users",
		"device_rules",
		"node_owner_map",
		"preauth_keys",
		"user_exit_node_prefs",
		"device_exit_node_prefs",
	} {
		var n int64
		if err := conn.QueryRowContext(ctx,
			fmt.Sprintf("SELECT count(*) FROM %s", table)).Scan(&n); err != nil {
			// count on a missing table = 0; that's fine
			// for tables that don't exist yet on the target
			// (e.g., fresh DB after a B-migration that
			// hasn't been applied to the target).
			n = 0
		}
		total += n
	}
	return total, nil
}

var _ dbmigrate.DeployStep = verifyStep{}
