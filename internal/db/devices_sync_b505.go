package db

// devices_sync_b505.go — the `devices` table is a PROJECTION, and it was empty.
//
// WITHDRAWN WHAT WAS MEASURED (2026-11-20, reference deployment): `SELECT count(*)
// FROM devices` answered 0 while headscale carried 15 live nodes and
// `node_owner_map` carried a row for each of them. The table is created by the
// schema, has a `QSelectUserDevices` query waiting on it, and NOTHING wrote it —
// the readers that used to depend on it were moved to live headscale data long
// ago (B390's comment in internal/feature/admin/exit_node_truth_b390.go says so),
// so it quietly became an empty shell that still looks like a source of truth to
// anyone opening the database.
//
// THE FIX, and why it is a projection rather than a second source of truth:
// `node_owner_map` owns ATTRIBUTION (who owns a node, which tag it carries) and
// headscale owns LIVENESS (online, last_seen, addresses). `devices` carries
// exactly the union of those two — identity plus liveness — so it is refreshed
// from them on every node-discovery tick and never written from anywhere else.
// Nothing may treat it as authoritative: it is the cheap local read of "what the
// tailnet looked like at the last tick".
//
// Columns and where each one comes from:
//
//	user_id             portal_users.id, resolved through node_owner_map.username
//	hostname            headscale (the device's current name), node_owner_map as fallback
//	node_id             headscale node id (also copied to headscale_node_id: both
//	                    columns exist in the schema and used to disagree)
//	ip_addresses        headscale, comma-joined, IPv4 first
//	os                  node_owner_map.os — the panel's own marker (auto-detected on
//	                    the first /my/devices load, editable on /admin/devices); the
//	                    headscale API surface parsed by this client has no OS field,
//	                    so an empty value here means "the panel has not detected it yet",
//	                    never "headscale says there is none"
//	last_seen, online   headscale
//	created_at          left alone: it is this table's own bookkeeping, not the node's
//
// A live node that no portal user owns is counted as `Unattributed` and gets no
// row — inventing a user for it would be exactly the kind of guess B284/B316 exist
// to prevent.

import (
	"database/sql"
	"strconv"
	"strings"
)

// DeviceState is the per-node LIVENESS the caller read from headscale. The
// attribution part (which portal user owns it, what platform marker it has) comes
// from node_owner_map inside the sync.
type DeviceState struct {
	NodeID   string
	Hostname string
	IPs      string
	LastSeen int64
	Online   bool
}

// DeviceProjectionResult is what one pass did — every number is reported because a
// projection nobody can see the effect of is indistinguishable from one that never
// ran (the state this table was in).
type DeviceProjectionResult struct {
	Written      int // rows inserted or updated
	Removed      int // stale rows deleted
	Unattributed int // live nodes no portal user owns (correctly projected as nothing)
}

// deviceOwnerForProjection is one node_owner_map row joined to its portal user.
type deviceOwnerForProjection struct {
	NodeID   string
	Hostname string
	OS       string
	UserID   int64
}

// SyncDeviceProjection refreshes `devices` from the live node list and the
// ownership record. It is idempotent: the same input twice writes the same rows
// twice and deletes nothing.
func SyncDeviceProjection(d *sql.DB, states []DeviceState) (DeviceProjectionResult, error) {
	var res DeviceProjectionResult
	if d == nil {
		return res, nil
	}
	owners, err := loadDeviceOwnersForProjection(d)
	if err != nil {
		return res, err
	}
	existing, err := loadExistingDeviceRows(d)
	if err != nil {
		return res, err
	}
	// byNodeID / byHostname let a legacy row (written before node_id was stored)
	// be adopted instead of duplicated.
	byNodeID := map[string]deviceRowKey{}
	byHost := map[string]deviceRowKey{}
	for _, row := range existing {
		if row.NodeID != "" {
			byNodeID[row.NodeID] = row
		}
		if h := strings.ToLower(strings.TrimSpace(row.Hostname)); h != "" {
			if _, taken := byHost[h]; !taken {
				byHost[h] = row
			}
		}
	}
	// keep is the set of (user, node) pairs that are still real; everything else
	// this pass deletes.
	keep := map[string]bool{}
	written := 0
	for _, st := range states {
		owner, ok := owners[strings.TrimSpace(st.NodeID)]
		if !ok || owner.UserID <= 0 {
			res.Unattributed++
			continue
		}
		hostname := strings.TrimSpace(st.Hostname)
		if hostname == "" {
			hostname = owner.Hostname
		}
		if hostname == "" {
			// No name at all: a row keyed on another empty column would be a ghost.
			res.Unattributed++
			continue
		}
		keep[deviceProjectionKey(owner.UserID, st.NodeID)] = true
		osName := strings.TrimSpace(owner.OS)
		online := int64(0)
		if st.Online {
			online = 1
		}
		ips := strings.TrimSpace(st.IPs)

		row, found := byNodeID[st.NodeID]
		if !found {
			if legacy, ok := byHost[strings.ToLower(hostname)]; ok && legacy.NodeID == "" {
				row, found = legacy, true
			}
		}
		if found {
			if _, err := d.Exec(
				`UPDATE devices
				    SET user_id = $1, hostname = $2, node_id = $3, headscale_node_id = $3,
				        ip_addresses = $4, os = $5, last_seen = $6, online = $7
				  WHERE id = $8`,
				owner.UserID, hostname, st.NodeID, ips, osName, st.LastSeen, online, row.ID); err != nil {
				return res, err
			}
		} else {
			if _, err := d.Exec(
				`INSERT INTO devices (user_id, hostname, node_id, headscale_node_id, ip_addresses, os, last_seen, online)
				 VALUES ($1, $2, $3, $3, $4, $5, $6, $7)`,
				owner.UserID, hostname, st.NodeID, ips, osName, st.LastSeen, online); err != nil {
				return res, err
			}
		}
		keep[deviceProjectionKey(owner.UserID, st.NodeID)] = true
		written++
	}
	res.Written = written
	for _, row := range existing {
		if row.NodeID == "" {
			// A row with no node identity cannot be matched against the live list.
			// It is either pre-projection garbage or a device that was never
			// attributed; deleting it on a guess would be worse than keeping it,
			// so it is left alone and reported by the count instead.
			res.Unattributed++
			continue
		}
		if keep[deviceProjectionKey(row.UserID, row.NodeID)] {
			continue
		}
		if _, err := d.Exec(`DELETE FROM devices WHERE id = $1`, row.ID); err != nil {
			return res, err
		}
		res.Removed++
	}
	return res, nil
}

func deviceProjectionKey(userID int64, nodeID string) string {
	return strconv.FormatInt(userID, 10) + "|" + strings.TrimSpace(nodeID)
}

// deviceRowKey is the minimal projection row the sync needs to identify a row.
type deviceRowKey struct {
	ID       int64
	UserID   int64
	NodeID   string
	Hostname string
}

func loadDeviceOwnersForProjection(d *sql.DB) (map[string]deviceOwnerForProjection, error) {
	rows, err := d.Query(`SELECT nom.node_id, COALESCE(nom.hostname, ''), COALESCE(nom.os, ''), pu.id
	                        FROM node_owner_map nom
	                        JOIN portal_users pu ON LOWER(pu.username) = LOWER(nom.username)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]deviceOwnerForProjection{}
	for rows.Next() {
		var o deviceOwnerForProjection
		if err := rows.Scan(&o.NodeID, &o.Hostname, &o.OS, &o.UserID); err != nil {
			return nil, err
		}
		if id := strings.TrimSpace(o.NodeID); id != "" {
			out[id] = o
		}
	}
	return out, rows.Err()
}

func loadExistingDeviceRows(d *sql.DB) ([]deviceRowKey, error) {
	rows, err := d.Query(`SELECT id, user_id, COALESCE(node_id, ''), COALESCE(hostname, '') FROM devices`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []deviceRowKey
	for rows.Next() {
		var r deviceRowKey
		if err := rows.Scan(&r.ID, &r.UserID, &r.NodeID, &r.Hostname); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
