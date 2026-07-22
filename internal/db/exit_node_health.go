// Package db — exit_node_health + exit_node_state_changes helpers.
//
// 2026-07-15: v0.13.0 — exit-node health monitor.
//
// These tables back the background monitor in
// internal/monitoring/exit_node_monitor.go. The split is:
//
//   exit_node_health          — current snapshot per node (hot path,
//                                updated on every tick, ~one row per
//                                node)
//
//   exit_node_state_changes   — append-only transition log (cold
//                                path, written only on detected
//                                changes; the dispatch loop reads
//                                rows where alerted_at = 0)
//
// Together they answer three operator questions:
//
//   1. "Which exit-nodes are healthy right now?"  → ListExitNodeHealth
//   2. "How many?"                                → CountHealthyExitNodes
//   3. "What changed since I last looked?"        → ListPendingExitNodeStateChanges
//
// The helpers below are kept intentionally small (read / write /
// query by state) so the monitor's tick() reads like the prose
// description of the operation.

package db

import (
	"database/sql"
	"time"
)

// ExitNodeHealth is the typed view of one row in exit_node_health.
// Mirrors the schema in migrations_v0.36.go. Booleans are stored as
// INTEGER (0/1) in the DB; we expose them as bool at the helper
// boundary so call sites don't have to remember which fields are
// 0/1 vs. unix timestamps.
type ExitNodeHealth struct {
	NodeID               string
	Hostname             string
	Online               bool
	LastSeen             string    // RFC3339 from headscale, may be "" if never seen
	LastSeenParsed       time.Time // parsed; zero if LastSeen == "" or unparseable
	AdvertisedRoutesOK   bool
	HasExitTag           bool
	State                string    // unknown | online | offline | degraded
	Healthy              bool
	LastCheckAt          time.Time
	LastStateChangeAt    time.Time
	ConsecutiveFailures  int
}

// ExitNodeStateChange is the typed view of one row in
// exit_node_state_changes. Used by the monitor's dispatch loop to
// find rows that have not yet triggered a Telegram alert.
type ExitNodeStateChange struct {
	ID         int64
	NodeID     string
	Hostname   string
	FromState  string
	ToState    string
	DetectedAt time.Time
	AlertedAt  time.Time // zero value means "not yet alerted"
	Note       string
}

// UpsertExitNodeHealth inserts or replaces the row for nodeID. The
// monitor calls this once per tick per node, so the path is
// deliberately thin (no SELECT, no transaction). The "did the
// state change since last tick?" comparison is the caller's job —
// it needs the previous snapshot, which the monitor reads in the
// same tick via GetExitNodeHealth before calling this.
func UpsertExitNodeHealth(d dbExec, h ExitNodeHealth) error {
	_, err := d.Exec(
		`INSERT INTO exit_node_health (
			node_id, hostname, online, last_seen, advertised_routes_ok,
			has_exit_tag, state, healthy, last_check_at,
			last_state_change_at, consecutive_failures
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (node_id) DO UPDATE SET
			hostname = EXCLUDED.hostname,
			online = EXCLUDED.online,
			last_seen = EXCLUDED.last_seen,
			advertised_routes_ok = EXCLUDED.advertised_routes_ok,
			has_exit_tag = EXCLUDED.has_exit_tag,
			state = EXCLUDED.state,
			healthy = EXCLUDED.healthy,
			last_check_at = EXCLUDED.last_check_at,
			last_state_change_at = EXCLUDED.last_state_change_at,
			consecutive_failures = EXCLUDED.consecutive_failures`,
		h.NodeID, h.Hostname, boolToInt(h.Online), h.LastSeen,
		boolToInt(h.AdvertisedRoutesOK), boolToInt(h.HasExitTag),
		h.State, boolToInt(h.Healthy),
		unixOrZero(h.LastCheckAt), unixOrZero(h.LastStateChangeAt),
		h.ConsecutiveFailures,
	)
	return err
}

// GetExitNodeHealth returns the current snapshot for nodeID, or
// (zero-value, sql.ErrNoRows) if no row exists. Callers that want
// "the empty snapshot is the natural no-previous-state" semantics
// can use a one-liner: GetExitNodeHealth → if err == ErrNoRows,
// treat as fresh.
func GetExitNodeHealth(d *sql.DB, nodeID string) (ExitNodeHealth, error) {
	row := d.QueryRow(
		`SELECT node_id, hostname, online, last_seen, advertised_routes_ok,
			has_exit_tag, state, healthy, last_check_at,
			last_state_change_at, consecutive_failures
		 FROM exit_node_health WHERE node_id = $1`,
		nodeID,
	)
	var h ExitNodeHealth
	var online, routesOK, hasTag, healthy int
	var lastCheckUnix, lastChangeUnix int64
	var lastSeen string
	err := row.Scan(&h.NodeID, &h.Hostname, &online, &lastSeen,
		&routesOK, &hasTag, &h.State, &healthy, &lastCheckUnix,
		&lastChangeUnix, &h.ConsecutiveFailures)
	if err != nil {
		return h, err
	}
	h.Online = online != 0
	h.AdvertisedRoutesOK = routesOK != 0
	h.HasExitTag = hasTag != 0
	h.Healthy = healthy != 0
	h.LastSeen = lastSeen
	if lastSeen != "" {
		// headscale returns RFC3339Nano; time.Parse handles both.
		if t, perr := time.Parse(time.RFC3339Nano, lastSeen); perr == nil {
			h.LastSeenParsed = t
		} else if t, perr := time.Parse(time.RFC3339, lastSeen); perr == nil {
			h.LastSeenParsed = t
		}
	}
	if lastCheckUnix > 0 {
		h.LastCheckAt = time.Unix(lastCheckUnix, 0).UTC()
	}
	if lastChangeUnix > 0 {
		h.LastStateChangeAt = time.Unix(lastChangeUnix, 0).UTC()
	}
	return h, nil
}

// ListExitNodeHealth returns every snapshot, ordered by hostname.
// Used by /admin/exit-nodes and /exit_nodes_health to render the
// operator-facing list. Empty slice (not nil) when no rows.
func ListExitNodeHealth(d *sql.DB) ([]ExitNodeHealth, error) {
	rows, err := d.Query(
		`SELECT node_id, hostname, online, last_seen, advertised_routes_ok,
			has_exit_tag, state, healthy, last_check_at,
			last_state_change_at, consecutive_failures
		 FROM exit_node_health ORDER BY hostname`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ExitNodeHealth{}
	for rows.Next() {
		var h ExitNodeHealth
		var online, routesOK, hasTag, healthy int
		var lastCheckUnix, lastChangeUnix int64
		var lastSeen string
		if err := rows.Scan(&h.NodeID, &h.Hostname, &online, &lastSeen,
			&routesOK, &hasTag, &h.State, &healthy, &lastCheckUnix,
			&lastChangeUnix, &h.ConsecutiveFailures); err != nil {
			return nil, err
		}
		h.Online = online != 0
		h.AdvertisedRoutesOK = routesOK != 0
		h.HasExitTag = hasTag != 0
		h.Healthy = healthy != 0
		h.LastSeen = lastSeen
		if lastSeen != "" {
			if t, perr := time.Parse(time.RFC3339Nano, lastSeen); perr == nil {
				h.LastSeenParsed = t
			} else if t, perr := time.Parse(time.RFC3339, lastSeen); perr == nil {
				h.LastSeenParsed = t
			}
		}
		if lastCheckUnix > 0 {
			h.LastCheckAt = time.Unix(lastCheckUnix, 0).UTC()
		}
		if lastChangeUnix > 0 {
			h.LastStateChangeAt = time.Unix(lastChangeUnix, 0).UTC()
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// CountHealthyExitNodes returns the number of rows in
// exit_node_health where healthy = 1. The /admin/exit-nodes banner
// uses this for "if 0, show a warning" — the cheap COUNT(*) query
// is the right shape for that.
func CountHealthyExitNodes(d *sql.DB) (int, error) {
	var n int
	err := d.QueryRow(`SELECT COUNT(*) FROM exit_node_health WHERE healthy = 1`).Scan(&n)
	return n, err
}

// RecordExitNodeStateChange inserts one row into
// exit_node_state_changes. Returns the new id. The monitor calls
// this only when the computed state differs from the previous
// snapshot's state — false positives (a transient online→offline
// blip recovered within the same tick) are filtered upstream.
//
// The new row has alerted_at = 0; the dispatch loop will update
// it to a unix timestamp once the alert has been queued.
func RecordExitNodeStateChange(d dbExec, sc ExitNodeStateChange) (int64, error) {
	res, err := d.Exec(
		`INSERT INTO exit_node_state_changes
			(node_id, hostname, from_state, to_state, detected_at, note)
			VALUES ($1, $2, $3, $4, $5, $6)`,
		sc.NodeID, sc.Hostname, sc.FromState, sc.ToState,
		unixOrZero(sc.DetectedAt), sc.Note,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListPendingExitNodeStateChanges returns the rows in
// exit_node_state_changes where alerted_at = 0, ordered by
// detected_at (oldest first). The dispatch loop processes this
// list on every tick and marks each row as alerted after the
// Telegram send is queued.
//
// Limit defaults to 32 — enough to drain a backlog from a long
// offline period (multiple nodes) without blowing up the tick
// duration if Telegram is rate-limiting.
func ListPendingExitNodeStateChanges(d *sql.DB, limit int) ([]ExitNodeStateChange, error) {
	if limit <= 0 {
		limit = 32
	}
	rows, err := d.Query(
		`SELECT id, node_id, hostname, from_state, to_state, detected_at, note
		 FROM exit_node_state_changes
		 WHERE alerted_at = 0
		 ORDER BY detected_at ASC
		 LIMIT $1`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ExitNodeStateChange{}
	for rows.Next() {
		var sc ExitNodeStateChange
		var detectedUnix int64
		if err := rows.Scan(&sc.ID, &sc.NodeID, &sc.Hostname,
			&sc.FromState, &sc.ToState, &detectedUnix, &sc.Note); err != nil {
			return nil, err
		}
		if detectedUnix > 0 {
			sc.DetectedAt = time.Unix(detectedUnix, 0).UTC()
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

// MarkExitNodeStateChangeAlerted updates alerted_at to the
// current unix time. Idempotent — re-marking is a no-op. Called
// by the dispatch loop after Notifier.SendAlert returns.
func MarkExitNodeStateChangeAlerted(d dbExec, id int64) error {
	_, err := d.Exec(
		`UPDATE exit_node_state_changes SET alerted_at = $1 WHERE id = $2 AND alerted_at = 0`,
		time.Now().Unix(), id,
	)
	return err
}

// DeleteExitNodeHealth removes the row for nodeID. Used when a
// node disappears from headscale (admin deleted it) so the
// /admin/exit-nodes list stays in sync. Idempotent.
func DeleteExitNodeHealth(d dbExec, nodeID string) error {
	_, err := d.Exec(`DELETE FROM exit_node_health WHERE node_id = $1`, nodeID)
	return err
}

// LatestExitNodeState returns the most recent transition for
// nodeID, or ("", "", sql.ErrNoRows) if no transitions have
// been recorded. The monitor uses this to dedup: if the
// previous tick already recorded an offline transition for
// this node, the current tick's "still offline" observation
// is a no-op for the alert path.
func LatestExitNodeState(d *sql.DB, nodeID string) (string, string, error) {
	var fromState, toState string
	err := d.QueryRow(
		`SELECT from_state, to_state FROM exit_node_state_changes
		 WHERE node_id = $1 ORDER BY detected_at DESC, id DESC LIMIT 1`,
		nodeID,
	).Scan(&fromState, &toState)
	return fromState, toState, err
}

// boolToInt is the 0/1 encoder for the INTEGER columns. The
// helper is local because the same pattern repeats in every
// schema we touch (see node_owner_map.go for an earlier copy);
// if a third user shows up, lift this to a shared helpers file.
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// unixOrZero returns t.Unix() when t is non-zero, else 0. Used
// to feed the INTEGER unix columns from time.Time values that
// may be zero (e.g. "never checked yet" for a fresh node). The
// 0 sentinel matters because the SELECTs treat 0 as "never",
// not "January 1 1970".
func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}
