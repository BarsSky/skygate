package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"time"
	// 2026-07-22: v0.27.0 — driver abstraction. Both backends
	// are registered via blank import; the active one is chosen
	// at Open() time by DetectBackend(dsn).
	_ "github.com/jackc/pgx/v5/stdlib" // pgx (PostgreSQL)
	_ "github.com/mattn/go-sqlite3"     // sqlite3 (default)
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
	// 2026-07-22: v0.27.0 — driver abstraction. If dataDir looks
	// like a postgres DSN, use the pgx driver; otherwise treat it
	// as a SQLite file path (legacy behavior, unchanged).
	switch DetectBackend(dataDir) {
	case BackendPostgres:
		return openPostgres(dataDir)
	default:
		return openSQLite(dataDir)
	}
}

// openSQLite is the legacy SQLite-only Open path. Kept as a
// separate function so Open() reads as a clean dispatcher and
// unit tests can call it directly.
func openSQLite(dbPath string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return nil, fmt.Errorf("mkdir: %w", err)
	}
	conn, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	if err := conn.Ping(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	conn.SetMaxOpenConns(1)
	conn.SetMaxIdleConns(1)
	registerBackend(conn, BackendSQLite)
	// 2026-07-09: refactor v0.6.0 — Open() now bootstraps schema. Migrations
	// are idempotent (CREATE TABLE IF NOT EXISTS + ALTER with duplicate-column
	// guards) so calling migrate() on every Open is safe and matches what
	// fresh deployments + unit tests expect.
	if err := migrateSQLite(conn); err != nil {
		conn.Close()
		unregisterBackend(conn)
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return conn, nil
}

// openPostgres is the new v0.27.0 PostgreSQL path. The dsn is
// expected to be a full libpq-style URL, e.g.
//
//	postgres://skygate:secret@127.0.0.1:5432/skygate?sslmode=disable
//
// Pool sizing is the production-recommended default for a small
// Go HTTP service: 10 open / 5 idle. Tuning for specific workloads
// is left to the operator via the DSN's `pool_max_conns` parameter
// (pgx supports it natively).
func openPostgres(dsn string) (*sql.DB, error) {
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("pgx open: %w", err)
	}
	if err := conn.Ping(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("pgx ping: %w", err)
	}
	conn.SetMaxOpenConns(10)
	conn.SetMaxIdleConns(5)
	conn.SetConnMaxLifetime(30 * time.Minute)
	registerBackend(conn, BackendPostgres)
	if err := migratePostgres(conn); err != nil {
		conn.Close()
		unregisterBackend(conn)
		return nil, fmt.Errorf("pgx migrate: %w", err)
	}
	return conn, nil
}

// migrate is the dispatcher. It picks the right per-backend
// migration chain based on BackendOf(d). New callers should use
// this; legacy code that referenced the old "migrate()" name was
// updated to migrateSQLite() in v0.27.0.
func migrate(d *sql.DB) error {
	switch BackendOf(d) {
	case BackendPostgres:
		return migratePostgres(d)
	default:
		return migrateSQLite(d)
	}
}

// migratePostgres is the v0.27.0 PostgreSQL migration chain.
// Implemented in migrations_pg.go (auto-generated from the SQLite
// sources in migrations_v0.XX.go via port_migrations_pg.py). The
// chain is the same set of versions as the SQLite chain so a
// fresh PG DB ends up schema-equivalent to a fresh SQLite DB.
func migratePostgres(d *sql.DB) error {
	// Order MUST match the SQLite chain in migrateSQLite (above).
	// Schema dependencies:
	//   V025 (portal_users) is the FK target for everything else.
	//   V020 (device_rules + friends) depends on V025.
	//   V021/V022 ALTER device_rules (additive, depend on V020).
	//   V023 (personal_api_tokens) depends on V025.
	//   V024 ALTER exit_servers (depends on V020).
	//   V026 ALTER exit_servers ADD accept_routes (depends on V024).
	//   V027 (telegram_alerts) is independent.
	//   V028 ALTER node_owner_map (additive, depends on V025).
	//   V029 (telegram_bindings) depends on V025.
	//   V030 ALTER portal_users (depends on V025).
	//   V031 (telegram_login_tokens + global_settings rows)
	//     depends on V025 + V020 (global_settings created in V021).
	//   V032 (telegram_rate_limit) is independent.
	//   V033 ALTER telegram_bindings (depends on V029).
	//   V034-v0.43: each new table or column depends on V025
	//     (FK target) and is otherwise additive.
	//
	// The chain below runs all PG versions in dependency order.
	// V025 (portal_users) is FIRST because V020+ all FK to it.
	for _, fn := range []func(*sql.DB) error{
		migrateV025PG, // CREATE portal_users (FK target for everything)
		migrateV020PG, // CREATE device_rules + friends (FK to portal_users)
		migrateV021PG, // ALTER device_rules ADD action + global_settings
		migrateV022PG, // ALTER device_rules ADD device_ip
		migrateV023PG, // CREATE personal_api_tokens (FK to portal_users)
		migrateV024PG, // ALTER exit_servers (ssh_target, ssh_key_path)
		migrateV026PG, // ALTER exit_servers ADD accept_routes
		migrateV027PG, // CREATE telegram_alerts
		migrateV028PG, // ALTER node_owner_map (tag columns)
		migrateV029PG, // CREATE telegram_bindings (FK to portal_users)
		migrateV030PG, // ALTER portal_users (default_device_node_id, default_exit_node_id)
		migrateV031PG, // CREATE telegram_login_tokens + global_settings rows
		migrateV032PG, // CREATE telegram_rate_limit
		migrateV033PG, // ALTER telegram_bindings ADD lang
		migrateV034PG, // ALTER node_owner_map ADD hostname
		migrateV035PG, // ALTER portal_users (headscale_user_id, headscale_api_key_enc)
		migrateV036PG, // CREATE exit_node_health + exit_node_state_changes
		migrateV037PG, // ALTER personal_api_tokens (expires_at, auto_rotate)
		migrateV038PG, // CREATE user_subnets + denorm on portal_users
		migrateV039PG, // CREATE user_subnet_shares
		migrateV041PG, // CREATE headscale_releases
		migrateV042PG, // CREATE invite_codes
		migrateV043PG, // CREATE meshes + mesh_members
	} {
		if err := fn(d); err != nil {
			return fmt.Errorf("migrate v%sPG: %w", funcName(fn), err)
		}
	}
	return nil
}

// funcName returns the function name for error messages.
func funcName(fn func(*sql.DB) error) string {
	name := runtime.FuncForPC(reflect.ValueOf(fn).Pointer()).Name()
	// Strip package prefix (e.g. "skygate/internal/db.migrateV020PG"
	// -> "migrateV020PG").
	if idx := strings.LastIndex(name, "."); idx >= 0 {
		name = name[idx+1:]
	}
	return name
}

// migrateSQLite is the legacy SQLite migration chain. All
// existing migration files (migrations_v0.XX.go) are called
// from here. Unchanged from the pre-v0.27.0 implementation
// except for being wrapped in a function so the dispatcher
// in migrate() can route by backend.
func migrateSQLite(d *sql.DB) error {
	queries := []string{
		"PRAGMA journal_mode=WAL",
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
	return nil
}
