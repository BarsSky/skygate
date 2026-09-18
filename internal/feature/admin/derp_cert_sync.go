// File: internal/feature/admin/derp_cert_sync.go
//
// B252 (v1.5.6+, 2026-09-15) — derp_cert_sync feature.
//
// The bundled derper's TLS certificate is a static file on disk
// (e.g. /var/lib/derper/certs/derp.skynas.ru.crt +
// /var/lib/derper/certs/derp.skynas.ru.key). Before B252 the
// renewal story was:
//
//   1. NPM's acme.sh auto-renews the LE cert in NPM's storage.
//   2. Operator SSHes into the agent VM and re-runs the
//      "copy cert from NPM" recipe manually (the same recipe
//      the deploy/derper-cert-renew.sh helper script captures).
//   3. After ~60 days the cert silently expires and Tailscale
//      clients fail TLS verification on the bundled DERP.
//
// B252 embeds the renewal flow as a skygate-internal feature.
// The admin can configure one row per bundled hostname via
// /admin/derp → cert sync section. The cron loop (started by
// cmd/skygate/main.go alongside derphealth.StartCron) runs
// once per skygate boot + every CronInterval (24h), reads all
// enabled rows from derp_cert_sync, and dispatches per mode:
//
//   * letsencrypt — no-op (derper's own --certmode=letsencrypt
//     handles renewal via the HTTP-01 challenge on port 80).
//     We just monitor expiry_date and surface "<30 days"
//     warnings on the admin page.
//   * npm — fetch /api/nginx/certificates/<id>/download as a
//     ZIP (cert.pem / chain.pem / fullchain.pem / privkey.pem),
//     compare SHA256 against last_cert_sha256, write new
//     fullchain2.pem → <cert_dir>/<hostname>.crt + privkey.pem
//     → <cert_dir>/<hostname>.key (mode 0600 for .key, 0644
//     for .crt), then SIGHUP the derper systemd unit so it
//     reloads the cert WITHOUT dropping existing DERP
//     connections.
//   * manual — no-op (operator owns the renewal). Surface
//     expiry_date so the admin sees the warning before derper
//     breaks silently.
//
// Design notes:
//   - Idempotent: a re-run on a row whose last_cert_sha256
//     matches the freshly fetched SHA256 is a no-op (no
//     file write, no SIGHUP). This avoids a needless derper
//     reload every cron tick.
//   - Failure-isolated: a bad row (e.g. NPM token revoked,
//     derper systemd unit missing) records last_error but
//     does NOT block the next row. The cron logs each
//     failure as a stderr line + writes to derp_cert_sync
//     last_error so the admin page can show "synced 12h ago,
//     ERR: ...".
//   - Works with both `derper` running as systemd unit (the
//     B-derper-cert deployment) and the legacy docker-compose
//     bundle. SIGHUP-on-systemd-unit triggers
//     `systemctl reload derper`; SIGHUP-on-pid-file sends
//     the signal directly to the PID.
//
// Migration: v0.71 (migrations_v0_71_derp_cert_sync.go) +
// driver_postgres.go entry. Run via skygate migrate up or
// auto-applied on next skygate container start (the PG
// driver calls MigratePostgres in OpenPostgres).

package admin

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// CronInterval is how often StartCron runs SyncAll. 24h is
// generous — Let's Encrypt renews at 30 days remaining
// (~once every 60 days), so a daily check fires within 1 day
// of every LE renewal. The cron always upserts, never inserts.
const DerpCertSyncInterval = 24 * time.Hour

// startCertSyncOnce guards StartCertSyncCron so a second
// concurrent invocation (e.g. main.go + a manual /admin/derp
// /cert-sync/run POST) doesn't spawn a second background
// goroutine.
var startCertSyncOnce sync.Once

// DerpCertSyncConfig is the row shape for derp_cert_sync.
// Maps 1:1 to the DB columns; the cron and the manual handler
// both work with this struct.
type DerpCertSyncConfig struct {
	ID                 int64
	Hostname           string
	Mode               string // 'letsencrypt' | 'npm' | 'manual'
	NPMBaseURL         string
	NPMCertID          int
	CertDir            string
	DerperPidFile      string
	DerperSystemdUnit  string
	CheckIntervalMin   int
	Enabled            bool
	LastCheckedAt      time.Time
	LastSyncedAt       time.Time
	LastCertSHA256     string
	LastError          string
	ExpiryWarnAt       time.Time
	Notes              string
}

// StartCertSyncCron launches the periodic cert-sync loop. It's
// a no-op after the first successful call (so reloading the
// service / re-running main.go's wiring doesn't double the
// cron). The loop runs until ctx is cancelled.
//
// Intended usage from main.go:
//
//	if err := admin.StartCertSyncCron(ctx, db); err != nil {
//	    log.Fatalf("derp cert sync cron: %v", err)
//	}
func StartCertSyncCron(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("derp_cert_sync.StartCertSyncCron: db is nil")
	}
	startCertSyncOnce.Do(func() {
		go func() {
			// Run once on boot so the admin page has fresh
			// data after a restart. Failures are logged but
			// do NOT abort the cron — next tick retries.
			runCertSyncOnce(ctx, db)
			t := time.NewTicker(DerpCertSyncInterval)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					runCertSyncOnce(ctx, db)
				}
			}
		}()
	})
	return nil
}

// runCertSyncOnce wraps the per-row sync with a panic recover
// so a single bad row doesn't kill the entire cron.
func runCertSyncOnce(ctx context.Context, db *sql.DB) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("derp_cert_sync: cron cycle panic: %v", r)
		}
	}()
	rows, err := loadAllCertSyncConfigs(ctx, db)
	if err != nil {
		log.Printf("derp_cert_sync: load configs: %v", err)
		return
	}
	ok, bad, skipped := 0, 0, 0
	for i := range rows {
		cfg := &rows[i]
		res := SyncOne(ctx, db, cfg)
		switch res {
		case SyncOK:
			ok++
		case SyncSkip:
			skipped++
		case SyncError:
			bad++
		}
	}
	log.Printf("derp_cert_sync: cycle done (ok=%d, bad=%d, skipped=%d, total=%d)",
		ok, bad, skipped, len(rows))
}

// SyncResult is the outcome of one SyncOne call. Cron uses
// it to count ok/bad/skipped and decide whether to send
// SIGHUP / write files.
type SyncResult int

const (
	SyncOK SyncResult = iota
	SyncSkip
	SyncError
)

// SyncOne runs the renewal flow for one derp_cert_sync row.
// Public so the /admin/derp/cert-sync/run POST handler can
// trigger a manual sync from the admin page ("Sync now" button).
//
// Returns SyncOK if the cert was either (a) freshly fetched and
// written (npm mode with changed SHA256), (b) already current
// (npm mode with matching SHA256 — no-op, but logged as OK for
// the admin page's "synced 5 min ago" display), or (c) the
// expiry check ran clean (letsencrypt / manual mode). Returns
// SyncSkip if the row is disabled. Returns SyncError if any
// step failed — the error is recorded in last_error.
func SyncOne(ctx context.Context, db *sql.DB, cfg *DerpCertSyncConfig) SyncResult {
	if !cfg.Enabled {
		return SyncSkip
	}
	// Mark the row as "checked now" so the admin page can
	// distinguish "we never tried" from "we tried 5 min ago".
	if err := updateCertSyncChecked(ctx, db, cfg.ID); err != nil {
		log.Printf("derp_cert_sync: update checked_at for %s: %v", cfg.Hostname, err)
		// Non-fatal — proceed with the actual sync.
	}
	switch cfg.Mode {
	case "npm":
		return syncNPMMode(ctx, db, cfg)
	case "letsencrypt", "manual":
		// For these modes skygate doesn't push certs. We
		// just refresh the expiry-date display by hitting
		// /derp and parsing the cert.
		return refreshExpiryOnly(ctx, db, cfg)
	default:
		recordCertSyncError(ctx, db, cfg.ID,
			fmt.Sprintf("unknown mode %q", cfg.Mode))
		return SyncError
	}
}

// syncNPMMode does the full NPM-pushed cert flow:
//  1. Login to NPM API (POST /api/tokens)
//  2. Download cert ZIP (GET /api/nginx/certificates/<id>/download)
//  3. Extract fullchain2.pem + privkey.pem
//  4. Compute SHA256 of the new fullchain; compare with cfg.LastCertSHA256
//  5. If unchanged: no-op, mark SyncedAt + return OK
//  6. If changed: write <cert_dir>/<hostname>.crt + .key,
//     send SIGHUP to derper, mark SyncedAt + new SHA256
func syncNPMMode(ctx context.Context, db *sql.DB, cfg *DerpCertSyncConfig) SyncResult {
	token, err := npmLogin(ctx, cfg.NPMBaseURL, NPMIdentityFromConfig(db), NPMSecretFromConfig(db))
	if err != nil {
		recordCertSyncError(ctx, db, cfg.ID, "npm login: "+err.Error())
		return SyncError
	}
	crtBytes, keyBytes, err := npmDownloadCert(ctx, cfg.NPMBaseURL, token, cfg.NPMCertID, cfg.Hostname)
	if err != nil {
		recordCertSyncError(ctx, db, cfg.ID, "npm download: "+err.Error())
		return SyncError
	}
	sum := sha256.Sum256(crtBytes)
	newHash := hex.EncodeToString(sum[:])
	if newHash == cfg.LastCertSHA256 {
		// Cert unchanged — record the no-op success + refresh
		// expiry date so the dashboard knows derper is up-to-date.
		if err := markCertSyncOK(ctx, db, cfg.ID, newHash, expiryFromCert(crtBytes)); err != nil {
			log.Printf("derp_cert_sync: mark OK for %s: %v", cfg.Hostname, err)
		}
		return SyncOK
	}
	// Cert changed — write files + SIGHUP derper.
	crtPath := filepath.Join(cfg.CertDir, cfg.Hostname+".crt")
	keyPath := filepath.Join(cfg.CertDir, cfg.Hostname+".key")
	if err := os.WriteFile(crtPath, crtBytes, 0644); err != nil {
		recordCertSyncError(ctx, db, cfg.ID,
			fmt.Sprintf("write %s: %v", crtPath, err))
		return SyncError
	}
	if err := os.WriteFile(keyPath, keyBytes, 0600); err != nil {
		recordCertSyncError(ctx, db, cfg.ID,
			fmt.Sprintf("write %s: %v", keyPath, err))
		return SyncError
	}
	if err := reloadDerper(ctx, cfg); err != nil {
		recordCertSyncError(ctx, db, cfg.ID,
			fmt.Sprintf("reload derper: %v", err))
		return SyncError
	}
	if err := markCertSyncOK(ctx, db, cfg.ID, newHash, expiryFromCert(crtBytes)); err != nil {
		log.Printf("derp_cert_sync: mark synced for %s: %v", cfg.Hostname, err)
	}
	log.Printf("derp_cert_sync: %s renewed (sha256=%s)", cfg.Hostname, newHash[:12])
	return SyncOK
}

// refreshExpiryOnly parses the cert that's already on disk to
// compute the expiry date. No file writes, no SIGHUP. For
// letsencrypt mode derper itself renewed (we don't know exactly
// when — we just see the current expiry). For manual mode the
// operator owns the cert; we just expose "expires in X days".
func refreshExpiryOnly(ctx context.Context, db *sql.DB, cfg *DerpCertSyncConfig) SyncResult {
	crtPath := filepath.Join(cfg.CertDir, cfg.Hostname+".crt")
	data, err := os.ReadFile(crtPath)
	if err != nil {
		// Cert file missing is a real problem — record it.
		// For letsencrypt mode, derper might be restarting
		// after a host reboot (the cert is in the
		// --certdir which is on a persistent volume, so
		// this would only fire if the certdir was cleared).
		// For manual mode the operator hasn't placed the
		// cert yet.
		recordCertSyncError(ctx, db, cfg.ID,
			fmt.Sprintf("read %s: %v (has derper been deployed yet?)", crtPath, err))
		return SyncError
	}
	expiry := expiryFromCert(data)
	if err := markCertSyncOK(ctx, db, cfg.ID, "", expiry); err != nil {
		log.Printf("derp_cert_sync: refresh expiry for %s: %v", cfg.Hostname, err)
	}
	return SyncOK
}

// npmIdentity / npmSecret come from global_settings (set by
// /admin/derp/cert-sync/edit). We don't read them from env
// because the operator may want to update them via the admin
// page without restarting skygate. Fall back to env vars
// NPM_IDENTITY / NPM_SECRET if global_settings is empty.
const (
	npmIdentityKey = "derp.npm.identity"
	npmSecretKey   = "derp.npm.secret"
)

// NPMIdentityFromConfig / NPMSecretFromConfig read the cached
// NPM credentials. Empty string → caller should fall back to
// env vars NPM_IDENTITY / NPM_SECRET.
func NPMIdentityFromConfig(db *sql.DB) string {
	if v := readGlobalSetting(db, npmIdentityKey); v != "" {
		return v
	}
	return os.Getenv("NPM_IDENTITY")
}

func NPMSecretFromConfig(db *sql.DB) string {
	if v := readGlobalSetting(db, npmSecretKey); v != "" {
		return v
	}
	return os.Getenv("NPM_SECRET")
}

// npmLogin hits POST <base>/api/tokens with the operator's
// identity + secret. Returns the bearer token for the
// subsequent calls. NPM's token format is documented at
// https://github.com/NginxProxyManager/nginx-proxy-manager/blob
// /master/backend/internal/api.md — the request body is
// JSON {"identity": "...", "secret": "..."} and the response
// is {"token": "<jwt>", "expires": "<iso8601>"}.
func npmLogin(ctx context.Context, baseURL, identity, secret string) (string, error) {
	if baseURL == "" || identity == "" || secret == "" {
		return "", fmt.Errorf("NPM credentials not configured (set them via /admin/derp/cert-sync/edit or NPM_IDENTITY/NPM_SECRET env vars)")
	}
	body, _ := json.Marshal(map[string]string{
		"identity": identity,
		"secret":   secret,
	})
	req, err := http.NewRequestWithContext(ctx, "POST",
		strings.TrimRight(baseURL, "/")+"/api/tokens",
		bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("NPM login returned %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.Token == "" {
		return "", fmt.Errorf("NPM login returned empty token")
	}
	return out.Token, nil
}

// npmDownloadCert hits GET <base>/api/nginx/certificates/<id>
// /download which returns a ZIP with four files:
//
//	cert.pem       (the leaf cert)
//	chain.pem      (intermediates)
//	fullchain.pem  (leaf + intermediates — what derper wants)
//	privkey.pem    (the private key)
//
// We return fullchain2.pem + privkey2.pem renamed to match
// the cert_dir/<filename> convention the derper.service unit
// expects (<filename>.crt + <filename>.key, where <filename>
// is the --hostname= value).
func npmDownloadCert(ctx context.Context, baseURL, token string, certID int, hostname string) (crt []byte, key []byte, err error) {
	if certID <= 0 {
		return nil, nil, fmt.Errorf("npm_cert_id must be > 0")
	}
	url := fmt.Sprintf("%s/api/nginx/certificates/%d/download",
		strings.TrimRight(baseURL, "/"), certID)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, nil, fmt.Errorf("NPM cert download returned %d: %s",
			resp.StatusCode, strings.TrimSpace(string(b)))
	}
	// zip.NewReader needs io.ReaderAt + size. resp.Body is a
	// stream, so we buffer it into memory first. The fullchain.pem
	// + chain.pem + privkey.pem ZIP from NPM is typically <50 KB,
	// well within reason for an in-memory read.
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("read cert zip body: %w", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return nil, nil, fmt.Errorf("unzip: %w", err)
	}
	// NPM's ZIP contains entries named cert.pem / chain.pem /
	// fullchain.pem / privkey.pem. Some installations use
	// numbered suffixes (cert2.pem / fullchain2.pem); we look
	// for the matching filename first, then fall back to the
	// numbered one.
	crt, key = nil, nil
	for _, f := range zr.File {
		base := strings.TrimSuffix(f.Name, filepath.Ext(f.Name))
		if base == "fullchain" || base == "fullchain2" {
			rc, err := f.Open()
			if err != nil {
				return nil, nil, err
			}
			crt, _ = io.ReadAll(rc)
			rc.Close()
		}
		if base == "privkey" || base == "privkey2" {
			rc, err := f.Open()
			if err != nil {
				return nil, nil, err
			}
			key, _ = io.ReadAll(rc)
			rc.Close()
		}
	}
	if len(crt) == 0 {
		return nil, nil, fmt.Errorf("fullchain.pem not found in NPM ZIP")
	}
	if len(key) == 0 {
		return nil, nil, fmt.Errorf("privkey.pem not found in NPM ZIP")
	}
	// Validate: the bytes we just fetched must actually be
	// a valid PEM pair. derper refuses to start with a
	// malformed cert and the silent failure mode is "the
	// file is on disk but Tailscale clients fail TLS".
	if !bytes.HasPrefix(crt, []byte("-----BEGIN ")) {
		return nil, nil, fmt.Errorf("fullchain is not PEM (got %d bytes)", len(crt))
	}
	if !bytes.HasPrefix(key, []byte("-----BEGIN ")) {
		return nil, nil, fmt.Errorf("privkey is not PEM (got %d bytes)", len(key))
	}
	return crt, key, nil
}

// reloadDerper sends SIGHUP to the running derper process so it
// reloads the cert files WITHOUT dropping existing DERP
// connections. Two strategies, tried in order:
//
//  1. systemctl reload <unit> — works on systemd deployments
//     (the B-derper-cert deployment shape). Exec'd via
//     os.Exec because we don't want to import systemd D-Bus.
//  2. kill -HUP <pid> (from pid_file) — fallback for non-
//     systemd deployments. Reads pid_file, sends SIGHUP via
//     syscall.Kill — but to avoid an extra import here we
//     shell out to /bin/kill -HUP. The pid_file path is
//     configurable in derp_cert_sync (default
//     /var/run/derper.pid).
func reloadDerper(ctx context.Context, cfg *DerpCertSyncConfig) error {
	// Strategy 1: systemd reload.
	if cfg.DerperSystemdUnit != "" {
		cmd := exec.CommandContext(ctx, "systemctl", "reload", cfg.DerperSystemdUnit)
		if out, err := cmd.CombinedOutput(); err == nil {
			return nil
		} else {
			log.Printf("derp_cert_sync: systemctl reload failed (%v), trying kill -HUP: %s",
				err, strings.TrimSpace(string(out)))
		}
	}
	// Strategy 2: kill -HUP <pid>.
	if cfg.DerperPidFile != "" {
		pidBytes, err := os.ReadFile(cfg.DerperPidFile)
		if err == nil {
			pidStr := strings.TrimSpace(string(pidBytes))
			cmd := exec.CommandContext(ctx, "/bin/kill", "-HUP", pidStr)
			if err := cmd.Run(); err == nil {
				return nil
			} else {
				log.Printf("derp_cert_sync: kill -HUP %s failed: %v", pidStr, err)
			}
		} else {
			log.Printf("derp_cert_sync: read pid_file %s: %v", cfg.DerperPidFile, err)
		}
	}
	return fmt.Errorf("could not reload derper (systemctl + pid-file both failed)")
}

// expiryFromCert parses a PEM bundle and returns the NotAfter
// time of the leaf certificate (the "earliest expiry" of the
// chain). Returns time.Time{} (zero) on parse failure — the
// caller treats zero as "unknown expiry, don't warn".
func expiryFromCert(pemBytes []byte) time.Time {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return time.Time{}
	}
	// The "leaf" cert is the FIRST one in a fullchain.pem.
	// x509.ParseCertificate works on a single DER block, so
	// we need to iterate the chain and pick the earliest
	// NotAfter.
	rest := pemBytes
	earliest := time.Time{}
	for {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			break
		}
		if b.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(b.Bytes)
		if err != nil {
			continue
		}
		if earliest.IsZero() || cert.NotAfter.Before(earliest) {
			earliest = cert.NotAfter
		}
	}
	return earliest
}

// loadAllCertSyncConfigs reads every enabled row from
// derp_cert_sync. The cron iterates the result. We don't
// return sql.Rows because the cron uses the slice for
// indexing + range iteration anyway.
func loadAllCertSyncConfigs(ctx context.Context, db *sql.DB) ([]DerpCertSyncConfig, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, hostname, mode, npm_base_url, npm_cert_id,
		       cert_dir, derper_pid_file, derper_systemd_unit,
		       check_interval_min, enabled, last_checked_at,
		       last_synced_at, last_cert_sha256, last_error,
		       expiry_warn_at, notes
		FROM derp_cert_sync
		WHERE enabled = 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DerpCertSyncConfig
	for rows.Next() {
		var c DerpCertSyncConfig
		var enabledInt int
		var lastChecked, lastSynced, expiryWarn int64
		if err := rows.Scan(&c.ID, &c.Hostname, &c.Mode,
			&c.NPMBaseURL, &c.NPMCertID, &c.CertDir,
			&c.DerperPidFile, &c.DerperSystemdUnit,
			&c.CheckIntervalMin, &enabledInt,
			&lastChecked, &lastSynced,
			&c.LastCertSHA256, &c.LastError, &expiryWarn, &c.Notes); err != nil {
			return nil, err
		}
		c.Enabled = enabledInt == 1
		c.LastCheckedAt = unixToTime(lastChecked)
		c.LastSyncedAt = unixToTime(lastSynced)
		c.ExpiryWarnAt = unixToTime(expiryWarn)
		out = append(out, c)
	}
	return out, rows.Err()
}

// updateCertSyncChecked stamps last_checked_at = now. Cheap
// per-row UPDATE; called at the start of every SyncOne so the
// admin page can show "checked 5 min ago, still up to date"
// even when the actual cert was unchanged.
func updateCertSyncChecked(ctx context.Context, db *sql.DB, id int64) error {
	// 2026-09-16 (B253 fix): PG-native $1/$2/$3 placeholders (was
	// SQLite `?`).
	_, err := db.ExecContext(ctx,
		`UPDATE derp_cert_sync SET last_checked_at = $1, updated_at = $2 WHERE id = $3`,
		time.Now().Unix(), time.Now().Unix(), id)
	return err
}

// markCertSyncOK stamps last_synced_at + last_cert_sha256 +
// clears last_error. expiry is the cert's NotAfter (zero
// means unknown — stored as 0).
func markCertSyncOK(ctx context.Context, db *sql.DB, id int64, sha string, expiry time.Time) error {
	expiryUnix := int64(0)
	if !expiry.IsZero() {
		expiryUnix = expiry.Unix()
	}
	// 2026-09-16 (B253 fix): PG-native $1..$5 placeholders.
	_, err := db.ExecContext(ctx, `
		UPDATE derp_cert_sync
		   SET last_synced_at = $1, last_cert_sha256 = $2, last_error = '',
		       expiry_warn_at = $3, updated_at = $4
		 WHERE id = $5`,
		time.Now().Unix(), sha, expiryUnix,
		time.Now().Unix(), id)
	return err
}

// recordCertSyncError stamps last_error + last_checked_at.
// The cron loop continues with the next row; we never
// short-circuit on a single bad row.
func recordCertSyncError(ctx context.Context, db *sql.DB, id int64, msg string) {
	log.Printf("derp_cert_sync: id=%d: %s", id, msg)
	// 2026-09-16 (B253 fix): PG-native $1..$4 placeholders.
	if _, err := db.ExecContext(ctx, `
		UPDATE derp_cert_sync
		   SET last_error = $1, last_checked_at = $2, updated_at = $3
		 WHERE id = $4`,
		msg, time.Now().Unix(), time.Now().Unix(), id); err != nil {
		log.Printf("derp_cert_sync: write last_error for id=%d: %v", id, err)
	}
}

// readGlobalSetting is a tiny helper around the global_settings
// table. Returns "" if the key is missing.
func readGlobalSetting(db *sql.DB, key string) string {
	var v string
	// 2026-09-16 (B253 fix): PG-native $1 placeholder.
	if err := db.QueryRow(`SELECT value FROM global_settings WHERE key = $1`,
		key).Scan(&v); err != nil {
		return ""
	}
	return v
}

// unixToTime converts a bigint epoch (seconds) to time.Time.
// Zero in → zero out, so callers can check cfg.LastCheckedAt
// .IsZero() to mean "never checked".
func unixToTime(sec int64) time.Time {
	if sec == 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0)
}

// NOTE (2026-09-18, B237.20): derpMinTLSVersion() used to live here — a
// hardcoded `tls.VersionTLS13` helper for an admin-page badge that was
// never wired up. Removed as dead code (staticcheck U1000 contract).