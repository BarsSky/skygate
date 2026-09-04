package db

// migrations_v0_69_b235_3.go — v0.69 (B235.3) — add
// `name` column to derp_health (the Tailscale short
// label "1f", "22w") + a v0.68-back-compat form for
// pre-V069 databases.
//
// B235 split DERPInfo into Host (FQDN) + Name (short
// label), but the B189 V062 migration created the
// derp_health table without a `name` column. The
// upsertQuery therefore silently dropped the short
// label, and the .Name pill on /admin/derp/dashboard
// (added in B235) never rendered for public DERP rows
// — only the FQDN was persisted. B235.3 closes that
// gap: the new `name TEXT NOT NULL DEFAULT ''` column
// is ADDed (NOT NULL with default so existing rows
// are valid), the upsertQuery now writes `name`, and
// the dashboard template reuses the existing
// `{{if .Name}}` pill (no template change).
//
// 2026-09-04: v0.69 (B235.3).

import (
	"database/sql"
)

// migrateV069PG adds the `name` column to
// derp_health. Idempotent: re-running the migration
// on a table that already has `name` is a no-op
// (ADD COLUMN IF NOT EXISTS is PG 9.6+).
func migrateV069PG(d *sql.DB) error {
	// Step 1: add the `name` column. NOT NULL with
	// DEFAULT '' so existing rows are valid without
	// backfill — the next ProbeAll tick will populate
	// it from FetchPublicDERPs.
	if _, err := d.Exec(`
		ALTER TABLE derp_health
		  ADD COLUMN IF NOT EXISTS name TEXT NOT NULL DEFAULT ''
	`); err != nil {
		return err
	}
	return nil
}
