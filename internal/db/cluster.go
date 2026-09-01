// Package db — cluster.go owns the cluster_database CRUD helpers
// for the cluster-management feature (B195, see
// docs/internal/cluster-management.md).
//
// Phase 1.1 only needs GetClusterDatabase (read). Phase 1.2
// (edit form) will add SetClusterDatabase. Phase 1.4 (migration
// workflow) will add the migration state machine.

package db

import (
	"database/sql"
	"errors"
	"time"
)

// ErrClusterDatabaseNotFound is returned when GetClusterDatabase
// is called with an id that doesn't exist (vs. an actual DB error).
// The admin UI checks for this to render "not configured" vs.
// an error badge.
var ErrClusterDatabaseNotFound = errors.New("cluster_database not found")

// ClusterDatabase is the in-memory shape of one row in
// cluster_database. The migration lives in
// migrations_v0_64_b195.go.
//
// Note: only the fields Phase 1.1 needs are here. Phase 1.2
// (Set) and Phase 2 (cluster UI) will extend.
type ClusterDatabase struct {
	ID              string
	ClusterID       string
	PrimaryNodeID   string
	ReplicaNodeIDs  []string
	DSNTemplate     string
	DBName          string
	Username        string
	SSLMode         string
	CurrentDSN      string
	UpdatedBy       string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// GetClusterDatabase returns the cluster_database row with the
// given id, or ErrClusterDatabaseNotFound if it doesn't exist.
// Other errors are returned as-is (DB connection, etc.).
//
// sql.ErrNoRows is normalized to ErrClusterDatabaseNotFound so
// the admin page can render "not configured" without checking
// for the specific sentinel.
func GetClusterDatabase(d *sql.DB, id string) (*ClusterDatabase, error) {
	row := d.QueryRow(`
		SELECT id, cluster_id, primary_node_id, replica_node_ids,
		       dsn_template, dbname, username, sslmode, current_dsn,
		       updated_by, created_at, updated_at
		FROM cluster_database
		WHERE id = $1
	`, id)
	out := &ClusterDatabase{}
	err := row.Scan(
		&out.ID, &out.ClusterID, &out.PrimaryNodeID, &out.ReplicaNodeIDs,
		&out.DSNTemplate, &out.DBName, &out.Username, &out.SSLMode, &out.CurrentDSN,
		&out.UpdatedBy, &out.CreatedAt, &out.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, ErrClusterDatabaseNotFound
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SetClusterDatabase is the upsert (INSERT ... ON CONFLICT) that
// the Phase 1.2 edit form will call. It is included here so the
// schema is covered, but the admin handler is not wired yet.
//
// Phase 1.2 will wire it to /admin/database/edit.
func SetClusterDatabase(d *sql.DB, cd *ClusterDatabase) error {
	_, err := d.Exec(`
		INSERT INTO cluster_database (
			id, cluster_id, primary_node_id, replica_node_ids,
			dsn_template, dbname, username, sslmode, current_dsn,
			updated_by, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NOW(), NOW())
		ON CONFLICT (id) DO UPDATE SET
			cluster_id = EXCLUDED.cluster_id,
			primary_node_id = EXCLUDED.primary_node_id,
			replica_node_ids = EXCLUDED.replica_node_ids,
			dsn_template = EXCLUDED.dsn_template,
			dbname = EXCLUDED.dbname,
			username = EXCLUDED.username,
			sslmode = EXCLUDED.sslmode,
			current_dsn = EXCLUDED.current_dsn,
			updated_by = EXCLUDED.updated_by,
			updated_at = NOW()
	`, cd.ID, cd.ClusterID, cd.PrimaryNodeID, cd.ReplicaNodeIDs,
		cd.DSNTemplate, cd.DBName, cd.Username, cd.SSLMode, cd.CurrentDSN,
		cd.UpdatedBy)
	return err
}
