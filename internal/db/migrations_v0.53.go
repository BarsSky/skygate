package db

import (
	"database/sql"
	"fmt"
)

// migrateV053 (v0.33.1.33, 2026-08-10): add exit_servers.ssh_port
// column for the B85 per-row SSH port override.
//
// Background. v0.33.1.29 B81 added the LookupExitServerSSHTarget
// helper that builds the SSH target for the next
// SetAdvertisedRoutes call. The chain is:
//
//	1. exit_servers.ssh_target (operator override — may include
//	   a non-default port via "user@host:port" syntax, e.g.
//	   "root@karolina.example.com:18022")
//	2. "root@<tailscale_ip>" (B81 auto-fallback, NO port —
//	   defaults to port 22)
//	3. "" (no row / no tailscale_ip)
//
// Step 2 is the "no operator override, just use the Tailscale
// IP" fast path. The B81 commit didn't include a port there
// because every live exit-server on the operator's VM had
// sshd on port 22. After the v0.33.1.32 B84 deploy brought
// the chain to /admin/telegram and the operator hit
// "Operation timed out" on ssh root@100.64.0.3, the operator
// observed (2026-08-10) that the design intent is "use
// Tailscale for SSH because the standard public path may be
// blocked, AND other ports may be open on the exit-node
// besides the canonical 22".
//
// The fix: add a per-row `exit_servers.ssh_port` column
// (TEXT, NOT NULL, DEFAULT '') and have the B81 auto-fallback
// use it:
//
//	auto-fallback = "root@<tailscale_ip>" if ssh_port is empty
//	auto-fallback = "root@<tailscale_ip>:<ssh_port>" otherwise
//
// The SetAdvertisedRoutes helper at internal/headscale/routes.go
// already parses "user@host:port" syntax (splits into target +
// -p <port> for the ssh command) — see the comment block at
// routes.go:222-230. So the new ssh_port value just slots into
// the existing string. No headscale-side changes.
//
// Why per-row, not a global SKYGATE_EXIT_SSH_PORT env var:
// different exit-nodes can have different ports. The operator
// might have emilia on 22, karolina on 18022, sharlotta on
// 2222 — a per-row column captures that without a per-node
// env var explosion. The existing global SKYGATE_EXIT_SSH_KEY
// (key path) is fine as a single env var because ALL exit-nodes
// share the same operator SSH key.
//
// Backward compat: existing rows have ssh_port = '' (the
// DEFAULT), so the auto-fallback produces "root@<tailscale_ip>"
// with no port — exactly the v0.33.1.29/v0.33.1.32 behaviour.
// New rows can set ssh_port via the /admin/exit-nodes form.
// The form pre-fills ssh_port = '' by default (preserves
// v0.33.1.29 behaviour for operators who don't need a
// non-default port).
//
// Idempotency. v0.33.1.21 B70 introduced the
// "runMigrateOnly" pre-deploy step + the
// TestRunMigrateOnly_Idempotent test (cmd/skygate/migrate_only_test.go)
// that runs the migration chain TWICE. SQLite's
// `ALTER TABLE ADD COLUMN` does NOT support `IF NOT EXISTS`
// pre-3.35 (and skygate is on the alpine `golang:1.25-alpine`
// base, which ships SQLite 3.40+, so the syntax DOES work —
// but the pre-check via pragma_table_info is portable across
// every supported version and a defensive guard against
// future SQLite downgrades). We do the check inline below.
func migrateV053(d *sql.DB) error {
	// Portable idempotency check: pragma_table_info exists
	// in every SQLite skygate supports, and on PG the
	// equivalent check is via information_schema.columns (the
	// PG version of this function is migrateV053PG and uses
	// `ADD COLUMN IF NOT EXISTS` directly, so the check
	// below only runs on SQLite).
	var n int
	if err := d.QueryRow(
		`SELECT count(*) FROM pragma_table_info('exit_servers') WHERE name='ssh_port'`,
	).Scan(&n); err != nil {
		return fmt.Errorf("migrate v0.53: check column: %w", err)
	}
	if n > 0 {
		// Column already present (re-run after partial apply,
		// or a backup restored from a post-V053 snapshot).
		// Migration is a no-op.
		return nil
	}
	if _, err := d.Exec(`ALTER TABLE exit_servers ADD COLUMN ssh_port TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("migrate v0.53: %w", err)
	}
	return nil
}
