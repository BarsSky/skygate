// pref_hygiene.go — B279 (v1.5.46) — removes exit-node preferences that
// store a tailnet-wide CLASS tag.
//
// WHY
// ---
// Live on the native host `aro` (2026-09-22) both preference rows held
// the class tag `tag:exit-node`:
//
//	user_exit_node_prefs    user=100 tag:exit-node via=0
//	device_exit_node_prefs  workpc   tag:exit-node via=1
//
// A class tag names a ROLE shared by every relay, never one node, so
// every consumer that derives something per-node from it invents data.
// The exit_rules copy stripped "tag:exit-" and produced the hostname
// "node"; the operator's "Use preferred" click then wrote that phantom
// into 23 device_rules rows (`my_exit_rules_apply_preferred
// preferred=node updated=21` in audit_log), the ACL pinned those
// prefixes to a relay that does not exist, and the route sync had
// nothing to advertise — the whole feature looked broken while the only
// real relay, `exit-node-vps`, sat idle.
//
// The correct expression of "any exit-node" is NOT a stored value: it
// is the absence of a preference. PlanDevicePrefChange now emits a
// "clear" change for the per-device rows; this pass covers the
// user-level rows (and any per-device row whose device has no rules, so
// the reconciler's per-device loop never visits it).
//
// Mode: inherits SKYGATE_PREFERRED_RECONCILER_LIVE (dry-run logs only).
//
// 2026-09-22: B279.
package exit_rules

import (
	"context"
	"log"
	"strconv"

	"skygate/internal/db"
)

// ClearClassTagPrefs drops every exit-node preference whose tag is a
// class tag (tag:exit-node / tag:public / tag:private /
// tag:subnet-router). Returns the number of rows removed (or, in
// dry-run mode, the number that WOULD be removed).
//
// 2026-09-22: B279.
func (s *Service) ClearClassTagPrefs(ctx context.Context) (int, error) {
	live := PreferredExitReconcilerLive()
	cleared := 0

	// 1) Per-user preferences.
	rows, err := s.dbc().QueryContext(ctx, `
		SELECT user_id, exit_node_tag FROM user_exit_node_prefs
	`)
	if err != nil {
		return 0, err
	}
	type userPref struct {
		userID int64
		tag    string
	}
	var userPrefs []userPref
	for rows.Next() {
		var p userPref
		if err := rows.Scan(&p.userID, &p.tag); err == nil && p.userID > 0 {
			userPrefs = append(userPrefs, p)
		}
	}
	rows.Close()
	for _, p := range userPrefs {
		if !db.IsClassTag(p.tag) {
			continue
		}
		if !live {
			log.Printf("preferred-reconciler: DRY-RUN would CLEAR user pref user_id=%d tag=%s (class tag, names no node)", p.userID, p.tag)
			cleared++
			continue
		}
		if err := db.SetUserExitNodePref(s.dbc(), p.userID, "", 0, false); err != nil {
			log.Printf("preferred-reconciler: CLEAR user pref user_id=%d tag=%s FAILED: %v", p.userID, p.tag, err)
			continue
		}
		_ = db.AppendAuditLogWithTarget(s.dbc(), 0, "system",
			"preferred_exit_class_tag_cleared",
			"CLEAR user pref user_id="+itoa(p.userID)+" tag="+p.tag+" reason=class-tag-not-a-node-identity",
			"portal_user", itoa(p.userID))
		log.Printf("preferred-reconciler: CLEAR user pref user_id=%d tag=%s (class tag — no node is named by it)", p.userID, p.tag)
		cleared++
	}

	// 2) Per-device preferences. PlanDevicePrefChange clears the rows
	// the reconciler visits (devices that have rules); this covers the
	// rest, so a stale class-tag row cannot sit on a device forever and
	// be resurrected the next time that device gets a rule.
	devRows, err := s.dbc().QueryContext(ctx, `
		SELECT user_id, device_hostname, exit_node_tag FROM device_exit_node_prefs
	`)
	if err != nil {
		return cleared, err
	}
	type devPref struct {
		userID   int64
		hostname string
		tag      string
	}
	var devPrefs []devPref
	for devRows.Next() {
		var p devPref
		if err := devRows.Scan(&p.userID, &p.hostname, &p.tag); err == nil && p.userID > 0 && p.hostname != "" {
			devPrefs = append(devPrefs, p)
		}
	}
	devRows.Close()
	for _, p := range devPrefs {
		if !db.IsClassTag(p.tag) {
			continue
		}
		if !live {
			log.Printf("preferred-reconciler: DRY-RUN would CLEAR device pref user_id=%d host=%s tag=%s (class tag, names no node)", p.userID, p.hostname, p.tag)
			cleared++
			continue
		}
		if err := db.DeleteDeviceExitNodePref(s.dbc(), p.userID, p.hostname); err != nil {
			log.Printf("preferred-reconciler: CLEAR device pref user_id=%d host=%s FAILED: %v", p.userID, p.hostname, err)
			continue
		}
		_ = db.AppendAuditLogWithTarget(s.dbc(), 0, "system",
			"preferred_exit_class_tag_cleared",
			"CLEAR device pref user_id="+itoa(p.userID)+" host="+p.hostname+" tag="+p.tag+" reason=class-tag-not-a-node-identity",
			"headscale_node", p.hostname)
		log.Printf("preferred-reconciler: CLEAR device pref user_id=%d host=%s tag=%s (class tag)", p.userID, p.hostname, p.tag)
		cleared++
	}
	return cleared, nil
}

// itoa keeps the audit detail a plain string at the two call sites.
func itoa(v int64) string { return strconv.FormatInt(v, 10) }
