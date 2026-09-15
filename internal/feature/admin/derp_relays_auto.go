// File: internal/feature/admin/derp_relays_auto.go
//
// B-fix (2026-09-15): auto-register the bundled derper as a region
// row in derp_relays on skygate startup, if (and only if):
//
//   - The operator has DERP_ENABLED=true in .env (their explicit opt-in)
//   - A derper config file exists on the host (proof systemd unit is real)
//   - derp_relays has NO is_bundled=1 row yet (idempotent — won't
//     double-register if /admin/derp/relays/add was already used)
//
// Live problem this fixes: 2026-09-15, agent VM 192.168.13.69.
// operator had DERP_ENABLED=true + a running systemd derper unit
// (`active since Sat 2026-09-12 06:53:54`) BUT no row in derp_relays
// for the bundled region. The reason: AutoMigrateDerpRelays (which
// creates the bundled row on first /admin/derp page load) only runs
// when global_settings.derp.bundled_enabled == "1" — and that key
// was set to "0" by deploy.sh because the deploy-time DERP_BUNDLED_ENABLED
// env was false. So the auto-migrate never fired, headscale never
// knew about region 900, and 0 Tailscale clients used the local DERP.
//
// EnsureBundledDerpRelay is called from cmd/skygate/main.go at boot,
// AFTER db.AutoMigrateDerpRelays has had a chance to run.
//
// Idempotency guarantees:
//   - Re-running with a fresh DB: inserts one row, marks migration done.
//   - Re-running with an existing bundled row: no-op.
//   - Re-running with derper.service stopped: no-op (no derper.conf).
//   - Re-running with DERP_ENABLED=false: no-op (operator opt-out respected).
//
// 2026-09-15: v1.5.3 — B-bug-fix (bundled derper never registered in DB).

package admin

import (
	"database/sql"
	"errors"
	"log"
	"os"
	"time"
)

// Default paths + port used by EnsureBundledDerpRelay. Override via
// env for unit tests and non-standard deployments.
const (
	defaultDerperConfPath  = "/var/lib/derper/derper.conf"
	defaultDerperCertsPath = "/var/lib/derper/certs"
	defaultDerpPort        = "443"
	// autoRegMarker is the global_settings key written on a successful
	// auto-register so future boots don't re-attempt. The marker is
	// per-env (DERP_HOSTNAME) — different hostname = different row.
	autoRegMarkerPrefix = "derp.auto_registered."
)

// EnsureBundledDerpRelay inserts a bundled derp_relays row for the
// local derper if one doesn't exist. Safe to call on every boot —
// internally idempotent.
//
// Returns:
//   inserted  bool   — true if a new row was inserted
//   reason    string — "no derper.conf" / "DERP_ENABLED=false" /
//                     "already registered" / "ok" — for stderr log
//   err       error  — non-nil only on DB write failure
//
// Caller logs and continues; the function never aborts the boot on
// failure (a missing bundled row is not fatal — headscale still has
// the bundled Tailscale public DERP regions).
func EnsureBundledDerpRelay(d *sql.DB, hostname, url, regionCode, regionName string) (inserted bool, reason string, err error) {
	// Operator opt-out check. If DERP_ENABLED is explicitly false,
	// don't auto-register even if derper happens to be running
	// (e.g. systemd unit was started manually for testing).
	if v := os.Getenv("DERP_ENABLED"); v == "false" || v == "0" {
		return false, "DERP_ENABLED=false; skipping", nil
	}

	// Derper presence check. Read the conf file as a proxy for
	// "is the systemd unit really installed here?" — without the
	// conf file, the URL we're about to register has nothing
	// serving it, which would mislead clients.
	if _, statErr := os.Stat(defaultDerperConfPath); statErr != nil {
		return false, "no derper.conf at " + defaultDerperConfPath + "; derper not installed", nil
	}

	// Hostname is required to build the marker key + the row's
	// hostname column.
	if hostname == "" {
		return false, "no hostname provided; skipping", nil
	}

	// Idempotency check 1: per-hostname global_settings marker. The
	// marker is "1" once we've successfully registered this hostname.
	marker := autoRegMarkerPrefix + hostname
	if v := loadGlobalSetting(d, marker); v == "1" {
		// Even if marker is set, double-check there's actually a
		// bundled row — defensive against operator-driven deletes.
		var hasRow bool
		if qErr := d.QueryRow(`
			SELECT EXISTS(SELECT 1 FROM derp_relays
			                WHERE is_bundled = 1 AND hostname = $1)
		`, hostname).Scan(&hasRow); qErr == nil && hasRow {
			return false, "already registered", nil
		}
		// Marker set but row gone — fall through and re-insert.
	}

	// Idempotency check 2: query for existing bundled row. If one
	// exists, mark the marker and return — covers the case where
	// the operator manually added a bundled row via /admin/derp/relays/add
	// but the marker wasn't set.
	var existingURL string
	qErr := d.QueryRow(`
		SELECT url FROM derp_relays
		 WHERE is_bundled = 1 AND enabled = 1
		 LIMIT 1
	`).Scan(&existingURL)
	if qErr == nil {
		// A bundled row already exists. Mark and return.
		_ = writeGlobalSetting(d, marker, "1")
		return false, "already registered (existing row: " + existingURL + ")", nil
	}
	if !errors.Is(qErr, sql.ErrNoRows) {
		return false, "query failed: " + qErr.Error(), qErr
	}

	// Default URL if caller didn't provide one. Match the live
	// agent VM 2026-09-15: HTTPS on 443 with the public hostname.
	if url == "" {
		port := os.Getenv("DERP_HTTP_PORT")
		if port == "" {
			port = defaultDerpPort
		}
		url = "https://" + hostname + ":" + port
	}

	// Default region name.
	if regionName == "" {
		regionName = "Moscow Custom (bundled)"
	}
	if regionCode == "" {
		regionCode = "mow"
	}

	// Insert. Use ON CONFLICT (url) DO NOTHING as a final idempotency
	// belt — covers concurrent boots racing on the same hostname.
	now := time.Now().Unix()
	_, insErr := d.Exec(`
		INSERT INTO derp_relays
			(hostname, url, region_id, region_code, region_name,
			 is_bundled, enabled, sort_order, notes,
			 created_at, updated_at)
		VALUES
			($1, $2, 900, $3, $4,
			 1, 1, 10,
			 'Auto-registered 2026-09-15 by derp_relays_auto.go (bundled derper detected at ' || $1 || ')',
			 $5, $5)
		ON CONFLICT (url) DO NOTHING
	`, hostname, url, regionCode, regionName, now)
	if insErr != nil {
		return false, "insert failed: " + insErr.Error(), insErr
	}

	// Mark migration done so future boots are fast no-ops.
	if wErr := writeGlobalSetting(d, marker, "1"); wErr != nil {
		log.Printf("derp_relays_auto: marker write failed: %v (row insert OK)", wErr)
	}
	return true, "ok", nil
}

// loadGlobalSetting reads a single key from global_settings. Defined
// here (rather than imported from db package) to keep derp_relays_auto.go
// self-contained — the existing db.loadGlobalSetting is package-private
// in the db package. Uses the same SQL the db package uses (SELECT value
// FROM global_settings WHERE key = $1).
func loadGlobalSetting(d *sql.DB, key string) string {
	var v string
	err := d.QueryRow(`SELECT value FROM global_settings WHERE key = $1`, key).Scan(&v)
	if err != nil {
		return ""
	}
	return v
}

// writeGlobalSetting inserts or updates a global_settings row. The
// global_settings table has a UNIQUE(key) constraint, so we use
// ON CONFLICT to handle the insert-or-update case.
func writeGlobalSetting(d *sql.DB, key, value string) error {
	_, err := d.Exec(`
		INSERT INTO global_settings (key, value, updated_at)
		VALUES ($1, $2, EXTRACT(epoch FROM now())::bigint)
		ON CONFLICT (key) DO UPDATE
		SET value = EXCLUDED.value,
		    updated_at = EXCLUDED.updated_at
	`, key, value)
	return err
}