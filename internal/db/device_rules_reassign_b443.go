// device_rules_reassign_b443.go — B443 (2026-10-29) — A RULE BELONGS TO THE DEVICE'S
// OWNER, NOT TO WHOEVER WROTE IT FIRST.
//
// /admin/devices Transfer (and the adoption of an ownerless node) rewrote
// `node_owner_map` only. Two consequences, both cross-user:
//
//   - the PREVIOUS owner kept every rule of a device he no longer controls, so he could
//     still decide where that device's traffic goes — and the new owner saw a device with
//     no rules while it was demonstrably routed,
//   - a transferred rule that carried the `all_devices` marker would be re-materialised by
//     propagateAllDeviceRules onto the NEW owner's devices, and a `group_label` would show
//     the previous owner's taxonomy in the new owner's page.
//
// The rules therefore MOVE with the device, and the two per-owner markers are cleared:
// "all MY devices" is a statement about the previous owner's device set, not about the
// device that changed hands.
package db

import "database/sql"

// DeviceRuleReassignResult reports what a transfer did to the rules, so the caller can
// SAY it instead of silently rewriting somebody else's configuration.
type DeviceRuleReassignResult struct {
	// Moved is the number of rule rows whose owner changed.
	Moved int64
	// GroupsCleared is how many of the moved rows had a group label (the previous
	// owner's own naming) — reported separately because losing it is a visible change.
	GroupsCleared int64
	// AllDevicesCleared is how many of the moved rows carried the all-devices marker.
	AllDevicesCleared int64
}

// ReassignDeviceRules hands every rule of `deviceID` to the new owner. It is idempotent:
// rows already owned by the target are untouched, so a repeated transfer moves nothing.
//
// user_name is written too — it is the denormalised owner the admin views, the ACL
// source resolution and the B351 backfill read, so leaving it stale would keep showing the
// previous owner in the very pages that must show the new one.
func ReassignDeviceRules(d *sql.DB, deviceID int64, newUserID int64, newUsername string) (DeviceRuleReassignResult, error) {
	var out DeviceRuleReassignResult
	if d == nil || deviceID <= 0 || newUserID <= 0 {
		return out, nil
	}
	// Count the markers BEFORE the UPDATE: the same rows are about to change.
	_ = d.QueryRow(`SELECT
	        COALESCE(SUM(CASE WHEN COALESCE(group_label,'') <> '' THEN 1 ELSE 0 END), 0),
	        COALESCE(SUM(CASE WHEN all_devices = 1 THEN 1 ELSE 0 END), 0)
	      FROM device_rules
	     WHERE device_id = $1 AND user_id <> $2`,
		deviceID, newUserID).Scan(&out.GroupsCleared, &out.AllDevicesCleared)

	res, err := d.Exec(`UPDATE device_rules
	                       SET user_id = $1,
	                           user_name = $2,
	                           all_devices = 0,
	                           group_label = ''
	                     WHERE device_id = $3
	                       AND (user_id <> $1 OR COALESCE(user_name,'') <> $2)`,
		newUserID, newUsername, deviceID)
	if err != nil {
		return DeviceRuleReassignResult{}, err
	}
	if n, aerr := res.RowsAffected(); aerr == nil {
		out.Moved = n
	}
	return out, nil
}
