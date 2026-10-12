// Package exit_rules — target_view_admin.go (B376, 2026-10-11).
//
// The admin-side by-target view: the same "one row per destination, devices as
// tags in the row" projection the operator asked for on /my/exit-rules, adapted
// to the cross-user page.
//
// Why a separate function rather than a generic one: cdn_group_admin.go already
// made that choice for the CDN grouping (its header explains it — the per-call
// generic boilerplate is bigger than the two thin wrappers), and the admin page
// needs something the /my projection does not carry: the OWNER. Two users can
// hold a rule for the same destination on devices with similar names, and on the
// admin page a device tag that does not say whose device it is is worse than no
// tag at all.
//
// The grouping itself is NOT duplicated: this file projects AdminRule into the
// RuleRow shape, calls the single GroupRulesByTarget (target_view.go) for the
// grouping and the sorting, then re-attaches the richer AdminRule rows by their
// natural key. That way the key rule — a CDN rule groups by the domain the
// operator typed, accept and deny never merge, the action is part of the key —
// has exactly ONE implementation for both pages.
//
// Deliberately NOT here: a per-row delete control. The admin TREE's delete button
// posts to /admin/exit-rules/delete, which no route registers (measured
// 2026-10-11: grep over the whole tree finds the form in the template and no
// mux.Handle for it), so it answers 404. Reproducing that control in a second
// place would spread the defect; the new view is an overview and the tree keeps
// whatever deletion story it has until that route exists.
package exit_rules

import "sort"

// TargetRowAdmin mirrors TargetRow for /admin/exit-rules: same fields, plus the
// richer AdminRule rows (the annotation fields B178/B182/B184 fill) and the
// owner in the device tags.
type TargetRowAdmin struct {
	Key         string
	TargetType  string
	TargetValue string
	Source      string
	CDN         string
	IsCDNGroup  bool
	Action      string
	Count       int

	// Rules are the underlying device_rules rows, sorted by (TargetValue, ID).
	Rules []AdminRule

	// Devices are the per-(device, exit node) tags. DeviceName carries
	// "<user>/<device>" so a cross-user row cannot be misread.
	Devices []TargetDeviceTag
}

// GroupAdminRulesByTarget projects the admin rule slice into the by-target view.
// Pure: no DB, no Service.
func GroupAdminRulesByTarget(rules []AdminRule) []TargetRowAdmin {
	if len(rules) == 0 {
		return []TargetRowAdmin{}
	}

	projected := make([]RuleRow, len(rules))
	label := map[int]string{}
	for i, r := range rules {
		projected[i] = RuleRow{
			ID:           int64(r.ID),
			UserID:       int64(r.UserID),
			DeviceID:     r.DeviceID,
			ExitNode:     r.ExitNode,
			TargetType:   r.TargetType,
			TargetValue:  r.TargetValue,
			ParentDomain: r.ParentDomain,
			Action:       r.Action,
			AllDevices:   r.AllDevices,
		}
		// The owner belongs in the tag: device ids are unique across the
		// tailnet, but the NAME is what the operator reads, and two users
		// routinely have a "workpc".
		lab := r.DeviceName
		switch {
		case r.UserName != "" && lab != "":
			lab = r.UserName + "/" + lab
		case r.UserName != "":
			lab = r.UserName
		}
		if lab != "" {
			label[r.DeviceID] = lab
		}
	}

	rows := GroupRulesByTarget(projected, func(id int) string { return label[id] })

	byKey := map[string][]AdminRule{}
	for i, r := range rules {
		key, _ := targetRowFor(projected[i])
		byKey[key] = append(byKey[key], r)
	}

	out := make([]TargetRowAdmin, 0, len(rows))
	for _, row := range rows {
		rich := byKey[row.Key]
		// GroupRulesByTarget already sorted the projected copies; sort the
		// richer ones by the same key so the expander order matches.
		sort.Slice(rich, func(a, b int) bool {
			if rich[a].TargetValue != rich[b].TargetValue {
				return rich[a].TargetValue < rich[b].TargetValue
			}
			return rich[a].ID < rich[b].ID
		})
		out = append(out, TargetRowAdmin{
			Key:         row.Key,
			TargetType:  row.TargetType,
			TargetValue: row.TargetValue,
			Source:      row.Source,
			CDN:         row.CDN,
			IsCDNGroup:  row.IsCDNGroup,
			Action:      row.Action,
			Count:       row.Count,
			Rules:       rich,
			Devices:     row.Devices,
		})
	}
	return out
}

// TargetRowAdminDeviceCount counts the DISTINCT devices across the admin rows —
// the "N destinations on M devices" summary. A device that routes two
// destinations is counted once, exactly like the /my helper.
func TargetRowAdminDeviceCount(rows []TargetRowAdmin) int {
	seen := map[int]bool{}
	for _, row := range rows {
		for _, d := range row.Devices {
			seen[d.DeviceID] = true
		}
	}
	return len(seen)
}
