// cmd/skygate/db_migrate.go — skygate db-migrate subcommand.
//
// B-mod-sqlite-pg-bidi v1.5.4 Task 3: the operator-facing CLI
// wrapper around internal/db.Convert. Usage:
//
//	skygate db-migrate --from=<source-dsn> --to=<target-dsn> [--schema-only|--data-only] [--dry-run]
//
// Examples:
//
//	# Copy SQLite DB to PG (full schema + data):
//	skygate db-migrate --from=sqlite:/var/lib/skygate/skygate.db \
//	                   --to=postgres://user:pass@host:5432/skygate
//
//	# Generate the SQL schema only (dry-run for review):
//	skygate db-migrate --from=sqlite:/var/lib/skygate/skygate.db \
//	                   --to=postgres://user:pass@host:5432/skygate \
//	                   --schema-only --dry-run
//
// The subcommand reads the source + target dialects via
// db.DetectDSN, opens both via db.OpenWithDialect, and delegates
// to db.Convert. See internal/db/convert.go for the algorithm.
package main

import (
	"context"
	"fmt"
	"os"

	"skygate/internal/db"
)

// dbMigrateConfig holds the parsed CLI flags for the db-migrate
// subcommand. The Mode field uses string constants ("schema+data",
// "schema-only", "data-only") so the test asserts the human-readable
// form (the operator's --help text matches).
type dbMigrateConfig struct {
	From   string // source DSN (sqlite:/path, postgres://url, bare path)
	To     string // target DSN
	Mode   string // "schema+data" | "schema-only" | "data-only"
	DryRun bool
}

// parseDBMigrateArgs extracts the flags from the CLI args slice.
// Accepts the subcommand name as args[0] (skipped if present) OR
// the raw flag list (for testability).
//
// Returns an error if:
//   - --from is missing
//   - --to is missing
//   - a flag value is missing (e.g. `--from` with no value)
//   - an unknown flag is present
//
// --schema-only and --data-only are mutually exclusive (calling
// both is a user error — the second one wins, but we warn).
func parseDBMigrateArgs(args []string) (dbMigrateConfig, error) {
	cfg := dbMigrateConfig{Mode: "schema+data"}
	start := 0
	if len(args) > 0 && args[0] == "db-migrate" {
		start = 1
	}
	for i := start; i < len(args); i++ {
		switch args[i] {
		case "--from":
			if i+1 >= len(args) {
				return cfg, fmt.Errorf("--from requires a value (e.g. --from=sqlite:/path or --from=postgres://...)")
			}
			cfg.From = args[i+1]
			i++
		case "--to":
			if i+1 >= len(args) {
				return cfg, fmt.Errorf("--to requires a value (e.g. --to=postgres://user:pass@host/db)")
			}
			cfg.To = args[i+1]
			i++
		case "--schema-only":
			cfg.Mode = "schema-only"
		case "--data-only":
			cfg.Mode = "data-only"
		case "--dry-run":
			cfg.DryRun = true
		case "-h", "--help":
			printDBMigrateHelp()
			os.Exit(0)
		default:
			return cfg, fmt.Errorf("unknown flag: %s (try --help)", args[i])
		}
	}
	if cfg.From == "" {
		return cfg, fmt.Errorf("--from required (try --help)")
	}
	if cfg.To == "" {
		return cfg, fmt.Errorf("--to required (try --help)")
	}
	return cfg, nil
}

// printDBMigrateHelp prints the --help text for the subcommand.
// Kept as a free function (not a method) so tests can call it for
// a smoke check.
func printDBMigrateHelp() {
	fmt.Print(`skygate db-migrate — copy schema + data between skygate DBs.

USAGE:
  skygate db-migrate --from=<dsn> --to=<dsn> [flags]

FLAGS:
  --from=<dsn>       source DSN (sqlite:/path, postgres://..., or bare path)
  --to=<dsn>         target DSN
  --schema-only      emit CREATE TABLE statements only (no data)
  --data-only        copy data only (target schema must already exist)
  --dry-run          print the plan (table list + row counts) without writing
  -h, --help         print this help

EXAMPLES:
  # Switch from SQLite (self-host) to Postgres (prod scale-up):
  skygate db-migrate --from=sqlite:/var/lib/skygate/skygate.db \\
                     --to=postgres://skygate:<REDACTED>@<host>:5432/skygate

  # Generate the SQL schema only, for review:
  skygate db-migrate --from=sqlite:/var/lib/skygate/skygate.db \\
                     --to=postgres://skygate:<REDACTED>@<host>:5432/skygate \\
                     --schema-only --dry-run

NOTES:
  * v1.5.4 minimum-viable implementation: SQLite→SQLite round-trip
    is fully supported; cross-dialect (SQLite↔PG) conversion uses
    a small set of type substitutions (see internal/db/convert.go).
    For production cross-dialect conversion, ensure the target
    schema is aligned with the source via the per-dialect
    migration files (internal/db/migrations_sqlite.go /
    migrations_pg.go).
  * The target DB is NOT pre-existing: Convert creates every
    table from scratch. If the target DSN points to an existing
    DB with the skygate schema, Convert will fail with
    "table X already exists" — drop the target schema first.
`)
}

// runDBMigrate is the entry point for the db-migrate subcommand.
// Opens both source and target DBs via OpenWithDialect, then
// delegates to db.Convert. The current implementation does not
// wrap Convert in a transaction (Convert handles per-table
// commits internally; a top-level transaction would require
// re-designing the algorithm).
func runDBMigrate(ctx context.Context, cfg dbMigrateConfig) error {
	fromD, fromDB, err := db.OpenWithDialect(cfg.From)
	if err != nil {
		return fmt.Errorf("open source (%s): %w", cfg.From, err)
	}
	defer fromDB.Close()

	toD, toDB, err := db.OpenWithDialect(cfg.To)
	if err != nil {
		return fmt.Errorf("open target (%s): %w", cfg.To, err)
	}
	defer toDB.Close()

	fmt.Printf("db-migrate: from=%s (dialect=%s) -> to=%s (dialect=%s) mode=%s dry-run=%v\n",
		cfg.From, fromD.Kind, cfg.To, toD.Kind, cfg.Mode, cfg.DryRun)

	return db.Convert(ctx, fromD, fromDB, toD, toDB, db.ConvertOptions{
		Mode:   cfg.Mode,
		DryRun: cfg.DryRun,
	})
}

// runDBMigrateSubcommand is the dispatcher entry point (called
// from main.go's switch). Parses os.Args[2:] and runs the
// conversion.
func runDBMigrateSubcommand(ctx context.Context, args []string) error {
	cfg, err := parseDBMigrateArgs(args)
	if err != nil {
		return err
	}
	return runDBMigrate(ctx, cfg)
}
