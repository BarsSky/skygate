// tailscale_config.go — every Tailscale setting skygate resolves at
// read time, and the source it came from.
//
// Split out of tailscale.go in refactor Phase D (2026-10-01).  The
// resolution ORDER is the contract here (DB override > env var >
// default) and the /admin/tailscale page renders which layer won, so
// the readers and their `...Source` companions belong together.
//
//   - tailscaleAuthKeyPath / tailscaleAuthKeyPathSource (B258 + B259)
//   - tailscaleAuthKeyDisabled / tailscaleAuthKeyMissingForStart
//   - tailscaleLoginServer / tailscaleLoginServerSource (B258)
//   - tailscaleHostname / tailscaleHostnameDefault (B251 reserved name)
//   - tailscaleStateDir

package admin

import (
	"os"
	"strings"

	"skygate/internal/db"
)

// tailscaleAuthKeyPathDBKey is the global_settings key for
// the operator's "auth key path" override (B258 + B259).
// Mirrors tailscale.login_server (B258's sibling) — the
// operator can toggle the path between /data/ts/authkey
// (enabled — tailscaled runs in the container) and /dev/null
// (disabled — entrypoint skip path) from the web UI without
// touching docker-compose.yml.
//
// Resolution order at read time (highest priority first):
//  1. global_settings[tailscale.auth_key_path]   (web-UI override)
//  2. s.TailscaleAuthKeyPath                     (SKYGATE_TS_AUTHKEY_FILE env var)
//  3. /data/ts/authkey                           (default)
//
// The web-UI override is consulted FIRST so the operator
// can flip the path without restarting the container or
// editing docker-compose.yml. The env-var fallback still
// applies for first-boot setups (the env var seeds the DB
// only when the row is empty — the v259 enable handler
// populates the DB on first invocation).
const tailscaleAuthKeyPathDBKey = "tailscale.auth_key_path"

// tailscaleAuthKeyPath returns the resolved path. See
// tailscaleAuthKeyPathDBKey for the resolution order. The
// default /data/ts/authkey lives in /data which is
// bind-mounted from the host's data/ dir, so it survives
// container restarts.
//
// Nil-safe: when s.DB is nil (unit tests, very early boot),
// the DB layer is skipped entirely and the helper falls
// through to the env-var / default layers. Production code
// always has s.DB set by the time this is called.
func (s *Service) tailscaleAuthKeyPath() string {
	// 1. Web-UI override (DB). Empty string means "not set".
	if s.DB != nil {
		if v, err := db.GetGlobalSetting(s.dbc(), tailscaleAuthKeyPathDBKey, ""); err == nil && v != "" {
			return v
		}
	}
	// 2. Env-var bootstrap (SKYGATE_TS_AUTHKEY_FILE).
	if s.TailscaleAuthKeyPath != "" {
		return s.TailscaleAuthKeyPath
	}
	// 3. Last-resort default.
	return "/data/ts/authkey"
}

// tailscaleAuthKeyDisabled is the B258 mirror of the entrypoint
// skip check (entrypoint.sh lines 50-55):
//   - `[ -f "/dev/null" ]` returns false (character device,
//     not a regular file) so the entrypoint correctly skips
//     tailscaled when SKYGATE_TS_AUTHKEY_FILE=/dev/null.
//   - The same sentinel must disable the UI's Start button so
//     the operator doesn't see a confusing
//     "read auth key: no such file" error on click.
//
// Returns true when the path is:
//   - "/dev/null" — the standard "disabled" sentinel; reading
//     returns EOF, so we cannot ever start tailscaled from here
//   - "/dev/null/*" — defensive, in case the operator typos
//     "/dev/nul" or similar
//   - empty string — defensive, treats missing path as disabled
//     (the entrypoint skips too)
//
// We deliberately do NOT treat "file doesn't exist" as
// disabled — that's a misconfiguration, not an intentional
// disable. The UI surfaces the "file missing" error in the
// Start response so the operator can paste a key to recover.
func (s *Service) tailscaleAuthKeyDisabled() bool {
	path := strings.TrimSpace(s.tailscaleAuthKeyPath())
	if path == "" {
		return true
	}
	// /dev/null + variants.
	if path == "/dev/null" || strings.HasPrefix(path, "/dev/null/") {
		return true
	}
	// /dev/null is a character device, not a regular file.
	// Stat the path: if it exists but isn't a regular file,
	// treat as disabled.
	if info, err := os.Stat(path); err == nil {
		if !info.Mode().IsRegular() {
			return true
		}
	}
	return false
}

// tailscaleAuthKeyMissingForStart is the B258.1 mirror of the
// "missing" UI state. Returns true when:
//   - the path is NOT disabled (i.e. tailscaleAuthKeyDisabled
//     returned false), AND
//   - the auth-key file does not exist OR is empty.
//
// In this state the operator has previously enabled in-container
// Tailscale (DB or env points at a regular file like /data/ts/authkey)
// but the file is gone — typically because the container was
// recreated without a volume-bind of /data/ts/, or the operator
// deleted it by hand after a save.
//
// The handler calls this in handleTailscaleStart to give a clear
// actionable error if the operator bypasses the (now hard-disabled)
// Start button. Pre-B258.1 the same call surfaced as
// "read auth key: open /data/ts/authkey: no such file or directory"
// which was a confusing ENOENT for a non-engineer operator to
// translate into "paste a key here".
func (s *Service) tailscaleAuthKeyMissingForStart() bool {
	if s.tailscaleAuthKeyDisabled() {
		return false
	}
	set, _ := s.readTailscaleAuthKey()
	return !set
}

// SetGlobalSettingForTest is a thin wrapper around
// db.SetGlobalSetting exposed on the Service so unit tests
// can seed the global_settings table without forking on
// the per-backend placeholder syntax (?  vs  $1,$2,...).
// The v0.33.1.13 login-server tests use it to set up a
// known DB state and assert the resolution order. v0.33.1.13.
func (s *Service) SetGlobalSettingForTest(key, value string) error {
	return db.SetGlobalSetting(s.dbc(), key, value)
}

// tailscaleLoginServerDBKey is the global_settings key for
// the user-editable headscale URL (set via /admin/tailscale
// "save_login_server" action). Lives in global_settings so
// the value survives container restarts, Postgres → SQLite
// migrations, and VM clones. v0.33.1.13.
//
// Resolution order at read time (highest priority first):
//  1. global_settings[tailscale.login_server]  (web-UI override)
//  2. s.TailscaleLoginServer                   (SKYGATE_TS_LOGIN_SERVER env var)
//  3. "https://head.example.com"               (last-resort default)
//
// The env var is only consulted on first start (when the
// global_settings row is empty) — once the operator saves a
// value via the web UI, the env var is ignored until the
// operator clears the DB row (e.g. via `sqlite3 skygate.db
// "DELETE FROM global_settings WHERE key='tailscale.login_server'"`).
// This makes the deployment fully re-creatable from the web
// UI without touching the .env file.
const tailscaleLoginServerDBKey = "tailscale.login_server"

func (s *Service) tailscaleLoginServer() string {
	// 1. Web-UI override (DB). Empty string means "not set".
	if v, err := db.GetGlobalSetting(s.dbc(), tailscaleLoginServerDBKey, ""); err == nil && v != "" {
		return v
	}
	// 2. Env-var bootstrap.
	if s.TailscaleLoginServer != "" {
		return s.TailscaleLoginServer
	}
	// 3. Last-resort default.
	return "https://head.example.com"
}

// tailscaleLoginServerSource reports which of the three layers
// (db / env / default) the running config is actually using.
// Returns "db", "env", or "default". The template uses this
// to render a small "source: ..." hint so the operator knows
// whether changing the .env file would have any effect.
func (s *Service) tailscaleLoginServerSource() string {
	if v, err := db.GetGlobalSetting(s.dbc(), tailscaleLoginServerDBKey, ""); err == nil && v != "" {
		return "db"
	}
	if s.TailscaleLoginServer != "" {
		return "env"
	}
	return "default"
}

// tailscaleAuthKeyPathSource returns "db", "env", or "default"
// based on where tailscaleAuthKeyPath() resolved its value
// from. Mirrors tailscaleLoginServerSource — the template
// uses this to render a "source: db/web-UI" vs "source:
// SKYGATE_TS_AUTHKEY_FILE env" hint so the operator knows
// which value actually wins. B259.
//
// Nil-safe: when s.DB is nil (unit tests), falls through to
// the env-var / default layer.
func (s *Service) tailscaleAuthKeyPathSource() string {
	if s.DB != nil {
		if v, err := db.GetGlobalSetting(s.dbc(), tailscaleAuthKeyPathDBKey, ""); err == nil && v != "" {
			return "db"
		}
	}
	if s.TailscaleAuthKeyPath != "" {
		return "env"
	}
	return "default"
}

// tailscaleHostnameDefault is the reserved canonical hostname of the VM that runs the
// skygate container itself.
//
// B251: `skygate-host` is reserved for the single VM that runs the skygate container
// (the `infra` headscale user). The pre-B251 default `skygate-host-1` was a
// placeholder from v0.33.1.9 when only one skygate VM existed; with HA and replicas
// the un-suffixed form is canonical and `isInfraNode` in internal/nodeownership
// matches it strictly. The prefix `skygate-host-` was deprecated in B251 — only the
// exact reserved name moves into `infra` automatically.
func tailscaleHostnameDefault() string {
	return "skygate-host"
}

// tailscaleHostname is the name the tailnet should see.
//
// B321 (2026-09-25): this used to return the frozen `SKYGATE_TS_HOSTNAME` value from
// the container environment, so an install whose .env still carried the legacy
// `skygate-host-1` placeholder renamed its node straight back to the suffixed form on
// every Start click and after every update — the operator's live report. The
// resolution now lives in tailscaleHostnameResolved (DB > env > default) and the
// legacy placeholder shape is rewritten to tailscaleHostnameDefault() instead of being
// honoured.
func (s *Service) tailscaleHostname() string {
	name, _, _ := s.tailscaleHostnameResolved()
	if name == "" {
		return tailscaleHostnameDefault()
	}
	return name
}

// tailscaleStateDir is the --statedir tailscaled writes to.
// The /var/lib/tailscale directory needs to be on a persistent
// path so the auth state + node identity survive container
// restarts. v0.32.x bind-mounts this from the host.
func (s *Service) tailscaleStateDir() string {
	return "/var/lib/tailscale"
}
