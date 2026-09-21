// device_rules_all_devices.go — B276.1: reading and re-materialising the
// "all my devices" intent (v0.74, device_rules.all_devices).
//
// The natural key of a rule is (user_id, device_id, exit_node_id, target_type,
// target_value) — the same key `FindDeviceRuleID` looks up and the same one the
// UNIQUE index enforces. The intent, however, belongs to (user_id, exit_node_id,
// target_type, target_value): "this rule, on every device I own". So the helpers
// here work on the group key and only ever widen a group to the user's devices.
package db

import (
	"database/sql"
	"fmt"
)

// DeviceRuleGroup is one "all my devices" rule: the natural key minus the device.
type DeviceRuleGroup struct {
	UserID       int64
	ExitNode     string
	TargetType   string
	TargetValue  string
	Action       string
	DeviceIP     string
	ParentDomain string
}

// MarkDeviceRulesAllDevices records the intent on every row of one rule group —
// the device the form targeted plus the copies the save-time fan-out created, so a
// rule saved for "all my devices" is recognisable as such later.
//
// Matching on the group key (user + exit node + type + value) rather than on the
// single row is deliberate: the fan-out has already produced the sibling rows, and
// marking only the first one would leave the rest looking like hand-made
// single-device rules — the propagation pass would then copy some of them and not
// others.
func MarkDeviceRulesAllDevices(d *sql.DB, userID int64, exitNode, targetType, targetValue string) (int64, error) {
	res, err := d.Exec(
		`UPDATE device_rules SET all_devices = 1
		  WHERE user_id = $1 AND exit_node_id = $2 AND target_type = $3 AND target_value = $4`,
		userID, exitNode, targetType, targetValue)
	if err != nil {
		return 0, fmt.Errorf("mark all_devices (user=%d %s %s): %w", userID, targetType, targetValue, err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ListAllDeviceRuleGroups returns every enabled rule group that was saved for "all
// my devices", one entry per (user, exit node, type, value).
func ListAllDeviceRuleGroups(d *sql.DB) ([]DeviceRuleGroup, error) {
	rows, err := d.Query(
		`SELECT user_id, COALESCE(exit_node_id,''), target_type, target_value, action,
		        COALESCE(device_ip,''), COALESCE(parent_domain,'')
		   FROM device_rules
		  WHERE enabled = 1 AND all_devices = 1
		  GROUP BY user_id, COALESCE(exit_node_id,''), target_type, target_value, action,
		           COALESCE(device_ip,''), COALESCE(parent_domain,'')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeviceRuleGroup
	for rows.Next() {
		var g DeviceRuleGroup
		if err := rows.Scan(&g.UserID, &g.ExitNode, &g.TargetType, &g.TargetValue, &g.Action,
			&g.DeviceIP, &g.ParentDomain); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// DeviceIDsForPortalUser returns the headscale node ids attributed to a portal
// user in node_owner_map — the same projection the tag reconciler and the
// "all my devices" fan-out use, matched on the portal username (case-insensitively,
// as nodeownership does). Devices attributed to nobody are deliberately absent: a
// rule can only be addressed to a device the ACL can name.
func DeviceIDsForPortalUser(d *sql.DB, userID int64) ([]int, error) {
	rows, err := d.Query(
		`SELECT n.node_id
		   FROM node_owner_map n
		   JOIN portal_users p ON LOWER(p.username) = LOWER(n.username)
		  WHERE p.id = $1`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var nodeID string
		if err := rows.Scan(&nodeID); err != nil {
			return nil, err
		}
		var id int
		if _, err := fmt.Sscanf(nodeID, "%d", &id); err != nil || id == 0 {
			continue
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
