// Package db — exit_servers helpers.
//
// Этап 10 part 5 (2026-07-12). The exit_servers table holds the
// admin-curated list of nodes that skygate treats as exit-nodes —
// distinct from "nodes that the autoupdater discovered via
// headscale". Before this file, the 6 raw SQL strings were duplicated
// across internal/handlers/admin_exit_nodes.go (4 strings),
// internal/handlers/exit_rules_sync.go (1), and
// internal/handlers/exit_rules.go (1) — plus a dead constant
// (qSelectEnabledExitServers) in queries.go that referenced a
// non-existent `name` column.
//
// The helpers are split by intent (read / write / discover) so each
// call site reads like a description of the operation rather than a
// raw SQL string. The shape matches the production migration
// (migrations.go migrateV020 + v0.24 + v0.26):
//
//   id            INTEGER PRIMARY KEY AUTOINCREMENT
//   node_id       TEXT NOT NULL UNIQUE
//   hostname      TEXT NOT NULL
//   tailscale_ip  TEXT NOT NULL DEFAULT ''
//   ssh_target    TEXT NOT NULL DEFAULT ''     v0.24
//   ssh_key_path  TEXT NOT NULL DEFAULT ''     v0.24
//   description   TEXT DEFAULT ''              (nullable in old schemas)
//   enabled       INTEGER NOT NULL DEFAULT 1
//   accept_routes INTEGER NOT NULL DEFAULT 0   v0.26
//   created_at    INTEGER DEFAULT (strftime('%s','now'))
//
// BUG FIX in passing: the inline `SELECT name FROM exit_servers WHERE
// enabled=1` query in exit_rules.go:319 referenced a column that
// never existed in any migration (the table has `hostname`). The
// result was discarded (`serverRows, _ := a.DB.Query(...)`) so the
// dashboard silently missed every real exit server's hostname. The
// new ListEnabledExitServerHostnames helper queries the right column
// and is wired into the same call site.
//
// Write helpers accept a small dbExec interface so callers can pass
// either *sql.DB or *sql.Tx. The current call sites all use *sql.DB
// (the writes happen on user-driven form posts, not in a tx), but
// keeping the door open matches the pattern set by node_owner_map.go.

package db

import (
	"database/sql"
)

// ExitServer is the typed view of one row in exit_servers. It is
// the in-memory shape used by both the admin /admin/exit-nodes page
// (where it gets enriched with routes / online state from headscale)
// and the dashboard's per-exit-node load panel.
//
// AcceptRoutes uses the tri-state encoding documented in
// migrations_v0.26.go: -1 = false, 0 = unset, 1 = true. The DB column
// is INTEGER; we expose it as int (not bool) so the unset case is
// preserved through the helper boundary.
type ExitServer struct {
	ID           int64
	NodeID       string
	Hostname     string
	TailscaleIP  string
	SSHTarget    string
	SSHKeyPath   string
	Description  string
	Enabled      bool
	AcceptRoutes int
}

// ListExitServers returns every row in exit_servers, ordered by
// hostname. The /admin/exit-nodes page is the only caller; it then
// enriches the rows with headscale's view (routes, online) by
// matching on node_id. The query uses COALESCE on description so
// legacy rows with NULL description don't break the Scan.
func ListExitServers(d *sql.DB) ([]ExitServer, error) {
	rows, err := d.Query(qSelectAllExitServers)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ExitServer{}
	for rows.Next() {
		var e ExitServer
		var enabled int
		if err := rows.Scan(
			&e.ID, &e.NodeID, &e.Hostname, &e.TailscaleIP,
			&e.SSHTarget, &e.SSHKeyPath,
			&enabled, &e.Description, &e.AcceptRoutes,
		); err != nil {
			return nil, err
		}
		e.Enabled = enabled != 0
		out = append(out, e)
	}
	return out, rows.Err()
}

// ListEnabledExitServerHostnames returns the hostnames of every
// enabled exit server. Used by the dashboard's per-exit-node load
// panel to add known exit-server names to the set of node names
// already pulled from device_rules.
//
// Replaces the inline `SELECT name FROM exit_servers WHERE enabled=1`
// query at exit_rules.go:319. That query referenced a `name` column
// that has never existed in any migration (the table has `hostname`)
// — the result was being silently dropped, so the dashboard never
// showed admin-curated exit-nodes that had no device_rules. After
// this refactor the dashboard sees the full set.
//
// Empty slice (not nil) when no rows match.
func ListEnabledExitServerHostnames(d *sql.DB) ([]string, error) {
	rows, err := d.Query(
		`SELECT hostname FROM exit_servers WHERE enabled = 1 ORDER BY hostname`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// LookupExitServerAcceptRoutes returns the AcceptRoutes preference
// stored on the exit_servers row whose hostname matches. The value
// uses the tri-state encoding (-1 / 0 / 1) documented in
// migrations_v0.26.go and exit_rules_sync.go's lookupAcceptRoutes.
//
// Returns (0, nil) when no row matches — the "unset" case is the
// safe default for SSH-driven `tailscale set --accept-routes` so
// returning 0 is both the natural fallback and matches the
// pre-refactor behaviour of lookupAcceptRoutes. Callers can ignore
// the error and treat it as "not configured".
func LookupExitServerAcceptRoutes(d *sql.DB, hostname string) (int, error) {
	var accept int
	err := d.QueryRow(qSelectAcceptRoutesByHost, hostname).Scan(&accept)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return accept, nil
}

// UpsertExitServer inserts a new row or replaces the existing one
// for nodeID. Used by /admin/exit-nodes add/edit form. The
// INSERT ... ON CONFLICT(node_id) DO UPDATE pattern means a re-add
// of the same node_id is treated as an update (hostname, ssh_target,
// ssh_key_path, description, accept_routes are all overwritten) —
// the admin's intent is "this is the new state for this node".
//
// We pass tailscale_ip as empty (the form doesn't expose it; the
// discovery path sets it later). If the form ever exposes
// tailscale_ip, this helper is the one place to widen.
//
// accept_routes uses the same -1/0/1 tri-state.
func UpsertExitServer(d dbExec, nodeID, hostname, sshTarget, sshKeyPath, description string, acceptRoutes int) error {
	_, err := d.Exec(
		qInsertOrReplaceExitServer,
		nodeID, hostname, sshTarget, sshKeyPath, description, acceptRoutes,
	)
	return err
}

// DeleteExitServerByNodeID removes the row whose node_id matches.
// Used by /admin/exit-nodes delete. Idempotent — deleting a
// non-existent row is not an error in SQLite.
func DeleteExitServerByNodeID(d dbExec, nodeID string) error {
	_, err := d.Exec(qDeleteExitServerByNodeID, nodeID)
	return err
}

// InsertIgnoreExitServerOnDiscovery inserts a new row if and only
// if no row for nodeID exists yet. Used by ensureExitServers() at
// the top of AdminExitNodes: every headscale node that either has
// the tag:exit-node tag OR advertises any route becomes a candidate,
// and we want the row to appear — but if an admin has already
// manually added (and possibly disabled!) the same node, INSERT OR
// IGNORE respects the existing row (preserves admin intent and
// admin-set enabled flag).
//
// The helper takes tailscale_ip as a single string. The caller
// (ensureExitServers) joins headscale's []IPAddresses with comma
// to keep the storage format consistent with the v0.20 schema
// (TEXT, comma-joined). The discovery path doesn't set ssh_target,
// ssh_key_path, description, or accept_routes — those are
// admin-curated and stay default ('' / 0).
func InsertIgnoreExitServerOnDiscovery(d dbExec, nodeID, hostname, tailscaleIP string) error {
	_, err := d.Exec(qInsertExitServerOnDiscovery, nodeID, hostname, tailscaleIP)
	return err
}
