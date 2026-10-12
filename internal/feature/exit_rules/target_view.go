// Package exit_rules — target_view.go (B376, 2026-10-11).
//
// The "by target" view: ONE ROW PER DESTINATION, with the devices that
// carry that destination rendered as tags inside the same row.
//
// Why this exists (operator report, 2026-10-11):
//
//	«не удобно отображение по группам у пользователя» — the device-first
//	tree (/my/exit-rules) nests (device → exit node → CDN group → CIDR rows),
//	so answering "which of my machines route rutracker.org, and through which
//	relay?" means opening a section per device and comparing by eye. The same
//	destination added on two devices appears twice, in two different branches,
//	with no visual link between them.
//
// The storage model does NOT change: every entry is still an independent
// device_rules row, the ACL generator, the DNS auto-updater, the prefix
// owner table (B275/B276) and SyncAdvertisedRoutes are untouched. This is a
// view-layer projection exactly like cdn_group.go (B237.22):
//
//   - no new table, no migration (the operator explicitly chose "UI/API
//     only, no schema change");
//   - no bulk delete of a group (a grouped row still renders one delete
//     button per underlying rule — deleting "the destination" is a separate
//     decision nobody asked for);
//   - no change to the per-rule status badges (StatusByRuleID still keys on
//     the individual rule id).
//
// Grouping key. Two rules land in the same row when they mean the same thing
// to the operator AND carry the same action:
//
//   - a CDN-derived rule (parent_domain = "cdn:<cdn>:<domain>") groups by the
//     ORIGINAL DOMAIN the operator typed, not by the CIDR: 15 Cloudflare
//     ranges of discordapp.com become one row named "discordapp.com";
//   - everything else groups by (target_type, target_value);
//   - accept and deny are NEVER merged — a target accepted on one device and
//     denied on another is exactly the state the operator must see, so the
//     action is part of the key.
//
// Pure by construction: no DB, no Service, no clock. Device names arrive
// through a resolver callback so the function stays testable with a
// two-line fixture (see target_view_test.go).
package exit_rules

import (
	"sort"
	"strings"
)

// TargetDeviceTag is one device that carries a rule for a target. A device
// appears exactly once per row even when the row bundles many CIDRs (a CDN
// expansion), and RuleCount says how many of them it carries — the operator
// asked to see WHICH devices, and the count is what tells "1 manual /32" from
// "15 Cloudflare ranges" without expanding anything.
type TargetDeviceTag struct {
	// DeviceID is the headscale node id the rule was written for.
	DeviceID int

	// DeviceName is the human label from the resolver callback ("" when the
	// resolver could not name the device — the template then falls back to
	// the id, never to an empty tag).
	DeviceName string

	// ExitNode is the relay hostname stored on the rule ("" = auto).
	ExitNode string

	// RuleCount is the number of underlying device_rules rows this device
	// contributes to the row.
	RuleCount int

	// AllDevices is the B276.1 «все мои устройства» marker. True when ANY of
	// the device's rows in this group carries it, so the by-target view can
	// show the same "covers devices registered later" badge the tree shows.
	AllDevices bool
}

// TargetRow is one destination with every device it is enabled on.
type TargetRow struct {
	// Key is the natural key the row was built from. Stable across renders
	// (it is derived from the rule fields, never from map iteration order)
	// so the template can use it as an anchor id.
	Key string

	// TargetType is the type shown in the row badge: "domain" for CDN
	// groups (that is what the operator typed) and the row's own
	// target_type otherwise.
	TargetType string

	// TargetValue is what the row is named after: the original domain for a
	// CDN group, the CIDR/IP/domain otherwise.
	TargetValue string

	// Source is the operator-entered domain of a CDN group ("" for a plain
	// row). Same value as TargetValue for CDN rows; kept separate so the
	// template does not have to re-derive "is this a domain I typed?".
	Source string

	// CDN is the detected CDN name of a CDN group ("cloudflare", "akamai",
	// …). Empty for a plain row.
	CDN string

	// IsCDNGroup is true when the row bundles the per-CIDR rows a domain
	// rule expanded into.
	IsCDNGroup bool

	// Action is "accept" or "deny" (empty input is normalised to "accept").
	Action string

	// Count is len(Rules) — the number of underlying device_rules rows.
	Count int

	// Rules are the underlying rows, sorted by TargetValue (stable, so the
	// expander looks the same on every render).
	Rules []RuleRow

	// Devices are the per-(device, exit node) tags, sorted by device name.
	Devices []TargetDeviceTag
}

// GroupRulesByTarget projects a flat rule slice into the by-target view.
//
// deviceName may be nil (tests, or a page that has no headscale access); the
// tag then carries an empty DeviceName and the template falls back to the id.
func GroupRulesByTarget(rules []RuleRow, deviceName func(int) string) []TargetRow {
	if len(rules) == 0 {
		return []TargetRow{}
	}

	rows := make([]TargetRow, 0, len(rules))
	rowIndex := map[string]int{}

	// tagIndex keys a tag inside a row: the same device can contribute many
	// rows (CDN expansion) and must still render as ONE tag.
	type tagKey struct {
		row  int
		dev  int
		exit string
	}
	tagIndex := map[tagKey]int{}

	for _, r := range rules {
		key, row := targetRowFor(r)
		idx, ok := rowIndex[key]
		if !ok {
			row.Key = key
			rows = append(rows, row)
			idx = len(rows) - 1
			rowIndex[key] = idx
		}
		rows[idx].Rules = append(rows[idx].Rules, r)
		rows[idx].Count = len(rows[idx].Rules)

		tk := tagKey{row: idx, dev: r.DeviceID, exit: r.ExitNode}
		if ti, ok := tagIndex[tk]; ok {
			rows[idx].Devices[ti].RuleCount++
			if r.AllDevices {
				rows[idx].Devices[ti].AllDevices = true
			}
			continue
		}
		name := ""
		if deviceName != nil {
			name = deviceName(r.DeviceID)
		}
		rows[idx].Devices = append(rows[idx].Devices, TargetDeviceTag{
			DeviceID:   r.DeviceID,
			DeviceName: name,
			ExitNode:   r.ExitNode,
			RuleCount:  1,
			AllDevices: r.AllDevices,
		})
		tagIndex[tk] = len(rows[idx].Devices) - 1
	}

	for i := range rows {
		sort.Slice(rows[i].Rules, func(a, b int) bool {
			if rows[i].Rules[a].TargetValue != rows[i].Rules[b].TargetValue {
				return rows[i].Rules[a].TargetValue < rows[i].Rules[b].TargetValue
			}
			return rows[i].Rules[a].ID < rows[i].Rules[b].ID
		})
		// Devices are sorted by label so the row reads the same on every
		// render; the exit node is the tiebreaker (one device pinned to two
		// relays produces two tags).
		sort.Slice(rows[i].Devices, func(a, b int) bool {
			an, bn := strings.ToLower(rows[i].Devices[a].DeviceName), strings.ToLower(rows[i].Devices[b].DeviceName)
			if an != bn {
				return an < bn
			}
			if rows[i].Devices[a].DeviceID != rows[i].Devices[b].DeviceID {
				return rows[i].Devices[a].DeviceID < rows[i].Devices[b].DeviceID
			}
			return rows[i].Devices[a].ExitNode < rows[i].Devices[b].ExitNode
		})
	}

	// Rows are sorted by display value (case-insensitive), then by action,
	// then by key. The value sort is what makes the destination list
	// scannable; the key tiebreak keeps the order deterministic when a
	// target exists in both accept and deny form.
	sort.SliceStable(rows, func(a, b int) bool {
		av, bv := strings.ToLower(rows[a].TargetValue), strings.ToLower(rows[b].TargetValue)
		if av != bv {
			return av < bv
		}
		if rows[a].Action != rows[b].Action {
			return rows[a].Action < rows[b].Action
		}
		return rows[a].Key < rows[b].Key
	})

	return rows
}

// targetRowFor derives the natural key and the row skeleton for one rule.
//
// The key carries the action on purpose: merging an accept and a deny into
// one row would hide the conflict the operator has to resolve.
func targetRowFor(r RuleRow) (string, TargetRow) {
	action := strings.TrimSpace(r.Action)
	if action == "" {
		action = "accept"
	}
	if cdn, source, ok := ParseCDNGroupMarker(r.ParentDomain); ok {
		return "cdn|" + r.ParentDomain + "|" + action, TargetRow{
			TargetType:  "domain",
			TargetValue: source,
			Source:      source,
			CDN:         cdn,
			IsCDNGroup:  true,
			Action:      action,
		}
	}
	return "t|" + r.TargetType + "|" + r.TargetValue + "|" + action, TargetRow{
		TargetType:  r.TargetType,
		TargetValue: r.TargetValue,
		Action:      action,
	}
}

// TargetRowDeviceCount sums the DISTINCT devices across rows — the "N rules on
// M devices" summary the /my page renders above the list. A device counted in
// two rows (it routes two destinations) is counted once.
func TargetRowDeviceCount(rows []TargetRow) int {
	seen := map[int]bool{}
	for _, row := range rows {
		for _, d := range row.Devices {
			seen[d.DeviceID] = true
		}
	}
	return len(seen)
}
