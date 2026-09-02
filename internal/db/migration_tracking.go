// Package db — migration integrity tracking (v0.32.19).
//
// The migration system in this repo is based on idempotent SQL
// functions (migrateV0NN(d *sql.DB) error). Each migration is
// expected to be re-runnable: it uses `IF NOT EXISTS` guards and
// ignores "duplicate column" errors from ALTER TABLE.
//
// That design has one weakness: if a developer changes the body of
// an OLD migration (e.g. fixes a typo, changes a column type), the
// change is silently absorbed — the existing DB already has the
// pre-fix schema, the new code never re-runs, and the operator
// has no way to detect the drift until something breaks much
// later.
//
// This file adds a tracking table that records, for each
// migration, the SHA-256 of the migration body. At startup, the
// migrator checks every recorded checksum against the current
// in-binary SQL. A mismatch is a hard error (the migration was
// modified after being applied).
//
// Initial mode (v0.32.19) is SOFT: checksums are recorded but
// mismatches only produce a warning log line, not a fatal error.
// This gives observability without breaking any existing DB on
// the first deploy. After one release cycle of observation,
// switch the mode to HARD (fatal on mismatch) — that's v0.32.20.
package db

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"regexp"
	"strings"
)

// MigrationIntegrityMode controls whether a checksum mismatch is
// fatal or just a warning. SOFT is the v0.32.19 default; HARD is
// the target for v0.32.20.
type MigrationIntegrityMode int

const (
	// IntegrityModeSoft logs the mismatch and continues. The DB
	// keeps working; the warning is observable in skygate logs
	// and in /admin/audit (action='migration_checksum_mismatch').
	IntegrityModeSoft MigrationIntegrityMode = iota
	// IntegrityModeHard returns an error from the migrator.
	// Caller (Open) propagates the error and skygate refuses to
	// start, surfacing the problem at the operator's desk.
	IntegrityModeHard
)

// migrationIntegrityMode is the runtime mode. Default SOFT.
// To enable HARD, set SKYGATE_MIGRATION_INTEGRITY=hard in .env
// (or `migration_integrity=hard` in the config).
var migrationIntegrityMode = IntegrityModeSoft

// SetMigrationIntegrityMode is called once at startup from
// config.New() to apply the env-var override.
func SetMigrationIntegrityMode(mode MigrationIntegrityMode) {
	migrationIntegrityMode = mode
}

// ensureMigrationTrackingTable creates the applied_migrations
// table if it doesn't exist. Idempotent.
func ensureMigrationTrackingTable(d *sql.DB) error {
	_, err := d.Exec(`
		CREATE TABLE IF NOT EXISTS applied_migrations (
			version     INTEGER PRIMARY KEY,
			sha256      TEXT    NOT NULL,
			source_file TEXT    NOT NULL DEFAULT '',
			applied_at  INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
			first_seen  TEXT    NOT NULL DEFAULT ''
		)
	`)
	if err != nil {
		return fmt.Errorf("ensure applied_migrations: %w", err)
	}
	return nil
}

// ComputeMigrationChecksum returns the SHA-256 hex digest of the
// provided SQL string, AFTER whitespace normalization. The
// normalization is intentionally minimal: collapse runs of
// whitespace (including newlines) to a single space, then trim.
// This means a cosmetic reformat doesn't change the checksum,
// but a semantic change (different column, different DEFAULT,
// etc.) does.
func ComputeMigrationChecksum(sql string) string {
	normalized := normalizeMigrationSQL(sql)
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])
}

var (
	whitespaceRun = regexp.MustCompile(`\s+`)
)

// normalizeMigrationSQL collapses all whitespace runs to a single
// space and trims the result. This is the "what the migration
// does" fingerprint — cosmetic reformatting doesn't change it,
// semantic changes (e.g. DEFAULT value, column type) do.
func normalizeMigrationSQL(s string) string {
	return strings.TrimSpace(whitespaceRun.ReplaceAllString(s, " "))
}

// RecordMigrationApplied inserts a row into applied_migrations.
// On conflict (re-record with the same version), the existing
// row is left unchanged — first-write-wins. Returns the rowid
// of the (possibly new) row.
//
// B213: explicit ON CONFLICT (version) DO NOTHING so the
// `skygate migrate up` re-run is safe (idempotent). The
// pre-B213 implementation was a bare INSERT, which would
// fail with a UNIQUE-constraint violation on the second
// run. With the DO NOTHING, RecordMigrationApplied is
// truly idempotent — the only "real" errors are
// transient (DB down, FK violation), which we surface.
func RecordMigrationApplied(d *sql.DB, version int, sha256Hex, sourceFile, firstSeenVersion string) error {
	_, err := d.Exec(`
		INSERT INTO applied_migrations (version, sha256, source_file, first_seen)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (version) DO NOTHING
	`, version, sha256Hex, sourceFile, firstSeenVersion)
	if err != nil {
		return fmt.Errorf("record migration v%d: %w", version, err)
	}
	return nil
}

// GetRecordedMigrationChecksum returns the recorded sha256 for
// a given migration version. Empty string + nil error means
// the migration has not been recorded (first run, or a backfill
// is needed).
func GetRecordedMigrationChecksum(d *sql.DB, version int) (sha256Hex, firstSeen string, err error) {
	row := d.QueryRow(`
		SELECT sha256, first_seen FROM applied_migrations WHERE version = $1
	`, version)
	if err := row.Scan(&sha256Hex, &firstSeen); err != nil {
		if err == sql.ErrNoRows {
			return "", "", nil
		}
		return "", "", fmt.Errorf("get recorded sha for v%d: %w", version, err)
	}
	return sha256Hex, firstSeen, nil
}

// VerifyMigrationChecksum checks that the recorded sha256 for
// `version` matches the current in-binary sha256 of `sql`.
// Returns:
//   - ok=true, recorded="" : first run for this version (no record yet).
//     Caller should apply the migration and call RecordMigrationApplied.
//   - ok=true, recorded=X, current=X : match. Migration was already
//     applied with this exact SQL body. Caller can skip.
//   - ok=false, recorded=X, current=Y : MISMATCH. The migration
//     body in the binary differs from what was previously applied.
//     Caller behavior depends on migrationIntegrityMode:
//       SOFT: log a warning, return ok=true (continue).
//       HARD: return ok=false + non-nil error (refuse to start).
func VerifyMigrationChecksum(d *sql.DB, version int, currentSQL string) (ok bool, recorded, current string, err error) {
	current = ComputeMigrationChecksum(currentSQL)
	recorded, firstSeen, err := GetRecordedMigrationChecksum(d, version)
	if err != nil {
		return false, "", current, err
	}
	if recorded == "" {
		// First run — not recorded yet.
		return true, "", current, nil
	}
	if recorded == current {
		// Match — migration already applied with this exact body.
		return true, recorded, current, nil
	}
	// Mismatch.
	msg := fmt.Sprintf(
		"migration v%d checksum mismatch: recorded=%s, current=%s (first_seen=%s). "+
			"The migration body was modified after being applied. "+
			"This is a HARD error if integrity mode is hard; "+
			"a warning only if mode is soft. "+
			"To recover, restore the previous migration body in the binary and rebuild.",
		version, recorded[:12], current[:12], firstSeen,
	)
	if migrationIntegrityMode == IntegrityModeHard {
		return false, recorded, current, fmt.Errorf("%s", msg)
	}
	log.Printf("[migration-integrity] WARNING: %s", msg)
	return true, recorded, current, nil
}

// AllMigrationsForAudit returns a snapshot of all recorded
// migrations for the admin audit page (if/when we wire it).
// Returns slice ordered by version ASC.
func AllMigrationsForAudit(d *sql.DB) ([]MigrationRecord, error) {
	rows, err := d.Query(`
		SELECT version, sha256, source_file, applied_at, first_seen
		FROM applied_migrations
		ORDER BY version ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("query applied_migrations: %w", err)
	}
	defer rows.Close()
	var out []MigrationRecord
	for rows.Next() {
		var r MigrationRecord
		if err := rows.Scan(&r.Version, &r.SHA256, &r.SourceFile, &r.AppliedAt, &r.FirstSeen); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// MigrationRecord is one row in applied_migrations, for the audit
// /admin page (not yet wired; the type exists so the helper is
// usable from tests today).
type MigrationRecord struct {
	Version    int
	SHA256     string
	SourceFile string
	AppliedAt  int64
	FirstSeen  string
}

// IsIntegrityHard reports whether the current mode is HARD
// (for tests + the env-var override in cmd/skygate/main.go).
func IsIntegrityHard() bool {
	return migrationIntegrityMode == IntegrityModeHard
}
