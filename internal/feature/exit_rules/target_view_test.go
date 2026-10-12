package exit_rules

import (
	"testing"
)

// B376: the by-target view collapses the per-CIDR rows a CDN expansion
// produced into ONE row named after the domain the operator typed, because
// that is the only name they recognise ("discordapp.com", not
// "104.16.0.0/12").
func TestGroupRulesByTarget_CDNGroupCollapsesCIDRRows(t *testing.T) {
	rules := []RuleRow{
		{ID: 1, DeviceID: 9, ExitNode: "emilia", TargetType: "subnet", TargetValue: "104.16.0.0/12", ParentDomain: "cdn:cloudflare:discordapp.com", Action: "accept"},
		{ID: 2, DeviceID: 9, ExitNode: "emilia", TargetType: "subnet", TargetValue: "104.24.0.0/14", ParentDomain: "cdn:cloudflare:discordapp.com", Action: "accept"},
		{ID: 3, DeviceID: 29, ExitNode: "emilia", TargetType: "subnet", TargetValue: "104.16.0.0/12", ParentDomain: "cdn:cloudflare:discordapp.com", Action: "accept"},
	}

	rows := GroupRulesByTarget(rules, func(id int) string {
		if id == 9 {
			return "skyworker"
		}
		return "cyborg"
	})

	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1 (the CDN expansion must collapse)", len(rows))
	}
	row := rows[0]
	if !row.IsCDNGroup {
		t.Errorf("IsCDNGroup = false, want true")
	}
	if row.TargetValue != "discordapp.com" || row.Source != "discordapp.com" {
		t.Errorf("TargetValue/Source = %q/%q, want discordapp.com", row.TargetValue, row.Source)
	}
	if row.CDN != "cloudflare" {
		t.Errorf("CDN = %q, want cloudflare", row.CDN)
	}
	if row.TargetType != "domain" {
		t.Errorf("TargetType = %q, want domain (that is what the operator typed)", row.TargetType)
	}
	if row.Count != 3 {
		t.Errorf("Count = %d, want 3", row.Count)
	}
	if len(row.Devices) != 2 {
		t.Fatalf("got %d device tags, want 2", len(row.Devices))
	}
	// skyworker carries two of the three ranges, cyborg one — the tag must
	// say so rather than repeating the device twice.
	if row.Devices[0].DeviceName != "cyborg" || row.Devices[0].RuleCount != 1 {
		t.Errorf("tag[0] = %+v, want cyborg/1", row.Devices[0])
	}
	if row.Devices[1].DeviceName != "skyworker" || row.Devices[1].RuleCount != 2 {
		t.Errorf("tag[1] = %+v, want skyworker/2", row.Devices[1])
	}
}

// B371: accept and deny for the same destination are a conflict the operator
// must SEE, so the action is part of the grouping key.
func TestGroupRulesByTarget_ActionIsPartOfTheKey(t *testing.T) {
	rules := []RuleRow{
		{ID: 1, DeviceID: 9, TargetType: "subnet", TargetValue: "10.0.0.0/8", Action: "accept"},
		{ID: 2, DeviceID: 29, TargetType: "subnet", TargetValue: "10.0.0.0/8", Action: "deny"},
	}
	rows := GroupRulesByTarget(rules, nil)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2 (accept and deny must not merge)", len(rows))
	}
	if rows[0].Action != "accept" || rows[1].Action != "deny" {
		t.Errorf("actions = %q/%q, want accept/deny (sorted by action)", rows[0].Action, rows[1].Action)
	}
}

// B371: plain (non-CDN) rules group by (type, value), not by parent_domain.
func TestGroupRulesByTarget_PlainRulesGroupByValue(t *testing.T) {
	rules := []RuleRow{
		{ID: 1, DeviceID: 9, ExitNode: "emilia", TargetType: "subnet", TargetValue: "162.254.192.0/21", Action: "accept"},
		{ID: 2, DeviceID: 29, ExitNode: "emilia", TargetType: "subnet", TargetValue: "162.254.192.0/21", Action: "accept"},
		{ID: 3, DeviceID: 9, ExitNode: "emilia", TargetType: "domain", TargetValue: "example.com", Action: "accept"},
	}
	rows := GroupRulesByTarget(rules, nil)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	// Sorted case-insensitively by display value: 162… before example.com.
	if rows[0].TargetValue != "162.254.192.0/21" || len(rows[0].Devices) != 2 {
		t.Errorf("row[0] = %+v, want the shared /21 with 2 device tags", rows[0])
	}
	if rows[1].TargetValue != "example.com" || rows[1].IsCDNGroup {
		t.Errorf("row[1] = %+v, want a plain example.com row", rows[1])
	}
}

// B371: an empty rule set is an empty list, never a nil that the template
// would have to guard.
func TestGroupRulesByTarget_Empty(t *testing.T) {
	rows := GroupRulesByTarget(nil, nil)
	if rows == nil {
		t.Fatal("got nil, want an empty non-nil slice")
	}
	if len(rows) != 0 {
		t.Errorf("got %d rows, want 0", len(rows))
	}
}

// B371: the «все мои устройства» marker survives the projection — the
// by-target row must be able to show the same badge the tree shows.
func TestGroupRulesByTarget_AllDevicesMarkerPreserved(t *testing.T) {
	rules := []RuleRow{
		{ID: 1, DeviceID: 9, TargetType: "domain", TargetValue: "example.com", Action: "accept", AllDevices: true},
		{ID: 2, DeviceID: 29, TargetType: "domain", TargetValue: "example.com", Action: "accept", AllDevices: true},
	}
	rows := GroupRulesByTarget(rules, nil)
	if len(rows) != 1 || len(rows[0].Devices) != 2 {
		t.Fatalf("got %d rows / %d tags", len(rows), len(rows[0].Devices))
	}
	for _, d := range rows[0].Devices {
		if !d.AllDevices {
			t.Errorf("tag %+v lost the all-devices marker", d)
		}
	}
}

// B371: one device pinned to two relays yields two tags (the relay is part of
// the tag identity), and the name resolver is optional.
func TestGroupRulesByTarget_TagIdentityIncludesExitNode(t *testing.T) {
	rules := []RuleRow{
		{ID: 1, DeviceID: 9, ExitNode: "emilia", TargetType: "subnet", TargetValue: "10.0.0.0/8", Action: "accept"},
		{ID: 2, DeviceID: 9, ExitNode: "karolina", TargetType: "subnet", TargetValue: "10.0.0.0/8", Action: "accept"},
	}
	rows := GroupRulesByTarget(rules, nil)
	if len(rows) != 1 || len(rows[0].Devices) != 2 {
		t.Fatalf("got %d rows / %d tags, want 1/2", len(rows), len(rows[0].Devices))
	}
	if rows[0].Devices[0].ExitNode != "emilia" || rows[0].Devices[1].ExitNode != "karolina" {
		t.Errorf("tags = %+v, want emilia then karolina (tiebreak on exit node)", rows[0].Devices)
	}
	if rows[0].Devices[0].DeviceName != "" {
		t.Errorf("DeviceName = %q, want empty with a nil resolver", rows[0].Devices[0].DeviceName)
	}
}

// B371: the summary counts DISTINCT devices, not tags — a device carrying two
// destinations is one device.
func TestTargetRowDeviceCount_Distinct(t *testing.T) {
	rows := []TargetRow{
		{Devices: []TargetDeviceTag{{DeviceID: 9}, {DeviceID: 29}}},
		{Devices: []TargetDeviceTag{{DeviceID: 9}}},
	}
	if got := TargetRowDeviceCount(rows); got != 2 {
		t.Errorf("TargetRowDeviceCount = %d, want 2", got)
	}
}

// B371: rules without an action stored (legacy rows) are shown as accept —
// the same default the ACL generator applies.
func TestGroupRulesByTarget_EmptyActionDefaultsToAccept(t *testing.T) {
	rows := GroupRulesByTarget([]RuleRow{{ID: 1, TargetType: "ip", TargetValue: "8.8.8.8"}}, nil)
	if len(rows) != 1 || rows[0].Action != "accept" {
		t.Fatalf("rows = %+v, want one accept row", rows)
	}
}
