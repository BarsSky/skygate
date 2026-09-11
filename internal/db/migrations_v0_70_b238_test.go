package db

// migrations_v0_70_b238_test.go — v0.70 (B238) —
// source-level test that pins the migration shape.
//
// Coverage:
//   - migrateV070PG function exists
//   - CREATE OR REPLACE FUNCTION portal_users_audit_trigger
//   - DROP TRIGGER IF EXISTS portal_users_audit (idempotent)
//   - CREATE TRIGGER AFTER UPDATE ON portal_users FOR EACH ROW
//   - Trigger body uses OLD.password_hash IS DISTINCT FROM NEW.password_hash
//     (so non-password updates don't fire)
//   - Trigger writes action='password_change_db' into audit_log
//   - Detail includes old_prefix + new_prefix + txid
//   - target_type='portal_user' + target_id=OLD.username (B221 schema)
//   - driver_postgres.go has the v0.70 entry pointing at
//     the right source file
//
// 2026-09-11: v0.70 (B238).

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestMigrateV070PG_SourceShape(t *testing.T) {
	src := readV070Source(t)

	must := []string{
		`migrateV070PG`,
		`CREATE OR REPLACE FUNCTION portal_users_audit_trigger()`,
		`RETURNS TRIGGER AS`,
		`OLD.password_hash IS DISTINCT FROM NEW.password_hash`,
		`password_change_db`,
		`old_prefix=`,
		`new_prefix=`,
		`txid=`,
		`pg_current_xact_id`,
		`target_type`,
		`portal_user`,
		`DROP TRIGGER IF EXISTS portal_users_audit ON portal_users`,
		`CREATE TRIGGER portal_users_audit`,
		`AFTER UPDATE ON portal_users`,
		`FOR EACH ROW`,
		`EXECUTE FUNCTION portal_users_audit_trigger()`,
	}
	for _, m := range must {
		if !regexp.MustCompile(regexp.QuoteMeta(m)).MatchString(src) {
			t.Errorf("migrateV070PG source missing required fragment: %q", m)
		}
	}
}

func TestMigrateV070PG_Registered(t *testing.T) {
	b, err := os.ReadFile("driver_postgres.go")
	if err != nil {
		t.Fatalf("read driver_postgres.go: %v", err)
	}
	src := string(b)
	if !strings.Contains(src, `migrateV070PG`) {
		t.Errorf("migrateV070PG not in driver_postgres.go dispatch")
	}
	if !strings.Contains(src, `migrations_v0_70_b238.go`) {
		t.Errorf("migrateV070PG entry doesn't reference its source file")
	}
	if !strings.Contains(src, `70, "v0.70 (B238): portal_users AFTER UPDATE audit trigger`) {
		t.Errorf("migrateV070PG entry doesn't have the v0.70 B238 label")
	}
}

func readV070Source(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("migrations_v0_70_b238.go")
	if err != nil {
		t.Fatalf("read migration source: %v", err)
	}
	return string(b)
}
