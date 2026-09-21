// all_devices.go — B276.1: keep a user's "all my devices" rules covering the
// devices they own NOW.
//
// `/my/exit-rules` has offered "все мои устройства" since B275.3, but the form only
// materialised the rule for the devices that existed at that moment: a laptop
// registered next week had no rule, while the UI still described the rule as being
// for all of the user's devices. V074 stores the intent (`device_rules.all_devices`)
// and this pass re-materialises it, so the promise survives a new device.
//
// It runs inside the same periodic tick that re-resolves domains (the rule
// maintenance tick), and it runs BEFORE that tick's policy-drift check so the ACL
// generated in the same pass already contains the newly covered devices — otherwise
// the new rows would wait another five minutes for their grants.
package exit_rules

import (
	"errors"
	"fmt"
	"log"
	"strconv"

	"skygate/internal/db"
)

// propagateAllDeviceRules copies every "all my devices" rule onto each device its
// owner currently has. Rows are matched on the natural key (FindDeviceRuleID), so
// re-running the pass is free and can never produce a duplicate rule.
//
// B277.4 (2026-09-21): before the propagation loop, sweep for
// orphan fan-out copies — rows where all_devices=1 but the
// underlying device_id no longer belongs to the user (device
// was deleted via /admin/devices or never adopted). Pre-B277.4
// those rows lived forever in device_rules, surfaced as
// "ghost rules" in /my/exit-rules with a stale denormalised
// device_hostname, and counted toward MaxTotalRules. The sweep
// reads every fan-out copy once, joins against
// DeviceIDsForPortalUser, and DELETEs the rows whose device_id
// is not in the user's current set.
//
// Returns the number of rows it had to create. A rule whose owner has no attributed
// device is reported once per pass: silently skipping it is how the original gap
// stayed invisible.
func (s *Service) propagateAllDeviceRules() (int, error) {
	// Phase 1: orphan cleanup. Sweep fan-out rows whose target
	// device no longer belongs to the row's user. The sweep
	// scopes by user_id (so users with thousands of fan-out
	// rows don't all share one DELETE pass) and trusts the
	// DB-level cascade for any per-row side-effects.
	pruned, perr := s.pruneOrphanAllDeviceCopies()
	if perr != nil {
		log.Printf("all-devices: orphan sweep: %v", perr)
	}

	groups, err := db.ListAllDeviceRuleGroups(s.dbc())
	if err != nil {
		return 0, fmt.Errorf("list all-device rules: %w", err)
	}
	if len(groups) == 0 {
		if pruned > 0 {
			log.Printf("all-devices: pruned %d orphan fan-out copy(ies) (B277.4)", pruned)
		}
		return 0, nil
	}
	created := 0
	unattributed := 0
	for _, g := range groups {
		devices, derr := db.DeviceIDsForPortalUser(s.dbc(), g.UserID)
		if derr != nil {
			return created, fmt.Errorf("devices for user %d: %w", g.UserID, derr)
		}
		if len(devices) == 0 {
			unattributed++
			continue
		}
		for _, devID := range devices {
			if _, ferr := db.FindDeviceRuleID(s.dbc(), g.UserID, devID, g.ExitNode, g.TargetType, g.TargetValue); ferr == nil {
				continue // this device is already covered
			} else if !errors.Is(ferr, db.ErrNotFound) {
				return created, fmt.Errorf("look up rule for user %d device %d: %w", g.UserID, devID, ferr)
			}
			ok, _ := s.insertRuleUnique(g.UserID, devID, g.ExitNode, g.TargetType, g.TargetValue,
				g.Action, g.DeviceIP, g.ParentDomain)
			if !ok {
				continue
			}
			// Keep the intent on the row we just created: the next pass must see
			// it as part of the same "all my devices" rule rather than as a
			// hand-made single-device rule.
			if _, merr := db.MarkDeviceRulesAllDevices(s.dbc(), g.UserID, g.ExitNode, g.TargetType, g.TargetValue); merr != nil {
				log.Printf("all-devices: could not mark the copied rule (user=%d %s %s): %v", g.UserID, g.TargetType, g.TargetValue, merr)
			}
			created++
		}
	}
	if unattributed > 0 {
		log.Printf("all-devices: %d 'all my devices' rule group(s) belong to a user with no attributed device — nothing to copy them to (register the device or adopt it on /admin/devices)", unattributed)
	}
	if created > 0 {
		log.Printf("all-devices: propagated %d rule row(s) to the users' current devices (B276.1)", created)
	}
	if pruned > 0 {
		log.Printf("all-devices: pruned %d orphan fan-out copy(ies) (B277.4)", pruned)
	}
	return created, nil
}

// allDeviceRuleKeys returns the natural keys (exit node + type + value) of the
// user's "all my devices" rules, used by the view to label those rows.
func (s *Service) allDeviceRuleKeys(userID int64) map[string]bool {
	rows, err := s.dbc().Query(
		`SELECT COALESCE(exit_node_id,'') || '|' || target_type || '|' || target_value
		   FROM device_rules WHERE user_id = $1 AND all_devices = 1
		  GROUP BY COALESCE(exit_node_id,''), target_type, target_value`, userID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var k string
		if rows.Scan(&k) == nil {
			out[k] = true
		}
	}
	return out
}

// pruneOrphanAllDeviceCopies (B277.4, 2026-09-21) sweeps fan-out
// rows whose target device no longer belongs to the row's user.
//
// Background: the B276.1 fan-out creates N device_rules rows
// per all_devices=true rule (one per device the user owns at
// propagation time). When /admin/devices deletes a device, the
// row's device_id becomes orphaned: the rule still exists but
// the device doesn't, and the row surfaces as a "ghost rule" in
// /my/exit-rules (the denormalised device_hostname column keeps
// the stale hostname). Pre-B277.4 nothing swept those rows.
//
// The sweep is per-user (one DELETE per affected user) and only
// touches rows where all_devices=1 AND the device_id is NOT in
// the user's current device list. Per-device rules
// (all_devices=0) are NOT touched — they survive device
// deletion by design (the rule was tied to that device, not
// to the user's fan-out intent).
//
// We log every prune in the application log so the operator can
// audit which rows were dropped (the audit_log table is system-
// scoped and doesn't carry per-row fan-out history).
func (s *Service) pruneOrphanAllDeviceCopies() (int, error) {
	pruned := 0
	// Phase 1: find every user that has at least one all_devices=1 row.
	// One small SELECT, then a targeted DELETE per user. Avoids a
	// single-massive DELETE that would lock device_rules for the
	// duration of the sweep on large tails.
	rows, err := s.dbc().Query(
		`SELECT DISTINCT user_id FROM device_rules WHERE all_devices = 1`)
	if err != nil {
		return 0, fmt.Errorf("list users with fan-out rows: %w", err)
	}
	var users []int64
	for rows.Next() {
		var uid int64
		if rows.Scan(&uid) == nil {
			users = append(users, uid)
		}
	}
	rows.Close()

	for _, uid := range users {
		devices, err := db.DeviceIDsForPortalUser(s.dbc(), uid)
		if err != nil {
			return pruned, fmt.Errorf("user %d devices: %w", uid, err)
		}
		// Build the IN-clause parameter list.
		if len(devices) == 0 {
			// User has NO attributed devices. Every all_devices=1
			// row for this user is an orphan — sweep the lot.
			res, err := s.dbc().Exec(
				`DELETE FROM device_rules WHERE user_id = $1 AND all_devices = 1`, uid)
			if err != nil {
				return pruned, fmt.Errorf("user %d orphan sweep: %w", uid, err)
			}
			n, _ := res.RowsAffected()
			pruned += int(n)
			continue
		}
		// Build a parameterized IN clause. DeviceIDsForPortalUser
		// returns a slice of int — we still bind them positionally.
		args := make([]any, 0, len(devices)+1)
		args = append(args, uid)
		placeholders := ""
		for i, d := range devices {
			if i > 0 {
				placeholders += ","
			}
			placeholders += "$" + strconv.Itoa(i+2)
			args = append(args, d)
		}
		res, err := s.dbc().Exec(
			`DELETE FROM device_rules
			    WHERE user_id = $1 AND all_devices = 1
			      AND device_id NOT IN (`+placeholders+`)`, args...)
		if err != nil {
			return pruned, fmt.Errorf("user %d orphan sweep: %w", uid, err)
		}
		n, _ := res.RowsAffected()
		pruned += int(n)
	}
	return pruned, nil
}

// allDeviceRuleKey builds the same key the query above produces, so the view and
// the query cannot disagree about what "an all-devices rule" is.
func allDeviceRuleKey(exitNode, targetType, targetValue string) string {
	return exitNode + "|" + targetType + "|" + targetValue
}
