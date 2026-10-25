// internal/deploy/ha_b420_test.go — `skygate ha promote|demote|reclaim`
// and the deploy auditor must actually WRITE.
//
// B420 root cause: writeApplyActiveRole, clearApplyActiveRole,
// writeHAAudit and writeDeployAudit all spliced PostgreSQL's `now()`
// into a statement targeting an INTEGER epoch column (global_settings
// .updated_at / audit_log.created_at — both INTEGER in BOTH migration
// chains). PostgreSQL rejects it ("column updated_at is of type integer
// but expression is of type timestamp with time zone"); SQLite rejects it
// too ("no such function: now"). Consequences: `skygate ha promote`
// always returned an error without recording the requested role, and the
// HA / deploy-push audit rows were never written.
//
// These tests call the REAL functions against a REAL SQLite database
// built by the real migration chain, and pin that the pre-fix shape
// still fails — so the fix cannot be reverted without a red test and the
// rationale cannot rot into folklore.
package deploy

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	skygatedb "skygate/internal/db"
)

func openHA420DB(t *testing.T) *sql.DB {
	t.Helper()
	conn, err := skygatedb.OpenFastSQLiteForTest(filepath.Join(t.TempDir(), "b420-ha.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	prev := skygatedb.ActiveDialect()
	skygatedb.SetActiveDialect(skygatedb.DialectSQLite)
	t.Cleanup(func() { skygatedb.SetActiveDialect(prev) })
	if err := skygatedb.ApplyMigrations(conn, skygatedb.DialectSQLite); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	return conn
}

// TestB420_WriteApplyActiveRolePersistsOnSQLite is the operator-visible
// half: the role the operator asked for must be stored.
func TestB420_WriteApplyActiveRolePersistsOnSQLite(t *testing.T) {
	conn := openHA420DB(t)
	ctx := context.Background()

	if err := writeApplyActiveRole(ctx, conn, "skygate-standby"); err != nil {
		t.Fatalf("writeApplyActiveRole: %v — `skygate ha promote` would report this to the operator", err)
	}
	var value string
	var updatedAt int64
	if err := conn.QueryRow(
		`SELECT value, updated_at FROM global_settings WHERE key = $1`, ApplyActiveRoleKey,
	).Scan(&value, &updatedAt); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if value != "skygate-standby" {
		t.Fatalf("value = %q, want skygate-standby", value)
	}
	if now := time.Now().Unix(); updatedAt <= 0 || updatedAt < now-86400 || updatedAt > now+86400 {
		t.Fatalf("updated_at = %d, want a plausible epoch (now=%d)", updatedAt, now)
	}

	// clearApplyActiveRole must work too (it is the demote/reclaim path).
	if err := clearApplyActiveRole(ctx, conn); err != nil {
		t.Fatalf("clearApplyActiveRole: %v", err)
	}
	if err := conn.QueryRow(
		`SELECT value FROM global_settings WHERE key = $1`, ApplyActiveRoleKey,
	).Scan(&value); err != nil {
		t.Fatalf("read back after clear: %v", err)
	}
	if value != "" {
		t.Fatalf("value after clear = %q, want empty", value)
	}
}

// TestB420_WriteHAAuditPersistsOnSQLite covers the audit half of the HA
// and deploy verbs.
func TestB420_WriteHAAuditPersistsOnSQLite(t *testing.T) {
	conn := openHA420DB(t)
	ctx := context.Background()

	if err := writeHAAudit(ctx, conn, "ha.promote", "skygate-standby", "requested by test"); err != nil {
		t.Fatalf("writeHAAudit: %v", err)
	}
	if err := writeDeployAudit(ctx, conn, "ha.deploy.push", "skygate-standby", map[string]string{"version": "v1.5.166"}); err != nil {
		t.Fatalf("writeDeployAudit: %v", err)
	}

	rows, err := conn.Query(`SELECT action, detail, created_at FROM audit_log`)
	if err != nil {
		t.Fatalf("select audit rows: %v", err)
	}
	defer rows.Close()
	// A SET, not a sequence: both rows land in the same second, and
	// created_at is a second-resolution epoch, so "ORDER BY created_at"
	// cannot distinguish them (and the production page orders by the
	// same column).
	seen := map[string]bool{}
	for rows.Next() {
		var (
			action  string
			detail  string
			created int64
		)
		if err := rows.Scan(&action, &detail, &created); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if created <= 0 {
			t.Errorf("%s: created_at = %d, want an epoch", action, created)
		}
		seen[action] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(seen) != 2 || !seen["ha.promote"] || !seen["ha.deploy.push"] {
		t.Fatalf("audit actions = %v, want both ha.promote and ha.deploy.push", seen)
	}
}

// TestB420_PreFixNowWriteStillFailsOnSQLite pins the defect class.
func TestB420_PreFixNowWriteStillFailsOnSQLite(t *testing.T) {
	conn := openHA420DB(t)
	_, err := conn.Exec(
		`INSERT INTO global_settings (key, value, updated_at) VALUES ($1, $2, now())`,
		"b420-probe", "x")
	if err == nil {
		t.Fatal("the pre-B420 `now()` INSERT succeeded on SQLite — the premise of this block is now wrong: re-measure and update the comments")
	}
	t.Logf("pre-B420 now() INSERT correctly rejected on SQLite: %v", err)
}
