// Package cluster — cluster.go owns the cluster table
// helpers. The cluster table is the root of the
// cluster_* tree (cluster_node / cluster_invite /
// cluster_database all FK to cluster.id). On a fresh
// install the table is empty, which means the FK
// constraint blocks every cluster_node INSERT and
// every cluster_invite INSERT.
//
// Phase 2.2 (B200) auto-creates the single "skygate-staging"
// cluster row on the first AddNode or IssueInvite call
// so the admin UI is self-sufficient (no SQL needed
// for the bootstrap).

package cluster

import (
	"database/sql"
	"errors"
)

// DefaultClusterID is the single cluster skygate ships
// with. Multi-cluster support lands in a later phase;
// for now there's exactly one cluster per skygate
// instance and the id is hard-coded here.
const DefaultClusterID = "skygate-staging"

// ErrClusterNotFound is returned by LookupCluster when
// no row matches the id.
var ErrClusterNotFound = errors.New("cluster not found")

// Cluster is the in-memory shape of one cluster row.
// The chain JSONB field is left as raw bytes here
// (the admin UI parses it as needed; we don't need to
// decode it for the Phase 2.2 actions).
type Cluster struct {
	ID        string
	Name      string
	ChainJSON []byte
}

// LookupCluster returns the cluster row with the given id.
// Returns ErrClusterNotFound if the row is missing.
func LookupCluster(d *sql.DB, id string) (*Cluster, error) {
	if id == "" {
		return nil, ErrClusterNotFound
	}
	row := d.QueryRow(`SELECT id, name, chain FROM cluster WHERE id = $1`, id)
	out := &Cluster{}
	if err := row.Scan(&out.ID, &out.Name, &out.ChainJSON); err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrClusterNotFound
		}
		return nil, err
	}
	return out, nil
}

// EnsureCluster inserts the cluster row with the given id
// if it doesn't exist yet. Idempotent — calling it twice
// is a no-op. Returns nil on success.
//
// Use this in the admin handlers' "first use" path so
// the operator never has to run SQL to create the
// bootstrap cluster. The chain JSONB defaults to '[]'
// (empty); the canonical chain view is on /admin/ha
// (the ha package owns the chain data).
func EnsureCluster(d *sql.DB, id, name string) error {
	if id == "" {
		return errors.New("cluster: empty id")
	}
	if name == "" {
		name = id
	}
	_, err := d.Exec(`
		INSERT INTO cluster (id, name, chain)
		VALUES ($1, $2, '[]'::jsonb)
		ON CONFLICT (id) DO NOTHING
	`, id, name)
	return err
}
