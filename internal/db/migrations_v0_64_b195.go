// v1.5.0+ (B195) — cluster management tables.
//
// Phase 0 of docs/internal/cluster-management.md (D1):
// state lives in headscale metadata. We add a "cluster_*" table
// family to skygate's main DB (or to headscale's DB — TBD by where
// the deploy chooses to install these). For now we use skygate's
// own DB so the cluster state lives next to the skygate process
// that consumes it.
//
// Six tables in this migration:
//   cluster              — definition of one cluster
//   cluster_node         — one node in a cluster (with roles, state)
//   cluster_database     — one DB cluster (primary + replicas + DSN template)
//   cluster_migration    — DDL migration history (D6)
//   cluster_invite       — pending node invites (D4)
//   cluster_audit        — admin actions log
//
// Idempotent: re-running is a no-op (CREATE TABLE IF NOT EXISTS,
// CREATE INDEX IF NOT EXISTS). All tables use IF NOT EXISTS so it's
// safe to re-run.
//
// Phase 1.1 (/admin/database, read-only) only needs cluster_database;
// the others are scaffolding for Phase 1.2-1.4 (migrations, invites,
// audit) and Phase 2-3 (cluster UI, failover).

package db

import (
	"database/sql"
)

// migrateV064PG — v1.5.0+ (B195) — cluster management tables.
//
// All tables follow the project conventions:
//   * id TEXT PRIMARY KEY (string ids, not auto-increment; admin-friendly)
//   * created_at / updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
//   * JSONB for structured fields (chain, detail, etc.)
//   * All foreign keys are TEXT (not bigint) for joinability from logs
func migrateV064PG(d *sql.DB) error {
	queries := []string{
		// cluster — definition of one cluster
		`CREATE TABLE IF NOT EXISTS cluster (
			id          TEXT PRIMARY KEY,
			name        TEXT NOT NULL DEFAULT '',
			chain       JSONB NOT NULL DEFAULT '[]'::jsonb,
			created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,

		// cluster_node — one node in a cluster
		`CREATE TABLE IF NOT EXISTS cluster_node (
			id              TEXT PRIMARY KEY,
			cluster_id      TEXT NOT NULL REFERENCES cluster(id) ON DELETE CASCADE,
			hostname        TEXT NOT NULL DEFAULT '',
			tailscale_ip    TEXT,
			roles           TEXT[] NOT NULL DEFAULT '{}',
			state           TEXT NOT NULL DEFAULT 'pending',
			skygate_version TEXT NOT NULL DEFAULT '',
			joined_at       TIMESTAMPTZ,
			last_seen_at    TIMESTAMPTZ,
			created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_cluster_node_cluster ON cluster_node(cluster_id)`,
		`CREATE INDEX IF NOT EXISTS idx_cluster_node_state ON cluster_node(state) WHERE state IN ('pending', 'draining', 'failed')`,

		// cluster_database — one DB cluster, with primary + replicas
		`CREATE TABLE IF NOT EXISTS cluster_database (
			id                TEXT PRIMARY KEY,
			cluster_id        TEXT NOT NULL REFERENCES cluster(id) ON DELETE CASCADE,
			primary_node_id   TEXT REFERENCES cluster_node(id) ON DELETE SET NULL,
			replica_node_ids   TEXT[] NOT NULL DEFAULT '{}',
			dsn_template      TEXT NOT NULL DEFAULT '',
			dbname            TEXT NOT NULL DEFAULT '',
			username          TEXT NOT NULL DEFAULT '',
			sslmode           TEXT NOT NULL DEFAULT 'disable',
			current_dsn       TEXT NOT NULL DEFAULT '',
			updated_by        TEXT NOT NULL DEFAULT '',
			created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_cluster_database_cluster ON cluster_database(cluster_id)`,
		`CREATE INDEX IF NOT EXISTS idx_cluster_database_primary ON cluster_database(primary_node_id) WHERE primary_node_id IS NOT NULL`,

		// cluster_migration — DDL migration history (D6)
		`CREATE TABLE IF NOT EXISTS cluster_migration (
			id              BIGSERIAL PRIMARY KEY,
			cluster_id      TEXT NOT NULL REFERENCES cluster(id) ON DELETE CASCADE,
			database_id     TEXT NOT NULL REFERENCES cluster_database(id) ON DELETE CASCADE,
			version         TEXT NOT NULL,
			description     TEXT NOT NULL DEFAULT '',
			applied_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			applied_by_node TEXT NOT NULL DEFAULT '',
			checksum        TEXT NOT NULL DEFAULT '',
			duration_ms     INTEGER NOT NULL DEFAULT 0,
			UNIQUE (database_id, version)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_cluster_migration_db ON cluster_migration(database_id, applied_at DESC)`,

		// cluster_invite — pending node invites (D4)
		`CREATE TABLE IF NOT EXISTS cluster_invite (
			id              TEXT PRIMARY KEY,
			cluster_id      TEXT NOT NULL REFERENCES cluster(id) ON DELETE CASCADE,
			role            TEXT NOT NULL DEFAULT '',
			target_hostname TEXT NOT NULL DEFAULT '',
			issued_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			expires_at      TIMESTAMPTZ NOT NULL,
			used_at         TIMESTAMPTZ,
			used_by_node_id TEXT,
			signature       TEXT NOT NULL DEFAULT '',
			status          TEXT NOT NULL DEFAULT 'pending'
		)`,
		`CREATE INDEX IF NOT EXISTS idx_cluster_invite_status ON cluster_invite(status, expires_at)`,

		// cluster_audit — admin actions log
		`CREATE TABLE IF NOT EXISTS cluster_audit (
			id              BIGSERIAL PRIMARY KEY,
			cluster_id      TEXT NOT NULL DEFAULT '',
			actor           TEXT NOT NULL DEFAULT 'system',
			action          TEXT NOT NULL,
			target_node_id  TEXT NOT NULL DEFAULT '',
			detail          JSONB,
			result          TEXT NOT NULL DEFAULT 'ok',
			error_message   TEXT NOT NULL DEFAULT '',
			created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_cluster_audit_cluster_time ON cluster_audit(cluster_id, created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_cluster_audit_action ON cluster_audit(action, created_at DESC)`,
	}
	for _, q := range queries {
		if _, err := d.Exec(q); err != nil {
			return err
		}
	}
	return nil
}
