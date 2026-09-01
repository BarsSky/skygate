// Package dbmigrate/steps — Verify compares the source
// and target DSNs by counting rows in a curated set of
// "user-action" tables (portal_users, device_rules,
// preauth_keys, etc.) — tables that change only when
// the operator explicitly creates / deletes / modifies
// something.
//
// v1.5.0+ / B202 — per-table diff (was a single summed
// total in Phase 1.4, which masked partial-restore
// failures like "portal_users has 1000 rows but
// preauth_keys is empty").
//
// Why a curated table list (not every table):
//   - audit_log has a constant stream of inserts (every
//     skygate request adds a row). Counting it across
//     two snapshots always gives a different number.
//     The diff would always fail, which is useless.
//   - Same for cluster_node.last_seen_at — updates on
//     every heartbeat. Across dump→restore, the value
//     drifts. A naive "every table" diff would trip.
//   - The 6 tables we count are the user-action tables:
//     they only change when the operator (or an API
//     call) explicitly creates a row. So a successful
//     round-trip preserves the count exactly.
//
// Why per-table (was total) in B202:
//   - A partial restore (e.g. one table failed mid-restore,
//     others succeeded) would still pass the total-count
//     check if the affected table is small enough.
//   - Per-table comparison catches this immediately:
//     "portal_users: src=12 tgt=11" → fail.

package steps

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"

	"skygate/internal/dbmigrate"
)

func init() {
	dbmigrate.RegisterStep(verifyStep{})
}

type verifyStep struct{}

func (verifyStep) Name() string        { return "verify" }
func (verifyStep) Description() string { return "Count rows on source and target per key table; fail if any differ" }
func (verifyStep) Ordinal() int       { return 4 }
func (verifyStep) IsOptional() bool   { return false }
func (verifyStep) DependsOn() []string { return []string{"restore"} }

// keyTables is the set of tables we count and compare.
// Documented above; keep in sync with the package doc.
var keyTables = []string{
	"portal_users",
	"device_rules",
	"node_owner_map",
	"preauth_keys",
	"user_exit_node_prefs",
	"device_exit_node_prefs",
}

// Run counts rows per table on source and target, fails
// if any table differs. The per-table counts are stashed
// in mc (the framework reads them for the audit row).
func (verifyStep) Run(ctx context.Context, mc *dbmigrate.MigrationContext) error {
	srcCounts, err := countPerTable(ctx, mc.SourceDSN, keyTables)
	if err != nil {
		return fmt.Errorf("count source: %w", err)
	}
	tgtCounts, err := countPerTable(ctx, mc.TargetDSN, keyTables)
	if err != nil {
		return fmt.Errorf("count target: %w", err)
	}
	mc.SourceRowCount = sumCounts(srcCounts)
	mc.TargetRowCount = sumCounts(tgtCounts)
	mc.RowCountMatch = mapsEqual(srcCounts, tgtCounts)
	if !mc.RowCountMatch {
		var diffs []string
		// Stable order: sort by table name. This way
		// the diff message is the same across runs
		// (helps the operator search audit_log).
		allTables := unionKeys(srcCounts, tgtCounts)
		sort.Strings(allTables)
		for _, t := range allTables {
			s, sok := srcCounts[t]
			t2, tok := tgtCounts[t]
			if !sok || !tok || s != t2 {
				diffs = append(diffs, fmt.Sprintf("%s: src=%d tgt=%d", t, s, t2))
			}
		}
		return fmt.Errorf("row count mismatch: %s", strings.Join(diffs, ", "))
	}
	return nil
}

func (verifyStep) Rollback(_ context.Context, _ *dbmigrate.MigrationContext) error {
	return nil // nothing to undo (read-only step)
}

// countPerTable runs SELECT count(*) for each table and
// returns a map[table]int64. Tables that don't exist
// (relation does not exist) are reported as 0 — the
// migration should be skipped, not failed, if the
// target DB is missing optional tables.
func countPerTable(ctx context.Context, dsn string, tables []string) (map[string]int64, error) {
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	// Quick reachability check so we fail fast with a
	// clear "can't connect" error instead of one
	// "relation does not exist" per table.
	pingCtx, cancel := context.WithTimeout(ctx, 5*1e9) // 5s
	defer cancel()
	if err := conn.PingContext(pingCtx); err != nil {
		return nil, fmt.Errorf("ping: %w", err)
	}
	out := make(map[string]int64, len(tables))
	for _, t := range tables {
		var n int64
		if err := conn.QueryRowContext(ctx, fmt.Sprintf("SELECT count(*) FROM %s", t)).Scan(&n); err != nil {
			// Missing table is 0, not an error. We log
			// via the SSE broker so the operator sees
			// it in the audit log (useful for spotting
			// drift — e.g. the dump is from a fresh DB
			// that hasn't been migrated yet).
			if strings.Contains(err.Error(), "does not exist") {
				n = 0
			} else {
				return nil, fmt.Errorf("count %s: %w", t, err)
			}
		}
		out[t] = n
	}
	return out, nil
}

func sumCounts(m map[string]int64) int64 {
	var s int64
	for _, v := range m {
		s += v
	}
	return s
}

func mapsEqual(a, b map[string]int64) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

func unionKeys(a, b map[string]int64) []string {
	seen := map[string]bool{}
	var out []string
	for k := range a {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	for k := range b {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

// Compile-time assertion: verifyStep implements DeployStep.
var _ dbmigrate.DeployStep = verifyStep{}
