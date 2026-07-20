// exit_node_choice.go — preferred exit-node per user_subnet.
//
// v0.19.0 closes the v0.16.0+ subnets roadmap's last
// big feature: each portal user with a personal subnet
// can pick a "preferred exit-node" — Skygate publishes
// a special DNS record
//
//   exitnode.skygate-subnet-<username>.<base-domain>
//
// pointing to that exit-node's Tailscale IP. The user's
// tailnet clients can then use the FQDN as their
// default route without remembering the exit-node's
// tailnet IP.
//
// The choice is persisted on user_subnets
// (preferred_exit_node_id, headscale node ID). Empty
// = no preference; the ACL builder skips the user
// when this is empty.
//
// Why on user_subnets and not on portal_users? Because
// the per-user-subnet route is per-subnet, and a user
// can have at most one subnet today (UNIQUE(user_id)
// on user_subnets). If we ever support multiple subnets
// per user, this column stays on user_subnets and each
// subnet gets its own preferred exit-node.
//
// The ACL builder reads this column to populate
// `dns.extra_records` in the headscale policy. Each
// record is an A record (or AAAA for IPv6) pointing
// to the chosen exit-node's Tailscale IP. Skygate
// re-pushes the ACL whenever the user changes their
// choice (admin UI button or bot /mysubnet exit-node).
package subnet

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrInvalidExitNodeID is returned by SetPreferredExitNode
// when the operator passes a node_id that doesn't
// correspond to any headscale node. We can't validate
// against headscale here (we don't have a *headscale.Client
// in this package), so the caller is expected to verify
// the node_id is real before calling. The ACL builder
// also handles "no such node" gracefully (skips the
// record, logs a warning).
//
// This error is a safety net for direct DB calls; the
// admin handler and bot path both validate first.
var ErrInvalidExitNodeID = errors.New("subnet: invalid exit node id")

// GetPreferredExitNode returns the headscale node ID
// (e.g. "11" for karolina) the user picked, or "" if
// no preference is set. Returns sql.ErrNoRows if the
// user has no subnet (caller decides whether that's
// an error or just "no choice yet").
func GetPreferredExitNode(d *sql.DB, userID int64) (string, error) {
	var nodeID string
	err := d.QueryRow(`
		SELECT preferred_exit_node_id
		  FROM user_subnets
		 WHERE user_id = ?
	`, userID).Scan(&nodeID)
	if err != nil {
		return "", err
	}
	return nodeID, nil
}

// SetPreferredExitNode records the user's choice and
// updates the updated_at timestamp. Returns
// ErrNotFound if the user has no subnet (you can't
// pick an exit-node for a subnet you don't have).
//
// The caller is responsible for re-pushing the ACL
// after this call (so the new DNS record takes
// effect). The admin handler and bot do this via
// acl.ApplyACLPipelineForPlane, same as the v0.17.1
// share/allocate pattern.
func SetPreferredExitNode(d *sql.DB, userID int64, nodeID string) error {
	if nodeID == "" {
		return fmt.Errorf("subnet: node_id required (use ClearPreferredExitNode to unset)")
	}
	now := time.Now().Unix()
	res, err := d.Exec(`
		UPDATE user_subnets
		   SET preferred_exit_node_id = ?,
		       updated_at = ?
		 WHERE user_id = ?
	`, nodeID, now, userID)
	if err != nil {
		return fmt.Errorf("subnet: set preferred exit node: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("subnet: set preferred exit node: rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ClearPreferredExitNode resets the user's choice.
// After this, the ACL builder skips the user (no
// `exitnode.skygate-subnet-<user>` DNS record is
// published). Returns ErrNotFound if the user has
// no subnet.
func ClearPreferredExitNode(d *sql.DB, userID int64) error {
	now := time.Now().Unix()
	res, err := d.Exec(`
		UPDATE user_subnets
		   SET preferred_exit_node_id = '',
		       updated_at = ?
		 WHERE user_id = ?
	`, now, userID)
	if err != nil {
		return fmt.Errorf("subnet: clear preferred exit node: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("subnet: clear preferred exit node: rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListUsersWithPreferredExitNode returns the (userID,
// preferred_exit_node_id) pairs for every user_subnets
// row with a non-empty choice. The ACL builder uses
// this to populate `dns.extra_records` in the headscale
// policy — one record per user.
//
// Order is by user_id (deterministic) so the ACL JSON
// output is stable across rebuilds. The headscale API
// treats policy JSON as a black box (it re-validates
// on every push), but a stable order makes diffs in
// `acl_snapshots.config` easier to read.
//
// Used by internal/acl/acl.go's GenerateACLForPlane.
func ListUsersWithPreferredExitNode(d *sql.DB) ([]PreferredExitNodeChoice, error) {
	rows, err := d.Query(`
		SELECT user_id, preferred_exit_node_id
		  FROM user_subnets
		 WHERE preferred_exit_node_id != ''
		 ORDER BY user_id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("subnet: list preferred exit nodes: %w", err)
	}
	defer rows.Close()
	var out []PreferredExitNodeChoice
	for rows.Next() {
		var c PreferredExitNodeChoice
		if err := rows.Scan(&c.UserID, &c.NodeID); err != nil {
			return nil, fmt.Errorf("subnet: list preferred exit nodes scan: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// PreferredExitNodeChoice is a (userID, headscale-node-ID)
// pair. The ACL builder looks up the node's Tailscale
// IP via the headscale API and adds a DNS extra_record.
//
// Stored as a struct (not a tuple) so the ACL builder
// can extend the shape later (e.g. "username" for
// building the FQDN, "plane_url" for per-plane
// routing) without changing every caller.
type PreferredExitNodeChoice struct {
	UserID int64
	NodeID string
}
