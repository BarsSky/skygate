package db

// migrations_v0_69_b235_3_test.go — v0.69 (B235.3) —
// source-level test that pins the migration shape.
//
// Coverage:
//   - migrateV069PG function exists + ALTER TABLE derp_health
//   - ADD COLUMN IF NOT EXISTS name (idempotent)
//   - NOT NULL DEFAULT '' (so existing rows are valid
//     without backfill — the next ProbeAll tick
//     populates the column from FetchPublicDERPs)
//   - driver_postgres.go has the v0.69 entry pointing
//     at the right source file
//
// 2026-09-04: v0.69 (B235.3).

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestMigrateV069PG_SourceShape(t *testing.T) {
	src := readV069Source(t)

	must := []string{
		`migrateV069PG`,
		`ALTER TABLE derp_health`,
		`ADD COLUMN IF NOT EXISTS name`,
		`NOT NULL DEFAULT ''`,
	}
	for _, m := range must {
		if !regexp.MustCompile(regexp.QuoteMeta(m)).MatchString(src) {
			t.Errorf("migrateV069PG source missing required fragment: %q", m)
		}
	}
}

func TestMigrateV069PG_Registered(t *testing.T) {
	b, err := os.ReadFile("driver_postgres.go")
	if err != nil {
		t.Fatalf("read driver_postgres.go: %v", err)
	}
	src := string(b)
	if !strings.Contains(src, `migrateV069PG`) {
		t.Errorf("migrateV069PG not in driver_postgres.go dispatch")
	}
	if !strings.Contains(src, `migrations_v0_69_b235_3.go`) {
		t.Errorf("migrateV069PG entry doesn't reference its source file")
	}
}

func readV069Source(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("migrations_v0_69_b235_3.go")
	if err != nil {
		t.Fatalf("read migration source: %v", err)
	}
	return string(b)
}
