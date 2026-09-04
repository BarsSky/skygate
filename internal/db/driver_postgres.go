// driver_postgres.go — PostgreSQL backend (v1.3.0+)
//
// As of v1.3.0, skygate is PG-only. There is no build tag — the
// pgx driver is always registered via the `_ "github.com/jackc/
// pgx/v5/stdlib"` import below, and MigratePostgres is the
// canonical migration entry point.
//
// The PG migration functions live in migrations_pg.go (also no
// build tag) so the helper is reachable in unit tests.

package db

import (
	"database/sql"
	"fmt"
	"log"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// OpenPostgres opens a PostgreSQL connection. The dsn is the
// standard libpq URL form:
//
//	postgres://skygate:<password>@<host>:5432/skygate?sslmode=disable
//
// Pool sizing follows the small-Go-HTTP-service defaults: 10 open /
// 5 idle. Operators tune via the DSN's `pool_max_conns` parameter
// (pgx-native).
//
// 2026-08-04: v0.33.1 — auto-migrate on open. Pre-fix, OpenPostgres
// returned a bare *sql.DB without running MigratePostgres, so the
// v0.33.0 tables (headscale_acl_rules + system_tests_runs) were
// only created when the operator manually ran
// `cmd/apply_pg_migrations`. On the live VM the cutover happened
// before v0.33.0 was deployed, so the manual apply picked up
// everything up to v0.49 (no v0.50, no v0.51) — and the
// /admin/headscale/acl page returned http.StatusInternalServerError "relation
// headscale_acl_rules does not exist" until the operator
// triggered a deploy. Calling MigratePostgres() here makes
// the PG path symmetric with the SQLite Open() → migrate(conn)
// path: every container start re-applies the idempotent
// migration chain. New operators don't have to know about
// the standalone `apply_pg_migrations` tool.
func OpenPostgres(dsn string) (*sql.DB, error) {
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
	if err := MigratePostgres(conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("pgx migrate: %w", err)
	}
	registerBackend(conn, BackendPostgres)
	return conn, nil
}

// MigrationEntry is one row in the canonical migration
// list (see pgMigrations below). B213 added the Name +
// SourceFile + Version fields so the B213 CLI can print
// `skygate migrate status` (which would otherwise have
// no metadata to show — the pre-B213 function list
// didn't track version/name/source).
//
// Run is the function that applies the DDL. It MUST be
// idempotent (CREATE TABLE IF NOT EXISTS, etc.) so the
// `skygate migrate up` re-run is safe.
//
// Version is the integer in the applied_migrations.version
// column. It's NOT the same as the function name suffix
// (migrateV066PG → 66) — they're the same number today
// but the version is the canonical key for the bookkeeping
// table, and the function name is just a Go identifier.
//
// Name is a human-readable label (e.g. "v0.66 B211:
// cluster_node UNIQUE constraint") for `migrate status`.
//
// SourceFile is the relative path of the migration's Go
// source file. Used in the bookkeeping table for audit
// (so an operator can see WHICH Go file is responsible
// for the schema row).
type MigrationEntry struct {
	Version    int
	Name       string
	SourceFile string
	Run        func(*sql.DB) error
}

// pgMigrations is the ordered list of all PG migrations
// the current skygate binary knows about. B213 replaces
// the pre-B213 anonymous-function list with this struct
// list so the framework has metadata to record in
// applied_migrations + to show in `skygate migrate status`.
//
// Ordering: V025 runs first because V020+ have FOREIGN
// KEY → portal_users (which V025 creates). The rest are
// in numeric order with the historical V040 / V052 /
// V062 / V064 gaps preserved (those numbers were
// used for hotfixes that were rolled into other
// migrations).
var pgMigrations = []MigrationEntry{
	{25, "v0.25 (B0): portal_users + auth tables", "migrations_pg.go", migrateV025PG},
	{20, "v0.20 (B0): exit_servers", "migrations_pg.go", migrateV020PG},
	{21, "v0.21 (B0): device_rules action", "migrations_pg.go", migrateV021PG},
	{22, "v0.22 (B0): device_rules fix", "migrations_pg.go", migrateV022PG},
	{23, "v0.23 (B0): device_rules + portal_users", "migrations_pg.go", migrateV023PG},
	{24, "v0.24 (B0): exit_servers.ssh_target", "migrations_pg.go", migrateV024PG},
	{26, "v0.26 (B0): exit_servers.accept_routes", "migrations_pg.go", migrateV026PG},
	{27, "v0.27 (B0): telegram_alerts", "migrations_pg.go", migrateV027PG},
	{28, "v0.28 (B0): device_rules.parent_domain", "migrations_pg.go", migrateV028PG},
	{29, "v0.29 (B0): telegram_bindings", "migrations_pg.go", migrateV029PG},
	{30, "v0.30 (B0): portal_users.default_device_node_id", "migrations_pg.go", migrateV030PG},
	{31, "v0.31 (B0): telegram_login_tokens", "migrations_pg.go", migrateV031PG},
	{32, "v0.32 (B0): telegram_rate_limit", "migrations_pg.go", migrateV032PG},
	{33, "v0.33 (B0): telegram_bindings.lang", "migrations_pg.go", migrateV033PG},
	{34, "v0.34 (B0): node_owner_map.hostname", "migrations_pg.go", migrateV034PG},
	{35, "v0.35 (B0): portal_users.headscale_url", "migrations_pg.go", migrateV035PG},
	{36, "v0.36 (B0): exit-node health monitor", "migrations_pg.go", migrateV036PG},
	{37, "v0.37 (B0): personal_api_tokens.expires_at", "migrations_pg.go", migrateV037PG},
	{38, "v0.38 (B0): user_subnets", "migrations_pg.go", migrateV038PG},
	{39, "v0.39 (B0): user_subnets constraints", "migrations_pg.go", migrateV039PG},
	{41, "v0.41 (B0): ACL + backfill", "migrations_pg.go", migrateV041PG},
	{42, "v0.42 (B0): user_subnets schema", "migrations_pg.go", migrateV042PG},
	{43, "v0.43 (B0):", "migrations_pg.go", migrateV043PG},
	{44, "v0.44 (B0):", "migrations_pg.go", migrateV044PG},
	{45, "v0.45 (B0): device_rules cleanup", "migrations_pg.go", migrateV045PG},
	{46, "v0.46 (B0): exit_node_prefs backfill", "migrations_pg.go", migrateV046PG},
	{47, "v0.47 (B0): ACL backfill (skipped — see migrations_v0_47_test.go)", "migrations_pg.go", migrateV047PG},
	{48, "v0.48 (B0):", "migrations_pg.go", migrateV048PG},
	{49, "v0.49 (B0): applied_migrations tracking table", "migrations_pg.go", migrateV049PG},
	{50, "v0.50 (B0):", "migrations_pg.go", migrateV050PG},
	{51, "v0.51 (B0):", "migrations_pg.go", migrateV051PG},
	{53, "v0.53 (B0): exit_servers.ssh_port", "migrations_pg.go", migrateV053PG},
	{54, "v0.54 (B0):", "migrations_pg.go", migrateV054PG},
	{55, "v0.55 (B0):", "migrations_pg.go", migrateV055PG},
	{56, "v0.56 (B0):", "migrations_pg.go", migrateV056PG},
	{57, "v0.57 (B0):", "migrations_pg.go", migrateV057PG},
	{58, "v0.58 (B0):", "migrations_pg.go", migrateV058PG},
	{59, "v0.59 (B0):", "migrations_pg.go", migrateV059PG},
	{60, "v0.60 (B183): Telegram + audit_log", "migrations_v0_60_b183_test.go", migrateV060PG},
	{61, "v0.61 (B188): dev-tag owner_map", "migrations_v0_61_b188_test.go", migrateV061PG},
	{62, "v0.62 (B194):", "migrations_v0_62_b194.go", migrateV062PG},
	{63, "v0.63 (B194):", "migrations_v0_63_b194.go", migrateV063PG},
	{64, "v0.64 (B195): cluster_* tables (cluster / cluster_node / cluster_database / cluster_migration / cluster_invite / cluster_audit)", "migrations_v0_64_b195.go", migrateV064PG},
	{65, "v0.65 (B198): dbmigrate_run + dbmigrate_step", "migrations_v0_65_b198.go", migrateV065PG},
	{66, "v0.66 (B211): cluster_node UNIQUE (cluster_id, hostname)", "migrations_v0_66_b211.go", migrateV066PG},
	{67, "v0.67 (B221): audit_log.target_type + target_id (Phase 4.1 generic audit log)", "migrations_v0_67_b221.go", migrateV067PG},
	{68, "v0.68 (B232): repair device_rules_natural_key_uniq shape drift (B188.2 ON CONFLICT 6-col)", "migrations_v0_68_b232.go", migrateV068PG},
	{69, "v0.69 (B235.3): derp_health.name column for the B235 .Name short-label pill", "migrations_v0_69_b235_3.go", migrateV069PG},
}

// PGMigrations returns the list of migrations the current
// skygate binary knows about. The B213 `skygate migrate
// status` CLI uses this to compute the "pending"
// migrations (in the binary but not in the applied table)
// + the "extra" applied migrations (in the table but no
// longer in the binary — i.e. a binary downgrade).
func PGMigrations() []MigrationEntry {
	out := make([]MigrationEntry, len(pgMigrations))
	copy(out, pgMigrations)
	return out
}

// MigratePostgres runs every PG migration in version
// order, records each successful application in
// applied_migrations (idempotent — first-write-wins on
// the version primary key), and returns the first error
// encountered. The migration state is whatever the DB
// was left in (PG transactions make this atomic per-
// function, not per-call).
//
// B213: this used to be a bare function list with no
// bookkeeping. Now each entry has Version + Name +
// SourceFile metadata, and after the entry's Run()
// succeeds we call RecordMigrationApplied() so the
// applied_migrations table reflects reality. The pre-B213
// applied_migrations table was empty in the live agent
// (because nothing populated it); B213's first run on
// any existing DB back-fills all 47 entries.
//
// V025 runs first because V020+ have FOREIGN KEY →
// portal_users (which V025 creates).
func MigratePostgres(d *sql.DB) error {
	// Set lock_timeout so concurrent migrators fail fast instead of
	// deadlocking. 5s is generous; live migrations finish in
	// well under a second on a fresh DB.
	if _, err := d.Exec(`SET lock_timeout = '5s'`); err != nil {
		return fmt.Errorf("SET lock_timeout: %w", err)
	}
	// Ensure the applied_migrations tracking table exists
	// (B198's bookkeeping) BEFORE we try to write to it.
	// ensureMigrationTrackingTable is idempotent.
	if err := ensureMigrationTrackingTable(d); err != nil {
		return fmt.Errorf("ensure applied_migrations table: %w", err)
	}
	for _, e := range pgMigrations {
		if err := e.Run(d); err != nil {
			return fmt.Errorf("migration v%d (%s): %w", e.Version, e.Name, err)
		}
		// Record the successful application. The sha256
		// of the migration body is NOT available here
		// (the function only sees the *sql.DB, not its
		// own source) — we leave it empty in the
		// bookkeeping row. The pre-B213 schema's
		// checksum-verify path was for cosmetic-reformat
		// detection; B213 keeps the column for forward
		// compat but doesn't populate it.
		versionStr := fmt.Sprintf("v0.%d", e.Version)
		if err := RecordMigrationApplied(d, e.Version, "", e.SourceFile, versionStr); err != nil {
			// A duplicate INSERT is fine (first-write-
			// wins via ON CONFLICT). Other errors
			// (DB down, FK violation) are NOT fine —
			// the migration already ran, so the schema
			// is correct, but the bookkeeping is out
			// of sync. Log + continue (the operator
			// can manually backfill later). We don't
			// return the error because returning here
			// would re-run the same migration on next
			// boot (the bookkeeping would say "not
			// applied yet" forever).
			log.Printf("migrate: WARNING: record v%d (%s) failed: %v (continuing — schema is correct, bookkeeping is out of sync)", e.Version, e.Name, err)
		}
	}
	return nil
}
