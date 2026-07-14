// 2026-07-14: Этап 14 v6 — backup config via UI.
//
// Replaces the hard-coded `BACKUP_DIR=...` env var with a per-deployment
// configuration stored in global_settings. The admin sets destination
// (URL or path), protocol (local / smb / nfs / sftp), mountpoint,
// credentials, retention, and schedule from the /admin/backup page.
// Two schedulers consume the config:
//
//   - in-app: a goroutine in the skygate process (started by
//     cmd/skygate/main.go) checks every minute and runs the
//     backup when due. Best-effort: dies with the process.
//
//   - system cron: `scripts/backup_cron.sh` (or systemd timer)
//     invokes `skygate backup-run`, which reads the same config
//     from the DB and runs the backup. More reliable because
//     it survives the skygate container being down.
//
// Both are configurable from the UI; either or both can be
// enabled. When both fire near-simultaneously, the in-app run
// is gated by a process-local mutex and the cron invocation
// does the same via the SKIP_WHEN_LOCKED file, so double
// backups don't pile up.

package backup

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"skygate/internal/db"
)

// Protocol enumerates the supported backup destinations. The
// string values are stored verbatim in global_settings
// (backup.protocol) — never rename them without a migration.
type Protocol string

const (
	ProtocolLocal Protocol = "local" // local filesystem path, no mount
	ProtocolSMB   Protocol = "smb"   // SMB/CIFS share (mount -t cifs)
	ProtocolNFS   Protocol = "nfs"   // NFS share (mount -t nfs)
	ProtocolSFTP  Protocol = "sftp"  // SSHFS / FUSE (mount -t fuse.sshfs)
)

// AllProtocols lists every protocol the UI offers. The order
// here is also the order they appear in the dropdown.
var AllProtocols = []Protocol{ProtocolLocal, ProtocolSMB, ProtocolNFS, ProtocolSFTP}

// IsValidProtocol reports whether p is a known protocol. Used
// by the form validator and by the auto-detect fallback (when
// the user pastes a URL and the protocol field is empty, the
// form setter sniffs the scheme and writes a known value).
func IsValidProtocol(p Protocol) bool {
	for _, k := range AllProtocols {
		if k == p {
			return true
		}
	}
	return false
}

// Config is the full backup configuration persisted in
// global_settings. Field names map 1:1 to the storage keys
// (prefixed "backup."). Use Load / Save to round-trip.
type Config struct {
	// Destination is the protocol-specific target:
	//   local: absolute directory path on the host
	//   smb:   //host/share/path (or smb://host/share/path)
	//   nfs:   host:/export/path
	//   sftp:  user@host:/path (or sftp://...)
	// Empty string means "backup feature is not configured".
	Destination string

	// Protocol selects the mount + transport strategy. If
	// empty, Load() will auto-detect from Destination's scheme
	// (smb://, nfs://, sftp://, file://). Local paths (no
	// scheme) default to ProtocolLocal.
	Protocol Protocol

	// Mountpoint is the local directory where network shares
	// are mounted before the backup runs. For local, this
	// field is unused. The directory is created on demand
	// (mkdir -p, mode 0700) and removed on unmount failure
	// only if RunBackup created it (so a pre-existing
	// mountpoint is never deleted).
	Mountpoint string

	// Username + Password are SMB credentials. Password is
	// stored as plaintext in the DB (consistent with the
	// existing telegram.bot_token pattern in secrets.go — the
	// deployment's disk encryption is the threat-model
	// boundary). For SFTP, Username is the SSH user and
	// Password is unused (SSH key path is below).
	Username string
	Password string

	// SSHKeyPath is the path to the SSH private key for
	// SFTP/SSHFS mount. Unused for other protocols. The
	// file is chmod 600'd by the mount helper before being
	// passed to sshfs.
	SSHKeyPath string

	// KeepCount is how many archive files to retain at the
	// destination. Older archives are pruned on each
	// successful run. 0 = keep all (use with caution —
	// disk will fill).
	KeepCount int

	// Schedule is a 5-field cron expression. The in-app
	// scheduler uses a tiny parser that handles
	// "* * * * *" and "M H * * *" (the two patterns the UI
	// exposes); for anything else the user can wire
	// system cron and leave Schedule empty. Default is
	// "0 3 * * *" (daily at 03:00).
	Schedule string

	// Enabled is the master switch. When false, the in-app
	// scheduler is a no-op and "Run now" still works (so an
	// admin can test the config without scheduling). System
	// cron should also check this (the skygate backup-run
	// subcommand returns immediately when Enabled=false).
	Enabled bool

	// InAppEnabled controls the in-app goroutine scheduler
	// specifically. Independent of Enabled so the admin can
	// pause in-app backups while keeping system cron.
	InAppEnabled bool

	// --- status fields (read-only from the admin UI, written
	// by RunBackup). ---

	LastRun     int64  // unix seconds of last completed run (0 = never)
	LastStatus  string // "ok" / "fail" / "running" / ""
	LastError   string // last failure message (truncated to 1KB)
	LastArchive string // basename of the most recent archive
}

// storage keys (kept as constants so the schema name is in
// exactly one place — see migrations_*.go for the JSON column
// type if we ever move this to a real table).
const (
	keyDestination = "backup.destination"
	keyProtocol    = "backup.protocol"
	keyMountpoint  = "backup.mountpoint"
	keyUsername    = "backup.username"
	keyPassword    = "backup.password"
	keySSHKeyPath  = "backup.ssh_key_path"
	keyKeepCount   = "backup.keep_count"
	keySchedule    = "backup.schedule"
	keyEnabled     = "backup.enabled"
	keyInApp       = "backup.in_app_enabled"
	keyLastRun     = "backup.last_run"
	keyLastStatus  = "backup.last_status"
	keyLastError   = "backup.last_error"
	keyLastArchive = "backup.last_archive"
)

// Default values applied on first Load when no row exists. The
// admin is expected to fill destination before "Run now" works
// — keep it empty so a freshly-upgraded deployment doesn't
// accidentally rsync to /var/skygate-backups.
func Default() *Config {
	return &Config{
		Destination:  "",
		Protocol:     ProtocolLocal,
		Mountpoint:   "/mnt/skygate-backups",
		Username:     "",
		Password:     "",
		SSHKeyPath:   "/home/skyadmin/.ssh/skygate_sync",
		KeepCount:    10,
		Schedule:     "0 3 * * *",
		Enabled:      false,
		InAppEnabled: false,
		LastRun:      0,
		LastStatus:   "",
		LastError:    "",
		LastArchive:  "",
	}
}

// Load reads the config from global_settings. Missing keys
// fall back to Default() values. The function never returns
// nil — a Config with default values is returned on first
// deploy (before the admin saves anything) so callers don't
// need a nil check.
func Load(d *sql.DB) (*Config, error) {
	c := Default()
	rows, err := d.Query(`SELECT key, value FROM global_settings WHERE key LIKE 'backup.%'`)
	if err != nil {
		return nil, fmt.Errorf("load backup config: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		switch k {
		case keyDestination:
			c.Destination = v
		case keyProtocol:
			if IsValidProtocol(Protocol(v)) {
				c.Protocol = Protocol(v)
			} else if v != "" {
				// Unknown value in the DB. Don't silently
				// drop it; the form will display the raw
				// value so the admin can decide. For
				// Load() semantics we leave Protocol
				// empty so the auto-detect kicks in.
				c.Protocol = ""
			}
		case keyMountpoint:
			c.Mountpoint = v
		case keyUsername:
			c.Username = v
		case keyPassword:
			c.Password = v
		case keySSHKeyPath:
			c.SSHKeyPath = v
		case keyKeepCount:
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				c.KeepCount = n
			}
		case keySchedule:
			c.Schedule = v
		case keyEnabled:
			c.Enabled = v == "1"
		case keyInApp:
			c.InAppEnabled = v == "1"
		case keyLastRun:
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				c.LastRun = n
			}
		case keyLastStatus:
			c.LastStatus = v
		case keyLastError:
			c.LastError = v
		case keyLastArchive:
			c.LastArchive = v
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Auto-detect protocol from the destination URL if the
	// stored Protocol is empty (or invalid → already cleared
	// above). Saves the admin a click when they paste a URL
	// into the form but forget to pick the dropdown.
	if c.Protocol == "" && c.Destination != "" {
		c.Protocol = detectProtocol(c.Destination)
	}
	return c, nil
}

// Save writes the mutable fields to global_settings. Status
// fields (LastRun, LastStatus, LastError, LastArchive) are
// preserved as-is — the caller is expected to reload and
// update them via SetStatus.
func Save(d *sql.DB, c *Config) error {
	if c == nil {
		return errors.New("nil config")
	}
	// Validate the protocol explicitly. Empty string is
	// allowed (= "auto-detect on next load").
	if c.Protocol != "" && !IsValidProtocol(c.Protocol) {
		return fmt.Errorf("invalid protocol %q", c.Protocol)
	}
	if c.KeepCount < 0 {
		return fmt.Errorf("keep_count must be >= 0, got %d", c.KeepCount)
	}
	pairs := map[string]string{
		keyDestination: c.Destination,
		keyProtocol:    string(c.Protocol),
		keyMountpoint:  c.Mountpoint,
		keyUsername:    c.Username,
		keyPassword:    c.Password,
		keySSHKeyPath:  c.SSHKeyPath,
		keyKeepCount:   strconv.Itoa(c.KeepCount),
		keySchedule:    c.Schedule,
		keyEnabled:     boolToOnOff(c.Enabled),
		keyInApp:       boolToOnOff(c.InAppEnabled),
	}
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for k, v := range pairs {
		if _, err := tx.Exec(
			`INSERT INTO global_settings (key, value) VALUES (?, ?)
			 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = strftime('%s','now')`,
			k, v,
		); err != nil {
			return fmt.Errorf("save %s: %w", k, err)
		}
	}
	return tx.Commit()
}

// SetStatus updates only the last_run / last_status /
// last_error / last_archive fields. Called by RunBackup at
// the start (status=running) and end (status=ok|fail).
func SetStatus(d *sql.DB, status, errMsg, archive string, runAt time.Time) error {
	if runAt.IsZero() {
		runAt = time.Now()
	}
	pairs := map[string]string{
		keyLastRun:     strconv.FormatInt(runAt.Unix(), 10),
		keyLastStatus:  status,
		keyLastError:   errMsg,
		keyLastArchive: archive,
	}
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for k, v := range pairs {
		if _, err := tx.Exec(
			`INSERT INTO global_settings (key, value) VALUES (?, ?)
			 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = strftime('%s','now')`,
			k, v,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// detectProtocol sniffs a destination URL and returns a
// known Protocol. The rule is simple: if the URL has a
// scheme, use it (smb://, nfs://, sftp://). Otherwise it's
// a local path. The only ambiguity is "//host/share/..." —
// Go's url.Parse rejects that as a relative URL, so we
// special-case it as SMB.
func detectProtocol(dest string) Protocol {
	dest = strings.TrimSpace(dest)
	if dest == "" {
		return ""
	}
	if strings.HasPrefix(dest, "//") {
		// UNC-style SMB path: //host/share/...
		return ProtocolSMB
	}
	// Try parsing as URL. We don't use url.Parse strictly
	// because "host:/path" (NFS) is not RFC-3986 valid.
	if strings.HasPrefix(dest, "smb://") {
		return ProtocolSMB
	}
	// 2026-07-14: SFTP must be checked BEFORE the
	// "contains :/" NFS heuristic, because
	// sftp://user@host/path and user@host:/path both
	// contain ":/" — but they are SFTP, not NFS.
	if strings.HasPrefix(dest, "sftp://") {
		return ProtocolSFTP
	}
	if strings.Contains(dest, "@") && strings.Contains(dest, ":") {
		// user@host:/path style — SFTP.
		return ProtocolSFTP
	}
	if strings.HasPrefix(dest, "nfs://") || strings.Contains(dest, ":/") {
		// ":/" is a strong NFS signal (host:/path) but
		// "C:/" on Windows would also match. Restrict to
		// cases where the part before ":/" has no Windows
		// drive letter.
		idx := strings.Index(dest, ":/")
		if idx > 0 {
			prefix := dest[:idx]
			if !isWindowsDriveLetter(prefix) {
				return ProtocolNFS
			}
		}
	}
	return ProtocolLocal
}

func isWindowsDriveLetter(s string) bool {
	if len(s) != 1 {
		return false
	}
	c := s[0]
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

func boolToOnOff(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// Validate returns the first error in the config that would
// prevent a successful RunBackup. The UI form calls this
// before "Test connection" / "Run now" so the operator
// sees "destination is required" instead of a confusing
// mount-failure stack trace.
func (c *Config) Validate() error {
	if c.Destination == "" {
		return errors.New("destination is required")
	}
	if c.Protocol == "" {
		// Should be impossible after Load() (auto-detect)
		// but a manually-built Config might have it empty.
		c.Protocol = detectProtocol(c.Destination)
	}
	if !IsValidProtocol(c.Protocol) {
		return fmt.Errorf("unknown protocol %q", c.Protocol)
	}
	if c.Protocol != ProtocolLocal && c.Mountpoint == "" {
		return errors.New("mountpoint is required for non-local destinations")
	}
	if c.KeepCount < 0 {
		return errors.New("keep_count must be >= 0")
	}
	if c.Schedule != "" {
		// A pre-flight check on the schedule format; the
		// in-app scheduler's parser is more lenient
		// (supports only "*" / "M H * * *") but the UI
		// also accepts a free-form cron and stores it
		// verbatim for system cron to consume.
		if _, err := ParseSchedule(c.Schedule); err != nil {
			return fmt.Errorf("schedule: %w", err)
		}
	}
	return nil
}

// DB returns the underlying *sql.DB. Convenience for handlers
// that need to chain Load → mutate → Save without juggling
// the *Config pointer.
func (c *Config) DB(d *sql.DB) *Config { _ = d; return c }

// _ guards against an unused-import warning if some
// downstream edit removes all callers of db. Cheap to keep.
var _ db.User
