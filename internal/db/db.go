package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"
	_ "github.com/mattn/go-sqlite3"
)

type User struct {
	ID                 int64
	Username           string
	IsAdmin            bool
	Theme              string
	PasswordHash       string
	HeadscaleUserID    int64
	CreatedAt          time.Time
	SubnetCIDR         string // denorm: empty if no subnet allocated
	SubnetStatus       string // denorm: "none" / "pending" / "active" / "disabled"
	SubnetRouterNodeID int64  // denorm: 0 if no router provisioned (v0.16.7+)
}

const (
	ThemeLinear = "linear"
	ThemeVercel = "vercel"
	ThemeSentry = "sentry"
	ThemeNvidia = "nvidia"
)

func ThemeLabel(t string) string {
	switch t {
	case ThemeLinear:
		return "Linear"
	case ThemeVercel:
		return "Vercel"
	case ThemeSentry:
		return "Sentry"
	case ThemeNvidia:
		return "NVIDIA"
	default:
		return "Linear"
	}
}

// OpenForTest opens a fresh in-temp-dir SQLite DB with the full
// production migration chain applied. Returns a *sql.DB that
// the test's t.Cleanup will close.
//
// Exported so packages outside internal/db (e.g.
// internal/monitoring) can build a real schema for integration
// tests without having to re-implement the migration chain.
// The DB lives on disk in a TempDir (not :memory:) so that
// concurrent connections in the pool see the same data —
// ":memory:" is per-connection in Go's database/sql, which
// causes subtle "missing table" failures in tests that
// share a *sql.DB across goroutines.
func OpenForTest(t interface {
	Helper()
	TempDir() string
	Cleanup(func())
}) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	d, err := Open(filepath.Join(dir, "t.db"))
	if err != nil {
		panic("db.OpenForTest: " + err.Error())
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func IsValidTheme(t string) bool {
	switch t {
	case ThemeLinear, ThemeVercel, ThemeSentry, ThemeNvidia:
		return true
	}
	return false
}

func GetUserTheme(d *sql.DB, userID int64) string {
	var theme string
	err := d.QueryRow("SELECT COALESCE(theme, 'dark') FROM portal_users WHERE id = ?", userID).Scan(&theme)
	if err != nil || !IsValidTheme(theme) {
		return ThemeLinear
	}
	return theme
}

func SetUserTheme(d *sql.DB, userID int64, theme string) error {
	_, err := d.Exec("UPDATE portal_users SET theme = ? WHERE id = ?", theme, userID)
	return err
}

func Open(dataDir string) (*sql.DB, error) {
	dbPath := dataDir
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return nil, fmt.Errorf("mkdir: %w", err)
	}
	// 2026-08-03: v0.32.14 — CASCADE-LOCK FIX. The pre-fix
	// connection string had synchronous=FULL + busy_timeout=5000
	// (good for crash-safety, pre-v0.32.4 fix) AND
	// SetMaxOpenConns(1) (catastrophic for concurrency). On
	// the live VM with concurrent admin traffic (login
	// audit_log write + dashboard SELECT + ensureExitServers
	// DELETE + cron-driven HEAD requests all hitting the DB
	// in the same second), the single connection pooled
	// 100% busy and every request blocked for the full
	// busy_timeout. The fix:
	//   - SetMaxOpenConns(15)  : 15 concurrent connections
	//     instead of 1. WAL mode allows multiple readers AND
	//     one writer concurrently; with 15 we get real
	//     parallelism for the read-heavy workload.
	//   - SetMaxIdleConns(5)   : keep 5 idle for warm-pool,
	//     drop the rest (default would be 2 = MaxOpen, all
	//     kept forever).
	//   - SetConnMaxLifetime(5m): recycle every 5 min so
	//     long-lived connections don't accumulate state.
	//   - synchronous=NORMAL   : still safe (default in WAL),
	//     no fsync on every commit, much faster writes.
	//     v0.32.4 corruption was caused by disk-FULL, not
	//     by missing FULL sync, so this is safe.
	//   - busy_timeout=2000     : 2s instead of 5s — fail fast
	//     on contention rather than queue 5s.
	// The PRAGMAs are still re-applied in migrate() below
	// (belt-and-suspenders for the journal_mode + foreign_keys
	// settings that the connection string doesn't cover).
	conn, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=2000")
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	if err := conn.Ping(); err != nil {
		return nil, fmt.Errorf("ping: %w", err)
	}
	conn.SetMaxOpenConns(15)
	conn.SetMaxIdleConns(5)
	conn.SetConnMaxLifetime(5 * time.Minute)
	// 2026-07-09: refactor v0.6.0 — Open() now bootstraps schema. Migrations
	// are idempotent (CREATE TABLE IF NOT EXISTS + ALTER with duplicate-column
	// guards) so calling migrate() on every Open is safe and matches what
	// fresh deployments + unit tests expect.
	if err := migrate(conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	// 2026-07-28: v0.31.0 — register the backend in the driver
	// abstraction so BackendOf(conn) returns BackendSQLite. Without
	// this, code that dispatches on backend type (the future PG path
	// in queries.go) cannot tell what engine it's talking to. The
	// Open() signature still takes a file path, so this is purely
	// additive — no caller changes.
	registerBackend(conn, BackendSQLite)
	return conn, nil
}

func migrate(d *sql.DB) error {
	queries := []string{
		// 2026-08-03: v0.32.14 — WAL mode (good for read
		// concurrency) + synchronous=NORMAL (default in WAL,
		// no fsync per commit; the v0.32.4 corruption was
		// caused by disk-FULL not by missing FULL sync).
		// See the Open() comment for the full rationale.
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		// Checkpoint every 1000 pages instead of the default 1000
		// (SQLite's default is actually the same, but being explicit
		// makes the corruption-recovery story clearer: after a crash,
		// at most 1000 pages of WAL needs to be replayed, which
		// happens in milliseconds).
		"PRAGMA wal_autocheckpoint=1000",
		// The 2s busy timeout is the "wait this long for another
		// writer to release the lock" budget. Pre-v0.32.14 this
		// was 5s, which combined with SetMaxOpenConns(1) made
		// every concurrent request wait the full 5s on the single
		// connection. 2s + MaxOpenConns(15) is the new budget.
		"PRAGMA busy_timeout=2000",
		"PRAGMA foreign_keys=ON",
	}
	for _, q := range queries {
		if _, err := d.Exec(q); err != nil {
			return err
		}
	}
	// 2026-07-11: Этап 9 part 2 — fixed migration ordering. The
	// 2026-07-09 refactor moved V020 (CREATE device_rules + friends) to
	// AFTER V021/V022 (ALTER device_rules), which made the ALTERs no-ops
	// (the table didn't exist yet) and then V020 created device_rules
	// WITHOUT the action + device_ip columns. The bug was latent
	// because the VM DB was bootstrapped under the old order; only a
	// fresh DB exposes it (which the new db_helpers_part2_test.go
	// does). Correct order:
	//
	//   V025 — portal_users + friends (FK target for everything else)
	//   V020 — CREATE device_rules / exit_servers / acl_snapshots / exit_rule_logs
	//   V021 — ALTER device_rules ADD action + global_settings
	//   V022 — ALTER device_rules ADD device_ip
	//   V023 — CREATE personal_api_tokens (FK → portal_users, already exists)
	//   V024 — ALTER exit_servers (needs exit_servers, already exists)
	//   V026 — ALTER exit_servers ADD accept_routes (needs V024 done)
	//   V027 — CREATE telegram_alerts (independent)
	//   V028 — ALTER node_owner_map (tag columns)
	//   V029 — CREATE telegram_bindings (chat_id → portal_user)
	//   V030 — ALTER portal_users (default_device_node_id,
	//          default_exit_node_id) — Этап 11 part 2a
	//   V031 — CREATE telegram_login_tokens (login-by-key) +
	//          global_settings rows (telegram.strict_mode,
	//          telegram.login_token_ttl_seconds) — Этап 12
	//   V032 — CREATE telegram_rate_limit (shared rate-limit
	//          store, replaces in-memory map) — Этап 13
	//   V033 — ALTER telegram_bindings ADD lang (per-chat
	//          language preference for bot i18n) — Этап 14 v5
	//   V036 — CREATE exit_node_health + exit_node_state_changes
	//          (background exit-node health monitor) — v0.13.0
	migrateV025(d)
	if err := migrateV020(d); err != nil {
		return fmt.Errorf("migrate v0.20: %w", err)
	}
	if err := migrateV021(d); err != nil {
		return fmt.Errorf("migrate v0.21: %w", err)
	}
	if err := migrateV022(d); err != nil {
		return fmt.Errorf("migrate v0.22: %w", err)
	}
	if err := migrateV023(d); err != nil {
		return fmt.Errorf("migrate v0.23: %w", err)
	}
	if err := migrateV024(d); err != nil {
		return fmt.Errorf("migrate v0.24: %w", err)
	}
	if err := migrateV026(d); err != nil {
		return fmt.Errorf("migrate v0.26: %w", err)
	}
	if err := migrateV027(d); err != nil {
		return fmt.Errorf("migrate v0.27: %w", err)
	}
	if err := migrateV028(d); err != nil {
		return fmt.Errorf("migrate v0.28: %w", err)
	}
	if err := migrateV029(d); err != nil {
		return fmt.Errorf("migrate v0.29: %w", err)
	}
	if err := migrateV030(d); err != nil {
		return fmt.Errorf("migrate v0.30: %w", err)
	}
	if err := migrateV031(d); err != nil {
		return fmt.Errorf("migrate v0.31: %w", err)
	}
	if err := migrateV032(d); err != nil {
		return fmt.Errorf("migrate v0.32: %w", err)
	}
	if err := migrateV033(d); err != nil {
		return fmt.Errorf("migrate v0.33: %w", err)
	}
	if err := migrateV034(d); err != nil {
		return fmt.Errorf("migrate v0.34: %w", err)
	}
	if err := migrateV035(d); err != nil {
		return fmt.Errorf("migrate v0.35: %w", err)
	}
	if err := migrateV036(d); err != nil {
		return fmt.Errorf("migrate v0.36: %w", err)
	}
	// 2026-07-16: v0.15.5 — personal API token TTL. Adds
	// expires_at + auto_rotate columns to personal_api_tokens.
	if err := migrateV037(d); err != nil {
		return fmt.Errorf("migrate v0.37: %w", err)
	}
	// 2026-07-17: v0.16.0 — per-user subnets schema. Adds
	// the user_subnets table + 3 denormalized columns on
	// portal_users (subnet_cidr / subnet_status /
	// subnet_router_node_id). See migrations_v0.38.go for
	// the full rationale.
	if err := migrateV038(d); err != nil {
		return fmt.Errorf("migrate v0.38: %w", err)
	}
	// 2026-07-17: v0.17.1 — cross-user IP-level subnet
	// sharing. Adds user_subnet_shares (grantor, grantee)
	// with FKs CASCADE on portal_users.id. See
	// migrations_v0.39.go for the design rationale.
	if err := migrationV039(d); err != nil {
		return fmt.Errorf("migrate v0.39: %w", err)
	}
	// 2026-07-20: v0.20.0 — headscale-update-monitor.
	// Adds the headscale_releases table (one row per
	// unique tag the monitor has seen). See
	// migrations_v0.41.go for the full rationale.
	// (v0.40 was the v0.19.0 dns.extra_records
	// migration that was reverted — the slot is
	// reserved for the future v0.19.1 re-enable when
	// headscale 0.30+ lands.)
	if err := migrationV041(d); err != nil {
		return fmt.Errorf("migrate v0.41: %w", err)
	}
	// 2026-07-20: v0.21.0 — user-to-user subnet
	// bridge. Adds the invite_codes table (one row
	// per outstanding / consumed invite). See
	// migrations_v0.42.go for the full lifecycle.
	if err := migrationV042(d); err != nil {
		return fmt.Errorf("migrate v0.42: %w", err)
	}
	// 2026-07-20: v0.22.0 — mesh (shared network).
	// Adds the meshes + mesh_members tables. The
	// mesh is a named group of users whose personal
	// subnets are all mutually visible (N-way
	// bridge, generalizing the v0.17.1 one-shot
	// share). See migrations_v0.43.go for the full
	// rationale + ACL integration.
	if err := migrationV043(d); err != nil {
		return fmt.Errorf("migrate v0.43: %w", err)
	}
	// 2026-07-24: v0.28.0 — per-device ACL via
	// tag:dev-<user>-<device>. Adds user_name +
	// device_hostname columns to device_rules and
	// backfills them from portal_users.username +
	// node_owner_map.hostname (joined via device_ip).
	// See migrations_v0.44.go for the full design.
	if err := migrateV044(d); err != nil {
		return fmt.Errorf("migrate v0.44: %w", err)
	}
	// 2026-07-24: v0.28.1 — user_exit_node_prefs
	// table. One row per user; holds the user's
	// preferred exit-node tag (e.g. "tag:exit-relay-1")
	// that GenerateACLWithVia uses as `via` in
	// grants[]. See migrations_v0.45.go for the full
	// design + forward path to per-device prefs.
	if err := migrateV045(d); err != nil {
		return fmt.Errorf("migrate v0.45: %w", err)
	}
	// v0.28.4: per-device preferred exit-node. Lets the
	// operator pin a specific device to a different
	// exit-node than the user's default (e.g. workstation-3 →
	// relay-3 while admin's default stays relay-1).
	// See migrations_v0.46.go for the design.
	if err := migrateV046(d); err != nil {
		return fmt.Errorf("migrate v0.46: %w", err)
	}
	// v0.28.5: explicit opt-in for the per-user /
	// per-device `via` constraint. The v0.28.3 design
	// unconditionally emitted `via=[]` for users with
	// a per-user pref, which broke older Tailscale
	// clients (Android in particular) that reject
	// policies with `via` they don't understand. The
	// fix is a per-row `via_enabled` flag — the
	// operator flips it on when they want strict
	// pinning, leaves it off (the new default) for
	// Android-friendly behavior. See migrations_v0.47.go.
	if err := migrateV047(d); err != nil {
		return fmt.Errorf("migrate v0.47: %w", err)
	}
	// 2026-07-29: per-device OS + device_type columns
	// on node_owner_map. See migrations_v0.48.go for
	// the design. The migration is idempotent (catches
	// the duplicate-column error on re-runs, same
	// pattern as v0.47).
	if err := migrateV048(d); err != nil {
		return fmt.Errorf("migrate v0.48: %w", err)
	}
	// 2026-08-03: v0.32.19 — migration integrity tracking.
	// Creates the applied_migrations table. See
	// migrations_v0.49.go for the rationale + soft/hard
	// mode semantics. Recording of older migrations
	// (V020-V048) is a v0.32.20 follow-up (requires
	// refactoring db.go to extract migration SQL bodies
	// into a map for sha256 computation).
	if err := ensureMigrationTrackingTable(d); err != nil {
		return fmt.Errorf("ensure migration tracking: %w", err)
	}
	if err := migrateV049(d); err != nil {
		return fmt.Errorf("migrate v0.49: %w", err)
	}
	return nil
}
