// skygate migrate ... — B213 (v1.5.0+) in-DB schema
// migration CLI. Phase 1.7 of
// docs/internal/cluster-management.md.
//
// The pre-B213 landscape:
//
//   - `skygate migrate-only` (v0.33.1.21): a one-shot
//     flag for the self-update orchestrator. Opens
//     the DB (which runs all migrations as part of
//     Open()), then exits. Useful for the
//     "apply pending migrations BEFORE the swap"
//     pre-flight check, but doesn't show what's in
//     the bookkeeping table.
//
//   - `applied_migrations` table (B198, v0.49): the
//     bookkeeping table. Pre-B213 NOTHING populated
//     it from the live migration chain — only the
//     unit tests wrote to it. Operators had no
//     way to see which migrations had been applied.
//
// B213 closes both gaps:
//
//  1. `internal/db.MigratePostgres` now records
//     every successful migration in
//     applied_migrations (with version, name,
//     source file, applied_at). The first
//     `skygate migrate up` on an existing DB
//     back-fills all 47 entries.
//
//  2. New `skygate migrate` subcommand with `up`
//     and `status` verbs:
//
//       skygate migrate up        apply pending
//                                 migrations +
//                                 record them
//                                 (idempotent:
//                                 safe to re-run)
//       skygate migrate status    show what
//                                 migrations are
//                                 in the binary +
//                                 what's been
//                                 applied, with
//                                 the delta
//                                 ("pending" =
//                                 in binary but
//                                 not in DB;
//                                 "extra" = in DB
//                                 but not in
//                                 binary, i.e.
//                                 binary downgrade)
//       skygate migrate down <v>  STUB (B213.1):
//                                 the framework
//                                 doesn't have
//                                 Down() functions
//                                 yet (all
//                                 migrations are
//                                 forward-only), so
//                                 this returns a
//                                 clear "not
//                                 implemented" error
//                                 for now. Adding
//                                 down() to all 47
//                                 migrations is a
//                                 future B-block.
//
// The pre-B213 `skygate migrate-only` flag is kept
// as a one-shot alias for `skygate migrate up` (the
// self-update orchestrator's docker run command
// references it). New code should use the new
// subcommand; the old flag is for backward compat.
//
// 2026-09-02: B213 (v1.5.0+).

package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"text/tabwriter"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"skygate/internal/config"
	"skygate/internal/db"
)

// runMigrateSubcommand is the dispatcher for
// `skygate migrate <verb>`. Verb defaults to "up"
// (the canonical "apply pending" action).
//
// Verb-vs-flag disambiguation: same as B211 / B212 —
// if args[0] starts with "-", treat it as a flag
// for the default verb.
func runMigrateSubcommand(args []string) error {
	verb := "up"
	if len(args) > 0 {
		switch args[0] {
		case "--help", "-h", "help":
			printMigrateUsage()
			return nil
		}
		if !startsWithDash(args[0]) {
			verb = args[0]
		}
	}
	switch verb {
	case "up", "":
		return runMigrateUp(args)
	case "down":
		return runMigrateDown(args)
	case "status":
		return runMigrateStatus(args)
	default:
		return fmt.Errorf("migrate: unknown verb %q (up / down / status)", verb)
	}
}

// startsWithDash is a tiny helper (avoids importing
// strings just for HasPrefix).
func startsWithDash(s string) bool {
	return len(s) > 0 && s[0] == '-'
}

// runMigrateUp applies any pending migrations. The
// underlying db.MigratePostgres is idempotent
// (CREATE TABLE IF NOT EXISTS etc.) AND now records
// each successful application in applied_migrations
// (B213's framework change). Safe to re-run.
//
// Output:
//   - line 1: "applied=N pending=0" (scriptable summary)
//   - subsequent lines: per-migration log (human-readable)
//
// Implementation note: we deliberately do NOT use
// db.OpenDSN (which auto-runs MigratePostgres on every
// open). That would defeat the "applied=N" count —
// the auto-migration would populate applied_migrations
// BEFORE we took the "before" snapshot, so the delta
// would always be 0. Instead we open a bare pgx
// connection, snapshot, then explicitly call
// MigratePostgres, then snapshot again.
func runMigrateUp(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("migrate up: config load: %w", err)
	}
	// Bare pgx open — NO migration auto-run. This is
	// the same as db.OpenDSN minus the MigratePostgres
	// call (which we want to count, not just execute).
	d, err := openBareDB(cfg.DBDSN)
	if err != nil {
		return fmt.Errorf("migrate up: open db: %w", err)
	}
	defer d.Close()

	// Take a snapshot of applied_migrations BEFORE
	// the run, so we can compute the "applied this run"
	// delta for the scriptable summary line.
	before, err := db.AllMigrationsForAudit(d)
	if err != nil {
		return fmt.Errorf("migrate up: snapshot before: %w", err)
	}
	beforeSet := make(map[int]bool, len(before))
	for _, m := range before {
		beforeSet[m.Version] = true
	}

	t0 := time.Now()
	if err := db.MigratePostgres(d); err != nil {
		fmt.Fprintf(os.Stderr, "migrate up: FAILED: %v\n", err)
		return err
	}
	dur := time.Since(t0)

	after, err := db.AllMigrationsForAudit(d)
	if err != nil {
		return fmt.Errorf("migrate up: snapshot after: %w", err)
	}
	appliedThisRun := 0
	for _, m := range after {
		if !beforeSet[m.Version] {
			appliedThisRun++
		}
	}
	// Scriptable summary on stdout (line 1) — for the
	// orchestrator's pre-flight check.
	fmt.Printf("applied=%d duration_ms=%d\n", appliedThisRun, dur.Milliseconds())
	fmt.Fprintf(os.Stderr, "migrate up: %d migrations applied (%.1fs)\n", appliedThisRun, dur.Seconds())
	return nil
}

// openBareDB opens a pgx connection to the given DSN
// WITHOUT running the migration chain. Used by
// runMigrateUp so the "applied=N" delta reflects
// the rows that MigratePostgres actually wrote (not
// the rows that auto-migration wrote during Open).
func openBareDB(dsn string) (*sql.DB, error) {
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	if err := conn.Ping(); err != nil {
		conn.Close()
		return nil, err
	}
	conn.SetMaxOpenConns(2)
	conn.SetMaxIdleConns(1)
	return conn, nil
}

// runMigrateDown is a STUB. The pre-B213 framework has
// no Down() functions (all migrations are forward-
// only). B213 returns a clear "not implemented" error
// for the verb so the operator gets a useful message
// instead of a silent no-op.
//
// Future B-block (B213.1 / Phase 1.7.1): add Down()
// functions for each migration, then implement this.
func runMigrateDown(args []string) error {
	fs := flag.NewFlagSet("migrate down", flag.ContinueOnError)
	_ = fs.String("target", "", "target version (e.g. 65 for v0.65)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return errors.New("migrate down: not implemented (Phase 1.7.1 — needs Down() functions for each migration; all 47 migrations are currently forward-only)")
}

// runMigrateStatus shows the current state of
// applied_migrations vs. the binary's known migrations.
//
// Output (text mode, the default):
//
//	version  name                                 applied_at           source
//	20       v0.20 (B0): exit_servers              2026-09-01 12:34:56  migrations_pg.go
//	...
//	66       v0.66 (B211): cluster_node UNIQUE...  2026-09-02 13:24:20  migrations_v0_66_b211.go
//
//	summary: applied=47 pending=0 extra=0
//
// "pending" = migration in the binary but NOT in the
// applied table. The next `skygate migrate up` would
// run these (idempotently).
//
// "extra" = migration in the applied table but NOT in
// the binary. This is a "binary downgrade" signature:
// the operator downgraded skygate to an older version
// that doesn't have the latest migrations.
//
// `--json` switches to machine-readable JSON.
func runMigrateStatus(args []string) error {
	fs := flag.NewFlagSet("migrate status", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("migrate status: config load: %w", err)
	}
	d, err := db.OpenDSN(cfg.DBDSN)
	if err != nil {
		return fmt.Errorf("migrate status: open db: %w", err)
	}
	defer d.Close()

	knownMigrations := db.PGMigrations()
	applied, err := db.AllMigrationsForAudit(d)
	if err != nil {
		return fmt.Errorf("migrate status: read applied: %w", err)
	}
	appliedByVersion := make(map[int]db.MigrationRecord, len(applied))
	for _, m := range applied {
		appliedByVersion[m.Version] = m
	}

	// Build the merged view: every known migration +
	// a flag for whether it's in the applied table.
	// Sort by Version ASC (the canonical order).
	type statusRow struct {
		Version    int
		Name       string
		SourceFile string
		Applied    bool
		AppliedAt  string
		FirstSeen  string
		Pending    bool
		Extra      bool // in applied table but not in binary
	}
	knownVersions := make(map[int]bool, len(knownMigrations))
	rows := make([]statusRow, 0, len(knownMigrations))
	for _, m := range knownMigrations {
		knownVersions[m.Version] = true
		a, ok := appliedByVersion[m.Version]
		row := statusRow{
			Version:    m.Version,
			Name:       m.Name,
			SourceFile: m.SourceFile,
			Applied:    ok,
			Pending:    !ok,
		}
		if ok {
			row.AppliedAt = time.Unix(a.AppliedAt, 0).UTC().Format(time.RFC3339)
			row.FirstSeen = a.FirstSeen
		}
		rows = append(rows, row)
	}
	// Plus any "extra" applied versions not in the
	// binary. Sort by Version ASC.
	extras := []statusRow{}
	for _, m := range applied {
		if !knownVersions[m.Version] {
			extras = append(extras, statusRow{
				Version:    m.Version,
				Name:       "<not in current binary>",
				SourceFile: m.SourceFile,
				Applied:    true,
				AppliedAt:  time.Unix(m.AppliedAt, 0).UTC().Format(time.RFC3339),
				FirstSeen:  m.FirstSeen,
				Extra:      true,
			})
		}
	}
	sort.Slice(extras, func(i, j int) bool { return extras[i].Version < extras[j].Version })
	rows = append(rows, extras...)
	sort.Slice(rows, func(i, j int) bool { return rows[i].Version < rows[j].Version })

	// Summary counts.
	appliedCount := 0
	pendingCount := 0
	extraCount := 0
	for _, r := range rows {
		if r.Extra {
			extraCount++
		} else if r.Applied {
			appliedCount++
		} else {
			pendingCount++
		}
	}

	if *asJSON {
		out := struct {
			Rows        []statusRow `json:"rows"`
			Applied     int         `json:"applied"`
			Pending     int         `json:"pending"`
			Extra       int         `json:"extra"`
			BinaryKnown int         `json:"binary_known"`
		}{
			Rows:        rows,
			Applied:     appliedCount,
			Pending:     pendingCount,
			Extra:       extraCount,
			BinaryKnown: len(knownMigrations),
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
		return nil
	}

	// Text mode.
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "VERSION\tNAME\tAPPLIED\tAPPLIED_AT\tSOURCE")
	for _, r := range rows {
		appliedStr := "yes"
		if r.Extra {
			appliedStr = "yes (extra — not in current binary)"
		} else if r.Pending {
			appliedStr = "PENDING"
		}
		fmt.Fprintf(tw, "v0.%d\t%s\t%s\t%s\t%s\n",
			r.Version, r.Name, appliedStr, r.AppliedAt, r.SourceFile)
	}
	_ = tw.Flush()
	fmt.Printf("\nsummary: applied=%d pending=%d extra=%d (binary knows %d migrations)\n",
		appliedCount, pendingCount, extraCount, len(knownMigrations))
	return nil
}

// printMigrateUsage prints the top-level skygate migrate help.
func printMigrateUsage() {
	fmt.Println("skygate migrate <verb> [args]")
	fmt.Println("")
	fmt.Println("  up        apply pending migrations + record them (idempotent — safe to re-run)")
	fmt.Println("  down      STUB: not yet implemented (Phase 1.7.1 — needs Down() functions for each migration)")
	fmt.Println("  status    show what migrations are in the binary + what's been applied")
	fmt.Println("                  (text or --json; reports 'pending' = in binary but not in DB,")
	fmt.Println("                  'extra' = in DB but not in binary = binary downgrade)")
	fmt.Println("")
	fmt.Println("Flags (status):")
	fmt.Println("  --json    emit JSON instead of text")
	fmt.Println("")
	fmt.Println("Pre-B213 alias: `skygate migrate-only` is kept as a one-shot alias for `migrate up`")
	fmt.Println("(used by the self-update orchestrator's pre-flight docker run).")
}
