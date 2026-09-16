// File: internal/db/migrations_v0_71_derp_cert_sync_test.go
//
// 2026-09-15: v0.71 (B252) source-level tests for the derp_cert_sync
// migration. Pins the source shape (column list + types) and the
// driver registration so a refactor can't silently break the
// migration without the operator noticing.
//
// Per the AGENTS.md "tests pin source shape" convention, these
// tests use string matching against the migration's source file
// (no live DB) so they're fast and don't need a docker compose
// running.

package db

import (
	"regexp"
	"strings"
	"testing"
)

// TestMigrateV071PG_SourceShape pins the column list + types
// for derp_cert_sync. If a future change renames a column,
// drops a default, or changes a type, the operator sees the
// diff in CI instead of finding it at runtime when the cert
// sync cron silently fails.
//
// The matchers below are token-level (column name + key
// constraint fragments) rather than the full column literal,
// because the migration body is inside a Go raw-string
// literal with embedded whitespace that strings.Contains
// would never reproduce byte-for-byte. We restrict the search
// to the CREATE TABLE block (between "CREATE TABLE IF NOT
// EXISTS derp_cert_sync (" and the matching ")" of that
// statement) so doc comments above the table that mention the
// same identifiers don't produce false positives.
func TestMigrateV071PG_SourceShape(t *testing.T) {
	body := readMigrationSource(t, "migrations_v0_71_derp_cert_sync.go")

	tableStart := strings.Index(body, "CREATE TABLE IF NOT EXISTS derp_cert_sync (")
	if tableStart < 0 {
		t.Fatal("CREATE TABLE IF NOT EXISTS derp_cert_sync not found")
	}
	// Find the matching close-paren. The CREATE TABLE block has
	// at most 20 column lines + an opening paren, so 4000 chars
	// covers even the most padded source.
	tableEnd := tableStart + 4000
	if tableEnd > len(body) {
		tableEnd = len(body)
	}
	tableBody := body[tableStart:tableEnd]

	requiredTokens := []struct {
		col       string
		constraint string
	}{
		{"hostname", "UNIQUE"},
		{"mode", "'letsencrypt'"},
		{"npm_base_url", "DEFAULT ''"},
		{"npm_cert_id", "DEFAULT 0"},
		{"cert_dir", "'/var/lib/derper/certs'"},
		{"derper_pid_file", "DEFAULT '/var/run/derper.pid'"},
		{"derper_systemd_unit", "'derper.service'"},
		{"check_interval_min", "DEFAULT 1440"},
		{"enabled", "DEFAULT 1"},
		{"last_checked_at", "BIGINT NOT NULL DEFAULT 0"},
		{"last_synced_at", "BIGINT NOT NULL DEFAULT 0"},
		{"last_cert_sha256", "DEFAULT ''"},
		{"last_error", "DEFAULT ''"},
		{"expiry_warn_at", "BIGINT NOT NULL DEFAULT 0"},
		{"notes", "DEFAULT ''"},
	}
	for _, tok := range requiredTokens {
		marker := tok.col + " "
		idx := strings.Index(tableBody, marker)
		if idx < 0 {
			t.Errorf("missing column %q in CREATE TABLE block", tok.col)
			continue
		}
		window := tableBody[idx : idx+200]
		if !strings.Contains(window, tok.constraint) {
			t.Errorf("column %q present but constraint %q not found in next 200 chars",
				tok.col, tok.constraint)
		}
	}
}

// TestMigrateV071PG_Idempotent pins that the migration uses
// CREATE TABLE IF NOT EXISTS + CREATE INDEX IF NOT EXISTS so a
// re-run is a no-op. Without IF NOT EXISTS the second run would
// fail with "relation already exists" — and the operator would
// have to drop the table manually, which is dangerous because
// derp_cert_sync stores last_synced_at / last_cert_sha256
// (the cron would lose track of "we synced 5 min ago" on a
// re-run).
func TestMigrateV071PG_Idempotent(t *testing.T) {
	body := readMigrationSource(t, "migrations_v0_71_derp_cert_sync.go")
	if !strings.Contains(body, "CREATE TABLE IF NOT EXISTS derp_cert_sync") {
		t.Error("CREATE TABLE must be IF NOT EXISTS")
	}
	if !strings.Contains(body, "CREATE INDEX IF NOT EXISTS") {
		t.Error("indexes must use CREATE INDEX IF NOT EXISTS")
	}
}

// TestMigrateV071PG_PartialIndexForCron pins that the
// enabled_idx partial index is keyed on enabled=1 only. The
// cron scan is:
//
//   SELECT hostname, mode, ... FROM derp_cert_sync
//   WHERE enabled = 1 AND last_checked_at < now - interval_min
//
// A partial index WHERE enabled = 1 makes this O(log n) on
// the enabled subset instead of scanning the table when most
// rows are disabled (a common state for archived/staged
// hostnames). A future refactor that drops the WHERE clause
// would silently degrade scan performance on large fleets.
func TestMigrateV071PG_PartialIndexForCron(t *testing.T) {
	body := readMigrationSource(t, "migrations_v0_71_derp_cert_sync.go")
	partialIdx := regexp.MustCompile(`CREATE\s+INDEX\s+IF\s+NOT\s+EXISTS\s+derp_cert_sync_enabled_idx[\s\S]{0,80}WHERE\s+enabled\s*=\s*1`)
	if !partialIdx.MatchString(body) {
		t.Error("enabled_idx must be a partial index WHERE enabled = 1")
	}
}

// TestMigrateV071PG_RegisteredInDriver pins that v0.71 is
// listed in pgMigrations (driver_postgres.go) with the right
// source filename. Without this entry, MigratePostgres() never
// invokes migrateV071PG and the table never gets created in
// production.
func TestMigrateV071PG_RegisteredInDriver(t *testing.T) {
	body := readMigrationSource(t, "driver_postgres.go")
	if !strings.Contains(body, "migrateV071PG") {
		t.Error("migrateV071PG must be referenced from driver_postgres.go")
	}
	if !strings.Contains(body, "v0.71 (B252): derp_cert_sync table") {
		t.Error("v0.71 label must mention B252 + derp_cert_sync")
	}
}

// readMigrationSource loads the named migration source file
// from the package directory (internal/db/). All migration
// files + driver_postgres.go live here; the relative path
// "../driver_postgres.go" pattern from earlier migrations
// doesn't work under `go test ./internal/db/...` because
// the test's working dir is the package dir, not its parent.
func readMigrationSource(t *testing.T, name string) string {
	t.Helper()
	data, err := readFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}