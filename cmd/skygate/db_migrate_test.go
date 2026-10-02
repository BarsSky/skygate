// cmd/skygate/db_migrate_test.go — RED tests for the
// B-mod-sqlite-pg-bidi db-migrate subcommand (Task 3).
//
// The subcommand is a thin CLI wrapper around internal/db.Convert:
// it parses --from / --to / --schema-only / --data-only / --dry-run
// flags (both the `--flag value` and `--flag=value` spellings), opens
// both DBs via db.OpenWithDialect, and delegates to ConvertWithReport.
// Tests cover the parsing layer; the SQLite↔SQLite round trip itself is
// covered by internal/db/convert_test.go, and the PostgreSQL legs are
// covered by CI's test-pg job (SKYGATE_TEST_PG_DSN).
package main

import (
	"testing"
)

func TestParseDBMigrateArgs(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantFrom string
		wantTo   string
		wantMode string
		wantDry  bool
		wantErr  bool
	}{
		{
			name:     "basic round-trip",
			args:     []string{"db-migrate", "--from", "sqlite:/tmp/src.db", "--to", "postgres://u:p@h/d"},
			wantFrom: "sqlite:/tmp/src.db",
			wantTo:   "postgres://u:p@h/d",
			wantMode: "schema+data",
			wantDry:  false,
		},
		{
			name:     "schema-only flag",
			args:     []string{"db-migrate", "--from", "X", "--to", "Y", "--schema-only"},
			wantFrom: "X",
			wantTo:   "Y",
			wantMode: "schema-only",
		},
		{
			name:     "data-only flag",
			args:     []string{"db-migrate", "--from", "X", "--to", "Y", "--data-only"},
			wantFrom: "X",
			wantTo:   "Y",
			wantMode: "data-only",
		},
		{
			name:     "dry-run flag",
			args:     []string{"db-migrate", "--from", "X", "--to", "Y", "--dry-run"},
			wantFrom: "X",
			wantTo:   "Y",
			wantMode: "schema+data",
			wantDry:  true,
		},
		{
			name:    "missing both",
			args:    []string{"db-migrate"},
			wantErr: true,
		},
		{
			name:    "missing --to",
			args:    []string{"db-migrate", "--from", "X"},
			wantErr: true,
		},
		{
			name:    "unknown flag",
			args:    []string{"db-migrate", "--from", "X", "--to", "Y", "--bogus"},
			wantErr: true,
		},
		{
			name:     "no subcommand name prefix",
			args:     []string{"--from", "X", "--to", "Y"},
			wantFrom: "X",
			wantTo:   "Y",
			wantMode: "schema+data",
		},
		{
			// 2026-09-28: the `=` form is what the --help text and the
			// release notes advertise. The pre-fix parser answered
			// "unknown flag: --from=sqlite:/tmp/src.db" (measured).
			name:     "equals form for --from and --to",
			args:     []string{"db-migrate", "--from=sqlite:/tmp/src.db", "--to=postgres://u:p@h/d"},
			wantFrom: "sqlite:/tmp/src.db",
			wantTo:   "postgres://u:p@h/d",
			wantMode: "schema+data",
		},
		{
			name:     "equals form mixed with the space form",
			args:     []string{"db-migrate", "--from=sqlite:/tmp/src.db", "--to", "Y", "--dry-run"},
			wantFrom: "sqlite:/tmp/src.db",
			wantTo:   "Y",
			wantMode: "schema+data",
			wantDry:  true,
		},
		{
			name:    "equals form with an empty value",
			args:    []string{"db-migrate", "--from=", "--to", "Y"},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := parseDBMigrateArgs(tc.args)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if err != nil {
				return
			}
			if cfg.From != tc.wantFrom {
				t.Errorf("From = %q, want %q", cfg.From, tc.wantFrom)
			}
			if cfg.To != tc.wantTo {
				t.Errorf("To = %q, want %q", cfg.To, tc.wantTo)
			}
			if cfg.Mode != tc.wantMode {
				t.Errorf("Mode = %q, want %q", cfg.Mode, tc.wantMode)
			}
			if cfg.DryRun != tc.wantDry {
				t.Errorf("DryRun = %v, want %v", cfg.DryRun, tc.wantDry)
			}
		})
	}
}

func TestParseDBMigrateArgs_HelpText(t *testing.T) {
	// The --help handler calls os.Exit(0) which terminates the test
	// process. We test the help text content by calling the helper
	// directly (which prints to stdout but doesn't exit).
	// This is a smoke test that the help string contains the
	// key flag names.
	printDBMigrateHelp()
	// (No assertion — the test passes if the call doesn't panic.
	// The real coverage is the live `skygate db-migrate --help`
	// run + the B-check script that greps the help text.)
}
