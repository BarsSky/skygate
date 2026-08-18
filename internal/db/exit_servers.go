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
	"errors"
	"fmt"
	"strings"
)

// ErrExitServerNotFound is returned by SetExitServerAcceptRoutes
// (v1.4.0 B140) when the UPDATE matches 0 rows — i.e. the
// node_id was deleted concurrently, or the URL was forged. The
// handler maps this to a 404 + "exit node not found" error.
// The "not found" error pattern matches the existing
// ErrUserNotFound (used in users.go + portal_users.go).
var ErrExitServerNotFound = errors.New("exit_servers row not found")

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
	// SSHPort is the per-row non-default SSH port (added in
	// v0.33.1.33 B85). The B81 auto-fallback builds
	// "root@<tailscale_ip>:<ssh_port>" when this is non-empty
	// (the SetAdvertisedRoutes helper parses user@host:port
	// syntax and emits `ssh -p <port>`). Empty = port 22
	// (the v0.33.1.29 / v0.33.1.32 default).
	SSHPort      string
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
			&e.SSHTarget, &e.SSHKeyPath, &e.SSHPort,
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

// LookupExitServerHostname returns the hostname of the exit_servers
// row whose node_id matches, or "" if no row exists. Used by
// the telegram /defaultexitnode command to render a friendly
// "current default is X" reply without a headscale call (the
// hostname is right there in the table).
//
// 2026-07-13: Этап 11 part 2a. Returns "" (not sql.ErrNoRows) for
// the "row gone" case so the caller can use a single empty-string
// check ("if hostname != \"\"") to distinguish "row exists" from
// "row missing or empty hostname" — both of which mean "no
// friendly name to show, fall back to the node_id".
func LookupExitServerHostname(d *sql.DB, nodeID string) (string, error) {
	var h string
	err := d.QueryRow(
		`SELECT COALESCE(hostname, '') FROM exit_servers WHERE node_id = $1`,
		nodeID,
	).Scan(&h)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return h, err
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

// ExitServerSSH is the per-exit-node SSH target + key path, sourced
// from exit_servers.ssh_target / ssh_key_path. 2026-08-04 v0.33.1:
// the previous sync code used a hard-coded `/home/admin/.ssh/config`
// (the legacy /home/admin operator layout) which silently failed in
// the dockerised skygate where no /home/admin/ exists — the
// headscale approve-routes step still succeeded, so the operator
// saw "ok approved=N" while the actual tailscaled on the relay was
// never re-configured. The fix reads the per-row config from the DB
// (with a Config-level default key path as the fallback) and routes
// the SSH target through the explicit `user@host:port` value.
type ExitServerSSH struct {
	// Target is the SSH target in `user@host[:port]` form (the value
	// from exit_servers.ssh_target). Empty when the row is missing or
	// the operator hasn't customised it; callers fall back to the
	// node's headscale hostname.
	Target string
	// KeyPath is the absolute path to the private key inside the
	// skygate container (the value from exit_servers.ssh_key_path).
	// Empty when the row is missing; callers fall back to the
	// Config.SSHKeyPath / SKYGATE_EXIT_SSH_KEY default.
	KeyPath string
}

// LookupExitServerSSH returns the per-exit-node SSH target + key path.
// Both fields are returned as "" (the "unset" fallback) when no row
// matches — the caller decides the global default for the empty case.
// Errors are returned for actual DB failures; sql.ErrNoRows is folded
// to ("", "") to keep the call site a one-liner.
func LookupExitServerSSH(d *sql.DB, hostname string) (ExitServerSSH, error) {
	var out ExitServerSSH
	err := d.QueryRow(qSelectExitServerSSH, hostname).Scan(&out.Target, &out.KeyPath)
	if err == sql.ErrNoRows {
		return ExitServerSSH{}, nil
	}
	if err != nil {
		return ExitServerSSH{}, err
	}
	return out, nil
}

// LookupExitServerSSHTarget returns the SSH target that the next
// SetAdvertisedRoutes call will use for the given exit-server
// hostname, applying the v0.33.1.29 B81 fallback chain:
//
//	1. exit_servers.ssh_target (operator override — non-default port,
//	   custom user, public IP, etc.). The most common case is
//	   "root@karolina.example.com:18022" on the live VM.
//	2. "root@<tailscale_ip>" or "root@<tailscale_ip>:<ssh_port>"
//	   (B81 + B85: the auto-fallback). When the operator hasn't set
//	   an override, the helper builds "root@<tailscale_ip>" from
//	   the tailscale_ip column populated by ensureExitServers. The
//	   Tailscale IP is always reachable from the skygate host
//	   (they're in the same headscale network by definition) — no
//	   public IP, no DNS, no firewall holes required. This is
//	   the v0.33.1.29 fix for the
//	   "ssh root@<firewalled-public-ip>:22: Operation timed out"
//	   failure mode where operators had set ssh_target to a
//	   public IP that wasn't actually open on port 22.
//
//	   v0.33.1.30 B82 follow-up: the tailscale_ip column can
//	   contain a comma-joined list of headscale IP addresses
//	   (IPv4 + IPv6) — e.g. "100.64.0.3,fd7a:115c:a1e0::3". The
//	   `ssh` CLI doesn't parse a comma in the target, so the
//	   helper takes the first IP from the list (typically the
//	   IPv4 address — headscale's API returns IPv4 first). The
//	   raw tailscale_ip column is unchanged so the
//	   /admin/exit-nodes table can still show the full list
//	   for diagnostic purposes.
//
//	   v0.33.1.33 B85 follow-up: the operator may have set
//	   exit_servers.ssh_port to a non-default port (the design
//	   intent: use Tailscale, AND remember the exit-node may
//	   have sshd on 2222 / 8022 / etc., not 22). When ssh_port
//	   is set, the auto-fallback is "root@<tailscale_ip>:<port>"
//	   — the SetAdvertisedRoutes helper at
//	   internal/headscale/routes.go:222-230 already parses the
//	   "user@host:port" syntax into target + -p <port> for the
//	   ssh command, so this just slots in. Empty ssh_port =
//	   no port suffix (preserves the v0.33.1.29/v0.33.1.32
//	   behaviour for operators who don't need a non-default
//	   port).
//	3. "" (no SSH target available). The caller must surface a
//	   clear "no ssh_target, no tailscale_ip — set one in
//	   /admin/exit-nodes" error instead of falling back to
//	   nodeHostname (which doesn't resolve for typical exit-nodes
//	   and produced a "Could not resolve hostname relay-N" error
//	   in the v0.33.1 era).
//
// Errors are returned for actual DB failures; sql.ErrNoRows is folded
// to ("", nil) so a missing row gives the same "no target" answer as
// a row with all fields empty (and the caller can decide whether to
// hard-fail or carry on with the global nodeHostname fallback).
//
// The legacy fallback to nodeHostname is intentionally NOT done here —
// it belongs in the caller's SetAdvertisedRoutes function (where it
// still exists for the "no exit_servers row at all" case), not in
// this helper (which is a per-row resolver). Keeping the two
// fallback chains separate means an exit-server with a row in
// `exit_servers` and an empty ssh_target column does NOT silently
// fall through to a DNS lookup that will fail in most setups.
func LookupExitServerSSHTarget(d *sql.DB, hostname string) (string, error) {
	var sshTarget, tailscaleIP, sshPort string
	err := d.QueryRow(qSelectExitServerSSHTarget, hostname).Scan(&sshTarget, &tailscaleIP, &sshPort)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	sshTarget = strings.TrimSpace(sshTarget)
	if sshTarget != "" {
		return sshTarget, nil
	}
	tailscaleIP = strings.TrimSpace(tailscaleIP)
	if tailscaleIP == "" {
		return "", nil
	}
	// tailscale_ip is stored as a comma-joined list of
	// headscale IP addresses (IPv4 + IPv6) by
	// ensureExitServers. Take the first one — the `ssh`
	// CLI doesn't parse a comma in the target, and
	// headscale's IPAddresses array returns IPv4 first.
	// The raw column stays untouched for the
	// /admin/exit-nodes table render.
	if i := strings.Index(tailscaleIP, ","); i >= 0 {
		tailscaleIP = tailscaleIP[:i]
	}
	tailscaleIP = strings.TrimSpace(tailscaleIP)
	if tailscaleIP == "" {
		return "", nil
	}
	// v0.33.1.33 B85: append the per-row SSH port when set.
	// sshPort comes from exit_servers.ssh_port (added in
	// migrateV053). Empty = no port suffix (the `ssh` CLI
	// defaults to port 22 in that case, which is the
	// v0.33.1.29 behaviour). Non-empty = the operator has
	// chosen a non-default port for this exit-node; the
	// SetAdvertisedRoutes helper parses "user@host:port" and
	// emits `ssh -p <port> ...` (see routes.go:222-230).
	sshPort = strings.TrimSpace(sshPort)
	if sshPort != "" {
		return "root@" + tailscaleIP + ":" + sshPort, nil
	}
	return "root@" + tailscaleIP, nil
}

// UpsertExitServer inserts a new row or replaces the existing one
// for nodeID. Used by /admin/exit-nodes add/edit form. The
// INSERT ... ON CONFLICT(node_id) DO UPDATE pattern means a re-add
// of the same node_id is treated as an update (hostname, ssh_target,
// ssh_key_path, description, accept_routes are all overwritten) —
// the admin's intent is "this is the new state for this node".
//
// v0.33.1.33 B85: also takes a per-row sshPort. The B81
// auto-fallback in LookupExitServerSSHTarget reads this
// column and appends ":<port>" to "root@<tailscale_ip>"
// when set. Empty sshPort = no port suffix (port 22 default,
// the v0.33.1.29 / v0.33.1.32 behaviour).
//
// We pass tailscale_ip as empty (the form doesn't expose it; the
// discovery path sets it later). If the form ever exposes
// tailscale_ip, this helper is the one place to widen.
//
// accept_routes uses the same -1/0/1 tri-state.
func UpsertExitServer(d dbExec, nodeID, hostname, sshTarget, sshKeyPath, description, sshPort string, acceptRoutes int) error {
	_, err := d.Exec(
		qInsertOrReplaceExitServer,
		nodeID, hostname, sshTarget, sshKeyPath, description, sshPort, acceptRoutes,
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

// SetExitServerAcceptRoutes updates just the accept_routes column
// for the given nodeID. v1.4.0 B140 — the per-row accept_routes
// toggle on /admin/exit-nodes. The pre-B140 UI only let admins
// set this value at initial node add (via the form in
// PostAdminExitNodesAdd); editing after add required either a
// re-add (which clobbered every other field) or direct SQL.
//
// state uses the same -1/0/1 tri-state as the column:
//   1  = true  (node accepts routes from peers)
//   0  = unset (Tailscale decides the default)
//   -1 = false (node does NOT accept routes from peers)
//
// Returns db.ErrExitServerNotFound (or a wrapped err from
// sql.ErrNoRows via the row existence check) when the node_id
// doesn't exist. The handler uses the existence check to
// distinguish "no-op because row was deleted concurrently"
// from "real error" — the former gets a 404, the latter
// gets a 500.
func SetExitServerAcceptRoutes(d dbExec, nodeID string, state int) error {
	// Validate state at the boundary. The handler also validates
	// (via parseAcceptRoutesFormValue) so this is defense-in-depth.
	if state < -1 || state > 1 {
		return fmt.Errorf("SetExitServerAcceptRoutes: state must be -1, 0, or 1 (got %d)", state)
	}
	res, err := d.Exec(qUpdateExitServerAcceptRoutes, state, nodeID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrExitServerNotFound
	}
	return nil
}

// GetExitServerHostname returns the hostname for a given node_id
// (empty string + nil error when the row doesn't exist). Used by
// the B140 handler for the audit log message: we want to log the
// human-readable hostname, not the numeric node_id. The pre-B140
// code only updated the column without checking; the B140 audit
// message is "exit_node_set_accept_routes node=<hostname>
// state=<state>" so the audit reader can see the change at a
// glance without joining against headscale.
func GetExitServerHostname(d dbExec, nodeID string) (string, error) {
	var hostname string
	err := d.QueryRow(qSelectExitServerByNodeID, nodeID).Scan(&hostname)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return hostname, err
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
