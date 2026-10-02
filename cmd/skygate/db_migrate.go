// cmd/skygate/db_migrate.go — skygate db-migrate subcommand.
//
// B-mod-sqlite-pg-bidi: the operator-facing CLI wrapper around
// internal/db.Convert. Usage:
//
//	skygate db-migrate --from <source-dsn> --to <target-dsn> [--schema-only|--data-only] [--dry-run]
//
// Both spellings of a flag value are accepted (`--from <dsn>` and
// `--from=<dsn>`); the `=` form is what the help text and the release
// notes advertise, and until 2026-09-28 it was rejected with
// "unknown flag: --from=…" — measured, not inferred.
//
// Examples:
//
//	# Move a self-hosted SQLite install onto PostgreSQL:
//	skygate db-migrate --from sqlite:/var/lib/skygate/skygate.db \
//	                   --to postgres://user:pass@host:5432/skygate
//
//	# Move PostgreSQL back to SQLite (scale down to a self-host):
//	skygate db-migrate --from postgres://user:pass@host:5432/skygate \
//	                   --to sqlite:/var/lib/skygate/skygate.db
//
// The subcommand reads the source + target dialects via db.DetectDSN,
// opens both via db.OpenWithDialect, and delegates to
// db.ConvertWithReport. See internal/db/convert.go for the algorithm:
// the target schema is created by the TARGET's own migration chain,
// the tables are filled parents-first from the target's FOREIGN KEY
// metadata, the copy runs in one transaction, and the per-table row
// counts are verified afterwards.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

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

// splitFlagValue reports whether arg is the "--flag=value" form of
// flag and, if so, returns the value.
func splitFlagValue(flag, arg string) (string, bool) {
	if strings.HasPrefix(arg, flag+"=") {
		return strings.TrimPrefix(arg, flag+"="), true
	}
	return "", false
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
func parseDBMigrateArgs(args []string) (dbMigrateConfig, error) {
	cfg := dbMigrateConfig{Mode: "schema+data"}
	start := 0
	if len(args) > 0 && args[0] == "db-migrate" {
		start = 1
	}
	for i := start; i < len(args); i++ {
		arg := args[i]

		// `--from=<dsn>` / `--to=<dsn>` first: the help text and the
		// release notes document the `=` form, and the pre-2026-09-28
		// parser rejected it with "unknown flag".
		if v, ok := splitFlagValue("--from", arg); ok {
			if v == "" {
				return cfg, fmt.Errorf("--from= requires a value (e.g. --from=sqlite:/path)")
			}
			cfg.From = v
			continue
		}
		if v, ok := splitFlagValue("--to", arg); ok {
			if v == "" {
				return cfg, fmt.Errorf("--to= requires a value (e.g. --to=postgres://user:pass@host/db)")
			}
			cfg.To = v
			continue
		}

		switch arg {
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
			return cfg, fmt.Errorf("unknown flag: %s (try --help)", arg)
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
  skygate db-migrate --from <dsn> --to <dsn> [flags]
  skygate db-migrate --from=<dsn> --to=<dsn> [flags]

FLAGS:
  --from=<dsn>       source DSN (sqlite:/path, postgres://..., or bare path)
  --to=<dsn>         target DSN
  --schema-only      create the target schema, copy no rows
  --data-only        copy rows only (the target schema must already exist)
  --dry-run          print the plan (table list + source row counts) without writing
  -h, --help         print this help

BOTH DIRECTIONS ARE SUPPORTED:
  SQLite -> PostgreSQL   scale a self-hosted install up
  PostgreSQL -> SQLite   move a production install back to a single file

WHAT THE CONVERSION DOES:
  * The target schema is created by the TARGET's own migration chain,
    so it gets the columns, indexes, triggers and partial UNIQUE
    indexes that backend actually needs — it is not translated from
    the source's DDL.
  * Tables are filled parents-before-children, ordered from the
    target's own FOREIGN KEY metadata.
  * Every row is copied inside ONE transaction; a failure rolls the
    whole copy back, so a retry is always safe.
  * Each value is coerced to the TARGET column's declared type
    (SQLite stores timestamps as INTEGER unix seconds, PostgreSQL as
    timestamptz, and each side's booleans differ).
  * Per-table row counts are verified afterwards, and the PostgreSQL
    identity sequences are advanced past the copied ids.

NOTES:
  * The target must be EMPTY: the schema is created from scratch, and
    a pre-existing skygate schema makes the run fail with
    "table X already exists". Use --data-only to merge into a target
    whose schema you already prepared.
  * applied_migrations is never copied — it belongs to the target's
    own migration run.
  * After a successful conversion, point skygate at the target
    (SKYGATE_DB) and restart it.
`)
}

// runDBMigrate is the entry point for the db-migrate subcommand.
// Opens both source and target DBs via OpenWithDialect, then
// delegates to db.ConvertWithReport and prints the report.
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

	rep, err := db.ConvertWithReport(ctx, fromD, fromDB, toD, toDB, db.ConvertOptions{
		Mode:   cfg.Mode,
		DryRun: cfg.DryRun,
	})
	if rep != nil {
		printConvertReport(rep)
	}
	if err != nil {
		return err
	}
	if cfg.DryRun {
		fmt.Println("dry run: nothing was written")
		return nil
	}
	fmt.Printf("db-migrate: done — %d row(s) copied, verified=%v\n", rep.CopiedRows, rep.Verified)
	return nil
}

// printConvertReport renders the per-table result so the operator (and
// the CI log) can see what a conversion actually did, not just that it
// exited 0.
func printConvertReport(rep *db.ConvertReport) {
	fmt.Printf("  %-32s %10s %10s %10s  %s\n", "table", "source", "target", "copied", "note")
	for _, t := range rep.Tables {
		note := t.Reason
		if note == "" && len(t.SkippedColumns) > 0 {
			note = "dropped column(s): " + strings.Join(t.SkippedColumns, ",")
		}
		fmt.Printf("  %-32s %10d %10d %10d  %s\n", t.Table, t.SourceRows, t.TargetRows, t.Copied, note)
	}
	for _, w := range rep.Warnings {
		fmt.Printf("  warning: %s\n", w)
	}
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
