// File: internal/db/migrations_v0_71_derp_cert_sync.go
//
// v0.71 (B252) — derp_cert_sync table.
//
// B252 closes the design gap where the bundled derper's TLS
// certificate is a static file on disk (e.g. /var/lib/derper/
// certs/derp.skynas.ru.{crt,key}) that does not auto-renew.
// The previous operator flow was:
//
//  1. NPM's acme.sh auto-renews the LE cert in NPM's storage
//     (~every 60 days for Let's Encrypt).
//  2. Operator has to SSH into the agent VM and re-run the
//     "copy cert from NPM API" recipe (POST /api/tokens +
//     GET /api/nginx/certificates/<id>/download + unzip +
//     cp to /var/lib/derper/certs + systemctl reload derper).
//  3. After ~60 days the cert silently expires and Tailscale
//     clients start failing TLS verification when they try
//     to use the bundled DERP.
//
// B252 fixes the renewal flow at the source. The new
// derp_cert_sync table tracks one row per (hostname, mode)
// pair. Three modes:
//
//   * letsencrypt — derper's own --certmode=letsencrypt handles
//     renewal via the HTTP-01 challenge. We don't need to
//     push certs; we just monitor expiry_date via a HEAD
//     on /derp and warn the operator when <30 days remain.
//   * npm — the operator fronts derper with Nginx Proxy Manager
//     (NPM cert_id + base_url). skygate periodically fetches
//     the cert from NPM, compares SHA256, writes the new
//     files to <cert_dir>, and sends SIGHUP to derper so it
//     reloads the cert WITHOUT dropping existing connections.
//   * manual — operator owns the cert renewal (e.g. they
//     pay for a paid CA, use a private CA, or import from
//     an air-gapped store). skygate only displays expiry_date
//     so the operator sees "14 days left" warnings before
//     derper silently breaks.
//
// The companion package internal/feature/admin/derp_cert_sync.go
// runs a daily cron that:
//   - Iterates over derp_cert_sync rows with enabled=1.
//   - For mode='npm': fetches /api/nginx/certificates/<id>/download,
//     compares SHA256 against last_cert_sha256, writes files if
//     changed, sends SIGHUP to derper.
//   - For mode='letsencrypt' / 'manual': no-op (derper self-renews
//     or operator owns it); expiry is exposed via the dashboard.
//   - For all modes: records last_synced_at, last_cert_sha256,
//     and last_error (if any) so the /admin/derp page can show
//     "synced 5 min ago" / "ERR: NPM returned 401".
//
// Idempotent migration: CREATE TABLE IF NOT EXISTS + the
// indexes use CREATE INDEX IF NOT EXISTS so re-running the
// migration is a no-op.

package db

import (
	"database/sql"
)

// migrateV071PG creates the derp_cert_sync table. One row per
// (hostname, mode) pair. hostname is the public DNS name clients
// dial derp.skynas.ru as (matches the --hostname= derper flag);
// mode picks the renewal strategy; the npm_* columns are only
// populated when mode='npm'.
//
// Indexes:
//   * hostname_idx — derp_cert_sync is read by hostname when the
//     cron loop iterates rows
//   * mode_enabled_idx — partial index for the cron scan
//     ("enabled AND mode IN ('npm','letsencrypt')")
func migrateV071PG(d *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS derp_cert_sync (
			id                  BIGSERIAL PRIMARY KEY,
			hostname            TEXT NOT NULL UNIQUE,
			mode                TEXT NOT NULL DEFAULT 'letsencrypt',
			npm_base_url        TEXT NOT NULL DEFAULT '',
			npm_cert_id         INTEGER NOT NULL DEFAULT 0,
			cert_dir            TEXT NOT NULL DEFAULT '/var/lib/derper/certs',
			derper_pid_file     TEXT NOT NULL DEFAULT '/var/run/derper.pid',
			derper_systemd_unit TEXT NOT NULL DEFAULT 'derper.service',
			check_interval_min  INTEGER NOT NULL DEFAULT 1440,
			enabled             INTEGER NOT NULL DEFAULT 1,
			last_checked_at     BIGINT NOT NULL DEFAULT 0,
			last_synced_at      BIGINT NOT NULL DEFAULT 0,
			last_cert_sha256    TEXT NOT NULL DEFAULT '',
			last_error          TEXT NOT NULL DEFAULT '',
			expiry_warn_at      BIGINT NOT NULL DEFAULT 0,
			notes               TEXT NOT NULL DEFAULT '',
			created_at          BIGINT NOT NULL DEFAULT (EXTRACT(EPOCH FROM now())::bigint),
			updated_at          BIGINT NOT NULL DEFAULT (EXTRACT(EPOCH FROM now())::bigint)
		)`,
		`CREATE INDEX IF NOT EXISTS derp_cert_sync_enabled_idx
			ON derp_cert_sync(enabled) WHERE enabled = 1`,
		`CREATE INDEX IF NOT EXISTS derp_cert_sync_hostname_idx
			ON derp_cert_sync(hostname)`,
	}
	for _, s := range stmts {
		if _, err := d.Exec(s); err != nil {
			return err
		}
	}
	return nil
}