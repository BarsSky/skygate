// Package dbmigrate/steps — Flip is the moment of truth:
// it updates cluster_database.current_dsn to the target DSN
// (so D8 picks it up) and writes the .env override on the
// skygate host (so a restart picks it up immediately).
//
// Per D8 the cluster_database wins on conflict; the Phase 3.1
// watchdog (skygate-watchdog) will hot-reload pgxpool when it
// detects the change. Until then, the operator must restart
// the skygate container.

package steps

import (
	"context"
	"database/sql"
	"fmt"
	"os"

	"skygate/internal/db"
	"skygate/internal/dbmigrate"
)

func init() {
	dbmigrate.RegisterStep(flipStep{})
}

type flipStep struct{}

func (flipStep) Name() string        { return "flip" }
func (flipStep) Description() string { return "Update cluster_database + .env to point to target" }
func (flipStep) Ordinal() int       { return 5 }
func (flipStep) IsOptional() bool   { return false }
func (flipStep) DependsOn() []string { return []string{"verify"} }

func (flipStep) Run(ctx context.Context, mc *dbmigrate.MigrationContext) error {
	if mc.DB == nil {
		return fmt.Errorf("MigrationContext.DB is nil; framework bug")
	}
	// The DB interface in MigrationContext is satisfied
	// by *sql.DB; we cast to call SetClusterDatabase which
	// takes a *sql.DB. The interface is to keep the
	// framework decoupled from the concrete type.
	conn, ok := mc.DB.(*sql.DB)
	if !ok {
		return fmt.Errorf("MC.DB is %T, not *sql.DB", mc.DB)
	}
	// 1. Update cluster_database with the new DSN. This is
	// the source of truth per D8.
	cd := &db.ClusterDatabase{
		ID:             "skygate-staging",
		ClusterID:      "skygate-staging",
		PrimaryNodeID:  "", // operator updates separately
		DSNTemplate:    buildDSNTemplate(mc),
		DBName:         mc.TargetDBName,
		Username:       mc.TargetUsername,
		SSLMode:        mc.TargetSSLMode,
		CurrentDSN:     redactDSNForStorage(mc.TargetDSN), // storage can keep redacted
		UpdatedBy:      mc.Operator,
	}
	if err := db.SetClusterDatabase(conn, cd); err != nil {
		return fmt.Errorf("cluster_database update: %w", err)
	}
	// 2. Update the .env file so the live process picks up
	// the new DSN on next restart. We use a temp-write +
	// rename so the file is never half-written. The
	// container restart is a separate action — see the
	// .env_warning i18n key.
	if err := updateEnvFile(mc); err != nil {
		// Non-fatal: the cluster_database already has the
		// truth; the .env is a fallback. Log via the
		// migration logs (the framework's StepLog) so the
		// operator sees the .env update failure.
		// (Phase 1.4: we just return the error so the
		// operator can re-run after fixing.)
		return fmt.Errorf(".env update: %w", err)
	}
	// 3. Append a cluster.db.migrate audit row.
	_ = db.AppendAuditLog(conn, 0, mc.Operator, "cluster.db.migrate",
		fmt.Sprintf("source=redacted target=%s run_id=%d",
			redactDSNForStorage(mc.TargetDSN), mc.RunID))
	return nil
}

func (flipStep) Rollback(ctx context.Context, mc *dbmigrate.MigrationContext) error {
	if mc.DB == nil {
		return nil
	}
	conn, ok := mc.DB.(*sql.DB)
	if !ok {
		return nil
	}
	// Rollback reverts the cluster_database row to the
	// pre-migration state. We re-read the source DSN from
	// the mc.SourceDSN (the framework stashed it at start).
	cd := &db.ClusterDatabase{
		ID:         "skygate-staging",
		ClusterID:  "skygate-staging",
		DSNTemplate: redactDSNForStorage(mc.SourceDSN),
		DBName:     extractDBName(mc.SourceDSN),
		Username:   extractUser(mc.SourceDSN),
		SSLMode:    extractSSLMode(mc.SourceDSN),
		CurrentDSN: redactDSNForStorage(mc.SourceDSN),
		UpdatedBy:  mc.Operator + " (rollback)",
	}
	return db.SetClusterDatabase(conn, cd)
}

// buildDSNTemplate composes the passwordless DSN template
// from MigrationContext fields. The %s is the password
// placeholder that the watchdog (Phase 3.1) substitutes at
// read time from .env.
func buildDSNTemplate(mc *dbmigrate.MigrationContext) string {
	port := mc.TargetPort
	if port == "" {
		port = "5432"
	}
	sslmode := mc.TargetSSLMode
	if sslmode == "" {
		sslmode = "disable"
	}
	return fmt.Sprintf("postgres://%s:%%s@%s:%s/%s?sslmode=%s",
		mc.TargetUsername, mc.TargetHost, port, mc.TargetDBName, sslmode)
}

// updateEnvFile rewrites SKYGATE_DB_DSN in .env. We don't
// touch any other env vars. The file is atomically
// replaced (write to .env.tmp, rename).
func updateEnvFile(mc *dbmigrate.MigrationContext) error {
	envPath := "/home/skyadmin/skygate/.env" // Phase 1.4 hardcoded
	data, err := os.ReadFile(envPath)
	if err != nil {
		return err
	}
	newLine := "SKYGATE_DB_DSN=" + mc.TargetDSN
	out := []byte{}
	replaced := false
	// Iterate line by line (split on \n).
	start := 0
	for i := 0; i < len(data); i++ {
		if data[i] != '\n' {
			continue
		}
		line := data[start:i]
		if len(line) >= 14 && string(line[:14]) == "SKYGATE_DB_DSN" {
			out = append(out, []byte(newLine+"\n")...)
			replaced = true
		} else {
			out = append(out, line...)
			out = append(out, '\n')
		}
		start = i + 1
	}
	if !replaced {
		out = append(out, []byte(newLine+"\n")...)
	}
	tmp := envPath + ".tmp"
	if err := os.WriteFile(tmp, out, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, envPath)
}

// small extractors for the rollback path.
func extractDBName(dsn string) string   { return between(dsn, "/", "?") }
func extractUser(dsn string) string     { return between(dsn, "://", ":") }
func extractSSLMode(dsn string) string  { return between(dsn, "sslmode=", "&") }
func between(s, a, b string) string {
	i := indexOf(s, a)
	if i < 0 {
		return ""
	}
	s = s[i+len(a):]
	j := indexOf(s, b)
	if j < 0 {
		return s
	}
	return s[:j]
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// redactDSNForStorage returns a DSN with the password
// replaced by "***".
func redactDSNForStorage(dsn string) string {
	out := []byte(dsn)
	for i := 0; i < len(out); i++ {
		if out[i] == '@' {
			for j := i - 1; j >= 0; j-- {
				if out[j] == ':' {
					return string(out[:j+1]) + "***" + string(out[i:])
				}
				if out[j] == '/' {
					break
				}
			}
			return dsn
		}
	}
	return dsn
}
