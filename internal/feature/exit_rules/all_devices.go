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

	"skygate/internal/db"
)

// propagateAllDeviceRules copies every "all my devices" rule onto each device its
// owner currently has. Rows are matched on the natural key (FindDeviceRuleID), so
// re-running the pass is free and can never produce a duplicate rule.
//
// Returns the number of rows it had to create. A rule whose owner has no attributed
// device is reported once per pass: silently skipping it is how the original gap
// stayed invisible.
func (s *Service) propagateAllDeviceRules() (int, error) {
	groups, err := db.ListAllDeviceRuleGroups(s.dbc())
	if err != nil {
		return 0, fmt.Errorf("list all-device rules: %w", err)
	}
	if len(groups) == 0 {
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

// allDeviceRuleKey builds the same key the query above produces, so the view and
// the query cannot disagree about what "an all-devices rule" is.
func allDeviceRuleKey(exitNode, targetType, targetValue string) string {
	return exitNode + "|" + targetType + "|" + targetValue
}
